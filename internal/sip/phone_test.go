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
	"mti-sip-phone/internal/core"
)

// testSink 收集 Phone 上报的事件，供断言。
type testSink struct {
	mu     sync.Mutex
	regs   []core.RegisterState
	events []string
	calls  []string
}

func (s *testSink) OnIncoming(callID, remote, remoteName string, hasVideo bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "incoming:"+callID)
}

func (s *testSink) OnVideoState(callID string, st core.VideoState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "video:"+callID)
}
func (s *testSink) OnRinging(callID string, earlyMedia bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "ringing:"+callID)
}
func (s *testSink) OnEstablished(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "established:"+callID)
}
func (s *testSink) OnTerminated(callID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "terminated:"+callID+":"+reason)
}
func (s *testSink) OnFailed(callID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "failed:"+callID+":"+reason)
}
func (s *testSink) OnRegisterState(state core.RegisterState, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regs = append(s.regs, state)
}

func (s *testSink) hasEvent(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// regStates 注册状态序列快照（带锁，供断言"先掉线再重注册"的先后顺序）。
func (s *testSink) regStates() []core.RegisterState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.RegisterState(nil), s.regs...)
}

func (s *testSink) lastReg() core.RegisterState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.regs) == 0 {
		return ""
	}
	return s.regs[len(s.regs)-1]
}

// waitRegState 轮询等待注册状态达到 want。
func (s *testSink) waitRegState(want core.RegisterState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.lastReg() == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (s *testSink) waitEvent(prefix string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.hasEvent(prefix) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// regRecord 测试服务器收到的一条 REGISTER（账号 + Expires），
// 供换账号先注销旧账号的断言用（只记带 Authorization 的那条，
// 即真正生效的注册/注销请求）。
type regRecord struct {
	user    string
	expires int
}

// regRecorder 线程安全地记录收到的 REGISTER。
type regRecorder struct {
	mu   sync.Mutex
	regs []regRecord
}

func (r *regRecorder) add(user string, expires int) {
	r.mu.Lock()
	r.regs = append(r.regs, regRecord{user: user, expires: expires})
	r.mu.Unlock()
}

func (r *regRecorder) list() []regRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]regRecord(nil), r.regs...)
}

// startTestServer 启动一个迷你 SIP 服务器（对端）：
//   - REGISTER：首次回 401（要求 Digest），带凭据后回 200
//   - INVITE：回 180 再 200（携带 SDP answer）
//   - BYE：回 200
func startTestServer(t *testing.T) (addr string) {
	addr, _ = startTestServerRecording(t)
	return addr
}

