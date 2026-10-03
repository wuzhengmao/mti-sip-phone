package updater

import (
	"strconv"
	"strings"
)

// compareVersions 比较版本号：a > b 返回 (1, true)，a < b 返回 (-1, true)，
// 相等返回 (0, true)；任一侧无法解析时返回 (0, false)（视为不可比）。
//
// 规则（与更新站点的语义化版本一致）：
//   - 主版本按 "." 分段做数字比较（1.0.10 > 1.0.9），缺段按 0，允许 "v" 前缀；
//   - "-" 或 "_" 之后是预发布标识，**预发布低于同号正式版**（1.0.5-rc < 1.0.5）
//     ——开发构建的版本号带 -rc，正式版发布时数字相同也必须能通知更新；
//   - 预发布标识逐段比较：数字段按数值、数字段低于字母数字段、段数少者低
//     （1.0.5-rc < 1.0.5-rc.1）；
//   - "+" 之后是构建元数据，不参与比较（1.0+meta == 1.0）。
//
// 不可比时调用方应放行——版本裁决权在前端，软电话只在可比时拒绝降级。
func compareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	if c := compareNumericParts(av.parts, bv.parts); c != 0 {
		return c, true
	}
	return comparePrerelease(av.pre, bv.pre), true
}

// parsedVersion 解析后的版本：数字段 + 预发布标识（正式版为空）。
type parsedVersion struct {
	parts []int
	pre   []string
}

// parseVersion 把 "1.2.3" / "v1.2.3-rc.1" 解析为数字段与预发布标识。
func parseVersion(v string) (parsedVersion, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if v == "" {
		return parsedVersion{}, false
	}
	// 构建元数据不参与比较
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	// "-" 与 "_" 都引入预发布标识（与站点一致，如 1.2.3-rc.1 / 1.2.3_rc1）
	var pre []string
	if i := strings.IndexAny(v, "-_"); i >= 0 {
		pre = strings.FieldsFunc(v[i+1:], func(r rune) bool {
			return r == '.' || r == '-' || r == '_'
		})
		v = v[:i]
	}
	if v == "" {
		return parsedVersion{}, false
	}
	fields := strings.Split(v, ".")
	parts := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return parsedVersion{}, false
		}
		parts = append(parts, n)
	}
	return parsedVersion{parts: parts, pre: pre}, true
}

// compareNumericParts 比较数字段（缺段按 0）。
func compareNumericParts(a, b []int) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// comparePrerelease 比较预发布标识：任一侧为空时正式版更高。
func comparePrerelease(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1 // 正式版 > 同号预发布版
	case len(b) == 0:
		return -1
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xerr == nil:
			return -1 // 数字段低于字母数字段（语义化版本规则）
		case yerr == nil:
			return 1
		default:
			if x != y {
				if x < y {
					return -1
				}
				return 1
			}
		}
	}
	switch {
	case len(a) < len(b):
		return -1 // 前缀相同时段数少者低（1.0.5-rc < 1.0.5-rc.1）
	case len(a) > len(b):
		return 1
	}
	return 0
}
