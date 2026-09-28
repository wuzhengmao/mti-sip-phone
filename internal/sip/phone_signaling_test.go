package sip

// 完整信令测试：覆盖 CANCEL / BYE / 603 / 200 竞态等挂断场景，
// 回归防护「通话窗口没有挂断 / 挂断了对方电话没挂断」一类问题。

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/media"
)

// testSDPOffer 测试用 SDP offer（对端 UAC 发出）。
const testSDPOffer = "v=0\r\n" +
	"o=- 1 1 IN IP4 127.0.0.1\r\n" +
	"s=test\r\n" +
	"c=IN IP4 127.0.0.1\r\n" +
	"t=0 0\r\n" +
	"m=audio 4000 RTP/AVP 0 8\r\n" +
	"a=rtpmap:0 PCMU/8000\r\n" +
	"a=rtpmap:8 PCMA/8000\r\n"

// testSDPAnswer 测试用 SDP answer（对端 UAS 发出）。
const testSDPAnswer = "v=0\r\n" +
	"o=- 1 1 IN IP4 127.0.0.1\r\n" +
	"s=test\r\n" +
	"c=IN IP4 127.0.0.1\r\n" +
	"t=0 0\r\n" +
	"m=audio 4002 RTP/AVP 0 8\r\n" +
	"a=rtpmap:0 PCMU/8000\r\n" +
	"a=rtpmap:8 PCMA/8000\r\n"

// ---------------------------------------------------------------------------
// 对端 UAC（真实 sipgo 栈）：INVITE / WaitAnswer / CANCEL / BYE
// ---------------------------------------------------------------------------

// testUAC 呼入测试中的对端主叫方。
type testUAC struct {
	ua     *sipgo.UserAgent
	server *sipgo.Server
	dcc    *sipgo.DialogClientCache

	mu           sync.Mutex
	byes         []string    // 收到的 BYE Call-ID
	waitErrs     []error     // WaitAnswer 返回的错误
	reInviteDirs []string    // 收到的 re-INVITE 媒体方向（保持测试用）
	lastReInvite *sip.Request // 最近收到的 re-INVITE（ACK 校验基准）
	ackState     int         // re-INVITE ACK 校验状态：0=等待 1=OK 2=头不一致 3=超时未到
}

func newTestUAC(t *testing.T) *testUAC {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-uac"))
	if err != nil {
		t.Fatalf("创建测试 UAC UA 失败: %v", err)
	}
	client, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatalf("创建测试 UAC client 失败: %v", err)
	}
	server, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建测试 UAC server 失败: %v", err)
	}
	u := &testUAC{ua: ua, server: server}
	// 对端需要监听 UDP 以接收 BYE（Contact 携带真实端口）
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("测试 UAC 监听失败: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)
	go func() { _ = server.ServeUDP(ln) }()
	t.Cleanup(func() { _ = ln.Close(); _ = ua.Close() })
	u.dcc = sipgo.NewDialogClientCache(client, sip.ContactHeader{
		Address: sip.Uri{User: "2001", Host: "127.0.0.1", Port: port},
	})
	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		u.mu.Lock()
		u.byes = append(u.byes, req.CallID().Value())
		u.mu.Unlock()
	})
	server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {})
	return u
}

// trackInvites 开启通话中 re-INVITE（保持/恢复）的处理：记录媒体方向并
// 回 200 + SDP answer；随后在 OnAck 中校验 ACK 的 From/CSeq 与 re-INVITE
// 一致（2xx 的 ACK 是独立事务、经 OnAck 分发；呼入场景 ACK From 曾误用
// 初始 INVITE 的对端身份，回归防护）。
func (u *testUAC) trackInvites() {
	u.server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		dir := media.SDPDirection(req.Body())
		u.mu.Lock()
		u.reInviteDirs = append(u.reInviteDirs, dir)
		u.lastReInvite = req.Clone()
		u.mu.Unlock()
		res := sip.NewSDPResponseFromRequest(req, []byte(testSDPAnswer))
		_ = tx.Respond(res)
	})
	u.server.OnAck(func(ack *sip.Request, tx sip.ServerTransaction) {
		u.mu.Lock()
		req := u.lastReInvite
		u.mu.Unlock()
		if req == nil {
			return
		}
		reqTag, _ := req.From().Params.Get("tag")
		ackTag, _ := ack.From().Params.Get("tag")
		u.mu.Lock()
		if reqTag != ackTag || ack.CSeq().SeqNo != req.CSeq().SeqNo ||
			ack.CSeq().MethodName != sip.ACK {
			u.ackState = 2
		} else {
			u.ackState = 1
		}
		u.mu.Unlock()
	})
}

