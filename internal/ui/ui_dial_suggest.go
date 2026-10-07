package ui

// 拨号输入框的建议下拉（最近呼叫 + 通信录匹配，REQ-UI-024 修订）。
//
// **为什么不用 widget.PopUp / PopUpMenu**（改这里之前必读）：
// Fyne 把"顶层 overlay"当成了一个独立的键盘焦点域——画布上只要挂了 overlay，
// `canvas.focusManager()` 就返回**这个 overlay 自己的**焦点管理器
//（driver/software/canvas.go 的 focusManager()，GLFW 侧同理），而
// `FocusManager.Focus(obj)` 会先在**它自己的内容树**里找 obj，找不到就不生效
//（internal/app/focus_manager.go）。于是：
//   - PopUpMenu 的 Show() 会 canvas.Focus(自己)（widget/popup_menu.go），
//     焦点被抢走 → 接着敲的字进不了输入框；
//   - 换成不碰焦点的 widget.PopUp 也没用：overlay 一旦在顶，主内容的焦点
//     就查不到了（canvas.Focused() 变成 overlay 域里的 nil），
//     而 `canvas.Focus(输入框)` 因为"输入框不在 overlay 的树里"照样失败。
//   - 真机现象（用户实测）："输入后弹出下拉，再打字就无效，要关掉下拉才能继续"。
//
// 所以这里的下拉是**窗口内容 Stack 里的顶层图层**（与 ui_tooltip.go 同一招）：
// 不切焦点域，输入框一直能收到键；图层里的行是普通 Button（Tappable），
// 点得到；图层其它地方不可点，命中测试照常穿透到下面的页面。

import (
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/contactsearch"
)

// dialSuggestMaxRows 最多列几行（再多屏幕放不下，也没必要）。
const dialSuggestMaxRows = 8

// dialSuggestHideDelay 失焦后**延迟**收起下拉的时间。
//
// 为什么不能立即收：Fyne 的鼠标处理是 press/release 两次独立命中测试
// （internal/driver/glfw/window.go 的 mouseClicked）——按下时先做"点到非聚焦控件
// → canvas.Unfocus()"，**再**在抬起时重新找一次点击处的对象，只有"和按下时是
// 同一个对象"才派发 Tapped。所以在 FocusLost 里立刻藏掉图层的话，抬起那一下
// 命中到的是下面的页面，点选建议**永远点不中**（用户实测："下拉出现逻辑对了，
// 但是选不上了"）。延迟这一会儿足够点击走完 press→release；这段时间里若真点中了
// 某一行，pickDialSuggestion 会取消这个定时器。
const dialSuggestHideDelay = 250 * time.Millisecond

// dialSuggestLayer 建议下拉图层：铺满所在容器，平时隐藏，show 时在输入框正下方
// 画一块圆角底色 + 若干可点的行。
type dialSuggestLayer struct {
	widget.BaseWidget

	bg   *canvas.Rectangle
	rows *fyne.Container // 竖排的行（每次刷新重建）
	box  *fyne.Container

	shown  bool
	anchor fyne.Position // 输入框左下角的**画布绝对坐标**
	width  float32       // 下拉宽度（跟输入框一样宽）
	origin fyne.Position // 图层原点（布局时刷新，用于绝对坐标换算）
}

func newDialSuggestLayer() *dialSuggestLayer {
	l := &dialSuggestLayer{}
	l.bg = canvas.NewRectangle(tooltipBgColor()) // 与弹窗同底色（随主题明暗）
	l.bg.CornerRadius = 4
	l.rows = container.NewVBox()
	l.box = container.New(dialSuggestLayout{l}, l.bg, container.NewPadded(l.rows))
	l.box.Hide()
	l.ExtendBaseWidget(l)
	return l
}

func (l *dialSuggestLayer) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(l.box)
}

// MinSize 恒为 0：图层不参与布局（否则这块"什么都没有"的层会把页面撑大）。
func (l *dialSuggestLayer) MinSize() fyne.Size { return fyne.NewSize(0, 0) }

