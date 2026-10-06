package media

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"

	"mti-sip-phone/internal/logx"
)

// supportedCodecs 支持的编解码器（按优先级排序：PCMA 优先，其次 PCMU，
// 然后 G.722（宽频），最后是可选编解码器（Opus））。双方能力交集取最前者。
// Opus 由构建标签 audio 提供（见 optionalCodecs，以后其他音频编解码库同此），
// 未启用的构建里它不存在，
// SDP 也就不会宣告。
//
// G.722 是纯 Go 实现、任何构建都有，排在 Opus 之前（REQ-MEDIA-011 的用户决策，
// 真机验证过互通）：对端同时提供 G.722 与 Opus 时选 G.722（7 kHz 宽频、64 kbps
// 固定码率），而对端有 G.711 时仍按老规矩走 G.711（行为与历史一致）。
var supportedCodecs = append([]Codec{
	NewG711Codec(false), // PCMA（优先）
	NewG711Codec(true),  // PCMU
	NewG722Codec(),      // G.722（宽频，排在 Opus 之前）
}, optionalCodecs...)

// OpusSupported 当前构建是否含 Opus 编解码器（黄金测试与排查用）。
func OpusSupported() bool { return len(optionalCodecs) > 0 }

// codecSummary 本构建支持的编解码器名列表（日志用）。
func codecSummary() string {
	names := make([]string, len(supportedCodecs))
	for i, c := range supportedCodecs {
		names[i] = c.Name()
	}
	return strings.Join(names, ",")
}

// plcCodec 可选接口：丢包隐藏（有状态编解码器实现，如 Opus；G.711 没有，
// 丢包时补静音）。
type plcCodec interface {
	// DecodePLC 按上一包状态外推 samples 个样本；无法隐藏时返回 nil。
	DecodePLC(samples int) []int16
}

// resetter 可选接口：复位编解码器内部状态（新通话 / 媒体流切换 /
// 编解码切换时调用）。G.711 无状态，不实现。
type resetter interface {
	Reset()
}

// resetCodec 复位有状态编解码器（见 resetter）。
func resetCodec(c Codec) {
	if r, ok := c.(resetter); ok {
		r.Reset()
	}
}

// applyNegotiatedCodec 协商切换编解码器的**唯一入口**（两个 SDP 入口共用）：
// 复位编解码器状态、更新 PCM 管线采样率，并在速率真的变了时重置播放缓冲、
// 清掉采集侧残留的半帧、通知音频层重开设备。
//
// "速率真的变了"才动手很重要：保持/取消保持这类 re-INVITE 会走同一条路径，
// 速率没变时不能清缓冲（否则每次保持都丢一段声音）。
func (e *Engine) applyNegotiatedCodec(c Codec) {
	resetCodec(c)
	rate := c.SampleRate()
	if rate <= 0 || rate == e.SampleRate() {
		return
	}
	e.sampleRate.Store(int32(rate))

	// 播放缓冲里是**旧速率**的样本，按新速率播放会变速变调：直接清空
	//（此刻对端多半也在换流/换编码，丢掉的是切换瞬间的几十毫秒）。
	e.playMu.Lock()
	e.ring.SetRate(rate)
	e.ring.Reset()
	e.playMu.Unlock()
	// 采集侧同理：不足一帧的残留样本是按旧速率采的，不能拼进新编码器的帧
	e.micMu.Lock()
	e.micBuf = e.micBuf[:0]
	e.micMu.Unlock()
	if e.probe != nil {
		e.probe.setRate(rate)
	}
	logx.Logger().Info("媒体采样率已切换", "rate", rate, "codec", c.Name())
	if fn := e.rateChange.Load(); fn != nil {
		(*fn)(rate)
	}
}

// Engine 一次通话的媒体引擎：RTP/RTCP 收发 + 播放缓冲。
// 与音频设备层解耦：音频采集回调调用 FeedMic（接受任意帧长，内部按
// 编解码器帧长累积发送），播放侧调用 ReadPlayback（按任意帧长读取）。
type Engine struct {
	codecMu sync.RWMutex
	codec   Codec
	// activePT 线上实际使用的负载类型号。**不是 codec.PayloadType()**：
	// 本端作为 answerer 时必须复用对端 offer 里的号（RFC 3264），否则
	// 动态负载类型（视频全部是动态 PT）会协商串味。音频 PCMA/PCMU 是
	// 静态 PT 8/0，两种取值恰好相同，故此前未暴露。
	activePT atomic.Uint32

	// sampleRate 当前协商编解码器要求的 PCM 管线采样率（Hz）。G.711/Opus 恒为
	// 8000（与历史行为一致），G.722 是 16000（REQ-MEDIA-011：协商到它时整条
	// 音频管线切到 16 kHz 宽频）。由 applyNegotiatedCodec 统一维护；音频线程、
	// 播放缓冲与诊断探针都无锁读它。
	sampleRate atomic.Int32
	// rateChange 采样率变更回调（audio.Manager 在 AttachEngine 时注入）：
	// 设备已按旧速率开流时，回调里排一次"关流重开"。
	rateChange atomic.Pointer[func(rate int)]

	// dtmfPT 对端 SDP 里 telephone-event 的负载类型号（0 = 对端没声明）。
	// 收到对端 DTMF 时按这个号识别——**发送**方向恒用本端声明的
	// telephoneEventPT，两者可以不同（同 activePT 的方向性）。
	dtmfPT atomic.Uint32
	// dtmfIn 收到对端 DTMF 时的回调（audio.Manager 在 AttachEngine 时注入）：
	// 对端发的是**事件包不是音频**，本机要自己按标准发声，否则用户什么都听不到。
	dtmfIn atomic.Pointer[func(digit string)]
	// lastDTMF 上一次处理的 (事件码<<32 | RTP时间戳)：事件包会冗余重传，
	// 同一个事件只在首次到达时通知一次。
	lastDTMF atomic.Uint64

	conn      *net.UDPConn
	rtcp      *rtcpSession
	remoteMu  sync.RWMutex
	remote    *net.UDPAddr
	localIP   string // 本机绑定地址（socket 用）
	sdpIP     string // SDP 宣告地址（NAT 场景与绑定地址不同；空则用 localIP）
	localPort int

	ssrc  uint32
	sndMu sync.Mutex
	seq   uint16
	ts    uint32

	muted     atomic.Bool
	holdTx    atomic.Bool // 因对端要求（re-INVITE sendonly/inactive）而停止发送
	micMuted  atomic.Bool // 麦克风静音（纯本地，无 SIP 信令，区别于保持的 muted）
	playMuted atomic.Bool // 扬声器静音（纯本地，停止播放但继续接收）
	keepalive atomic.Bool // 早期媒体阶段持续发送静音保活（NAT 打洞）

	// 音量控制（REQ-MEDIA-006）：采集侧增益与播放侧音量，纯本地。
	//
	// **存的是「百分比 − 100」的偏移量（-100…+100），不是 float32 的位模式**：
	// 零值必须等于 100%（单位增益）。若直接存 float 位模式，未设置过的引擎
	// 读回来是 +0.0 —— 也就是"静音"，会被误读成禁用。偏移量让零值天然等于
	// 100%，显式的 0% 则存 -100，两者不会混淆。
	micGainOff atomic.Int32 // 麦克风增益偏移（-100…+100 → 0–200%）
	playVolOff atomic.Int32 // 扬声器音量偏移（-100…0 → 0–100%）

	// 播放缓冲：样本粒度环形缓冲 + 序号跟踪
	playMu   sync.Mutex
	ring     *playRing
	recvSSRC uint32
	hasSeq   bool
	lastSeq  uint16
	extSeq   uint32 // 序号回绕计数（RTCP 报告用）

	// 采集累积：不足一帧的样本留待下次回调
	micMu  sync.Mutex
	micBuf []int16

	// lastTrimLog 播放缓冲截断日志限频（避免刷屏）
	lastTrimLog time.Time

	// probe 媒体诊断探针（SIP_PHONE_AUDIODUMP=1 时非 nil，见 debug_probe.go）
	probe *mediaProbe

	// crypto SDES-SRTP 状态（REQ-MEDIA-009）：本段音频的密钥、收发上下文与策略。
	// 零值 = 明文，与历史行为一致。视频是独立的一段（独立密钥），
	// 在 VideoEngine 上另有一份。
	crypto cryptoState

	stop chan struct{}
	wg   sync.WaitGroup

	// 收发统计（供日志与 RTCP 报告使用）
	rxPackets atomic.Uint64
	rxLost    atomic.Uint64
	txPackets atomic.Uint64
	txBytes   atomic.Uint64
	anyPkts   atomic.Uint64 // 到达 socket 的任意 UDP 包数（含非 RTP，排查用）
}

