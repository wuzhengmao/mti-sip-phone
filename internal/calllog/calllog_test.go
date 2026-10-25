package calllog

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"mti-sip-phone/internal/core"
)

// openTest 建一个临时库；测试结束自动关闭。
func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "call-log.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// queryAll 查全部（页大小给足）。
func queryAll(t *testing.T, s *Store, remote string) core.CallLogPage {
	t.Helper()
	page, err := s.Query(core.CallLogFilter{Remote: remote, Limit: 1000})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	return page
}

// start 造一条通话记录。
func start(s *Store, id, dir, remote string, at time.Time) {
	s.Start(core.CallRecord{CallID: id, Direction: dir, Remote: remote, StartedAt: at})
}

func TestStartQueryAndOrder(t *testing.T) {
	s, _ := openTest(t)
	base := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	start(s, "c1", "outgoing", "1001", base)
	start(s, "c2", "incoming", "1002", base.Add(time.Minute))
	start(s, "c3", "outgoing", "1003", base.Add(2*time.Minute))

	page := queryAll(t, s, "")
	if page.Total != 3 || len(page.Records) != 3 {
		t.Fatalf("应有 3 条记录，得到 total=%d len=%d", page.Total, len(page.Records))
	}
	// 按开始时间倒序：c3 → c2 → c1
	for i, want := range []string{"c3", "c2", "c1"} {
		if got := page.Records[i].CallID; got != want {
			t.Fatalf("第 %d 条应为 %s，得到 %s", i, want, got)
		}
	}
	if got := page.Records[2].StartedAt.UnixMilli(); got != base.UnixMilli() {
		t.Fatalf("开始时间应还原为 %d，得到 %d", base.UnixMilli(), got)
	}
	// 未接通/进行中的字段应是零值，不能是 1970 年
	if !page.Records[0].AnsweredAt.IsZero() || !page.Records[0].EndedAt.IsZero() {
		t.Fatalf("未接通记录的 AnsweredAt/EndedAt 应为零值，得到 %v / %v",
			page.Records[0].AnsweredAt, page.Records[0].EndedAt)
	}
}

func TestStartIsIdempotent(t *testing.T) {
	s, _ := openTest(t)
	at := time.Now()
	start(s, "c1", "outgoing", "1001", at)
	// 同一 call_id 再来一次（带不同的号码）不应产生第二条
	s.Start(core.CallRecord{CallID: "c1", Direction: "outgoing", Remote: "9999", StartedAt: at})

	page := queryAll(t, s, "")
	if page.Total != 1 {
		t.Fatalf("重复 Start 应只有 1 条，得到 %d", page.Total)
	}
	if page.Records[0].Remote != "1001" {
		t.Fatalf("重复 Start 不该改写已有记录，得到 %q", page.Records[0].Remote)
	}
}

// TestFinishIsIdempotent：SIP 层对未接通呼出会上报两次结束（OnFailed 之后
// 还会再来一次 OnTerminated），只有第一次生效、保留更具体的原因。
func TestFinishIsIdempotent(t *testing.T) {
	s, _ := openTest(t)
	at := time.Now()
	start(s, "c1", "outgoing", "1001", at)
	firstEnd := at.Add(3 * time.Second)
	s.Finish("c1", firstEnd, "486 Busy Here", false)
	s.Finish("c1", at.Add(9*time.Second), "failed", false) // 第二次上报

	rec := queryAll(t, s, "").Records[0]
	if rec.Reason != "486 Busy Here" {
		t.Fatalf("第二次结束上报不该覆盖原因，得到 %q", rec.Reason)
	}
	if rec.EndedAt.UnixMilli() != firstEnd.UnixMilli() {
		t.Fatalf("第二次结束上报不该覆盖结束时刻，得到 %v", rec.EndedAt)
	}
}

func TestFinishWithoutStartIsNoop(t *testing.T) {
	s, _ := openTest(t)
	s.Finish("missing", time.Now(), "failed", false) // 不该 panic
	if page := queryAll(t, s, ""); page.Total != 0 {
		t.Fatalf("无开始记录时结束上报不该产生记录，得到 %d 条", page.Total)
	}
}

