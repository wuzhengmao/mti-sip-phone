package updater

import (
	"strconv"
	"strings"
)

// compareVersions 比较版本号：a > b 返回 (1, true)，a < b 返回 (-1, true)，
// 相等返回 (0, true)；任一侧无法解析时返回 (0, false)（视为不可比）。
//
// 规则：按 "." 分段做数字比较（1.0.10 > 1.0.9）；段内取数字前缀
// （"2-beta" 视作 2）；允许 "v" 前缀。不可比时调用方应放行——
// 版本裁决权在前端，软电话只在可比时拒绝降级。
func compareVersions(a, b string) (int, bool) {
	as, aok := parseVersion(a)
	bs, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			if av > bv {
				return 1, true
			}
			return -1, true
		}
	}
	return 0, true
}

// parseVersion 把 "1.2.3" 解析为 [1 2 3]。
func parseVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		i := 0
		for i < len(p) && p[i] >= '0' && p[i] <= '9' {
			i++
		}
		if i == 0 {
			return nil, false
		}
		n, err := strconv.Atoi(p[:i])
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}
