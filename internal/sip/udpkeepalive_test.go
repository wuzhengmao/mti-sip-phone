package sip

import (
	"testing"

	"mti-sip-phone/internal/config"
)

// TestUDPMappingWanted 监听端口映射保活的触发条件（REQ-ACC-026 相关真机缺陷修复）：
// 只有「UDP 传输 + 宣告了 NAT 地址（声明 IP 或 STUN 公网 IP）」才需要保活。
func TestUDPMappingWanted(t *testing.T) {
	p := newTestPhone(t, &testSink{})

	set := func(acc config.Account, pub string) {
		p.mu.Lock()
		p.acc, p.publicIP = acc, pub
		p.mu.Unlock()
	}

	// UDP 但没有任何 NAT 地址 → 不需要（Contact 指向本机网卡）
	set(config.Account{Transport: "udp"}, "")
	if p.udpMappingWanted() {
		t.Fatal("UDP 且无声明/公网 IP 时不应启用保活")
	}

	// UDP + 声明 IP → 需要
	set(config.Account{Transport: "udp", PublicIP: "10.168.5.10"}, "")
	if !p.udpMappingWanted() {
		t.Fatal("UDP + 声明 IP 时应启用保活")
	}

	// UDP + STUN 公网 IP → 需要
	set(config.Account{Transport: "udp"}, "203.0.113.5")
	if !p.udpMappingWanted() {
		t.Fatal("UDP + STUN 公网 IP 时应启用保活")
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
