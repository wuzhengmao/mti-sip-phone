package ui

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"regexp"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"golang.org/x/image/draw"
)

// 图标资源：用户从 iconfont 导出的 256×256 PNG（白色前景，主持人图标为蓝色），
// 观感与网站一致。**直接内嵌 PNG，不再经 SVG 光栅化**——图标库的原始渲染就是
// 最终效果。
//
// 命名对照（icons/ 里的原始文件已重命名到这里）：
//
//	phone-out / phone-in / video-out / video-in   呼叫·接听·方向图标
//	contacts / history                           通信录 / 通话记录入口
//	host                                         会议 owner 标记
//	consult / consult-transfer / threeway / transfer
//	add-member / remove-member / monitor / whisper / intercept / intervene
//	conference / conference-video                普通会议 / 视频会议
//
//go:embed assets/*.png
var iconPNGFiles embed.FS

// newIconPNG 从内嵌 PNG 生成 Fyne 资源（文件名不含 .png 后缀）。
func newIconPNG(name string) fyne.Resource {
	b, err := iconPNGFiles.ReadFile("assets/" + name + ".png")
	if err != nil {
		b = nil
	}
	return fyne.NewStaticResource(name+".png", b)
}

// newIconPNGPadded 把 PNG 图标按 contentRatio（0~1）缩到画布中央、四周留透明边，
// 让图标"视觉变小"：iconfont 导出的 PNG 图形几乎铺满 256 画布，比 Fyne 内置的
// SVG 线稿图标（如设置齿轮）显大一圈；导航按钮这类要与内置图标对齐的场景用它。
func newIconPNGPadded(name string, contentRatio float64) fyne.Resource {
	return paddedPNG(name, contentRatio, nil)
}

// newIconPNGPaddedTinted 同 newIconPNGPadded，但把图形染成指定颜色（保留 alpha）。
func newIconPNGPaddedTinted(name string, contentRatio float64, c color.NRGBA) fyne.Resource {
	return paddedPNG(name, contentRatio, &c)
}

// disabledPNG 生成"禁用态"图标：留白缩放后按**当前主题的禁用色**染色。
//
// Fyne 的 buttonRenderer 只在 `Disabled() && svg.IsResourceSVG(icon)` 时换用
// theme.NewDisabledResource——**位图（PNG）图标不会自动变灰**（上游 TODO），
// 所以顶栏的通信录/通话记录（PNG）要在禁用时手动换这一份，观感才与设置按钮
// （SVG，自动变灰）一致。
func disabledPNG(name string, contentRatio float64) fyne.Resource {
	c := theme.Color(theme.ColorNameDisabled)
	n, ok := c.(color.NRGBA)
	if !ok {
		r, g, b, a := c.RGBA()
		n = color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
	}
	return paddedPNG(name, contentRatio, &n)
}

func paddedPNG(name string, contentRatio float64, tint *color.NRGBA) fyne.Resource {
	b, err := iconPNGFiles.ReadFile("assets/" + name + ".png")
	if err != nil {
		return fyne.NewStaticResource(name+".png", b)
	}
	src, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return fyne.NewStaticResource(name+".png", b)
	}
	sb := src.Bounds()
	size := sb.Dx()
	target := int(float64(size) * contentRatio)
	if target <= 0 || target >= size {
		return fyne.NewStaticResource(name+".png", b)
	}
	small := image.NewNRGBA(image.Rect(0, 0, target, target))
	draw.CatmullRom.Scale(small, small.Bounds(), src, sb, draw.Over, nil)
	dst := image.NewNRGBA(sb)
	off := (size - target) / 2
	for y := 0; y < target; y++ {
		for x := 0; x < target; x++ {
			px := small.NRGBAAt(x, y)
			if tint != nil && px.A > 0 {
				px = color.NRGBA{R: tint.R, G: tint.G, B: tint.B, A: px.A}
			}
			dst.SetNRGBA(off+x, off+y, px)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return fyne.NewStaticResource(name+".png", b)
	}
	return fyne.NewStaticResource(name+".png", out.Bytes())
}

// newIconPNGFit 把 PNG 图标裁到内容外接框后**等比放大铺满画布**（四周留
// margin 比例），用于统一"原始图片有效区域偏小"的图标（三方图标）：不裁掉内容，
// 只把空白边去掉再撑满，观感大小与其它图标看齐。
func newIconPNGFit(name string, margin float64) fyne.Resource {
	b, err := iconPNGFiles.ReadFile("assets/" + name + ".png")
	if err != nil {
		return fyne.NewStaticResource(name+".png", b)
	}
	src, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return fyne.NewStaticResource(name+".png", b)
	}
	bounds := src.Bounds()
	minX, minY, maxX, maxY := contentBox(src)
	bw, bh := maxX-minX+1, maxY-minY+1
	if bw <= 0 || bh <= 0 {
		return fyne.NewStaticResource(name+".png", b)
	}
	size := bounds.Dx()
	target := float64(size) * (1 - 2*margin)
	scale := math.Min(target/float64(bw), target/float64(bh))
	w := int(math.Round(float64(bw) * scale))
	h := int(math.Round(float64(bh) * scale))
	if w <= 0 || h <= 0 {
		return fyne.NewStaticResource(name+".png", b)
	}
	crop := image.NewNRGBA(image.Rect(0, 0, bw, bh))
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			crop.SetNRGBA(x, y, nrgbaAt(src, minX+x, minY+y))
		}
	}
	scaled := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), crop, crop.Bounds(), draw.Over, nil)
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	offX, offY := (size-w)/2, (size-h)/2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.SetNRGBA(offX+x, offY+y, scaled.NRGBAAt(x, y))
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return fyne.NewStaticResource(name+".png", b)
	}
	return fyne.NewStaticResource(name+".png", out.Bytes())
}