// waitAckState 等待 ACK 校验出结果（最多 6 秒），返回状态。
func (u *testUAC) waitAckState(timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		s := u.ackState
		u.mu.Unlock()
		if s != 0 {
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

// waitInviteDir 等待收到指定媒体方向的 re-INVITE。
func (u *testUAC) waitInviteDir(dir string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		for _, d := range u.reInviteDirs {
			if d == dir {
				u.mu.Unlock()
				return true
			}
		}
		u.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// invite 向 remoteAddr 发起 INVITE（R-URI 账号 1001），后台等待应答。
// onResp 在每个响应到达时回调（用于 180 到达后触发取消）。
func (u *testUAC) invite(t *testing.T, remoteAddr string, onResp func(*sip.Response)) (*sipgo.DialogClientSession, context.CancelFunc) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		t.Fatalf("对端地址非法: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	recipient := sip.Uri{User: "1001", Host: host, Port: port}
	from := &sip.FromHeader{Address: sip.Uri{User: "2001", Host: host}, Params: sip.NewParams()}
	from.Params.Add("tag", sip.GenerateTagN(8))
	to := &sip.ToHeader{Address: sip.Uri{User: "1001", Host: host}}
	dlg, err := u.dcc.Invite(context.Background(), recipient, []byte(testSDPOffer),
		from, to, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		t.Fatalf("对端 INVITE 失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		err := dlg.WaitAnswer(ctx, sipgo.AnswerOptions{
			OnResponse: func(res *sip.Response) error {
				if onResp != nil {
					onResp(res)
				}
				return nil
			},
		})
		u.mu.Lock()
		u.waitErrs = append(u.waitErrs, err)
		u.mu.Unlock()
	}()
	return dlg, cancel
}

// waitBye 等待收到 BYE。
func (u *testUAC) waitBye(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		n := len(u.byes)
		u.mu.Unlock()
		if n > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitAnswerOK 等待 WaitAnswer 成功返回（对端已收到 200，可发 ACK）。
func (u *testUAC) waitAnswerOK(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		for _, err := range u.waitErrs {
			if err == nil {
				u.mu.Unlock()
				return true
			}
		}
		u.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitErrStatusCode 等待 WaitAnswer 返回携带指定状态码的错误（如 603）。
func (u *testUAC) waitErrStatusCode(code int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		for _, err := range u.waitErrs {
			if dlgErr, ok := err.(*sipgo.ErrDialogResponse); ok && dlgErr.Res.StatusCode == code {
				u.mu.Unlock()
				return true
			}
		}
		u.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// 对端 UAS（真实 sipgo 栈）：180 →（延迟）200 → 记录 CANCEL/BYE
// ---------------------------------------------------------------------------

// peerServer 呼出测试中的对端被叫方。
type peerServer struct {
	ua  *sipgo.UserAgent
	srv *sipgo.Server

	mu           sync.Mutex
	cancels      []string // 收到的 CANCEL Call-ID
	byes         []string // 收到的 BYE Call-ID
	inviteDone   []string // INVITE 事务终止的 Call-ID（CANCEL 会终止 INVITE 事务）
	reInviteDirs []string // 收到的 re-INVITE 媒体方向（保持测试用）
	listenPort   int      // 本地监听端口（200 的 Contact 用）
}

// newPeerServer 启动对端 UAS。answerDelay<=0 时收到 INVITE 只回 180 不接听。
func newPeerServer(t *testing.T, answerDelay time.Duration) (addr string, peer *peerServer) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-peer"))
	if err != nil {
		t.Fatalf("创建对端 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建对端 server 失败: %v", err)
	}
	peer = &peerServer{ua: ua, srv: srv}

	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// re-INVITE（To 带 tag，通话中保持/恢复）：记录媒体方向并直接回 200
		if req.To() != nil && req.To().Params != nil {
			if _, ok := req.To().Params.Get("tag"); ok {
				peer.mu.Lock()
				peer.reInviteDirs = append(peer.reInviteDirs, media.SDPDirection(req.Body()))
				peer.mu.Unlock()
				res := sip.NewSDPResponseFromRequest(req, []byte(testSDPAnswer))
				_ = tx.Respond(res)
				go func() {
					select {
					case <-tx.Acks():
					case <-time.After(5 * time.Second):
					}
				}()
				return
			}
		}
		// 初始呼叫流程
		callID := req.CallID().Value()
		// 观察 INVITE 事务终止：CANCEL 到达后 sipgo 事务层自动回 200+487
		// 并终止事务（此时 OnCancel handler 不经过事务匹配路径不会被调用）
		go func() {
			<-tx.Done()
			peer.mu.Lock()
			peer.inviteDone = append(peer.inviteDone, callID)
			peer.mu.Unlock()
		}()
		_ = tx.Respond(sip.NewResponseFromRequest(req, 180, "Ringing", nil))
		if answerDelay > 0 {
			go func() {
				time.Sleep(answerDelay)
				res := sip.NewResponseFromRequest(req, 200, "OK", []byte(testSDPAnswer))
				res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
				// 200 必须携带 Contact（RFC 3261），re-INVITE 的目标地址依赖它
				res.AppendHeader(&sip.ContactHeader{Address: sip.Uri{User: "1002", Host: "127.0.0.1", Port: peer.listenPort}})
				_ = tx.Respond(res)
			}()
		}
	})
	srv.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		// 事务不匹配路径的兜底记录（匹配路径由事务层自动处理）
		peer.mu.Lock()
		peer.cancels = append(peer.cancels, req.CallID().Value())
		peer.mu.Unlock()
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
		peer.mu.Lock()
		peer.byes = append(peer.byes, req.CallID().Value())
		peer.mu.Unlock()
	})
	srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {})

	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("对端监听失败: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.LocalAddr().String())
	peer.listenPort, _ = strconv.Atoi(portStr)
	t.Cleanup(func() { _ = ln.Close(); _ = ua.Close() })
	go func() { _ = srv.ServeUDP(ln) }()
	return ln.LocalAddr().String(), peer
}

