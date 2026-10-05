package media

import (
	"context"
	"errors"
	"fmt"
	"image"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"

	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/video"
)

// VideoEngine 一次通话的视频媒体引擎：与音频引擎**平级的独立 RTP/RTCP 端口对**。
//
// 为什么不复用 Engine：时钟率不同（90000 vs 8000）、接收粒度不同
// （访问单元 vs 20ms 采样帧）、抖动策略不同（音频填静音、视频只能整帧丢弃
// 并等下一个关键帧）、生命周期也不同（对端可以单独拒绝视频）。二者只共享
// bindRTPRTCP 与 rtcpSession。
//
// 并发模型（每通通话最多 3 个 goroutine）：
//
//	recvLoop    读 UDP → 解包重组访问单元 → decodeCh
//	decodeLoop  取访问单元 → 解码 → 转 RGBA → 帧回调
//	captureLoop 摄像头取帧 → 编码 → 打包发送 →（本地回显帧回调）
//
// 锁纪律：任何后端调用（摄像头开关、编解码器创建）都**不得持锁**——
// 与 audio.Manager 的教训一致（miniaudio 曾因持锁调后端把 UI 线程卡死 15 秒）。
type VideoEngine struct {
	conn      *net.UDPConn
	rtcp      *rtcpSession
	localIP   string
	sdpIP     string
	localPort int
	remoteMu  sync.RWMutex
	remote    *net.UDPAddr

	// 协商结果
	codecMu    sync.RWMutex
	codec      VideoCodec
	peerPT     uint8  // 对端 offer/answer 中的负载类型号（线上用）
	answerFMTP string // answer 用的 fmtp（packetization-mode 已按对端 offer 取值）

	pay videoPacketizer
	dep videoDepacketizer

	// 发送侧
	sndMu    sync.Mutex
	ssrc     uint32
	seq      uint16
	ts       uint32
	lastTS   time.Time
	forceKey atomic.Bool // 下一帧强制关键帧（收到 PLI/FIR 时置位）

	// 开关（原子，采集/发送/解码三处读取）
	enabled     atomic.Bool // 需求 8：关闭视频（不显示远端、不推本地）
	sendEnabled atomic.Bool // 本次通话是否推送本地视频（普通接听 = false）
	previewOn   atomic.Bool // 需求 7：本地回显开关（**只管本地显示，不影响推向远端**）
	holdTx      atomic.Bool // 对端保持
	// localWantedFn 界面回显之外的本地帧消费者（网页订阅本地画面，REQ-VIDEO-009）
	localWantedFn atomic.Pointer[func() bool]

	// 接收侧（recvMu 只护重组状态）
	recvMu   sync.Mutex
	recvSSRC uint32
	hasSeq   bool
	lastSeq  uint16
	rxLost   atomic.Uint64
	au       []byte
	inFrame  bool
	frameTS  uint32
	waitKey  bool
	auCh     chan auUnit // 访问单元 → 解码循环（容量 4，满则丢帧）

	// 解码（decMu 护解码器与尺寸）
	decMu   sync.Mutex
	dec     video.Decoder
	decW    int
	decH    int
	remoteW atomic.Int32
	remoteH atomic.Int32

	// 采集
	camMu         sync.Mutex
	camConfigured bool // StartCamera 是否已给出采集参数（未给出时监管协程不开设备）
	cameraID      string
	cameraName    string // 设备名（Windows 上驱动 ID 每次启动都会变，按名称兜底匹配）
	camW          int
	camH          int
	camFPS        int
	camBitrate    int
	camCancel     context.CancelFunc
	camDone       chan struct{}
	camReq        chan struct{} // 通知采集监管协程重新评估启停

	// 输出
	sinkMu  sync.RWMutex
	sink    FrameSink
	stateMu sync.RWMutex
	stateCb func() // 状态变化回调（协商成功/开关切换/分辨率变化），低频

	stop      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	rxPackets atomic.Uint64
	anyPkts   atomic.Uint64 // 到达本端口的任意 UDP 包数（前 3 个打日志，诊断用）
	rxOtherPT atomic.Uint64 // 负载类型不符被丢弃的包数（诊断用）
	txPackets atomic.Uint64
	txBytes   atomic.Uint64
}

// FrameSink 视频帧回调。**在媒体层 goroutine 上同步调用**，实现必须立即返回：
// 禁止 fyne.DoAndWait 之类的阻塞调用，否则解码循环会被卡住、UDP 接收缓冲
// 溢出，之后持续花屏。img 每帧新分配，调用方可安全保留引用。
type FrameSink func(local bool, img *image.RGBA)

// NewVideoEngine 创建视频引擎并绑定独立的 RTP/RTCP 连续端口。
// 未启用视频的构建返回 ErrVideoUnsupported。
func NewVideoEngine(localIP string) (*VideoEngine, error) {
	if !video.Supported() {
		return nil, ErrVideoUnsupported
	}
	conn, rtcpConn, err := bindRTPRTCP(localIP)
	if err != nil {
		return nil, err
	}
	v := &VideoEngine{
		conn:      conn,
		localIP:   localIP,
		localPort: conn.LocalAddr().(*net.UDPAddr).Port,
		ssrc:      uint32(time.Now().UnixNano()),
		seq:       uint16(time.Now().UnixNano() >> 7),
		stop:      make(chan struct{}),
		camReq:    make(chan struct{}, 1),
		auCh:      make(chan auUnit, 4),
	}
	v.enabled.Store(true)
	v.previewOn.Store(true)
	v.rtcp = newRTCPSession(rtcpConn, v)
	v.wg.Add(1)
	go v.cameraSupervisor()
	logx.Logger().Debug("视频引擎已创建", "localPort", v.localPort, "rtcpPort", v.localPort+1)
	return v, nil
}

// ErrVideoUnsupported 当前构建不支持视频。
var ErrVideoUnsupported = video.ErrUnsupported

