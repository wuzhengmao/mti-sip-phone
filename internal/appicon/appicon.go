// Package appicon 应用图标的原始 SVG 与光栅化（纯 Go，不依赖 fyne）。
//
// 图标只有一份真相源放在这里，三个消费者各自加工：
//
//   - internal/ui（完整版）：包成 fyne.StaticResource，给窗口与 Fyne 托盘用；
//   - internal/tray（仅托盘无 UI 版）：光栅化成 PNG/ICO 交给 fyne.io/systray
//     —— 那条路不能引入 fyne（无 UI 版不链接 fyne/GLFW）；
//   - internal/selfinstall（自安装）：Linux 桌面快捷方式的 Icon 需要 PNG
//     （ELF 二进制不能内嵌图标，图标靠 .desktop 的 Icon 字段体现）。
//
// 光栅化统一走本包的 Rasterize（与 Fyne 内部同一套 oksvg/rasterx 算法）。
//
// 放成独立叶子包是为了不让 internal/ui 与 internal/tray 互相 import。
package appicon

import (
	"errors"
	"image"
	"strings"

	"github.com/fyne-io/oksvg"
	"github.com/srwiley/rasterx"
)

// 托盘/窗口图标配色（REQ-UI-011）：未注册红色、已注册绿色。
const (
	ColorRed   = "#e53935"
	ColorGreen = "#43a047"
)

// PhoneSVG 返回指定颜色的话机图标（Material Design phone 路径）。
// 颜色写成 fill 属性而不是 currentColor：SVG 渲染器（Fyne 的 oksvg、
// 无 UI 版的光栅化）都按字面取色，不依赖主题替换。
func PhoneSVG(color string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="M6.62 10.79a15.05 15.05 0 0 0 6.59 6.59l2.2-2.2a1 1 0 0 1 1.02-.24c1.12.37 2.33.57 3.57.57a1 1 0 0 1 1 1V20a1 1 0 0 1-1 1A17 17 0 0 1 3 4a1 1 0 0 1 1-1h3.5a1 1 0 0 1 1 1c0 1.24.2 2.45.57 3.57a1 1 0 0 1-.24 1.02l-2.21 2.2z"/>
</svg>`
}

// PhoneRedSVG 未注册状态的托盘图标（红色话机）。
func PhoneRedSVG() string { return PhoneSVG(ColorRed) }

// PhoneGreenSVG 已注册状态的托盘图标（绿色话机）。
func PhoneGreenSVG() string { return PhoneSVG(ColorGreen) }

// Rasterize 把图标 SVG 渲染成 size×size 的位图。视图框（0 0 24 24，正方形）
// 等比缩放到目标方框，正方形之间不会变形。
func Rasterize(svg string, size float64) (image.Image, error) {
	icon, err := oksvg.ReadIconStream(strings.NewReader(svg))
	if err != nil {
		return nil, err
	}
	icon.SetTarget(0, 0, size, size)

	img := image.NewRGBA(image.Rect(0, 0, int(size), int(size)))
	scanner := rasterx.NewScannerGV(int(size), int(size), img, img.Bounds())
	raster := rasterx.NewDasher(int(size), int(size), scanner)
	if err := drawSVG(icon, raster); err != nil {
		return nil, err
	}
	return img, nil
}

// drawSVG 渲染并兜住渲染器内部的 panic（第三方 SVG 解析/绘制对畸形输入会
// panic，Fyne 侧同样做了这层保护）。
func drawSVG(icon *oksvg.SvgIcon, raster *rasterx.Dasher) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("渲染 SVG 时崩溃")
		}
	}()
	icon.Draw(raster, 1)
	return nil
}
