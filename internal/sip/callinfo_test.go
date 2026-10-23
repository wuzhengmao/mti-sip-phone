package sip

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
)

// TestParseCallInfo INVITE 的 Call-Info：display-name 是百分号编码的对端名字，
// call-type=consult 表示咨询腿（REQ-ACC-029）。URI 自带的参数不能当头部参数。
func TestParseCallInfo(t *testing.T) {
	req := sip.NewRequest(sip.INVITE, sip.Uri{User: "2999", Host: "127.0.0.1"})
	req.AppendHeader(sip.NewHeader("Call-Info",
		"<sip:2901@10.168.4.40;transport=udp>;display-name=%E6%B5%8B%E8%AF%951;call-type=consult"))

	ci := parseCallInfo(req)
	if ci.DisplayName != "测试1" {
		t.Fatalf("display-name 应解码成 测试1，实际 %q", ci.DisplayName)
	}
	if ci.CallType != "consult" {
		t.Fatalf("call-type 应为 consult，实际 %q", ci.CallType)
	}

	// 没有 Call-Info（或没有这两个参数）时都为空
	if got := parseCallInfo(sip.NewRequest(sip.INVITE, sip.Uri{User: "2999", Host: "h"})); got.DisplayName != "" || got.CallType != "" {
		t.Fatalf("无 Call-Info 时应为空，实际 %+v", got)
	}

	// answer-after 仍按原语义解析（REQ-CALL-015）
	req2 := sip.NewRequest(sip.INVITE, sip.Uri{User: "2999", Host: "h"})
	req2.AppendHeader(sip.NewHeader("Call-Info", "<sip:2901@h>;answer-after=2"))
	if n := parseAnswerAfter(req2); n != 2 {
		t.Fatalf("answer-after 应为 2，实际 %d", n)
	}
}

// TestBuildCallInfo 呼出 INVITE 的 Call-Info：display-name 百分号编码（汉字/分隔符），
// 没有名字时只带 call-type（用户要求"如果有的话"）。
func TestBuildCallInfo(t *testing.T) {
	if got, want := buildCallInfo("2002", "10.168.4.40", "李四", "call"),
		"<sip:2002@10.168.4.40>;display-name=%E6%9D%8E%E5%9B%9B;call-type=call"; got != want {
		t.Fatalf("Call-Info 不符：\n got %q\nwant %q", got, want)
	}
	if got, want := buildCallInfo("2002", "10.168.4.40", "", "call"),
		"<sip:2002@10.168.4.40>;call-type=call"; got != want {
		t.Fatalf("无名时应省略 display-name：\n got %q\nwant %q", got, want)
	}
	// 名字里的 `;` `=` 空格必须转义，否则对端会把它当参数分隔符
	if got, want := buildCallInfo("1", "h", "a;b=c d", "call"),
		"<sip:1@h>;display-name=a%3Bb%3Dc%20d;call-type=call"; got != want {
		t.Fatalf("特殊字符应转义：\n got %q\nwant %q", got, want)
	}
}

// TestOutgoingCallInfo 呼出 INVITE 的 Call-Info：display-name 取注入的查名结果
// （core 注入的通信录查询），call-type 固定 call（REQ-ACC-029）。
func TestOutgoingCallInfo(t *testing.T) {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("callinfo-server"))
	if err != nil {
		t.Fatalf("创建测试服务器 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建测试服务器失败: %v", err)
	}
	var mu sync.Mutex
	got := ""
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		mu.Lock()
		if h := req.GetHeader("Call-Info"); h != nil {
			got = h.Value()
		}
		mu.Unlock()
		_ = tx.Respond(sip.NewResponseFromRequest(req, 486, "Busy Here", nil))
	})
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {})

	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 UDP 失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = srv.ServeUDP(ln) }()

	host, portStr, _ := net.SplitHostPort(ln.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)

	p := newTestPhone(t, &testSink{})
	// core 注入的查名回调：这里模拟"2002 在通信录里叫李四"
	p.SetPeerNameLookup(func(no string) string {
		if no == "2002" {
			return "李四"
		}
		return ""
	})
	_ = p.Register(context.Background(), config.Account{
		Server: host, Port: port, Transport: "udp", Account: "1001",
	})
	time.Sleep(200 * time.Millisecond)

	if _, err := p.Dial(context.Background(), "2002"); err != nil {
		t.Fatalf("发起呼叫失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		v := got
		mu.Unlock()
		if v != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	v := got
	mu.Unlock()
	want := "<sip:2002@" + host + ">;display-name=%E6%9D%8E%E5%9B%9B;call-type=call"
	if v != want {
		t.Fatalf("呼出 Call-Info 不符：\n got %q\nwant %q", v, want)
	}
}

// 要带上对端名称（PeerName）与 consult 标记。
func TestIncomingCallInfo(t *testing.T) {
	sink := &testSink{}
	_, addr := newListeningPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	ua, err := sipgo.NewUA(sipgo.WithUserAgent("caller-test"))
	if err != nil {
		t.Fatalf("创建对端 UA 失败: %v", err)
	}
	client, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatalf("创建对端客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	dcc := sipgo.NewDialogClientCache(client, sip.ContactHeader{
		Address: sip.Uri{User: "2901", Host: "127.0.0.1"},
	})

	from := &sip.FromHeader{Address: sip.Uri{User: "2901", Host: host}, Params: sip.NewParams()}
	from.Params.Add("tag", sip.GenerateTagN(8))
	to := &sip.ToHeader{Address: sip.Uri{User: "2999", Host: host}}
	headers := []sip.Header{
		sip.NewHeader("Content-Type", "application/sdp"),
		sip.NewHeader("Call-Info", "<sip:2901@"+host+">;display-name=%E6%B5%8B%E8%AF%951;call-type=consult"),
		from, to,
	}
	_, err = dcc.Invite(context.Background(),
		sip.Uri{User: "2999", Host: host, Port: port},
		[]byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=t\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 9 RTP/AVP 0 8\r\n"),
		headers...)
	if err != nil {
		t.Fatalf("发送 INVITE 失败: %v", err)
	}

	if !sink.waitEvent("incoming:", 5*time.Second) {
		t.Fatal("Phone 未收到来电事件")
	}
	info := sink.lastIncomingInfo()
	if info.Remote != "2901" {
		t.Fatalf("Remote 应为 2901，实际 %q", info.Remote)
	}
	if info.PeerName != "测试1" {
		t.Fatalf("PeerName 应为 测试1，实际 %q", info.PeerName)
	}
	if !info.Consult {
		t.Fatal("call-type=consult 时应标记 Consult")
	}
}
