package ui

// 通信录页（REQ-UI-032 ~ REQ-UI-037）。
//
// 与通话记录页同款：**切在主窗体内**（不新开窗口），竖屏手机式紧凑列表，
// 一条联系人两行：
//
//	号码 ★                                    [呼叫] [视频呼叫]
//	名称  备注（太长截断补省略号，不会挤乱布局）
//
// 两个 tab（常用联系人 / 所有联系人）用**自绘分段控件**，不用
// container.NewAppTabs——它的 tab 按钮高 = 文字高 + 2×SizeNameInnerPadding(12)
// ≈ 43px，本项目还额外带内边距，620px 高的主窗体里太占地方，也不符合一贯的
// 紧凑风格（同 panelRowLayout / fixedRowLayout 的取舍）。
//
// 「所有联系人」tab 顶部有搜索框：**音头 / 全拼 / 汉字任意组合**匹配名称
// （见 contact_search.go），并模糊匹配号码与备注。
//
// 数据全部来自 core 的内存快照（core.Contacts）：刷新是**同步**的，不需要查库、
// 不需要后台 goroutine（写入才会碰数据库，见 core.SetContacts 的说明）。

import (
	"fmt"
	"image/color"
	"os"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/contactsearch"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

const (
	// contactsPageSize 每批渲染的行数（滚到接近底部再渲染下一批）。
	// 全部联系人都在内存里，这里只是控制一次建多少个行控件。
	contactsPageSize = 50
	// contactsRowHeight 行高（与通话记录行一致）。
	contactsRowHeight = 46
	// contactsRowGap/contactsRowSep 行距与分割线（同通话记录页）。
	contactsRowGap = 2
	contactsRowSep = 1
	// contactsRowIconSize 行内按钮尺寸；contactsRowButtonRadius 圆角。
	contactsRowIconSize     = 24
	contactsRowButtonRadius = 2
	// contactsFontMain/contactsFontSmall 行内字号（与通话记录页一致）。
	contactsFontMain  = 14
	contactsFontSmall = 12
	// contactsStarSize 常用标记 ★ 的尺寸。
	contactsStarSize = 14
	// contactsNameMaxRatio 第二行名称最多占整行宽的比例（剩下给备注）。
	contactsNameMaxRatio = 0.62
	// contactsSearchDelay 搜索输入防抖。
	contactsSearchDelay = 300 * time.Millisecond
	// contactsLoadAhead 距底部还有这么多像素就渲染下一批。
	contactsLoadAhead = 4 * contactsRowHeight
	// contactsTextLeftPad 行内文字距列表左缘的留白。
	contactsTextLeftPad = 2
	// contactsWhoMax 号码/名称展示的最大长度（rune），真正的截断由 plainText
	// 按可用宽度做（这里只是先挡一道超长文本）。
	contactsWhoMax = 60
	// contactsCSVMaxSize 导入的 CSV 文件大小上限（防止手滑选了个大文件）。
	contactsCSVMaxSize = 8 << 20
)

// contactsState 通信录页的状态与控件。
type contactsState struct {
	a *App

	all  []contactEntry // 全量（按拼音、再按号码排序）
	rows []contactEntry // 当前 tab + 搜索过滤后的行
	// shown 已渲染的行数（滚动分页：一次只建这么多行控件）。
	shown int

	query    string // 搜索串（只在「所有联系人」tab 生效）
	starOnly bool   // 当前 tab 是否为「常用联系人」

	// sel 选中的号码键集合。按**规范化号码**而不是行号：重新加载、切换 tab、
	// 筛选、删除都会让行号整体重排。
	sel map[string]bool
	// pressIdx/pressShift 按下时的行号与 Shift 状态（MouseDown 记，MouseUp 用）。
	pressIdx   int
	pressShift bool
	dragging   bool

	view        *contactsListView
	scroll      *container.Scroll
	empty       *widget.Label
	search      *widget.Entry
	searchTimer *time.Timer
	header      *fyne.Container // 顶栏 + tab 条 + 搜索框（切 tab 时刷新它）

	tabStar *segmentedButton
	tabAll  *segmentedButton
}

// contactEntry 一行数据：联系人与它的拼音索引（索引只建一次，搜索与排序共用）。
type contactEntry struct {
	c   core.Contact
	idx contactsearch.Index
}

// contactKey 行的唯一键（规范化号码）。
func (e contactEntry) key() string { return core.ContactKey(e.c.Number) }

// ---------------------------------------------------------------------------
// 页面
// ---------------------------------------------------------------------------

// buildContactsPage 构建通信录页。
func (a *App) buildContactsPage() {
	st := &contactsState{a: a, sel: map[string]bool{}}
	a.contacts = st

	// ---- 顶栏：返回 + 标题 + 增加/导入/导出/清空（REQ-UI-032，按钮都塞顶栏）----
	// 清空用 DangerImportance（主题错误红），与通话记录页的「清空通话记录」一致。
	clearBtn := topBarIconButton(theme.DeleteIcon(), st.clearAll)
	clearBtn.Importance = widget.DangerImportance // 清空 = 危险动作（主题错误红）
	topBar := pageTopBar("通信录", a.showDialPage, container.NewHBox(
		topBarIconButton(theme.ContentAddIcon(), st.showAddDialog),
		topBarIconButton(theme.DownloadIcon(), st.importCSV),
		topBarIconButton(theme.UploadIcon(), st.exportCSV),
		clearBtn,
	))

	// ---- 两个 tab ----
	st.tabStar = newSegmentedButton("常用联系人", func() { st.setTab(true) })
	st.tabAll = newSegmentedButton("所有联系人", func() { st.setTab(false) })
	tabBar := container.New(segmentedLayout{gap: 0}, st.tabStar, st.tabAll)

	// ---- 搜索框（只在「所有联系人」tab 显示）----
	st.search = widget.NewEntry()
	st.search.SetPlaceHolder("搜索名称（拼音/音头/汉字）、号码或备注")
	st.search.OnChanged = func(v string) { st.scheduleSearch(v) }
	st.search.OnSubmitted = func(string) { st.reload() }
	st.search.Hide()

	// ---- 列表（滚动加载更多）----
	st.view = newContactsListView(st)
	st.scroll = container.NewVScroll(st.view)
	st.scroll.OnScrolled = func(fyne.Position) { st.maybeLoadMore() }

	st.empty = widget.NewLabel("暂无联系人")
	st.empty.Alignment = fyne.TextAlignCenter
	st.empty.Hide() // 首屏加载完成前不显示空态，避免"暂无联系人"闪一下
	body := container.NewStack(st.scroll, container.NewCenter(st.empty))

	// 头部（顶栏 + tab + 搜索框）：切 tab 时要刷新它，搜索框的显隐才会重新排版
	st.header = container.NewVBox(topBar, tabBar, st.search)
	a.contactsPage = container.NewBorder(st.header, nil, nil, nil, body)
	st.setTab(true) // 默认停在「常用联系人」
}

// topBarIconButton 内页顶栏的纯图标按钮（低强调，与主窗体顶栏的设置/通话记录
// 按钮同款）。**用标准 Fyne 按钮**：自绘紧凑版虽然更省地方，但和主窗体其它按钮
// 的观感不统一（用户反馈），这里保持与标准按钮一致。
func topBarIconButton(icon fyne.Resource, fn func()) *widget.Button {
	b := widget.NewButtonWithIcon("", icon, fn)
	b.Importance = widget.LowImportance
	return b
}

// showContacts 切到通信录页（主窗体顶栏按钮）。**通话中也可以打开**：只浏览，
// 行内操作按钮按当前话务/拨号复用状态显示（见 listRowActions，REQ-ACC-035）。
func (a *App) showContacts() {
	a.hideTip()         // 切页时收起备注提示
	a.hideDialSuggest() // 拨号建议下拉同理
	a.mainContent.Objects[0] = a.contactsPage
	a.mainContent.Refresh()
	a.contacts.reload()
}

// setTab 切换 tab：常用联系人（无搜索框）/ 所有联系人（有搜索框）。
func (st *contactsState) setTab(starOnly bool) {
	st.starOnly = starOnly
	st.tabStar.SetSelected(starOnly)
	st.tabAll.SetSelected(!starOnly)
	if starOnly {
		// 「常用联系人」不提供搜索：切过去时清掉搜索词并收起搜索框，
		// 免得用户在常用页看到"被搜索过滤过的常用联系人"这种糊涂状态
		st.query = ""
		if st.search != nil {
			st.search.SetText("")
			st.search.Hide()
		}
	} else if st.search != nil {
		st.search.Show()
	}
	// 刷新头部：搜索框的显隐要让 VBox 重新排版（只 Show/Hide 子对象时
	// 父容器不一定重算布局）
	if st.header != nil {
		st.header.Refresh()
	}
	if st.a.contactsPage != nil {
		st.reload()
	}
}

// contactsOnCallEnded 通话结束后若正停在通信录页，刷新（数字/名称可能在
// 通话期间被改过——虽然通信录页在通话中进不来，保险起见）。
func (a *App) contactsOnCallEnded() {
	if a.contacts == nil || a.contactsPage == nil || len(a.mainContent.Objects) == 0 {
		return
	}
	if a.mainContent.Objects[0] != a.contactsPage {
		return
	}
	a.contacts.reload()
}

// ---------------------------------------------------------------------------
// 加载、过滤与排序
// ---------------------------------------------------------------------------

// reload 重建全量数据并应用当前的 tab/搜索过滤。
func (st *contactsState) reload() {
	if st.a.core == nil {
		return
	}
	list := st.a.core.Contacts()
	all := make([]contactEntry, 0, len(list))
	for _, c := range list {
		all = append(all, contactEntry{c: c, idx: contactsearch.NewIndex(c.Name)})
	}
	// 排序：名称按拼音（空名称排最前，用户刚建好还没起名的条目一眼能看到），
	// 同音/同名再按规范化号码，结果稳定
	sort.SliceStable(all, func(i, j int) bool {
		ki, kj := all[i].idx.SortKey(), all[j].idx.SortKey()
		if ki != kj {
			return ki < kj
		}
		return all[i].key() < all[j].key()
	})
	st.all = all
	st.applyFilter()
	// 通信录变了，通话面板上的名称/★ 也要跟着变（通话中进不来通信录页，
	// 但网页可以导入 CSV，这条路径会走到）
	if st.a.activeCall != nil {
		st.a.setCallName(st.a.activeCall)
	}
}

// applyFilter 按 tab 与搜索词过滤，并重新渲染第一批。
func (st *contactsState) applyFilter() {
	rows := make([]contactEntry, 0, len(st.all))
	for _, e := range st.all {
		if st.starOnly && !e.c.Starred {
			continue
		}
		if !st.match(e) {
			continue
		}
		rows = append(rows, e)
	}
	st.rows = rows
	st.shown = 0
	st.sel = map[string]bool{}
	st.dragging = false
	st.view.setRows(nil)
	st.refreshEmpty()
	st.renderMore()
	if st.scroll != nil {
		st.scroll.ScrollToTop()
	}
}

// match 行是否命中搜索词：规则在 internal/contactsearch（**与网页控制台共用一份**，
// 含音头/全拼/汉字/号码/备注/纯数字，改那边两边同时生效）。
func (st *contactsState) match(e contactEntry) bool {
	return contactsearch.Match(e.idx, e.c.Number, e.c.Note, st.query)
}

// scheduleSearch 搜索输入防抖：停止输入后统一重过滤一次。
func (st *contactsState) scheduleSearch(v string) {
	st.query = v
	if st.searchTimer != nil {
		st.searchTimer.Stop()
	}
	st.searchTimer = time.AfterFunc(contactsSearchDelay, func() {
		fyne.Do(func() { st.applyFilter() })
	})
}

// renderMore 再渲染一批行（滚动分页）。
func (st *contactsState) renderMore() {
	if st.shown >= len(st.rows) {
		return
	}
	end := st.shown + contactsPageSize
	if end > len(st.rows) {
		end = len(st.rows)
	}
	batch := st.rows[st.shown:end]
	st.shown = end
	st.view.appendRows(batch)
	st.refreshEmpty()
}

// maybeLoadMore 滚动位置接近底部时渲染下一批。
func (st *contactsState) maybeLoadMore() {
	if st.shown >= len(st.rows) {
		return
	}
	bottom := st.scroll.Offset.Y + st.scroll.Size().Height
	if bottom >= st.view.MinSize().Height-contactsLoadAhead {
		st.renderMore()
	}
}

// refreshEmpty 空态提示（区分"没有联系人"与"没有匹配的联系人"）。
func (st *contactsState) refreshEmpty() {
	if st.empty == nil {
		return
	}
	if len(st.rows) > 0 {
		st.empty.Hide()
		return
	}
	switch {
	case st.starOnly:
		st.empty.SetText("暂无常用联系人\n（右键联系人可设为常用）")
	case strings.TrimSpace(st.query) != "":
		st.empty.SetText("没有匹配的联系人")
	default:
		st.empty.SetText("暂无联系人\n（点右上角 ＋ 新增，或用「导入」导入 CSV）")
	}
	st.empty.Show()
}

// ---------------------------------------------------------------------------
// 选择（单击 / Shift 单击 / 拖动）
// ---------------------------------------------------------------------------

// indexAt 列表内的 y 坐标 → 行号（行高固定）。
func (st *contactsState) indexAt(y float32) int {
	if y < 0 {
		return -1
	}
	return int(y / contactsRowHeight)
}

// clickSelect 单击：只选它；Shift 单击：加入/移出；点空白处：清空。
func (st *contactsState) clickSelect(idx int, shift bool) {
	if idx < 0 || idx >= len(st.rows) {
		if len(st.sel) > 0 {
			st.sel = map[string]bool{}
			st.refreshSelection()
		}
		return
	}
	key := st.rows[idx].key()
	if shift {
		if st.sel[key] {
			delete(st.sel, key)
		} else {
			st.sel[key] = true
		}
	} else {
		soleSelected := len(st.sel) == 1 && st.sel[key]
		st.sel = map[string]bool{}
		if !soleSelected { // 再点一次已选中的行 = 取消选择
			st.sel[key] = true
		}
	}
	st.refreshSelection()
}

// dragSelect 拖动选择：按下行到当前行的连续区间。
func (st *contactsState) dragSelect(from, to int) {
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
		sel[st.rows[i].key()] = true
	}
	st.sel = sel
	st.refreshSelection()
}