// update 重建行并显示（anchor 为输入框左下角的画布绝对坐标，width 为下拉宽度）。
func (l *dialSuggestLayer) update(entries []dialSuggestEntry, anchor fyne.Position, width float32) {
	if len(entries) == 0 {
		l.hide()
		return
	}
	l.rows.RemoveAll()
	for _, e := range entries {
		e := e
		btn := widget.NewButton(e.label, e.onTap)
		// 菜单观感：左对齐 + 低重要性（无底色，悬停才亮）
		btn.Alignment = widget.ButtonAlignLeading
		btn.Importance = widget.LowImportance
		l.rows.Add(btn)
	}
	l.anchor = anchor
	l.width = width
	l.shown = true
	l.box.Show()
	l.box.Refresh() // 首次显示时行还没算过尺寸，显示前强制重排一次
	l.Refresh()
}

// hide 收起下拉（未显示时是空操作）。
func (l *dialSuggestLayer) hide() {
	if !l.shown {
		return
	}
	l.shown = false
	l.box.Hide()
	l.Refresh()
}

// moveWith 只更新锚点（输入框被拖动/窗口尺寸变化时用）。
func (l *dialSuggestLayer) moveWith(anchor fyne.Position, width float32) {
	if !l.shown {
		return
	}
	l.anchor, l.width = anchor, width
	l.box.Refresh()
}

// dialSuggestLayout 把底色与行容器摆到输入框下方（图层内坐标）。
type dialSuggestLayout struct{ l *dialSuggestLayer }

func (lay dialSuggestLayout) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(0, 0) }

func (lay dialSuggestLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	l := lay.l
	bg, padded := objs[0], objs[1]
	if !l.shown || size.Width <= 0 {
		return
	}
	// 画布绝对坐标 → 图层内坐标（在这里换算：布局时原点一定是最新的）
	origin := fyne.Position{}
	if app := fyne.CurrentApp(); app != nil && app.Driver() != nil {
		origin = app.Driver().AbsolutePositionForObject(l)
	}
	l.origin = origin
	pos := l.anchor.Subtract(origin)
	w := l.width
	if w <= 0 || w > size.Width-4 {
		w = size.Width - 4
	}
	h := padded.MinSize().Height
	if pos.Y+h > size.Height-2 { // 下面放不下：往上翻（输入框一般在页面上半部，很少走到）
		pos.Y = pos.Y - h - 2*theme.Padding() - 24
	}
	if pos.Y < 2 {
		pos.Y = 2
	}
	if pos.X < 2 {
		pos.X = 2
	}

	bg.Resize(fyne.NewSize(w, h))
	bg.Move(pos)
	padded.Resize(fyne.NewSize(w, h))
	padded.Move(pos)
}

// dialEntry 拨号输入框：普通的 Entry（**不要 SelectEntry 右边那个下拉箭头**，
// 用户要求去掉），只在获得/失去焦点时通知外层——获得焦点且内容为空时列出
// 最近呼叫，"失去焦点就收起下拉"（与网页版行为一致）。
type dialEntry struct {
	widget.Entry
	app *App
}

func newDialEntry(a *App) *dialEntry {
	e := &dialEntry{app: a}
	e.ExtendBaseWidget(e)
	return e
}

func (e *dialEntry) FocusGained() {
	e.Entry.FocusGained()
	if e.app != nil {
		e.app.cancelHideDialSuggest() // 又聚焦了：撤销"待收起"
		e.app.refreshDialSuggestions(e.Text)
	}
}

func (e *dialEntry) FocusLost() {
	e.Entry.FocusLost()
	if e.app != nil {
		e.app.scheduleHideDialSuggest() // 延迟收，见 dialSuggestHideDelay 的说明
	}
}

// wireDialInput 把拨号输入框接上建议下拉：**内容一变就刷新**（直接敲字、点击
// 拨号按键、主窗体按键都走 SetText → OnChanged），获得焦点且内容为空时列最近呼叫，
// 失去焦点就收起（焦点钩子在 dialEntry 里）。
func (a *App) wireDialInput() {
	if a.dialSelect == nil {
		return
	}
	a.dialSelect.OnChanged = func(text string) { a.refreshDialSuggestions(text) }
}

// dialInputFocused 输入框当前是否持有键盘焦点。
//
// **只有有焦点才弹下拉**（用户要求）：点击拨号按键、未聚焦时在主窗体敲数字都会
// 改输入框内容（走 SetText → OnChanged），但那两条路径输入框并没有焦点，
// 不该冒出下拉框来。
func (a *App) dialInputFocused() bool {
	if a.dialSelect == nil {
		return false
	}
	cv := fyne.CurrentApp().Driver().CanvasForObject(a.dialSelect)
	if cv == nil {
		return false
	}
	return cv.Focused() == fyne.Focusable(a.dialSelect)
}