// SetSDPIP 设置 SDP 宣告地址（NAT 穿透场景：对端可达地址与本地绑定
// 地址不同，如注册回显的 received 地址或 STUN 公网地址）。
func (e *Engine) SetSDPIP(ip string) {
	e.sdpIP = ip
	logx.Logger().Debug("SDP 宣告地址已设置", "ip", ip)
}

// NewEngine 创建媒体引擎并绑定 RTP/RTCP 连续端口（RTCP = RTP + 1）。
// 初始编解码器为本地优先级最高的（PCMA），SDP 协商后切换。
func NewEngine(localIP string) (*Engine, error) {
	conn, rtcpConn, err := bindRTPRTCP(localIP)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		codec:     supportedCodecs[0],
		conn:      conn,
		localIP:   localIP,
		localPort: conn.LocalAddr().(*net.UDPAddr).Port,
		ssrc:      uint32(time.Now().UnixNano()),
		seq:       uint16(time.Now().UnixNano() >> 4),
		ring:      newPlayRing(8),
		stop:      make(chan struct{}),
	}
	e.activePT.Store(uint32(e.codec.PayloadType()))
	e.sampleRate.Store(int32(e.codec.SampleRate()))
	e.probe = newMediaProbe(e.localPort)
	e.rtcp = newRTCPSession(rtcpConn, e)
	logx.Logger().Debug("媒体引擎已创建", "codec", e.codec.Name(),
		"支持", codecSummary(), "localPort", e.localPort, "rtcpPort", e.localPort+1)
	if opusInitErr != nil {
		// 库已编进来但实例创建失败：本次通话不可能协商到 Opus，记下来备用
		logx.Logger().Warn("Opus 编解码器不可用", "err", opusInitErr)
	}
	return e, nil
}

// payloadType 线上使用的负载类型号（见 activePT 字段说明）。
func (e *Engine) payloadType() uint8 { return uint8(e.activePT.Load()) }

// SampleRate 当前 PCM 管线采样率（Hz）：协商前是本端首选编解码器的速率
//（PCMA = 8000），协商到 G.722 之后是 16000。音频设备、播放缓冲、诊断 WAV
// 都按它工作。
func (e *Engine) SampleRate() int { return int(e.sampleRate.Load()) }

// SetSampleRateListener 注册采样率变更回调（audio.Manager 在 AttachEngine 时
// 注入；nil 取消）。回调在**协商线程**上同步调用，实现方必须非阻塞
//（音频层的做法是排进自己的 worker 队列）。
func (e *Engine) SetSampleRateListener(fn func(rate int)) {
	if fn == nil {
		e.rateChange.Store(nil)
		return
	}
	e.rateChange.Store(&fn)
}

// PayloadType 线上使用的音频负载类型号（answer 必须复用它，RFC 3264）。
func (e *Engine) PayloadType() uint8 { return e.payloadType() }

// SetSRTPPolicy 设置媒体加密策略（REQ-MEDIA-009）。必须在生成 SDP 之前调用
//（呼出：生成 offer 之前；呼入：解析对端 offer 之前），零值 = 关闭。
func (e *Engine) SetSRTPPolicy(p SRTPPolicy) { e.crypto.setPolicy(p) }

// SRTPActive 本次通话的音频媒体是否已加密。
func (e *Engine) SRTPActive() bool { return e.crypto.active() }

// SRTPSuite 已启用的加密套件名（未加密时空串，供日志/界面展示）。
func (e *Engine) SRTPSuite() string { return e.crypto.suiteName() }

// bindRTPRTCP 绑定一对连续 UDP 端口（RTP 与 RTCP），音频与视频各绑一对。
//
// RFC 3550 约定 **RTP 用偶数端口、RTCP = RTP + 1**：服务端/网关普遍按这个
// 约定配对（奇数 RTP 会被判为不规范，个别实现直接不予互通），所以随机端口
// 命中奇数时重选，直到拿到偶数端口对。
func bindRTPRTCP(ip string) (*net.UDPConn, *net.UDPConn, error) {
	ipAddr := net.ParseIP(ip)
	for i := 0; i < 50; i++ {
		c1, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ipAddr, Port: 0})
		if err != nil {
			return nil, nil, err
		}
		port := c1.LocalAddr().(*net.UDPAddr).Port
		if port >= 65535 || port%2 != 0 {
			_ = c1.Close()
			continue // 奇数端口不合 RFC 3550 约定（>=65535 也无从再取 +1），重选
		}
		c2, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ipAddr, Port: port + 1})
		if err != nil {
			_ = c1.Close()
			continue // RTCP 端口被占用，重新选 RTP 端口
		}
		return c1, c2, nil
	}
	return nil, nil, fmt.Errorf("绑定 RTP/RTCP 端口对失败（RTP 需为偶数端口）")
}

// LocalPort 本地 RTP 端口（用于 SDP）。
func (e *Engine) LocalPort() int { return e.localPort }

// LocalIP 本机 IP（用于 SDP）。
func (e *Engine) LocalIP() string { return e.localIP }

// Codec 当前协商的编解码器。
func (e *Engine) Codec() Codec {
	e.codecMu.RLock()
	defer e.codecMu.RUnlock()
	return e.codec
}

