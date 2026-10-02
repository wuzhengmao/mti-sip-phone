package media

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"

	"mti-sip-phone/internal/logx"
)

// rtcpSession RTCP 收发：周期发送 SR（Sender Report，携带发送统计），
// 接收对端 SR/RR 并记录日志，通话结束时发送 BYE（Goodbye）。
// RTCP 端口为 RTP 端口 + 1（RFC 3605 约定，SDP 中通过 a=rtcp 宣告；
// 对端 offer 若声明 a=rtcp 则按声明端口）。
type rtcpSession struct {
	conn *net.UDPConn

	mu     sync.RWMutex
	remote *net.UDPAddr // 对端 RTCP 地址

	stopCh chan struct{}
	wg     sync.WaitGroup

	ep rtcpEndpoint

	// 对端信息（收到 SR 后记录，供 RR 报告块与日志使用）
	peerSSRC atomic.Uint32

	// PLI/FIR 发送限流（防止对端解码器被关键帧请求打爆）
	pliMu   sync.Mutex
	lastPLI time.Time
}

// rtcpEndpoint RTCP 报告所需的端点快照。音频 Engine 与视频 VideoEngine 都实现它。
// 抽成接口是为了让两个媒体引擎共用这一份 RTCP 收发实现。
type rtcpEndpoint interface {
	rtcpSSRC() uint32
	rtcpRTPTime() uint32
	rtcpTxStats() (packets, bytes uint64)
	// rtcpRecvState 返回最近接收序号与丢包计数；ok=false 表示尚未收到过媒体。
	rtcpRecvState() (lastSeq uint16, extSeq uint32, lost uint64, ok bool)
	// rtcpOnFeedback 处理对端反馈包（视频用 PLI/FIR 触发关键帧；音频忽略）。
	rtcpOnFeedback(pkt rtcp.Packet)
}

// RTCP 报告发送周期。
const rtcpInterval = 5 * time.Second

// PLI/FIR 最小发送间隔（RFC 5104 建议的拥塞控制下限量级）。
const pliMinInterval = 500 * time.Millisecond

func newRTCPSession(conn *net.UDPConn, ep rtcpEndpoint) *rtcpSession {
	return &rtcpSession{conn: conn, ep: ep, stopCh: make(chan struct{})}
}

// setRemote 更新对端 RTCP 地址（媒体协商时随 RTP 地址一起更新）。
// port 为对端 SDP 声明的 RTCP 端口，0 表示未声明（按惯例 = RTP 端口 + 1）。
func (r *rtcpSession) setRemote(addr *net.UDPAddr, port int) {
	r.mu.Lock()
	if addr != nil {
		if port == 0 {
			port = addr.Port + 1
		}
		r.remote = &net.UDPAddr{IP: addr.IP, Port: port}
	}
	r.mu.Unlock()
}

// start 启动接收循环与周期发送。
func (r *rtcpSession) start() {
	r.wg.Add(2)
	go r.recvLoop()
	go r.sendLoop()
}

// stop 停止收发循环。
func (r *rtcpSession) stop() {
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
	if r.conn != nil {
		_ = r.conn.Close()
	}
	r.wg.Wait()
}

// sendBye 发送 RTCP Goodbye（通话结束时通知对端媒体流结束）。
func (r *rtcpSession) sendBye() {
	r.mu.RLock()
	remote := r.remote
	r.mu.RUnlock()
	if remote == nil || r.conn == nil {
		return
	}
	bye := &rtcp.Goodbye{Sources: []uint32{r.ep.rtcpSSRC()}}
	data, err := bye.Marshal()
	if err != nil {
		return
	}
	_, _ = r.conn.WriteToUDP(data, remote)
}

// recvLoop 接收并解析 RTCP 包（SR/RR/BYE 记录日志）。
func (r *rtcpSession) recvLoop() {
	defer r.wg.Done()
	buf := make([]byte, 2048)
	for {
		select {
		case <-r.stopCh:
			return
		default:
		}
		_ = r.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, raddr, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			select {
			case <-r.stopCh:
				return
			default:
				logx.Logger().Debug("RTCP 读取错误", "err", err)
				return
			}
		}
		pkts, err := rtcp.Unmarshal(buf[:n])
		if err != nil {
			logx.Logger().Debug("RTCP 解析失败", "err", err)
			continue
		}
		for _, p := range pkts {
			r.handlePacket(p, raddr)
		}
	}
}