// dialSuggestEntry 建议下拉里的一行。
type dialSuggestEntry struct {
	label string
	onTap func()
}

// refreshDialSuggestions 按当前输入刷新建议：
//   - 空输入：不弹（最近呼叫点一下输入框的箭头按钮也能看，见 refreshRecentDials）；
//   - 非空：**同时查通信录**（contactsearch，与通信录页的搜索完全同一套：
//     音头/全拼/汉字/号码/备注）+ 最近呼叫里包含该串的；点到哪一条就把**号码**
//     填进输入框（不直接拨号）。
func (a *App) refreshDialSuggestions(text string) {
	if a.mainSuggest == nil {
		return
	}
	// 输入框没有焦点（点拨号按键、主窗体敲数字）：不弹，已弹的也收起
	if !a.dialInputFocused() {
		a.hideDialSuggest()
		return
	}
	q := strings.TrimSpace(text)
	var entries []dialSuggestEntry
	lower := strings.ToLower(q)
	// 最近呼叫：空输入时**全列**（获得焦点就看得到，与网页一致），有输入时筛包含该串的
	for _, n := range a.core.RecentDials() {
		if q == "" || strings.Contains(strings.ToLower(n), lower) {
			n := n
			entries = append(entries, dialSuggestEntry{
				label: n + "（最近）",
				onTap: func() { a.pickDialSuggestion(n) },
			})
		}
	}
	if q == "" {
		if len(entries) == 0 {
			a.hideDialSuggest()
			return
		}
		pos0 := fyne.CurrentApp().Driver().AbsolutePositionForObject(a.dialSelect)
		a.mainSuggest.update(entries, pos0.Add(fyne.NewPos(0, a.dialSelect.Size().Height)),
			a.dialSelect.Size().Width)
		return
	}
	hits := 0
	for _, c := range a.core.Contacts() {
		if hits >= 6 || len(entries) >= dialSuggestMaxRows {
			break
		}
		if !contactsearch.Match(contactsearch.NewIndex(c.Name), c.Number, c.Note, q) {
			continue
		}
		label := c.Number
		if c.Name != "" {
			label += " " + c.Name
		}
		if c.Note != "" {
			label += " - " + c.Note
		}
		if c.Starred {
			label += " ★"
		}
		c := c
		entries = append(entries, dialSuggestEntry{
			label: label,
			onTap: func() { a.pickDialSuggestion(c.Number) },
		})
		hits++
	}
	if len(entries) > dialSuggestMaxRows {
		entries = entries[:dialSuggestMaxRows]
	}
	// 锚点 = 输入框左下角（画布绝对坐标）：先取输入框的绝对位置与尺寸
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(a.dialSelect)
	a.mainSuggest.update(entries, pos.Add(fyne.NewPos(0, a.dialSelect.Size().Height)),
		a.dialSelect.Size().Width)
}

// pickDialSuggestion 选中一条建议：把号码填进输入框并收起下拉。
func (a *App) pickDialSuggestion(number string) {
	a.dialSelect.SetText(number)
	a.hideDialSuggest()
}

// scheduleHideDialSuggest 失焦后**延迟**收起：给点击走完 press→release 的时间
// （立即收会让"抬起"那一下命中不到那一行，点选就永远点不中，见 dialSuggestHideDelay）。
func (a *App) scheduleHideDialSuggest() {
	a.cancelHideDialSuggest()
	a.hideSuggestTimer = time.AfterFunc(dialSuggestHideDelay, func() {
		fyne.Do(func() {
			if a.mainSuggest != nil {
				a.mainSuggest.hide()
			}
		})
	})
}

// cancelHideDialSuggest 撤销"待收起"（重新聚焦、或马上要手动收起时）。
func (a *App) cancelHideDialSuggest() {
	if a.hideSuggestTimer != nil {
		a.hideSuggestTimer.Stop()
		a.hideSuggestTimer = nil
	}
}

// hideDialSuggest 立刻收起拨号建议下拉（切页、进通话、输入清空/无焦点时调用）。
func (a *App) hideDialSuggest() {
	a.cancelHideDialSuggest()
	if a.mainSuggest != nil {
		a.mainSuggest.hide()
	}
}
