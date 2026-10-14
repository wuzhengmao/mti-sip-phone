package sip

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
)

// TestRemoteHangup 呼出接通后对端挂断（BYE）：Phone 应上报通话结束。
// 回归防护：此前 BYE 只按呼入会话 dialogS 匹配 Call-ID，呼出会话
// （dialogC）的对端 BYE 被忽略，界面停留"通话中"。
func TestRemoteHangup(t *testing.T) {
	addr := startTestServer(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	acc := config.Account{Server: host, Port: port, Transport: "udp", Account: "1001", Password: "secret"}
	_ = p.Register(context.Background(), acc)
	time.Sleep(200 * time.Millisecond)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("通话未建立")
	}

	// 模拟对端 BYE 到达：构造与 INVITE 同 Call-ID 的 BYE 请求，
	// 走 handleRemoteBye 处理链（tx 传 nil 仅跳过响应发送）
	p.mu.Lock()
	cs := p.calls[callID]
	var byeReq *sip.Request
	if cs != nil && cs.dialogC != nil {
		byeReq = cs.dialogC.InviteRequest.Clone()
		byeReq.Method = sip.BYE
	}
	p.mu.Unlock()
	if byeReq == nil {
		t.Fatal("无法构造 BYE 请求")
	}
	p.handleRemoteBye(byeReq, nil)

	if !sink.waitEvent("terminated:"+callID, 2*time.Second) {
		t.Fatal("对端挂断后 Phone 未上报通话结束（matchByCallID 未覆盖呼出会话）")
	}
}

// TestRemoteHangupCaseInsensitiveCallID 对端 BYE 的 Call-ID 与本地记录
// **大小写不一致**（中间设备规范化 Call-ID 大小写，实测存在）：matchForTeardown
// 应按同一通话处理并结束通话——否则会话残留在 p.calls，卡死单通话限制。
func TestRemoteHangupCaseInsensitiveCallID(t *testing.T) {
	addr := startTestServer(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	acc := config.Account{Server: host, Port: port, Transport: "udp", Account: "1001", Password: "secret"}
	_ = p.Register(context.Background(), acc)
	time.Sleep(200 * time.Millisecond)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("通话未建立")
	}

	// 构造 Call-ID 大小写翻转的 BYE（先删旧头再挂新头：RemoveHeader 大小写
	// 敏感且只删第一条，这里恰好就是要删 "Call-ID"）
	p.mu.Lock()
	cs := p.calls[callID]
	var byeReq *sip.Request
	if cs != nil && cs.dialogC != nil {
		origCallID := cs.dialogC.InviteRequest.CallID().Value()
		byeReq = cs.dialogC.InviteRequest.Clone()
		byeReq.RemoveHeader("Call-ID")
		flipped := sip.CallIDHeader(strings.ToUpper(origCallID))
		byeReq.AppendHeader(&flipped)
		byeReq.Method = sip.BYE
	}
	p.mu.Unlock()
	if byeReq == nil {
		t.Fatal("无法构造 BYE 请求")
	}
	p.handleRemoteBye(byeReq, nil)

	if !sink.waitEvent("terminated:"+callID, 2*time.Second) {
		t.Fatal("Call-ID 大小写不一致的对端 BYE 未按同一通话处理（会话会残留）")
	}
}

// TestMatchForTeardownFallback 失配降级：
// ①唯一会话且 Call-ID 取不到 → 按它处理；②多条会话 → 宁可不删（防误删新通话）。
func TestMatchForTeardownFallback(t *testing.T) {
	p := newTestPhone(t, &testSink{})

	// 假会话要在 Phone.Close 之前清掉（Close 会关会话的媒体引擎，假会话
	// 没有引擎会空指针）；t.Cleanup 是 LIFO，后注册的先跑。
	t.Cleanup(func() {
		p.mu.Lock()
		delete(p.calls, "cx")
		delete(p.calls, "cy")
		p.mu.Unlock()
	})
	p.mu.Lock()
	p.calls["cx"] = &callSession{id: "cx"} // 无 dialog：Call-ID 取不到
	p.mu.Unlock()
	if got := p.matchForTeardown("unknown-call-id", "BYE"); got == nil || got.id != "cx" {
		t.Fatalf("唯一且无 Call-ID 的会话应按它处理，实际 %+v", got)
	}

	p.mu.Lock()
	p.calls["cy"] = &callSession{id: "cy"}
	p.mu.Unlock()
	if got := p.matchForTeardown("unknown-call-id", "BYE"); got != nil {
		t.Fatalf("多条会话失配时应返回 nil（防误删新通话），实际 %+v", got)
	}

	p.mu.Lock()
	delete(p.calls, "cy")
	p.mu.Unlock()
	if got := p.matchForTeardown("unknown-call-id", "BYE"); got == nil || got.id != "cx" {
		t.Fatalf("删回单条后应重新命中，实际 %+v", got)
	}
}