// refreshSelection 按选中集合刷新各行底色。
func (st *contactsState) refreshSelection() {
	for _, obj := range st.view.rows() {
		if row, ok := obj.(*contactsRow); ok {
			row.setSelected(st.sel[row.key])
		}
	}
}

// selectedKeys 当前选中的号码键（按列表顺序，结果稳定）。
func (st *contactsState) selectedKeys() []string {
	keys := make([]string, 0, len(st.sel))
	for _, e := range st.rows {
		if st.sel[e.key()] {
			keys = append(keys, e.key())
		}
	}
	return keys
}

// byKey 按号码键取条目（找不到返回零值与 false）。
func (st *contactsState) byKey(key string) (contactEntry, bool) {
	for _, e := range st.all {
		if e.key() == key {
			return e, true
		}
	}
	return contactEntry{}, false
}

// menuTargets 右键要操作的行：右键的那行若在选中集合里就针对整批选中项，
// 否则只针对它自己（**右键不改变选中状态**，与通话记录页一致）。
func (st *contactsState) menuTargets(idx int) []string {
	if idx < 0 || idx >= len(st.rows) {
		return nil
	}
	e := st.rows[idx]
	if st.sel[e.key()] {
		if batch := st.selectedKeys(); len(batch) > 1 {
			return batch
		}
	}
	return []string{e.key()}
}

