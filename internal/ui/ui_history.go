package ui

// 通话记录页（REQ-CALL-007 / REQ-UI-031）。
//
// 与配置页一样**切在主窗体内**（不新开窗口），竖屏手机式紧凑列表，一条记录两行：
//
//	[方向图标] 号码/显示名                              开始时间
//	已接通 · 振铃 8 秒 · 通话 1 分 27 秒          [呼叫] [视频呼叫]
//
// 方向图标 = 话机/摄像头 + 右上角箭头（↗ 呼出 / ↙ 呼入），**绿色=已接通、
// 红色=未接通**；通话结果与方向一眼可辨。行距 2px，行间一条细分割线（深色主题
// 用深灰、浅色主题用浅灰）。删除/清空放在页面**底部**（图标 + 文字）。
//
// 分页方式：**滚动到接近底部就加载下一批**（`container.Scroll.OnScrolled`），
// 不做页码/上一页下一页。
//
// 多选：没有复选框、全选、计数这些控件——
//   - 单击某行 = 只选它（再点一次取消）；
//   - Shift + 单击 = 把该行加入/移出选择（可跨行点选）；
//   - 按住拖动 = 选中按下行到当前行的连续区间。
//
// 三个交互都落在 `historyListView` 这一层：Fyne 取"指针下**最深**的匹配对象"，
// 行与行内按钮都不是 Draggable（按钮是 Tappable），所以整片列表区域的
// 按下/拖动都会落到列表容器上，而且事件坐标是相对本容器的——换算行号只需
// `y / 行高`，不需要知道滚动偏移。

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

const (
	// historyPageSize 每批加载条数（滚到接近底部自动加载下一批）。
	historyPageSize = 50
	// historyRowHeight 一整行的高度 = 内容 + 行距 + 分割线。
	historyRowHeight = 46
	// historyRowGap 行距（用户指定 2px）：内容与分割线之间的空隙。
	historyRowGap = 2
	// historyRowSep 分割线高度。
	historyRowSep = 1
	// historyDirIconSize 行首方向图标尺寸（要放得下话机 + 箭头 + 摄像头标记）。
	historyDirIconSize = 30
	// historyVideoMarkSize 第一行号码右边「视频通话」摄像头标记的尺寸。
	historyVideoMarkSize = 16
	// historyVideoIconSize 行内「视频呼叫」按钮里摄像头图标的尺寸（摄像头是
	// 扁宽图形，同尺寸下比话机显小，单独放大一点）。
	historyVideoIconSize = 16
	// historyRowButtonRadius 行内按钮的圆角（比默认 6 更方一点）。
	historyRowButtonRadius = 2
	// historyScrollGap 行内内容右侧给滚动条让出的宽度：滚动条由列表自己画在
	// 列表右缘上，行内容（时间与按钮）都要躲开它，列表宽度本身不变。
	historyScrollGap = 14
	// 方向箭头相对"话机右上角"的微调（像素）。基准是向右上角收：x 减、y 加；
	// 然后两个方向都再上移 historyDirArrowUp，来电箭头再右移 historyDirArrowInDX
	//（来电箭头是呼出箭头的 180° 旋转，视觉重心偏左下，所以补一点）。
	historyDirArrowDX   = float32(4)
	historyDirArrowDY   = float32(7)
	historyDirArrowUp   = float32(1)
	historyDirArrowInDX = float32(1)
	// historyRowIconSize 行内图形按钮尺寸（呼叫/视频呼叫）。
	historyRowIconSize = 24
	// historyLoadAhead 距底部还有这么多像素就预加载下一批。
	historyLoadAhead = 4 * historyRowHeight
	// historyFilterDelay 筛选输入防抖。
	historyFilterDelay = 300 * time.Millisecond
	// historyWhoMax 号码/显示名展示的最大长度（rune），真正的截断由
	// plainText 按可用宽度做（这里只是先挡一道超长文本）。
	historyWhoMax = 60
	// historyFontMain/historyFontSmall 行内字号：比默认正文小一号，
	// 一屏能多放几条（用户要求紧凑）。
	historyFontMain  = 14
	historyFontSmall = 12
)

