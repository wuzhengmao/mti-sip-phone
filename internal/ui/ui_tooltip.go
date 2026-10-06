package ui

// 备注悬停提示（REQ-UI-037）：名称上停住鼠标，在指针下方弹出该联系人的备注。
//
// **为什么不用 widget.PopUp / canvas.Overlays()**（改这里之前必读）：
// Fyne 的指针命中测试一旦发现画布上有 overlay，就**只查 overlay 层**、
// 不再回落到内容层（internal/driver/util.go 的 FindObjectAtPositionMatching：
// `if overlay != nil { 只遍历 overlay } else { 遍历 roots }`）。于是：
//   - overlay 是 Hoverable（OverlayContainer 就是）→ 鼠标一动就把被提示控件的
//     悬停状态抢走 → 触发 MouseOut → 提示消失 → 指针还在原地又 MouseIn →
//     反复显示/隐藏（闪烁）；
//   - overlay 做成非 Hoverable → 提示显示期间整个界面收不到指针事件，
//     提示再也没有人来关掉它。
//
// 所以改用**窗口内容 Stack 的顶层图层**：图层只是自绘矩形 + 文本，两者都不是
// Hoverable/Tappable，命中测试在它身上找不到匹配项，照常穿透到下面的行；
// 而它在 Stack 里排最后，绘制在最上层且不受滚动容器裁剪。

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	// tooltipMaxW 提示框最大宽度（超过就截断补省略号）：备注再长也不能铺满屏幕。
	tooltipMaxW = 240
	// tooltipPadX/tooltipPadY 提示框内边距。
	tooltipPadX = 6
	tooltipPadY = 4
	// tooltipOffsetX/tooltipOffsetY 提示框相对指针的偏移：**必须错开指针**，
	// 否则指针落在提示框上又是另一套麻烦。
	tooltipOffsetX = 12
	tooltipOffsetY = 18
	// tooltipDelay 悬停多久才弹（避免扫过列表时一路乱弹）。
	tooltipDelay = 300 * time.Millisecond
	// tooltipFont 提示文字字号。
	tooltipFont = 12
)

// tooltipLayer 悬浮提示图层：铺满所在容器，平时隐藏，show 时在指定位置画一块
// 圆角底色 + 一行文本。
type tooltipLayer struct {
	widget.BaseWidget

	bg    *canvas.Rectangle
	label *plainText
	box   *fyne.Container

	shown bool
	above bool // true = 显示在指针/控件**上方**（配置页的 ⓘ 用）
	// absPos 画布**绝对**坐标；换算成图层内坐标放在 Layout 里做——show 的那一刻
	// 图层可能还没布局过（origin 取到 0），直接换算会把提示画到错误的位置
	//（表现为"第一次点了看不见/位置不对，之后就正常"）。
	absPos fyne.Position
	pos    fyne.Position // 图层内坐标（Layout 里由 absPos 换算）

	// shownText 当前显示的文字：Cursor() 驱动的悬停据此判断"要不要换内容"，
	shownText string
}

func newTooltipLayer() *tooltipLayer {
	l := &tooltipLayer{}
	l.bg = canvas.NewRectangle(tooltipBgColor())
	l.bg.CornerRadius = 4
	l.label = newThemedText("", tooltipFont, false)
	l.box = container.New(tooltipBoxLayout{l}, l.bg, l.label)
	l.box.Hide()
	l.ExtendBaseWidget(l)
	return l
}

func (l *tooltipLayer) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(l.box)
}

// MinSize 恒为 0：图层不参与布局（Stack 会把子对象撑满，但最小尺寸不能被它
// 抬高，否则窗口/页面会被这块"什么都没有"的层撑大）。
func (l *tooltipLayer) MinSize() fyne.Size { return fyne.NewSize(0, 0) }

// show 在画布绝对坐标 absPos 处弹出提示（默认在指针**右下**，越界会自动翻转/钳制）。
func (l *tooltipLayer) show(text string, absPos fyne.Position) {
	l.showAt(text, absPos)
}

func (l *tooltipLayer) showAt(text string, absPos fyne.Position) {
	if text == "" {
		l.hide()
		return
	}
	l.absPos = absPos
	l.label.SetText(text)
	l.shownText = text
	l.shown = true
	l.box.Show()
	l.box.Refresh() // 首次显示时标签的尺寸可能还没算出来，显示前强制重排一次
	l.Refresh()
}

