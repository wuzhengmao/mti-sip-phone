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

// BindSIPListenerPair 在 40000~50000 范围内随机起点探测并**直接绑定**
// UDP + TCP 同号端口，返回已占用的 listener 与端口号。随机起点避免多次启动
// 总是选中同一端口。
//
// **探测即占用**（绑定成功即持有、不再释放），这是与旧 FindFreeSIPPort 的
// 关键区别：旧的"试绑探测→释放→稍后再由 sipgo 绑定"在两步之间存在 TOCTOU
// 窗口——**多实例几乎同时启动**时可能都探测到同一空闲端口，后绑定者失败
// （表现为该实例收不到呼入）。返回的 listener 直接交给 ServeUDP/ServeTCP。
func BindSIPListenerPair() (net.PacketConn, net.Listener, int, error) {
	try := func(p int) (net.PacketConn, net.Listener, bool) {
		uln, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", p))
		if err != nil {
			return nil, nil, false
		}
		tln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", p))
		if err != nil {
			_ = uln.Close()
			return nil, nil, false
		}
		return uln, tln, true
	}
	start := sipPortMin + rand.Intn(sipPortMax-sipPortMin)
	// 先向后探测，再回绕到起点之前
	for _, rng := range [][2]int{{start, sipPortMax}, {sipPortMin, start}} {
		for p := rng[0]; p < rng[1]; p++ {
			if uln, tln, ok := try(p); ok {
				logx.Logger().Info("本地 SIP 监听端口已选择", "port", p)
				return uln, tln, p, nil
			}
		}
	}
	return nil, nil, 0, fmt.Errorf("%d~%d 端口区间没有可用的 UDP+TCP 端口对", sipPortMin, sipPortMax)
}

// BindFreeTCPPort 在 40000~50000 范围内找一个 TCP 空闲端口并**直接绑定**
// （TLS 监听用；TLS 跑在 TCP 上，不能与明文 TCP 监听共用端口）。同样"探测即
// 占用"消除 TOCTOU 窗口（同 BindSIPListenerPair）。随机起点避免每次启动都落
// 在同一端口。
func BindFreeTCPPort() (net.Listener, int, error) {
	try := func(p int) (net.Listener, bool) {
		ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", p))
		if err != nil {
			return nil, false
		}
		return ln, true
	}
	start := sipPortMin + rand.Intn(sipPortMax-sipPortMin)
	for _, rng := range [][2]int{{start, sipPortMax}, {sipPortMin, start}} {
		for p := rng[0]; p < rng[1]; p++ {
			if ln, ok := try(p); ok {
				return ln, p, nil
			}
		}
	}
	return nil, 0, fmt.Errorf("%d~%d 端口区间没有空闲 TCP 端口", sipPortMin, sipPortMax)
}
