package sip

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
)

// crlf 测试 SDP 换行（避免在字符串里塞一堆转义）
const crlf = "\r\n"

// TLS 对话信令的两个真机缺陷回归（REQ-ACC-006）：
//
//  1. 振铃中挂断（CANCEL）：sipgo 构造 CANCEL 时走 dialog 的 buildReq，会把
//     **响应里的 Record-Route** 当成 Route 塞进去，于是 CANCEL 被发到代理地址
//     （真机上那条腿连不上）→ CANCEL 静默丢失、对端一直振铃。
//  2. 呼入接听后立刻失败：in-dialog 的传输不能长期改写**收到的那条 INVITE**
//     ——它同时被服务端事务使用，改掉会把 200 OK 发坏（transaction terminated）。

// TestCancelWhileRingingOverTLS 振铃中挂断：对端 183 带 Record-Route 时，
// CANCEL 仍必须按 INVITE 的路径发出去（对端要收到 CANCEL）。
func TestCancelWhileRingingOverTLS(t *testing.T) {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("cancel-peer"),
		sipgo.WithUserAgenTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	if err != nil {
		t.Fatalf("UA: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("Server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(); _ = ua.Close() })

	cancels := make(chan struct{}, 4)
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "300"))
		_ = tx.Respond(res)
	})
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// 183 带 Record-Route（真机 OpenSIPS 会给临时响应也加）
		res := sip.NewResponseFromRequest(req, 183, "Session Progress", []byte(testSDPAnswer))
		res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		// 真机形状：OpenSIPS 双 Record-Route（r2=on 那条是对话所在的 TLS 腿）
		res.AppendHeader(sip.NewHeader("Record-Route", "<sip:127.0.0.1:5099;transport=tls;r2=on;lr;nat=yes>"))
		res.AppendHeader(sip.NewHeader("Record-Route", "<sip:127.0.0.1:5098;lr;nat=yes>"))
		_ = tx.Respond(res)
	})
	srv.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		select {
		case cancels <- struct{}{}:
		default:
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		_ = srv.ServeTLS(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{testTLSCert(t)}}))
	}()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	sink := &testSink{}
	p := newTestPhone(t, sink)
	p.SetTLSDir(t.TempDir())
	acc := config.Account{Server: host, Port: port, Transport: "tls", Account: "1001", Password: "s", Expires: 300}
	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if !sink.waitEvent("ringing:"+callID, 5*time.Second) {
		t.Fatalf("未振铃: %v", sink.events)
	}
	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	select {
	case <-cancels:
	case <-time.After(3 * time.Second):
		t.Fatal("振铃中挂断后对端没收到 CANCEL")
	}
}

// TestIncomingOverTLS 呼入（TLS）接听后必须能正常建立并保持：
// sipgo 的 TerminateGracefully 对可靠传输会在 handler 返回时立刻终止 INVITE 事务，
// 而接听流程正在 RespondSDP 里等 ACK——handler 早返回就会把刚接通的通话打成
// "transaction terminated"（真机：接听后约 0.5 秒自动挂断；UDP 靠事务被保留躲过）。
// 这里同时覆盖：建立成功、本端挂断的 BYE 能送到对端。
func TestIncomingOverTLS(t *testing.T) {
	sink := &testSink{}
	p := newTestPhone(t, sink)
	p.SetTLSDir(t.TempDir())
	// 先注册一次（本地 TLS 监听随 Register 启动），再取监听端口
	acc := config.Account{Server: "127.0.0.1", Port: 5099, Transport: "tls", Account: "1001", Password: "s", Expires: 300}
	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p.mu.Lock()
	tlsPort := p.localTLSPort
	p.mu.Unlock()
	if tlsPort == 0 {
		t.Fatal("本端 TLS 监听未启动")
	}
	// TLS 监听是异步起的：等端口真的可连（否则对端拨号被拒）
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tlsPort), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TLS 监听未就绪: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 对端（UAC）：经 TLS 发 INVITE，answer-after=0 触发本端自动接听；
	// 接完 200 OK 后按 RFC 回 ACK（本端 RespondSDP 会等它）
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("tls-uac"),
		sipgo.WithUserAgenTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	if err != nil {
		t.Fatalf("UA: %v", err)
	}
	t.Cleanup(func() { _ = ua.Close() })
	client, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	// 对端自己的 TLS 监听（本端挂断时发的 BYE 要有个落点）
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("Server: %v", err)
	}
	byes := make(chan struct{}, 4)
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		select {
		case byes <- struct{}{}:
		default:
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	peerLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("对端监听: %v", err)
	}
	t.Cleanup(func() { _ = peerLn.Close() })
	go func() {
		_ = srv.ServeTLS(tls.NewListener(peerLn, &tls.Config{Certificates: []tls.Certificate{testTLSCert(t)}}))
	}()
	_, peerPortStr, _ := net.SplitHostPort(peerLn.Addr().String())
	peerPort, _ := strconv.Atoi(peerPortStr)
	tlsContact := sip.Uri{User: "2901", Host: "127.0.0.1", Port: peerPort}
	tlsContact.Scheme = "sips"
	tlsContact.UriParams = sip.NewParams()
	tlsContact.UriParams.Add("transport", "tls")
	dcc := sipgo.NewDialogClientCache(client, sip.ContactHeader{Address: tlsContact})
	recipient := sip.Uri{User: "1001", Host: "127.0.0.1", Port: tlsPort}
	recipient.Scheme = "sips"
	recipient.UriParams = sip.NewParams()
	recipient.UriParams.Add("transport", "tls")
	from := &sip.FromHeader{Address: sip.Uri{User: "2901", Host: "127.0.0.1"}, Params: sip.NewParams()}
	from.Params.Add("tag", sip.GenerateTagN(8))
	to := &sip.ToHeader{Address: sip.Uri{User: "1001", Host: "127.0.0.1"}}
	headers := []sip.Header{
		sip.NewHeader("Content-Type", "application/sdp"),
		sip.NewHeader("Call-Info", "<sip:2901@127.0.0.1>;answer-after=0"),
		from, to,
	}
	dlg, err := dcc.Invite(context.Background(), recipient,
		[]byte("v=0"+crlf+"o=- 1 1 IN IP4 127.0.0.1"+crlf+"s=t"+crlf+
			"c=IN IP4 127.0.0.1"+crlf+"t=0 0"+crlf+"m=audio 9 RTP/AVP 0 8"+crlf),
		headers...)
	if err != nil {
		t.Fatalf("发送 INVITE: %v", err)
	}
	if err := dlg.WaitAnswer(context.Background(), sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("未收到本端 200 OK（接听失败）: %v", err)
	}
	// 对端 ACK：本端 RespondSDP 等到它才会走"接通"流程
	if err := dlg.Ack(context.Background()); err != nil {
		t.Fatalf("发送 ACK: %v", err)
	}
	if !sink.waitEvent("established:", 3*time.Second) {
		t.Fatalf("本端未建立通话（200 OK 发坏 / 事务被终止）: %v", sink.events)
	}
	// 本端挂断：BYE 必须送到对端（无路由集时用远端 Contact，沿用对话传输 = TLS）
	time.Sleep(300 * time.Millisecond)
	p.mu.Lock()
	callID := ""
	for id := range p.calls {
		callID = id
	}
	p.mu.Unlock()
	if callID == "" {
		t.Fatal("本端没有进行中的通话")
	}
	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	select {
	case <-byes:
	case <-time.After(3 * time.Second):
		t.Fatal("对端未收到 BYE（本端挂断没能送达）")
	}
}
