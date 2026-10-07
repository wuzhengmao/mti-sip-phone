package tray

import (
	"bytes"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"

	"github.com/fyne-io/oksvg"
	"github.com/srwiley/rasterx"

	"mti-sip-phone/internal/logx"
)

// iconSize 托盘图标的像素尺寸。systray 在各种 DPI 下自行缩放，64 与 Fyne
// 侧完全一致（fyne.io/fyne/v2 的 driver 用 64 渲染后交给同一个 systray）。
const iconSize = 64

// loadIcon 把 SVG 光栅化并转成本平台 systray 要的图标格式：
// Windows 要 ICO、其余平台（Linux 的 StatusNotifier）要 PNG。
//
// 光栅化用的是 Fyne 内部同一套库（fyne-io/oksvg + srwiley/rasterx），观感与
// 桌面版托盘一致。任何一步失败都退化为纯色圆点并记日志——托盘图标缺失会让
// 用户连右键菜单都点不到（没有别的退出入口），必须保证有东西显示。
func loadIcon(svg string, colorHex string) []byte {
	img, err := rasterizeSVG(svg)
	if err != nil {
		logx.Logger().Warn("托盘图标渲染失败，退化为纯色圆点", "err", err)
		img = fallbackIcon(colorHex)
	}
	data, err := encodeIcon(img)
	if err != nil {
		logx.Logger().Warn("托盘图标编码失败，退化为纯色圆点", "err", err)
		if data, err = encodeIcon(fallbackIcon(colorHex)); err != nil {
			logx.Logger().Error("托盘图标完全不可用（托盘可能不显示图标）", "err", err)
			return nil
		}
	}
	return data
}

// rasterizeSVG 把 SVG 渲染成 iconSize×iconSize 的位图。
func rasterizeSVG(svg string) (image.Image, error) {
	icon, err := oksvg.ReadIconStream(strings.NewReader(svg))
	if err != nil {
		return nil, err
	}
	// 视图框（0 0 24 24，正方形）等比缩放到目标方框：SetTarget 会把 viewBox
	// 映射到给定矩形，正方形之间不会变形。
	icon.SetTarget(0, 0, iconSize, iconSize)

	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	scanner := rasterx.NewScannerGV(iconSize, iconSize, img, img.Bounds())
	raster := rasterx.NewDasher(iconSize, iconSize, scanner)
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

// fallbackIcon 兜底图标：纯色实心圆（渲染失败时至少让托盘有条目可点）。
func fallbackIcon(colorHex string) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	clr, err := parseHexColor(colorHex)
	if err != nil {
		clr = color.RGBA{R: 0x43, G: 0xa0, B: 0x47, A: 0xff}
	}
	r := float64(iconSize) / 2
	cx, cy := r-0.5, r-0.5
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy <= (r-2)*(r-2) {
				img.Set(x, y, clr)
			}
		}
	}
	return img
}

// parseHexColor 解析 "#rrggbb"（图标配色是代码里的常量，格式固定）。
func parseHexColor(s string) (color.Color, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return nil, errors.New("颜色格式不是 #rrggbb")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return color.RGBA{R: b[0], G: b[1], B: b[2], A: 0xff}, nil
}

// encodePNG PNG 编码（Linux 及其它平台用）。
func encodePNG(img image.Image) ([]byte, error) {
	buf := &bytes.Buffer{}
	if err := png.Encode(buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
