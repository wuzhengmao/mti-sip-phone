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
