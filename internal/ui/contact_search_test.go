package ui

// 通信录拼音搜索与排序（REQ-UI-035）的回归测试：纯函数，不建界面
// （Fyne 测试驱动里 fyne.Do 是内联执行，渲染测试容易 flaky，见 CLAUDE.md）。

import "testing"

// TestMatchNamePinyin 音头/全拼/前缀/汉字/混合输入都要能命中。
func TestMatchNamePinyin(t *testing.T) {
	idx := newNameIndex("张三")
	hit := []string{"zs", "ZS", "zhangsan", "zhangs", "张", "san", "张san", "zsa", "ZhangSan"}
	for _, q := range hit {
		if !matchName(idx, q) {
			t.Fatalf("%q 应命中「张三」", q)
		}
	}
	miss := []string{"ls", "sz", "zhangx", "李四", "wangwu", "zsan1"}
	for _, q := range miss {
		if matchName(idx, q) {
			t.Fatalf("%q 不该命中「张三」", q)
		}
	}
}

// TestMatchNameOrderSensitive 顺序敏感：查询字符必须按名称从左到右出现
// （"sz"/"ns" 这种逆序组合不该命中）。
func TestMatchNameOrderSensitive(t *testing.T) {
	idx := newNameIndex("张三")
	if !matchName(idx, "zhangs") {
		t.Fatal("前缀应命中")
	}
	for _, q := range []string{"sz", "ns", "zz", "hn"} {
		if matchName(idx, q) {
			t.Fatalf("逆序/无关组合 %q 不该命中「张三」", q)
		}
	}
}

// TestMatchNameNonHan ASCII/数字/符号名称与无拼音字符。
func TestMatchNameNonHan(t *testing.T) {
	idx := newNameIndex("Alice-2")
	// "lice" 从第 2 个字符开始匹配（起点可以任意，中间不能跳过）
	for _, q := range []string{"alice", "ALI", "lice", "2", "ce-2"} {
		if !matchName(idx, q) {
			t.Fatalf("%q 应命中「Alice-2」", q)
		}
	}
	if matchName(idx, "bob") {
		t.Fatalf("「Alice-2」不该被 bob 命中")
	}

	// 日文假名（字典里没有拼音）：回退成字符本身，仍可原样匹配
	jp := newNameIndex("こんにちは")
	if !matchName(jp, "こん") {
		t.Fatal("日文名应能按字符匹配")
	}

	// 空名称：只有空查询命中
	empty := newNameIndex("")
	if !matchName(empty, "") || matchName(empty, "z") {
		t.Fatal("空名称只应命中空查询")
	}
}

// TestMatchNameHeteronym 多音字：候选读音任一命中即可。
func TestMatchNameHeteronym(t *testing.T) {
	idx := newNameIndex("重庆")
	if !matchName(idx, "chongqing") {
		t.Fatal("多音字候选 chong 应能命中")
	}
	if !matchName(idx, "zhongqing") {
		t.Fatal("多音字候选 zhong 应能命中")
	}
	if !matchName(idx, "cq") {
		t.Fatal("音头应取首选读音")
	}
}

// TestDigitsOnly 号码的"纯数字"匹配（输入 138 命中 +86 138-0000-0000）。
func TestDigitsOnly(t *testing.T) {
	cases := map[string]string{
		"+86 138-0000-0000": "+8613800000000",
		"138":               "138",
		"*123#":             "123",
		"+":                 "",
		"abc":               "",
		"sip:2999@host":     "2999",
	}
	for in, want := range cases {
		if got := digitsOnly(in); got != want {
			t.Fatalf("digitsOnly(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestNameIndexSortKey 排序键：全拼拼接（无名称得到空串，排最前）。
func TestNameIndexSortKey(t *testing.T) {
	if got := newNameIndex("张三").sortKey(); got != "zhangsan" {
		t.Fatalf("排序键应为 zhangsan，实际 %q", got)
	}
	if got := newNameIndex("").sortKey(); got != "" {
		t.Fatalf("空名称的排序键应为空串，实际 %q", got)
	}
	if got := newNameIndex("Alice").sortKey(); got != "alice" {
		t.Fatalf("英文名应小写化，实际 %q", got)
	}
}

// TestSyllablesCache 逐字符缓存不该改变结果（同一字符多次查询一致）。
func TestSyllablesCache(t *testing.T) {
	a := newNameIndex("重张")
	b := newNameIndex("重张")
	if a.full != b.full || len(a.syllables) != len(b.syllables) {
		t.Fatalf("两次构建索引应一致：%+v vs %+v", a, b)
	}
	if len(a.syllables) != 2 {
		t.Fatalf("逐字符索引长度应等于字符数，实际 %d", len(a.syllables))
	}
}
