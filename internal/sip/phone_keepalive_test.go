package sip

// 服务端心跳（REQ-ACC-020/021）的回归测试：
//   - 启用时 REGISTER 的 User-Agent 带 optchk 标记（服务端据此发 OPTIONS）；
//   - 心跳计数挂在**传输层**读钩子上（事务层会把同 branch 的新探测当重传）；
//   - 至少 5 条心跳才取平均武装判定；超过 2.5 个周期没收到即立即重新注册；
//   - **不专门处理 OPTIONS**：sipgo 默认回 405，实测服务端接受（回 200 反而
//     在 NAT/rport 场景对不上而判失败）——本文件用测试锁住这个决定。

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"

	"mti-sip-phone/internal/core"
)

// startRegCapture 启动只处理 REGISTER 的服务器：不挑战 Digest（首次请求即可
// 拿到报文），回 200 并记录请求原文。
func startRegCapture(t *testing.T) (string, *atomic.Value) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-reg-capture"))
	if err != nil {
		t.Fatalf("创建 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	var raw atomic.Value
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		raw.Store(req.String())
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "3600"))
		_ = tx.Respond(res)
	})
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = ua.Close() })
	go func() { _ = srv.ServeUDP(ln) }()
	return ln.LocalAddr().String(), &raw
}

// registerAndCapture 注册一次并返回 REGISTER 报文原文。
func registerAndCapture(t *testing.T, optChk bool) string {
	t.Helper()
	addr, raw := startRegCapture(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	acc.Expires = 3600
	acc.ChkOpt = optChk

	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := raw.Load().(string); ok && v != "" {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("未收到 REGISTER 请求")
	return ""
}

// userAgentLine 取报文里的 User-Agent 行。
func userAgentLine(raw string) string {
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "user-agent:") {
			return line
		}
	}
	return "(无 User-Agent 行)"
}

// TestRegisterUserAgentOptChk 启用服务端心跳时 User-Agent 带 optchk 标记。
func TestRegisterUserAgentOptChk(t *testing.T) {
	raw := registerAndCapture(t, true)
	if want := UserAgentName() + " optchk"; !strings.Contains(raw, want) {
		t.Fatalf("User-Agent 应含 %q，实际：%s", want, userAgentLine(raw))
	}
}

// TestRegisterUserAgentWithoutOptChk 关闭时只发标准标识。
func TestRegisterUserAgentWithoutOptChk(t *testing.T) {
	raw := registerAndCapture(t, false)
	if strings.Contains(raw, "optchk") {
		t.Fatalf("未启用服务端心跳时不应带 optchk，实际：%s", userAgentLine(raw))
	}
}

// TestReadFilterCountsOptionsWireHeartbeats 心跳计数走传输层读钩子：
// 每一条线上的 OPTIONS（不看事务层去重）都算，且**只认注册服务器来的**；
// 其它方法/来源不算。
func TestReadFilterCountsOptionsWireHeartbeats(t *testing.T) {
	p := &Phone{}
	// 计数现在同时看开关与传输（仅 UDP）：这里用默认的 udp + 开启心跳
	p.acc = config.Account{Server: "10.0.0.2", ChkOpt: true}
	opts := func(method string) []byte {
		return []byte(method + " sip:2999@10.0.0.2:5060 SIP/2.0\r\nContent-Length: 0\r\n\r\n")
	}
	from := func(ip string) sip.TransportReadProps {
		return sip.TransportReadProps{RemoteAddr: &net.UDPAddr{IP: net.ParseIP(ip), Port: 5060}}
	}
	if _, err := p.readFilter(from("10.0.0.2"), opts("INVITE")); err != nil {
		t.Fatalf("读钩子返回错误: %v", err)
	}
	if !p.hbLast.IsZero() {
		t.Fatal("非 OPTIONS 报文不应记作心跳")
	}
	if _, err := p.readFilter(from("10.9.9.9"), opts("OPTIONS")); err != nil {
		t.Fatalf("读钩子返回错误: %v", err)
	}
	if !p.hbLast.IsZero() {
		t.Fatal("非注册服务器来的 OPTIONS 不应记作心跳")
	}
	if _, err := p.readFilter(from("10.0.0.2"), opts("OPTIONS")); err != nil {
		t.Fatalf("读钩子返回错误: %v", err)
	}
	if p.hbLast.IsZero() {
		t.Fatal("注册服务器的 OPTIONS 应记作心跳")
	}
	// 钩子只统计，不能改动/吞掉报文（OPTIONS 由 sipgo 默认回 405）
	out, _ := p.readFilter(from("10.0.0.2"), opts("OPTIONS"))
	if len(out) == 0 {
		t.Fatal("读钩子不得吞掉报文")
	}
}