// 行内图标颜色（方向图标与主题无关，固定色）。
const (
	historyColorOK   = "#43a047" // 已接通：绿
	historyColorFail = "#e53935" // 未接通：红
)

// historyState 通话记录页的状态与控件。
type historyState struct {
	a *App

	rows    []core.CallRecord
	hasMore bool
	loading bool
	filter  string
	loadErr error

	// sel 选中的 call_id 集合。按 call_id 而不是行号：重新加载、筛选、
	// 删除都会让行号整体重排。
	sel map[string]bool
	// pressIdx/pressShift 按下时的行号与 Shift 状态（MouseDown 记，
	// MouseUp 用；拖动时区间起点也取自 pressIdx）。
	pressIdx   int
	pressShift bool
	dragging   bool

	// gen 每次 reload 自增：作废在途的查询结果。
	gen int

	view        *historyListView
	scroll      *container.Scroll
	filterEntry *widget.Entry
	emptyLabel  *widget.Label
	filterTimer *time.Timer
}

// ---------------------------------------------------------------------------
// 页面
// ---------------------------------------------------------------------------

// buildHistoryPage 构建通话记录页（主窗口内容之一，与配置页并列）。
func (a *App) buildHistoryPage() {
	st := &historyState{a: a, sel: map[string]bool{}, hasMore: true}
	a.history = st

	// ---- 顶栏：返回 + 标题（与配置页同款）…… 清空通话记录（标准按钮外观，
	// 深红底白字，与标题同一行、居右）----
	// 「清空通话记录」：**纯标准按钮**——DangerImportance（主题错误红）+
	// 主题垃圾桶图标，文字与图标颜色都由 Fyne 按主题取（错误色上的对比色，
	// 深色主题下是近黑色），不做任何自定义；只在整体上多留 2px 顶部空隙。
	clearBtn := widget.NewButton("清空通话记录", st.clearAll)
	clearBtn.Importance = widget.DangerImportance
	clearBtn.SetIcon(theme.DeleteIcon())
	// 顶部多留 2px（下移 4px 后被 Center 拉回一半）；x 方向左移，让按钮右边缘
	// 与下方筛选输入框的边框对齐（实机截图上按钮比输入框更靠右，按用户要求
	// 左移；测试驱动的 1:1 渲染量不出这个偏差，所以这里是按实机反馈定的值）
	clearSlot := container.New(offsetLayout{dx: -3, dy: 4}, container.NewCenter(clearBtn))
	topBar := pageTopBar("通话记录", a.showDialPage, clearSlot)

	// ---- 筛选（按号码子串）----
	st.filterEntry = widget.NewEntry()
	st.filterEntry.SetPlaceHolder("按号码筛选")
	st.filterEntry.OnChanged = func(v string) { st.scheduleFilter(v) }
	st.filterEntry.OnSubmitted = func(string) { st.reload() }

	// ---- 列表（滚动加载更多）----
	st.view = newHistoryListView(st)
	st.scroll = container.NewVScroll(st.view)
	st.scroll.OnScrolled = func(fyne.Position) { st.maybeLoadMore() }

	st.emptyLabel = widget.NewLabel("暂无通话记录")
	st.emptyLabel.Alignment = fyne.TextAlignCenter
	st.emptyLabel.Hide() // 首屏加载完成前不显示空态，避免"暂无记录"闪一下
	// 列表宽度保持不变（滚动条是列表自己的一部分，不缩列表）；行内内容右侧
	// 留白由每一行自己让出（见 newHistoryRow 的 scrollPad）
	body := container.NewStack(st.scroll, container.NewCenter(st.emptyLabel))

	a.historyPage = container.NewBorder(
		container.NewVBox(topBar, st.filterEntry), nil, nil, nil, body)
}

