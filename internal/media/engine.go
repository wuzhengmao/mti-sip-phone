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
	e.rtcp = newRTCPSession(rtcpConn, e)
	logx.Logger().Debug("媒体引擎已创建", "codec", e.codec.Name(), "localPort", e.localPort, "rtcpPort", e.localPort+1)
	return e, nil
}

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
		if pkt.PayloadType != codec.PayloadType() {
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
	e.ring.Write(frame)
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
			PayloadType:    codec.PayloadType(),
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
func (e *Engine) SDPNegotiatedAnswer() string {
	e.codecMu.RLock()
	pt := e.codec.PayloadType()
	e.codecMu.RUnlock()
	return e.sdp("sendrecv", []uint8{pt})
}

// SDPAnswer 解析远端 offer，协商编解码并返回本地 answer。
// 成功时 Engine 已切换到协商的编解码器并设置远端 RTP 地址。
func (e *Engine) SDPAnswer(offer []byte) (string, error) {
	return e.SDPAnswerWithDirection(offer, "sendrecv")
}

// SDPAnswerWithDirection 解析远端 offer 并返回指定方向的 answer
//（对端 re-INVITE 用：方向镜像，如对端 recvonly → 本端 sendonly）。
func (e *Engine) SDPAnswerWithDirection(offer []byte, dir string) (string, error) {
	remote, pt, rtcpPort, err := parseSDPOffer(offer)
	if err != nil {
		return "", err
	}
	e.codecMu.Lock()
	e.codec = codecByPT(pt)
	e.codecMu.Unlock()
	e.setRemote(remote, rtcpPort)
	// 远端地址更新后打洞（NAT 下对端媒体包需先有出向包放行）
	e.punchHole()
	return e.sdp(dir, []uint8{pt}), nil
}

// SDPApplyAnswer 处理远端对本地 offer 的应答：提取远端 RTP 地址并切换编解码。
func (e *Engine) SDPApplyAnswer(answer []byte) error {
	remote, pt, rtcpPort, err := parseSDPAnswer(answer, e.localPort)
	if err != nil {
		return err
	}
	e.codecMu.Lock()
	e.codec = codecByPT(pt)
	e.codecMu.Unlock()
	e.setRemote(remote, rtcpPort)
	// 远端地址更新（如 183 → 200 端口变化）后重新打洞
	e.punchHole()
	logx.Logger().Debug("SDP 协商完成", "codec", codecByPT(pt).Name(), "remote", remote.String())
	return nil
}

// sdp 构造 SDP 文本。c= 使用 SDP 宣告地址（NAT 场景为对端可达地址，
// 未设置时为本机地址）。rtcpPort 为本端 RTCP 端口（RTP + 1）。
func (e *Engine) sdp(dir string, pts []uint8) string {
	ip := e.sdpIP
	if ip == "" {
		ip = e.localIP
	}
	var b strings.Builder
	fmt.Fprintf(&b, "v=0\r\no=- %d 1 IN IP4 %s\r\ns=mti-sip-phone\r\nc=IN IP4 %s\r\nt=0 0\r\n",
		e.ssrc, ip, ip)
	fmt.Fprintf(&b, "m=audio %d RTP/AVP", e.localPort)
	for _, pt := range pts {
		fmt.Fprintf(&b, " %d", pt)
	}
	b.WriteString(" 101\r\n")
	for _, pt := range pts {
		fmt.Fprintf(&b, "a=rtpmap:%d %s\r\n", pt, codecByPT(pt).RTPMap())
	}
	fmt.Fprintf(&b, "a=rtpmap:101 telephone-event/8000\r\n"+
		"a=fmtp:101 0-16\r\n"+
		"a=rtcp:%d\r\n"+
		"a=%s\r\n", e.localPort+1, dir)
	return b.String()
}

// parseSDPOffer 解析 offer：提取远端 RTP 地址、首选编解码负载类型与
// 对端声明的 RTCP 端口（0 = 未声明）。
// 编码选择规则：双方能力交集按本地优先级（PCMA > PCMU）。
func parseSDPOffer(offer []byte) (*net.UDPAddr, uint8, int, error) {
	sd := &sdp.SessionDescription{}
	if err := sd.Unmarshal(offer); err != nil {
		return nil, 0, 0, fmt.Errorf("SDP 解析失败: %w", err)
	}
	return negotiateSDP(sd)
}

// parseSDPAnswer 解析 answer（对端响应）。
func parseSDPAnswer(answer []byte, localPort int) (*net.UDPAddr, uint8, int, error) {
	sd := &sdp.SessionDescription{}
	if err := sd.Unmarshal(answer); err != nil {
		return nil, 0, 0, fmt.Errorf("SDP 解析失败: %w", err)
	}
	remote, pt, rtcpPort, err := negotiateSDP(sd)
	if err != nil {
		return nil, 0, 0, err
	}
	// answer 中若 m= 端口为 0 表示拒绝该媒体
	if remote.Port == 0 {
		return nil, 0, 0, fmt.Errorf("对端拒绝了音频媒体")
	}
	return remote, pt, rtcpPort, nil
}

// negotiateSDP 从 SDP 中提取连接地址、端口、编解码协商结果与 RTCP 端口。
func negotiateSDP(sd *sdp.SessionDescription) (*net.UDPAddr, uint8, int, error) {
	if len(sd.MediaDescriptions) == 0 {
		return nil, 0, 0, fmt.Errorf("SDP 缺少媒体描述")
	}
	md := sd.MediaDescriptions[0]

	// 连接地址：m= 行的 c= 优先，其次会话级 c=
	ip := sd.ConnectionInformation
	port := md.MediaName.Port.Value
	if md.ConnectionInformation != nil {
		ip = md.ConnectionInformation
	}
	if ip == nil {
		return nil, 0, 0, fmt.Errorf("SDP 缺少连接信息")
	}
	addr := &net.UDPAddr{IP: net.ParseIP(ip.Address.Address), Port: port}
	if addr.IP == nil {
		return nil, 0, 0, fmt.Errorf("SDP 连接地址非法: %s", ip.Address.Address)
	}

	// 对端声明的 RTCP 端口（a=rtcp，RFC 3605；未声明按惯例 RTP + 1）
	rtcpPort := 0
	for _, attr := range md.Attributes {
		if attr.Key == "rtcp" {
			if v, err := strconv.Atoi(attr.Value); err == nil {
				rtcpPort = v
			}
		}
	}

	// 提取对端支持的编解码负载类型
	remotePTs := make(map[uint8]bool)
	for _, pt := range md.MediaName.Formats {
		v, err := strconv.ParseUint(pt, 10, 8)
		if err == nil {
			remotePTs[uint8(v)] = true
		}
	}

	// 按本地优先级协商
	for _, c := range supportedCodecs {
		if remotePTs[c.PayloadType()] {
			return addr, c.PayloadType(), rtcpPort, nil
		}
	}
	return nil, 0, 0, fmt.Errorf("无共同编解码器（对端支持: %s）", strings.Join(md.MediaName.Formats, ","))
}

// SDPDirection 提取 SDP 媒体方向属性（sendonly/sendrecv/recvonly/inactive）。
// 无方向属性时按 RFC 3264 默认双向。
func SDPDirection(sdpText []byte) string {
	sd := &sdp.SessionDescription{}
	if err := sd.Unmarshal(sdpText); err != nil {
		return "sendrecv"
	}
	if len(sd.MediaDescriptions) == 0 {
		return "sendrecv"
	}
	for _, attr := range sd.MediaDescriptions[0].Attributes {
		switch attr.Key {
		case "sendonly", "sendrecv", "recvonly", "inactive":
			return attr.Key
		}
	}
	return "sendrecv"
}

// MirrorDirection 镜像媒体方向（对端 offer 的方向 → 本端 answer 的方向）：
// 对端只发不收（sendonly）→ 本端只收不发（recvonly），依此类推。
func MirrorDirection(dir string) string {
	switch dir {
	case "sendonly":
		return "recvonly"
	case "recvonly":
		return "sendonly"
	case "inactive":
		return "inactive"
	default:
		return "sendrecv"
	}
}

// codecByPT 按负载类型取编解码器（协商失败兜底 PCMU）。
func codecByPT(pt uint8) Codec {
	for _, c := range supportedCodecs {
		if c.PayloadType() == pt {
			return c
		}
	}
	return supportedCodecs[0]
}