// TestLifecycleOrder：开始/接通/结束连发（写入是异步的），查询走同一条
// FIFO 队列，因此一定能看到完整终态——这条保证若被破坏（例如改成
// go fn() 并发写），Finish 会先于 Start 执行、UPDATE 影响 0 行。
func TestLifecycleOrder(t *testing.T) {
	s, _ := openTest(t)
	at := time.Now()
	start(s, "c1", "incoming", "1001", at)
	s.MarkAnswered("c1", at.Add(2*time.Second))
	s.Finish("c1", at.Add(65*time.Second), "remote_hangup", false)

	rec := queryAll(t, s, "").Records[0]
	if rec.AnsweredAt.UnixMilli() != at.Add(2*time.Second).UnixMilli() {
		t.Fatalf("接通时刻不对: %v", rec.AnsweredAt)
	}
	if rec.EndedAt.UnixMilli() != at.Add(65*time.Second).UnixMilli() {
		t.Fatalf("结束时刻不对: %v", rec.EndedAt)
	}
	if rec.Missed || rec.Reason != "remote_hangup" {
		t.Fatalf("结束原因/未接标记不对: missed=%v reason=%q", rec.Missed, rec.Reason)
	}
}

func TestMarkAnsweredOnlyOnce(t *testing.T) {
	s, _ := openTest(t)
	at := time.Now()
	start(s, "c1", "outgoing", "1001", at)
	s.MarkAnswered("c1", at.Add(1*time.Second))
	s.MarkAnswered("c1", at.Add(5*time.Second)) // 重复

	if got := queryAll(t, s, "").Records[0].AnsweredAt; got.UnixMilli() != at.Add(1*time.Second).UnixMilli() {
		t.Fatalf("接通时刻应只记第一次，得到 %v", got)
	}
}

func TestPaging(t *testing.T) {
	s, _ := openTest(t)
	base := time.Date(2026, 10, 4, 9, 0, 0, 0, time.Local)
	for i := 0; i < 120; i++ {
		start(s, fmt.Sprintf("c%03d", i), "outgoing", fmt.Sprintf("10%02d", i), base.Add(time.Duration(i)*time.Second))
	}

	var seen []string
	offset, pages := 0, 0
	for {
		page, err := s.Query(core.CallLogFilter{Limit: 50, Offset: offset})
		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}
		if page.Total != 120 {
			t.Fatalf("总数应为 120，得到 %d", page.Total)
		}
		if len(page.Records) == 0 {
			break
		}
		for _, r := range page.Records {
			seen = append(seen, r.CallID)
		}
		offset += len(page.Records)
		pages++
		if !page.HasMore {
			break
		}
	}
	if pages != 3 || len(seen) != 120 {
		t.Fatalf("应分 3 页共 120 条，得到 pages=%d len=%d", pages, len(seen))
	}
	if seen[0] != "c119" || seen[119] != "c000" {
		t.Fatalf("分页顺序不对: 首 %s 末 %s", seen[0], seen[119])
	}
}

func TestFilterByRemote(t *testing.T) {
	s, _ := openTest(t)
	at := time.Now()
	start(s, "c1", "outgoing", "1001", at)
	start(s, "c2", "outgoing", "2002", at.Add(time.Second))
	start(s, "c3", "incoming", "1002", at.Add(2*time.Second))

	page := queryAll(t, s, "100")
	if page.Total != 2 || len(page.Records) != 2 {
		t.Fatalf("按 100 筛选应命中 2 条，得到 total=%d len=%d", page.Total, len(page.Records))
	}
	// LIKE 的通配符必须当普通字符处理（用 instr 而非 LIKE 的原因）
	if page := queryAll(t, s, "%"); page.Total != 0 {
		t.Fatalf("%% 应作为普通字符、命中 0 条，得到 %d", page.Total)
	}
	if page := queryAll(t, s, "_"); page.Total != 0 {
		t.Fatalf("_ 应作为普通字符、命中 0 条，得到 %d", page.Total)
	}
}