// LocalPort 本地视频 RTP 端口（SDP 用）。
func (v *VideoEngine) LocalPort() int { return v.localPort }

// SetSDPIP 设置 SDP 宣告地址（NAT 场景与绑定地址不同）。
func (v *VideoEngine) SetSDPIP(ip string) { v.sdpIP = ip }

// SetFrameSink 注册帧回调（通常只调一次）。
func (v *VideoEngine) SetFrameSink(s FrameSink) {
	v.sinkMu.Lock()
	v.sink = s
	v.sinkMu.Unlock()
}

// SetLocalWantedFunc 注入「界面回显之外还有人要本地帧吗」的判定，例如
// 网页订阅了本地画面（REQ-VIDEO-009）。
//
// 本地帧的 YUV→RGBA 转换每帧要分配一整张图，所以采集循环只在**确实有人要**
// 时才做：回显开着（界面要）或本判定为真（网页要）。传 nil 表示没有额外消费者。
// ⚠️ 判定在采集 goroutine 上逐帧调用，实现必须快（内部只读一个状态位）。
func (v *VideoEngine) SetLocalWantedFunc(f func() bool) {
	v.localWantedFn.Store(&f)
}

// localWanted 当前是否有界面回显之外的消费者需要本地帧。
func (v *VideoEngine) localWanted() bool {
	if f := v.localWantedFn.Load(); f != nil && *f != nil {
		return (*f)()
	}
	return false
}

// localFramesWanted 本地帧是否要投递给界面/网页：视频开着，且
// **回显开着或网页订阅了本地画面**（需求 7 修订：关闭回显只关界面显示，
// 不影响网页订阅；禁用视频则两边都没有）。
func (v *VideoEngine) localFramesWanted() bool {
	return v.enabled.Load() && (v.previewOn.Load() || v.localWanted())
}

func (v *VideoEngine) emit(local bool, img *image.RGBA) {
	v.sinkMu.RLock()
	s := v.sink
	v.sinkMu.RUnlock()
	if s != nil {
		s(local, img)
	}
}

// SetStateCallback 注册状态变化回调（协商成功、开关切换、拿到对端分辨率）。
// 低频调用，实现在媒体层 goroutine 上同步执行，必须立即返回。
func (v *VideoEngine) SetStateCallback(cb func()) {
	v.stateMu.Lock()
	v.stateCb = cb
	v.stateMu.Unlock()
}

// notifyState 触发状态回调。
func (v *VideoEngine) notifyState() {
	v.stateMu.RLock()
	cb := v.stateCb
	v.stateMu.RUnlock()
	if cb != nil {
		cb()
	}
}

// IsEnabled 需求 8 的总开关状态。
func (v *VideoEngine) IsEnabled() bool { return v.enabled.Load() }

// IsPreviewOn 需求 7 的本地回显开关状态。
func (v *VideoEngine) IsPreviewOn() bool { return v.previewOn.Load() }

// IsSending 本次通话是否**要**推送本地视频（总开关 + 允许发送 + 摄像头已配置）。
// 这是"意图"，不随本地回显开关变化——界面据此决定要不要显示回显区域。
func (v *VideoEngine) IsSending() bool {
	v.camMu.Lock()
	configured := v.camConfigured
	v.camMu.Unlock()
	return configured && v.enabled.Load() && v.sendEnabled.Load()
}

// --- SDP ---

// offerSection 生成 offer 用的视频媒体段：列出本端**全部可用编码**及其本端 PT 号。
func (v *VideoEngine) offerSection(dir string) *sdpSection {
	var codecs []sdpCodec
	for _, c := range usableVideoCodecs() {
		codecs = append(codecs, sdpCodec{PT: c.PT, Encoding: c.Name, ClockRate: c.ClockRate, FMTP: c.FMTP})
	}
	if len(codecs) == 0 {
		return nil
	}
	return &sdpSection{Kind: "video", Port: v.localPort, RTCPPort: v.localPort + 1, Codecs: codecs, Dir: dir}
}

// answerSection 生成 answer 用的视频媒体段：只列协商到的那一个编码，
// 且**必须用对端的 PT 号**（RFC 3264）。
//
// ⚠️ 不能拿对端 PT 去查本端编码表：本端表里是 96–99，对端 offer 可能是
// 100/101…，查不到就会返回 nil，上层随即输出 m=video 0——表现为「视频被拒绝、
// 两边都看不到画面」。协商结果（编码 + 对端 PT）已存在 codec/peerPT 里，直接用。
func (v *VideoEngine) answerSection(dir string) *sdpSection {
	v.codecMu.RLock()
	c, pt, fmtp := v.codec, v.peerPT, v.answerFMTP
	v.codecMu.RUnlock()
	if c.Name == "" {
		return nil
	}
	if fmtp == "" {
		fmtp = c.FMTP
	}
	return &sdpSection{
		Kind: "video", Port: v.localPort, RTCPPort: v.localPort + 1,
		Codecs: []sdpCodec{{PT: pt, Encoding: c.Name, ClockRate: c.ClockRate, FMTP: fmtp}},
		Dir:    dir,
	}
}

// RejectedSection 生成视频拒绝段（m=video 0 ...）。
// RFC 3264 要求 answer 的 m= 行数量与顺序跟 offer 一致，不接受视频时
// **必须**回 0 端口而不是省略该行，否则严格的对端（FreeSWITCH 部分版本）会回 488。
func RejectedSection(offer *MediaInfo) string {
	var b []byte
	b = fmt.Appendf(b, "m=video 0 RTP/AVP")
	for _, pt := range offer.PTs {
		b = fmt.Appendf(b, " %d", pt)
	}
	return string(append(b, "\r\n"...))
}

// Available 视频是否已协商成功且未被对端拒绝。
func (v *VideoEngine) Available() bool {
	v.codecMu.RLock()
	defer v.codecMu.RUnlock()
	return v.codec.Name != ""
}