// hide 收起提示（未显示时是空操作）。
func (l *tooltipLayer) hide() {
	if !l.shown {
		return
	}
	l.shown = false
	l.shownText = ""
	l.box.Hide()
	l.Refresh()
}

// hideTip 收起主窗体的备注提示（切页、列表重载、窗口隐藏时调用：
// 指针可能没离开那一行，MouseOut 不会来）。
func (a *App) hideTip() {
	if a.mainTip != nil {
		a.mainTip.hide()
	}
}

// tooltipBgColor 提示框底色：与 Fyne 弹窗一致（随主题明暗）。
func tooltipBgColor() color.Color {
	app := fyne.CurrentApp()
	if app == nil {
		return color.NRGBA{R: 0x2e, G: 0x32, B: 0x38, A: 0xff}
	}
	return app.Settings().Theme().Color(theme.ColorNameOverlayBackground, app.Settings().ThemeVariant())
}

// tooltipBoxLayout 把底色与文本摆到 l.pos 处，并按图层尺寸做翻转与钳制。
type tooltipBoxLayout struct{ l *tooltipLayer }

func (lay tooltipBoxLayout) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(0, 0) }

func (lay tooltipBoxLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	l := lay.l
	bg, label := objs[0], objs[1]
	if !l.shown {
		return
	}
	// 画布绝对坐标 → 图层内坐标（在这里换算：布局时原点一定是最新的）
	origin := fyne.Position{}
	if app := fyne.CurrentApp(); app != nil && app.Driver() != nil {
		origin = app.Driver().AbsolutePositionForObject(l)
	}
	l.pos = l.absPos.Subtract(origin).Add(fyne.NewPos(tooltipOffsetX, tooltipOffsetY))

	tw := l.label.fullWidth()
	if tw > tooltipMaxW {
		tw = tooltipMaxW
	}
	th := l.label.MinSize().Height
	w := tw + 2*tooltipPadX
	h := th + 2*tooltipPadY

	x, y := l.pos.X, l.pos.Y
	if x+w > size.Width-2 {
		x = size.Width - w - 2 // 右边放不下：向左挪（不越过左边缘）
	}
	if x < 2 {
		x = 2
	}
	if y+h > size.Height-2 {
		y = l.pos.Y - h - tooltipOffsetY*2 // 下面放不下：翻到指针上方
	}
	if y < 2 {
		y = 2
	}

	bg.Resize(fyne.NewSize(w, h))
	bg.Move(fyne.NewPos(x, y))
	label.Resize(fyne.NewSize(tw, th))
	label.Move(fyne.NewPos(x+tooltipPadX, y+tooltipPadY))
}

// ---------------------------------------------------------------------------

// hoverNote 可悬停的单行文本：鼠标停住 tooltipDelay 后弹出备注提示，
// 移开立即收起。备注为空、或所在窗口没有提示图层时，行为与普通文本一致。
type hoverNote struct {
	widget.BaseWidget

	text *plainText
	tip  *tooltipLayer
	note string

	timer *time.Timer
}

func newHoverNote(s string, size float32, note string, tip *tooltipLayer) *hoverNote {
	h := &hoverNote{tip: tip, note: note}
	h.text = newThemedText(s, size, false)
	h.ExtendBaseWidget(h)
	return h
}

func (h *hoverNote) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(h.text)
}

// SetText 更新文本（通话面板的对端名称会随通话状态变化）。
func (h *hoverNote) SetText(s string) { h.text.SetText(s) }

// fullWidth 原始文本（未截断）的像素宽度：布局（markAfterLayout）要用它，
// 拿截断后的宽度会自我迭代、把名字永久截短。
func (h *hoverNote) fullWidth() float32 { return h.text.fullWidth() }

// SetNote 更新备注（同一控件复用于不同联系人时用）。
func (h *hoverNote) SetNote(note string) { h.note = note }

func (h *hoverNote) MouseIn(ev *desktop.MouseEvent) {
	if h.tip == nil || h.note == "" {
		return
	}
	pos := ev.AbsolutePosition
	h.stopTimer()
	h.timer = time.AfterFunc(tooltipDelay, func() {
		fyne.Do(func() { h.tip.show(h.note, pos) })
	})
}

func (h *hoverNote) MouseMoved(*desktop.MouseEvent) {}

func (h *hoverNote) MouseOut() {
	h.stopTimer()
	if h.tip != nil {
		h.tip.hide()
	}
}

func (h *hoverNote) stopTimer() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}