// showHistory 切到通话记录页（主窗体顶栏按钮，仅非通话状态可用）。
func (a *App) showHistory() {
	if a.activeCallID != "" {
		a.showInfo("提示", "通话中无法查看通话记录")
		return
	}
	a.mainContent.Objects[0] = a.historyPage
	a.mainContent.Refresh()
	a.history.reload()
}

// historyOnCallEnded 通话结束后若正停在记录页，刷新列表。
// 记录在 core 侧是"先入库再发事件"，这里查得到最新一条。
func (a *App) historyOnCallEnded() {
	if a.history == nil || a.historyPage == nil || len(a.mainContent.Objects) == 0 {
		return
	}
	if a.mainContent.Objects[0] != a.historyPage {
		return
	}
	a.history.reload()
}

// ---------------------------------------------------------------------------
// 加载
// ---------------------------------------------------------------------------

// scheduleFilter 筛选输入防抖：停止输入后统一重载一次。
func (st *historyState) scheduleFilter(v string) {
	st.filter = v
	if st.filterTimer != nil {
		st.filterTimer.Stop()
	}
	st.filterTimer = time.AfterFunc(historyFilterDelay, func() {
		fyne.Do(func() { st.reload() })
	})
}

// reload 清空并重新加载第一批（进入页面、筛选变化、删除之后）。
func (st *historyState) reload() {
	if st.filterTimer != nil {
		st.filterTimer.Stop()
		st.filterTimer = nil
	}
	st.gen++ // 作废在途查询
	st.loading = false
	st.rows = nil
	st.hasMore = true
	st.loadErr = nil
	st.sel = map[string]bool{}
	st.dragging = false
	st.view.setRows(nil)
	st.refreshEmpty()
	st.loadMore()
}

// maybeLoadMore 滚动位置接近底部时加载下一批。
func (st *historyState) maybeLoadMore() {
	if !st.hasMore || st.loading {
		return
	}
	bottom := st.scroll.Offset.Y + st.scroll.Size().Height
	if bottom >= st.view.MinSize().Height-historyLoadAhead {
		st.loadMore()
	}
}

// loadMore 取下一批（后台查库，结果回主线程追加）。
func (st *historyState) loadMore() {
	if st.loading || !st.hasMore || st.a.core == nil {
		return
	}
	st.loading = true
	gen, filter, offset := st.gen, st.filter, len(st.rows)
	go func() {
		page, err := st.a.core.QueryCallLog(core.CallLogFilter{
			Remote: filter,
			Limit:  historyPageSize,
			Offset: offset,
		})
		fyne.Do(func() {
			if gen != st.gen {
				return // 期间已重载（筛选/删除），本次结果作废
			}
			st.loading = false
			st.loadErr = err
			if err == nil {
				st.rows = append(st.rows, page.Records...)
				st.hasMore = page.HasMore
				st.view.appendRows(page.Records)
			} else {
				st.hasMore = false
				logx.Logger().Warn("查询通话记录失败", "err", err)
			}
			st.refreshEmpty()
			// 一屏没填满就直接接着加载（不然滚不动、也就不会再触发加载）
			st.maybeLoadMore()
		})
	}()
}

// refreshEmpty 空态与不可用提示。
func (st *historyState) refreshEmpty() {
	switch {
	case st.loadErr != nil && len(st.rows) == 0:
		// 只有一条都没加载出来时才提示不可用：加载更多途中失败不该盖住已有记录
		st.emptyLabel.SetText("通话记录不可用：" + st.loadErr.Error())
		st.emptyLabel.Show()
	case len(st.rows) == 0 && !st.hasMore && st.filter != "":
		st.emptyLabel.SetText("没有匹配的通话记录")
		st.emptyLabel.Show()
	case len(st.rows) == 0 && !st.hasMore:
		st.emptyLabel.SetText("暂无通话记录")
		st.emptyLabel.Show()
	default:
		st.emptyLabel.Hide()
	}
}

// ---------------------------------------------------------------------------
// 选择（单击 / Shift 单击 / 拖动）
// ---------------------------------------------------------------------------

