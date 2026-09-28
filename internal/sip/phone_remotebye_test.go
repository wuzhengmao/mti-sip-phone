package sip

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
)

// TestRemoteHangup 呼出接通后对端挂断（BYE）：Phone 应上报通话结束。
// 回归防护：此前 BYE 只按呼入会话 dialogS 匹配 Call-ID，呼出会话
//（dialogC）的对端 BYE 被忽略，界面停留"通话中"。
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