// SetMuted 静音：停止发送本地音频（保持接收）。
func (e *Engine) SetMuted(muted bool) { e.muted.Store(muted) }

// SetMicMuted 麦克风静音：纯本地停止/恢复发送，不发 SIP 信令
//（通话面板麦克风开关用；区别于 Mute 的保持 re-INVITE）。
func (e *Engine) SetMicMuted(muted bool) { e.micMuted.Store(muted) }

// SetPlayMuted 扬声器静音：纯本地停止/恢复播放，接收侧不受影响。
func (e *Engine) SetPlayMuted(muted bool) { e.playMuted.Store(muted) }

// PlayMuted 扬声器是否静音（音频层据此决定要不要播"收到对端 DTMF"的按键音）。
func (e *Engine) PlayMuted() bool { return e.playMuted.Load() }

// SetMicGain 麦克风增益（纯本地，REQ-MEDIA-006）：1 = 原始电平，0–2
//（界面显示 0–200%），超出范围会被夹取。
// 0 与静音的差别只在于"发不发包"：界面把 0% 表达为 SetMicMuted(true)，
// 增益本身不必（也不该）用 0 表达禁用。
func (e *Engine) SetMicGain(gain float32) {
	e.micGainOff.Store(int32(clampPercent(gain*100)) - 100)
}

// MicGain 当前麦克风增益（1 = 原始电平）。
func (e *Engine) MicGain() float32 { return float32(e.micGainOff.Load()+100) / 100 }

// SetSpeakerVolume 扬声器音量（纯本地，REQ-MEDIA-006）：1 = 原始电平，0–1
//（界面显示 0–100%），超出范围会被夹取。
func (e *Engine) SetSpeakerVolume(vol float32) {
	off := int32(clampPercent(vol*100)) - 100
	if off > 0 {
		off = 0
	}
	e.playVolOff.Store(off)
}

// SpeakerVolume 当前扬声器音量（1 = 原始电平）。
func (e *Engine) SpeakerVolume() float32 { return float32(e.playVolOff.Load()+100) / 100 }

// clampPercent 把百分比夹取到 0–200（增益上限 200%）。
func clampPercent(p float32) int {
	v := int(p + 0.5) // 四舍五入，避免 0.999… 掉档
	if v < 0 {
		return 0
	}
	if v > 200 {
		return 200
	}
	return v
}

// SetRemoteHold 远端保持：对端 re-INVITE 声明只发不收（sendonly）或
// 完全停发（inactive）时，本端停止发送媒体。
func (e *Engine) SetRemoteHold(hold bool) { e.holdTx.Store(hold) }

// SetKeepalive 早期媒体阶段保活：持续发送静音帧（每 20ms）。
// 部分 NAT 设备对无回流流量的 UDP 映射超时极短——打洞后停止发送会
// 导致映射被清除、对端媒体包被丢弃（回铃音 rx=0 的根因）；持续
// 发送保活映射，接通后关闭恢复真实音频发送。
func (e *Engine) SetKeepalive(on bool) {
	e.keepalive.Store(on)
	if on {
		logx.Logger().Info("早期媒体保活已开启（持续发送静音）")
	}
}

// Start 启动媒体收发（在确定远端地址后调用）：
// RTP 接收循环 + RTCP 收发 + NAT 打洞。
func (e *Engine) Start() {
	// 编解码器实例在包级共享（supportedCodecs 是原型），新通话开始时必须
	// 复位其 C 状态——否则上一通话的解码器状态会串到本次（本应用限单通话，
	// 复位后即可安全复用）。
	resetCodec(e.Codec())
	e.wg.Add(1)
	go e.recvLoop()
	e.rtcp.start()
	e.probe.start(e) // 诊断探针（未启用时为 nil，零开销）
	// NAT 打洞：向对端 RTP 地址发送静音帧，让 NAT 建立端口映射。
	// 对称 NAT 下对端发来的媒体包（如回铃音）必须先有出向包放行，
	// 否则 rx=0（回铃音听不到——bug 修复）。
	e.punchHole()
}

// recvLoop 接收 RTP 包：解析 → 解码 → 按序号写入播放缓冲。
func (e *Engine) recvLoop() {
	defer e.wg.Done()
	buf := make([]byte, 2048)
	lastSeq := uint16(0)
	hasLast := false
	for {
		select {
		case <-e.stop:
			return
		default:
		}
		_ = e.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, raddr, err := e.conn.ReadFromUDP(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			select {
			case <-e.stop:
				return
			default:
				logx.Logger().Debug("RTP 读取错误", "err", err)
				return
			}
		}

		// 任意到达 socket 的 UDP 包都记录前 3 个（Info 级，排查用）：
		// 若对端发了媒体包但此处从未打印，说明包未到达本机网络层
		if c := e.anyPkts.Add(1); c <= 3 {
			preview := fmt.Sprintf("%x", buf[:min(n, 16)])
			logx.Logger().Info("引擎收到 UDP 包", "src", raddr.String(), "len", n, "preview", preview)
		}

		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			logx.Logger().Debug("RTP 包解析失败", "err", err)
			continue
		}
		// SDES-SRTP（REQ-MEDIA-009）：RTP 头部只认证不加密，所以先按密文解析出
		// 头部（定位 SSRC/序号），再解密整包并用明文重新解析 payload。
		// 解密失败（密钥不符 / 被改包 / 重放）一律丢弃——绝不能拿密文当音频解码。
		plain, ok := e.crypto.unprotectRTP(buf[:n], &pkt.Header)
		if !ok {
			continue
		}
		if e.crypto.active() {
			if err := pkt.Unmarshal(plain); err != nil {
				logx.Logger().Debug("SRTP 解密后解析失败", "err", err)
				continue
			}
		}

		e.rxPackets.Add(1)
		if e.probe != nil {
			e.probe.addPkt()
		}
		// 首个外来包日志（Info 级，排查用）：确认对端 RTP 是否
		// 到达本机——若从未打印则对端媒体包从未到达（网络/对方未发送）
		if e.rxPackets.Load() == 1 {
			logx.Logger().Info("收到首个 RTP 包", "remote", raddr.String(), "len", n, "pt", pkt.PayloadType, "ssrc", pkt.SSRC)
		}
		// 丢包统计：序号跳变
		if hasLast {
			gap := pkt.SequenceNumber - lastSeq - 1
			if gap > 0 && gap < 0x8000 {
				e.rxLost.Add(uint64(gap))
			}
		}
		lastSeq = pkt.SequenceNumber
		hasLast = true

		// 对端的 DTMF（RFC 4733 telephone-event）：这不是音频，是事件包，
		// 必须由本机**自己发声**（真机也是这么做的），否则用户听不到
		if pt := uint8(e.dtmfPT.Load()); pt != 0 && pkt.PayloadType == pt {
			e.handleDTMFEvent(pkt)
			continue
		}
		// 仅处理协商的音频负载类型（其余如 comfort-noise 一律忽略）
		e.codecMu.RLock()
		codec := e.codec
		e.codecMu.RUnlock()
		if pkt.PayloadType != e.payloadType() {
			continue
		}
		frame, err := codec.Decode(pkt.Payload)
		if err != nil {
			logx.Logger().Debug("音频解码失败", "err", err)
			continue
		}
		e.playPush(pkt.SSRC, pkt.SequenceNumber, frame)
	}
}

