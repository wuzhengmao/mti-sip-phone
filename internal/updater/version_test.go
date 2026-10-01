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
		{"v1.0.2", "1.0.2", 0, true},  // 允许 v 前缀
		{"1.0", "1.0.0", 0, true},     // 缺段按 0
		{"1.0.2-beta", "1.0.2", 0, true}, // 后缀取数字前缀
		{"2.0", "1.9.9", 1, true},
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