func TestDeleteAndClear(t *testing.T) {
	s, _ := openTest(t)
	at := time.Now()
	for i := 0; i < 5; i++ {
		start(s, fmt.Sprintf("c%d", i), "outgoing", fmt.Sprintf("100%d", i), at.Add(time.Duration(i)*time.Second))
	}

	n, err := s.Delete([]string{"c1", "c3", "不存在"})
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("应删除 2 条，得到 %d", n)
	}
	if page := queryAll(t, s, ""); page.Total != 3 {
		t.Fatalf("删除后应剩 3 条，得到 %d", page.Total)
	}
	if n, err := s.Delete(nil); err != nil || n != 0 {
		t.Fatalf("空删除应无副作用，得到 n=%d err=%v", n, err)
	}

	n, err = s.Clear()
	if err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if n != 3 {
		t.Fatalf("应清空 3 条，得到 %d", n)
	}
	if page := queryAll(t, s, ""); page.Total != 0 || page.HasMore {
		t.Fatalf("清空后应为空，得到 total=%d hasMore=%v", page.Total, page.HasMore)
	}
}

// TestReopenKeepsDataAndSweepsInterrupted：重开后数据仍在；上次运行遗留的
// "进行中"记录被清扫为中断，且时长不为负。
func TestReopenKeepsDataAndSweepsInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "call-log.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)

	start(s, "done", "outgoing", "1001", at)
	s.Finish("done", at.Add(10*time.Second), "remote_hangup", false)

	start(s, "unanswered", "incoming", "1002", at.Add(time.Minute)) // 未接通
	start(s, "answered", "incoming", "1003", at.Add(2*time.Minute))
	s.MarkAnswered("answered", at.Add(2*time.Minute+5*time.Second)) // 接通后进程没了
	// 不 Finish，直接关闭（模拟进程被终止）
	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	defer s2.Close()
	page := queryAll(t, s2, "")
	if page.Total != 3 {
		t.Fatalf("重开后应有 3 条记录，得到 %d", page.Total)
	}
	byID := map[string]core.CallRecord{}
	for _, r := range page.Records {
		byID[r.CallID] = r
	}
	if got := byID["done"]; got.Reason != "remote_hangup" || got.EndedAt.UnixMilli() != at.Add(10*time.Second).UnixMilli() {
		t.Fatalf("已完成记录被重开改写: %+v", got)
	}
	// 未接通的来电：结束时刻 = 开始时刻，时长 0（不为负），且算未接
	if got := byID["unanswered"]; got.Reason != core.ReasonInterrupted || !got.Missed ||
		got.EndedAt.UnixMilli() != got.StartedAt.UnixMilli() {
		t.Fatalf("未接来电的中断清扫不对: %+v", got)
	}
	// 已接通的来电：结束时刻 = 接通时刻（不早于开始），不算未接
	if got := byID["answered"]; got.Reason != core.ReasonInterrupted || got.Missed ||
		got.EndedAt.Before(got.StartedAt) {
		t.Fatalf("已接通来电的中断清扫不对: %+v", got)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s, _ := openTest(t)
	if err := s.Close(); err != nil {
		t.Fatalf("首次关闭失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("重复关闭应无副作用: %v", err)
	}
	// 关闭后的写入与查询都不该 panic
	start(s, "c1", "outgoing", "1001", time.Now())
	if _, err := s.Query(core.CallLogFilter{}); err == nil {
		t.Fatal("关闭后查询应返回错误")
	}
}

func TestTruncateUntrustedText(t *testing.T) {
	s, _ := openTest(t)
	long := make([]rune, maxTextLen+50)
	for i := range long {
		long[i] = 'x'
	}
	start(s, "c1", "incoming", string(long), time.Now())

	rec := queryAll(t, s, "").Records[0]
	if len([]rune(rec.Remote)) != maxTextLen {
		t.Fatalf("超长号码应截断到 %d 个字符，得到 %d", maxTextLen, len([]rune(rec.Remote)))
	}
}

// TestDisplayNameRoundTrip 座席显示名落库并原样查回（REQ-CALL-007 补充）：
// 通话记录展示名称时优先用它，其次才回退通信录。
func TestDisplayNameRoundTrip(t *testing.T) {
	s, _ := openTest(t)
	s.Start(core.CallRecord{CallID: "c1", Direction: "outgoing", Remote: "2901",
		DisplayName: "测试1", StartedAt: time.Now()})
	rec := queryAll(t, s, "").Records[0]
	if rec.DisplayName != "测试1" {
		t.Fatalf("display_name 应原样查回，得到 %q", rec.DisplayName)
	}
}

// TestLegacySchemaGetsDisplayNameColumn 旧库（user_version=1，没有 display_name）
// 打开时自动补列：旧记录 displayName 为空，新写入可正常保存。
func TestLegacySchemaGetsDisplayNameColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "call-log.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("建旧库失败: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE call_log (
		call_id TEXT PRIMARY KEY, direction TEXT NOT NULL, remote TEXT NOT NULL,
		remote_name TEXT NOT NULL DEFAULT '', started_at INTEGER NOT NULL,
		answered_at INTEGER NOT NULL DEFAULT 0, ended_at INTEGER NOT NULL DEFAULT 0,
		has_video INTEGER NOT NULL DEFAULT 0, missed INTEGER NOT NULL DEFAULT 0,
		reason TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
		INSERT INTO call_log (call_id, direction, remote, started_at, created_at)
		VALUES ('old', 'outgoing', '1001', 1, 1);`); err != nil {
		db.Close()
		t.Fatalf("建旧表失败: %v", err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if rec := queryAll(t, s, "").Records[0]; rec.CallID != "old" || rec.DisplayName != "" {
		t.Fatalf("旧记录应保留且 displayName 为空，得到 %+v", rec)
	}
	s.Start(core.CallRecord{CallID: "new", Direction: "outgoing", Remote: "2002",
		DisplayName: "李四", StartedAt: time.Now()})
	page := queryAll(t, s, "")
	var got string
	for _, r := range page.Records {
		if r.CallID == "new" {
			got = r.DisplayName
		}
	}
	if got != "李四" {
		t.Fatalf("补列后新记录应能保存 display_name，得到 %q", got)
	}
}

// TestPlaceholderRemoteName 网关的占位显示名（FreeSWITCH 出局腿把 From 的
// display-name 写成 "Outbound Call"）不应显示在通话记录里：写入时过滤，
// 老记录（库里已有）读取时同样过滤，界面回退到号码。
func TestPlaceholderRemoteName(t *testing.T) {
	s, _ := openTest(t)
	base := time.Now()
	s.Start(core.CallRecord{
		CallID: "c1", Direction: "incoming", Remote: "91381003",
		RemoteName: "Outbound Call", StartedAt: base,
	})
	s.Start(core.CallRecord{
		CallID: "c2", Direction: "incoming", Remote: "1002",
		RemoteName: "张三", StartedAt: base.Add(time.Minute),
	})
	page := queryAll(t, s, "")
	if len(page.Records) != 2 {
		t.Fatalf("应有 2 条记录，得到 %d", len(page.Records))
	}
	for _, r := range page.Records {
		if r.CallID == "c1" && r.RemoteName != "" {
			t.Fatalf("占位显示名应被过滤，实际 %q", r.RemoteName)
		}
		if r.CallID == "c2" && r.RemoteName != "张三" {
			t.Fatalf("正常显示名应保留，实际 %q", r.RemoteName)
		}
	}

	// 模拟老库：直接写一条带占位名的记录，读取时也要过滤
	if _, err := s.db.Exec(`INSERT INTO call_log
		(call_id, direction, remote, remote_name, display_name, started_at, answered_at,
		 ended_at, has_video, missed, reason, created_at)
		VALUES ('legacy','incoming','91381003','OutboundCall','',0,0,0,0,0,'',0)`); err != nil {
		t.Fatalf("插入老记录失败: %v", err)
	}
	page = queryAll(t, s, "")
	for _, r := range page.Records {
		if r.CallID == "legacy" && r.RemoteName != "" {
			t.Fatalf("老记录的占位显示名读取时也应过滤，实际 %q", r.RemoteName)
		}
	}
}