// TestHeartbeatIntervalLearning 间隔学习：至少 5 条心跳（4 个样本）才取平均；
// 样本不足不武装；亚秒级重发不算样本；间隔突增视为探测重启、重新学习。
func TestHeartbeatIntervalLearning(t *testing.T) {
	base := time.Now()
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	p := &Phone{}
	p.watchNow = func() time.Time { return time.Unix(0, clock.Load()) }

	p.noteHeartbeat()
	for i := 0; i < 3; i++ {
		clock.Add(int64(10 * time.Second))
		p.noteHeartbeat()
	}
	if got := p.heartbeatInterval(); got != 0 {
		t.Fatalf("样本不足（3 个）时不应得出间隔，实际 %v", got)
	}
	clock.Add(int64(10 * time.Second))
	p.noteHeartbeat()
	if got := p.heartbeatInterval(); got != 10*time.Second {
		t.Fatalf("5 条心跳后平均间隔应为 10s，实际 %v", got)
	}
	// 亚秒级重发：只更新时刻，不算样本（否则会把间隔算小）
	clock.Add(int64(200 * time.Millisecond))
	p.noteHeartbeat()
	if got := p.heartbeatInterval(); got != 10*time.Second {
		t.Fatalf("重复探测不应改变间隔，实际 %v", got)
	}
	// 间隔突增（120s > 3×10s）= 探测中断后重启：样本清零重新学习
	clock.Add(int64(2 * time.Minute))
	p.noteHeartbeat()
	if got := p.heartbeatInterval(); got != 0 {
		t.Fatalf("探测重启后应重新学习（间隔清空），实际 %v", got)
	}
}

