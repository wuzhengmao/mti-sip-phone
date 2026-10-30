package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

// statusDot 注册状态圆点：大圆点表示状态，颜色区分。
// 绿色=注册成功，黄色=注册中，红色=注册失败，灰色=未注册/未配置。
type statusDot struct {
	widget.BaseWidget
	state string
	// 已注册时的坐席状态（free/busy/work/leave）：busy/leave 黄色、work 蓝色，
	// 与托盘/窗口图标的口径一致（REQ-ACC-034）。
	busy   bool // 置忙（isBusy）
	isWork bool // 话后整理中（isWork）
	leave  bool // 离席（isLeave）
}

// 状态颜色（与托盘/应用图标同口径，REQ-UI-011 修订）：
// 绿=已注册且空闲、黄=已注册且坐席置忙/离席、蓝=整理中、红=注册中/注册失败、
// 灰=未注册/未配置。
var dotColors = map[string]color.Color{
	"registered":   color.NRGBA{R: 0x2e, G: 0xcc, B: 0x40, A: 0xff}, // 绿
	"registering":  color.NRGBA{R: 0xe5, G: 0x39, B: 0x35, A: 0xff}, // 红（托盘图标注册中也是红）
	"failed":       color.NRGBA{R: 0xe5, G: 0x39, B: 0x35, A: 0xff}, // 红
	"unregistered": color.NRGBA{R: 0x9e, G: 0x9e, B: 0x9e, A: 0xff}, // 灰
}

// busyColor 已注册且坐席置忙/离席的颜色（与托盘黄色图标一致：#fbc02d）。
var busyColor = color.NRGBA{R: 0xfb, G: 0xc0, B: 0x2d, A: 0xff}

// isWorkColor 已注册且坐席话后整理中的颜色（蓝色，与托盘蓝色图标一致：#2196f3）。
var isWorkColor = color.NRGBA{R: 0x21, G: 0x96, B: 0xf3, A: 0xff}

func newStatusDot(state string) *statusDot {
	d := &statusDot{state: state}
	d.ExtendBaseWidget(d)
	return d
}

// color 当前显示颜色：已注册时按坐席状态（离席/置忙黄、整理蓝），其余按注册状态。
func (d *statusDot) color() color.Color {
	if d.state == "registered" {
		switch {
		case d.leave:
			return busyColor
		case d.isWork:
			return isWorkColor
		case d.busy:
			return busyColor
		}
	}
	if c, ok := dotColors[d.state]; ok {
		return c
	}
	return dotColors["unregistered"]
}

// setAgentStatus 更新坐席状态（free/busy/work/leave，只影响已注册时的颜色）。
func (d *statusDot) setAgentStatus(status string) {
	busy := status == "busy"
	isWork := status == "work"
	leave := status == "leave"
	if d.busy == busy && d.isWork == isWork && d.leave == leave {
		return
	}
	d.busy, d.isWork, d.leave = busy, isWork, leave
	d.Refresh()
}

// setState 更新状态并重绘。
func (d *statusDot) setState(state string) {
	if _, ok := dotColors[state]; !ok {
		state = "unregistered"
	}
	if d.state == state {
		return
	}
	d.state = state
	d.Refresh()
}

// CreateRenderer 渲染为圆形。
func (d *statusDot) CreateRenderer() fyne.WidgetRenderer {
	c := canvas.NewCircle(d.color())
	return &statusDotRenderer{dot: d, circle: c}
}

type statusDotRenderer struct {
	dot    *statusDot
	circle *canvas.Circle
}

func (r *statusDotRenderer) Layout(size fyne.Size) {
	r.circle.Resize(size)
}

func (r *statusDotRenderer) MinSize() fyne.Size {
	return fyne.NewSize(16, 16)
}

func (r *statusDotRenderer) Refresh() {
	r.circle.FillColor = r.dot.color()
	r.circle.Refresh()
}

func (r *statusDotRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.circle}
}

func (r *statusDotRenderer) Destroy() {}