// handlePacket 处理单个 RTCP 包：复合包展开，SR/RR/Goodbye 记录日志。
func (r *rtcpSession) handlePacket(p rtcp.Packet, raddr *net.UDPAddr) {
	switch p := p.(type) {
	case *rtcp.CompoundPacket:
		for _, inner := range *p {
			r.handlePacket(inner, raddr)
		}
	case *rtcp.SenderReport:
		r.peerSSRC.Store(p.SSRC)
		logx.Logger().Debug("收到 RTCP SR", "src", raddr.String(), "ssrc", p.SSRC,
			"packets", p.PacketCount, "octets", p.OctetCount)
	case *rtcp.ReceiverReport:
		logx.Logger().Debug("收到 RTCP RR", "src", raddr.String(), "ssrc", p.SSRC)
	case *rtcp.Goodbye:
		logx.Logger().Info("收到 RTCP Goodbye（对端媒体结束）", "src", raddr.String())
	case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
		// 对端画面损坏，请求关键帧（视频用；音频实现忽略）
		r.ep.rtcpOnFeedback(p)
	}
}

// sendPLI 发送 RTCP PLI（Picture Loss Indication）请求对端立即编一个关键帧。
// 内部限流：至少间隔 pliMinInterval，避免丢包抖动时把对端编码器打爆。
func (r *rtcpSession) sendPLI(mediaSSRC uint32) {
	r.pliMu.Lock()
	if time.Since(r.lastPLI) < pliMinInterval {
		r.pliMu.Unlock()
		return
	}
	r.lastPLI = time.Now()
	r.pliMu.Unlock()

	r.mu.RLock()
	remote := r.remote
	r.mu.RUnlock()
	if remote == nil || r.conn == nil {
		return
	}
	pli := &rtcp.PictureLossIndication{SenderSSRC: r.ep.rtcpSSRC(), MediaSSRC: mediaSSRC}
	data, err := pli.Marshal()
	if err != nil {
		return
	}
	if _, err := r.conn.WriteToUDP(data, remote); err != nil {
		logx.Logger().Debug("RTCP PLI 发送失败", "err", err)
		return
	}
	logx.Logger().Debug("已请求关键帧（RTCP PLI）")
}

// sendLoop 周期发送 RTCP 报告。
func (r *rtcpSession) sendLoop() {
	defer r.wg.Done()
	ticker := time.NewTicker(rtcpInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.sendReport()
		}
	}
}

// sendReport 发送 RTCP 复合报告 [SR + RR]。
// 仅在本端发送过媒体（txPackets > 0）时发送；收到过对端 SR 时附带
// 接收报告块。
func (r *rtcpSession) sendReport() {
	tx, txBytes := r.ep.rtcpTxStats()
	if tx == 0 {
		return // 未发送过媒体，无需报告
	}
	r.mu.RLock()
	remote := r.remote
	r.mu.RUnlock()
	if remote == nil {
		return
	}

	sr := &rtcp.SenderReport{
		SSRC:        r.ep.rtcpSSRC(),
		NTPTime:     ntpNow(),
		RTPTime:     r.ep.rtcpRTPTime(),
		PacketCount: uint32(tx),
		OctetCount:  uint32(txBytes),
	}
	pkts := []rtcp.Packet{sr}
	// 收到过对端 SR：附上接收报告块
	if peer := r.peerSSRC.Load(); peer != 0 {
		if lastSeq, extSeq, lost, ok := r.ep.rtcpRecvState(); ok {
			sr.Reports = []rtcp.ReceptionReport{{
				SSRC:               peer,
				FractionLost:       0, // 精确丢包率统计后续版本实现
				TotalLost:          uint32(lost),
				LastSequenceNumber: extSeq<<16 | uint32(lastSeq),
				Jitter:             0,
			}}
		}
	}
	data, err := rtcp.Marshal(pkts)
	if err != nil {
		logx.Logger().Debug("RTCP 报告构造失败", "err", err)
		return
	}
	if _, err := r.conn.WriteToUDP(data, remote); err != nil {
		logx.Logger().Debug("RTCP 发送失败", "err", err)
		return
	}
	logx.Logger().Debug("已发送 RTCP SR", "packets", tx, "octets", txBytes)
}

// ntpNow 当前时间的 NTP 时间戳（RTCP SR 用）。
func ntpNow() uint64 {
	now := time.Now()
	sec := uint64(now.Unix() + 2208988800)
	frac := uint64(now.Nanosecond()) * (1 << 32) / 1e9
	return sec<<32 | frac
}
