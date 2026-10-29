package ui

// 在线座席页（REQ-ACC-035）：坐席模式下从主窗体顶栏的「座位表」图标打开，
// 调用 CTI 动作 `listAgents` 拉取在线坐席（协议 §9.5 / §11，结果走
// MSP_RESULT.data），支持按座席号 / 显示名 / 技能组搜索，右上角刷新按钮重新拉取。
//
// 列表与通信录同款（两行紧凑行）：
//
//	[座席图标] 座席号                                   [监听] [呼叫] [视频呼叫]
//	           显示名
//
// 座席图标颜色：绿 = 空闲（free）；黄 = 呼叫中 / 振铃中 / 通话中；红 = 置忙或异常
// （busy / leave / not_ready / miss_phone / offline / free_offhook）。
// 右侧操作按钮随座席自身状态与本机话务/拨号复用状态变化，见 listRowActions。

import (
	"image/color"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/core"
)

const (
	agentsRowHeight    = 46
	agentsRowGap       = 2
	agentsRowSep       = 1
	agentsButtonSize   = 24
	agentsButtonRadius = 2
	agentsFontMain     = 14
	agentsFontSmall    = 12
	agentsStatusIcon   = 18
	agentsSearchDelay  = 300 * time.Millisecond
	agentsTextLeftPad  = 2
)

// agentEntry 一行在线坐席（MSP_RESULT.data 的一项）。
type agentEntry struct {
	No          string // agent 座席号
	DisplayName string // displayName 显示名
	ACD         string // acd 技能组（"110:1,122:1"）
	Mode        string // agentMode：normal / dial_out / monitor
	Status      string // agentStatus：free / ringing / talking / …
}

// agentsState 在线座席页的状态与控件。
type agentsState struct {
	a *App

	all    []agentEntry
	rows   []agentEntry
	query  string
	loaded bool // 是否已拿到过列表（决定空态文案）

	search      *widget.Entry
	searchTimer *time.Timer
	body        *fyne.Container // 列表行容器（fixedRowsLayout）
	scroll      *container.Scroll
	empty       *widget.Label
	page        *fyne.Container
}

// buildAgentsPage 构建在线座席页（与通信录/通话记录一样切在主窗体内）。
func (a *App) buildAgentsPage() {
	st := &agentsState{a: a}
	a.agents = st

	topBar := pageTopBar("在线座席", a.showDialPage, nil)

	st.search = widget.NewEntry()
	st.search.SetPlaceHolder("搜索座席号 / 名称 / 技能组")
	st.search.OnChanged = func(v string) { st.scheduleSearch(v) }
	st.search.OnSubmitted = func(string) { st.applyFilter() }
	refreshBtn := topBarIconButton(theme.ViewRefreshIcon(), st.request)
	searchRow := container.NewBorder(nil, nil, nil, refreshBtn, st.search)

	st.body = container.New(fixedRowsLayout{rowHeight: agentsRowHeight})
	st.scroll = container.NewVScroll(st.body)
	st.empty = widget.NewLabel("")
	st.empty.Alignment = fyne.TextAlignCenter
	st.empty.Hide() // 首次加载完成前不显示空态
	bodyStack := container.NewStack(st.scroll, container.NewCenter(st.empty))

	st.page = container.NewBorder(container.NewVBox(topBar, searchRow), nil, nil, nil, bodyStack)
	a.agentsPage = st.page
}

// showAgents 切到在线座席页（主窗体顶栏的座位表图标；通话中也可打开，只浏览）。
func (a *App) showAgents() {
	if a.agentsPage == nil || len(a.mainContent.Objects) == 0 {
		return
	}
	a.hideTip()
	a.hideDialSuggest()
	a.mainContent.Objects[0] = a.agentsPage
	a.mainContent.Refresh()
	a.agents.request()
}

// request 调 listAgents 拉取在线坐席（结果在 cti.result 里按 action 分发回来）。
func (st *agentsState) request() {
	if st.a == nil || st.a.core == nil {
		return
	}
	if err := st.a.runCTI(nil, "listAgents", nil); err != nil {
		// 已有指令在途/发送失败：runCTI 已经提示过，这里保持页面原样
		return
	}
	if !st.loaded {
		st.showEmpty("正在获取坐席列表…")
	}
}

// setAgents 收到 listAgents 结果后刷新列表。
func (st *agentsState) setAgents(list []agentEntry) {
	st.loaded = true
	st.all = list
	st.applyFilter()
}

// setError 拉取失败：没有缓存列表时在空态区显示原因（有列表就保住旧数据，
// 只弹 toast——toast 在调用方弹）。
func (st *agentsState) setError(msg string) {
	st.loaded = true
	if len(st.all) == 0 {
		st.showEmpty("获取失败：" + msg)
	}
}

// scheduleSearch 搜索输入防抖（与通信录一致）。
func (st *agentsState) scheduleSearch(v string) {
	st.query = v
	if st.searchTimer != nil {
		st.searchTimer.Stop()
	}
	st.searchTimer = time.AfterFunc(agentsSearchDelay, func() {
		fyne.Do(func() { st.applyFilter() })
	})
}

