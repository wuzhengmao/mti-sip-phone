package ui

import (
	"image"
	"image/color"

	"fyne.io/fyne/v2/driver/desktop"
)

// moveCursor "可移动"光标（四向箭头），通话面板顶部拖动条悬停时用。
//
// 为什么要自定义：Fyne 的标准光标只有默认/文本/十字/手型/水平与垂直缩放/
// 两个斜向缩放（见 driver/desktop/cursor.go），**没有** move（all-resize）
// 这一种，而"这条线可以拖着走"最贴切的提示就是四向箭头。桌面驱动支持
// 自定义光标位图（glfw.CreateCursor），所以这里直接把图形画出来：
// 黑色主体 + 1px 白色描边，压在深色/浅色背景上都看得清。
type moveCursor struct{}

// 21×21 的位图，热点取正中。
const (
	moveCursorSize = 21
	moveCursorHot  = 10
)

// moveCursorImage 画四向箭头：十字主干 + 四个实心三角箭头 + 白色描边。
var moveCursorImage = func() *image.RGBA {
	const (
		sz = moveCursorSize
		c  = moveCursorHot
	)
	mask := make([]bool, sz*sz)
	set := func(x, y int) {
		if x < 0 || y < 0 || x >= sz || y >= sz {
			return
		}
		mask[y*sz+x] = true
	}

	// 十字主干（3px 粗）
	for i := c - 5; i <= c+5; i++ {
		set(c-1, i)
		set(c, i)
		set(c+1, i)
		set(i, c-1)
		set(i, c)
		set(i, c+1)
	}
	// 四个箭头（实心三角，从外往内逐行变宽）
	for d := 0; d <= 3; d++ {
		w := 3 - d
		for x := c - w; x <= c+w; x++ {
			set(x, 1+d)    // 上
			set(x, sz-2-d) // 下
			set(1+d, x)    // 左
			set(sz-2-d, x) // 右
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, sz, sz))
	for y := 0; y < sz; y++ {
		for x := 0; x < sz; x++ {
			if mask[y*sz+x] {
				img.Set(x, y, color.Black)
				continue
			}
			// 紧贴主体的透明像素涂白：深浅背景下都看得见
			near := false
			for dy := -1; dy <= 1 && !near; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= sz || ny >= sz {
						continue
					}
					if mask[ny*sz+nx] {
						near = true
						break
					}
				}
			}
			if near {
				img.Set(x, y, color.White)
			}
		}
	}
	return img
}()

// 编译期确认实现了 desktop.Cursor。
var _ desktop.Cursor = moveCursor{}

func (moveCursor) Image() (image.Image, int, int) {
	return moveCursorImage, moveCursorHot, moveCursorHot
}