// NegotiatedCodec 已协商的视频编码。
func (v *VideoEngine) NegotiatedCodec() (VideoCodec, bool) {
	v.codecMu.RLock()
	defer v.codecMu.RUnlock()
	return v.codec, v.codec.Name != ""
}

// ApplyOffer 应用对端 offer 的视频段（呼入路径）。返回是否协商成功。
func (v *VideoEngine) ApplyOffer(info *MediaInfo) bool {
	if info == nil || info.Rejected || info.Addr == nil {
		return false
	}
	rc, peerPT, fmtp, ok := negotiateVideo(info)
	if !ok {
		logx.Logger().Warn("无共同视频编解码器", "对端支持", ptListString(info.PTs))
		return false
	}
	v.setNegotiated(rc, peerPT, fmtp)
	v.setRemote(info.Addr, info.RTCPPort)
	v.punchHole()
	v.notifyState()
	mode := info.PacketizationMode(peerPT)
	logx.Logger().Info("视频协商完成（呼入）", "codec", rc.Name, "peerPT", peerPT,
		"remote", info.Addr.String(), "dir", info.Dir, "对端mode", mode)
	if mode == 0 && rc.Name == videoCodecH264 {
		// 单 NAL 模式：对端只发单包，本端照收；本端发送仍可能在必要时用 FU-A 分片
		//（见 negotiateVideo 注释）。这条日志用于解释"对方看不到我"的可能原因。
		logx.Logger().Info("对端声明 packetization-mode=0（单 NAL），本端按需仍用 FU-A 分片发送")
	}
	return true
}

// ApplyAnswer 应用对端 answer 的视频段（呼出路径）。返回是否协商成功。
func (v *VideoEngine) ApplyAnswer(info *MediaInfo) bool {
	if info == nil || info.Rejected || info.Addr == nil {
		return false
	}
	// answer 里的 PT 取自本端 offer，直接按号取编码即可
	if len(info.PTs) == 0 {
		return false
	}
	pt := info.PTs[0]
	rc, ok := videoCodecByPT(pt)
	if !ok {
		logx.Logger().Warn("对端 answer 的视频负载类型未知", "pt", pt)
		return false
	}
	// 呼出路径不会再构造 answer，这里只记录本端编码的 fmtp（answerSection 用不到）
	v.setNegotiated(rc, pt, rc.FMTP)
	v.setRemote(info.Addr, info.RTCPPort)
	v.punchHole()
	v.notifyState()
	logx.Logger().Info("视频协商完成（呼出）", "codec", rc.Name, "pt", pt, "remote", info.Addr.String())
	// 本端 offer 写的是 packetization-mode=1，对端**显式**回 0 说明它只收单 NAL，
	// 本端的大帧（FU-A 分片）它多半解不了——留条线索，别让它变成"对方看不到我"的悬案。
	if mode, ok := info.FmtpParam(pt, "packetization-mode"); ok && strings.TrimSpace(mode) == "0" && rc.Name == videoCodecH264 {
		logx.Logger().Info("对端 answer 声明 packetization-mode=0（单 NAL），本端仍按需用 FU-A 分片发送")
	}
	return true
}

// setNegotiated 记录协商结果。answerFMTP 是 answer 里要回给对端的 fmtp
//（packetization-mode 已按对端 offer 取值，见 answerVideoFMTP）。
func (v *VideoEngine) setNegotiated(c VideoCodec, peerPT uint8, answerFMTP string) {
	v.codecMu.Lock()
	v.codec = c
	v.peerPT = peerPT
	v.answerFMTP = answerFMTP
	v.codecMu.Unlock()
	v.pay = newVideoPacketizer(c.Name)
	v.dep = newVideoDepacketizer(c.Name)
}

// --- 生命周期 ---

// Start 启动视频收发（确定远端地址后调用）。
func (v *VideoEngine) Start() {
	v.wg.Add(3)
	go v.recvLoop()
	go v.decodeLoop()
	go v.placeholderLoop()
	v.rtcp.start()
	v.punchHole()
	// 诊断：8 秒后还没收到对端任何视频包就打一条线索（只打一次，挂断则不打）。
	// 「视频窗口开了但对方画面一直不来」时，它能立刻区分「对端没发」与
	// 「本端没收到/被过滤」，省掉一轮抓包。
	time.AfterFunc(8*time.Second, v.diagNoRemoteVideo)
}

// --- 占位画面（摄像头不可用/被禁用时的发送源）---

// placeholderW/H 占位画面尺寸；帧率与码率都很低——内容是一块静止的深灰，
// 目的只是让对端把这条腿当"在发视频"，不是给人看的。
const (
	placeholderW   = 320
	placeholderH   = 240
	placeholderFPS = 5
	placeholderBps = 256_000
)

