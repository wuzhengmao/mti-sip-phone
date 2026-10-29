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

	"mti-sip-phone/internal/logx"
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
	// bar / barColor / barSeq：**底部居中消息条**模式（toast 用，见 showBar）。
	// 背景色与窗体背景明显区分、贴在窗口下缘居中，比悬停提示醒目得多。
	bar      bool
	barColor color.NRGBA
	barSeq   int
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
	logx.Logger().Debug("显示提示", "text", text, "x", absPos.X, "y", absPos.Y)
	l.bar = false
	l.bg.FillColor = tooltipBgColor()
	l.bg.CornerRadius = 4
	l.absPos = absPos
	l.label.SetText(text)
	l.shownText = text
	l.shown = true
	l.box.Show()
	l.relayout()
}

// showBar 在窗口**下缘居中**显示一条消息条（toast）：背景色由调用方给
// （与窗体背景明显区分），停留 d 后自动收起。
func (l *tooltipLayer) showBar(text string, bgc color.NRGBA, d time.Duration) {
	if text == "" {
		l.hide()
		return
	}
	l.bar = true
	l.barColor = bgc
	l.bg.FillColor = bgc
	l.bg.CornerRadius = 8
	l.label.SetText(text)
	l.shownText = text
	l.shown = true
	l.box.Show()
	l.relayout()
	l.barSeq++
	seq := l.barSeq
	go func() {
		time.Sleep(d)
		fyne.Do(func() {
			if l.barSeq == seq { // 期间又弹了一条：别把新的关掉
				l.hide()
			}
		})
	}()
}

// relayout 立即按图层当前尺寸把底色与文字摆好，并**让画布重绘**。
//
// 两个坑叠在一起，缺一不可（用户实测"一会有一会没"）：
//  1. 图层的尺寸没变时容器不一定重跑布局，而底色与文字的尺寸在第一次显示前
//     是 0——所以这里显式跑一次布局；
//  2. **隐藏的对象从未被画布遍历过，Fyne 的 cache 里没有它的画布归属**
//     （`cache.GetCanvasForObject` 返回 nil）——`Show()` 只改 Hidden 标志，
//     之后所有 `Refresh()` 都找不到画布、不会把画布标脏，于是"日志说显示了、
//     屏幕上一个像素都没变"，直到别的事件（点击、窗口重排）触发一次整帧重绘
//     才突然出现。所以刷新必须走**始终可见的图层控件本身** `l`。
func (l *tooltipLayer) relayout() {
	size := l.box.Size()
	if size.Width <= 0 || size.Height <= 0 {
		size = l.Size()
		if size.Width > 0 && size.Height > 0 {
			l.box.Resize(size)
		}
	}
	tooltipBoxLayout{l}.Layout([]fyne.CanvasObject{l.bg, l.label}, size)
	l.bg.Refresh()
	l.label.Refresh()
	l.box.Refresh()
	l.Refresh()
	canvas.Refresh(l)
	logx.Logger().Debug("提示布局", "layer", l.Size(), "box", size,
		"bgPos", l.bg.Position(), "bgSize", l.bg.Size(), "boxVisible", l.box.Visible())
}

