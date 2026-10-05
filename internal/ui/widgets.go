package ui

// 内页共用的自绘控件（通话记录页与配置页统一样式）。

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// plainText 单行文本（基于 canvas.Text）。
//
// **不能用 widget.Label 做紧凑排版**：本项目主题把 SizeNameInnerPadding 提到
// 12（给拨号盘大按键用），widget.Label 的 MinSize 因此是"文字高 + 2×12 ≈ 43px"，
// 两行就 86px——塞进几十像素高的记录行里会直接叠在一起。canvas.Text 没有这层
// 内边距。
//
// 代价是 canvas.Text 不会自动截断，所以这里在 Resize（拿到实际分配宽度）时
// 按像素宽度截断并补省略号，避免文字画到相邻控件上。
type plainText struct {
	widget.BaseWidget
	text   *canvas.Text
	full   string // 原始文本（截断后仍保留，宽度变大时可还原）
	themed bool   // 颜色跟随主题前景色：主题（深/浅）切换时重新取色
}

func newPlainText(s string, size float32, col color.Color, bold bool) *plainText {
	t := &plainText{full: s}
	t.text = canvas.NewText(s, col)
	t.text.TextSize = size
	t.text.TextStyle = fyne.TextStyle{Bold: bold}
	t.ExtendBaseWidget(t)
	return t
}

// newThemedText 用**主题前景色**创建自绘文本（长驻控件用：通话面板的
// 状态/时长、悬停提示图层等只建一次；若把创建当时的颜色缓存下来，深/浅色
// 主题切换后会一直是旧颜色、要重启才恢复——用户实测）。
func newThemedText(s string, size float32, bold bool) *plainText {
	t := newPlainText(s, size, theme.ForegroundColor(), bold)
	t.themed = true
	return t
}

// Refresh 跟随主题的文本在每次刷新时重新取前景色（主题切换后驱动会刷新
// 控件树，见 App 里注册的 Settings 监听）。
func (t *plainText) Refresh() {
	if t.themed {
		t.text.Color = theme.ForegroundColor()
		t.text.Refresh()
	}
	t.BaseWidget.Refresh()
}

func (t *plainText) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(t.text)
}

// SetColor 更新文字颜色（注册状态文字要跟着圆点颜色走）。
// 显式指定颜色后不再跟随主题（否则刷新时会被主题前景色顶掉）。
func (t *plainText) SetColor(c color.Color) {
	t.themed = false
	t.text.Color = c
	t.text.Refresh()
}

// SetBold 更新字重。
func (t *plainText) SetBold(bold bool) {
	t.text.TextStyle = fyne.TextStyle{Bold: bold}
	t.text.Refresh()
}

// SetText 更新文本（截断在下次 Resize 时按新宽度重算）。
func (t *plainText) SetText(s string) {
	if t.full == s {
		return
	}
	t.full = s
	t.text.Text = s
	t.text.Refresh()
}

// Resize 先按分到的宽度截断文本，再走正常布局。
func (t *plainText) Resize(size fyne.Size) {
	if clipped := clipToWidth(t.full, t.text.TextSize, t.text.TextStyle, size.Width); clipped != t.text.Text {
		t.text.Text = clipped
		t.text.Refresh()
	}
	t.BaseWidget.Resize(size)
}

// fullWidth 原始文本（未截断）的像素宽度。给"文本 + 紧跟其后的标记"这类布局
// 用：**不能拿截断后的 MinSize 当依据**——布局会先用很小的宽度试算一次，
// 文本被截断后 MinSize 就再也回不去了（表现为名字被永久截短）。
func (t *plainText) fullWidth() float32 {
	return fyne.MeasureText(t.full, t.text.TextSize, t.text.TextStyle).Width
}

// clipToWidth 按像素宽度截断（超出加省略号）。截断结果与输入宽度一一对应，
// 所以不会与布局形成来回抖动。
func clipToWidth(s string, size float32, style fyne.TextStyle, maxW float32) string {
	if maxW <= 0 || fyne.MeasureText(s, size, style).Width <= maxW {
		return s
	}
	runes := []rune(s)
	for i := len(runes) - 1; i > 0; i-- {
		cand := string(runes[:i]) + "…"
		if fyne.MeasureText(cand, size, style).Width <= maxW {
			return cand
		}
	}
	return "…"
}

