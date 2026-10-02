package video

import "image"

// 摄像头原始帧 → I420 的转换。
//
// 摄像头驱动给出的像素格式五花八门（Windows DirectShow 多为 NV12/YUY2，
// Linux V4L2 多为 YUYV/MJPEG），编码器只吃 I420，统一在这里归一化。

// ycbcrToI420 把 image.YCbCr 转成 I420。
// 4:2:0 直接搬；4:2:2 需要色度行两两平均；4:4:4 需要 2x2 平均。
func ycbcrToI420(src *image.YCbCr) *YUV420P {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w%2 != 0 {
		w--
	}
	if h%2 != 0 {
		h--
	}
	dst := NewYUV420P(w, h)

	// Y 平面（逐行拷贝，源可能有步长填充）
	for y := 0; y < h; y++ {
		srcRow := src.Y[(y+b.Min.Y-src.Rect.Min.Y)*src.YStride+(b.Min.X-src.Rect.Min.X):]
		copy(dst.Y[y*dst.StrideY:(y+1)*dst.StrideY], srcRow[:w])
	}

	cw, ch := w/2, h/2
	switch src.SubsampleRatio {
	case image.YCbCrSubsampleRatio420:
		for y := 0; y < ch; y++ {
			srcRow := src.Cb[(y+b.Min.Y/2-src.Rect.Min.Y/2)*src.CStride+(b.Min.X/2-src.Rect.Min.X/2):]
			copy(dst.U[y*dst.StrideU:(y+1)*dst.StrideU], srcRow[:cw])
			srcRow = src.Cr[(y+b.Min.Y/2-src.Rect.Min.Y/2)*src.CStride+(b.Min.X/2-src.Rect.Min.X/2):]
			copy(dst.V[y*dst.StrideV:(y+1)*dst.StrideV], srcRow[:cw])
		}
	case image.YCbCrSubsampleRatio422:
		// 422 → 420：色度垂直方向两行取平均
		for y := 0; y < ch; y++ {
			r0 := (2*y + b.Min.Y - src.Rect.Min.Y) * src.CStride
			r1 := (2*y + 1 + b.Min.Y - src.Rect.Min.Y) * src.CStride
			x0 := b.Min.X/2 - src.Rect.Min.X/2
			for x := 0; x < cw; x++ {
				dst.U[y*dst.StrideU+x] = avg2(src.Cb[r0+x0+x], src.Cb[r1+x0+x])
				dst.V[y*dst.StrideV+x] = avg2(src.Cr[r0+x0+x], src.Cr[r1+x0+x])
			}
		}
	default:
		// 4:4:4 等：色度 2x2 取平均
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				var su, sv, n int
				for dy := 0; dy < 2; dy++ {
					for dx := 0; dx < 2; dx++ {
						yy := 2*y + dy + b.Min.Y - src.Rect.Min.Y
						xx := 2*x + dx + b.Min.X - src.Rect.Min.X
						if yy < 0 || yy >= src.Rect.Dy() || xx < 0 || xx >= src.Rect.Dx() {
							continue
						}
						su += int(src.Cb[yy*src.CStride+xx])
						sv += int(src.Cr[yy*src.CStride+xx])
						n++
					}
				}
				if n > 0 {
					dst.U[y*dst.StrideU+x] = byte(su / n)
					dst.V[y*dst.StrideV+x] = byte(sv / n)
				}
			}
		}
	}
	return dst
}

func avg2(a, b byte) byte { return byte((int(a) + int(b) + 1) / 2) }

// yuyvToI420 把 YUYV（YUY2）打包格式转成 I420。
// 布局：Y0 U0 Y1 V0（每 4 字节 2 像素）。
func yuyvToI420(buf []byte, w, h int) *YUV420P {
	if w%2 != 0 {
		w--
	}
	if h%2 != 0 {
		h--
	}
	dst := NewYUV420P(w, h)
	for y := 0; y < h; y++ {
		rowOff := y * w * 2
		if rowOff+w*2 > len(buf) {
			break
		}
		row := buf[rowOff : rowOff+w*2]
		for x := 0; x < w; x += 2 {
			dst.Y[y*dst.StrideY+x] = row[x*2]
			dst.Y[y*dst.StrideY+x+1] = row[x*2+2]
			if y%2 == 0 {
				dst.U[(y/2)*dst.StrideU+x/2] = row[x*2+1]
				dst.V[(y/2)*dst.StrideV+x/2] = row[x*2+3]
			} else {
				// 第二行色度与上一行平均（422 → 420）
				i := (y / 2) * dst.StrideU
				dst.U[i+x/2] = avg2(dst.U[i+x/2], row[x*2+1])
				dst.V[i+x/2] = avg2(dst.V[i+x/2], row[x*2+3])
			}
		}
	}
	return dst
}

// rgbaToI420 把 RGBA 转成 I420（部分驱动只给 RGB 格式时用）。
func rgbaToI420(pix []byte, stride, w, h int) *YUV420P {
	if w%2 != 0 {
		w--
	}
	if h%2 != 0 {
		h--
	}
	dst := NewYUV420P(w, h)
	for y := 0; y < h; y++ {
		row := pix[y*stride:]
		for x := 0; x < w; x++ {
			r := int(row[x*4])
			g := int(row[x*4+1])
			b := int(row[x*4+2])
			yy := (77*r + 150*g + 29*b) >> 8
			dst.Y[y*dst.StrideY+x] = clamp8(yy)
			if y%2 == 0 && x%2 == 0 {
				u := ((-43*r - 85*g + 128*b) >> 8) + 128
				v := ((128*r - 107*g - 21*b) >> 8) + 128
				i := (y / 2) * dst.StrideU
				dst.U[i+x/2] = clamp8(u)
				dst.V[i+x/2] = clamp8(v)
			}
		}
	}
	return dst
}

func clamp8(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// rgbaToI420FromImage 兜底：任意 image.Image → I420
// （摄像头给出 JPEG 解码后的 RGB 或驱动只提供不常见格式时用）。
func rgbaToI420FromImage(img image.Image) *YUV420P {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]byte, w*4*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			o := (y*w + x) * 4
			pix[o] = byte(r >> 8)
			pix[o+1] = byte(g >> 8)
			pix[o+2] = byte(bb >> 8)
			pix[o+3] = 0xff
		}
	}
	return rgbaToI420(pix, w*4, w, h)
}
