package sip

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

var (
	testCertOnce sync.Once
	testCert     tls.Certificate
)

// testTLSCert 测试用自签证书：**整个包只生成一次**。RSA 2048 生成一次约 100ms，
// 每个用例各生成一份会明显拖慢整套测试，把既有的 5 秒超时 flaky 挤出来。
func testTLSCert(t *testing.T) tls.Certificate {
	t.Helper()
	testCertOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mti-sip-test-cert")
		if err != nil {
			t.Fatalf("临时目录: %v", err)
		}
		cert, err := loadOrCreateCert(dir)
		if err != nil {
			t.Fatalf("生成测试证书: %v", err)
		}
		testCert = cert
	})
	return testCert
}

// TestSelfSignedCertReuse 自签证书：首次生成并落盘，之后复用同一份
// （重启不变，便于服务端固定信任）。
func TestSelfSignedCertReuse(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateCert(dir)
	if err != nil {
		t.Fatalf("生成自签证书失败: %v", err)
	}
	second, err := loadOrCreateCert(dir)
	if err != nil {
		t.Fatalf("二次读取自签证书失败: %v", err)
	}
	if len(first.Certificate) == 0 || len(second.Certificate) == 0 {
		t.Fatal("证书内容为空")
	}
	if string(first.Certificate[0]) != string(second.Certificate[0]) {
		t.Fatal("第二次调用应复用已生成的证书，实际重新生成了")
	}
	// 私钥权限必须是 0600（内含私钥，不能给其他用户读）；
	// Windows 不按 POSIX 权限位落地，只在类 Unix 平台断言
	fi, err := os.Stat(filepath.Join(dir, tlsKeyFile))
	if err != nil {
		t.Fatalf("私钥文件不存在: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("私钥权限应为 0600，实际 %o", perm)
		}
	}
}

// TestClientTLSVerifyToggle 服务器证书校验开关（REQ-ACC-006）：
// 默认不校验（自签证书也能连）；开关打开后按系统根证书校验，自签证书被拒。
func TestClientTLSVerifyToggle(t *testing.T) {
	cert := testTLSCert(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				srv := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
				_ = srv.Handshake()
				_ = srv.Close()
			}()
		}
	}()

	handshake := func(verify bool) error {
		var flag tlsVerifyFlag
		flag.on.Store(verify)
		conn, err := tls.Dial("tcp4", ln.Addr().String(), clientTLSConfig(&flag))
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	}

	if err := handshake(false); err != nil {
		t.Fatalf("默认（不校验）应握手成功，实际失败: %v", err)
	}
	if err := handshake(true); err == nil {
		t.Fatal("开启校验后自签证书应握手失败（系统根证书里没有它）")
	}
}

// TestContactForTransport Contact 地址按传输方式构造：
// udp/tcp 用明文端口与 sip:，tls 用 TLS 端口、sips: 且带 transport=tls。
func TestContactForTransport(t *testing.T) {
	p := newTestPhone(t, &testSink{})
	p.mu.Lock()
	p.localPort = 45678
	p.localTLSPort = 45679
	p.mu.Unlock()

	plain := p.contactFor(config.Account{Account: "1001", Transport: "udp"})
	if plain.Port != 45678 || !strings.HasPrefix(plain.String(), "sip:") {
		t.Fatalf("udp Contact 应为 sip:1001@host:45678，实际 %s", plain.String())
	}
	if plain.UriParams.Has("transport") {
		t.Fatalf("udp Contact 不应带 transport 参数：%s", plain.String())
	}

	tlsContact := p.contactFor(config.Account{Account: "1001", Transport: "tls"})
	s := tlsContact.String()
	if !strings.HasPrefix(s, "sips:") {
		t.Fatalf("tls Contact 应为 sips: 开头，实际 %s", s)
	}
	if tlsContact.Port != 45679 {
		t.Fatalf("tls Contact 端口应为 TLS 监听端口 45679，实际 %d（%s）", tlsContact.Port, s)
	}
	if tr, _ := tlsContact.UriParams.Get("transport"); tr != "tls" {
		t.Fatalf("tls Contact 应带 transport=tls，实际 %q（%s）", tr, s)
	}
	// UDP 不带 transport 参数（缺省即 UDP）
	udp := p.contactFor(config.Account{Account: "1001", Transport: "udp"})
	if udp.UriParams.Has("transport") {
		t.Fatalf("udp Contact 不应带 transport 参数：%s", udp.String())
	}
	// TCP 的 Contact 必须显式写 transport=tcp：sip: URI 不带参数时默认按 UDP 回
	tc := p.contactFor(config.Account{Account: "1001", Transport: "tcp"})
	if tr, _ := tc.UriParams.Get("transport"); tr != "tcp" {
		t.Fatalf("tcp Contact 应带 transport=tcp，实际 %q（%s）", tr, tc.String())
	}
}