// hide 收起提示（未显示时是空操作）。
func (l *tooltipLayer) hide() {
	if !l.shown {
		return
	}
	l.shown = false
	l.shownText = ""
	l.bar = false
	l.bg.FillColor = tooltipBgColor()
	l.bg.CornerRadius = 4
	l.box.Hide()
	l.Refresh()
	// 同 relayout：提示框此前是可见的（画布缓存里有它），但这里统一走图层
	// 控件本身，避免依赖内部容器的画布归属。
	canvas.Refresh(l)
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
	if l.bar {
		lay.barLayout(bg, label, size)
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

// barLayout 「底部居中消息条」的布局（toast）：横向居中、贴窗口下缘留 6px。
func (lay tooltipBoxLayout) barLayout(bg, label fyne.CanvasObject, size fyne.Size) {
	tw := lay.l.label.fullWidth()
	if maxW := size.Width - 2*tooltipPadX - 16; tw > maxW {
		tw = maxW
	}
	if tw < 0 {
		tw = 0
	}
	th := label.MinSize().Height
	w := tw + 2*tooltipPadX
	h := th + 2*tooltipPadY
	x := (size.Width - w) / 2
	if x < 2 {
		x = 2
	}
	y := size.Height - h - 6
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

	// clipOnly 备注自动取**完整文本**，且只有被截断（显示带 …）时才弹提示：
	// 监听面板的号码/名称行用它（用户要求：被截断时悬停看全文）。
	clipOnly bool

	timer *time.Timer
}

func newHoverNote(s string, size float32, note string, tip *tooltipLayer) *hoverNote {
	h := &hoverNote{tip: tip, note: note}
	h.text = newThemedText(s, size, false)
	h.ExtendBaseWidget(h)
	return h
}

// newHoverClipNote 截断时悬停显示完整文本的文本控件（放得下时不弹提示）。
func newHoverClipNote(s string, size float32, tip *tooltipLayer) *hoverNote {
	h := newHoverNote(s, size, s, tip)
	h.clipOnly = true
	return h
}

func (h *hoverNote) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(h.text)
}

// SetText 更新文本（通话面板的对端名称会随通话状态变化）。
func (h *hoverNote) SetText(s string) {
	h.text.SetText(s)
	if h.clipOnly {
		h.note = s // 提示内容始终是完整文本（截断后 text 里是带 … 的版本）
	}
}

// fullWidth 原始文本（未截断）的像素宽度：布局（markAfterLayout）要用它，
// 拿截断后的宽度会自我迭代、把名字永久截短。
func (h *hoverNote) fullWidth() float32 { return h.text.fullWidth() }

// SetNote 更新备注（同一控件复用于不同联系人时用）。
func (h *hoverNote) SetNote(note string) { h.note = note }

func (h *hoverNote) MouseIn(ev *desktop.MouseEvent) {
	if h.tip == nil || h.note == "" {
		return
	}
	// clipOnly：没被截断时不弹（完整内容已经看得见）
	if h.clipOnly && h.text.text.Text == h.text.full {
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

// hoverIcon 可悬停的图标：鼠标停住 tooltipDelay 后弹出备注，移开立即收起。
// 主窗体顶栏的坐席按钮、监听按钮与自动接听按钮用它（REQ-ACC-028 / REQ-ACC-029）：
// 设置 onTap 后它同时是**可点击**的图标按钮。
type hoverIcon struct {
	widget.BaseWidget

	box    *fyne.Container
	imgBox *fyne.Container
	img    *canvas.Image
	bg     *canvas.Rectangle // 悬停时的圆角背景（与通信录等顶栏按钮一致）
	tip    *tooltipLayer
	note   string
	hit    float32 // 命中区边长（比图标本体大一圈，方便悬停）
	timer  *time.Timer

	onTap    func()        // 点击回调（nil = 纯展示图标，不可点）
	res      fyne.Resource // 当前图标
	offRes   fyne.Resource // 禁用态图标（nil = 禁用不变色）
	disabled bool
	hovered  bool
}

// hoverIconHitPad 命中区比图标本体每边多出的像素：图标本体只有 ~16px，
// 直接用本体做命中区很难指中（用户实测"移上去没反应"）。
const hoverIconHitPad = 6

func newHoverIcon(res fyne.Resource, size float32, tip *tooltipLayer) *hoverIcon {
	h := &hoverIcon{tip: tip, hit: size + 2*hoverIconHitPad, res: res}
	h.img = canvas.NewImageFromResource(res)
	h.img.FillMode = canvas.ImageFillContain
	h.img.SetMinSize(fyne.NewSize(size, size))
	h.imgBox = container.New(centeredIconLayout{}, h.img)
	h.bg = canvas.NewRectangle(color.NRGBA{})
	h.bg.CornerRadius = 6
	h.box = container.NewStack(h.bg, h.imgBox)
	h.ExtendBaseWidget(h)
	return h
}

func (h *hoverIcon) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(h.box)
}

// MinSize 命中区尺寸（图标居中绘制在更大的透明区域里）。
func (h *hoverIcon) MinSize() fyne.Size { return fyne.NewSize(h.hit, h.hit) }

// SetNote 更新悬停提示文案（坐席 displayName，可能为空 = 不弹提示）。
// 提示正在显示时**就地刷新**：坐席状态切换（空闲→置忙等）时指针仍停在按钮上，
// 不刷新的话要移出去再进来才更新（用户实测）。
func (h *hoverIcon) SetNote(note string) {
	old := h.note
	h.note = note
	if h.tip == nil {
		return
	}
	if note == "" {
		h.tip.hide()
		return
	}
	// 只有"当前显示的就是本图标的旧提示"时才刷新，避免抢走别的图标正在显示的提示。
	if h.tip.shown && h.tip.shownText == old {
		h.tip.show(note, h.tip.absPos)
	}
}

// SetOnTap 设置点击回调（设置后图标可点击）。
func (h *hoverIcon) SetOnTap(fn func()) { h.onTap = fn }

// centeredIconLayout 单个对象在区域内**水平+垂直居中**，可再纵向偏移 dy
// （正值下移）。hoverIcon 的图标与命中区/悬停背景的对齐用它——注意不能复用
// shiftedLayout：那个只做纵向居中、横向靠左（图标会偏左）。
type centeredIconLayout struct{ dy float32 }

func (l centeredIconLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	if len(objs) == 0 {
		return fyne.Size{}
	}
	return objs[0].MinSize()
}

func (l centeredIconLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	m := objs[0].MinSize()
	objs[0].Resize(m)
	objs[0].Move(fyne.NewPos((size.Width-m.Width)/2, (size.Height-m.Height)/2+l.dy))
}

// SetIconOffsetY 图标在命中区内纵向微调（正值下移；按钮位置不变）。
func (h *hoverIcon) SetIconOffsetY(dy float32) {
	if h.imgBox == nil {
		return
	}
	h.imgBox.Layout = centeredIconLayout{dy: dy}
	h.imgBox.Refresh()
}

// setHover 悬停时的圆角背景：与顶栏通信录/设置等按钮一致（淡填充 + 1px 轮廓）。
func (h *hoverIcon) setHover(on bool) {
	if h.hovered == on || h.bg == nil {
		return
	}
	h.hovered = on
	// 只有**可点击**的图标才显示悬停轮廓（纯展示图标，如在线座席行的状态图标，
	// 移上去不该出现按钮背景）
	if on && !h.disabled && h.onTap != nil {
		h.bg.FillColor = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x12}
		h.bg.StrokeColor = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x3c}
		h.bg.StrokeWidth = 1
	} else {
		h.bg.FillColor = color.NRGBA{}
		h.bg.StrokeColor = color.NRGBA{}
		h.bg.StrokeWidth = 0
	}
	h.bg.Refresh()
}