// waitInviteDone 等待 INVITE 事务终止（CANCEL 到达并处理完毕的标志）。
func (p *peerServer) waitInviteDone(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		n := len(p.inviteDone)
		p.mu.Unlock()
		if n > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitBye 等待收到 BYE。
func (p *peerServer) waitBye(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		n := len(p.byes)
		p.mu.Unlock()
		if n > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitInviteDir 等待收到指定媒体方向的 re-INVITE。
func (p *peerServer) waitInviteDir(dir string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		for _, d := range p.reInviteDirs {
			if d == dir {
				p.mu.Unlock()
				return true
			}
		}
		p.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// 手动对端（原始 UDP）：精确控制响应时序，复现 CANCEL 与 200 的竞态
// ---------------------------------------------------------------------------

// manualPeer 按请求类型执行自定义响应的原始 UDP 对端。
type manualPeer struct {
	ln net.PacketConn

	mu     sync.Mutex
	events []string // 收到的请求类型序列（INVITE/CANCEL/ACK/BYE）
	invite *sip.Request
}

// newManualPeer 启动手动对端，返回地址与收到请求的事件序列。
func newManualPeer(t *testing.T) (addr string, peer *manualPeer) {
	t.Helper()
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("手动对端监听失败: %v", err)
	}
	peer = &manualPeer{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, raddr, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			msg, err := sip.ParseMessage(append([]byte{}, buf[:n]...))
			if err != nil {
				continue
			}
			req, ok := msg.(*sip.Request)
			if !ok {
				continue
			}
			peer.mu.Lock()
			peer.events = append(peer.events, string(req.Method))
			peer.mu.Unlock()
			go peer.handle(req, raddr)
		}
	}()
	return ln.LocalAddr().String(), peer
}

// handle 手动对端的响应策略：
//   - INVITE → 180（并保存请求，供 CANCEL 后对 INVITE 补 200 使用）
//   - CANCEL → 200（对 CANCEL 事务），随后对 INVITE 补 200（模拟竞态：取消后仍回 2xx）
//   - ACK   → 记录（不响应）
//   - BYE   → 200
func (p *manualPeer) handle(req *sip.Request, raddr net.Addr) {
	switch req.Method {
	case sip.INVITE:
		p.mu.Lock()
		p.invite = req.Clone()
		p.mu.Unlock()
		res := sip.NewResponseFromRequest(req, 180, "Ringing", nil)
		_, _ = p.ln.WriteTo([]byte(res.String()), raddr)
	case sip.CANCEL:
		// 先回 CANCEL 的 200
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		_, _ = p.ln.WriteTo([]byte(res.String()), raddr)
		// 竞态：对 INVITE 事务再回 200（模拟对端在取消前已完成接听）。
		// 注意必须用保存的 INVITE 请求构造响应（CANCEL 与 INVITE 的
		// CSeq 方法不同，用 CANCEL 构造的 200 无法被对端事务层匹配）。
		go func() {
			time.Sleep(50 * time.Millisecond)
			p.mu.Lock()
			inviteReq := p.invite
			p.mu.Unlock()
			if inviteReq == nil {
				return
			}
			inviteRes := sip.NewResponseFromRequest(inviteReq, 200, "OK", []byte(testSDPAnswer))
			inviteRes.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			_, _ = p.ln.WriteTo([]byte(inviteRes.String()), raddr)
		}()
	case sip.ACK:
		// 无需响应
	case sip.BYE:
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		_, _ = p.ln.WriteTo([]byte(res.String()), raddr)
	}
}

// hasEvent 检查请求事件序列中是否出现指定方法。
func (p *manualPeer) hasEvent(method string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		for _, e := range p.events {
			if e == method {
				p.mu.Unlock()
				return true
			}
		}
		p.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// 本地 Phone 监听辅助
// ---------------------------------------------------------------------------

// newTestPhoneListening 创建 Phone 并监听 UDP（呼入通道），返回监听地址。
func newTestPhoneListening(t *testing.T, sink core.EventSink) (*Phone, string) {
	t.Helper()
	p := newTestPhone(t, sink)
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Phone 监听失败: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)
	p.localPort = port // 同包测试可直接设置（Contact 头构造用）
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = p.server.ServeUDP(ln) }()
	return p, ln.LocalAddr().String()
}

