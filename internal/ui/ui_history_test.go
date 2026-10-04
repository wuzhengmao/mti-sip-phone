package ui

// 通话记录（REQ-CALL-007 / REQ-UI-031）展示文案、方向图标与选择手势的回归测试。

import (
	"sort"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

var historyBase = time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)

// rec 造一条记录：answered/ended 传 0 表示该时刻不存在。
func rec(dir string, startedSec, answeredSec, endedSec int, reason string, missed bool) core.CallRecord {
	r := core.CallRecord{
		CallID:    "c1",
		Direction: dir,
		Remote:    "1002",
		StartedAt: historyBase.Add(time.Duration(startedSec) * time.Second),
		Reason:    reason,
		Missed:    missed,
	}
	if answeredSec > 0 {
		r.AnsweredAt = historyBase.Add(time.Duration(answeredSec) * time.Second)
	}
	if endedSec > 0 {
		r.EndedAt = historyBase.Add(time.Duration(endedSec) * time.Second)
	}
	return r
}

func TestHistoryStatusText(t *testing.T) {
	cases := []struct {
		name string
		rec  core.CallRecord
		want string
	}{
		// 来电：未接听 / 拒接 / 已接通
		{"来电接通", rec("incoming", 0, 10, 90, "remote_hangup", false), "已接通"},
		{"来电未接（对端取消）", rec("incoming", 0, 0, 7, "remote_cancel", true), "未接听"},
		{"来电对端挂断", rec("incoming", 0, 0, 7, "remote_hangup", true), "未接听"},
		{"本端拒接", rec("incoming", 0, 0, 5, "rejected", false), "拒接"},
		{"振铃中本端挂断", rec("incoming", 0, 0, 5, "local_hangup", false), "未接听"},
		{"来电其他未接听", rec("incoming", 0, 0, 5, "failed", false), "未接听"},
		// 呼出：未接通 / 取消 / 已接通
		{"呼出接通", rec("outgoing", 0, 10, 90, "remote_hangup", false), "已接通"},
		{"呼出对方忙", rec("outgoing", 0, 0, 5, "486 Busy Here", false), "未接通"},
		{"呼出无应答", rec("outgoing", 0, 0, 30, "408 Request Timeout", false), "未接通"},
		{"呼出本端取消", rec("outgoing", 0, 0, 5, "local_hangup", false), "取消"},
		{"崩溃中断", rec("outgoing", 0, 0, 5, core.ReasonInterrupted, false), "已中断"},
		// 进行中：结束上报还没到（记录已入库）
		{"通话中", rec("outgoing", 0, 10, 0, "", false), "通话中"},
		{"来电振铃中", rec("incoming", 0, 0, 0, "", false), "振铃中"},
		{"呼出呼叫中", rec("outgoing", 0, 0, 0, "", false), "呼叫中"},
	}
	for _, c := range cases {
		if got := historyStatusText(c.rec); got != c.want {
			t.Fatalf("%s: 状态应为 %q，实际 %q", c.name, c.want, got)
		}
	}
}

// TestHistoryStatusTextNotAnsweredIsNotConnected 拒接/本端挂断的来电
// missed=false（用户当场处理了），不能因此标成"已接通"。
func TestHistoryStatusTextNotAnsweredIsNotConnected(t *testing.T) {
	for _, reason := range []string{"rejected", "local_hangup"} {
		r := rec("incoming", 0, 0, 5, reason, false)
		if got := historyStatusText(r); got == "已接通" {
			t.Fatalf("%s: 未接通的来电不该显示已接通", reason)
		}
	}
}

// TestHistoryDirectionResources 方向图标：话机是主体、箭头表方向（↗/↙）、
// 摄像头标记表视频通话，颜色都表示结果（绿=已接通、红=未接通）。
func TestHistoryDirectionResources(t *testing.T) {
	mk := func(dir string, video, answered bool) core.CallRecord {
		r := core.CallRecord{CallID: "c", Direction: dir, HasVideo: video}
		if answered {
			r.AnsweredAt = historyBase
		}
		return r
	}
	cases := []struct {
		rec          core.CallRecord
		phone, arrow fyne.Resource
	}{
		{mk("incoming", false, true), IconPhoneGreen, IconArrowInGreen},
		{mk("incoming", false, false), IconPhoneRed, IconArrowInRed},
		{mk("outgoing", false, true), IconPhoneGreen, IconArrowOutGreen},
		{mk("outgoing", false, false), IconPhoneRed, IconArrowOutRed},
		// 视频通话在方向图标上不体现（第一行号码右边另有摄像头标记）
		{mk("incoming", true, true), IconPhoneGreen, IconArrowInGreen},
		{mk("incoming", true, false), IconPhoneRed, IconArrowInRed},
		{mk("outgoing", true, true), IconPhoneGreen, IconArrowOutGreen},
		{mk("outgoing", true, false), IconPhoneRed, IconArrowOutRed},
	}
	for i, c := range cases {
		phone, arrow := historyDirectionResources(c.rec)
		if phone != c.phone || arrow != c.arrow {
			t.Fatalf("第 %d 种组合图标不对：%v/%v", i, phone.Name(), arrow.Name())
		}
	}
}