// indexAt 列表内的 y 坐标 → 行号（行高固定）。
func (st *historyState) indexAt(y float32) int {
	if y < 0 {
		return -1
	}
	return int(y / historyRowHeight)
}

// clickSelect 单击：只选它；Shift 单击：加入/移出；点空白处：清空。
func (st *historyState) clickSelect(idx int, shift bool) {
	if idx < 0 || idx >= len(st.rows) {
		if len(st.sel) > 0 {
			st.sel = map[string]bool{}
			st.refreshSelection()
		}
		return
	}
	id := st.rows[idx].CallID
	if shift {
		if st.sel[id] {
			delete(st.sel, id)
		} else {
			st.sel[id] = true
		}
	} else {
		soleSelected := len(st.sel) == 1 && st.sel[id]
		st.sel = map[string]bool{}
		if !soleSelected { // 再点一次已选中的行 = 取消选择
			st.sel[id] = true
		}
	}
	st.refreshSelection()
}

// dragSelect 拖动选择：按下行到当前行的连续区间。
func (st *historyState) dragSelect(from, to int) {
	n := len(st.rows)
	if n == 0 {
		return
	}
	from = clampInt(from, 0, n-1)
	to = clampInt(to, 0, n-1)
	if from > to {
		from, to = to, from
	}
	sel := make(map[string]bool, to-from+1)
	for i := from; i <= to; i++ {
		sel[st.rows[i].CallID] = true
	}
	st.sel = sel
	st.refreshSelection()
}

