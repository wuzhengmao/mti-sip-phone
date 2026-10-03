package sip

// 注册看护（REQ-CALL-009）的回归测试：系统休眠唤醒、网络出口地址变化、
// 网络恢复后必须立即重新注册。
//
// 检测逻辑用注入的假时钟与假探测函数驱动（watchNow/watchProbe），
// 测试不依赖真实休眠或真实网络切换；实机休眠/切网仍须人工验证。

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/stun"

	"mti-sip-phone/internal/core"
)

// waitRegCount 等待"带凭据的 REGISTER"次数达到 want。
func waitRegCount(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if n.Load() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待注册次数 >= %d 超时，实际 %d", want, n.Load())
}

// waitRegistered 等待注册状态变为 registered。
func waitRegistered(t *testing.T, sink *testSink) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sink.lastReg() == core.RegRegistered {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待注册成功超时，实际状态: %v", sink.lastReg())
}

// startRegCounterTCP 同 stun_test.go 的 startRegCounter，但走 TCP——
// 验证网络变化后能丢弃 sipgo 复用的 TCP 信令连接。
func startRegCounterTCP(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-reg-counter-tcp"))
	if err != nil {
		t.Fatalf("创建计数服务器 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建计数服务器失败: %v", err)
	}
	var n atomic.Int32
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		if req.GetHeader("Authorization") == nil {
			res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
			res.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest realm="mti-test", nonce="abc123", algorithm=MD5`))
			_ = tx.Respond(res)
			return
		}
		n.Add(1)
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "3600"))
		_ = tx.Respond(res)
	})
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("计数服务器监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = ua.Close() })
	go func() { _ = srv.ServeTCP(ln) }()
	return ln.Addr().String(), &n
}

// startFixedStunServer 启动返回固定映射 IP（运行期可改）的 STUN 服务器：
// stun_test.go 的 startTestStunServer 回显源地址（与 localIP 相同），
// 区分不出"网络变化后是否重探到了新公网地址"。
func startFixedStunServer(t *testing.T, ip *atomic.Value) string {
	t.Helper()
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("STUN 服务器监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, raddr, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			msg := &stun.Message{Raw: append([]byte{}, buf[:n]...)}
			if err := msg.Decode(); err != nil || msg.Type != stun.BindingRequest {
				continue
			}
			_, portStr, _ := net.SplitHostPort(raddr.String())
			port, _ := strconv.Atoi(portStr)
			res := stun.MustBuild(stun.NewTransactionIDSetter(msg.TransactionID), stun.BindingSuccess)
			_ = (stun.XORMappedAddress{IP: net.ParseIP(ip.Load().(string)), Port: port}).AddTo(res)
			_, _ = ln.WriteTo(res.Raw, raddr)
		}
	}()
	return ln.LocalAddr().String()
}

// TestOutboundIP 出向地址探测：UDP Dial 只做路由选择，端口无需真实监听。
func TestOutboundIP(t *testing.T) {
	ip, err := probeOutboundIP(context.Background(), "127.0.0.1:1")
	if err != nil {
		t.Fatalf("探测出向地址失败: %v", err)
	}
	if ip != "127.0.0.1" {
		t.Fatalf("出向地址应为 127.0.0.1，实际 %q", ip)
	}
}

// TestNetworkChangeTriggersReRegister 出口地址变化后必须立即重新注册，
// 且地址稳定后不得反复触发。
func TestNetworkChangeTriggersReRegister(t *testing.T) {
	regAddr, regCount := startRegCounter(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	var probeIP atomic.Value
	probeIP.Store("192.0.2.1")
	p.watchTick = 30 * time.Millisecond
	p.watchProbe = func(ctx context.Context, target string) (string, error) {
		return probeIP.Load().(string), nil
	}

	host, portStr, _ := net.SplitHostPort(regAddr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	acc.Expires = 3600 // 续注册间隔足够长，第二次 REGISTER 必然由看护触发

	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	waitRegCount(t, regCount, 1)
	waitRegistered(t, sink)

	probeIP.Store("192.0.2.2") // 网络切换
	waitRegCount(t, regCount, 2)
	waitRegistered(t, sink) // 重新注册后状态必须回到 registered（不能停在注册中）

	// 地址稳定后不得再触发
	time.Sleep(300 * time.Millisecond)
	if got := regCount.Load(); got != 2 {
		t.Fatalf("出口地址稳定后不应重复触发注册，REGISTER 次数=%d", got)
	}
}

// TestSleepResumeTriggersReRegister 系统休眠唤醒（墙钟跳变）后必须立即重新注册。
func TestSleepResumeTriggersReRegister(t *testing.T) {
	regAddr, regCount := startRegCounter(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	base := time.Now()
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	var probeIP atomic.Value
	probeIP.Store("192.0.2.1")
	p.watchTick = 30 * time.Millisecond
	p.watchNow = func() time.Time { return time.Unix(0, clock.Load()) }
	p.watchProbe = func(ctx context.Context, target string) (string, error) {
		return probeIP.Load().(string), nil
	}

	host, portStr, _ := net.SplitHostPort(regAddr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	acc.Expires = 3600

	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	waitRegCount(t, regCount, 1)
	waitRegistered(t, sink)

	// 模拟休眠 2 分钟后唤醒：墙钟跳变（单调时钟不走）必须被检出
	clock.Store(base.Add(2 * time.Minute).UnixNano())
	waitRegCount(t, regCount, 2)
	waitRegistered(t, sink)
}

// TestStunRefreshOnNetworkChange 网络变化后必须先重探 STUN 公网地址再重新注册
// （旧 NAT 映射已失效，沿用旧地址会"注册成功但不可达"）。
func TestStunRefreshOnNetworkChange(t *testing.T) {
	var stunIP atomic.Value
	stunIP.Store("203.0.113.7")
	stunAddr := startFixedStunServer(t, &stunIP)
	regAddr, regCount := startRegCounter(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	var probeIP atomic.Value
	probeIP.Store("192.0.2.1")
	p.watchTick = 30 * time.Millisecond
	p.watchProbe = func(ctx context.Context, target string) (string, error) {
		return probeIP.Load().(string), nil
	}

	host, portStr, _ := net.SplitHostPort(regAddr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	acc.Expires = 3600
	acc.STUNEnabled = true
	acc.STUNServer = stunAddr

	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}

	// 初始 STUN 就绪 → 立即重新注册，Contact 带上首个公网地址
	waitRegCount(t, regCount, 2)
	if got := p.localHost(); got != "203.0.113.7" {
		t.Fatalf("初始 STUN 后 Contact 主机应为 203.0.113.7，实际 %q", got)
	}

	// 网络切换：出口地址与公网地址同时变化
	probeIP.Store("192.0.2.2")
	stunIP.Store("198.51.100.9")
	waitRegCount(t, regCount, 3)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && p.localHost() != "198.51.100.9" {
		time.Sleep(20 * time.Millisecond)
	}
	if got := p.localHost(); got != "198.51.100.9" {
		t.Fatalf("网络变化后应重探到新公网地址，实际 %q", got)
	}
	waitRegistered(t, sink)
}

// TestTcpConnectionReset 网络变化时丢弃池中复用的 TCP 信令连接
// （TCP 连接死亡后不会因发送失败被清出池，不丢弃会一直黑洞）。
func TestTcpConnectionReset(t *testing.T) {
	regAddr, _ := startRegCounterTCP(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(regAddr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	acc.Transport = "tcp"
	acc.Expires = 3600

	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	waitRegistered(t, sink)

	key := net.JoinHostPort(host, strconv.Itoa(port))
	if _, err := p.client.TransportLayer().GetConnection("tcp", key); err != nil {
		t.Fatalf("注册后池中应有复用的 TCP 连接: %v", err)
	}

	p.resetClientConnection(context.Background(), acc)

	// 关闭是异步清出池的（读循环报错后 sipgo 自行删除），轮询等待
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := p.client.TransportLayer().GetConnection("tcp", key); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("网络变化后未丢弃池中的 TCP 信令连接")
}

// TestWatchStopsAfterUnregister 注销后看护必须停止（不得再触发注册）。
func TestWatchStopsAfterUnregister(t *testing.T) {
	regAddr, regCount := startRegCounter(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	var probeIP atomic.Value
	probeIP.Store("192.0.2.1")
	p.watchTick = 30 * time.Millisecond
	p.watchProbe = func(ctx context.Context, target string) (string, error) {
		return probeIP.Load().(string), nil
	}

	host, portStr, _ := net.SplitHostPort(regAddr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	acc.Expires = 3600

	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	waitRegistered(t, sink)

	if err := p.Unregister(context.Background()); err != nil {
		t.Fatalf("Unregister 失败: %v", err)
	}
	base := regCount.Load() // 注销的 REGISTER（expires=0）已计入

	probeIP.Store("192.0.2.2") // 注销后再发生网络变化也不得注册
	time.Sleep(300 * time.Millisecond)
	if got := regCount.Load(); got != base {
		t.Fatalf("注销后不应再触发注册，REGISTER 次数由 %d 变为 %d", base, got)
	}
	if sink.lastReg() != core.RegUnregistered {
		t.Fatalf("注销后状态应为 unregistered，实际: %v", sink.lastReg())
	}
}
