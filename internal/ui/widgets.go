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
	text *canvas.Text
	full string // 原始文本（截断后仍保留，宽度变大时可还原）
}

func newPlainText(s string, size float32, col color.Color, bold bool) *plainText {
	t := &plainText{full: s}
	t.text = canvas.NewText(s, col)
	t.text.TextSize = size
	t.text.TextStyle = fyne.TextStyle{Bold: bold}
	t.ExtendBaseWidget(t)
	return t
}

func (t *plainText) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(t.text)
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
	if t, ok := o.(*plainText); ok {
		return t.fullWidth()
	}
	return o.MinSize().Width
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

// markAfterLayout 一行文本 + 紧跟其后的小标记（如视频通话的摄像头图标），
// 整体靠左。文本放得下时按**自身宽度**收窄，标记就紧贴在文字右边；放不下时
// 文本按可用宽度截断，标记始终可见。
type markAfterLayout struct{ gap float32 }

func (l markAfterLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	text, mark := objs[0], objs[1]
	m := text.MinSize()
	size := fyne.NewSize(m.Width, m.Height)
	if mark.Visible() {
		mm := mark.MinSize()
		size.Width += l.gap + mm.Width
		size.Height = fyne.Max(size.Height, mm.Height)
	}
	return size
}

func (l markAfterLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	text, mark := objs[0], objs[1]
	markW := float32(0)
	if mark.Visible() {
		mm := mark.MinSize()
		markW = mm.Width + l.gap
		mark.Resize(mm)
	}
	tw := naturalWidth(text)
	if tw > size.Width-markW {
		tw = size.Width - markW
	}
	if tw < 0 {
		tw = 0
	}
	text.Resize(fyne.NewSize(tw, size.Height))
	text.Move(fyne.NewPos(0, 0))
	if mark.Visible() {
		mm := mark.MinSize()
		mark.Move(fyne.NewPos(tw+l.gap, (size.Height-mm.Height)/2))
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
