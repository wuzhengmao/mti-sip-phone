package ui

// 通信录的拼音索引、搜索匹配与排序（REQ-UI-035）。
//
// 放在 ui 层的理由：只有通信录页面用得到拼音，核心层与 WS 层都不需要
// （网页端不做增删改查，只导入导出）。go-pinyin 依赖因此只进 UI 包。
//
// 搜索要支持**音头（首字母）、全拼、汉字**的任意组合，例如「张三」用
// zs / zhangsan / zhangs / 张三 / 张san 都能命中。做法是逐字符建候选音节，
// 再用"逐字符推进"的匹配（见 matchName）：每个字符允许三种消耗方式——
// 吃掉一整个音节、把查询串的剩余部分当成该音节的前缀（首字母就在此列）、
// 或直接匹配字符本身。

import (
	"strings"
	"sync"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

// pinyinArgs 拼音参数：多音字给全部候选（「重庆」的 chong/zhong 都能命中），
// 非汉字字符回退成字符本身（保证逐字符索引与名称一一对应，ASCII 名字也能搜）。
var pinyinArgs = func() pinyin.Args {
	a := pinyin.NewArgs()
	a.Heteronym = true
	a.Fallback = func(r rune, _ pinyin.Args) []string { return []string{string(r)} }
	return a
}()

// pinyinCache 单字符 → 候选音节（小写）。联系人列表里同一个字反复出现，
// 逐字符查字典一次即可；界面线程与后台加载都可能在用，加锁保护。
var (
	pinyinCacheMu sync.Mutex
	pinyinCache   = map[rune][]string{}
)

// syllablesOf 单字符的候选音节（小写）。结果缓存。
func syllablesOf(r rune) []string {
	pinyinCacheMu.Lock()
	if got, ok := pinyinCache[r]; ok {
		pinyinCacheMu.Unlock()
		return got
	}
	pinyinCacheMu.Unlock()

	got := pinyin.Pinyin(string(r), pinyinArgs)
	var cands []string
	if len(got) == 1 {
		for _, c := range got[0] {
			cands = append(cands, strings.ToLower(c))
		}
	}
	if len(cands) == 0 { // 兜底：字典没给结果也要有一个候选，索引不能错位
		cands = []string{strings.ToLower(string(r))}
	}

	pinyinCacheMu.Lock()
	pinyinCache[r] = cands
	pinyinCacheMu.Unlock()
	return cands
}

// nameIndex 一个名称的搜索索引。
type nameIndex struct {
	raw       []rune     // 原名称（逐字符，用于汉字/字母直接匹配）
	syllables [][]string // 逐字符候选音节（小写），与 raw 一一对应
	full      string     // 逐字符首选音节拼接（排序键：张三 → zhangsan）
}

// newNameIndex 构建名称索引。空名称得到空索引（排序时排最前、匹配时只可能
// 命中空查询）。
func newNameIndex(name string) nameIndex {
	raw := []rune(name)
	idx := nameIndex{raw: raw, syllables: make([][]string, 0, len(raw))}
	var full strings.Builder
	for _, r := range raw {
		cands := syllablesOf(r)
		idx.syllables = append(idx.syllables, cands)
		full.WriteString(cands[0])
	}
	idx.full = full.String()
	return idx
}

// sortKey 排序键（全拼；空名称得到空串 → 排在最前）。
func (idx nameIndex) sortKey() string { return idx.full }

// matchName 名称是否匹配查询串（音头/全拼/汉字混合）。空查询视为命中。
func matchName(idx nameIndex, query string) bool {
	q := []rune(strings.ToLower(strings.TrimSpace(query)))
	if len(q) == 0 {
		return true
	}
	// 快速路径：原文字符串直接包含（汉字整体、ASCII 名称的子串如 "lice" 命中 "Alice"）
	if strings.Contains(strings.ToLower(string(idx.raw)), string(q)) {
		return true
	}
	// memo：matchFrom(i,j) 的结果与"从哪开始"无关，所以所有起点共用一份，
	// 整体代价 O(名称长 × 查询长)。0=未算，1=真，2=假。
	memo := make([]uint8, (len(idx.raw)+1)*(len(q)+1))
	// 起点可以是名称的任意字符（"张三" 用 san 搜 → 从"三"开始匹配），
	// 但**跳过只允许发生在开始之前**：一旦开始消耗查询串就不再跳过。
	// 否则匹配退化成"子序列"，"zs" 会命中「赵六…测试…」这种只要字序对得上
	// 的任意组合，噪声太大（实测）。
	for i := 0; i <= len(idx.raw); i++ {
		if matchFrom(idx, i, q, 0, memo) {
			return true
		}
	}
	return false
}

// matchFrom 从名称第 i 个字符开始、逐字符推进匹配查询串（j 为查询串下标）。
//
// 每个名称字符有四种消耗方式（任一命中即可）：
//
//	① 音头：q[j] 等于该音节首字母（"zs" 命中「张三」）
//	② 整音节：q[j:j+len] 正好是该音节（"zhangsan" 命中「张三」）
//	③ 尾段前缀：查询串剩余部分是音节的前缀（"zhangsa" 命中「张三」，查询到此结束）
//	④ 字符本身：q[j] 就是该字符（汉字、字母、数字原样输入）
//
// 要求名称的匹配是**连续一段**（中间不能跳过字符），这是搜索质量的关键：
// 宽松的子序列匹配在长名称上会命中一堆无关记录。
func matchFrom(idx nameIndex, i int, q []rune, j int, memo []uint8) bool {
	if j == len(q) {
		return true // 查询串已全部消耗（名称剩余字符不再要求）
	}
	if i >= len(idx.raw) {
		return false
	}
	key := i*(len(q)+1) + j
	if v := memo[key]; v != 0 {
		return v == 1
	}
	hit := false
	// ④ 字符直接匹配
	if unicode.ToLower(idx.raw[i]) == q[j] {
		hit = matchFrom(idx, i+1, q, j+1, memo)
	}
	if !hit {
		for _, cand := range idx.syllables[i] {
			cr := []rune(cand)
			if len(cr) == 0 {
				continue
			}
			// ① 音头
			if cr[0] == q[j] && matchFrom(idx, i+1, q, j+1, memo) {
				hit = true
				break
			}
			// ② 整音节
			if len(q)-j >= len(cr) && hasRunePrefix(cr, q[j:j+len(cr)]) &&
				matchFrom(idx, i+1, q, j+len(cr), memo) {
				hit = true
				break
			}
			// ③ 尾段前缀（查询串到此结束）
			if len(q)-j < len(cr) && hasRunePrefix(cr, q[j:]) {
				hit = true
				break
			}
		}
	}
	if hit {
		memo[key] = 1
	} else {
		memo[key] = 2
	}
	return hit
}

// hasRunePrefix 判断 s 是否以 prefix 开头（按 rune 比较）。
func hasRunePrefix(s, prefix []rune) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i, r := range prefix {
		if s[i] != r {
			return false
		}
	}
	return true
}

// digitsOnly 只保留数字与开头的 +（用于"输入 138 命中 +86 138-0000-0000"）。
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && b.Len() == 0:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "+" {
		return ""
	}
	return out
}