// firstIncomingCallID 取第一个来电事件的 callID（incoming:callID 格式）。
func (s *testSink) firstIncomingCallID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if len(e) > len("incoming:") && e[:len("incoming:")] == "incoming:" {
			return e[len("incoming:"):]
		}
	}
	return ""
}

// waitTerminated 等待指定通话的结束事件并返回原因。
func (s *testSink) waitTerminated(callID string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	prefix := "terminated:" + callID + ":"
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, e := range s.events {
			if len(e) > len(prefix) && e[:len(prefix)] == prefix {
				reason := e[len(prefix):]
				s.mu.Unlock()
				return reason, true
			}
		}
		s.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	return "", false
}

// ---------------------------------------------------------------------------
// 测试用例
// ---------------------------------------------------------------------------

// startEarlyMediaPeer 手动对端：INVITE → 180 → 183（早期媒体 SDP，
// 服务器回铃音）→ 200（正式 SDP）。用于早期媒体接收的回归测试。
func startEarlyMediaPeer(t *testing.T) string {
	t.Helper()
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("早期媒体对端监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, raddr, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			msg, err := sip.ParseMessage(append([]byte{}, buf[:n]...))
			if err != nil {
				continue
			}
			req, ok := msg.(*sip.Request)
			if !ok || req.Method != sip.INVITE {
				continue
			}
			// 180 → 183（带早期媒体 SDP）→ 200
			_ = func() error { _, err := ln.WriteTo([]byte(sip.NewResponseFromRequest(req, 180, "Ringing", nil).String()), raddr); return err }()
			go func(req *sip.Request) {
				time.Sleep(100 * time.Millisecond)
				res183 := sip.NewResponseFromRequest(req, 183, "Session Progress", []byte(testSDPAnswer))
				res183.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
				_, _ = ln.WriteTo([]byte(res183.String()), raddr)
				time.Sleep(500 * time.Millisecond)
				res200 := sip.NewResponseFromRequest(req, 200, "OK", []byte(testSDPAnswer))
				res200.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
				_, _ = ln.WriteTo([]byte(res200.String()), raddr)
			}(req)
		}
	}()
	return ln.LocalAddr().String()
}