// ---------------------------------------------------------------------------
// 右键菜单与增删改
// ---------------------------------------------------------------------------

// showRowMenu 右键菜单（REQ-UI-034）：单行 = 设为/取消常用、修改、删除；
// 多选 = 批量设常用/取消常用、批量删除。
func (st *contactsState) showRowMenu(idx int, pos fyne.Position) {
	targets := st.menuTargets(idx)
	if len(targets) == 0 {
		return
	}
	var items []*fyne.MenuItem
	if len(targets) == 1 {
		e, ok := st.byKey(targets[0])
		if !ok {
			return
		}
		if e.c.Starred {
			items = append(items, fyne.NewMenuItem("取消常用", func() { st.setStarred(targets, false) }))
		} else {
			items = append(items, fyne.NewMenuItem("设为常用", func() { st.setStarred(targets, true) }))
		}
		rec := e.c
		items = append(items, fyne.NewMenuItem("修改", func() { st.showEditDialog(&rec, "", nil) }))
		items = append(items, fyne.NewMenuItem("删除", func() { st.confirmDelete(targets) }))
	} else {
		// 选中项**全是常用**就不给「批量设为常用」，**全不是**就不给「批量取消常用」
		// （用户要求：已经没有意义的动作不出现在菜单里）。条数不显示在文案里。
		anyStarred, allStarred := st.starredStats(targets)
		if !allStarred {
			items = append(items, fyne.NewMenuItem("批量设为常用", func() { st.setStarred(targets, true) }))
		}
		if anyStarred {
			items = append(items, fyne.NewMenuItem("批量取消常用", func() { st.setStarred(targets, false) }))
		}
		items = append(items,
			fyne.NewMenuItem(fmt.Sprintf("删除选中的 %d 条", len(targets)), func() { st.confirmDelete(targets) }),
		)
	}
	widget.NewPopUpMenu(fyne.NewMenu("", items...), st.a.mainW.Canvas()).ShowAtPosition(pos)
}

