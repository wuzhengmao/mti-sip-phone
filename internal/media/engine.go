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

// 支持的编解码器（按优先级排序：PCMA 优先，其次 PCMU；双方能力交集取最前者）
var supportedCodecs = []Codec{
	NewG711Codec(false), // PCMA（优先）
	NewG711Codec(true),  // PCMU
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
	e.rtcp = newRTCPSession(rtcpConn, e)
	logx.Logger().Debug("媒体引擎已创建", "codec", e.codec.Name(), "localPort", e.localPort, "rtcpPort", e.localPort+1)
	return e, nil
}

// payloadType 线上使用的负载类型号（见 activePT 字段说明）。
func (e *Engine) payloadType() uint8 { return uint8(e.activePT.Load()) }

// PayloadType 线上使用的音频负载类型号（answer 必须复用它，RFC 3264）。
func (e *Engine) PayloadType() uint8 { return e.payloadType() }

// bindRTPRTCP 绑定一对连续 UDP 端口（RTP 与 RTCP）。
func bindRTPRTCP(ip string) (*net.UDPConn, *net.UDPConn, error) {
	ipAddr := net.ParseIP(ip)
	for i := 0; i < 50; i++ {
		c1, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ipAddr, Port: 0})
		if err != nil {
			return nil, nil, err
		}
		port := c1.LocalAddr().(*net.UDPAddr).Port
		if port >= 65535 {
			_ = c1.Close()
			continue
		}
		c2, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ipAddr, Port: port + 1})
		if err != nil {
			_ = c1.Close()
			continue // RTCP 端口被占用，重新选 RTP 端口
		}
		return c1, c2, nil
	}
	return nil, nil, fmt.Errorf("绑定 RTP/RTCP 连续端口失败")
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
	e.wg.Add(1)
	go e.recvLoop()
	e.rtcp.start()
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

		e.rxPackets.Add(1)
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

		// 仅处理协商的负载类型；telephone-event（DTMF）后续版本支持
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
//   - 序号小跳变（丢包）填静音帧——PLC 采用静音而非循环上一帧，
//     避免对端停发期间（如回铃音停顿期）无限循环最后 20ms 音频
//     产生持续嗡鸣噪音（"回铃音变成很大噪音"的根因）；
//   - SSRC 切换或序号大跳变（> 2 秒，如 183 早期媒体 → 200 新媒体流
//     重新编号）重置缓冲，以新流为起点（否则旧序列阻塞新语音，
//     "接通后听不到对方声音"的根因）。
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
		e.ring.Write(frame)
		return
	}
	// 丢包：先填静音帧
	if gap > 0 {
		e.ring.Write(make([]int16, int(gap)*len(frame)))
	}
	e.lastSeq = seq
	if dropped := e.ring.Write(frame); dropped > 0 {
		// 缓冲超深被截断：说明读侧已经落后（丢包补静音、对端突发都会垫高深度）。
		// 限频打日志，这是排查"声音延迟越来越大"的第一手线索。
		if time.Since(e.lastTrimLog) > 10*time.Second {
			e.lastTrimLog = time.Now()
			logx.Logger().Warn("播放缓冲超深已截断（声音延迟被压回目标值）",
				"丢弃样本", dropped, "当前深度ms", e.ring.Depth()*1000/8000,
				"累计丢弃", e.ring.Trimmed())
		}
	}
}