// playPush 将解码帧按 RTP 序号写入播放缓冲：
//   - 乱序/过期包丢弃（G.711 20ms 包乱序概率极低）；
//   - 序号小跳变（丢包）填替代帧——有 PLC 的编解码器（Opus）用 libopus
//     的丢包隐藏外推，其余（G.711）补静音。**都不能循环上一帧**：对端停发
//     期间（如回铃音停顿期）无限循环最后 20ms 音频会产生持续嗡鸣噪音
//     （"回铃音变成很大噪音"的根因）；
//   - SSRC 切换或序号大跳变（> 2 秒，如 183 早期媒体 → 200 新媒体流
//     重新编号）重置缓冲并复位编解码器状态，以新流为起点（否则旧序列阻塞
//     新语音，"接通后听不到对方声音"的根因）。
func (e *Engine) playPush(ssrc uint32, seq uint16, frame []int16) {
	e.playMu.Lock()
	defer e.playMu.Unlock()
	if !e.hasSeq {
		e.recvSSRC = ssrc
		e.lastSeq = seq
		e.hasSeq = true
		e.ring.Write(frame)
		return
	}
	gap := seq - e.lastSeq - 1
	if gap >= 0x8000 {
		// 乱序 / 过期包：丢弃
		return
	}
	if e.probe != nil {
		e.probe.writeRx(frame) // 诊断：真正进入播放缓冲的音频样本
	}
	// 序号回绕（65535 → 0）：扩展序号 +1（RTCP 报告用）
	if seq < e.lastSeq {
		e.extSeq++
	}
	// SSRC 切换或大跳变：重置缓冲
	if ssrc != e.recvSSRC || gap > 100 {
		if ssrc != e.recvSSRC {
			logx.Logger().Info("媒体流 SSRC 变化，重置播放缓冲", "oldSSRC", e.recvSSRC, "newSSRC", ssrc, "seq", seq)
		}
		e.recvSSRC = ssrc
		e.lastSeq = seq
		e.extSeq = 0
		e.ring.Reset()
		// 新流的解码状态必须从头开始（旧流的 C 状态会让首个解码帧出杂音）。
		// 解码器由本接收协程独占（Decode 在调用本函数之前、同一协程），
		// 此处复位不会与 Decode 并发。
		resetCodec(e.Codec())
		// SRTP 的接收序号/ROC 推断同样要清零：新流换了 SSRC 或序号大幅回退后，
		// 沿用旧状态会算出错误的 ROC，表现为「接通后解密全部失败、听不到声音」。
		e.crypto.resetRx()
		e.ring.Write(frame)
		return
	}
	// 丢包：先用 PLC 外推（有的话），其余补静音
	if gap > 0 {
		e.fillLoss(int(gap), len(frame))
	}
	e.lastSeq = seq
	if dropped := e.ring.Write(frame); dropped > 0 {
		// 缓冲超深被截断：说明读侧已经落后（丢包补静音、对端突发都会垫高深度）。
		// 限频打日志，这是排查"声音延迟越来越大"的第一手线索。
		if time.Since(e.lastTrimLog) > 10*time.Second {
			e.lastTrimLog = time.Now()
			logx.Logger().Warn("播放缓冲超深已截断（声音延迟被压回目标值）",
				"丢弃样本", dropped, "当前深度ms", e.ring.Depth()*1000/e.SampleRate(),
				"累计丢弃", e.ring.Trimmed(),
				"切点能量比", fmt.Sprintf("%.2f", e.ring.CutRatio()))
		}
	}
}

// plcMaxFrames 丢包隐藏最多外推的帧数（5 × 20ms = 100ms）。
// 再多就不像话了：连续丢包超过 100ms 时硬撑出来的"语音"比静音更糟。
const plcMaxFrames = 5

// fillLoss 丢包填充：n 个丢失帧、每帧 samples 个样本。
// 有 PLC 的编解码器（Opus）先外推若干帧，其余一律补静音；G.711 没有 PLC，
// 行为与历史完全一致（整段静音）。
// 调用方持有 playMu；编解码器状态由本接收协程独占。
func (e *Engine) fillLoss(n, samples int) {
	if samples <= 0 || n <= 0 {
		return
	}
	plc, _ := e.Codec().(plcCodec)
	used := 0
	if plc != nil {
		for ; used < n && used < plcMaxFrames; used++ {
			f := plc.DecodePLC(samples)
			if f == nil {
				break
			}
			if e.probe != nil {
				e.probe.writeFill(len(f))
			}
			e.ring.Write(f)
		}
	}
	if rest := n - used; rest > 0 {
		if e.probe != nil {
			e.probe.writeFill(rest * samples)
		}
		e.ring.Write(make([]int16, rest*samples))
	}
}

// FeedMic 音频采集回调：接受任意帧长输入（音频回调帧长不一定等于
// 20ms），按编解码器帧长（20ms = FrameSize：G.711/Opus 160、G.722 320）
// 累积，凑满一帧编码发送，
// 不足一帧的部分留待下次回调。
func (e *Engine) FeedMic(pcm []int16) {
	e.micMu.Lock()
	defer e.micMu.Unlock()
	if !e.hasRemote() {
		return
	}
	e.codecMu.RLock()
	codec := e.codec
	e.codecMu.RUnlock()

	// 早期媒体保活：丢弃麦克风数据，仅发静音帧维持 NAT 映射
	if e.keepalive.Load() {
		e.micBuf = e.micBuf[:0]
		payload, err := codec.Encode(make([]int16, codec.FrameSize()))
		if err == nil {
			e.sendPacket(codec, payload)
		}
		return
	}
	// 静音 / 麦克风静音 / 对端保持：丢弃麦克风数据
	if e.muted.Load() || e.micMuted.Load() || e.holdTx.Load() {
		e.micBuf = e.micBuf[:0]
		return
	}
	// 累积并按帧长发送。麦克风增益只作用在**本次新追加的那段**上（既有余量
	// 早已缩放过了），也**不改调用方传入的缓冲**。
	start := len(e.micBuf)
	e.micBuf = append(e.micBuf, pcm...)
	applyGain(e.micBuf[start:], e.MicGain())
	if e.probe != nil {
		// 诊断：落盘"实际送编码器的采集样本"（含增益），与 rx/play 两份 WAV 一起
		// 就能看出增益有没有生效、是不是已经削波
		e.probe.writeMic(e.micBuf[start:])
	}
	fs := codec.FrameSize()
	for len(e.micBuf) >= fs {
		payload, err := codec.Encode(e.micBuf[:fs])
		if err != nil {
			logx.Logger().Error("音频编码失败", "err", err)
			e.micBuf = e.micBuf[:0]
			break
		}
		e.sendPacket(codec, payload)
		e.micBuf = e.micBuf[fs:]
	}
}