// TestHistorySeparatorColor 分割线颜色随主题明暗：深色深灰、浅色浅灰。
func TestHistorySeparatorColor(t *testing.T) {
	test.NewApp() // 需要 Fyne 应用才能读主题
	if got := historySeparatorColor(); got == nil {
		t.Fatal("分割线颜色不该为空")
	}
}

func TestHistoryDurationText(t *testing.T) {
	cases := []struct {
		name string
		rec  core.CallRecord
		want string
	}{
		// 已接通：拨号/振铃时长（开始 → 接通）+ 通话时长（接通 → 结束）
		{"呼出已接通", rec("outgoing", 0, 10, 90, "remote_hangup", false), "拨号 10 秒 · 通话 1 分 20 秒"},
		{"来电已接通", rec("incoming", 0, 8, 80, "remote_hangup", false), "振铃 8 秒 · 通话 1 分 12 秒"},
		// 未接通：只有拨号/振铃时长 = 开始 → 结束
		{"呼出未接通", rec("outgoing", 0, 0, 5, "486 Busy Here", false), "拨号 5 秒"},
		{"来电未接", rec("incoming", 0, 0, 7, "remote_cancel", true), "振铃 7 秒"},
		{"来电拒接", rec("incoming", 0, 0, 3, "rejected", false), "振铃 3 秒"},
	}
	for _, c := range cases {
		if got := historyDurationText(c.rec); got != c.want {
			t.Fatalf("%s: 时长应为 %q，实际 %q", c.name, c.want, got)
		}
	}
}

// TestHistoryDurationTextInProgress 进行中的记录算到此刻（至少 1 秒）。
func TestHistoryDurationTextInProgress(t *testing.T) {
	r := core.CallRecord{CallID: "c1", Direction: "incoming", Remote: "1002", StartedAt: time.Now()}
	if got := historyDurationText(r); got != "振铃 1 秒" {
		t.Fatalf("进行中的来电应为振铃时长，实际 %q", got)
	}
	r.AnsweredAt = time.Now()
	if got := historyDurationText(r); !strings.Contains(got, "通话 1 秒") {
		t.Fatalf("进行中且已接通应带上通话时长，实际 %q", got)
	}
}

func TestHistoryTimeText(t *testing.T) {
	now := historyBase
	if got := historyTimeText(historyBase.Add(90*time.Minute), now); got != "10-04 11:30" {
		t.Fatalf("同年应省略年份，实际 %q", got)
	}
	if got := historyTimeText(historyBase.AddDate(-1, 0, 0), now); got != "2025-10-04 10:00" {
		t.Fatalf("跨年应带上年份，实际 %q", got)
	}
	if got := historyTimeText(time.Time{}, now); got != "" {
		t.Fatalf("零值时间应为空串，实际 %q", got)
	}
}

func TestHistoryWhoText(t *testing.T) {
	r := core.CallRecord{Remote: "1002", RemoteName: "张三"}
	if got := historyWhoText(r); got != "张三（1002）" {
		t.Fatalf("有显示名时应带上号码，实际 %q", got)
	}
	r.RemoteName = ""
	if got := historyWhoText(r); got != "1002" {
		t.Fatalf("无显示名时用号码，实际 %q", got)
	}
	r.RemoteName = "1002"
	if got := historyWhoText(r); got != "1002" {
		t.Fatalf("显示名与号码相同时不该重复，实际 %q", got)
	}
	// 来自 SIP 报文的文本：控制字符要去掉
	r = core.CallRecord{Remote: "1002", RemoteName: "张\n三"}
	if got := historyWhoText(r); got != "张三（1002）" {
		t.Fatalf("控制字符应清理，实际 %q", got)
	}
}

// TestHistoryMetaText 第二行 = 状态 · 振铃/拨号时长（· 通话时长）。
func TestHistoryMetaText(t *testing.T) {
	got := historyMetaText(rec("incoming", 0, 0, 7, "remote_cancel", true))
	if want := "未接听 · 振铃 7 秒"; got != want {
		t.Fatalf("第二行文案应为 %q，实际 %q", want, got)
	}
	got = historyMetaText(rec("outgoing", 0, 10, 90, "remote_hangup", false))
	if want := "已接通 · 拨号 10 秒 · 通话 1 分 20 秒"; got != want {
		t.Fatalf("第二行文案应为 %q，实际 %q", want, got)
	}
}

// TestClipToWidth 超宽文本按像素截断并补省略号。
func TestClipToWidth(t *testing.T) {
	style := fyne.TextStyle{}
	if got := clipToWidth("1002", 14, style, 200); got != "1002" {
		t.Fatalf("放得下就不该截断，实际 %q", got)
	}
	long := "一个特别特别长的显示名用来测试截断效果"
	got := clipToWidth(long, 14, style, 80)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("超宽应截断并补省略号，实际 %q", got)
	}
	if w := fyne.MeasureText(got, 14, style).Width; w > 80 {
		t.Fatalf("截断后宽度 %.1f 仍超过 80", w)
	}
	if maxW := float32(0); clipToWidth(long, 14, style, maxW) != long {
		t.Fatal("宽度未知（0）时不该截断")
	}
}