// applyFilter 按搜索词过滤并重建列表。
func (st *agentsState) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(st.query))
	// 本机座席不显示在自己的在线座席列表里（用户要求）
	self := ""
	if st.a != nil && st.a.core != nil {
		self = core.NormalizeNumber(st.a.core.State().Register.Account)
	}
	st.rows = st.rows[:0]
	for _, e := range st.all {
		if self != "" && core.NormalizeNumber(e.No) == self {
			continue
		}
		if q == "" ||
			strings.Contains(strings.ToLower(e.No), q) ||
			strings.Contains(strings.ToLower(e.DisplayName), q) ||
			strings.Contains(strings.ToLower(e.ACD), q) {
			st.rows = append(st.rows, e)
		}
	}
	st.rebuild()
}

// rebuild 重建列表行控件。
func (st *agentsState) rebuild() {
	if st.body == nil {
		return
	}
	rows := make([]fyne.CanvasObject, 0, len(st.rows))
	for _, e := range st.rows {
		rows = append(rows, newAgentRow(st.a, e))
	}
	st.body.Objects = rows
	st.body.Refresh()
	switch {
	case len(st.rows) > 0:
		st.empty.Hide()
	case !st.loaded:
		// 加载中：保留"正在获取…"
	default:
		st.showEmpty("暂无在线座席")
	}
}

// showEmpty 显示空态/加载提示。
func (st *agentsState) showEmpty(text string) {
	if st.empty == nil {
		return
	}
	st.empty.SetText(text)
	st.empty.Show()
}

// parseAgentEntries 解析 MSP_RESULT.data（[]any of map[string]any）。
func parseAgentEntries(data any) []agentEntry {
	arr, ok := data.([]any)
	if !ok {
		return nil
	}
	out := make([]agentEntry, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		e := agentEntry{
			No:          strVal(m, "agent"),
			DisplayName: strVal(m, "displayName"),
			ACD:         strVal(m, "acd"),
			Mode:        strVal(m, "agentMode"),
			Status:      strVal(m, "agentStatus"),
		}
		if e.No == "" {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].No < out[j].No })
	return out
}

func strVal(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// skillGroups 把 acd（"110:1,122:1"）拆成技能组列表（去空白、去空项）。
func skillGroups(acd string) []string {
	var groups []string
	for _, g := range strings.Split(acd, ",") {
		if g = strings.TrimSpace(g); g != "" {
			groups = append(groups, g)
		}
	}
	return groups
}

// newSkillTag 技能组小标签：圆角底色 + 文字；note 非空时悬停显示提示
// （"more" 标签用它展示其余技能组）。
func newSkillTag(text, note string, tip *tooltipLayer) fyne.CanvasObject {
	var label fyne.CanvasObject
	if note != "" {
		label = newHoverNote(text, agentsFontSmall, note, tip)
	} else {
		label = newPlainText(text, agentsFontSmall, theme.ForegroundColor(), false)
	}
	bg := canvas.NewRectangle(theme.Color(theme.ColorNameButton))
	bg.CornerRadius = 3
	return container.New(tagLayout{padX: 5, padY: 1}, bg, label)
}

// tagLayout [底色, 内容]：内容按自身尺寸居中、底色铺满，整体最小尺寸 =
// 内容 + 内边距（技能组 tag 用）。
type tagLayout struct{ padX, padY float32 }

func (l tagLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	m := objs[1].MinSize()
	return fyne.NewSize(m.Width+2*l.padX, m.Height+2*l.padY)
}

func (l tagLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	objs[0].Resize(size)
	objs[0].Move(fyne.NewPos(0, 0))
	m := objs[1].MinSize()
	objs[1].Resize(m)
	objs[1].Move(fyne.NewPos(l.padX, (size.Height-m.Height)/2))
}

// shiftXLayout 单个子对象按自身尺寸摆放、再水平偏移 dx（正值右移；负值左移）。
// 在线座席行的状态文字用它微调位置（右对齐块里再左移 4px）。
type shiftXLayout struct{ dx float32 }

func (l shiftXLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	return objs[0].MinSize()
}

func (l shiftXLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	m := objs[0].MinSize()
	objs[0].Resize(m)
	objs[0].Move(fyne.NewPos(l.dx, (size.Height-m.Height)/2))
}

// rightLinesLayout 若干行**右对齐**的竖直排列（每行右缘贴住块右缘，行距 0）。
// 在线座席行里"座席状态 / 技能组"这一块用它（用户要求靠右显示）。
type rightLinesLayout struct{}

func (l rightLinesLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		w = fyne.Max(w, m.Width)
		h += m.Height
	}
	return fyne.NewSize(w, h)
}

func (l rightLinesLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		o.Resize(m)
		o.Move(fyne.NewPos(size.Width-m.Width, y))
		y += m.Height
	}
}