// startTestTLSServer 启动一个只认 TLS 的迷你 SIP 服务器（自签证书），
// 记录收到的 REGISTER 供断言；REGISTER 直接回 200（不挑战 Digest，
// 本测试关注的是传输层不是认证）。
func startTestTLSServer(t *testing.T) (addr string, got, invites chan *sip.Request) {
	t.Helper()
	cert := testTLSCert(t)
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("tls-test-server"))
	if err != nil {
		t.Fatalf("创建测试服务器 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建测试服务器失败: %v", err)
	}
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ua.Close()
	})
	got = make(chan *sip.Request, 4)
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		select {
		case got <- req:
		default:
		}
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "300"))
		_ = tx.Respond(res)
	})
	// 呼出：180 → 200（带 SDP answer），模拟真实振铃时序
	invites = make(chan *sip.Request, 4)
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		select {
		case invites <- req:
		default:
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, 180, "Ringing", nil))
		go func() {
			time.Sleep(200 * time.Millisecond)
			answer := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=test\r\nc=IN IP4 127.0.0.1\r\n" +
				"t=0 0\r\nm=audio 9 RTP/AVP 0 8\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\n"
			res := sip.NewResponseFromRequest(req, 200, "OK", []byte(answer))
			res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			_ = tx.Respond(res)
		}()
	})
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {})
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		_ = srv.ServeTLS(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}}))
	}()
	return ln.Addr().String(), got, invites
}

// TestRegisterOverTLS 端到端：transport=tls 时注册报文走 TLS 连接，
// R-URI 与 Contact 都是 sips:、Contact 带 transport=tls、Via 协议为 TLS；
// 同时验证本端 TLS 监听（自签证书）已按需启动。
func TestRegisterOverTLS(t *testing.T) {
	addr, got, _ := startTestTLSServer(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	sink := &testSink{}
	p := newTestPhone(t, sink)
	p.SetTLSDir(filepath.Join(t.TempDir(), "tls"))

	acc := config.Account{
		Server:    host,
		Port:      port,
		Transport: "tls",
		Account:   "1001",
		Password:  "secret",
		Expires:   300,
	}
	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 返回错误: %v", err)
	}

	var req *sip.Request
	select {
	case req = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("TLS 服务器未收到 REGISTER")
	}
	if req.Recipient.Scheme != "sips" {
		t.Errorf("R-URI 应为 sips:，实际 %q（%s）", req.Recipient.Scheme, req.Recipient.String())
	}
	if via := req.Via(); via == nil || !strings.EqualFold(via.Transport, "TLS") {
		t.Errorf("Via 传输应为 TLS，实际 %v", via)
	}
	contact := req.Contact()
	if contact == nil || !strings.HasPrefix(contact.Address.String(), "sips:") {
		t.Errorf("Contact 应为 sips:，实际 %v", contact)
	} else if tr, _ := contact.Address.UriParams.Get("transport"); tr != "tls" {
		t.Errorf("Contact 应带 transport=tls，实际 %q（%s）", tr, contact.Address.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && sink.lastReg() != core.RegRegistered {
		time.Sleep(50 * time.Millisecond)
	}
	if sink.lastReg() != core.RegRegistered {
		t.Fatalf("注册状态应为 registered，实际: %v", sink.regs)
	}

	// 本端 TLS 监听（接收 TLS 呼入）应随注册启动并占一个端口
	p.mu.Lock()
	tlsPort := p.localTLSPort
	p.mu.Unlock()
	if tlsPort == 0 {
		t.Fatal("transport=tls 时应启动本端 TLS 监听")
	}
	if _, err := os.Stat(filepath.Join(p.tlsDir, tlsCertFile)); err != nil {
		t.Fatalf("TLS 监听应生成自签证书: %v", err)
	}
}

// TestDialOverTLS 呼出走 TLS：INVITE / ACK / BYE 都在 TLS 连接上，
// R-URI 为 sips: 且带 transport=tls 参数（sipgo 的 dialog 层据此选传输层）。
func TestDialOverTLS(t *testing.T) {
	addr, _, invites := startTestTLSServer(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	sink := &testSink{}
	p := newTestPhone(t, sink)
	p.SetTLSDir(filepath.Join(t.TempDir(), "tls"))

	acc := config.Account{
		Server:    host,
		Port:      port,
		Transport: "tls",
		Account:   "1001",
		Password:  "secret",
		Expires:   300,
	}
	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 返回错误: %v", err)
	}

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 返回错误: %v", err)
	}

	var invite *sip.Request
	select {
	case invite = <-invites:
	case <-time.After(5 * time.Second):
		t.Fatal("TLS 服务器未收到 INVITE")
	}
	if invite.Recipient.Scheme != "sips" {
		t.Errorf("INVITE R-URI 应为 sips:，实际 %q（%s）", invite.Recipient.Scheme, invite.Recipient.String())
	}
	if tr, _ := invite.Recipient.UriParams.Get("transport"); tr != "tls" {
		t.Errorf("INVITE R-URI 应带 transport=tls，实际 %q（%s）", tr, invite.Recipient.String())
	}
	if via := invite.Via(); via == nil || !strings.EqualFold(via.Transport, "TLS") {
		t.Errorf("INVITE Via 传输应为 TLS，实际 %v", via)
	}
	if contact := invite.Contact(); contact == nil || !strings.HasPrefix(contact.Address.String(), "sips:") {
		t.Errorf("INVITE Contact 应为 sips:，实际 %v", contact)
	}

	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatalf("未收到通话建立事件: %v", sink.events)
	}
	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 返回错误: %v", err)
	}
}