// refreshSelection 按选中集合刷新各行底色。
func (st *historyState) refreshSelection() {
	for _, obj := range st.view.rows() {
		if row, ok := obj.(*historyRow); ok {
			row.setSelected(st.sel[row.rec.CallID])
		}
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------------------
// 删除与回拨
// ---------------------------------------------------------------------------

// showRowMenu 右键菜单（删除入口，页面上不再有删除按钮）。
//
// 右键**不改变选中状态**（用户要求）：右键的那一行若已在选中集合里，
// 菜单针对整批选中项；否则只针对这一行。
func (st *historyState) showRowMenu(idx int, pos fyne.Position) {
	ids := st.menuTargets(idx)
	if len(ids) == 0 {
		return
	}
	label := "删除这条记录"
	if len(ids) > 1 {
		label = fmt.Sprintf("删除选中的 %d 条记录", len(ids))
	}
	menu := fyne.NewMenu("", fyne.NewMenuItem(label, func() { st.confirmDelete(ids) }))
	widget.NewPopUpMenu(menu, st.a.mainW.Canvas()).ShowAtPosition(pos)
}

// menuTargets 右键要删的记录：右键的那一行若在选中集合里就针对整批选中项，
// 否则只针对它自己（**右键不改选中状态**）。
func (st *historyState) menuTargets(idx int) []string {
	if idx < 0 || idx >= len(st.rows) {
		return nil
	}
	rec := st.rows[idx]
	if st.sel[rec.CallID] {
		if batch := st.selectedIDs(); len(batch) > 1 {
			return batch
		}
	}
	return []string{rec.CallID}
}

// selectedIDs 当前选中的 call_id（按列表顺序，结果稳定）。
func (st *historyState) selectedIDs() []string {
	ids := make([]string, 0, len(st.sel))
	for _, r := range st.rows {
		if st.sel[r.CallID] {
			ids = append(ids, r.CallID)
		}
	}
	return ids
}

// confirmDelete 删除前二次确认。
func (st *historyState) confirmDelete(ids []string) {
	if len(ids) == 0 {
		return
	}
	st.a.showConfirmIn(st.a.mainW, "删除通话记录",
		fmt.Sprintf("删除 %d 条通话记录？此操作不可恢复。", len(ids)),
		func(ok bool) {
			if ok {
				st.deleteIDs(ids)
			}
		})
}

// deleteIDs 后台删除并重载列表。
func (st *historyState) deleteIDs(ids []string) {
	go func() {
		_, err := st.a.core.DeleteCallLogRecords(ids)
		fyne.Do(func() {
			if err != nil {
				st.a.showErrorIn(st.a.mainW, err)
				return
			}
			st.reload()
		})
	}()
}

// clearAll 一键清空（二次确认）。
func (st *historyState) clearAll() {
	st.a.showConfirmIn(st.a.mainW, "清空通话记录",
		"确定清空全部通话记录？此操作不可恢复。", func(ok bool) {
			if !ok {
				return
			}
			go func() {
				_, err := st.a.core.ClearCallLog()
				fyne.Do(func() {
					if err != nil {
						st.a.showErrorIn(st.a.mainW, err)
						return
					}
					st.reload()
				})
			}()
		})
}

// onHistoryCall 从记录回拨：复用主窗体的拨号入口（错误提示、通话面板、
// 最近号码都一致），只在发起前挡掉"已有通话进行中"——否则 onCallButton
// 的语义是**挂断当前通话**。
func (a *App) onHistoryCall(rec core.CallRecord, video bool) {
	if a.uiState != uiIdle || a.activeCallID != "" {
		a.showInfoIn(a.mainW, "提示", "已有通话进行中，无法发起新的呼叫")
		return
	}
	if rec.Remote == "" {
		return
	}
	a.dialSelect.SetText(rec.Remote)
	if video {
		a.onVideoCallButton()
	} else {
		a.onCallButton()
	}
}

// ---------------------------------------------------------------------------
// 列表容器
// ---------------------------------------------------------------------------

// historyListView 记录列表：固定行高纵向排列（行距由行自己留），并统一承接
// 单击/Shift 单击/拖动三种选择手势（见文件头注释）。
type historyListView struct {
	widget.BaseWidget
	st  *historyState
	box *fyne.Container
}

func newHistoryListView(st *historyState) *historyListView {
	v := &historyListView{st: st}
	v.box = container.New(historyRowsLayout{})
	v.ExtendBaseWidget(v)
	return v
}

func (v *historyListView) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(v.box)
}

// rows 当前的行控件。
func (v *historyListView) rows() []fyne.CanvasObject { return v.box.Objects }

// setRows 重建全部行（reload 用；recs 为 nil 即清空）。
func (v *historyListView) setRows(recs []core.CallRecord) {
	v.box.Objects = v.box.Objects[:0]
	v.appendRows(recs)
}

// appendRows 追加一批行。
func (v *historyListView) appendRows(recs []core.CallRecord) {
	for _, rec := range recs {
		v.box.Add(newHistoryRow(v.st, rec))
	}
	v.box.Refresh()
	v.Refresh()
}

// MouseDown 记下按下位置与 Shift 状态（拖动也用它当区间起点）。
func (v *historyListView) MouseDown(ev *desktop.MouseEvent) {
	v.st.pressIdx = v.st.indexAt(ev.Position.Y)
	v.st.pressShift = ev.Modifier&fyne.KeyModifierShift != 0
}

// MouseUp 未发生拖动时按单击处理（拖动结束也会走到这里，用 dragging 挡掉）；
// 右键弹删除菜单，**不改选中状态**。
func (v *historyListView) MouseUp(ev *desktop.MouseEvent) {
	if v.st.dragging {
		return
	}
	if ev.Button == desktop.MouseButtonSecondary {
		v.st.showRowMenu(v.st.indexAt(ev.Position.Y), ev.AbsolutePosition)
		return
	}
	v.st.clickSelect(v.st.pressIdx, v.st.pressShift)
}

// Dragged 拖动选择连续区间。
func (v *historyListView) Dragged(ev *fyne.DragEvent) {
	v.st.dragging = true
	v.st.dragSelect(v.st.pressIdx, v.st.indexAt(ev.Position.Y))
}

// DragEnd 拖动结束。
func (v *historyListView) DragEnd() { v.st.dragging = false }

// historyRowsLayout 固定行高的纵向排列（行距 0：行距与分割线由行内部留）。
type historyRowsLayout struct{}

func (historyRowsLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, float32(len(objs))*historyRowHeight)
}

