// Package appicon 应用图标资源（纯 Go，不依赖 fyne）。
//
// 图标真相源放在这里，各消费者各自加工：
//
//   - internal/ui（完整版）：包成 fyne.StaticResource，给窗口与 Fyne 托盘用；
//   - internal/tray（仅托盘无 UI 版）：解码 PNG 缩到托盘尺寸、转 PNG/ICO
//     —— 那条路不能引入 fyne（无 UI 版不链接 fyne/GLFW）；
//   - internal/selfinstall（自安装）：Linux 桌面快捷方式的 Icon 用绿 PNG
//     （ELF 二进制不能内嵌图标，图标靠 .desktop 的 Icon 字段体现）。
//
// 放成独立叶子包是为了不让 internal/ui 与 internal/tray 互相 import。
package appicon

import (
	"bytes"
	"embed"
	"image"
	"image/color"
	"image/png"

	"mti-sip-phone/internal/logx"
)

// 话机图标的 PNG 版（用户从 iconfont 导出的 256×256 原始渲染，绿/红两色），
// 观感与网站一致——托盘/窗口/快捷方式图标直接用它，不再经 SVG 光栅化。
//
//go:embed assets/mti-phone-green.png assets/mti-phone-red.png
var phonePNGs embed.FS

// PhoneGreenPNG 已注册且坐席空闲（绿色）话机图标 PNG 字节。
func PhoneGreenPNG() []byte { return phonePNGBytes("mti-phone-green.png") }

// PhoneRedPNG 未注册 / 注册失败（红色）话机图标 PNG 字节。
func PhoneRedPNG() []byte { return phonePNGBytes("mti-phone-red.png") }

func phonePNGBytes(name string) []byte {
	b, _ := phonePNGs.ReadFile("assets/" + name)
	return b
}

// 托盘/窗口图标配色（REQ-UI-011）：未注册/注册中红色、已注册且空闲绿色、
// 已注册且坐席置忙黄色、已注册且话后整理中蓝色。绿色还用于图标生成失败时的
// 兜底圆点颜色。
const (
	ColorRed    = "#e53935"
	ColorGreen  = "#43a047"
	ColorYellow = "#fbc02d"
	ColorBlue   = "#2196f3"
)

// PhoneYellowPNG 已注册且坐席置忙（黄色）话机图标 PNG 字节：把绿图按 alpha
// 重新着色（形状不变，只换颜色）——仓库里只有绿/红两份原始图，黄色现生成。
func PhoneYellowPNG() []byte {
	return tintedPNG(PhoneGreenPNG(), ColorYellow)
}

// PhoneBluePNG 已注册且坐席处于话后整理态（蓝色）话机图标 PNG 字节。
func PhoneBluePNG() []byte {
	return tintedPNG(PhoneGreenPNG(), ColorBlue)
}

// tintedPNG 把 PNG 的图形按 hex 颜色重新着色（保留 alpha 通道）。
func tintedPNG(src []byte, hex string) []byte {
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return src
	}
	r, g, b := hexRGB(hex)
	bounds := img.Bounds()
	dst := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			dst.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: uint8(a >> 8)})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		logx.Logger().Warn("话机图标着色失败，退回原图", "err", err)
		return src
	}
	return buf.Bytes()
}

// hexRGB 解析 "#rrggbb"（非法输入退回白色）。
func hexRGB(hex string) (uint8, uint8, uint8) {
	if len(hex) == 7 && hex[0] == '#' {
		var v [3]uint8
		ok := true
		for i := 0; i < 3; i++ {
			hi, lo := hexNibble(hex[1+i*2]), hexNibble(hex[2+i*2])
			if hi < 0 || lo < 0 {
				ok = false
				break
			}
			v[i] = uint8(hi<<4 | lo)
		}
		if ok {
			return v[0], v[1], v[2]
		}
	}
	return 0xff, 0xff, 0xff
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// PhoneSVG 返回指定颜色的话机图标（Material Design phone 路径）。
// 接听按钮的白色/黑色话机用它（托盘/窗口图标已改走上面的 PNG）。
func PhoneSVG(color string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="M6.62 10.79a15.05 15.05 0 0 0 6.59 6.59l2.2-2.2a1 1 0 0 1 1.02-.24c1.12.37 2.33.57 3.57.57a1 1 0 0 1 1 1V20a1 1 0 0 1-1 1A17 17 0 0 1 3 4a1 1 0 0 1 1-1h3.5a1 1 0 0 1 1 1c0 1.24.2 2.45.57 3.57a1 1 0 0 1-.24 1.02l-2.21 2.2z"/>
</svg>`
}