// TestIncomingBusyWhileActive 单通话限制：已有通话时新来电直接回 486
//（USER_BUSY），不进入振铃流程。
func TestIncomingBusyWhileActive(t *testing.T) {
	sink := &testSink{}
	p, addr := newTestPhoneListening(t, sink)
	uac1 := newTestUAC(t)

	// 第一通：呼入并接听（进入通话状态）
	dlg1, _ := uac1.invite(t, addr, nil)
	if !sink.waitEvent("incoming:", 5*time.Second) {
		t.Fatal("本地未收到来电事件")
	}
	callID1 := sink.firstIncomingCallID()
	if err := p.Answer(callID1); err != nil {
		t.Fatalf("Answer 失败: %v", err)
	}
	if !uac1.waitAnswerOK(5 * time.Second) {
		t.Fatal("对端未收到 200")
	}
	if err := dlg1.Ack(context.Background()); err != nil {
		t.Fatalf("对端 ACK 失败: %v", err)
	}
	if !sink.waitEvent("established:"+callID1, 5*time.Second) {
		t.Fatal("第一通未进入通话状态")
	}

	// 第二通：新来电 → 486 Busy Here
	uac2 := newTestUAC(t)
	uac2.invite(t, addr, nil)
	if !uac2.waitErrStatusCode(486, 5*time.Second) {
		t.Fatal("已有通话时新来电应被 486（USER_BUSY）拒绝")
	}

	if err := p.Hangup(callID1); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
}