// startHeartbeatPhone 建一个注入假时钟/稳定探测的 Phone，并注册成功。
// watchGap 拉到 1 小时，避免假时钟跳变被当成"休眠唤醒"干扰用例。
func startHeartbeatPhone(t *testing.T, optChk bool) (*Phone, *atomic.Int32, *atomic.Int64, time.Time) {
	t.Helper()
	regAddr, regCount := startRegCounter(t)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	base := time.Now()
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	var probeIP atomic.Value
	probeIP.Store("192.0.2.1")
	p.watchTick = 30 * time.Millisecond
	p.watchGap = time.Hour
	p.watchNow = func() time.Time { return time.Unix(0, clock.Load()) }
	p.watchProbe = func(ctx context.Context, target string) (string, error) {
		return probeIP.Load().(string), nil
	}

	host, portStr, _ := net.SplitHostPort(regAddr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	acc.Expires = 3600 // 续注册间隔足够长，后续 REGISTER 只能由心跳漏检触发
	acc.ChkOpt = optChk

	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	waitRegCount(t, regCount, 1)
	waitRegistered(t, sink)
	return p, regCount, &clock, base
}

// feedHeartbeats 按 10 秒间隔喂 n 条心跳。
func feedHeartbeats(p *Phone, clock *atomic.Int64, n int) {
	p.noteHeartbeat()
	for i := 1; i < n; i++ {
		clock.Add(int64(10 * time.Second))
		p.noteHeartbeat()
	}
}

// TestHeartbeatMissTriggersReRegister 学满 5 条心跳后，**超过 2.5 个周期**
// 没收到探测 → 立即重新注册；触发后复位、不重复触发。
func TestHeartbeatMissTriggersReRegister(t *testing.T) {
	p, regCount, clock, _ := startHeartbeatPhone(t, true)
	sink := p.sink.(*testSink)
	feedHeartbeats(p, clock, 5)

	// 2.5×10s = 25s：推进到 20 秒（看护 30ms 一跳）不应触发
	clock.Add(int64(20 * time.Second))
	time.Sleep(200 * time.Millisecond)
	if got := regCount.Load(); got != 1 {
		t.Fatalf("2.0 个周期时不应触发，REGISTER 次数=%d", got)
	}
	// 超过 2.5 个周期：先置"未注册"（托盘变红），再重新注册
	clock.Add(int64(6 * time.Second))
	waitRegCount(t, regCount, 2)
	regs := sink.regStates()
	idx := -1
	for i, st := range regs {
		if st == core.RegUnregistered {
			idx = i
			break
		}
	}
	if idx <= 0 || regs[idx-1] != core.RegRegistered {
		t.Fatalf("超时应先置未注册（托盘变红）再重新注册，实际状态序列: %v", regs)
	}

	// 触发后复位：不再随时间重复注册（等服务端下一条心跳重新武装）
	clock.Add(int64(5 * time.Minute))
	time.Sleep(300 * time.Millisecond)
	if got := regCount.Load(); got != 2 {
		t.Fatalf("漏检触发一次后不应重复注册，REGISTER 次数=%d", got)
	}
}

// TestHeartbeatMissIgnoredWithoutHeartbeat 未观测到心跳 / 样本不足 / 未启用
// 心跳检测时都不触发（服务器不支持 optchk 时不发 OPTIONS，这条逻辑必须静默）。
func TestHeartbeatMissIgnoredWithoutHeartbeat(t *testing.T) {
	// ① 服务端从未探测
	_, regCount, clock, _ := startHeartbeatPhone(t, true)
	clock.Add(int64(10 * time.Minute))
	time.Sleep(300 * time.Millisecond)
	if got := regCount.Load(); got != 1 {
		t.Fatalf("未观测到心跳时不应触发重新注册，REGISTER 次数=%d", got)
	}

	// ② 样本不足（只有 3 条心跳）
	p2, regCount2, clock2, _ := startHeartbeatPhone(t, true)
	feedHeartbeats(p2, clock2, 3)
	clock2.Add(int64(10 * time.Minute))
	time.Sleep(300 * time.Millisecond)
	if got := regCount2.Load(); got != 1 {
		t.Fatalf("心跳样本不足时不应触发重新注册，REGISTER 次数=%d", got)
	}

	// ③ 未启用服务端心跳检测
	p3, regCount3, clock3, _ := startHeartbeatPhone(t, false)
	feedHeartbeats(p3, clock3, 5)
	clock3.Add(int64(10 * time.Minute))
	time.Sleep(300 * time.Millisecond)
	if got := regCount3.Load(); got != 1 {
		t.Fatalf("未启用心跳检测时不应触发重新注册，REGISTER 次数=%d", got)
	}
}

// TestOptionsGetsDefault405 不专门处理 OPTIONS：sipgo 默认回 405，服务端接受。
// 锁住这个决定，别再改回"自己回 200"（NAT/rport 场景下 200 的地址对不上，
// 服务端反而判探测失败、把注册摘掉）。
func TestOptionsGetsDefault405(t *testing.T) {
	sink := &testSink{}
	_, addr := newTestPhoneListening(t, sink)
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-options-probe"))
	if err != nil {
		t.Fatalf("创建探测 UA 失败: %v", err)
	}
	defer ua.Close()
	client, err := sipgo.NewClient(ua)
	if err != nil {
		t.Fatalf("创建探测客户端失败: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := sip.NewRequest(sip.OPTIONS, sip.Uri{User: "1001", Host: host, Port: port})
	res, err := client.Do(ctx, req)
	if err != nil {
		t.Fatalf("OPTIONS 无响应: %v", err)
	}
	if res.StatusCode != 405 {
		t.Fatalf("OPTIONS 应回 sipgo 默认的 405，实际 %d %s", res.StatusCode, res.Reason)
	}
}

// TestUserAgentOptChkOnlyUDP optchk 标记只加在 **UDP** 传输的注册上
// （用户要求：TCP/TLS 不需要服务端 OPTIONS 心跳探测）。
func TestUserAgentOptChkOnlyUDP(t *testing.T) {
	cases := []struct {
		transport string
		wantOpt   bool
	}{
		{"udp", true},
		{"", true}, // 空 = 默认 udp
		{"tcp", false},
		{"tls", false},
	}
	for _, c := range cases {
		ua := userAgentFor(config.Account{ChkOpt: true, Transport: c.transport})
		if got := strings.Contains(ua, "optchk"); got != c.wantOpt {
			t.Fatalf("transport=%q 时 optchk 应为 %v，实际 UA=%q", c.transport, c.wantOpt, ua)
		}
	}
	if ua := userAgentFor(config.Account{ChkOpt: false, Transport: "udp"}); strings.Contains(ua, "optchk") {
		t.Fatalf("未启用服务端心跳时不应带 optchk：%q", ua)
	}
}

// TestHeartbeatOnlyForUDP 服务端心跳只对 **UDP** 传输生效（REQ-ACC-020 修订）：
// tcp/tls 注册报文不带 optchk、服务端不按心跳探测，所以计数与间隔学习一并停用
// （超时重注册依赖"已学到间隔"，计数停了它自然不触发；netwatch 里另有显式判断）。
func TestHeartbeatOnlyForUDP(t *testing.T) {
	options := []byte("OPTIONS sip:2999@10.0.0.2:5060 SIP/2.0\r\nContent-Length: 0\r\n\r\n")
	props := sip.TransportReadProps{RemoteAddr: &net.UDPAddr{IP: net.ParseIP("10.0.0.2"), Port: 5060}}

	for _, tr := range []string{"tcp", "tls"} {
		p := &Phone{}
		p.acc = config.Account{Server: "10.0.0.2", Transport: tr, ChkOpt: true}
		if p.heartbeatWanted() {
			t.Fatalf("transport=%s 时不应启用服务端心跳检测", tr)
		}
		if _, err := p.readFilter(props, options); err != nil {
			t.Fatalf("读钩子返回错误: %v", err)
		}
		if !p.hbLast.IsZero() {
			t.Fatalf("transport=%s 时服务器的 OPTIONS 不应记作心跳", tr)
		}
	}
	for _, tr := range []string{"udp", ""} { // 空 = 默认 udp
		p := &Phone{}
		p.acc = config.Account{Server: "10.0.0.2", Transport: tr, ChkOpt: true}
		if !p.heartbeatWanted() {
			t.Fatalf("transport=%q 时应启用服务端心跳检测", tr)
		}
	}
	p := &Phone{}
	p.acc = config.Account{Server: "10.0.0.2", Transport: "udp", ChkOpt: false}
	if p.heartbeatWanted() {
		t.Fatal("未勾选开关时不应启用服务端心跳检测")
	}
}