// applyGain 就地把样本乘以增益 g，并削波到 int16 范围（REQ-MEDIA-006：
// 麦克风增益可到 200%，放大后必须削波，否则 int16 回绕会把响音变成爆音）。
// g == 1 直接返回——默认路径零开销。
func applyGain(buf []int16, g float32) {
	if g == 1 || len(buf) == 0 {
		return
	}
	for i, s := range buf {
		v := float32(s) * g
		switch {
		case v > 32767:
			buf[i] = 32767
		case v < -32768:
			buf[i] = -32768
		default:
			buf[i] = int16(v)
		}
	}
}

// punchHole NAT 打洞：向对端发送一帧静音，让 NAT 建立端口映射
//（对称 NAT 下对端媒体包必须先有出向包放行）。
func (e *Engine) punchHole() {
	e.codecMu.RLock()
	codec := e.codec
	e.codecMu.RUnlock()
	payload, err := codec.Encode(make([]int16, codec.FrameSize()))
	if err != nil {
		return
	}
	e.sendPacket(codec, payload)
	logx.Logger().Debug("NAT 打洞已发送")
}

// sendPacket 发送一个 RTP 包（编码后的负载）。sndMu 保护序号/时间戳
// 的递增与发送顺序（采集回调、DTMF、保活可能并发调用）。
func (e *Engine) sendPacket(codec Codec, payload []byte) {
	e.sndMu.Lock()
	defer e.sndMu.Unlock()
	if !e.hasRemote() {
		return
	}
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    e.payloadType(),
			SequenceNumber: e.seq,
			Timestamp:      e.ts,
			SSRC:           e.ssrc,
		},
		Payload: payload,
	}
	data, err := pkt.Marshal()
	if err != nil {
		return
	}
	// SDES-SRTP：加密启用后**所有**出向包都必须是密文——打洞、早期媒体保活、
	// 麦克风音频走的都是这里。漏一个明文包，对端会按认证失败丢弃甚至直接挂断。
	if data = e.crypto.protectRTP(data, &pkt.Header); data == nil {
		return
	}
	if _, err := e.conn.WriteToUDP(data, e.remote); err != nil {
		logx.Logger().Debug("RTP 发送失败", "err", err)
		return
	}
	e.seq++
	// 时间戳按 **RTP 时钟率** 递增，不是 PCM 帧长：Opus 的 PCM 帧长是 160
	//（8 kHz 管线）而时钟是 48000，每帧要走 960；G.722 反过来：PCM 帧长 320 而
	// 时钟是 8000，每帧只走 160（见 Codec.TimestampStep）。
	e.ts += uint32(codec.TimestampStep())
	e.txPackets.Add(1)
	e.txBytes.Add(uint64(len(payload)))
}

// hasRemote 是否已确定对端地址。
func (e *Engine) hasRemote() bool {
	e.remoteMu.RLock()
	defer e.remoteMu.RUnlock()
	return e.remote != nil
}

// setRemote 更新对端 RTP 地址，并同步 RTCP 地址。
// rtcpPort 为对端 SDP 声明的 RTCP 端口，0 表示未声明（惯例 = RTP 端口 + 1）。
func (e *Engine) setRemote(addr *net.UDPAddr, rtcpPort int) {
	e.remoteMu.Lock()
	e.remote = addr
	e.remoteMu.Unlock()
	e.rtcp.setRemote(addr, rtcpPort)
}

// ReadPlayback 播放侧回调：从播放缓冲取样本，数据不足时补静音。
// 接受任意长度（音频回调帧长不一定等于 20ms），由音频设备层周期调用。
// 扬声器静音时输出全零，但仍消费缓冲——取消静音后从当前流位置继续，
// 不会回放静音期间的旧数据。
// 扬声器音量（REQ-MEDIA-006）在这里生效：诊断探针随后记录的就是
// "设备实际拿到的样本"（含音量）。
func (e *Engine) ReadPlayback(buf []int16) int {
	e.playMu.Lock()
	n := e.ring.Read(buf)
	e.playMu.Unlock()
	if e.playMuted.Load() {
		n = 0
	} else {
		applyGain(buf[:n], e.SpeakerVolume())
	}
	for i := n; i < len(buf); i++ {
		buf[i] = 0
	}
	if e.probe != nil {
		e.probe.addRead(len(buf), n)
		e.probe.writePlay(buf) // 诊断：设备实际拿到的样本（含补的静音）
	}
	return len(buf)
}

// playState 播放序列快照（RTCP 报告用）。
func (e *Engine) playState() (lastSeq uint16, extSeq uint32) {
	e.playMu.Lock()
	defer e.playMu.Unlock()
	return e.lastSeq, e.extSeq
}

// rtpTimestamp 当前发送时间戳快照（RTCP SR 用）。
func (e *Engine) rtpTimestamp() uint32 {
	e.sndMu.Lock()
	defer e.sndMu.Unlock()
	return e.ts
}

// --- rtcpEndpoint 实现（音频）---

func (e *Engine) rtcpSSRC() uint32  { return e.ssrc }
func (e *Engine) rtcpRTPTime() uint32 { return e.rtpTimestamp() }

func (e *Engine) rtcpTxStats() (packets, bytes uint64) {
	return e.txPackets.Load(), e.txBytes.Load()
}

func (e *Engine) rtcpRecvState() (uint16, uint32, uint64, bool) {
	e.playMu.Lock()
	has, lastSeq, extSeq := e.hasSeq, e.lastSeq, e.extSeq
	e.playMu.Unlock()
	return lastSeq, extSeq, e.rxLost.Load(), has
}

// rtcpOnFeedback 音频不处理 PLI/FIR（仅视频有意义）。
func (e *Engine) rtcpOnFeedback(rtcp.Packet) {}

// RTCP 加解密（SRTP，REQ-MEDIA-009）。
func (e *Engine) rtcpProtect(pkt []byte) []byte            { return protectRTCPPkt(&e.crypto, pkt) }
func (e *Engine) rtcpUnprotect(pkt []byte) ([]byte, bool)  { return unprotectRTCPPkt(&e.crypto, pkt) }