// SetResource 换图标（状态色变化时用，如坐席空闲绿 / 置忙黄）。
func (h *hoverIcon) SetResource(res fyne.Resource) {
	if res == nil || h.res == res {
		return
	}
	h.res = res
	h.applyIcon()
}

// SetDisabledResource 禁用态的图标（nil = 禁用只拦点击、不变色）。
func (h *hoverIcon) SetDisabledResource(res fyne.Resource) {
	h.offRes = res
	h.applyIcon()
}

// Disable / Enable 与 Fyne 的可禁用控件同签名（applyMainCallMode 逐项开关用）。
func (h *hoverIcon) Disable() { h.disabled = true; h.setHover(false); h.applyIcon() }
func (h *hoverIcon) Enable()  { h.disabled = false; h.applyIcon() }

// applyIcon 应用当前图标并强制重栅格化（同 compactButton.rerasterIcon：
// 只换 Resource 时 canvas.Image 不一定重画）。
func (h *hoverIcon) applyIcon() {
	res := h.res
	if h.disabled && h.offRes != nil {
		res = h.offRes
	}
	if res == nil || h.img == nil {
		return
	}
	h.img.Resource = res
	h.img.Refresh()
	w, hh := h.img.Size().Width, h.img.Size().Height
	if w > 0 && hh > 0 {
		h.img.Resize(fyne.NewSize(w+1, hh+1))
		h.img.Resize(fyne.NewSize(w, hh))
	}
}

// Tapped 点击（没有 onTap 或已禁用时忽略）。
func (h *hoverIcon) Tapped(*fyne.PointEvent) {
	if h.disabled || h.onTap == nil {
		return
	}
	h.onTap()
}

// Cursor 可点击的图标用超链接光标（指针提示"这里能点"）。
func (h *hoverIcon) Cursor() desktop.Cursor {
	if h.onTap != nil && !h.disabled {
		return desktop.PointerCursor
	}
	return desktop.DefaultCursor
}