// placeholderLoop 摄像头不可用（禁用/打不开）但本次通话要发送视频时，
// 持续发送一路低帧率的**占位画面**（静止深灰），本端本地回显不受影响
// （摄像头没开，本来就没有回显）。
//
// 为什么必须发（实测，2026-10-05）：对端（FreeSWITCH 等）先把媒体发到 SDP
// 里宣告的地址，只有当它**收到本端该腿的有效媒体**之后，才会把发送目标切换成
// 实际观察到的源地址（对称 RTP / 媒体锁定）。摄像头禁用时本端在视频流上一个包
// 都不发，对端就始终把画面发往我们收不到的地址——本机内网地址（NAT 场景下对端
// 不可达；出向端口还会被 NAT 改写，无法预先宣告），表现是"看不见对方画面、
// 连视频窗口都不出现"。发一路占位画面即可让对端正常回送画面。
//
// 只在没有摄像头时运行：有摄像头时由采集循环发真帧（`sendEnabled` 为真→这里让位）。
func (v *VideoEngine) placeholderLoop() {
	defer v.wg.Done()
	t := time.NewTicker(time.Second / placeholderFPS)
	defer t.Stop()

	var (
		enc      video.Encoder
		encCodec string
		frame    *video.YUV420P
		idx      int
	)
	defer func() {
		if enc != nil {
			enc.Close()
		}
	}()

	for {
		select {
		case <-v.stop:
			return
		case <-t.C:
		}
		if !v.enabled.Load() || v.sendEnabled.Load() || v.holdTx.Load() {
			continue // 关闭视频 / 有摄像头在发真帧 / 对端保持：不需要占位画面
		}
		rc, ok := v.NegotiatedCodec()
		if !ok || !v.hasRemote() {
			continue // 还没协商出编码 / 还不知道对端地址
		}
		if enc == nil || encCodec != rc.Name {
			if enc != nil {
				enc.Close()
				enc, frame = nil, nil
			}
			w, h := placeholderW, placeholderH
			if rc.Name == videoCodecH263 {
				// H.263 只吃标准尺寸，缩到能容纳的最大档（同采集路径）
				w, h = standardH263Size(w, h)
			}
			e, err := video.NewEncoder(rc.Name, w, h, placeholderFPS, placeholderBps)
			if err != nil {
				logx.Logger().Warn("占位画面编码器创建失败", "codec", rc.Name, "err", err)
				continue
			}
			f := video.NewYUV420P(w, h)
			for i := range f.Y {
				f.Y[i] = 0x40 // 深灰
			}
			for i := range f.U {
				f.U[i] = 0x80
			}
			for i := range f.V {
				f.V[i] = 0x80
			}
			enc, encCodec, frame = e, rc.Name, f
			logx.Logger().Info("摄像头不可用，改为发送占位画面（静止深灰，让对端正常回送画面）",
				"codec", rc.Name, "size", fmt.Sprintf("%dx%d", w, h), "fps", placeholderFPS)
		}
		// 首个画面即关键帧，之后每秒一个（对端中途请求关键帧也走 forceKey）
		force := v.forceKey.Swap(false) || idx == 0 || idx%int(placeholderFPS) == 0
		data, _, err := enc.Encode(frame, force)
		idx++
		if err != nil {
			logx.Logger().Debug("占位画面编码失败", "err", err)
			continue
		}
		v.sendFrame(data)
	}
}

// diagNoRemoteVideo 启动后迟迟收不到对端视频时的诊断（见 Start）。
func (v *VideoEngine) diagNoRemoteVideo() {
	select {
	case <-v.stop:
		return // 已挂断，别误导
	default:
	}
	if v.rxPackets.Load() > 0 {
		return
	}
	rx, tx, _ := v.Stats()
	hint := "对端可能没有视频源（摄像头未开/被禁用），或仍在等本端画面"
	switch {
	case tx == 0:
		hint = "本端一个视频包也没发出（摄像头打开失败？）——对端可能因此在等本端画面"
	case v.anyPkts.Load() > 0 && rx == 0:
		hint = "有 UDP 包到达本端口但不是能用的视频 RTP（负载类型不符或不是 RTP）"
	}
	logx.Logger().Warn("视频已启动 8 秒仍未收到对端视频 RTP",
		"端口收包", v.anyPkts.Load(), "有效", rx, "负载类型不符", v.rxOtherPT.Load(),
		"已发", tx, "提示", hint)
}

// Close 停止全部活动并释放端口。幂等。
func (v *VideoEngine) Close() {
	v.closeOnce.Do(func() {
		close(v.stop)
		v.camMu.Lock()
		if v.camCancel != nil {
			v.camCancel()
			v.camCancel = nil
		}
		v.camMu.Unlock()
		// 摄像头关闭可能阻塞（DirectShow 尤甚），最多等 2 秒后继续收尾，
		// 不能拖死挂断流程（对齐 audio.Manager 的 closeWait 思路）。
		done := make(chan struct{})
		go func() { v.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			logx.Logger().Warn("视频引擎收尾超时，强制关闭端口")
		}
		v.rtcp.sendBye()
		v.rtcp.stop()
		v.decMu.Lock()
		if v.dec != nil {
			v.dec.Close()
			v.dec = nil
		}
		v.decMu.Unlock()
		_ = v.conn.Close()
	})
}

// --- 开关 ---

// SetEnabled 需求 8：关闭/恢复视频（远端不显示、本地不推送，视频窗口关闭）。
// 纯本地动作，不发 SIP 信令——与麦克风/扬声器开关同语义，避免 re-INVITE 竞态。
func (v *VideoEngine) SetEnabled(on bool) {
	if v.enabled.Swap(on) == on {
		return
	}
	if on {
		// 重新开启：解包器与解码器都要重置，并等下一个关键帧，
		// 否则会拿旧的参考帧解出花屏。
		v.resetReceive()
		v.RequestKeyframe()
	}
	v.syncCamera()
	v.notifyState()
}

// SetVideoPreview 需求 7：本地回显开关。
//
// **只控制本地画面是否投递给界面**，不触碰推送：摄像头照常采集编码发送，
// 远端始终能看到本端画面（用户明确要求"关闭回显只是关闭本地的回显"）。
// 因此这里既不调 syncCamera（不启停采集），也不改 sendEnabled。
func (v *VideoEngine) SetVideoPreview(on bool) {
	if v.previewOn.Swap(on) == on {
		return
	}
	v.notifyState()
}

// SetSendEnabled 本次通话是否推送本地视频（普通接听 = false，只收不发）。
func (v *VideoEngine) SetSendEnabled(on bool) {
	if v.sendEnabled.Swap(on) == on {
		return
	}
	v.syncCamera()
	v.notifyState()
}

// SetRemoteHold 对端保持：停止推送本地视频（远端画面保留）。
func (v *VideoEngine) SetRemoteHold(hold bool) {
	v.holdTx.Store(hold)
}

// RemoteSize 对端视频分辨率（收到首帧后才有值）。
func (v *VideoEngine) RemoteSize() (w, h int, ok bool) {
	w, h = int(v.remoteW.Load()), int(v.remoteH.Load())
	return w, h, w > 0 && h > 0
}