// starredStats 选中项里"有常用的"与"全是常用的"。
func (st *contactsState) starredStats(keys []string) (anyStarred, allStarred bool) {
	if len(keys) == 0 {
		return false, false
	}
	allStarred = true
	for _, k := range keys {
		e, ok := st.byKey(k)
		if !ok {
			allStarred = false
			continue
		}
		if e.c.Starred {
			anyStarred = true
		} else {
			allStarred = false
		}
	}
	return anyStarred, allStarred
}

// ensureSelection 右键时调整选中：该行没被选中就**先单选它**（清掉其它选中），
// 已选中则保持原选中集合不变（用户要求；与资源管理器的右键语义一致）。
func (st *contactsState) ensureSelection(idx int) {
	if idx < 0 || idx >= len(st.rows) {
		return
	}
	key := st.rows[idx].key()
	if st.sel[key] {
		return
	}
	st.sel = map[string]bool{key: true}
	st.refreshSelection()
}

// setStarred 批量设置/取消常用标志。
func (st *contactsState) setStarred(keys []string, starred bool) {
	if len(keys) == 0 {
		return
	}
	// 键是规范化号码，直接当号码用（存储层会再规范化一次）
	go func() {
		_, err := st.a.core.SetContactsStarred(keys, starred)
		fyne.Do(func() {
			if err != nil {
				st.a.showErrorIn(st.a.mainW, err)
				return
			}
			st.reload()
		})
	}()
}