// statusRowLayout 状态行布局：圆点 + 2px 固定间距 + 状态文字。
// 自定义布局的原因：HBox 项间距由主题 Padding 决定，这里固定 2px
// （用户要求圆点与文字紧贴）。
type statusRowLayout struct{}

// statusIconGapFirst 「状态文字」与第一个图标之间的间距：图标命中区比图形大一圈
// （hoverIcon 的 hit pad），视觉间距取与"图标与图标之间"相同的值（用户要求
// 座席按钮离「已注册」别太近）。
const statusIconGapFirst float32 = 3 + 2*hoverIconHitPad

func (statusRowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	dot, label := objects[0], objects[1]
	dotSize := fyne.NewSize(16, 16)
	dot.Resize(dotSize)
	dot.Move(fyne.NewPos(2, (size.Height-dotSize.Height)/2)) // 圆点右移 2px

	// 第三个及之后的对象（可选）：坐席按钮、班长模式的监听按钮（REQ-ACC-028/029），
	// 不显示时完全不占位。多个图标按顺序紧跟在文字后面，间距 3px。
	var icons []fyne.CanvasObject
	for _, o := range objects[2:] {
		if o.Visible() {
			icons = append(icons, o)
		}
	}
	iconW := float32(0)
	for i, icon := range icons {
		if i == 0 {
			iconW += statusIconGapFirst
		} else {
			iconW += 3
		}
		iconW += icon.MinSize().Width
	}

	// 可用宽度扣掉圆点、间距与（可见的）图标。
	// ⚠️ 文字宽度必须用**原始文本宽度**（naturalWidth），不能用 label.MinSize()：
	// 布局会先用较小宽度试算，plainText 一旦被截成「...」，MinSize 就只剩省略号
	// 的宽度，之后再也恢复不了（踩过：加了坐席图标后「已注册」变成「...」）。
	const gap = 2
	avail := size.Width - dotSize.Width - gap - 2 - iconW
	if avail < 0 {
		avail = 0
	}
	labelW := naturalWidth(label)
	if labelW > avail {
		labelW = avail
	}
	labelH := label.MinSize().Height
	label.Resize(fyne.NewSize(labelW, labelH))
	// 文字起点 = 圆点的 2px 左偏移 + 圆点直径 + 间距。**别漏掉那 2px**：
	// 只写 dotSize.Width+gap 的话文字实际是贴着圆点的（圆点先右移了 2px）。
	// 文字比圆点中心再上移 1px（视觉重心：汉字比圆圈偏下，用户要求；圆点不动）
	label.Move(fyne.NewPos(2+dotSize.Width+gap, (size.Height-labelH)/2-1))

	x := label.Position().X + label.Size().Width
	for i, icon := range icons {
		m := icon.MinSize()
		icon.Resize(m)
		if i == 0 {
			x += statusIconGapFirst
		} else {
			x += 3
		}
		// 图标命中区比本体大一圈（hoverIcon）：往左挪回命中区的内边距，
		// 保证视觉上图标仍紧跟在文字/前一个图标后面 3px。
		drawX := x
		if _, ok := icon.(*hoverIcon); ok {
			drawX -= hoverIconHitPad
		}
		icon.Move(fyne.NewPos(drawX, (size.Height-m.Height)/2))
		x += m.Width
	}
}

func (statusRowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	dot := fyne.NewSize(16, 16)
	label := objects[1]
	// 同样用原始文本宽度：MinSize 不能随"当前是否被截断"变化，否则一旦截短
	// 就再也拿不回足够的宽度（见 Layout 的说明）。
	labelW := naturalWidth(label)
	labelH := label.MinSize().Height
	w := 2 + dot.Width + 2 + labelW
	h := fyne.Max(dot.Height, labelH)
	first := true
	for _, o := range objects[2:] {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		gap := float32(3)
		if first {
			gap = statusIconGapFirst
			first = false
		}
		w += gap + m.Width
		h = fyne.Max(h, m.Height)
	}
	return fyne.NewSize(w, h)
}