func (historyRowsLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		o.Resize(fyne.NewSize(size.Width, historyRowHeight))
		o.Move(fyne.NewPos(0, y))
		y += historyRowHeight
	}
}

// ---------------------------------------------------------------------------
// 行控件
// ---------------------------------------------------------------------------

// historyRow 一条记录（两行 + 右侧两个图形按钮）。行控件**不复用**：
// 每行对应一条固定记录，所以按钮回调直接捕获 rec 是安全的。
type historyRow struct {
	widget.BaseWidget

	st  *historyState
	rec core.CallRecord

	bg       *canvas.Rectangle
	selected bool
	root     *fyne.Container
}

// historyRowLayout 行内布局：底色铺满，内容占上方（留出 2px 行距 + 1px 分割线），
// 分割线贴底。
//
// 不用 container.NewBorder 拼——borderLayout 会在中间与底部之间**额外**加
// theme.Padding()，行距就不是指定的 2px 了（同一个坑见 CLAUDE.md 的 vPad 条）。
type historyRowLayout struct{}

func (historyRowLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, historyRowHeight)
}

func (historyRowLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	contentH := size.Height - historyRowGap - historyRowSep
	bg, content, sep := objs[0], objs[1], objs[2]
	bg.Resize(size)
	bg.Move(fyne.NewPos(0, 0))
	content.Resize(fyne.NewSize(size.Width, contentH))
	content.Move(fyne.NewPos(0, 0))
	sep.Resize(fyne.NewSize(size.Width, historyRowSep))
	sep.Move(fyne.NewPos(0, contentH+historyRowGap))
}

func newHistoryRow(st *historyState, rec core.CallRecord) *historyRow {
	a := st.a
	r := &historyRow{st: st, rec: rec}

	// ---- 第一行：方向图标 + 号码/显示名（+ 视频通话标记）…… 开始时间 ----
	who := newPlainText(historyWhoText(rec), historyFontMain, theme.ForegroundColor(), false)
	when := newPlainText(historyTimeText(rec.StartedAt, time.Now()), historyFontSmall, theme.ForegroundColor(), false)
	// 视频通话标记：紧跟号码右边，颜色同文字（主题前景色）
	var mark fyne.CanvasObject = canvas.NewImageFromResource(nil)
	if rec.HasVideo {
		img := canvas.NewImageFromResource(theme.NewColoredResource(IconVideoMark, theme.ColorNameForeground))
		img.FillMode = canvas.ImageFillContain
		img.SetMinSize(fyne.NewSize(historyVideoMarkSize, historyVideoMarkSize))
		mark = img
	} else {
		mark.Hide()
	}
	whoWithMark := container.New(markAfterLayout{gap: 4}, who, mark)
	line1 := container.NewBorder(nil, nil, nil, when, whoWithMark)

	// ---- 第二行：状态 · 时长 …… 呼叫 / 视频呼叫 ----
	meta := newPlainText(historyMetaText(rec), historyFontSmall, theme.ForegroundColor(), false)
	callBtn := newCompactButton(IconPhoneWhite, colorHoldBlue, historyRowIconSize,
		func() { a.onHistoryCall(rec, false) })
	callBtn.SetCornerRadius(historyRowButtonRadius)
	actions := container.NewHBox(callBtn)
	if a.core != nil && a.core.VideoSupported() {
		videoBtn := newCompactButton(IconVideoOn, colorHoldBlue, historyRowIconSize,
			func() { a.onHistoryCall(rec, true) })
		videoBtn.SetCornerRadius(historyRowButtonRadius)
		videoBtn.SetIconSize(historyVideoIconSize) // 摄像头图形单独放大一点
		actions.Add(videoBtn)
	}
	line2 := container.NewBorder(nil, nil, meta, nil)

	lines := container.New(panelRowsLayout{}, line1, line2) // 两行纵向居中（行距 2px）
	// 左：方向图标（纵向跨两行，两行文字左边缘因此对齐，图标也不会被行高裁掉）；
	// 右：呼叫/视频呼叫按钮（纵向居中跨两行）。时间在第一行内右对齐，
	// 正好顶到按钮左边缘（用户要求）。
	// 最右再让出 historyScrollGap：滚动条是列表自己画的、压在列表右缘上，
	// 不留这一步滚动条会挡住按钮（列表宽度本身不动）
	content := container.NewBorder(nil, nil,
		historyDirectionIcon(rec),
		container.New(padRightLayout{pad: historyScrollGap}, container.NewCenter(actions)),
		lines)

	r.bg = canvas.NewRectangle(color.Transparent)
	sep := canvas.NewRectangle(historySeparatorColor())
	sep.SetMinSize(fyne.NewSize(1, historyRowSep))
	r.root = container.New(historyRowLayout{}, r.bg, content, sep)

	r.ExtendBaseWidget(r)
	return r
}

