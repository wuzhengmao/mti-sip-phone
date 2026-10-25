package tray

import (
	"bytes"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"

	"golang.org/x/image/draw"

	"mti-sip-phone/internal/logx"
)

// iconSize 托盘图标的像素尺寸。systray 在各种 DPI 下自行缩放，64 与 Fyne
// 侧完全一致（fyne.io/fyne/v2 的 driver 用 64 渲染后交给同一个 systray）。
const iconSize = 64

// loadIcon 把话机图标 PNG（iconfont 导出的 256×256）解码、缩放到 iconSize，
// 再转成本平台 systray 要的图标格式：Windows 要 ICO、其余平台要 PNG。
//
// 任何一步失败都退化为纯色圆点并记日志——托盘图标缺失会让用户连右键菜单都
// 点不到（没有别的退出入口），必须保证有东西显示。
func loadIcon(pngBytes []byte, colorHex string) []byte {
	img, err := decodeAndResize(pngBytes)
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

// decodeAndResize 解码 PNG 并用 CatmullRom 缩放到 iconSize×iconSize（256→64
// 平滑缩小，托盘小图标边缘不发虚）。
func decodeAndResize(data []byte) (image.Image, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	if b.Dx() == iconSize && b.Dy() == iconSize {
		return src, nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst, nil
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
