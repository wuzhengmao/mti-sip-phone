package sip

import (
	"fmt"
	"math/rand"
	"net"

	"mti-sip-phone/internal/logx"
)

// SIP 本地监听端口范围（REQ-ACC-013 修订）：40000~50000 之间随机选择空闲端口。
const (
	sipPortMin = 40000
	sipPortMax = 50000
)

// FindFreeSIPPort 在 40000~50000 范围内随机起点探测，返回 UDP 与 TCP
// 均空闲的本地端口。随机起点避免多次启动总是选中同一端口。
func FindFreeSIPPort() int {
	start := sipPortMin + rand.Intn(sipPortMax-sipPortMin)
	// 先向后探测
	for p := start; p < sipPortMax; p++ {
		if udpPortFree(p) && tcpPortFree(p) {
			logx.Logger().Info("本地 SIP 监听端口已选择", "port", p)
			return p
		}
	}
	// 再回绕到起点之前
	for p := sipPortMin; p < start; p++ {
		if udpPortFree(p) && tcpPortFree(p) {
			logx.Logger().Info("本地 SIP 监听端口已选择", "port", p)
			return p
		}
	}
	logx.Logger().Warn("40000~50000 端口区间全部被占用，回退使用", "port", sipPortMin)
	return sipPortMin
}

// udpPortFree 探测 UDP 端口是否可用（绑定全部接口，与 SIP 监听行为一致）。
func udpPortFree(port int) bool {
	ln, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// tcpPortFree 探测 TCP 端口是否可用。
func tcpPortFree(port int) bool {
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