// SendDTMF 发送一个 DTMF 音（RFC 2833 telephone-event，负载类型 101）。
// 异步发送：起始包（M=1）→ 持续包 → 结束包（E=1），总时长约 200ms。
func (e *Engine) SendDTMF(digit string) {
	code, ok := dtmfEventCode(digit)
	if !ok {
		return
	}
	go func() {
		for i := 0; i < 3; i++ {
			if !e.hasRemote() {
				return
			}
			e.sndMu.Lock()
			eol := byte(0x80) // E 位（结束事件）
			if i != 2 {
				eol = 0
			}
			duration := uint16(800 * i) // 单位 1/8000 s，每包间隔 100ms
			payload := []byte{
				code,
				0x0A | eol, // 音量 10（6bit）+ E 位
				byte(duration >> 8),
				byte(duration),
			}
			pkt := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    telephoneEventPT,
					SequenceNumber: e.seq,
					Timestamp:      e.ts,
					SSRC:           e.ssrc,
					Marker:         i == 0, // 起始包置 M
				},
				Payload: payload,
			}
			e.seq++
			data, err := pkt.Marshal()
			if err != nil {
				e.sndMu.Unlock()
				return
			}
			// SDES-SRTP：DTMF 也是 RTP 包，同样必须加密（它自己不走 sendPacket）
			if data = e.crypto.protectRTP(data, &pkt.Header); data == nil {
				e.sndMu.Unlock()
				return
			}
			if _, err := e.conn.WriteToUDP(data, e.remote); err != nil {
				logx.Logger().Debug("DTMF 包发送失败", "err", err)
				e.sndMu.Unlock()
				return
			}
			e.sndMu.Unlock()
			time.Sleep(100 * time.Millisecond)
		}
		logx.Logger().Debug("DTMF 已发送", "digit", digit)
	}()
}

// SetDTMFListener 注册"收到对端 DTMF"的回调（本机据此播放按键音）。
// 由音频层在 AttachEngine 时注入；nil 表示不发声。
func (e *Engine) SetDTMFListener(fn func(digit string)) {
	if fn == nil {
		e.dtmfIn.Store(nil)
		return
	}
	e.dtmfIn.Store(&fn)
}

// handleDTMFEvent 处理收到的 telephone-event 包：解出事件码、去重（同一事件
// 的冗余包只通知一次），再回调给音频层发声。
func (e *Engine) handleDTMFEvent(pkt rtp.Packet) {
	if len(pkt.Payload) < 1 {
		return
	}
	digit, ok := dtmfDigit(pkt.Payload[0])
	if !ok {
		return
	}
	key := uint64(pkt.Payload[0])<<32 | uint64(pkt.Timestamp)
	if e.lastDTMF.Swap(key) == key {
		return // 冗余重传的同一个事件
	}
	if fn := e.dtmfIn.Load(); fn != nil {
		logx.Logger().Debug("收到对端 DTMF", "digit", digit, "ts", pkt.Timestamp)
		(*fn)(digit)
	}
}

// dtmfDigit 把 RFC 4733 事件码映射回拨号字符（0-9 → 0-9，10 → *，11 → #，12-15 → A-D）。
func dtmfDigit(code byte) (string, bool) {
	switch {
	case code <= 9:
		return string(rune('0' + code)), true
	case code == 10:
		return "*", true
	case code == 11:
		return "#", true
	case code >= 12 && code <= 15:
		return string(rune('A' + code - 12)), true
	}
	return "", false
}

// dtmfEventCode 将拨号字符映射为 RFC 4733 事件码。
func dtmfEventCode(digit string) (byte, bool) {
	if len(digit) == 1 && digit[0] >= '0' && digit[0] <= '9' {
		return digit[0] - '0', true
	}
	switch digit {
	case "*":
		return 10, true
	case "#":
		return 11, true
	}
	return 0, false
}

// telephoneEventPT RFC 2833 事件负载类型（与 SDP 中的 101 一致）。
const telephoneEventPT = 101

// Stats 收发包统计快照。
func (e *Engine) Stats() (rx, lost, tx uint64) {
	return e.rxPackets.Load(), e.rxLost.Load(), e.txPackets.Load()
}

// Close 释放引擎：发 RTCP BYE、停收发循环、关端口。
func (e *Engine) Close() {
	e.probe.close()
	e.rtcp.sendBye()
	select {
	case <-e.stop:
	default:
		close(e.stop)
	}
	e.wg.Wait()
	e.rtcp.stop()
	if e.conn != nil {
		_ = e.conn.Close()
	}
	logx.Logger().Debug("媒体引擎已关闭", "rx", e.rxPackets.Load(), "lost", e.rxLost.Load(), "tx", e.txPackets.Load())
}

// ---------------------------------------------------------------------------
// SDP 协商
// ---------------------------------------------------------------------------

// codecPT 「编解码器 + 线上负载类型号」对。
// 必须成对传递：按 PT 号反查编码名只在本端 offer 的号段里成立，
// answer 用的是**对端**的号（Opus 等动态 PT 下会查错编码名）。
type codecPT struct {
	c  Codec
	pt uint8
}

// allCodecItems 本端支持的全部音频编解码器（各自的固定本端号，按优先级）。
func allCodecItems() []codecPT {
	items := make([]codecPT, len(supportedCodecs))
	for i, c := range supportedCodecs {
		items[i] = codecPT{c: c, pt: c.PayloadType()}
	}
	return items
}

// negotiatedItem 当前协商结果（编解码器 + 线上号）。
// 协商路径（SDPAnswerWithDirection / SDPApplyAnswer）已把两者一起写入，
// 这里直接取，不去按号反查。
func (e *Engine) negotiatedItem() codecPT {
	return codecPT{c: e.Codec(), pt: e.payloadType()}
}

// itemForPT 按线上 PT 取协商结果，供 SDPAnswerFor 使用（调用方传的是对端号）。
// 正常情况下与 activePT 一致；不一致说明调用顺序错了（协商还没发生），
// 按本端静态号兜底并告警——Opus 是动态号，兜底会退化成 PCMA。
func (e *Engine) itemForPT(pt uint8) codecPT {
	if pt == e.payloadType() {
		return codecPT{c: e.Codec(), pt: pt}
	}
	logx.Logger().Warn("SDP answer 的音频 PT 与协商结果不一致（按静态号兜底）",
		"pt", pt, "activePT", e.payloadType())
	return codecPT{c: codecByPT(pt), pt: pt}
}

// SDPOffer 生成本地 SDP offer（PCMA/PCMU/Opus + telephone-event，按优先级排序）。
func (e *Engine) SDPOffer() string {
	return e.sdp("sendrecv", allCodecItems(), true)
}

// SDPOfferWithDirection 生成指定媒体方向的 SDP offer
//（保持 re-INVITE 用：保持 sendonly / 恢复 sendrecv）。
// re-offer 必须**重复带上 a=crypto**：SDES 的口径是"重协商里不再出现 crypto
// 就表示不再加密"，漏掉会让对端以为本端取消了加密（密钥沿用本通首次生成的）。
func (e *Engine) SDPOfferWithDirection(dir string) string {
	return e.sdp(dir, allCodecItems(), true)
}

// SDPNegotiatedAnswer 呼入接听时的 200 answer：仅含协商后的编解码器
//（RFC 3264 answer 只列双方协商结果）。
// 用 activePT（对端号）而非 codec.PayloadType()（本端号）——音频静态 PT
// 两者相同，动态 PT（Opus）下必须用对端号。
func (e *Engine) SDPNegotiatedAnswer() string {
	return e.sdp("sendrecv", []codecPT{e.negotiatedItem()}, false)
}