// Stats 收发统计。
func (v *VideoEngine) Stats() (rx, tx, lost uint64) {
	return v.rxPackets.Load(), v.txPackets.Load(), v.rxLost.Load()
}

// RequestKeyframe 请求对端发关键帧（RTCP PLI，内部限流）。
func (v *VideoEngine) RequestKeyframe() {
	v.recvMu.Lock()
	ssrc := v.recvSSRC
	v.recvMu.Unlock()
	v.rtcp.sendPLI(ssrc)
}

// --- rtcpEndpoint 实现 ---

func (v *VideoEngine) rtcpSSRC() uint32 { return v.ssrc }

func (v *VideoEngine) rtcpRTPTime() uint32 {
	v.sndMu.Lock()
	defer v.sndMu.Unlock()
	return v.ts
}

func (v *VideoEngine) rtcpTxStats() (packets, bytes uint64) {
	return v.txPackets.Load(), v.txBytes.Load()
}

func (v *VideoEngine) rtcpRecvState() (uint16, uint32, uint64, bool) {
	v.recvMu.Lock()
	has, lastSeq := v.hasSeq, v.lastSeq
	v.recvMu.Unlock()
	return lastSeq, 0, v.rxLost.Load(), has
}

// rtcpOnFeedback 对端报告画面损坏 → 下一帧强制关键帧。
func (v *VideoEngine) rtcpOnFeedback(pkt rtcp.Packet) {
	switch pkt.(type) {
	case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
		v.forceKey.Store(true)
		logx.Logger().Debug("对端请求关键帧（RTCP 反馈）")
	}
}

// --- 收发 ---

func (v *VideoEngine) punchHole() {
	if !v.hasRemote() || v.pay == nil {
		return
	}
	// 视频打洞用极小负载占位（真正的帧要等采集），避免对端 NAT 映射过期。
	v.remoteMu.RLock()
	remote := v.remote
	v.remoteMu.RUnlock()
	if remote == nil {
		return
	}
	pkt := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: v.payloadType(), SSRC: v.ssrc}}
	if data, err := pkt.Marshal(); err == nil {
		_, _ = v.conn.WriteToUDP(data, remote)
	}
}

func (v *VideoEngine) hasRemote() bool {
	v.remoteMu.RLock()
	defer v.remoteMu.RUnlock()
	return v.remote != nil
}

func (v *VideoEngine) setRemote(addr *net.UDPAddr, rtcpPort int) {
	v.remoteMu.Lock()
	v.remote = addr
	v.remoteMu.Unlock()
	v.rtcp.setRemote(addr, rtcpPort)
}

func (v *VideoEngine) payloadType() uint8 {
	v.codecMu.RLock()
	defer v.codecMu.RUnlock()
	return v.peerPT
}

// sendFrame 把一帧编码数据打包成 RTP 发送。
// 同一帧的所有包共用一个时间戳，最后一包置 marker。
func (v *VideoEngine) sendFrame(frame []byte) {
	v.codecMu.RLock()
	pay := v.pay
	v.codecMu.RUnlock()
	if pay == nil {
		return
	}
	v.remoteMu.RLock()
	remote := v.remote
	v.remoteMu.RUnlock()
	if remote == nil {
		return
	}
	payloads := pay.Payloads(frame, videoMTU)
	if len(payloads) == 0 {
		return
	}

	v.sndMu.Lock()
	defer v.sndMu.Unlock()
	// 视频时间戳按墙钟推进（容忍变帧率），首帧兜底一帧间隔。
	now := time.Now()
	var delta uint32
	if v.lastTS.IsZero() {
		delta = 3000 // 90000 / 30
	} else {
		delta = uint32(now.Sub(v.lastTS).Seconds() * 90000)
		if delta == 0 {
			delta = 1
		}
	}
	v.lastTS = now
	v.ts += delta

	for i, p := range payloads {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         i == len(payloads)-1,
				PayloadType:    v.payloadType(),
				SequenceNumber: v.seq,
				Timestamp:      v.ts,
				SSRC:           v.ssrc,
			},
			Payload: p,
		}
		data, err := pkt.Marshal()
		if err != nil {
			continue
		}
		if _, err := v.conn.WriteToUDP(data, remote); err != nil {
			logx.Logger().Debug("视频 RTP 发送失败", "err", err)
			return
		}
		v.seq++
		if v.txPackets.Add(1) == 1 {
			// 首个发出的视频包：确认"采集→编码→发送"整条发送链路已通
			//（对端收不到画面时，用这条日志与「摄像头首帧已到达」对比即可定位）
			logx.Logger().Info("已发出首个视频 RTP 包",
				"pt", v.payloadType(), "ssrc", v.ssrc, "分片数", len(payloads))
		}
		v.txBytes.Add(uint64(len(p)))
	}
}

// recvLoop 接收视频 RTP：解包重组访问单元 → 解码通道。
func (v *VideoEngine) recvLoop() {
	defer v.wg.Done()
	buf := make([]byte, 2048)
	for {
		select {
		case <-v.stop:
			return
		default:
		}
		_ = v.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, raddr, err := v.conn.ReadFromUDP(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			select {
			case <-v.stop:
				return
			default:
				logx.Logger().Debug("视频 RTP 读取错误", "err", err)
				return
			}
		}
		// 任意到达 socket 的 UDP 包都记录前 3 个（Info 级，排查用），与音频引擎同款：
		// 「对方的视频收不到」时，这里从未打印 = 对端根本没往本端口发包（网络/对端问题）；
		// 打印了但 rx=0 = 包到了却被过滤（负载类型不符，见下面的分支）。
		if c := v.anyPkts.Add(1); c <= 3 {
			preview := fmt.Sprintf("%x", buf[:min(n, 16)])
			logx.Logger().Info("视频端口收到 UDP 包", "src", raddr.String(), "len", n, "preview", preview)
		}
		if !v.enabled.Load() {
			continue // 需求 8：关闭视频期间不接收
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}
		if pkt.PayloadType != v.payloadType() {
			// 负载类型不符就丢（协商前 peerPT=0，此时收到的都是野生包）。
			// 首次出现打条日志：排查「视频收不到」时，它能区分
			// 「对端根本没发」与「发了但 PT 对不上、被本端过滤掉」。
			if v.rxOtherPT.Add(1) == 1 {
				logx.Logger().Info("视频端口收到非协商负载类型，已忽略",
					"pt", pkt.PayloadType, "期望", v.payloadType(), "ssrc", pkt.SSRC)
			}
			continue
		}
		if v.rxPackets.Add(1) == 1 {
			logx.Logger().Info("收到首个视频 RTP 包",
				"pt", pkt.PayloadType, "ssrc", pkt.SSRC, "len", n)
		}
		v.handleRTP(&pkt)
	}
}

