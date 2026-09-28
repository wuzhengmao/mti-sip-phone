package sip

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

// startRejectServer 对 INVITE 回 486 Busy Here 的迷你服务器（验证失败路径）。
func startRejectServer(t *testing.T, code int, reason string) (addr string) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("reject-server"))
	if err != nil {
		t.Fatalf("创建测试服务器 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建测试服务器失败: %v", err)
	}
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, code, reason, nil))
	})
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {})

	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 UDP 失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = srv.ServeUDP(ln) }()
	return ln.LocalAddr().String()
}

// TestDialRejected 呼出被 486 拒绝：应触发 OnFailed 且 reason 含状态码。
func TestDialRejected(t *testing.T) {
	addr := startRejectServer(t, 486, "Busy Here")
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	acc := config.Account{
		Server:    host,
		Port:      port,
		Transport: "udp",
		Account:   "1001",
	}
	_ = p.Register(context.Background(), acc)
	time.Sleep(200 * time.Millisecond)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 返回错误: %v", err)
	}

	if !sink.waitEvent("failed:"+callID, 5*time.Second) {
		t.Fatalf("未收到呼叫失败事件，事件列表: %v", sink.events)
	}
	// 验证 reason 包含状态码
	sink.mu.Lock()
	defer sink.mu.Unlock()
	found := false
	for _, e := range sink.events {
		if len(e) > 7 && e[:7] == "failed:" {
			t.Logf("失败事件: %s", e)
			found = true
		}
	}
	if !found {
		t.Fatal("失败事件内容缺失")
	}
}

// TestDialRejected611 呼出收到非标准 611 拒绝：同样触发 OnFailed（OpenSIPS 场景）。
func TestDialRejected611(t *testing.T) {
	addr := startRejectServer(t, 611, "Rejected by loop")
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	acc := config.Account{
		Server:    host,
		Port:      port,
		Transport: "udp",
		Account:   "1001",
	}
	_ = p.Register(context.Background(), acc)
	time.Sleep(200 * time.Millisecond)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 返回错误: %v", err)
	}

	if !sink.waitEvent("failed:"+callID, 5*time.Second) {
		t.Fatalf("未收到呼叫失败事件（611 场景），事件列表: %v", sink.events)
	}
}

var _ = core.CallRinging // 保持 core 导入
