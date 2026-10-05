package protocol

import "testing"

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