// confirmDelete 删除前二次确认。
func (st *contactsState) confirmDelete(keys []string) {
	if len(keys) == 0 {
		return
	}
	text := fmt.Sprintf("删除 %d 位联系人？此操作不可恢复。", len(keys))
	if len(keys) == 1 {
		text = "删除该联系人？此操作不可恢复。"
	}
	st.a.showConfirmIn(st.a.mainW, "删除联系人", text, func(ok bool) {
		if !ok {
			return
		}
		go func() {
			_, err := st.a.core.DeleteContacts(keys)
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

// clearAll 一键清空（二次确认）。
func (st *contactsState) clearAll() {
	st.a.showConfirmIn(st.a.mainW, "清空通信录", "确定清空全部联系人？此操作不可恢复。", func(ok bool) {
		if !ok {
			return
		}
		go func() {
			_, err := st.a.core.ClearContacts()
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

// showAddDialog 新增联系人。
func (st *contactsState) showAddDialog() { st.showEditDialog(nil, "", nil) }

// showContactDialogFor 从别处（通话记录右键）打开联系人对话框：该号码已在通信录
// 里就打开修改，否则带着号码打开新增。保存成功后除刷新通信录列表外还会调
// onSaved，调用方用它刷新自己的列表（通话记录行的名称会跟着变）。
func (st *contactsState) showContactDialogFor(number string, onSaved func()) {
	if c, ok := st.a.contactFor(number); ok {
		cc := c
		st.showEditDialog(&cc, "", onSaved)
		return
	}
	st.showEditDialog(nil, number, onSaved)
}

// showEditDialog 新增/修改联系人。prev 为 nil 表示新增。
//
// 校验失败（号码为空、号码重复）时**不关窗**：错误提示显示在对话框里，
// 用户改完直接再点确定（Fyne 的 dialog 默认按钮回调一定会关窗，所以用
// NewCustomWithoutButtons + SetButtons 自己接管）。
func (st *contactsState) showEditDialog(prev *core.Contact, prefillNumber string, onSaved func()) {
	a := st.a
	editing := prev != nil
	title := "新增联系人"
	if editing {
		title = "修改联系人"
	}
	numberEntry := newFormEntry()
	numberEntry.SetPlaceHolder("号码，如 2999 或 sip:2999@主机")
	nameEntry := newFormEntry()
	nameEntry.SetPlaceHolder("名称（必填）")
	noteEntry := newFormEntry()
	noteEntry.SetPlaceHolder("备注（可空）")
	starCheck := widget.NewCheck("常用联系人", nil)
	errLabel := widget.NewLabel("")
	errLabel.Wrapping = fyne.TextWrapWord
	errLabel.Importance = widget.DangerImportance
	errLabel.Hide()
	if editing {
		numberEntry.SetText(prev.Number)
		nameEntry.SetText(prev.Name)
		noteEntry.SetText(prev.Note)
		starCheck.SetChecked(prev.Starred)
	} else if prefillNumber != "" {
		numberEntry.SetText(prefillNumber) // 从通话记录右键带过来的号码
	}

	content := container.NewVBox(
		formItem("号码", numberEntry),
		formItem("名称", nameEntry),
		formItem("备注", noteEntry),
		starCheck,
		errLabel,
	)
	d := dialog.NewCustomWithoutButtons(title, content, a.mainW)
	// 对话框默认按内容最小尺寸铺在主窗体（320px）画布上，表单字段只有窄窄一条；
	// 撑到接近主窗体宽度、按内容高度算高度（用户要求"尽量撑大点"）
	d.Resize(fyne.NewSize(300, 336))
	submit := func() {
		number := strings.TrimSpace(numberEntry.Text)
		name := strings.TrimSpace(nameEntry.Text)
		if number == "" {
			errLabel.SetText("请输入号码")
			errLabel.Show()
			return
		}
		if name == "" {
			errLabel.SetText("请填写名称")
			errLabel.Show()
			return
		}
		prevNumber := ""
		if editing {
			prevNumber = prev.Number
		}
		// 号码去重（REQ-UI-033）：当场用内存快照判断，命中就在对话框里提示；
		// 存储层还会再判一次（并发写入时以它为准）
		if cur, exists := a.core.LookupContact(number); exists &&
			core.ContactKey(cur.Number) != core.ContactKey(prevNumber) {
			errLabel.SetText("该号码已在通信录中：" + cur.Number +
				strings.TrimSpace(" "+cur.Name))
			errLabel.Show()
			return
		}
		c := core.Contact{
			Number:  number,
			Name:    name,
			Note:    strings.TrimSpace(noteEntry.Text),
			Starred: starCheck.Checked,
		}
		d.Hide()
		go func() {
			err := a.core.SaveContact(prevNumber, c)
			fyne.Do(func() {
				if err != nil {
					a.showErrorIn(a.mainW, err)
					return
				}
				st.reload()
				if onSaved != nil {
					onSaved()
				}
			})
		}()
	}
	cancelBtn := widget.NewButton("取消", func() { d.Hide() })
	okBtn := widget.NewButton("确定", submit)
	okBtn.Importance = widget.HighImportance // 主要动作
	d.SetButtons([]fyne.CanvasObject{cancelBtn, okBtn})
	// 回车 = 确定（三个输入框都接上）
	numberEntry.OnSubmitted = func(string) { submit() }
	nameEntry.OnSubmitted = func(string) { submit() }
	noteEntry.OnSubmitted = func(string) { submit() }
	a.withDialogTheme(d)
	d.Show()
}

// onContactCall 从通信录拨号：复用主窗体的拨号入口（错误提示、通话面板、
// 最近号码都一致），只在发起前挡掉"已有通话进行中"——否则 onCallButton 的
// 语义是**挂断当前通话**。
func (a *App) onContactCall(c core.Contact, video bool) {
	if a.uiState != uiIdle || a.activeCallID != "" {
		a.showInfoIn(a.mainW, "提示", "已有通话进行中，无法发起新的呼叫")
		return
	}
	if c.Number == "" {
		return
	}
	a.dialSelect.SetText(c.Number)
	if video {
		a.onVideoCallButton()
	} else {
		a.onCallButton()
	}
}

// ---------------------------------------------------------------------------
// CSV 导入导出（REQ-UI-036）
// ---------------------------------------------------------------------------

// exportCSV 导出为 CSV：先取文本（内存快照，很快），再弹系统保存对话框。
func (st *contactsState) exportCSV() {
	text, err := st.a.core.ExportContactsCSV()
	if err != nil {
		st.a.showErrorIn(st.a.mainW, err)
		return
	}
	if len(st.all) == 0 {
		st.a.showInfoIn(st.a.mainW, "提示", "通信录为空，没有可导出的联系人")
		return
	}
	defaultName := "contacts-" + time.Now().Format("20060102") + ".csv"
	// 系统文件对话框自带消息循环会阻塞，必须在后台 goroutine 调用
	go func() {
		path, res := showSystemFileSaveDialog("导出通信录", defaultName)
		fyne.Do(func() {
			switch res {
			case fileDialogPicked:
				st.writeCSVFile(path, text)
			case fileDialogCancelled:
				// 用户取消：什么都不做
			default:
				st.exportCSVFallback(defaultName, text)
			}
		})
	}()
}

// exportCSVFallback 系统对话框不可用时回退到 Fyne 内置保存对话框。
func (st *contactsState) exportCSVFallback(defaultName, text string) {
	d := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil || w == nil {
			return
		}
		path := w.URI().Path()
		_ = w.Close()
		st.writeCSVFile(path, text)
	}, st.a.mainW)
	d.SetFileName(defaultName)
	st.a.withDialogTheme(d)
	d.Show()
}

// writeCSVFile 写文件（后台 goroutine，结果回主线程提示）。
func (st *contactsState) writeCSVFile(path, text string) {
	if path == "" {
		return
	}
	go func() {
		err := os.WriteFile(path, []byte(text), 0o644)
		fyne.Do(func() {
			if err != nil {
				logx.Logger().Warn("导出通信录失败", "path", path, "err", err)
				st.a.showErrorIn(st.a.mainW, fmt.Errorf("导出失败：%v", err))
				return
			}
			logx.Logger().Info("已导出通信录", "path", path, "count", len(st.all))
			st.a.showInfoIn(st.a.mainW, "导出完成",
				fmt.Sprintf("已导出 %d 位联系人到：\n%s", len(st.all), path))
		})
	}()
}

// importCSV 从 CSV 导入：弹系统打开对话框选文件，读进来交给 core 合并。
func (st *contactsState) importCSV() {
	go func() {
		path, res := showSystemFileOpenDialog("导入通信录")
		fyne.Do(func() {
			switch res {
			case fileDialogPicked:
				st.readCSVFile(path)
			case fileDialogCancelled:
			default:
				st.importCSVFallback()
			}
		})
	}()
}

// importCSVFallback 系统对话框不可用时回退到 Fyne 内置打开对话框。
func (st *contactsState) importCSVFallback() {
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}
		path := r.URI().Path()
		_ = r.Close()
		st.readCSVFile(path)
	}, st.a.mainW)
	st.a.withDialogTheme(d)
	d.Show()
}

// readCSVFile 读文件并导入（后台 goroutine）。
func (st *contactsState) readCSVFile(path string) {
	if path == "" {
		return
	}
	go func() {
		info, err := os.Stat(path)
		if err == nil && info.Size() > contactsCSVMaxSize {
			fyne.Do(func() {
				st.a.showErrorIn(st.a.mainW,
					fmt.Errorf("文件过大（%d MB 以上），请确认是通信录 CSV", contactsCSVMaxSize>>20))
			})
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fyne.Do(func() { st.a.showErrorIn(st.a.mainW, fmt.Errorf("读取文件失败：%v", err)) })
			return
		}
		res, err := st.a.core.ImportContactsCSV(string(data))
		fyne.Do(func() {
			if err != nil {
				st.a.showErrorIn(st.a.mainW, err)
				return
			}
			st.reload()
			msg := fmt.Sprintf("导入完成：新增 %d 条，覆盖 %d 条。", res.Added, res.Updated)
			if res.Skipped > 0 {
				msg += fmt.Sprintf("\n跳过 %d 行（没有号码或格式不对）。", res.Skipped)
			}
			if res.Added == 0 && res.Updated == 0 {
				msg += "\n没有可导入的联系人。"
			}
			st.a.showInfoIn(st.a.mainW, "导入通信录", msg)
		})
	}()
}

// ---------------------------------------------------------------------------
// 列表容器与行控件
// ---------------------------------------------------------------------------

// contactsListView 联系人列表：固定行高纵向排列，统一承接单击/Shift 单击/
// 拖动三种选择手势（与通话记录页的 historyListView 同一套做法：
// 行与行内按钮都不是 Draggable，所以整片区域的按下/拖动都落到这里，
// 事件坐标相对本容器，换算行号只需 y / 行高）。
type contactsListView struct {
	widget.BaseWidget
	st  *contactsState
	box *fyne.Container
}

func newContactsListView(st *contactsState) *contactsListView {
	v := &contactsListView{st: st}
	v.box = container.New(fixedRowsLayout{rowHeight: contactsRowHeight})
	v.ExtendBaseWidget(v)
	return v
}

func (v *contactsListView) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(v.box)
}

// rows 当前的行控件。
func (v *contactsListView) rows() []fyne.CanvasObject { return v.box.Objects }

// setRows 重建全部行（reload 用；nil 即清空）。
func (v *contactsListView) setRows(entries []contactEntry) {
	v.box.Objects = v.box.Objects[:0]
	v.appendRows(entries)
}

// appendRows 追加一批行。
func (v *contactsListView) appendRows(entries []contactEntry) {
	for _, e := range entries {
		v.box.Add(newContactsRow(v.st, e))
	}
	v.box.Refresh()
	v.Refresh()
}

// MouseDown 记下按下位置与 Shift 状态（拖动也用它当区间起点）。
func (v *contactsListView) MouseDown(ev *desktop.MouseEvent) {
	v.st.pressIdx = v.st.indexAt(ev.Position.Y)
	v.st.pressShift = ev.Modifier&fyne.KeyModifierShift != 0
}

// MouseUp 未发生拖动时按单击处理；右键弹菜单（**不改选中状态**）。
func (v *contactsListView) MouseUp(ev *desktop.MouseEvent) {
	if v.st.dragging {
		return
	}
	if ev.Button == desktop.MouseButtonSecondary {
		idx := v.st.indexAt(ev.Position.Y)
		v.st.ensureSelection(idx) // 右键的行没选中时先单选它
		v.st.showRowMenu(idx, ev.AbsolutePosition)
		return
	}
	v.st.clickSelect(v.st.pressIdx, v.st.pressShift)
}

// Dragged 拖动选择连续区间。
func (v *contactsListView) Dragged(ev *fyne.DragEvent) {
	v.st.dragging = true
	v.st.dragSelect(v.st.pressIdx, v.st.indexAt(ev.Position.Y))
}

// DragEnd 拖动结束。
func (v *contactsListView) DragEnd() { v.st.dragging = false }

// contactsRow 一位联系人（两行 + 右侧两个图形按钮）。行控件不复用：
// 每行对应一条固定记录，按钮回调直接捕获条目是安全的。
type contactsRow struct {
	widget.BaseWidget

	st  *contactsState
	key string

	bg       *canvas.Rectangle
	selected bool
	root     *fyne.Container
}

func newContactsRow(st *contactsState, e contactEntry) *contactsRow {
	a := st.a
	r := &contactsRow{st: st, key: e.key()}

	// ---- 第一行：号码 + ★ ----
	number := newPlainText(sanitizeNotifyText(e.c.Number, contactsWhoMax),
		contactsFontMain, theme.ForegroundColor(), false)
	line1 := container.New(markAfterLayout{gap: 4}, number, starMark(e.c.Starred))

	// ---- 第二行：名称 + 备注（备注吃剩余宽度，过长由 plainText 截断）----
	name := newPlainText(sanitizeNotifyText(e.c.Name, contactsWhoMax),
		contactsFontSmall, theme.ForegroundColor(), false)
	note := newPlainText(sanitizeNotifyText(e.c.Note, contactsWhoMax),
		contactsFontSmall, theme.PlaceHolderColor(), false)
	if e.c.Note == "" {
		note.Hide()
	}
	line2 := container.New(nameNoteLayout{gap: 6, maxRatio: contactsNameMaxRatio}, name, note)

	lines := container.New(panelRowsLayout{}, line1, line2) // 两行纵向居中（行距 2px）

	// ---- 右侧：操作按钮（纵向居中跨两行）----
	// 按钮随通话/拨号复用状态变化（空闲=呼叫/视频呼叫；通话中隐藏；咨询/转移/
	// 会议复用=对应话务动作），见 listRowActions。
	contact := e.c
	var videoAct func()
	if a.core != nil && a.core.VideoUsable() {
		videoAct = func() { a.onContactCall(contact, true) }
	}
	actions := a.listRowActions(contact.Number, contactsRowIconSize, contactsRowButtonRadius,
		historyVideoIconSize, func() { a.onContactCall(contact, false) }, videoAct, false, "")
	// 最右让出 historyScrollGap：滚动条压在列表右缘上，不留这一步会挡住按钮；
	// 左侧留 contactsTextLeftPad：两行文字不贴着窗口左缘（用户要求）
	content := container.NewBorder(nil, nil, nil,
		container.New(padRightLayout{pad: historyScrollGap}, container.NewCenter(actions)),
		container.New(padLeftLayout{pad: contactsTextLeftPad}, lines))

	r.bg = canvas.NewRectangle(color.Transparent)
	sep := canvas.NewRectangle(historySeparatorColor())
	sep.SetMinSize(fyne.NewSize(1, contactsRowSep))
	r.root = container.New(fixedRowLayout{gap: contactsRowGap, sep: contactsRowSep}, r.bg, content, sep)

	r.ExtendBaseWidget(r)
	return r
}

func (r *contactsRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(r.root)
}

// setSelected 选中态：浅色底（颜色随主题）。
func (r *contactsRow) setSelected(on bool) {
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

// starMark 常用联系人标记：金色 ★；不常用时返回一个隐藏的占位对象
// （markAfterLayout 会自动跳过不可见标记）。
func starMark(on bool) fyne.CanvasObject {
	img := canvas.NewImageFromResource(IconStar)
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(contactsStarSize, contactsStarSize))
	if !on {
		img.Hide()
	}
	return img
}

// ---------------------------------------------------------------------------
// tab 分段控件
// ---------------------------------------------------------------------------

// segmentedButton 分段控件里的一个 tab：选中蓝底白字，未选中主题输入底色 + 前景字。
type segmentedButton struct {
	widget.BaseWidget

	bg       *canvas.Rectangle
	text     *canvas.Text
	selected bool
	onTap    func()
}

func newSegmentedButton(label string, onTap func()) *segmentedButton {
	b := &segmentedButton{onTap: onTap}
	b.bg = canvas.NewRectangle(segmentedBgColor(false))
	b.text = canvas.NewText(label, theme.ForegroundColor())
	b.text.Alignment = fyne.TextAlignCenter
	b.text.TextStyle = fyne.TextStyle{Bold: true}
	b.ExtendBaseWidget(b)
	b.SetSelected(false)
	return b
}

func (b *segmentedButton) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewStack(b.bg, container.NewCenter(b.text)))
}