func (r *historyRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(r.root)
}

// setSelected 选中态：浅色底（颜色随主题，深色主题下也可见）。
func (r *historyRow) setSelected(on bool) {
	if r.selected == on {
		return
	}
	r.selected = on
	if on {
		r.bg.FillColor = theme.SelectionColor()
	} else {
		r.bg.FillColor = color.Transparent
	}
	r.bg.Refresh()
}

// ---------------------------------------------------------------------------
// 展示文案与配色（纯函数，便于单测）
// ---------------------------------------------------------------------------

// historyDirectionIcon 方向图标：**话机为主体**，右上角贴粗方向箭头
// （↗ 呼出 / ↙ 呼入）；颜色表示结果（绿=已接通、红=未接通）。
// 视频通话不在这里体现——第一行号码右边另有一个摄像头标记。
//
// 两个图元是各自独立出图、在这里用 canvas.Image 叠放的：Fyne 的 SVG 渲染器
// 不认 <g transform>，把它们拼进一个 SVG 文件会整张空白（实测）。
func historyDirectionIcon(r core.CallRecord) fyne.CanvasObject {
	phone, arrow := historyDirectionResources(r)

	phoneImg := canvas.NewImageFromResource(phone)
	phoneImg.FillMode = canvas.ImageFillContain
	arrowImg := canvas.NewImageFromResource(arrow)
	arrowImg.FillMode = canvas.ImageFillContain

	return container.NewCenter(container.New(
		historyDirIconLayout{incoming: r.Direction == "incoming"}, phoneImg, arrowImg))
}

// historyDirectionResources 方向图标用到的两个图元：话机（主体）与方向箭头，
// 颜色都表示结果（绿=已接通、红=未接通）。
func historyDirectionResources(r core.CallRecord) (phone, arrow fyne.Resource) {
	ok := !r.AnsweredAt.IsZero()
	incoming := r.Direction == "incoming"

	phone, arrow = IconPhoneRed, IconArrowOutRed
	switch {
	case ok:
		phone, arrow = IconPhoneGreen, IconArrowOutGreen
	}
	if incoming {
		arrow = IconArrowInRed
		if ok {
			arrow = IconArrowInGreen
		}
	}
	return phone, arrow
}

// historyDirIconLayout 方向图标内部布局：话机占主体、靠左下，粗箭头压在它的
// 右上角（两者在斜边上接合，像一个整体，重心不堆在右上）。
//
// 箭头位置按方向微调：来电箭头是呼出箭头的 180° 旋转，视觉重心偏左下，
// 所以比呼出再往右挪一点（见下面几个常量）。
type historyDirIconLayout struct{ incoming bool }

func (historyDirIconLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(historyDirIconSize, historyDirIconSize)
}

func (l historyDirIconLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	phone, arrow := objs[0], objs[1]

	ps := fyne.NewSize(size.Width*0.76, size.Height*0.76)
	phone.Resize(ps)
	phone.Move(fyne.NewPos(0, size.Height-ps.Height))

	ax := size.Width*0.44 - historyDirArrowDX
	ay := historyDirArrowDY - historyDirArrowUp
	if l.incoming {
		ax += historyDirArrowInDX
	}
	as := fyne.NewSize(size.Width*0.46, size.Height*0.46)
	arrow.Resize(as)
	arrow.Move(fyne.NewPos(ax, ay))
}

