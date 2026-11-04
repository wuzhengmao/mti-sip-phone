package protocol

import "testing"

// TestParseSipURLCredentialsAndListen 账号/密码可走查询参数（浏览器会拦截含
// user:pass@ 的子资源 URL，引导器拉起时改用 ?account=&password=），listen 指定
// 本地 WS 端口。userinfo 优先于查询参数。
func TestParseSipURLCredentialsAndListen(t *testing.T) {
	cases := []struct {
		raw           string
		account, pass string
		listen        int
	}{
		{"sip://10.0.0.1:5060?account=2001&password=p%40ss&listen=8765", "2001", "p@ss", 8765},
		{"sip://2001:pwd@10.0.0.1:5060?account=other&password=other", "2001", "pwd", 0}, // userinfo 优先
		{"sip://2001@10.0.0.1:5060?password=x&listen=0", "2001", "x", 0},                // listen=0 忽略
		{"sip://10.0.0.1:5060?account=2001&listen=99999", "2001", "", 0},                // listen 越界忽略
	}
	for _, c := range cases {
		acc, err := ParseSipURL(c.raw)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", c.raw, err)
		}
		if acc.Account != c.account || acc.Password != c.pass || acc.Listen != c.listen {
			t.Fatalf("%s 应为 account=%q password=%q listen=%d，实际 %q/%q/%d",
				c.raw, c.account, c.pass, c.listen, acc.Account, acc.Password, acc.Listen)
		}
	}
	// 两边都没账号 → 明确报错
	if _, err := ParseSipURL("sip://10.0.0.1:5060"); err == nil {
		t.Fatal("缺账号应报错")
	}
}

// TestParseSipURLInstance sip:// 的 instance 查询参数（多实例沙箱槽位，
// REQ-NFR-007）：缺省/0 = 主实例；1–99 合法；越界或非数字忽略（按主实例走）。
func TestParseSipURLInstance(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"sip://1001:pwd@10.0.0.1:5060", 0},
		{"sip://1001:pwd@10.0.0.1:5060?transport=udp", 0},
		{"sip://1001:pwd@10.0.0.1:5060?instance=2", 2},
		{"sip://1001:pwd@10.0.0.1:5060?transport=tcp&instance=9", 9},
		{"sip://1001:pwd@10.0.0.1:5060?instance=0", 0},
		{"sip://1001:pwd@10.0.0.1:5060?instance=100", 0}, // 越界忽略
		{"sip://1001:pwd@10.0.0.1:5060?instance=abc", 0}, // 非数字忽略
	}
	for _, c := range cases {
		acc, err := ParseSipURL(c.raw)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", c.raw, err)
		}
		if acc.Instance != c.want {
			t.Fatalf("%s 实例槽位应为 %d，实际 %d", c.raw, c.want, acc.Instance)
		}
	}
}

// TestParseSipsURL sips:// 前缀表示 TLS 传输（REQ-ACC-006）：默认端口 5061、
// transport=tls；即使带 ?transport=udp 也以前缀为准（sips 是更强的声明）。
func TestParseSipsURL(t *testing.T) {
	cases := []struct {
		raw          string
		wantTrans    string
		wantPort     int
		wantInstance int
	}{
		{"sips://2999:pwd@10.168.4.40", "tls", 5061, 0},
		{"sips://2999:pwd@10.168.4.40:5062", "tls", 5062, 0},
		{"sips://2999:pwd@10.168.4.40?transport=udp", "tls", 5061, 0},
		{"sips://2999:pwd@10.168.4.40?instance=3", "tls", 5061, 3},
		{"sip://2999:pwd@10.168.4.40:5061?transport=tls", "tls", 5061, 0},
		{"sip://2999:pwd@10.168.4.40", "udp", 5060, 0},
	}
	for _, c := range cases {
		acc, err := ParseSipURL(c.raw)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", c.raw, err)
		}
		if acc.Transport != c.wantTrans || acc.Port != c.wantPort || acc.Instance != c.wantInstance {
			t.Fatalf("%s 应为 transport=%s port=%d instance=%d，实际 transport=%s port=%d instance=%d",
				c.raw, c.wantTrans, c.wantPort, c.wantInstance, acc.Transport, acc.Port, acc.Instance)
		}
	}
}