func (h *hoverIcon) MouseIn(ev *desktop.MouseEvent) {
	logx.Logger().Debug("悬停图标", "note", h.note)
	h.setHover(true)
	h.startTipTimer(ev.AbsolutePosition)
}

// MouseMoved 指针在图标上移动。**不能是空实现**：Fyne 在控件隐藏后重新
// 显示时不会重置"当前悬停对象"（driver 里 hovered == mouseOver 就只发
// MouseMoved），如果图标出现时鼠标已经在它上面，就永远不会收到 MouseIn——
// 表现为"图标出来了，但移上去不弹提示，重新注册一次又好了"（用户实测）。
// 这里在"还没有在计时/没有显示"时补启动。
func (h *hoverIcon) MouseMoved(ev *desktop.MouseEvent) {
	h.setHover(true)
	if h.tip != nil && h.tip.shown {
		return
	}
	if h.timer != nil {
		return // 已经在倒计时，别反复重置
	}
	h.startTipTimer(ev.AbsolutePosition)
}

// startTipTimer 启动"停住 tooltipDelay 后显示提示"的计时。
func (h *hoverIcon) startTipTimer(pos fyne.Position) {
	if h.tip == nil || h.note == "" {
		return
	}
	if h.timer != nil {
		h.timer.Stop()
	}
	note := h.note
	h.timer = time.AfterFunc(tooltipDelay, func() {
		fyne.Do(func() {
			// 计时到点时确认指针仍在（note 可能已被清空）
			if h.note != "" {
				h.tip.show(note, pos)
			}
			h.timer = nil
		})
	})
}

func (h *hoverIcon) MouseOut() {
	logx.Logger().Debug("离开悬停图标")
	h.setHover(false)
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	if h.tip != nil {
		h.tip.hide()
	}
}

// ---------------------------------------------------------------------------

// 通话面板按钮的悬停提示：面板上只画图标，每个按钮停住 tooltipDelay 后弹出
// 一行文字说明（用户要求：「通话面板的每个按钮移动上去都给一个文字提示」）。

// setTip 给紧凑按钮挂上悬停提示（空文本 = 不提示，并立即收起已弹出的提示）。
func (b *compactButton) setTip(tip *tooltipLayer, text string) {
	old := b.tipText
	b.tip = tip
	b.tipText = text
	if text == "" {
		b.stopTip()
		return
	}
	// 提示正在显示且文案变了（保持/恢复、耳语/监听等）就就地刷新。
	if tip != nil && tip.shown && tip.shownText == old {
		tip.show(text, tip.absPos)
	}
}

func (b *compactButton) MouseIn(ev *desktop.MouseEvent) {
	b.hovered = true
	b.updateHoverBg()
	b.startTipTimer(ev.AbsolutePosition)
}

// MouseMoved 与 hoverIcon 同理：Fyne 在控件 Hide/Show 之间不重置悬停对象，
// 控件重新出现时只会收到 MouseMoved——悬停轮廓也要在这里补上。
func (b *compactButton) MouseMoved(ev *desktop.MouseEvent) {
	if !b.hovered {
		b.hovered = true
		b.updateHoverBg()
	}
	if b.tip == nil || b.tip.shown || b.tipTimer != nil {
		return
	}
	b.startTipTimer(ev.AbsolutePosition)
}

func (b *compactButton) MouseOut() {
	b.hovered = false
	b.updateHoverBg()
	b.stopTip()
}

func (b *compactButton) stopTip() {
	if b.tipTimer != nil {
		b.tipTimer.Stop()
		b.tipTimer = nil
	}
	if b.tip != nil {
		b.tip.hide()
	}
}

func (b *compactButton) startTipTimer(pos fyne.Position) {
	if b.tip == nil || b.tipText == "" {
		return
	}
	if b.tipTimer != nil {
		b.tipTimer.Stop()
	}
	b.tipTimer = time.AfterFunc(tooltipDelay, func() {
		fyne.Do(func() {
			// 计时到点时按钮可能已换了文案（保持/恢复）或被移出面板
			if b.tipText != "" {
				b.tip.show(b.tipText, pos)
			}
			b.tipTimer = nil
		})
	})
}
