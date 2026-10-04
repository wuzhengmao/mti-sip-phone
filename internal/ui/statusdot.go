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
}

// 状态颜色。
var dotColors = map[string]color.Color{
	"registered":   color.NRGBA{R: 0x2e, G: 0xcc, B: 0x40, A: 0xff}, // 绿
	"registering":  color.NRGBA{R: 0xf1, G: 0xc4, B: 0x0f, A: 0xff}, // 黄
	"failed":       color.NRGBA{R: 0xe5, G: 0x39, B: 0x35, A: 0xff}, // 红
	"unregistered": color.NRGBA{R: 0x9e, G: 0x9e, B: 0x9e, A: 0xff}, // 灰
}

func newStatusDot(state string) *statusDot {
	d := &statusDot{state: state}
	d.ExtendBaseWidget(d)
	return d
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
	c := canvas.NewCircle(dotColors[d.state])
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
	r.circle.FillColor = dotColors[r.dot.state]
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

func (statusRowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	dot, label := objects[0], objects[1]
	dotSize := fyne.NewSize(16, 16)
	dot.Resize(dotSize)
	dot.Move(fyne.NewPos(2, (size.Height-dotSize.Height)/2)) // 圆点右移 2px

	labelSize := label.MinSize()
	// 文字起点 = 圆点的 2px 左偏移 + 圆点直径 + 间距。**别漏掉那 2px**：
	// 只写 dotSize.Width+gap 的话文字实际是贴着圆点的（圆点先右移了 2px）。
	const gap = 2
	label.Resize(fyne.NewSize(fyne.Min(labelSize.Width, size.Width-dotSize.Width-gap-2), labelSize.Height))
	// 文字比圆点中心再上移 1px（视觉重心：汉字比圆圈偏下，用户要求；圆点不动）
	label.Move(fyne.NewPos(2+dotSize.Width+gap, (size.Height-labelSize.Height)/2-1))
}

func (statusRowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	dot := fyne.NewSize(16, 16)
	label := objects[1].MinSize()
	return fyne.NewSize(2+dot.Width+2+label.Width, fyne.Max(dot.Height, label.Height))
}
