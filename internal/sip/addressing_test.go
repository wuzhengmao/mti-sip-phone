package sip

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
)

// startCaptureServer 起一个"收下请求再回 200/486"的测试服务器，
// 用于断言本端**实际发出去**的报文（R-URI/From/To/Via/Contact/Route）。
func startCaptureServer(t *testing.T) (addr string, regs chan *sip.Request, invites chan *sip.Request) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("capture-server"))
	if err != nil {
		t.Fatalf("创建测试 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建测试服务器失败: %v", err)
	}
	regs = make(chan *sip.Request, 4)
	invites = make(chan *sip.Request, 4)

	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		select {
		case regs <- req:
		default:
		}
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "3600"))
		_ = tx.Respond(res)
	})
	srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		select {
		case invites <- req:
		default:
		}
		// 直接拒绝：本用例只关心发出去的报文长什么样
		_ = tx.Respond(sip.NewResponseFromRequest(req, 486, "Busy Here", nil))
	})

	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("测试服务器监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = srv.ServeUDP(ln) }()
	return ln.LocalAddr().String(), regs, invites
}

// waitReq 等一条被捕获的请求。
func waitReq(t *testing.T, ch chan *sip.Request, what string) *sip.Request {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatalf("5 秒内没收到%s", what)
		return nil
	}
}

// TestParseTargetWithDomain 纯号码补全：配了域名用域名（不带端口），
// 没配则用服务器地址:端口（历史行为不变）。
func TestParseTargetWithDomain(t *testing.T) {
	withDomain := config.Account{Server: "10.168.4.40", Port: 15060, Domain: "sip.mti-sh.cn"}
	u, err := parseTarget("2901", withDomain)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "sip.mti-sh.cn" || u.Port != 0 || u.User != "2901" {
		t.Fatalf("配域名时应为 sip:2901@sip.mti-sh.cn（不带端口），实际 %+v", u)
	}

	noDomain := config.Account{Server: "10.168.4.40", Port: 15060}
	u2, err := parseTarget("2901", noDomain)
	if err != nil {
		t.Fatal(err)
	}
	if u2.Host != "10.168.4.40" || u2.Port != 15060 {
		t.Fatalf("未配域名时应沿用服务器地址:端口，实际 %+v", u2)
	}

	// 显式 sip: URI 原样使用（用户写了主机就不改）
	u3, err := parseTarget("sip:2901@10.168.4.118", withDomain)
	if err != nil {
		t.Fatal(err)
	}
	if u3.Host != "10.168.4.118" {
		t.Fatalf("显式 URI 的主机不应被域名改写，实际 %+v", u3)
	}
}

// TestRegisterUsesDomain 配了域名后：REGISTER 的 R-URI / From / To 用域名、
// **不带服务器端口**，而报文仍然发到 Server:Port（能收到就说明目的地址没跑偏）。
func TestRegisterUsesDomain(t *testing.T) {
	addr, regs, _ := startCaptureServer(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	p := newTestPhone(t, &testSink{})
	acc := config.Account{
		Server: host, Port: port, Transport: "udp",
		Account: "1001", Password: "secret",
		Domain: "sip.mti-sh.cn",
	}
	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 返回错误: %v", err)
	}
	req := waitReq(t, regs, "REGISTER")

	if req.Recipient.Host != "sip.mti-sh.cn" {
		t.Errorf("R-URI 的域应为域名，实际 %q（完整：%s）", req.Recipient.Host, req.Recipient.String())
	}
	if req.Recipient.Port != 0 {
		t.Errorf("R-URI 不该带端口，实际 %d", req.Recipient.Port)
	}
	if h := req.From().Address.Host; h != "sip.mti-sh.cn" {
		t.Errorf("From 的域应为域名，实际 %q", h)
	}
	if h := req.To().Address.Host; h != "sip.mti-sh.cn" {
		t.Errorf("To 的域应为域名，实际 %q", h)
	}
	// Contact 是"对端怎么找到我"，永远是本机地址，不该被域名影响
	if c := req.Contact(); c == nil || c.Address.Host == "sip.mti-sh.cn" {
		t.Errorf("Contact 不该用域名，实际 %v", c)
	}
}

// TestDialUsesDomainAndDeclaredIP 呼出：域名进 From/To/R-URI，Route 指向
// Server:Port；同时「声明 IP」要出现在 Contact、Via 与 SDP 的 c= 行里。
func TestDialUsesDomainAndDeclaredIP(t *testing.T) {
	addr, _, invites := startCaptureServer(t)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	p := newTestPhone(t, &testSink{})
	acc := config.Account{
		Server: host, Port: port, Transport: "udp",
		Account: "1001", Password: "secret",
		Domain:   "sip.mti-sh.cn",
		PublicIP: "10.168.5.10", // 声明 IP（与真实出口地址无关，只影响"声明"）
	}
	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 返回错误: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	if _, err := p.Dial(context.Background(), "1002"); err != nil {
		t.Fatalf("Dial 返回错误: %v", err)
	}
	req := waitReq(t, invites, "INVITE")

	if req.Recipient.Host != "sip.mti-sh.cn" {
		t.Errorf("R-URI 的域应为域名，实际 %q", req.Recipient.Host)
	}
	if h := req.From().Address.Host; h != "sip.mti-sh.cn" {
		t.Errorf("From 的域应为域名，实际 %q", h)
	}
	if h := req.To().Address.Host; h != "sip.mti-sh.cn" {
		t.Errorf("To 的域应为域名，实际 %q", h)
	}
	// Route 指向真实服务器，否则域名解析不了、报文发不出去
	route := req.GetHeader("Route")
	if route == nil || !strings.Contains(route.Value(), addr) {
		t.Errorf("应在 Route 里指向服务器 %s，实际 %v", addr, route)
	}
	// 声明 IP：Contact 与 Via 的 sent-by 都要用它
	if c := req.Contact(); c == nil || c.Address.Host != "10.168.5.10" {
		t.Errorf("Contact 应用声明 IP，实际 %v", c)
	}
	if via := req.Via(); via == nil || via.Host != "10.168.5.10" {
		t.Errorf("Via 的 sent-by 应用声明 IP，实际 %v", via)
	}
	// SDP 的 c= 行
	if body := string(req.Body()); !strings.Contains(body, "c=IN IP4 10.168.5.10") {
		t.Errorf("SDP 的连接地址应为声明 IP，实际 SDP：\n%s", body)
	}
}

