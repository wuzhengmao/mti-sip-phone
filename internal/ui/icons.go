package ui

import (
	"fyne.io/fyne/v2"
)

// 托盘图标：话机形状（Material Design phone 图标 path），
// 未注册为红色，注册成功后为绿色（REQ-UI-011）。
// SVG 静态资源由 Fyne driver 渲染为托盘位图。

func phoneIconSVG(color string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="M6.62 10.79a15.05 15.05 0 0 0 6.59 6.59l2.2-2.2a1 1 0 0 1 1.02-.24c1.12.37 2.33.57 3.57.57a1 1 0 0 1 1 1V20a1 1 0 0 1-1 1A17 17 0 0 1 3 4a1 1 0 0 1 1-1h3.5a1 1 0 0 1 1 1c0 1.24.2 2.45.57 3.57a1 1 0 0 1-.24 1.02l-2.21 2.2z"/>
</svg>`
}

var (
	// IconPhoneRed 未注册状态托盘图标（红色话机）。
	IconPhoneRed = fyne.NewStaticResource("phone-red.svg", []byte(phoneIconSVG("#e53935")))
	// IconPhoneGreen 已注册状态托盘图标（绿色话机）。
	IconPhoneGreen = fyne.NewStaticResource("phone-green.svg", []byte(phoneIconSVG("#43a047")))
	// IconPhoneBlack 黑色话机（接听按钮图标，前景色黑色）。
	IconPhoneBlack = fyne.NewStaticResource("phone-black.svg", []byte(phoneIconSVG("#1a1a1a")))
)