// startTestServerRecording 同 startTestServer，额外返回 REGISTER 记录器。
func startTestServerRecording(t *testing.T) (addr string, rec *regRecorder) {
	t.Helper()
	rec = &regRecorder{}
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-server"))
	if err != nil {
		t.Fatalf("创建测试服务器 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建测试服务器失败: %v", err)
	}

	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		if req.GetHeader("Authorization") == nil {
			res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
			res.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest realm="mti-test", nonce="abc123", algorithm=MD5`))
			_ = tx.Respond(res)
			return
		}
		// 记录带凭据的 REGISTER（真正生效的注册/注销）
		user := ""
		if to := req.To(); to != nil {
			user = to.Address.User
		}
		expires := -1
		if eh := req.GetHeader("Expires"); eh != nil {
			if v, err := strconv.Atoi(eh.Value()); err == nil {
				expires = v
			}
		}
		rec.add(user, expires)
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "3600"))
		_ = tx.Respond(res)
	})

	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// 断言 INVITE 报文完整性：From/To 头必须存在（缺失会导致
		// OpenSIPS "bad from header"，回归防护）
		if req.From() == nil {
			t.Errorf("INVITE 缺少 From 头")
		}
		if req.To() == nil {
			t.Errorf("INVITE 缺少 To 头")
		}
		if req.From() != nil && req.From().Address.User != "1001" {
			t.Errorf("INVITE From 用户名应为 1001，实际 %q", req.From().Address.User)
		}
		// 回 180，间隔后回 200（模拟真实振铃时序，避免连发竞态）
		_ = tx.Respond(sip.NewResponseFromRequest(req, 180, "Ringing", nil))
		go func() {
			time.Sleep(300 * time.Millisecond)
			answer := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=test\r\nc=IN IP4 127.0.0.1\r\n" +
				"t=0 0\r\nm=audio 9 RTP/AVP 0 8\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\n"
			res := sip.NewResponseFromRequest(req, 200, "OK", []byte(answer))
			res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			_ = tx.Respond(res)
		}()
	})

	// ACK 无需响应，注册 handler 避免默认 405 告警
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {})

	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})

	// 监听 UDP 随机端口
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听 UDP 失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = srv.ServeUDP(ln) }()
	return ln.LocalAddr().String(), rec
}

// newTestPhone 创建被测 Phone（无音频管理器）。
func newTestPhone(t *testing.T, sink core.EventSink) *Phone {
	t.Helper()
	p, err := NewPhone(sink, nil, true)
	if err != nil {
		t.Fatalf("创建 Phone 失败: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// TestRegisterFlow 注册流程：401 挑战 → Digest 认证 → 200 注册成功。
func TestRegisterFlow(t *testing.T) {
	addr := startTestServer(t)
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
		Password:  "secret",
	}
	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 返回错误: %v", err)
	}

	// 等待 registered 状态（注册成功在后台循环中回调）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sink.lastReg() == core.RegRegistered {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sink.lastReg() != core.RegRegistered {
		t.Fatalf("注册状态应为 registered，实际: %v", sink.regs)
	}
}

// TestDialFlow 呼出流程：INVITE → 180 → 200 → 接通 → BYE。
func TestDialFlow(t *testing.T) {
	addr := startTestServer(t)
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
		Password:  "secret",
	}
	// 先注册（确保账户信息就位，呼出无需注册成功也可）
	_ = p.Register(context.Background(), acc)
	time.Sleep(200 * time.Millisecond)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 返回错误: %v", err)
	}

	if !sink.waitEvent("ringing:"+callID, 5*time.Second) {
		t.Fatal("未收到呼出振铃事件")
	}
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("未收到通话建立事件")
	}

	// 挂断
	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 返回错误: %v", err)
	}
	if !sink.waitEvent("terminated:"+callID, 3*time.Second) {
		t.Fatal("未收到通话结束事件")
	}
}

// TestUnregisterFlow 注销：expires=0 的 REGISTER。
func TestUnregisterFlow(t *testing.T) {
	addr := startTestServer(t)
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
		Password:  "secret",
	}
	_ = p.Register(context.Background(), acc)
	time.Sleep(300 * time.Millisecond)

	if err := p.Unregister(context.Background()); err != nil {
		t.Fatalf("Unregister 返回错误: %v", err)
	}
	if sink.lastReg() != core.RegUnregistered {
		t.Fatalf("注销后状态应为 unregistered，实际: %v", sink.lastReg())
	}
}

// TestSwitchAccountUnregistersOld 换账号（REQ-ACC-011 修订）：p.Register 换成
// 另一个账号时，必须先给**旧账号**发 expires=0 的 REGISTER 让它立即过期
// （否则旧绑定要等自然过期，期间打旧号码仍会送到本机）；且这次注销走**静默
// 路径**——不能把注册状态置成"未注册"（界面会闪、注册看护会被惊动）。
func TestSwitchAccountUnregistersOld(t *testing.T) {
	addr, rec := startTestServerRecording(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	// Expires 按真实配置给（默认 300）：0 表示"注销"，注册测试不能用
	oldAcc := config.Account{Server: host, Port: port, Transport: "udp", Account: "1001", Password: "secret", Expires: 300}
	newAcc := config.Account{Server: host, Port: port, Transport: "udp", Account: "1002", Password: "secret", Expires: 300}

	if err := p.Register(context.Background(), oldAcc); err != nil {
		t.Fatalf("注册旧账号失败: %v", err)
	}
	if !sink.waitRegState(core.RegRegistered, 3*time.Second) {
		t.Fatal("旧账号未注册成功")
	}

	// 换账号：注销旧账号（同步发出）→ 注册新账号
	if err := p.Register(context.Background(), newAcc); err != nil {
		t.Fatalf("换账号失败: %v", err)
	}

	// 等待服务器收到旧账号的 expires=0 与 新账号的注册
	deadline := time.Now().Add(3 * time.Second)
	var oldUnregAt, newRegAt = -1, -1
	for time.Now().Before(deadline) {
		oldUnregAt, newRegAt = -1, -1
		for i, r := range rec.list() {
			if r.user == "1001" && r.expires == 0 && oldUnregAt < 0 {
				oldUnregAt = i
			}
			if r.user == "1002" && r.expires > 0 && newRegAt < 0 {
				newRegAt = i
			}
		}
		if oldUnregAt >= 0 && newRegAt >= 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if oldUnregAt < 0 {
		t.Fatalf("换账号后服务器未收到旧账号（1001）的 expires=0 注销，记录: %+v", rec.list())
	}
	if newRegAt < 0 {
		t.Fatalf("新账号（1002）未注册，记录: %+v", rec.list())
	}
	if oldUnregAt > newRegAt {
		t.Fatalf("旧账号注销应早于新账号注册（先注销后注册），记录: %+v", rec.list())
	}

	// 静默注销：状态流里不应出现 unregistered（只有 registering→registered 的跳变）
	for _, st := range sink.regStates() {
		if st == core.RegUnregistered {
			t.Fatalf("换账号的旧账号注销不应把注册状态置成 unregistered（界面会闪未注册），状态流: %v", sink.regStates())
		}
	}
	if sink.lastReg() != core.RegRegistered {
		t.Fatalf("换账号后最终状态应为 registered，实际: %v", sink.lastReg())
	}
}

// TestSameAccountReregisterNoUnregister 同账号重注册（改密码等）**不**给服务端
// 发注销——只有真正换账号才打扰服务端。
func TestSameAccountReregisterNoUnregister(t *testing.T) {
	addr, rec := startTestServerRecording(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	acc := config.Account{Server: host, Port: port, Transport: "udp", Account: "1001", Password: "secret", Expires: 300}

	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if !sink.waitRegState(core.RegRegistered, 3*time.Second) {
		t.Fatal("未注册成功")
	}

	// 同账号、仅改密码：重注册
	acc2 := acc
	acc2.Password = "newpass"
	if err := p.Register(context.Background(), acc2); err != nil {
		t.Fatalf("重注册失败: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	for _, r := range rec.list() {
		if r.expires == 0 {
			t.Fatalf("同账号重注册不应发 expires=0 注销，记录: %+v", rec.list())
		}
	}
}