// SDPAnswer 解析远端 offer，协商编解码并返回本地 answer。
// 成功时 Engine 已切换到协商的编解码器并设置远端 RTP 地址。
func (e *Engine) SDPAnswer(offer []byte) (string, error) {
	return e.SDPAnswerWithDirection(offer, "sendrecv")
}

// SDPAnswerWithDirection 解析远端 offer 并返回指定方向的 answer
//（对端 re-INVITE 用：方向镜像，如对端 recvonly → 本端 sendonly）。
func (e *Engine) SDPAnswerWithDirection(offer []byte, dir string) (string, error) {
	remote, codec, peerPT, rtcpPort, err := parseSDPOffer(offer)
	if err != nil {
		return "", err
	}
	e.codecMu.Lock()
	e.codec = codec
	e.codecMu.Unlock()
	// answerer：线上用**对端**的负载类型号（RFC 3264）
	e.activePT.Store(uint32(peerPT))
	e.dtmfPT.Store(uint32(peerDTMFPT(offer))) // 对端 DTMF 用他们自己声明的号
	// 编解码器换了（对端号 + 新编码）：复位其内部状态、按需切换管线采样率
	e.applyNegotiatedCodec(codec)
	// SDES-SRTP（REQ-MEDIA-009）：必须在 setRemote/punchHole **之前**协商好——
	// punchHole 会立刻发一个包，加密启用后那个包必须是密文（对端会丢弃甚至挂断
	// 认证失败的明文包）。协商失败且策略要求加密时直接返回错误，由上层拒接。
	if err := e.applySRTPOffer(offer); err != nil {
		return "", err
	}
	e.setRemote(remote, rtcpPort)
	// 远端地址更新后打洞（NAT 下对端媒体包需先有出向包放行）
	e.punchHole()
	return e.sdp(dir, []codecPT{{c: codec, pt: peerPT}}, false), nil
}

// applySRTPOffer 解析 offer 里的音频段 a=crypto 并建立加密上下文（本端被叫）。
func (e *Engine) applySRTPOffer(offer []byte) error {
	info, err := parseMediaInfo(offer, "audio")
	if err != nil {
		// 解析失败不影响媒体协商本身（parseSDPOffer 已经成功过一次）
		return nil
	}
	return e.crypto.applyRemoteOffer(info.Crypto)
}

// applySRTPAnswer 解析 answer 里的音频段 a=crypto 并建立加密上下文（本端主叫）。
func (e *Engine) applySRTPAnswer(answer []byte) error {
	info, err := parseMediaInfo(answer, "audio")
	if err != nil {
		return nil
	}
	return e.crypto.applyRemoteAnswer(info.Crypto)
}

// SDPApplyAnswer 处理远端对本地 offer 的应答：提取远端 RTP 地址并切换编解码。
func (e *Engine) SDPApplyAnswer(answer []byte) error {
	remote, codec, pt, rtcpPort, err := parseSDPAnswer(answer, e.localPort)
	if err != nil {
		return err
	}
	e.codecMu.Lock()
	e.codec = codec
	e.codecMu.Unlock()
	// offerer：answer 里的号取自本端 offer，即为本端号
	e.activePT.Store(uint32(pt))
	e.dtmfPT.Store(uint32(peerDTMFPT(answer)))
	// 编解码器换了（183 早期媒体 → 200 可能换编码，比如 G.711 → G.722）：
	// 复位内部状态、按需切换管线采样率（变了会让音频层重开设备）
	e.applyNegotiatedCodec(codec)
	// SDES-SRTP：answer 里按 tag 匹配本端 offer 的密钥；策略要求加密而
	// 对端没接受时返回错误（由上层把本次呼叫判失败，见 ErrSRTPRequired）
	if err := e.applySRTPAnswer(answer); err != nil {
		return err
	}
	e.setRemote(remote, rtcpPort)
	// 远端地址更新（如 183 → 200 端口变化）后重新打洞
	e.punchHole()
	logx.Logger().Debug("SDP 协商完成", "codec", codec.Name(), "remote", remote.String())
	return nil
}

// sdpIPForSDP 取 SDP 宣告地址（NAT 场景为对端可达地址，未设置时为本机地址）。
func (e *Engine) sdpIPForSDP() string {
	if e.sdpIP != "" {
		return e.sdpIP
	}
	return e.localIP
}

// audioSection m=audio 段（不含会话头）。
// items 按优先级顺序（编解码器与线上号成对，见 codecPT）；telephone-event（101）恒在末尾。
// asOffer 决定 a=crypto 的来源：offer 用本端候选（策略允许时生成），
// answer 只在被叫协商成功后才写（RFC 4568：answer 里的 crypto 是答复，不能凭空加）。
func (e *Engine) audioSection(dir string, items []codecPT, asOffer bool) sdpSection {
	codecs := make([]sdpCodec, 0, len(items)+1)
	for _, it := range items {
		// 注意用 Name()（"PCMA" / "opus"）而不是 RTPMap()（"PCMA/8000"）——
		// buildSDP 会自己拼 "/<ClockRate>[/<Channels>]"，用 RTPMap 会输出
		// "a=rtpmap:8 PCMA/8000/8000" 这种非法行。
		// 时钟率与声道数取自编解码器本身：Opus 是 48000 且要写 "/2"（RFC 7587），
		// 不能像原来那样写死 8000。
		codecs = append(codecs, sdpCodec{
			PT:        it.pt,
			Encoding:  it.c.Name(),
			ClockRate: it.c.ClockRate(),
			Channels:  it.c.Channels(),
		})
	}
	codecs = append(codecs, sdpCodec{PT: telephoneEventPT, Encoding: "telephone-event", ClockRate: 8000, FMTP: "0-16"})
	var crypto []string
	if asOffer {
		crypto = e.crypto.offerLines()
	} else {
		crypto = e.crypto.answerLines()
	}
	return sdpSection{
		Kind:     "audio",
		Port:     e.localPort,
		RTCPPort: e.localPort + 1,
		Codecs:   codecs,
		Crypto:   crypto,
		Dir:      dir,
	}
}

// sdp 构造纯音频 SDP 文本（会话头 + m=audio）。asOffer 见 audioSection。
func (e *Engine) sdp(dir string, items []codecPT, asOffer bool) string {
	return buildSDP(e.ssrc, e.sdpIPForSDP(), []sdpSection{e.audioSection(dir, items, asOffer)})
}