// pageTopBar 内页顶栏：返回图标 + 标题（配置页与通话记录页统一样式），
// right 非空时放在右端（如记录页的「清空通话记录」按钮）。
func pageTopBar(title string, back func(), right fyne.CanvasObject) *fyne.Container {
	backBtn := widget.NewButtonWithIcon("", theme.NavigateBackIcon(), back)
	backBtn.Importance = widget.LowImportance
	size := fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText)
	titleObj := newPlainText(title, size, theme.ForegroundColor(), true)
	return container.NewBorder(nil, nil, container.NewHBox(backBtn, titleObj), right)
}

// naturalWidth 对象的"原始内容宽度"：plainText 用它未截断的宽度，其它对象
// 退化为 MinSize。布局据此判断放不放得下——用被自己截断过的宽度会自我迭代，
// 结果是名字被永久截短（踩过）。
func naturalWidth(o fyne.CanvasObject) float32 {
	switch t := o.(type) {
	case *plainText:
		return t.fullWidth()
	case *hoverNote: // 可悬停文本（ui_tooltip.go）内部也是 plainText
		return t.fullWidth()
	}
	return o.MinSize().Width
}

// padLeftLayout 左侧留出 pad 的空白、内容占其余宽度（通信录列表行的文字要
// 从左侧缩进一点点，与右侧的按钮留白对称）。
type padLeftLayout struct{ pad float32 }

func (l padLeftLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	m := objs[0].MinSize()
	return fyne.NewSize(m.Width+l.pad, m.Height)
}

func (l padLeftLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	o := objs[0]
	w := size.Width - l.pad
	if w < 0 {
		w = 0
	}
	o.Resize(fyne.NewSize(w, size.Height))
	o.Move(fyne.NewPos(l.pad, 0))
}

// padRightLayout 对象靠左（纵向居中），右侧留出 pad 的空白；MinSize 也把
// 这段空白算进去（用于给滚动条让位）。
type padRightLayout struct{ pad float32 }

func (l padRightLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	m := objs[0].MinSize()
	return fyne.NewSize(m.Width+l.pad, m.Height)
}

func (l padRightLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	o := objs[0]
	m := o.MinSize()
	o.Resize(m)
	o.Move(fyne.NewPos(0, (size.Height-m.Height)/2))
}

// markAfterLayout 一行文本 + 紧跟其后的一串小标记（如常用 ★ 与视频通话的
// 摄像头图标），整体靠左。文本放得下时按**自身宽度**收窄，标记紧贴在文字右边；
// 放不下时文本按可用宽度截断，标记始终可见（一个都不能被挤掉）。
//
// 早期只支持一个标记，通信录要「名称 + ★ + 摄像头」所以改成任意个。
type markAfterLayout struct{ gap float32 }

// marksWidth 可见标记的总宽（含各自前面的间距）。
func (l markAfterLayout) marksWidth(objs []fyne.CanvasObject) float32 {
	var w float32
	for _, o := range objs[1:] {
		if !o.Visible() {
			continue
		}
		w += l.gap + o.MinSize().Width
	}
	return w
}

func (l markAfterLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	text := objs[0]
	m := text.MinSize()
	size := fyne.NewSize(m.Width+l.marksWidth(objs), m.Height)
	for _, o := range objs[1:] {
		if o.Visible() {
			size.Height = fyne.Max(size.Height, o.MinSize().Height)
		}
	}
	return size
}

func (l markAfterLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	text := objs[0]
	markW := l.marksWidth(objs)
	tw := naturalWidth(text)
	if tw > size.Width-markW {
		tw = size.Width - markW
	}
	if tw < 0 {
		tw = 0
	}
	text.Resize(fyne.NewSize(tw, size.Height))
	text.Move(fyne.NewPos(0, 0))
	x := tw
	for _, o := range objs[1:] {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		o.Resize(m)
		o.Move(fyne.NewPos(x+l.gap, (size.Height-m.Height)/2))
		x += l.gap + m.Width
	}
}

