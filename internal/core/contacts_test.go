package core

// 通信录（REQ-UI-032 ~ REQ-UI-037）核心层回归测试：号码规范化、CSV 编解码、
// 内存快照与查找、通话事件里的 contact 字段。

import (
	"strings"
	"testing"

	"mti-sip-phone/internal/config"
)

// TestNormalizeNumber 号码规范化：同一个人的各种写法必须归一成同一个键。
func TestNormalizeNumber(t *testing.T) {
	cases := map[string]string{
		"2999":                      "2999",
		" 2999 ":                    "2999",
		"sip:2999@10.168.4.40:5060": "2999",
		"SIP:2999@HOST":             "2999",
		"sips:alice@example.com":    "alice",
		"2999@host":                 "2999",
		"<sip:2901@10.168.4.118>":   "2901",
		"张三 <sip:2999@host>":        "2999",
		"+86 138-0000-0000":         "+8613800000000",
		"(0512)6888-8888":           "051268888888",
		"2901;user=phone":           "2901",
		"sip:u;user=phone@h":        "u",
		"*123#":                     "*123#",
		"":                          "",
		"   ":                       "",
		"---":                       "",
		"Alice":                     "alice",
	}
	for in, want := range cases {
		if got := NormalizeNumber(in); got != want {
			t.Fatalf("NormalizeNumber(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestNormalizeNumberSamePerson 同一号码的多种写法必须得到同一个键
// （去重与匹配都建立在这上面）。
func TestNormalizeNumberSamePerson(t *testing.T) {
	forms := []string{"2999", " sip:2999@10.168.4.40:5060 ", "<sip:2999@host>", "2-9-9-9"}
	want := NormalizeNumber(forms[0])
	for _, f := range forms[1:] {
		if got := NormalizeNumber(f); got != want {
			t.Fatalf("%q 规范化成 %q，与 %q 的 %q 不一致", f, got, forms[0], want)
		}
	}
}

// fakeContacts 内存版通信录存储，供核心层测试（真实 SQL 行为由
// internal/contacts 的单测覆盖）。
type fakeContacts struct {
	list  []Contact
	saves []Contact // 收到保存的记录（含被覆盖前的状态）
	prev  []string  // 每次保存传入的 prevNumber
	star  int
	del   int
}

func (f *fakeContacts) List() ([]Contact, error) { return append([]Contact(nil), f.list...), nil }

func (f *fakeContacts) Save(prevNumber string, c Contact) error {
	f.saves = append(f.saves, c)
	f.prev = append(f.prev, prevNumber)
	key := NormalizeNumber(c.Number)
	for i := range f.list {
		if NormalizeNumber(f.list[i].Number) == key {
			f.list[i] = c
			return nil
		}
	}
	f.list = append(f.list, c)
	return nil
}

func (f *fakeContacts) SetStarred(numbers []string, starred bool) (int, error) {
	f.star = len(numbers)
	return len(numbers), nil
}

func (f *fakeContacts) Delete(numbers []string) (int, error) {
	f.del = len(numbers)
	return len(numbers), nil
}

func (f *fakeContacts) Clear() (int, error) { return len(f.list), nil }

// Merge 按真实语义（号码命中 → 只覆盖号码/名称/备注，常用标志取"或"、只增不减）
// 合并，供核心层验证"导入结果计数来自存储层"。
func (f *fakeContacts) Merge(list []Contact) (int, int, error) {
	added, updated := 0, 0
	for _, c := range list {
		key := NormalizeNumber(c.Number)
		hit := false
		for i := range f.list {
			if NormalizeNumber(f.list[i].Number) == key {
				f.list[i].Number, f.list[i].Name, f.list[i].Note = c.Number, c.Name, c.Note
				f.list[i].Starred = f.list[i].Starred || c.Starred
				hit = true
				break
			}
		}
		if hit {
			updated++
		} else {
			f.list = append(f.list, c)
			added++
		}
	}
	return added, updated, nil
}

func (f *fakeContacts) Close() error { return nil }

func newContactsApp(t *testing.T) (*App, *fakeContacts) {
	t.Helper()
	app := New(config.Default(), nil, NewBus())
	f := &fakeContacts{}
	app.SetContacts(f)
	return app, f
}

// TestLookupContactNormalized 查找要按规范化号码命中（通话记录里存的可能是
// sip:xxx@host 形式）。
func TestLookupContactNormalized(t *testing.T) {
	app, f := newContactsApp(t)
	f.list = []Contact{{Number: "2999", Name: "张三", Note: "前台"}}
	if err := app.ReloadContacts(); err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{"2999", "sip:2999@10.168.4.40:5060", "<sip:2999@host>"} {
		c, ok := app.LookupContact(remote)
		if !ok || c.Name != "张三" {
			t.Fatalf("按 %q 查找应命中张三，实际 %+v ok=%v", remote, c, ok)
		}
	}
	if _, ok := app.LookupContact("2901"); ok {
		t.Fatal("未录入的号码不该命中")
	}
	if _, ok := app.LookupContact(""); ok {
		t.Fatal("空号码不该命中")
	}
}

// TestSaveContactValidation 空号码拒绝、文本截断、写后快照立刻可查。
func TestSaveContactValidation(t *testing.T) {
	app, f := newContactsApp(t)
	if err := app.SaveContact("", Contact{Number: "   "}); err != ErrContactNumberEmpty {
		t.Fatalf("空号码应报 ErrContactNumberEmpty，实际 %v", err)
	}
	long := strings.Repeat("张", 300)
	if err := app.SaveContact("", Contact{Number: "1001", Name: long}); err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(f.saves[0].Name)); got != maxContactFieldLen {
		t.Fatalf("名称应截断到 %d 个字符，实际 %d", maxContactFieldLen, got)
	}
	if _, ok := app.LookupContact("1001"); !ok {
		t.Fatal("保存后应立刻能在快照里查到")
	}
	// 修改：prevNumber 要原样传给存储层
	if err := app.SaveContact("1001", Contact{Number: "1002", Name: "李四"}); err != nil {
		t.Fatal(err)
	}
	if f.prev[1] != "1001" {
		t.Fatalf("修改应把原号码传给存储层，实际 %q", f.prev[1])
	}
}

// TestContactKeysDedupe 批量操作前号码要去空、去重。
func TestContactKeysDedupe(t *testing.T) {
	got := contactKeys([]string{"2999", "sip:2999@host", "", "  ", "2901"})
	if len(got) != 2 || got[0] != "2999" || got[1] != "2901" {
		t.Fatalf("应去空去重后剩 [2999 2901]，实际 %v", got)
	}
}

// TestCallContactInfo 通话事件与状态快照里带通信录信息（REQ-UI-037 的网页侧）。
func TestCallContactInfo(t *testing.T) {
	app, f := newContactsApp(t)
	f.list = []Contact{{Number: "2999", Name: "张三", Note: "前台", Starred: true}}
	if err := app.ReloadContacts(); err != nil {
		t.Fatal(err)
	}
	app.upsertCall(&Call{ID: "c1", Direction: "incoming", Remote: "sip:2999@10.168.4.40", State: CallRinging})

	st := app.State()
	if len(st.Calls) != 1 || st.Calls[0].Contact == nil {
		t.Fatalf("状态快照应带上 contact，实际 %+v", st.Calls)
	}
	if c := st.Calls[0].Contact; c.Name != "张三" || c.Note != "前台" || !c.Starred {
		t.Fatalf("contact 内容不对：%+v", c)
	}
	// 快照是副本：不能因为补了 contact 就写进 a.calls（那会与其它读路径竞争）
	if app.calls["c1"].Contact != nil {
		t.Fatal("不该就地修改 a.calls 里的通话对象")
	}
	// 未命中的通话 contact 为 nil（JSON 里省略）
	app.upsertCall(&Call{ID: "c2", Direction: "outgoing", Remote: "2901", State: CallRinging})
	st = app.State()
	if st.Calls[1].Contact != nil {
		t.Fatalf("未命中通信录时 contact 应为 nil，实际 %+v", st.Calls[1].Contact)
	}
}

// TestMarshalParseContactsCSV CSV 往返：带表头与 BOM，字段含逗号/引号/换行也要能还原。
func TestMarshalParseContactsCSV(t *testing.T) {
	list := []Contact{
		{Number: "2999", Name: "张三", Note: `前台,"主管"`, Starred: true},
		{Number: "+8613800000000", Name: "李四", Note: "第一行\n第二行"},
		{Number: "1001", Name: "", Note: "", Starred: false},
	}
	text, err := MarshalContactsCSV(list)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, csvBOM) {
		t.Fatal("导出应带 UTF-8 BOM（Excel 打开中文不乱码）")
	}
	got, skipped := ParseContactsCSV(text)
	if skipped != 0 {
		t.Fatalf("不该有跳过行，实际 %d", skipped)
	}
	if len(got) != len(list) {
		t.Fatalf("往返条数不一致：%d vs %d", len(got), len(list))
	}
	for i := range list {
		if got[i] != list[i] {
			t.Fatalf("第 %d 条往返不一致：%+v vs %+v", i, got[i], list[i])
		}
	}
}

// TestParseContactsCSVTolerant 导入的容错：BOM、无表头、列数不足、空行、
// 常用列的各种写法、缺号码的行计数。
func TestParseContactsCSVTolerant(t *testing.T) {
	text := csvBOM + "号码,名称,备注\n" + // 缺「常用」列
		"2999,张三,前台\n" +
		"\n" + // 空行
		"2901,李四\n" + // 列数不足
		",王五,号码为空\n" + // 缺号码 → 跳过
		"1001\n" // 只有号码
	got, skipped := ParseContactsCSV(text)
	if skipped != 1 {
		t.Fatalf("应跳过 1 行（无号码），实际 %d", skipped)
	}
	want := []Contact{
		{Number: "2999", Name: "张三", Note: "前台"},
		{Number: "2901", Name: "李四"},
		{Number: "1001"},
	}
	if len(got) != len(want) {
		t.Fatalf("解析出 %d 条，期望 %d 条：%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条不对：%+v vs %+v", i, got[i], want[i])
		}
	}

	// 无表头 + 常用列的各种写法
	got, skipped = ParseContactsCSV("2999,张三,前台,是\n2901,李四,,1\n1001,王五,,no\n")
	if skipped != 0 || len(got) != 3 {
		t.Fatalf("无表头解析失败：skipped=%d got=%+v", skipped, got)
	}
	if !got[0].Starred || !got[1].Starred || got[2].Starred {
		t.Fatalf("常用列解析不对：%+v", got)
	}

	// 列序不同 + 多余列：按表头映射
	got, _ = ParseContactsCSV("备注,号码,多余,常用,名称\n前台,2999,x,是,张三\n")
	if len(got) != 1 || got[0].Name != "张三" || got[0].Note != "前台" || !got[0].Starred {
		t.Fatalf("按表头映射列失败：%+v", got)
	}

	// 引号不规范（LazyQuotes）不该整份拒收
	if got, _ := ParseContactsCSV("2999,\"张三,前台\n"); len(got) != 1 || got[0].Number != "2999" {
		t.Fatalf("不规范引号应容错解析：%+v", got)
	}

	// 空文本
	if got, skipped := ParseContactsCSV("   \n \n"); len(got) != 0 || skipped != 0 {
		t.Fatalf("空文本应解析出 0 条，实际 %+v skipped=%d", got, skipped)
	}
}

// TestImportContactsCSVResult 导入结果计数与存储层返回一致。
func TestImportContactsCSVResult(t *testing.T) {
	app, f := newContactsApp(t)
	f.list = []Contact{{Number: "2999", Name: "旧名字", Starred: true}}
	if err := app.ReloadContacts(); err != nil {
		t.Fatal(err)
	}
	res, err := app.ImportContactsCSV("号码,名称,备注,常用\n2999,新名字,前台,否\n2901,李四,,是\n,\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 {
		t.Fatalf("应跳过 1 行，实际 %d", res.Skipped)
	}
	if res.Added != 1 || res.Updated != 1 {
		t.Fatalf("新增/覆盖计数不对：%+v", res)
	}
}

// TestContactsJSONRoundTrip JSON 编解码（REQ-CONTACT-005）：导出带 count 与 contacts，
// 导入接受"带外壳的对象"与"裸数组"，没有号码的条目计入 skipped，语法错误报错。
func TestContactsJSONRoundTrip(t *testing.T) {
	list := []Contact{
		{Number: "2999", Name: "张三", Note: "前台", Starred: true},
		{Number: "+8613800000000", Name: "李四"},
	}
	data, err := MarshalContactsJSON(list)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"count":2`) {
		t.Fatalf("导出应带 count：%s", data)
	}
	got, skipped, err := ParseContactsJSON(data)
	if err != nil || skipped != 0 {
		t.Fatalf("回灌失败：err=%v skipped=%d", err, skipped)
	}
	if len(got) != 2 || got[0] != list[0] || got[1].Number != list[1].Number {
		t.Fatalf("往返不一致：%+v", got)
	}

	// 裸数组 + 缺号码 + 未知字段
	got, skipped, err = ParseContactsJSON([]byte(`[{"number":"1001","name":"王五","x":1},{"name":"没号码"}]`))
	if err != nil || skipped != 1 || len(got) != 1 || got[0].Name != "王五" {
		t.Fatalf("裸数组解析不对：%+v skipped=%d err=%v", got, skipped, err)
	}

	// 非数组/对象、坏 JSON
	for _, bad := range []string{`{不是 JSON`, `"字符串"`, `123`} {
		if _, _, err := ParseContactsJSON([]byte(bad)); err == nil {
			t.Fatalf("%s 应报错", bad)
		}
	}
	// 空输入：0 条、不报错
	if got, skipped, err := ParseContactsJSON(nil); err != nil || len(got) != 0 || skipped != 0 {
		t.Fatalf("空输入应得到空结果：%+v %d %v", got, skipped, err)
	}
}

// TestImportContactsJSON 走 App 的 JSON 导入：语义与 CSV 一致（常用只增不减）。
func TestImportContactsJSON(t *testing.T) {
	app, f := newContactsApp(t)
	f.list = []Contact{{Number: "2999", Name: "旧", Starred: true}, {Number: "2002", Name: "王五"}}
	if err := app.ReloadContacts(); err != nil {
		t.Fatal(err)
	}
	res, err := app.ImportContactsJSON([]byte(`[
		{"number":"2999","name":"新","starred":false},
		{"number":"2002","name":"王五","starred":true},
		{"number":"2901","name":"李四","starred":true}]`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Updated != 2 || res.Skipped != 0 {
		t.Fatalf("计数不对：%+v", res)
	}
	if c, _ := app.LookupContact("2999"); !c.Starred || c.Name != "新" {
		t.Fatalf("本地常用不该被冲掉：%+v", c)
	}
	if c, _ := app.LookupContact("2002"); !c.Starred {
		t.Fatalf("本地不常用而 JSON 是常用，应变常用：%+v", c)
	}
}