// TestIncomingTLSConnection 本端 TLS 监听可被对端连通：对端（跳过证书校验）
// 按注册 Contact 里的 sips: 地址连过来发 OPTIONS，能拿回响应就说明
// 监听→解析→处理器→同连接回写的整条链路通了（TLS 呼入的前提）。
func TestIncomingTLSConnection(t *testing.T) {
	addr, got, _ := startTestTLSServer(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	sink := &testSink{}
	p := newTestPhone(t, sink)
	p.SetTLSDir(filepath.Join(t.TempDir(), "tls"))
	acc := config.Account{Server: host, Port: port, Transport: "tls", Account: "1001", Password: "s", Expires: 300}
	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 返回错误: %v", err)
	}

	var reg *sip.Request
	select {
	case reg = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("TLS 服务器未收到 REGISTER")
	}
	contact := reg.Contact()
	if contact == nil {
		t.Fatal("REGISTER 缺少 Contact")
	}
	recipient := sip.Uri{}
	if err := sip.ParseUri(contact.Address.String(), &recipient); err != nil {
		t.Fatalf("解析 Contact 失败: %v（%s）", err, contact.Address.String())
	}

	// 对端（自签证书场景跳过校验）
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("tls-peer"),
		sipgo.WithUserAgenTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	if err != nil {
		t.Fatalf("创建对端 UA 失败: %v", err)
	}
	defer ua.Close()
	peer, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatalf("创建对端 Client 失败: %v", err)
	}

	req := sip.NewRequest(sip.OPTIONS, recipient)
	req.SetTransport("TLS")
	res, err := peer.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("经 TLS 向本端发送请求失败: %v", err)
	}
	if res == nil {
		t.Fatal("未收到响应（TLS 监听不可达）")
	}
	t.Logf("本端 TLS 监听响应: %d %s", res.StatusCode, res.Reason)
}

// TestDialogCachesFollowTransport dialog 缓存必须跟着传输方式换（sipgo 要求
// 每种传输一个实例，缓存里存着 Contact 头），但有通话进行中时不换
// （缓存里放着那通电话的 dialog，换掉会让 in-dialog 请求匹配不上）。
func TestDialogCachesFollowTransport(t *testing.T) {
	p := newTestPhone(t, &testSink{})
	p.SetTLSDir(t.TempDir())
	udpAcc := config.Account{Account: "1001", Server: "127.0.0.1", Port: 5060, Transport: "udp"}
	tlsAcc := config.Account{Account: "1001", Server: "127.0.0.1", Port: 5061, Transport: "tls"}

	keyOf := func() string {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.dialogKey
	}

	p.ensureDialogCaches(udpAcc)
	if p.dcc == nil || p.dsc == nil {
		t.Fatal("首次调用应创建 dcc/dsc")
	}
	udpKey := keyOf()
	if !strings.HasPrefix(udpKey, "sip:") {
		t.Fatalf("udp 的 Contact 应为 sip:，实际 %s", udpKey)
	}

	// 传输改成 tls：无通话时应重建（Contact 变 sips: 且带 transport=tls）
	p.ensureDialogCaches(tlsAcc)
	tlsKey := keyOf()
	if !strings.HasPrefix(tlsKey, "sips:") || !strings.Contains(tlsKey, "transport=tls") {
		t.Fatalf("改走 tls 后 Contact 应为 sips:…;transport=tls，实际 %s", tlsKey)
	}

	// 通话进行中：即便传输变了也不换缓存
	p.mu.Lock()
	p.calls["in-progress"] = &callSession{id: "in-progress"}
	p.mu.Unlock()
	p.ensureDialogCaches(udpAcc)
	if got := keyOf(); got != tlsKey {
		t.Fatalf("通话进行中不应重建缓存，实际 %s（期望仍是 %s）", got, tlsKey)
	}
	p.mu.Lock()
	delete(p.calls, "in-progress")
	p.mu.Unlock()
}