// TestDeclaredIPOverridesStun 声明 IP 优先于 STUN 探测到的公网地址
//（用户既然手填了就以它为准）。
func TestDeclaredIPOverridesStun(t *testing.T) {
	p := newTestPhone(t, &testSink{})
	p.mu.Lock()
	p.acc = config.Account{PublicIP: "10.168.5.10"}
	p.publicIP = "203.0.113.9" // 假装 STUN 探到的公网地址
	p.mu.Unlock()

	if got := p.localHost(); got != "10.168.5.10" {
		t.Fatalf("声明 IP 应压过 STUN 结果，实际 %q", got)
	}
	// 清掉声明 IP 后回到 STUN 结果
	p.mu.Lock()
	p.acc.PublicIP = ""
	p.mu.Unlock()
	if got := p.localHost(); got != "203.0.113.9" {
		t.Fatalf("未声明时应回到 STUN 结果，实际 %q", got)
	}
}

// TestLocalIPv4s 候选列表：**不含回环/链路本地**，且排序是"公网在前、私网在后"。
func TestLocalIPv4s(t *testing.T) {
	ips := LocalIPv4s()
	t.Log("本机候选：", ips) // 排查用：看清过滤/排序的实际结果
	seen := map[string]bool{}
	ranks := make([]int, 0, len(ips))
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil || ip.To4() == nil {
			t.Fatalf("候选 %q 不是合法 IPv4", s)
		}
		if ip.IsLoopback() {
			t.Errorf("候选不该含回环地址 %q", s)
		}
		if ip.IsLinkLocalUnicast() {
			t.Errorf("候选不该含链路本地地址 %q", s)
		}
		if seen[s] {
			t.Errorf("候选重复：%q", s)
		}
		seen[s] = true
		ranks = append(ranks, ipRank(ip))
	}
	for i := 1; i < len(ranks); i++ {
		if ranks[i-1] > ranks[i] {
			t.Fatalf("排序不对（应公网在前、内网在后）：%v", ips)
		}
	}
}

// TestVirtualInterfaceFilter 虚拟网卡判定：常见虚拟化平台（名字或 MAC）都要被认出，
// 真实网卡不能误伤——**误伤比漏判更糟**（用户会以为"网卡列表是空的"）。
func TestVirtualInterfaceFilter(t *testing.T) {
	mac := func(s string) net.HardwareAddr {
		m, err := net.ParseMAC(s)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	cases := []struct {
		name string
		mac  net.HardwareAddr
		want bool
	}{
		{"vEthernet (WSL)", mac("00:15:5d:11:22:33"), true},      // Hyper-V/WSL：名字+MAC 都命中
		{"Ethernet 2", mac("00:50:56:aa:bb:cc"), true},           // VMware：只有 MAC 命中
		{"Ethernet 3", mac("08:00:27:aa:bb:cc"), true},           // VirtualBox
		{"docker0", mac("02:42:aa:bb:cc:dd"), true},              // Docker
		{"TAP-Windows Adapter V9", mac("00:ff:aa:bb:cc:dd"), true},
		{"VirtualBox Host-Only Network", nil, true},              // 只有名字命中
		{"Tailscale", nil, true},
		{"以太网", mac("d8:5e:d3:11:22:33"), false},                // 真实网卡（Intel OUI）
		{"WLAN", mac("3c:22:fb:11:22:33"), false},
		{"Ethernet", nil, false},
	}
	for _, c := range cases {
		if got := isVirtualInterface(c.name, c.mac); got != c.want {
			t.Errorf("isVirtualInterface(%q, %v) = %v，期望 %v", c.name, c.mac, got, c.want)
		}
	}
}

// TestIPRank 排序权重：公网 0 < 运营商级 NAT 1 < RFC1918 私网 2。
func TestIPRank(t *testing.T) {
	cases := map[string]int{
		"8.8.8.8":      0,
		"203.0.113.7":  0,
		"100.64.1.1":   1, // CGNAT（RFC 6598）
		"10.0.0.1":     2,
		"172.16.5.5":   2,
		"192.168.3.15": 2,
	}
	for in, want := range cases {
		if got := ipRank(net.ParseIP(in)); got != want {
			t.Errorf("ipRank(%s) = %d，期望 %d", in, got, want)
		}
	}
}
