package sip

import (
	"testing"

	"mti-sip-phone/internal/config"
)

// TestUDPMappingWanted 监听端口映射保活的触发条件（REQ-ACC-038 修订）：
// UDP 传输一律保活（与 MSP_HEARTBEAT 解耦，独立维持监听端口 NAT 映射——
// 心跳可能被服务端关掉，不能当保活用）；非 UDP（TCP/TLS 有连接可复用）不需要。
func TestUDPMappingWanted(t *testing.T) {
	p := newTestPhone(t, &testSink{})

	set := func(acc config.Account, pub string) {
		p.mu.Lock()
		p.acc, p.publicIP = acc, pub
		p.mu.Unlock()
	}

	// UDP（即便没宣告任何 NAT 地址）→ 也要保活
	set(config.Account{Transport: "udp"}, "")
	if !p.udpMappingWanted() {
		t.Fatal("UDP 传输应启用保活（与心跳解耦）")
	}

	// 非 UDP（TLS/TCP 有连接可复用）→ 不需要
	set(config.Account{Transport: "tls", PublicIP: "10.168.5.10"}, "")
	if p.udpMappingWanted() {
		t.Fatal("TLS 传输不应启用 UDP 映射保活")
	}
	set(config.Account{Transport: "tcp", PublicIP: "10.168.5.10"}, "")
	if p.udpMappingWanted() {
		t.Fatal("TCP 传输不应启用 UDP 映射保活")
	}
}