func (v *VideoEngine) handleRTP(pkt *rtp.Packet) {
	dep := v.depacketizer()
	if dep == nil {
		return
	}

	needKeyframe := false
	v.recvMu.Lock()
	if v.hasSeq {
		gap := pkt.SequenceNumber - v.lastSeq - 1
		switch {
		case gap >= 0x8000:
			// 乱序 / 过期包：丢弃
			v.recvMu.Unlock()
			return
		case pkt.SSRC != v.recvSSRC || gap > 3000:
			// 换流（SSRC 变化 / 序号大跳变，如 183 早期媒体 → 200 新媒体流）：
			// 重组缓冲与解包器状态全部作废，等下一个关键帧。
			v.au = v.au[:0]
			v.inFrame = false
			v.hasSeq = false
			v.waitKey = true
			dep.Reset()
		case gap > 0:
			// 丢包：当前访问单元已不可用，必须整帧丢弃
			//（视频不能像音频那样填静音）。三件事缺一不可：
			//
			//  1. 清空已累积的访问单元；
			//  2. **重置解包器**：H264 的 FU-A 重组缓冲里还留着丢包前的分片，
			//     不重置的话丢包后的分片会续接到旧缓冲上，拼出一个"缝合怪"
			//     NAL——解码器照单全收，画面就是花屏（用户实测的"偶尔花屏"）；
			//  3. 等下一个关键帧（waitKey）并催对端出 IDR（PLI）。
			//     配合 PLI 通常只黑几十毫秒，比解出残缺画面好得多。
			v.rxLost.Add(uint64(gap))
			v.au = v.au[:0]
			v.inFrame = false
			v.waitKey = true
			dep.Reset()
			needKeyframe = true
		}
	}
	if !v.hasSeq {
		v.recvSSRC = pkt.SSRC
		v.hasSeq = true
		v.waitKey = true
		// 新流的第一包就催对端出关键帧（PLI）：解码要等 IDR，而不少对端
		// （如 MicroSIP）只在被请求或很久之后才发。不催的话表现为"接通后
		// 视频窗口一直不出画面，开关一下视频（内部会发 PLI）才出来"。
		needKeyframe = true
	}
	v.lastSeq = pkt.SequenceNumber

	// 时间戳变化说明上一帧已结束（兼容不按规范置 marker 的对端）
	if v.inFrame && pkt.Timestamp != v.frameTS {
		v.flushAU(dep)
	}
	if !v.inFrame {
		v.inFrame = true
		v.frameTS = pkt.Timestamp
	}
	v.recvMu.Unlock()
	if needKeyframe {
		// 丢包/换流后请求关键帧（内部有 500ms 限流，不会打爆对端编码器）
		v.RequestKeyframe()
	}

	data, complete := dep.Push(pkt.Payload, pkt.Marker)

	v.recvMu.Lock()
	v.au = append(v.au, data...)
	if complete {
		v.flushAU(dep)
	}
	v.recvMu.Unlock()
}

// depacketizer 取当前解包器（协商结果与解包器在 codecMu 下一起更新）。
func (v *VideoEngine) depacketizer() videoDepacketizer {
	v.codecMu.RLock()
	defer v.codecMu.RUnlock()
	return v.dep
}

// flushAU 把累积的完整访问单元交给解码循环。调用方须持有 recvMu。
//
// 关键帧判定必须在**整个访问单元**上做，不能用第一个分片：H264 的首个负载
// 是 STAP-A(SPS,PPS)，里面没有 IDR，按分片判会把真正的关键帧误判成帧间帧，
// 于是「等关键帧」状态下永远等不到、画面一直黑屏。
func (v *VideoEngine) flushAU(dep videoDepacketizer) {
	if len(v.au) == 0 {
		v.inFrame = false
		return
	}
	au := make([]byte, len(v.au))
	copy(au, v.au)
	key := dep.Keyframe(au)
	v.au = v.au[:0]
	v.inFrame = false

	if v.waitKey && !key {
		return // 等关键帧期间丢弃帧间帧
	}
	v.waitKey = false

	select {
	case v.auCh <- auUnit{data: au}:
	default:
		// 解码跟不上：丢弃本帧（视频可丢帧，绝不阻塞接收）
	}
}

// resetReceive 重置接收侧重组与解码状态（换流 / 重新开启视频）。
func (v *VideoEngine) resetReceive() {
	v.recvMu.Lock()
	v.au = v.au[:0]
	v.inFrame = false
	v.waitKey = true
	v.hasSeq = false
	if v.dep != nil {
		v.dep.Reset()
	}
	v.recvMu.Unlock()

	v.decMu.Lock()
	if v.dec != nil {
		v.dec.Reset()
	}
	v.decMu.Unlock()
}

// auUnit 一个待解码的完整访问单元。auCh 容量 4，满则丢帧。
type auUnit struct{ data []byte }

// decodeLoop 解码访问单元并投递 RGBA 帧。
func (v *VideoEngine) decodeLoop() {
	defer v.wg.Done()
	for {
		select {
		case <-v.stop:
			return
		case u := <-v.auCh:
			v.decodeOne(u.data)
		}
	}
}

