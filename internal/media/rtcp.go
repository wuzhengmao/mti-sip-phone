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

	eng *Engine

	// 对端信息（收到 SR 后记录，供 RR 报告块与日志使用）
	peerSSRC atomic.Uint32
}

// RTCP 报告发送周期。
const rtcpInterval = 5 * time.Second

func newRTCPSession(conn *net.UDPConn, eng *Engine) *rtcpSession {
	return &rtcpSession{conn: conn, eng: eng, stopCh: make(chan struct{})}
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
	bye := &rtcp.Goodbye{Sources: []uint32{r.eng.ssrc}}
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
	}
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
	tx := r.eng.txPackets.Load()
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
		SSRC:        r.eng.ssrc,
		NTPTime:     ntpNow(),
		RTPTime:     r.eng.rtpTimestamp(),
		PacketCount: uint32(tx),
		OctetCount:  uint32(r.eng.txBytes.Load()),
	}
	pkts := []rtcp.Packet{sr}
	// 收到过对端 SR：附上接收报告块
	if peer := r.peerSSRC.Load(); peer != 0 {
		lastSeq, extSeq := r.eng.playState()
		sr.Reports = []rtcp.ReceptionReport{{
			SSRC:               peer,
			FractionLost:       0, // 精确丢包率统计后续版本实现
			TotalLost:          uint32(r.eng.rxLost.Load()),
			LastSequenceNumber: extSeq<<16 | uint32(lastSeq),
			Jitter:             0,
		}}
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
	logx.Logger().Debug("已发送 RTCP SR", "packets", tx, "octets", r.eng.txBytes.Load())
}

// ntpNow 当前时间的 NTP 时间戳（RTCP SR 用）。
func ntpNow() uint64 {
	now := time.Now()
	sec := uint64(now.Unix() + 2208988800)
	frac := uint64(now.Nanosecond()) * (1 << 32) / 1e9
	return sec<<32 | frac
}