// SDPOfferFor 构造呼出 / 重协商用的 offer：会话头 + m=audio + 可选 m=video。
// videoDir 为空时按本端能力推导（能发 → sendrecv，只能收 → recvonly）；
// v 为 nil 或本构建无视频能力时不输出 m=video。
func SDPOfferFor(audio *Engine, v *VideoEngine, audioDir, videoDir string) string {
	sections := []sdpSection{audio.audioSection(audioDir, allCodecItems(), true)}
	if v != nil {
		if s := v.offerSection(videoDir); s != nil {
			sections = append(sections, *s)
		}
	}
	return buildSDP(audio.ssrc, audio.sdpIPForSDP(), sections)
}

// SDPAnswerFor 构造 200 OK 的 answer，**严格跟随对端 offer 的媒体顺序与数量**
// （RFC 3264 §6）：对端 offer 是 video,audio 时 answer 也必须 video 在前，
// 否则严格的对端（FreeSWITCH 部分版本）会回 488。
//
// audioPT 为对端 offer 中音频协商到的 PT 号（answer 必须复用对端号）。
// videoDir 为空或 v 为 nil（含本构建无视频能力、摄像头被禁用且只收不发等）
// 时，若对端 offer 含 m=video 则输出 m=video 0 拒绝段——**不能省略该行**。
func SDPAnswerFor(audio *Engine, v *VideoEngine, offer *Offer, audioPT uint8, videoDir string) string {
	var sections []sdpSection
	order := []string{"audio", "video"}
	if offer != nil && len(offer.Order()) > 0 {
		order = offer.Order()
	}
	for _, kind := range order {
		switch kind {
		case "audio":
			sections = append(sections, audio.audioSection("sendrecv", []codecPT{audio.itemForPT(audioPT)}, false))
		case "video":
			var sec *sdpSection
			if v != nil {
				// answer 只列协商到的那一个编码，且用对端 PT（见 answerSection 注释）
				sec = v.answerSection(videoDir)
			}
			if sec == nil {
				// 不接受视频：回拒绝段（带对端的负载类型列表）
				info, err := offer.Media("video")
				if err != nil {
					continue
				}
				sections = append(sections, sdpSection{Raw: RejectedSection(info)})
			} else {
				sections = append(sections, *sec)
			}
		}
	}
	if len(sections) == 0 {
		sections = append(sections, audio.audioSection("sendrecv", []codecPT{audio.itemForPT(audioPT)}, false))
	}
	return buildSDP(audio.ssrc, audio.sdpIPForSDP(), sections)
}

// parseSDPOffer 解析 offer：提取远端 RTP 地址、**对端 offer 中的负载类型号**
// 与对端声明的 RTCP 端口（0 = 未声明）。
// 编码选择规则：双方能力交集按本地优先级（PCMA > PCMU）。
//
// 返回值第二项是**对端的** PT 号——本端作为 answerer 必须复用该号
// （RFC 3264）。音频静态 PT 下它与本地号相同，动态 PT 下不同。
func parseSDPOffer(offer []byte) (*net.UDPAddr, Codec, uint8, int, error) {
	sd := &sdp.SessionDescription{}
	if err := sd.Unmarshal(offer); err != nil {
		return nil, nil, 0, 0, fmt.Errorf("SDP 解析失败: %w", err)
	}
	return negotiateMedia(sd, "audio")
}

// parseSDPAnswer 解析 answer（对端响应）。
// answer 中的 PT 号必然取自本端 offer，故直接按号取编解码器。
func parseSDPAnswer(answer []byte, localPort int) (*net.UDPAddr, Codec, uint8, int, error) {
	sd := &sdp.SessionDescription{}
	if err := sd.Unmarshal(answer); err != nil {
		return nil, nil, 0, 0, fmt.Errorf("SDP 解析失败: %w", err)
	}
	remote, codec, pt, rtcpPort, err := negotiateMedia(sd, "audio")
	if err != nil {
		return nil, nil, 0, 0, err
	}
	// answer 中若 m= 端口为 0 表示拒绝该媒体
	if remote.Port == 0 {
		return nil, nil, 0, 0, fmt.Errorf("对端拒绝了音频媒体")
	}
	return remote, codec, pt, rtcpPort, nil
}

// negotiateMedia 从 SDP 中协商指定媒体类型：返回远端 RTP 地址、本地编解码器、
// **对端 PT 号**（answer 复用）与对端 RTCP 端口。
//
// 媒体定位按类型（audio/video）而非 MediaDescriptions[0]：视频加入后
// 第一段未必是音频（部分对端 offer 把 video 排在前面）。
// 编解码匹配按 rtpmap 编码名（PCMA/PCMU），兼顾无 rtpmap 的纯静态号码对端。
func negotiateMedia(sd *sdp.SessionDescription, kind string) (*net.UDPAddr, Codec, uint8, int, error) {
	info, err := mediaInfoOf(sd, kind)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	if info.Rejected {
		return nil, nil, 0, 0, fmt.Errorf("对端拒绝了%s媒体", mediaKindName(kind))
	}
	if info.Addr == nil {
		return nil, nil, 0, 0, fmt.Errorf("SDP 缺少%s的连接信息", mediaKindName(kind))
	}

	codec, pt, ok := matchAudioCodec(info)
	if !ok {
		return nil, nil, 0, 0, fmt.Errorf("无共同编解码器（对端支持: %s）", ptListString(info.PTs))
	}
	return info.Addr, codec, pt, info.RTCPPort, nil
}

// matchAudioCodec 按本地优先级在 offer 的负载类型中挑音频编解码器
//（PCMA > PCMU > opus，opus 是动态 PT，只在对方也支持时才选中）。
// 先按 rtpmap 名匹配，再回退到静态负载类型号（对端未写 a=rtpmap 的旧式实现）。
func matchAudioCodec(info *MediaInfo) (Codec, uint8, bool) {
	for _, c := range supportedCodecs {
		for _, pt := range info.PTs {
			if info.EncodingMatches(pt, c.Name()) {
				return c, pt, true
			}
		}
	}
	// 回退：静态负载类型号直接命中。
	// 只认 0–95（静态号段）：Opus 的本端号 96 是动态号，对端用 96 号却不写
	// a=rtpmap 时不能当成 Opus（那是它自己的动态号，含义未知）。
	for _, c := range supportedCodecs {
		for _, pt := range info.PTs {
			if pt <= 95 && pt == c.PayloadType() && info.Rtpmap[pt] == "" {
				return c, pt, true
			}
		}
	}
	return nil, 0, false
}

// mediaKindName 媒体类型的中文名（日志与错误信息用）。
func mediaKindName(kind string) string {
	if kind == "video" {
		return "视频"
	}
	return "音频"
}

// ptListString 负载类型列表转字符串（错误信息用）。
func ptListString(pts []uint8) string {
	parts := make([]string, len(pts))
	for i, pt := range pts {
		parts[i] = strconv.Itoa(int(pt))
	}
	return strings.Join(parts, ",")
}

// codecByPT 按负载类型取编解码器（协商失败兜底 PCMA）。
func codecByPT(pt uint8) Codec {
	for _, c := range supportedCodecs {
		if c.PayloadType() == pt {
			return c
		}
	}
	return supportedCodecs[0]
}