func (v *VideoEngine) decodeOne(au []byte) {
	if !v.enabled.Load() {
		return
	}
	v.decMu.Lock()
	if v.dec == nil {
		rc, ok := v.NegotiatedCodec()
		if !ok {
			v.decMu.Unlock()
			return
		}
		d, err := video.NewDecoder(rc.Name)
		if err != nil {
			v.decMu.Unlock()
			logx.Logger().Error("创建视频解码器失败", "codec", rc.Name, "err", err)
			return
		}
		v.dec = d
	}
	dec := v.dec
	v.decMu.Unlock()

	f, err := dec.Decode(au)
	if err != nil {
		logx.Logger().Debug("视频解码失败", "err", err)
		return
	}
	if f == nil {
		return
	}
	if f.W != v.decW || f.H != v.decH {
		v.decW, v.decH = f.W, f.H
		v.remoteW.Store(int32(f.W))
		v.remoteH.Store(int32(f.H))
		logx.Logger().Info("对端视频尺寸", "w", f.W, "h", f.H)
		go v.notifyState()
	}
	v.emit(false, f.RGBA())
}

// --- 摄像头采集 ---

// StartCamera 指定摄像头与格式（通话建立后调用）。
// 不阻塞：设备在采集 goroutine 里打开（DirectShow 打开可能耗时 1 秒以上，
// 不能卡住 SIP 的信令 goroutine）。
func (v *VideoEngine) StartCamera(deviceID, deviceName string, w, h, fps, bitrateBps int) {
	v.camMu.Lock()
	v.cameraID, v.cameraName = deviceID, deviceName
	v.camW, v.camH, v.camFPS, v.camBitrate = w, h, fps, bitrateBps
	v.camConfigured = true
	v.camMu.Unlock()
	v.syncCamera()
}

// syncCamera 通知采集监管协程重新评估启停（非阻塞）。
func (v *VideoEngine) syncCamera() {
	select {
	case v.camReq <- struct{}{}:
	default:
	}
}

// cameraSupervisor 串行管理采集 goroutine 的启停，保证同一时刻只有一个采集器、
// 且上一路彻底关闭后才开下一路（Windows 下单设备不能并发打开）。
func (v *VideoEngine) cameraSupervisor() {
	defer v.wg.Done()
	for {
		select {
		case <-v.stop:
			v.stopCapture()
			return
		case <-v.camReq:
		}

		v.camMu.Lock()
		configured := v.camConfigured
		cancel := v.camCancel
		v.camMu.Unlock()
		// 未给出采集参数时不开设备：否则会用空 ID 抢先打开默认摄像头，
		// 用户配置的那台就再也打不开了。
		// 采集的启停只看"要不要推送"：本地回显开关不影响采集（关了回显仍要推流）
		want := configured && v.enabled.Load() && v.sendEnabled.Load()

		switch {
		case want && cancel == nil:
			ctx, c := context.WithCancel(context.Background())
			d := make(chan struct{})
			v.camMu.Lock()
			v.camCancel, v.camDone = c, d
			v.camMu.Unlock()
			// ⚠️ 采集 goroutine **不计入 v.wg**：Close() 会调 v.wg.Wait()，
			// 而这里可能与它并发 Add，触发 Go 运行时的
			// "sync: WaitGroup misuse: Add called concurrently with Wait" panic。
			// 改由 supervisor 串行等待（见 stopCapture），Close 只需等 supervisor 退出。
			go func() {
				defer close(d)
				v.captureLoop(ctx)
			}()
		case !want && cancel != nil:
			v.stopCapture()
		}
	}
}

// stopCapture 停掉当前采集并等它彻底收尾（含设备关闭），
// 避免与下一路采集抢占设备。可重复调用。
func (v *VideoEngine) stopCapture() {
	v.camMu.Lock()
	cancel, done := v.camCancel, v.camDone
	v.camCancel, v.camDone = nil, nil
	v.camMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	// 等的时间必须**小于 Close 的等待上限**，否则挂断会白等；
	// 超时也放行，open/close 在 camera 层已串行化，不会与下一路打架。
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		logx.Logger().Warn("摄像头关闭超时，继续")
	}
}

