package updater

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.1.0", "1.0.2", 1, true},
		{"1.0.2", "1.1.0", -1, true},
		{"1.0.10", "1.0.9", 1, true}, // 数字比较而非字符串比较
		{"1.0.2", "1.0.2", 0, true},
		{"v1.0.2", "1.0.2", 0, true}, // 允许 v 前缀
		{"1.0", "1.0.0", 0, true},    // 缺段按 0
		{"2.0", "1.9.9", 1, true},
		// 预发布低于同号正式版（开发构建 1.0.5-rc 要能收到正式版 1.0.5 的通知）
		{"1.0.2-beta", "1.0.2", -1, true},
		{"1.0.5", "1.0.5-rc", 1, true},
		{"1.0.5-rc", "1.0.4", 1, true},          // 预发布仍高于更低的正式版
		{"1.0.5-rc", "1.0.5-rc", 0, true},       // 同版本相等（重复告知幂等）
		{"1.0.5-rc", "1.0.5-rc.1", -1, true},    // 前缀相同时段数少者低
		{"1.0.5-rc.2", "1.0.5-rc.10", -1, true}, // 预发布数字段按数值比较
		{"1.0.5-rc", "1.0.5-beta", 1, true},     // 字母数字段按字典序
		{"1.0.5-rc.1", "1.0.5-rc", 1, true},
		{"1.0.5-1", "1.0.5-rc", -1, true}, // 数字段低于字母数字段
		{"1.0_rc1", "1.0", -1, true},      // "_" 同样引入预发布
		{"1.0+meta", "1.0", 0, true},      // 构建元数据不参与比较
		{"1.0+meta", "1.0+other", 0, true},
		{"", "1.0.0", 0, false},      // 不可比
		{"abc", "1.0.0", 0, false},   // 不可比
		{"1.x.3", "1.0.0", 0, false}, // 不可比
	}
	for _, c := range cases {
		got, ok := compareVersions(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("compareVersions(%q, %q) = (%d, %v)，期望 (%d, %v)",
				c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}
