package contacts

// 通信录存储（REQ-UI-032 ~ REQ-UI-037）的回归测试：去重（含改号）、
// 导入时常用标志只增不减、批量操作与持久化。

import (
	"errors"
	"path/filepath"
	"testing"

	"mti-sip-phone/internal/core"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "contacts.db"))
	if err != nil {
		t.Fatalf("打开通信录失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func listNames(t *testing.T, s *Store) map[string]core.Contact {
	t.Helper()
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]core.Contact, len(list))
	for _, c := range list {
		m[core.ContactKey(c.Number)] = c
	}
	return m
}

// TestSaveAndList 保存与读取（按规范化号码排序、时间戳落库）。
func TestSaveAndList(t *testing.T) {
	s := openTestStore(t)
	if err := s.Save("", core.Contact{Number: "2901", Name: "李四"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("", core.Contact{Number: "2999", Name: "张三", Note: "前台", Starred: true}); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("应有 2 条，实际 %d", len(list))
	}
	if list[0].Number != "2901" || list[1].Number != "2999" {
		t.Fatalf("应按号码排序，实际 %+v", list)
	}
	if !list[1].Starred || list[1].Note != "前台" {
		t.Fatalf("字段没落库：%+v", list[1])
	}
	if list[1].UpdatedAt.IsZero() {
		t.Fatal("updated_at 不该为 0")
	}
}

// TestSaveDuplicate 号码重复必须被拒（新增撞已有号码、改号撞别的条目），
// 且撞车时整条不落库。
func TestSaveDuplicate(t *testing.T) {
	s := openTestStore(t)
	if err := s.Save("", core.Contact{Number: "2999", Name: "张三"}); err != nil {
		t.Fatal(err)
	}
	// 同一号码的另一种写法也算重复
	err := s.Save("", core.Contact{Number: "sip:2999@10.168.4.40:5060", Name: "另一个张三"})
	if !errors.Is(err, core.ErrContactNumberExists) {
		t.Fatalf("新增重复号码应报 ErrContactNumberExists，实际 %v", err)
	}
	if got := listNames(t, s)["2999"].Name; got != "张三" {
		t.Fatalf("重复新增不该改动已有记录，实际名字 %q", got)
	}

	if err := s.Save("", core.Contact{Number: "2901", Name: "李四"}); err != nil {
		t.Fatal(err)
	}
	// 把 2901 改成 2999（已被占用）→ 拒绝，且 2901 不能被删掉
	err = s.Save("2901", core.Contact{Number: "2999", Name: "李四"})
	if !errors.Is(err, core.ErrContactNumberExists) {
		t.Fatalf("改号撞车应报 ErrContactNumberExists，实际 %v", err)
	}
	m := listNames(t, s)
	if _, ok := m["2901"]; !ok {
		t.Fatal("改号失败后原记录不该被删除")
	}

	// 改号到空闲号码：旧记录消失、新记录就位。
	// 常用标志以**传入值**为准（界面的修改对话框带着当前勾选状态整条提交）。
	if err := s.Save("2901", core.Contact{Number: "2902", Name: "李四", Starred: true}); err != nil {
		t.Fatal(err)
	}
	m = listNames(t, s)
	if _, ok := m["2901"]; ok {
		t.Fatal("改号后旧号码不该还在")
	}
	if c := m["2902"]; c.Name != "李四" || !c.Starred {
		t.Fatalf("改号后新记录不对：%+v", c)
	}
}

// TestSaveUpdatesExisting 修改同一个号码（号码没变）直接覆盖整条。
func TestSaveUpdatesExisting(t *testing.T) {
	s := openTestStore(t)
	if err := s.Save("", core.Contact{Number: "2999", Name: "张三"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("2999", core.Contact{Number: "2999", Name: "张三丰", Note: "改过", Starred: true}); err != nil {
		t.Fatal(err)
	}
	c := listNames(t, s)["2999"]
	if c.Name != "张三丰" || c.Note != "改过" || !c.Starred {
		t.Fatalf("修改没生效：%+v", c)
	}
	if n := len(listNames(t, s)); n != 1 {
		t.Fatalf("修改不该产生新记录，实际 %d 条", n)
	}
}

// TestSetStarredDeleteClear 批量设常用/删除/清空。
func TestSetStarredDeleteClear(t *testing.T) {
	s := openTestStore(t)
	for _, n := range []string{"1001", "1002", "1003"} {
		if err := s.Save("", core.Contact{Number: n, Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.SetStarred([]string{"1001", "1002"}, true)
	if err != nil || n != 2 {
		t.Fatalf("设常用应影响 2 条，实际 %d err=%v", n, err)
	}
	m := listNames(t, s)
	if !m["1001"].Starred || !m["1002"].Starred || m["1003"].Starred {
		t.Fatalf("常用标志不对：%+v", m)
	}
	n, err = s.SetStarred([]string{"1001"}, false)
	if err != nil || n != 1 {
		t.Fatalf("取消常用应影响 1 条，实际 %d err=%v", n, err)
	}
	if listNames(t, s)["1001"].Starred {
		t.Fatal("取消常用没生效")
	}
	if n, err = s.Delete([]string{"1002", "1003"}); err != nil || n != 2 {
		t.Fatalf("删除应影响 2 条，实际 %d err=%v", n, err)
	}
	if len(listNames(t, s)) != 1 {
		t.Fatalf("删除后应剩 1 条，实际 %d", len(listNames(t, s)))
	}
	if n, err = s.Clear(); err != nil || n != 1 {
		t.Fatalf("清空应删 1 条，实际 %d err=%v", n, err)
	}
	if len(listNames(t, s)) != 0 {
		t.Fatal("清空后不该还有记录")
	}
}

// TestMergeStarredOnlyGrows 导入时常用标志**只增不减**（用户口径）：
//   - 本地常用 + CSV 不是 → 保持常用；
//   - 本地不是 + CSV 是常用 → 变成常用；
//   - 新号码按 CSV 的值。
func TestMergeStarredOnlyGrows(t *testing.T) {
	s := openTestStore(t)
	if err := s.Save("", core.Contact{Number: "2999", Name: "旧名字", Starred: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("", core.Contact{Number: "2002", Name: "张三"}); err != nil {
		t.Fatal(err)
	}
	added, updated, err := s.Merge([]core.Contact{
		{Number: "2999", Name: "新名字", Note: "导入的备注", Starred: false}, // 本地常用 → 保持常用
		{Number: "2002", Name: "张三", Starred: true},                  // 本地不常用 → 变常用
		{Number: "sip:2901@host", Name: "李四", Starred: true},         // 新号码：取 CSV 的常用
		{Number: "1001", Name: "王五"},                                 // 新号码：CSV 也不是常用
		{Number: "2901", Name: "李四（重复行）"},                            // 文件内重复：常用仍为"或"的结果
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 || updated != 3 {
		t.Fatalf("新增/覆盖计数不对：added=%d updated=%d", added, updated)
	}
	m := listNames(t, s)
	if c := m["2999"]; c.Name != "新名字" || c.Note != "导入的备注" || !c.Starred {
		t.Fatalf("本地常用不该被 CSV 的「否」冲掉：%+v", c)
	}
	if c := m["2002"]; !c.Starred {
		t.Fatalf("本地不常用而 CSV 是常用，应变成常用：%+v", c)
	}
	if c := m["2901"]; c.Name != "李四（重复行）" || !c.Starred {
		t.Fatalf("新号码应整条新增且常用取 CSV：%+v", c)
	}
	if c := m["1001"]; c.Name != "王五" || c.Starred {
		t.Fatalf("CSV 不是常用的新号码不该变常用：%+v", c)
	}
	if n := len(m); n != 4 {
		t.Fatalf("应共 4 条，实际 %d", n)
	}
}

// TestPersistence 关闭后重新打开数据还在（WAL 落盘）。
func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save("", core.Contact{Number: "2999", Name: "张三", Starred: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	m := listNames(t, s2)
	if c, ok := m["2999"]; !ok || c.Name != "张三" || !c.Starred {
		t.Fatalf("重开后记录丢失或不对：%+v", m)
	}
}

// TestCloseIdempotent 关闭可重复调用，关闭后写入返回已关闭错误而不是 panic。
func TestCloseIdempotent(t *testing.T) {
	s := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("重复关闭应幂等，实际 %v", err)
	}
	if err := s.Save("", core.Contact{Number: "2999"}); !errors.Is(err, errClosed) {
		t.Fatalf("关闭后写入应返回 errClosed，实际 %v", err)
	}
}
