package sip

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/core"
)

// startCallerClient 启动一个测试对端：向 Phone 的监听地址发送 INVITE，
// 返回 (客户端, 发送 INVITE 函数)。
func startCallerClient(t *testing.T, phoneAddr string) (*sipgo.Client, func(answerAfter string) (*sipgo.DialogClientSession, error)) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("caller-test"))
	if err != nil {
		t.Fatalf("创建对端 UA 失败: %v", err)
	}
	client, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatalf("创建对端客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	host, portStr, _ := net.SplitHostPort(phoneAddr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	dcc := sipgo.NewDialogClientCache(client, sip.ContactHeader{
		Address: sip.Uri{User: "2901", Host: "127.0.0.1"},
	})

	invite := func(answerAfter string) (*sipgo.DialogClientSession, error) {
		recipient := sip.Uri{User: "2999", Host: host, Port: port}
		from := &sip.FromHeader{
			Address: sip.Uri{User: "2901", Host: host},
			Params:  sip.NewParams(),
		}
		from.Params.Add("tag", sip.GenerateTagN(8))
		to := &sip.ToHeader{Address: sip.Uri{User: "2999", Host: host}}
		var headers []sip.Header
		headers = append(headers, sip.NewHeader("Content-Type", "application/sdp"))
		if answerAfter != "" {
			headers = append(headers, sip.NewHeader("Call-Info", "<sip:2901@"+host+">;answer-after="+answerAfter))
		}
		headers = append(headers, from, to)
		return dcc.Invite(context.Background(), recipient,
			[]byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=t\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 9 RTP/AVP 0 8\r\n"),
			headers...)
	}
	return client, invite
}

// newListeningPhone 创建已监听随机端口的 Phone（呼入测试用）。
func newListeningPhone(t *testing.T, sink core.EventSink) (*Phone, string) {
	t.Helper()
	p, err := NewPhone(sink, nil, true)
	if err != nil {
		t.Fatalf("创建 Phone 失败: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	addr := ln.LocalAddr().String()
	_ = ln.Close() // 探测端口后交给 sipgo 监听（竞态可接受，测试环境）

	go func() { _ = p.ListenAndServe(context.Background(), "udp", addr) }()
	time.Sleep(200 * time.Millisecond)
	return p, addr
}

// TestIncomingAnswerAfterZero Call-Info answer-after=0：来电应立即自动接听。
func TestIncomingAnswerAfterZero(t *testing.T) {
	sink := &testSink{}
	_, addr := newListeningPhone(t, sink)

	_, invite := startCallerClient(t, addr)
	dlg, err := invite("0")
	if err != nil {
		t.Fatalf("发送 INVITE 失败: %v", err)
	}

	// 等待 200 OK（自动接听）
	err = dlg.WaitAnswer(context.Background(), sipgo.AnswerOptions{})
	if err != nil {
		t.Fatalf("未收到 200 OK（自动接听失败）: %v", err)
	}
	// 对端按 RFC 3261 回 ACK（sipgo 的 RespondSDP 会等待 ACK 才返回）
	if err := dlg.Ack(context.Background()); err != nil {
		t.Fatalf("发送 ACK 失败: %v", err)
	}
	t.Logf("对端 ACK 已发送")

	// Phone 侧应收到来电并建立通话
	if !sink.waitEvent("incoming:", 3*time.Second) {
		t.Fatal("Phone 未收到来电事件")
	}
	if !sink.waitEvent("established:", 3*time.Second) {
		t.Fatal("Phone 未自动接听（无 established 事件）")
	}
	_ = dlg.Bye(context.Background())
}

// TestIncomingAutoAnswerSwitch 本地自动接听开关：来电应立即自动接听。
func TestIncomingAutoAnswerSwitch(t *testing.T) {
	sink := &testSink{}
	p, addr := newListeningPhone(t, sink)
	_ = p.SetAutoAnswer(true)

	_, invite := startCallerClient(t, addr)
	dlg, err := invite("")
	if err != nil {
		t.Fatalf("发送 INVITE 失败: %v", err)
	}

	err = dlg.WaitAnswer(context.Background(), sipgo.AnswerOptions{})
	if err != nil {
		t.Fatalf("未收到 200 OK（自动接听失败）: %v", err)
	}
	// 对端按 RFC 3261 回 ACK（sipgo 的 RespondSDP 会等待 ACK 才返回）
	if err := dlg.Ack(context.Background()); err != nil {
		t.Fatalf("发送 ACK 失败: %v", err)
	}
	t.Logf("对端 ACK 已发送")
	if !sink.waitEvent("established:", 3*time.Second) {
		t.Fatal("Phone 未自动接听（无 established 事件）")
	}
	_ = dlg.Bye(context.Background())
}

// TestIncomingAnswerAfterDelay Call-Info answer-after=1：先收到 180，约 1 秒后自动接听。
func TestIncomingAnswerAfterDelay(t *testing.T) {
	sink := &testSink{}
	_, addr := newListeningPhone(t, sink)

	_, invite := startCallerClient(t, addr)
	dlg, err := invite("1")
	if err != nil {
		t.Fatalf("发送 INVITE 失败: %v", err)
	}

	start := time.Now()
	err = dlg.WaitAnswer(context.Background(), sipgo.AnswerOptions{
		OnResponse: func(res *sip.Response) error {
			if res.StatusCode == 180 {
				// 延迟接听场景应先振铃
				t.Logf("收到 180 振铃（延迟 %.0fms）", time.Since(start).Seconds()*1000)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("未收到 200 OK（延迟自动接听失败）: %v", err)
	}
	// 对端回 ACK（sipgo 的 RespondSDP 会等待 ACK 才返回）
	if err := dlg.Ack(context.Background()); err != nil {
		t.Fatalf("发送 ACK 失败: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 800*time.Millisecond {
		t.Fatalf("接听过早（%dms，应延迟约 1 秒）", elapsed.Milliseconds())
	}
	if !sink.waitEvent("established:", 3*time.Second) {
		t.Fatal("Phone 未自动接听")
	}
	_ = dlg.Bye(context.Background())
}