func (b *segmentedButton) MinSize() fyne.Size {
	m := b.text.MinSize()
	return fyne.NewSize(m.Width+24, 30)
}

// SetSelected 选中态：蓝底白字 / 输入底色 + 前景字。
func (b *segmentedButton) SetSelected(on bool) {
	b.selected = on
	b.bg.FillColor = segmentedBgColor(on)
	if on {
		b.text.Color = colorIconWhite
	} else {
		b.text.Color = theme.ForegroundColor()
	}
	b.bg.Refresh()
	b.text.Refresh()
}

// segmentedBgColor 分段按钮底色（选中=主题蓝，未选中=输入框底色）。
func segmentedBgColor(selected bool) color.Color {
	if selected {
		return colorHoldBlue
	}
	return theme.InputBackgroundColor()
}

func (b *segmentedButton) Tapped(*fyne.PointEvent) {
	if b.onTap != nil {
		b.onTap()
	}
}

func (b *segmentedButton) Cursor() desktop.Cursor { return desktop.PointerCursor }

// segmentedLayout 分段控件布局：每个子项等分整行宽度、拉满高度。
type segmentedLayout struct{ gap float32 }

func (l segmentedLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var sz fyne.Size
	first := true
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		if !first {
			sz.Width += l.gap
		}
		sz.Width += m.Width
		sz.Height = fyne.Max(sz.Height, m.Height)
		first = false
	}
	return sz
}

func (l segmentedLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	n := 0
	for _, o := range objs {
		if o.Visible() {
			n++
		}
	}
	if n == 0 {
		return
	}
	w := (size.Width - l.gap*float32(n-1)) / float32(n)
	x := float32(0)
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		o.Resize(fyne.NewSize(w, size.Height))
		o.Move(fyne.NewPos(x, 0))
		x += w + l.gap
	}
}