// agentStatusColor 座席状态颜色：绿=空闲、黄=呼叫/振铃/通话中、红=置忙或异常
//（含整理中/离席——在线座席列表的"不可用"统一红，与本机坐席按钮的黄/蓝口径不同）。
func agentStatusColor(status string) color.NRGBA {
	switch strings.ToLower(status) {
	case "free":
		return colorSuccessGreen
	case "dialing", "ringing", "talking":
		return colorHoldYellow
	default: // busy / acw / leave / not_ready / miss_phone / offline / free_offhook / 未知
		return colorDangerRed
	}
}

// agentStatusText 座席状态的悬停说明。
func agentStatusText(status string) string {
	switch strings.ToLower(status) {
	case "free":
		return "空闲"
	case "dialing":
		return "呼叫中"
	case "ringing":
		return "振铃中"
	case "talking":
		return "通话中"
	case "free_offhook":
		return "摘机"
	case "busy":
		return "置忙"
	case "acw":
		return "整理中"
	case "leave":
		return "离席"
	case "not_ready":
		return "示忙"
	case "miss_phone":
		return "话机未连接"
	case "offline":
		return "离线"
	}
	return status
}

// dialNumber 列表行拨号：复用主窗体的拨号入口（错误提示、通话面板、最近号码一致），
// 并挡掉"已有通话进行中"（否则 onCallButton 的语义是挂断当前通话）。
// displayName 是座席列表里的显示名：呼叫期间优先于通信录名称显示。
func (a *App) dialNumber(no, displayName string, video bool) {
	if a.uiState != uiIdle || a.activeCallID != "" {
		a.showInfoIn(a.mainW, "提示", "已有通话进行中，无法发起新的呼叫")
		return
	}
	if no == "" {
		return
	}
	a.dialSelect.SetText(no)
	if video {
		a.onVideoCallButtonNamed(displayName)
	} else {
		a.onCallButtonNamed(displayName)
	}
}

// ---------------------------------------------------------------------------
// 列表行
// ---------------------------------------------------------------------------

// agentRow 一位在线坐席（两行 + 左侧状态图标 + 右侧操作按钮）。
type agentRow struct {
	widget.BaseWidget
	root *fyne.Container
}

func newAgentRow(a *App, e agentEntry) *agentRow {
	r := &agentRow{}

	// ---- 左侧：座席图标（颜色表示状态；行内已有状态文字，图标不再弹提示）----
	statusIcon := newHoverIcon(tintIcon(IconAgentSeat, agentStatusColor(e.Status)), agentsStatusIcon, a.mainTip)
	left := container.NewHBox(container.New(padLeftLayout{pad: agentsTextLeftPad}, statusIcon))

	// ---- 两行：座席号 / 显示名 ----
	no := newPlainText(e.No, agentsFontMain, theme.ForegroundColor(), false)
	line1 := container.New(markAfterLayout{gap: 4}, no)
	name := newPlainText(e.DisplayName, agentsFontSmall, theme.PlaceHolderColor(), false)
	lines := container.New(panelRowsLayout{}, line1, name)

	// ---- 右侧：随座席状态/本机话务状态变化的操作按钮 ----
	var video func()
	if a.core != nil && a.core.VideoUsable() {
		noCopy, dispName := e.No, e.DisplayName
		video = func() { a.dialNumber(noCopy, dispName, true) }
	}
	noCopy, dispName := e.No, e.DisplayName
	actions := a.listRowActions(noCopy, agentsButtonSize, agentsButtonRadius, historyVideoIconSize,
		func() { a.dialNumber(noCopy, dispName, false) }, video, true, e.Status)

	// ---- 操作按钮左侧的信息块（靠右）：上层=座席状态、下层=技能组（tag）----
	// 技能组以 tag 显示：只显示第一个，多个时再加一个 "more" tag，
	// 悬停 "more" 显示其余技能组（用户要求）。
	statusText := newPlainText(agentStatusText(e.Status), agentsFontSmall,
		theme.ForegroundColor(), false)
	statusLine := container.New(shiftXLayout{dx: -4}, statusText) // 状态文字左移 4px（用户要求）
	groups := skillGroups(e.ACD)
	var skillLine fyne.CanvasObject
	switch len(groups) {
	case 0:
		skillLine = newPlainText("", agentsFontSmall, theme.PlaceHolderColor(), false)
		skillLine.Hide()
	case 1:
		skillLine = newSkillTag(groups[0], "", a.mainTip)
	default:
		skillLine = container.NewHBox(
			newSkillTag(groups[0], "", a.mainTip),
			newSkillTag("more", strings.Join(groups[1:], ", "), a.mainTip),
		)
	}
	infoBlock := container.NewCenter(container.New(rightLinesLayout{}, statusLine, skillLine))
	right := container.NewHBox(infoBlock, actions)

	content := container.NewBorder(nil, nil, left,
		container.New(padRightLayout{pad: historyScrollGap}, container.NewCenter(right)), lines)

	sep := canvas.NewRectangle(historySeparatorColor())
	sep.SetMinSize(fyne.NewSize(1, agentsRowSep))
	r.root = container.New(fixedRowLayout{gap: agentsRowGap, sep: agentsRowSep},
		canvas.NewRectangle(color.Transparent), content, sep)

	r.ExtendBaseWidget(r)
	return r
}

func (r *agentRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(r.root)
}