// FeedMic 音频采集回调：接受任意帧长输入（音频回调帧长不一定等于
// 20ms），按编解码器帧长（20ms=160 样本）累积，凑满一帧编码发送，
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
	// 累积并按帧长发送
	e.micBuf = append(e.micBuf, pcm...)
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
	if _, err := e.conn.WriteToUDP(data, e.remote); err != nil {
		logx.Logger().Debug("RTP 发送失败", "err", err)
		return
	}
	e.seq++
	e.ts += uint32(codec.FrameSize())
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
func (e *Engine) ReadPlayback(buf []int16) int {
	e.playMu.Lock()
	n := e.ring.Read(buf)
	e.playMu.Unlock()
	if e.playMuted.Load() {
		n = 0
	}
	for i := n; i < len(buf); i++ {
		buf[i] = 0
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

// allCodecPTs 全部支持的编解码器负载类型（按优先级顺序，SDP offer 用）。
func allCodecPTs() []uint8 {
	pts := make([]uint8, len(supportedCodecs))
	for i, c := range supportedCodecs {
		pts[i] = c.PayloadType()
	}
	return pts
}

// SDPOffer 生成本地 SDP offer（PCMA/PCMU + telephone-event，按优先级排序）。
func (e *Engine) SDPOffer() string {
	return e.sdp("sendrecv", allCodecPTs())
}

// SDPOfferWithDirection 生成指定媒体方向的 SDP offer
//（保持 re-INVITE 用：保持 sendonly / 恢复 sendrecv）。
func (e *Engine) SDPOfferWithDirection(dir string) string {
	return e.sdp(dir, allCodecPTs())
}

// SDPNegotiatedAnswer 呼入接听时的 200 answer：仅含协商后的编解码器
//（RFC 3264 answer 只列双方协商结果）。
// 用 activePT（对端号）而非 codec.PayloadType()（本端号）——音频静态 PT
// 两者相同，动态 PT 下必须用对端号。
func (e *Engine) SDPNegotiatedAnswer() string {
	return e.sdp("sendrecv", []uint8{e.payloadType()})
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
	e.setRemote(remote, rtcpPort)
	// 远端地址更新后打洞（NAT 下对端媒体包需先有出向包放行）
	e.punchHole()
	return e.sdp(dir, []uint8{peerPT}), nil
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
// pts 按优先级顺序；telephone-event（101）恒在末尾。
func (e *Engine) audioSection(dir string, pts []uint8) sdpSection {
	codecs := make([]sdpCodec, 0, len(pts)+1)
	for _, pt := range pts {
		// 注意用 Name()（"PCMA"）而不是 RTPMap()（"PCMA/8000"）——
		// buildSDP 会自己拼 "/<ClockRate>"，用 RTPMap 会输出
		// "a=rtpmap:8 PCMA/8000/8000" 这种非法行。
		codecs = append(codecs, sdpCodec{
			PT:        pt,
			Encoding:  codecByPT(pt).Name(),
			ClockRate: 8000,
		})
	}
	codecs = append(codecs, sdpCodec{PT: telephoneEventPT, Encoding: "telephone-event", ClockRate: 8000, FMTP: "0-16"})
	return sdpSection{
		Kind:     "audio",
		Port:     e.localPort,
		RTCPPort: e.localPort + 1,
		Codecs:   codecs,
		Dir:      dir,
	}
}

// sdp 构造纯音频 SDP 文本（会话头 + m=audio）。
func (e *Engine) sdp(dir string, pts []uint8) string {
	return buildSDP(e.ssrc, e.sdpIPForSDP(), []sdpSection{e.audioSection(dir, pts)})
}

// SDPOfferFor 构造呼出 / 重协商用的 offer：会话头 + m=audio + 可选 m=video。
// videoDir 为空时按本端能力推导（能发 → sendrecv，只能收 → recvonly）；
// v 为 nil 或本构建无视频能力时不输出 m=video。
func SDPOfferFor(audio *Engine, v *VideoEngine, audioDir, videoDir string) string {
	sections := []sdpSection{audio.audioSection(audioDir, allCodecPTs())}
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
			sections = append(sections, audio.audioSection("sendrecv", []uint8{audioPT}))
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
		sections = append(sections, audio.audioSection("sendrecv", []uint8{audioPT}))
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

// matchAudioCodec 按本地优先级在 offer 的负载类型中挑音频编解码器。
// 先按 rtpmap 名匹配，再回退到静态负载类型号（对端未写 a=rtpmap 的旧式实现）。
func matchAudioCodec(info *MediaInfo) (Codec, uint8, bool) {
	for _, c := range supportedCodecs {
		for _, pt := range info.PTs {
			if info.EncodingMatches(pt, c.Name()) {
				return c, pt, true
			}
		}
	}
	// 回退：静态负载类型号直接命中
	for _, c := range supportedCodecs {
		for _, pt := range info.PTs {
			if pt == c.PayloadType() && info.Rtpmap[pt] == "" {
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