// TestEarlyMediaRingback 呼出振铃中服务器下发 183 早期媒体（回铃音）：
// 本地必须立即建立媒体接收（引擎启动、远端地址就位），接通后复用
// 同一引擎而不重复启动。
func TestEarlyMediaRingback(t *testing.T) {
	addr := startEarlyMediaPeer(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	_ = p.Register(context.Background(), acc)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	if !sink.waitEvent("ringing:"+callID, 5*time.Second) {
		t.Fatal("未收到呼出振铃事件")
	}

	// 183 到达后媒体引擎应立即启动接收（mediaStarted）
	deadline := time.Now().Add(3 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		p.mu.Lock()
		cs := p.calls[callID]
		p.mu.Unlock()
		if cs != nil && cs.mediaStarted {
			started = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !started {
		t.Fatal("183 早期媒体到达后媒体引擎未启动（回铃音将无法播放）")
	}

	// 接通后正常建立（引擎复用，不重复启动）
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("通话未建立")
	}

	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
	if !sink.waitEvent("terminated:"+callID, 3*time.Second) {
		t.Fatal("本地未上报通话结束")
	}
}

// ---------------------------------------------------------------------------
// 测试用例
// ---------------------------------------------------------------------------

// TestOutgoingCancelDuringRinging 呼出振铃中本地挂断：对端必须收到 CANCEL
//（表现为 INVITE 事务被终止），本地上报通话结束。
func TestOutgoingCancelDuringRinging(t *testing.T) {
	addr, peer := newPeerServer(t, -1) // 只回 180 不接听
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	_ = p.Register(context.Background(), acc)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	if !sink.waitEvent("ringing:"+callID, 5*time.Second) {
		t.Fatal("未收到呼出振铃事件")
	}

	// 振铃中挂断 → 应发送 CANCEL
	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
	if !sink.waitEvent("terminated:"+callID, 3*time.Second) {
		t.Fatal("本地未上报通话结束")
	}
	// CANCEL 到达对端 → sipgo 事务层自动回 200+487 并终止 INVITE 事务
	if !peer.waitInviteDone(5 * time.Second) {
		t.Fatal("对端 INVITE 事务未终止：CANCEL 未到达或未生效")
	}
}

// TestOutgoingCancelRaceWith200 CANCEL 与对端 200 竞态（RFC 3261 §9.1）：
// 本地挂断发出 CANCEL 后对端仍回了 2xx，本地必须补 ACK + BYE 结束会话，
// 否则对端会一直保持通话。
func TestOutgoingCancelRaceWith200(t *testing.T) {
	addr, peer := newManualPeer(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	_ = p.Register(context.Background(), acc)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	if !sink.waitEvent("ringing:"+callID, 5*time.Second) {
		t.Fatal("未收到呼出振铃事件")
	}

	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
	if !sink.waitEvent("terminated:"+callID, 3*time.Second) {
		t.Fatal("本地未上报通话结束")
	}

	// 竞态补偿：本地必须补发 ACK + BYE 给对端的 200
	if !peer.hasEvent("ACK", 5*time.Second) {
		t.Fatal("取消后收到对端 200，本地未补发 ACK")
	}
	if !peer.hasEvent("BYE", 5*time.Second) {
		t.Fatal("取消后收到对端 200，本地未补发 BYE")
	}
}

// TestOutgoingHangupSendsBye 呼出接通后本地挂断：对端必须收到 BYE。
func TestOutgoingHangupSendsBye(t *testing.T) {
	addr, peer := newPeerServer(t, 300*time.Millisecond)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	_ = p.Register(context.Background(), acc)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("通话未建立")
	}

	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
	if !sink.waitEvent("terminated:"+callID, 3*time.Second) {
		t.Fatal("本地未上报通话结束")
	}
	if !peer.waitBye(5 * time.Second) {
		t.Fatal("对端未收到 BYE：本地挂断未通知对端")
	}
}

// TestIncomingCancelRemote 呼入振铃中对端取消（CANCEL）：本地窗口必须结束
// 并上报 remote_cancel。
func TestIncomingCancelRemote(t *testing.T) {
	sink := &testSink{}
	_, addr := newTestPhoneListening(t, sink)
	uac := newTestUAC(t)

	ringing := make(chan struct{}, 1)
	_, cancel := uac.invite(t, addr, func(res *sip.Response) {
		if res.StatusCode == 180 {
			select {
			case ringing <- struct{}{}:
			default:
			}
		}
	})

	if !sink.waitEvent("incoming:", 5*time.Second) {
		t.Fatal("本地未收到来电事件")
	}
	// 等 180 到达对端后取消呼叫（保证 INVITE 事务已建立）
	select {
	case <-ringing:
	case <-time.After(5 * time.Second):
		t.Fatal("对端未收到 180")
	}
	cancel()

	// 本地应上报 remote_cancel（对端取消）
	reason, ok := sink.waitTerminated(sink.firstIncomingCallID(), 5*time.Second)
	if !ok {
		t.Fatal("对端取消后本地未上报通话结束（CANCEL 未生效）")
	}
	if reason != "remote_cancel" {
		t.Fatalf("终止原因应为 remote_cancel，实际: %q", reason)
	}
}

// TestIncomingRejectSends603 呼入振铃中本地挂断（拒接）：对端必须收到 603。
func TestIncomingRejectSends603(t *testing.T) {
	sink := &testSink{}
	p, addr := newTestPhoneListening(t, sink)
	uac := newTestUAC(t)

	uac.invite(t, addr, nil)
	if !sink.waitEvent("incoming:", 5*time.Second) {
		t.Fatal("本地未收到来电事件")
	}
	callID := sink.firstIncomingCallID()
	if callID == "" {
		t.Fatal("未解析到来电 callID")
	}

	// 等 180 到达对端（本地回 180 在 handleIncoming 中同步执行）
	time.Sleep(300 * time.Millisecond)

	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
	if !sink.waitEvent("terminated:"+callID, 3*time.Second) {
		t.Fatal("本地未上报通话结束")
	}
	if !uac.waitErrStatusCode(603, 5*time.Second) {
		t.Fatal("对端未收到 603：振铃中挂断未拒接通知对端")
	}
}

// TestIncomingRemoteBye 呼入接通后对端挂断（BYE）：本地必须结束通话并
// 上报 remote_hangup。
func TestIncomingRemoteBye(t *testing.T) {
	sink := &testSink{}
	p, addr := newTestPhoneListening(t, sink)
	uac := newTestUAC(t)

	dlg, _ := uac.invite(t, addr, nil)
	if !sink.waitEvent("incoming:", 5*time.Second) {
		t.Fatal("本地未收到来电事件")
	}
	callID := sink.firstIncomingCallID()

	// 本地接听（RespondSDP 发出 200 后阻塞等待对端 ACK）
	if err := p.Answer(callID); err != nil {
		t.Fatalf("Answer 失败: %v", err)
	}
	// 对端收到 200 后发 ACK，本地 dialog 确认、接通流程继续
	if !uac.waitAnswerOK(5 * time.Second) {
		t.Fatal("对端未收到 200")
	}
	if err := dlg.Ack(context.Background()); err != nil {
		t.Fatalf("对端 ACK 失败: %v", err)
	}
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("本地未进入通话状态")
	}

	// 对端发 BYE
	if err := dlg.Bye(context.Background()); err != nil {
		t.Fatalf("对端 BYE 失败: %v", err)
	}
	reason, ok := sink.waitTerminated(callID, 5*time.Second)
	if !ok {
		t.Fatal("对端挂断后本地未上报通话结束（BYE 未生效）")
	}
	if reason != "remote_hangup" {
		t.Fatalf("终止原因应为 remote_hangup，实际: %q", reason)
	}
}

// TestIncomingLocalHangupSendsBye 呼入接通后本地挂断：对端必须收到 BYE。
func TestIncomingLocalHangupSendsBye(t *testing.T) {
	sink := &testSink{}
	p, addr := newTestPhoneListening(t, sink)
	uac := newTestUAC(t)

	dlg, _ := uac.invite(t, addr, nil)
	if !sink.waitEvent("incoming:", 5*time.Second) {
		t.Fatal("本地未收到来电事件")
	}
	callID := sink.firstIncomingCallID()

	if err := p.Answer(callID); err != nil {
		t.Fatalf("Answer 失败: %v", err)
	}
	// 对端收到 200 后发 ACK
	if !uac.waitAnswerOK(5 * time.Second) {
		t.Fatal("对端未收到 200")
	}
	if err := dlg.Ack(context.Background()); err != nil {
		t.Fatalf("对端 ACK 失败: %v", err)
	}
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("本地未进入通话状态")
	}

	// 本地挂断 → 对端收到 BYE
	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
	if !sink.waitEvent("terminated:"+callID, 3*time.Second) {
		t.Fatal("本地未上报通话结束")
	}
	if !uac.waitBye(5 * time.Second) {
		t.Fatal("对端未收到 BYE：呼入挂断未通知对端")
	}
}