// captureLoop 采集 → 编码 → 发送 + 本地回显。持有摄像头设备。
func (v *VideoEngine) captureLoop(ctx context.Context) {
	v.camMu.Lock()
	id, name := v.cameraID, v.cameraName
	w, h, fps, bitrate := v.camW, v.camH, v.camFPS, v.camBitrate
	v.camMu.Unlock()

	// Windows 的驱动 ID 每次进程启动都会变（mediadevices 包装层生成），
	// 配置里存的名字才是稳定标识——按「ID 精确 → 名称 → 第一个设备」回退。
	// 枚举有开销，但本函数跑在采集 goroutine 上，不影响信令与界面。
	if cams, cerr := video.ListCameras(); cerr == nil && len(cams) > 0 {
		picked := video.PickCamera(cams, id, name)
		if picked != id {
			logx.Logger().Info("摄像头按名称匹配", "配置ID", id, "名称", name, "实际ID", picked)
		}
		id = picked
	}

	cam, err := video.OpenCamera(id, w, h, fps)
	if err != nil {
		logx.Logger().Warn("打开摄像头失败", "id", id, "err", err)
		return
	}
	defer cam.Close()

	// 打开设备期间用户可能已挂断/关闭视频：立刻收摊
	select {
	case <-ctx.Done():
		return
	case <-v.stop:
		return
	default:
	}

	// ⚠️ **不能因为"还没协商出编码"就退出采集**：视频呼叫是拨出即开摄像头的，
	// 那时 SDP 还没协商（本地回显与编码无关，必须立刻能看）。早期在这里
	// `NegotiatedCodec()` 不成立就 return，表现为"视频窗口打开了、摄像头灯也亮了，
	// 但没有本地回显、对方也看不到我的视频"——整个采集 goroutine 静悄悄退出，
	// 而 startVideo 是幂等的，接通后不会再启一次。
	// 现在改为：编码器按需创建，协商好之前只做本地回显。
	// ⚠️ 编码器必须按**实际帧尺寸**建，不能按请求尺寸。
	// 摄像头驱动不保证按请求分辨率出图——Windows DirectShow 会挑最接近的
	// 一档。若编码器按 640×480 建、帧按 352×288 喂，OpenH264/libvpx 会按
	// 640×480 去读一块更小的缓冲，**越界读直接崩溃**（"接听后有时崩溃"的根因）。
	// 所以这里每帧比对尺寸，不一致就重建编码器。
	var (
		enc              video.Encoder
		encCodec         string // 当前编码器对应的编码名（重协商换编码时重建）
		encInW, encInH   int    // 编码器的输入尺寸（实际帧尺寸）
		encOutW, encOutH int    // 编码器的输出尺寸（H263 只吃标准尺寸，会不同）
		encFPS           = fps
		encFailKey       string // 建不出来的编码器参数（避免每帧重试刷屏）
	)
	defer func() {
		if enc != nil {
			enc.Close()
		}
	}()

	var readErrs, frames int
	// rebuildEncoder 按已协商的编码建编码器；失败时返回 false（本帧不发送，
	// 本地回显不受影响）。
	rebuildEncoder := func(rc VideoCodec, inW, inH int) bool {
		if enc != nil {
			enc.Close()
			enc = nil
		}
		encCodec = ""
		outW, outH := inW, inH
		if rc.Name == videoCodecH263 {
			// H.263 只接受 QCIF/CIF/4CIF 等标准尺寸，缩到能容纳的最大标准档
			outW, outH = standardH263Size(outW, outH)
		}
		e, err := video.NewEncoder(rc.Name, outW, outH, encFPS, bitrate)
		if err != nil {
			logx.Logger().Warn("创建视频编码器失败", "codec", rc.Name, "w", outW, "h", outH, "err", err)
			return false
		}
		enc, encCodec, encInW, encInH, encOutW, encOutH = e, rc.Name, inW, inH, outW, outH
		logx.Logger().Info("视频编码器已就绪",
			"codec", rc.Name, "输入", fmt.Sprintf("%dx%d", inW, inH),
			"编码", fmt.Sprintf("%dx%d", outW, outH), "fps", encFPS)
		go v.notifyState()
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-v.stop:
			return
		default:
		}
		yuv, err := cam.ReadFrame(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// 前几次用 Warn（默认日志级别 info 看不到 Debug，摄像头打不开时
			// 用户那边会表现为「本地和发送都没画面」却毫无线索），之后降到 Debug
			if readErrs++; readErrs <= 3 {
				logx.Logger().Warn("读取摄像头帧失败", "次数", readErrs, "err", err)
			} else {
				logx.Logger().Debug("读取摄像头帧失败", "err", err)
			}
			time.Sleep(20 * time.Millisecond) // 别把 CPU 打满
			continue
		}
		if len(yuv.Y) == 0 {
			continue // 驱动偶尔给空帧
		}
		if frames == 0 {
			logx.Logger().Info("摄像头首帧已到达",
				"尺寸", fmt.Sprintf("%dx%d", yuv.W, yuv.H))
		}
		frames++

		// 发送本地视频：需要 SDP 已协商出编码；没协商时只做本地回显
		if v.enabled.Load() && v.sendEnabled.Load() && !v.holdTx.Load() {
			if rc, ok := v.NegotiatedCodec(); ok {
				if enc == nil || encCodec != rc.Name || yuv.W != encInW || yuv.H != encInH {
					// 建失败就记住参数，别每帧重试刷屏（换编码/换尺寸才再试）
					key := fmt.Sprintf("%s/%dx%d", rc.Name, yuv.W, yuv.H)
					if key != encFailKey {
						if !rebuildEncoder(rc, yuv.W, yuv.H) {
							encFailKey = key
						}
					}
				}
				if enc != nil {
					// 需要缩放时（H263 标准尺寸）先缩放；缩放失败就丢这一帧
					src := yuv
					if encOutW != yuv.W || encOutH != yuv.H {
						scaled, serr := video.ScaleYUV(yuv, encOutW, encOutH)
						if serr != nil {
							logx.Logger().Debug("视频缩放失败", "err", serr)
							continue
						}
						src = scaled
					}
					// forceKey 只在实际编码时消费：没协商/建不出编码器时保留，
					// 等能编码时第一帧就出关键帧
					force := v.forceKey.Swap(false)
					data, _, err := enc.Encode(src, force)
					if err != nil {
						logx.Logger().Debug("视频编码失败", "err", err)
					} else {
						v.sendFrame(data)
					}
				}
			}
		}

		// 本地画面（需求 7）：用原始帧（未缩放），画面才是摄像头真实观感。
		// 与推送无关；关闭回显只影响界面显示，网页订阅了本地画面照样要投递
		//（见 localFramesWanted），禁用视频则两边都没有。
		if v.localFramesWanted() {
			if img, err := video.ToRGBA(yuv); err == nil {
				v.emit(true, img.RGBA())
			}
		}
	}
}

// ensureEncoderSize 处理「编码器要求标准图像尺寸」的情况（目前只有 H263-1998）。
// h263StandardSizes H.263 允许的图像尺寸（FFmpeg 的 h263p 编码器只接受这些值）。
var h263StandardSizes = [][2]int{
	{1408, 1152}, // 16CIF
	{704, 576},   // 4CIF
	{352, 288},   // CIF
	{176, 144},   // QCIF
	{128, 96},    // SQCIF
}

// standardH263Size 取**不大于**给定尺寸的最大 H.263 标准图像尺寸——
// 宁可缩小也不放大（放大既浪费码率又不提升清晰度）。
func standardH263Size(w, h int) (int, int) {
	for _, s := range h263StandardSizes {
		if s[0] <= w && s[1] <= h {
			return s[0], s[1]
		}
	}
	return 128, 96
}