// fixedRowsLayout 固定行高的纵向排列（通话记录页与通信录页共用）：
// 每行都拿到整行宽与固定高度，行距 0（行距与分割线由行内部留）。
type fixedRowsLayout struct{ rowHeight float32 }

func (l fixedRowsLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	n := 0
	for _, o := range objs {
		if o.Visible() {
			n++
		}
	}
	return fyne.NewSize(0, float32(n)*l.rowHeight)
}

func (l fixedRowsLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		o.Resize(fyne.NewSize(size.Width, l.rowHeight))
		o.Move(fyne.NewPos(0, y))
		y += l.rowHeight
	}
}

// fixedRowLayout 紧凑列表行：底色铺满，内容占上方（下方留出 gap + sep），
// 分割线贴底。**不用 container.NewBorder 拼**——borderLayout 会在中间与底部
// 之间额外加 theme.Padding()，行距就不是指定的值了（见 CLAUDE.md 的 vPad 条）。
type fixedRowLayout struct{ gap, sep float32 }

func (l fixedRowLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	m := objs[1].MinSize()
	return fyne.NewSize(m.Width, m.Height+l.gap+l.sep)
}

func (l fixedRowLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	contentH := size.Height - l.gap - l.sep
	bg, content, sep := objs[0], objs[1], objs[2]
	bg.Resize(size)
	bg.Move(fyne.NewPos(0, 0))
	content.Resize(fyne.NewSize(size.Width, contentH))
	content.Move(fyne.NewPos(0, 0))
	sep.Resize(fyne.NewSize(size.Width, l.sep))
	sep.Move(fyne.NewPos(0, contentH+l.gap))
}

// nameNoteLayout 通信录行的第二行：名称 + 备注。名称优先拿自然宽度
// （最多占整行 maxRatio），剩下的宽度给备注，放不下就各自截断（plainText 负责
// 补省略号）——备注再长也不会把名称挤没，也不会画到相邻控件上。
type nameNoteLayout struct {
	gap      float32
	maxRatio float32 // 名称最多占整行宽的比例（0 = 不限）
}

func (l nameNoteLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	name, note := objs[0], objs[1]
	m := name.MinSize()
	size := fyne.NewSize(m.Width, m.Height)
	if note.Visible() {
		nm := note.MinSize()
		size.Width += l.gap + nm.Width
		size.Height = fyne.Max(size.Height, nm.Height)
	}
	return size
}

func (l nameNoteLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	name, note := objs[0], objs[1]
	noteGap := float32(0)
	if note.Visible() {
		noteGap = l.gap
	}
	limit := size.Width - noteGap
	if l.maxRatio > 0 && limit > size.Width*l.maxRatio {
		limit = size.Width * l.maxRatio
	}
	nw := naturalWidth(name)
	if nw > limit {
		nw = limit
	}
	if nw < 0 {
		nw = 0
	}
	name.Resize(fyne.NewSize(nw, size.Height))
	name.Move(fyne.NewPos(0, 0))
	if note.Visible() {
		rest := size.Width - nw - noteGap
		if rest < 0 {
			rest = 0
		}
		note.Resize(fyne.NewSize(rest, size.Height))
		note.Move(fyne.NewPos(nw+noteGap, 0))
	}
}

// offsetLayout 把对象整体平移 (dx, dy)：顶栏按钮"上面多留一点空隙"，
// 或把按钮右边缘与下方输入框的边框对齐（圆角/边框的渲染差 1px）。
type offsetLayout struct{ dx, dy float32 }

func (l offsetLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	m := objs[0].MinSize()
	return fyne.NewSize(m.Width, m.Height+l.dy)
}

func (l offsetLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	o := objs[0]
	m := o.MinSize()
	o.Resize(m)
	o.Move(fyne.NewPos((size.Width-m.Width)/2+l.dx, l.dy))
}