// contentBox 非透明像素（alpha > 0x1000）的外接框（含端点）。
func contentBox(img image.Image) (minX, minY, maxX, maxY int) {
	b := img.Bounds()
	minX, minY, maxX, maxY = b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0x1000 {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	return
}

// nrgbaAt 取非预乘 RGBA 像素（不同 PNG 色彩类型都转成 NRGBA 再处理）。
func nrgbaAt(img image.Image, x, y int) color.NRGBA {
	c := img.At(x, y)
	return color.NRGBAModel.Convert(c).(color.NRGBA)
}

// newCircleMinusIcon 生成"圆圈里一个减号"的白色图标（强制挂断 / 移除成员用）：
// 圆环 + 中间横条，2×2 超采样抗锯齿。白色图形，无底色按钮按强调色着色。
func newCircleMinusIcon() fyne.Resource {
	const (
		size = 64
		ss   = 2
	)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size-1) / 2
	rOut := c - 1
	rIn := rOut - 5
	barW := rIn * 1.55
	barH := 5.0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			cov := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/float64(ss) - c - 0.5
					py := float64(y) + (float64(sy)+0.5)/float64(ss) - c - 0.5
					r := math.Hypot(px, py)
					inRing := r <= rOut && r >= rIn
					inBar := math.Abs(px) <= barW/2 && math.Abs(py) <= barH/2
					if inRing || inBar {
						cov++
					}
				}
			}
			if cov > 0 {
				img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255,
					A: uint8(cov * 255 / (ss * ss))})
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return fyne.NewStaticResource("circle-minus.png", nil)
	}
	return fyne.NewStaticResource("circle-minus.png", out.Bytes())
}

// svgFillRe 匹配自绘 SVG 里的纯色填充（我们的图标全是 fill="#rrggbb"）。
var svgFillRe = regexp.MustCompile(`fill="#[0-9a-fA-F]{6}"`)

// tintCache 着色结果缓存：图标着色在状态变化（滚轮每一步、静音开关）时都会触发，
// PNG 重新编码 256×256 有毫秒级开销，按「资源名 + 颜色」缓存一份。
var tintCache sync.Map // string -> fyne.Resource

// tintIcon 把图标染成指定颜色：PNG 按 alpha 重着色，SVG 把 fill 换成目标色。
// 无底色按钮（outline）用它把图标画成"原来的按钮底色"（用户要求）。
func tintIcon(res fyne.Resource, c color.NRGBA) fyne.Resource {
	if res == nil {
		return nil
	}
	key := fmt.Sprintf("%s|%02x%02x%02x", res.Name(), c.R, c.G, c.B)
	if v, ok := tintCache.Load(key); ok {
		return v.(fyne.Resource)
	}
	data := res.Content()
	var out fyne.Resource
	if bytes.HasPrefix(data, []byte("\x89PNG")) {
		src, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return res
		}
		b := src.Bounds()
		dst := image.NewNRGBA(b)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				_, _, _, a := src.At(x, y).RGBA()
				if a == 0 {
					continue
				}
				dst.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: uint8(a >> 8)})
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, dst); err != nil {
			return res
		}
		out = fyne.NewStaticResource("tint-"+res.Name(), buf.Bytes())
	} else {
		hex := fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
		s := svgFillRe.ReplaceAllString(string(data), `fill="`+hex+`"`)
		out = fyne.NewStaticResource("tint-"+res.Name(), []byte(s))
	}
	tintCache.Store(key, out)
	return out
}

// newIconPNGTinted 把白色 PNG 图标染成指定颜色（保留原 alpha，抗锯齿过渡不变）。
// 通话记录方向图标的"未接通 = 红"用它把白色呼入/呼出图标染红。
func newIconPNGTinted(name string, c color.NRGBA) fyne.Resource {
	b, err := iconPNGFiles.ReadFile("assets/" + name + ".png")
	if err != nil {
		return fyne.NewStaticResource(name+".png", b)
	}
	src, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return fyne.NewStaticResource(name+".png", b)
	}
	bounds := src.Bounds()
	dst := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, a := src.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: uint8(a >> 8)})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return fyne.NewStaticResource(name+".png", b)
	}
	return fyne.NewStaticResource(name+".png", out.Bytes())
}
