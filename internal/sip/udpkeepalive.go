package sip

import (
	"context"
	"net"
	"strconv"
	"time"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/logx"
)

// UDP 监听端口的 NAT 映射保活（真机缺陷：对端挂断后本端不挂）。
//
// 背景：sipgo 的**出站**请求（REGISTER/INVITE）走它自己的临时 UDP socket，
// 而 Contact 宣告的是**监听 socket** 的端口。穿过 NAT 时，临时 socket 有映射
//（所以对端的响应能回来），监听端口却**没有任何出站包**、NAT 上没有映射——
// 服务端按 Contact 把 in-dialog 请求（BYE / INFO）发到「NAT 公网地址:监听端口」
// 时会被 NAT 直接丢弃。症状：主叫/被叫一方挂断后，另一方的通话一直挂着
// （信令断了、媒体也停了，但本端收不到任何东西）。
//
// 修法：定期**从监听 socket 本身**向注册服务器发一个 CRLF 保活包——只为建立/
// 刷新 NAT 映射（RFC 5626 §3.5.1 的 CRLF keepalive；服务端忽略或回 CRLF，
// 我们的读钩子会过滤非 SIP 载荷）。只在「UDP + 宣告了 NAT 地址（声明 IP 或
// STUN 公网 IP）」时启用：没有 NAT 地址时 Contact 本来就指向本机网卡，不需要。

const (
	// udpMappingKeepaliveInterval 映射保活周期：常见 UDP NAT 超时 30–120 秒，
	// 20 秒留足余量。
	udpMappingKeepaliveInterval = 20 * time.Second
	// udpKeepalivePayload 保活载荷（CRLF，与 TCP/TLS 保活同一口径）。
	udpKeepalivePayload = "\r\n"
)

// udpMappingKeepalive 从 UDP 监听 socket 定期向注册服务器发保活包，
// 维持「NAT 公网地址:监听端口」的映射（见上方说明）。kick 为注册成功后的
// 立即补发信号（buffer=1，非阻塞投递）。
func (p *Phone) udpMappingKeepalive(ctx context.Context, ln net.PacketConn, kick <-chan struct{}) {
	ticker := time.NewTicker(udpMappingKeepaliveInterval)
	defer ticker.Stop()
	logged := false
	for {
		if p.udpMappingWanted() {
			for _, target := range p.registrationUDPAddrs() {
				if _, err := ln.WriteTo([]byte(udpKeepalivePayload), target); err == nil && !logged {
					logged = true
					logx.Logger().Info("UDP 监听端口映射保活已启动（防止 NAT 丢弃对端的 BYE/INFO）",
						"server", target.String(), "interval", udpMappingKeepaliveInterval)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-kick:
		}
	}
}

// kickUDPMapping 注册成功后让保活循环立即补发一次（非阻塞：通道 buffer=1，
// 重复投递丢弃即可）。
func (p *Phone) kickUDPMapping() {
	p.mu.Lock()
	ch := p.udpKick
	p.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// udpMappingWanted 是否需要监听端口映射保活：UDP 传输即需要。
//
// 之前只在「UDP + 宣告了 NAT 地址（声明 IP 或 STUN 公网 IP）」时保活——那套
// 假定「没宣告 NAT 地址就是平网、服务端能直连本机」。但多服务器 + NAT 场景下，
// 话机可能没配声明 IP 却在 NAT 后面，此时唯一在维持监听端口 NAT 映射的只有
// MSP_HEARTBEAT，而心跳又可能被服务端关掉（MSP_UNSUPPORTED / 退出坐席）——一停，
// 监听端口的 NAT 映射就过期、呼入全断。所以 UDP 一律从监听 socket 发 CRLF 保活
// （2 字节 / 20 秒，代价可忽略），与心跳解耦。
func (p *Phone) udpMappingWanted() bool {
	p.mu.Lock()
	tr := config.NormalizeTransport(p.acc.Transport)
	p.mu.Unlock()
	return tr == config.TransportUDP
}

// registrationUDPAddrs 全部 UDP 注册服务器的地址（保活包的目的地）。
// 多注册（REQ-ACC-038）时对每个 UDP 注册目标都发保活，维持各自服务端把
// in-dialog 请求送回监听端口的 NAT 映射。解析失败的跳过（下一轮再试）。
func (p *Phone) registrationUDPAddrs() []*net.UDPAddr {
	p.mu.Lock()
	registrars := p.registrars
	if len(registrars) == 0 {
		registrars = []config.Account{p.acc}
	}
	p.mu.Unlock()

	var out []*net.UDPAddr
	seen := map[string]bool{}
	for _, acc := range registrars {
		if config.NormalizeTransport(acc.Transport) != config.TransportUDP {
			continue
		}
		if acc.Server == "" || acc.Port <= 0 {
			continue
		}
		key := net.JoinHostPort(acc.Server, strconv.Itoa(acc.Port))
		if seen[key] {
			continue
		}
		seen[key] = true
		addr, err := net.ResolveUDPAddr("udp", key)
		if err != nil {
			continue
		}
		out = append(out, addr)
	}
	return out
}