// TestIncomingHangupBeforeAck 呼入接听后、对端 ACK 到达前本地挂断：
// 此前会误判为"振铃中"误发 603；修复后应发 BYE（sipgo 的 WriteBye
// 等待 dialog 确认后发出），对端补 ACK 后必须收到 BYE。
func TestIncomingHangupBeforeAck(t *testing.T) {
	sink := &testSink{}
	p, addr := newTestPhoneListening(t, sink)
	uac := newTestUAC(t)

	// 对端收到 200 后延迟补 ACK（留出挂断窗口）
	got200 := make(chan struct{}, 1)
	dlg, _ := uac.invite(t, addr, func(res *sip.Response) {
		if res.StatusCode == 200 {
			select {
			case got200 <- struct{}{}:
			default:
			}
		}
	})
	if !sink.waitEvent("incoming:", 5*time.Second) {
		t.Fatal("本地未收到来电事件")
	}
	callID := sink.firstIncomingCallID()

	// 接听（RespondSDP 发出 200 后阻塞等待 ACK）
	if err := p.Answer(callID); err != nil {
		t.Fatalf("Answer 失败: %v", err)
	}

	// 在 ACK 到达前挂断（200 已发出、本地 established 尚未置位）
	select {
	case <-got200:
	case <-time.After(5 * time.Second):
		t.Fatal("对端未收到 200")
	}
	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
	if !sink.waitEvent("terminated:"+callID, 3*time.Second) {
		t.Fatal("本地未上报通话结束")
	}

	// 对端补发 ACK → 本地 dialog 确认 → 后台 BYE 发出
	if err := dlg.Ack(context.Background()); err != nil {
		t.Fatalf("对端 ACK 失败: %v", err)
	}
	if !uac.waitBye(5 * time.Second) {
		t.Fatal("对端补 ACK 后仍未收到 BYE：接听未确认阶段挂断未通知对端")
	}
}

// testAccount 构造测试账户。
func testAccount(host string, port int) config.Account {
	return config.Account{
		Server:    host,
		Port:      port,
		Transport: "udp",
		Account:   "1001",
		Password:  "secret",
	}
}