// newHistoryGestureTest 建一个真实构建出来的记录页，用于验证选择手势语义
// （手势由列表容器在 MouseDown/MouseUp/Dragged 里翻译成这几个调用）。
func newHistoryGestureTest(t *testing.T) *historyState {
	t.Helper()
	app := test.NewApp() // 构建真实控件需要 Fyne 应用（光栅化 SVG 图标）
	a := &App{fyne: app, core: core.New(config.Default(), nil, core.NewBus())}
	a.buildHistoryPage()
	st := a.history
	for _, id := range []string{"a", "b", "c", "d"} {
		st.rows = append(st.rows, core.CallRecord{CallID: id, Direction: "outgoing", Remote: id, StartedAt: historyBase})
	}
	return st
}

// selectedIDs 当前选中集合（排序后便于比较）。
func selectedIDs(st *historyState) string {
	ids := make([]string, 0, len(st.sel))
	for id := range st.sel {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// TestHistoryClickSelect 单击：只选它、再点取消、点空白清空。
func TestHistoryClickSelect(t *testing.T) {
	st := newHistoryGestureTest(t)
	st.clickSelect(0, false)
	if got := selectedIDs(st); got != "a" {
		t.Fatalf("单击应只选中该行，实际 %q", got)
	}
	st.clickSelect(0, false) // 再点一次 = 取消
	if got := selectedIDs(st); got != "" {
		t.Fatalf("再点已选中的行应取消选择，实际 %q", got)
	}
	st.clickSelect(2, false)
	st.clickSelect(99, false) // 行号越界 = 点在空白处
	if got := selectedIDs(st); got != "" {
		t.Fatalf("点空白处应清空选择，实际 %q", got)
	}
}

// TestHistoryShiftSelect Shift 单击：加入/移出，可跨行点选。
func TestHistoryShiftSelect(t *testing.T) {
	st := newHistoryGestureTest(t)
	st.clickSelect(1, false) // 普通单击先选中 b
	st.clickSelect(3, true)  // Shift 加入 d
	if got := selectedIDs(st); got != "b,d" {
		t.Fatalf("Shift 单击应加入，实际 %q", got)
	}
	st.clickSelect(1, true) // Shift 再点 b = 移出
	if got := selectedIDs(st); got != "d" {
		t.Fatalf("Shift 再点应移出，实际 %q", got)
	}
}

// TestHistoryDragSelect 拖动选中连续区间（含反向拖动与越界）。
func TestHistoryDragSelect(t *testing.T) {
	st := newHistoryGestureTest(t)
	st.dragSelect(0, 2)
	if got := selectedIDs(st); got != "a,b,c" {
		t.Fatalf("拖动应选中连续区间，实际 %q", got)
	}
	st.dragSelect(3, 1) // 反向拖
	if got := selectedIDs(st); got != "b,c,d" {
		t.Fatalf("反向拖动应等价于正向，实际 %q", got)
	}
	st.dragSelect(-5, 99) // 越界要夹紧到有效行号
	if got := selectedIDs(st); got != "a,b,c,d" {
		t.Fatalf("越界拖动应夹紧到全部行，实际 %q", got)
	}
}

// TestHistoryIndexAt 坐标 → 行号（行高固定，列表容器不需要知道滚动偏移）。
func TestHistoryIndexAt(t *testing.T) {
	st := newHistoryGestureTest(t)
	cases := map[float32]int{-1: -1, 0: 0, historyRowHeight - 1: 0, historyRowHeight: 1, historyRowHeight*3 + 5: 3}
	for y, want := range cases {
		if got := st.indexAt(y); got != want {
			t.Fatalf("y=%.1f 应为第 %d 行，实际 %d", y, want, got)
		}
	}
}

// TestHistoryReloadClearsSelection 重新加载会清空选择（删除后不应残留选中）。
func TestHistoryReloadClearsSelection(t *testing.T) {
	st := newHistoryGestureTest(t)
	st.clickSelect(0, false)
	st.dragSelect(0, 3)
	st.reload()
	if got := selectedIDs(st); got != "" {
		t.Fatalf("重新加载后不该残留选中，实际 %q", got)
	}
}

// TestHistoryRowMenuTargets 右键菜单的删除目标：右键的行在选中集合里就删整批，
// 否则只删它自己（右键不改变选中状态）。
func TestHistoryRowMenuTargets(t *testing.T) {
	st := newHistoryGestureTest(t)

	// 未选中任何行：只删右键的那一条
	if ids := st.menuTargets(1); len(ids) != 1 || ids[0] != "b" {
		t.Fatalf("未选中时应只删右键的一行，实际 %v", ids)
	}
	// 右键的行在选中集合里：删整批
	st.clickSelect(1, false)
	st.clickSelect(3, true)
	if ids := st.menuTargets(3); len(ids) != 2 {
		t.Fatalf("右键已选中的行应删整批，实际 %v", ids)
	}
	// 右键的行不在选中集合里：仍只删它自己
	if ids := st.menuTargets(0); len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("右键未选中的行应只删它，实际 %v", ids)
	}
}