// TestParseSipURLUpdateSite sip:// 的 updatesite 查询参数（REQ-UPD-022）：
// 省略/空 = 空串（应用层视为不改配置）；值经百分号解码并去两端空格后原样透传
//（scheme 合法性由 core.SetUpdateConfig 校验，解析层不把关）；与其它参数、
// sips:// 前缀可组合。
func TestParseSipURLUpdateSite(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"sip://1001:pwd@10.0.0.1:5060", ""},
		{"sip://1001:pwd@10.0.0.1:5060?updatesite=https://update.wuzm219.cn", "https://update.wuzm219.cn"},
		{"sip://1001:pwd@10.0.0.1:5060?transport=tcp&instance=9&updatesite=https://update.wuzm219.cn", "https://update.wuzm219.cn"},
		{"sip://1001:pwd@10.0.0.1:5060?updatesite=https%3A%2F%2Fupdate.wuzm219.cn%2Fsub", "https://update.wuzm219.cn/sub"},
		{"sip://1001:pwd@10.0.0.1:5060?updatesite=%20https://update.wuzm219.cn%20", "https://update.wuzm219.cn"},
		{"sip://1001:pwd@10.0.0.1:5060?updatesite=%20%20", ""},         // 纯空格 → 视为省略
		{"sip://1001:pwd@10.0.0.1:5060?updatesite=ftp://x", "ftp://x"}, // 非法 scheme 透传（应用层拒绝）
		{"sips://2999:pwd@10.168.4.40?updatesite=https://update.wuzm219.cn", "https://update.wuzm219.cn"},
	}
	for _, c := range cases {
		acc, err := ParseSipURL(c.raw)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", c.raw, err)
		}
		if acc.UpdateSite != c.want {
			t.Fatalf("%s 更新站点应为 %q，实际 %q", c.raw, c.want, acc.UpdateSite)
		}
	}
}

// TestParseSipURLSlaves sip:// 的 slave 查询参数（可重复，REQ-ACC-038）：
// 每个 slave=host[:port]，端口缺省 0（用主服务器端口）；空/无端口项跳过。
func TestParseSipURLSlaves(t *testing.T) {
	acc, err := ParseSipURL("sip://2999:pwd@10.168.4.40:5060?slave=10.168.4.41&slave=10.168.4.42:5061&slave=")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(acc.Slaves) != 2 {
		t.Fatalf("期望 2 个备用服务器（空项跳过），实际 %d", len(acc.Slaves))
	}
	if acc.Slaves[0].Server != "10.168.4.41" || acc.Slaves[0].Port != 0 {
		t.Errorf("备用服务器 0 错误：%+v", acc.Slaves[0])
	}
	if acc.Slaves[1].Server != "10.168.4.42" || acc.Slaves[1].Port != 5061 {
		t.Errorf("备用服务器 1 错误：%+v", acc.Slaves[1])
	}
	// 无 slave 参数 → 空列表
	acc2, err := ParseSipURL("sip://2999:pwd@10.168.4.40:5060")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(acc2.Slaves) != 0 {
		t.Errorf("无 slave 参数应返回空列表，实际 %d", len(acc2.Slaves))
	}
}

// TestParseSipURLPreferPrimary sip:// 的 preferprimary 查询参数（REQ-ACC-038 修订）：
// 1/true 等真值 → 优先使用主节点；缺省/非法保持 false。
func TestParseSipURLPreferPrimary(t *testing.T) {
	for _, raw := range []string{
		"sip://2999:pwd@10.168.4.40:5060?preferprimary=1",
		"sip://2999:pwd@10.168.4.40:5060?preferprimary=true",
		"sip://2999:pwd@10.168.4.40:5060?preferprimary=TRUE",
	} {
		acc, err := ParseSipURL(raw)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", raw, err)
		}
		if !acc.PreferPrimary {
			t.Errorf("%q 应解析为 PreferPrimary=true", raw)
		}
	}
	// 缺省 / 非法值 → false
	for _, raw := range []string{
		"sip://2999:pwd@10.168.4.40:5060",
		"sip://2999:pwd@10.168.4.40:5060?preferprimary=0",
		"sip://2999:pwd@10.168.4.40:5060?preferprimary=maybe",
	} {
		acc, err := ParseSipURL(raw)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", raw, err)
		}
		if acc.PreferPrimary {
			t.Errorf("%q 应解析为 PreferPrimary=false", raw)
		}
	}
}

// TestSplitURL Windows 协议拉起时参数带启动器前缀，sip:// 与 sips:// 都要能识别
// （sips:// 里不含 "sip://" 子串，前缀匹配必须两个都查，且 sips 在前）。
func TestSplitURL(t *testing.T) {
	cases := []struct {
		arg  string
		want string
		ok   bool
	}{
		{"sip://2999:pwd@10.0.0.1:5060", "sip://2999:pwd@10.0.0.1:5060", true},
		{"sips://2999:pwd@10.0.0.1:5061", "sips://2999:pwd@10.0.0.1:5061", true},
		{"mti-sip-phone:sips://2999:pwd@10.0.0.1?instance=2", "sips://2999:pwd@10.0.0.1?instance=2", true},
		{"SIPS://2999:pwd@10.0.0.1", "SIPS://2999:pwd@10.0.0.1", true},
		{"--instance=2", "", false},
	}
	for _, c := range cases {
		got, ok := SplitURL(c.arg)
		if ok != c.ok || got != c.want {
			t.Fatalf("SplitURL(%q) = (%q, %v)，期望 (%q, %v)", c.arg, got, ok, c.want, c.ok)
		}
	}
	if u := FindURL([]string{"--instance=1", "mti-sip-phone:sips://a:b@c:5061"}); u != "sips://a:b@c:5061" {
		t.Fatalf("FindURL 应取出 sips:// 链接，实际 %q", u)
	}
}