// historySeparatorColor 行分割线：深色主题用深灰、浅色主题用浅灰（用户指定）。
func historySeparatorColor() color.Color {
	if fyne.CurrentApp() != nil &&
		fyne.CurrentApp().Settings().ThemeVariant() == theme.VariantDark {
		return color.NRGBA{R: 0x44, G: 0x48, B: 0x4f, A: 0xff} // 深灰
	}
	return color.NRGBA{R: 0xd0, G: 0xd4, B: 0xda, A: 0xff} // 浅灰
}

// historyMetaText 第二行：状态 + 振铃/拨号时长 +（已接通时）通话时长。
func historyMetaText(r core.CallRecord) string {
	parts := []string{historyStatusText(r)}
	if d := historyDurationText(r); d != "" {
		parts = append(parts, d)
	}
	return strings.Join(parts, " · ")
}

// historyWhoText 对端展示名：显示名（号码），只有其一（或相同）时用其一。
func historyWhoText(r core.CallRecord) string {
	who := r.Remote
	if r.RemoteName != "" && r.RemoteName != r.Remote {
		if who == "" {
			who = r.RemoteName
		} else {
			who = r.RemoteName + "（" + r.Remote + "）"
		}
	}
	// 号码/显示名来自 SIP 报文：去掉控制字符并截断
	return sanitizeNotifyText(who, historyWhoMax)
}

// historyStatusText 结果文案（用户指定：来电 = 未接听 / 拒接 / 已接通；
// 呼出 = 未接通 / 取消 / 已接通）。
//
// 注意 missed 的口径是"用户没处理"（本端拒接、振铃中本端挂断都不算未接，
// 见 core.missedOnEnd），所以**不能只看 missed 判"已接听"**，要按原因细分。
func historyStatusText(r core.CallRecord) string {
	if !r.AnsweredAt.IsZero() {
		if r.EndedAt.IsZero() {
			return "通话中" // 进行中（记录已入库、结束上报还没到）
		}
		return "已接通"
	}
	if r.EndedAt.IsZero() {
		if r.Direction == "incoming" {
			return "振铃中"
		}
		return "呼叫中"
	}
	if r.Reason == core.ReasonInterrupted {
		return "已中断"
	}
	if r.Direction == "incoming" {
		if r.Reason == "rejected" {
			return "拒接"
		}
		return "未接听"
	}
	if r.Reason == "local_hangup" {
		return "取消"
	}
	return "未接通"
}

// historyDurationText 时长文案：先振铃时长（来电）或拨号时长（呼出）——
// 即开始到接通（未接通就是到结束），已接通时再跟通话时长（接通到结束）。
func historyDurationText(r core.CallRecord) string {
	if r.StartedAt.IsZero() {
		return ""
	}
	ringEnd := historyEnd(r)
	if !r.AnsweredAt.IsZero() {
		ringEnd = r.AnsweredAt
	}
	parts := make([]string, 0, 2)
	if r.Direction == "incoming" {
		parts = append(parts, "振铃 "+formatDurationCN(ringEnd.Sub(r.StartedAt)))
	} else {
		parts = append(parts, "拨号 "+formatDurationCN(ringEnd.Sub(r.StartedAt)))
	}
	if !r.AnsweredAt.IsZero() {
		parts = append(parts, "通话 "+formatDurationCN(historyEnd(r).Sub(r.AnsweredAt)))
	}
	return strings.Join(parts, " · ")
}

// historyEnd 结束时刻；进行中的记录算到此刻。
func historyEnd(r core.CallRecord) time.Time {
	if r.EndedAt.IsZero() {
		return time.Now()
	}
	return r.EndedAt
}

// historyTimeText 时间文案：今年省略年份，跨年才带上。
func historyTimeText(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	if t.Year() == now.Year() {
		return t.Format("01-02 15:04")
	}
	return t.Format("2006-01-02 15:04")
}
