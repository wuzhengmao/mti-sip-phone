package ui

import (
	"math"
	"testing"
)

// TestVideoFitSize 视频窗口尺寸规则（用户指定）：
//  1. 优先按原视频宽高显示；
//  2. 宽或高超过屏幕 1/4 时等比缩小，缩小后宽高都在 1/4 内。
//
// 用 1920×1080 屏幕 → 上限 480×270 做基准。
func TestVideoFitSize(t *testing.T) {
	const maxW, maxH float32 = 480, 270
	cases := []struct {
		name   string
		fw, fh int
		w, h   float32
	}{
		{"原样（宽高都未超上限）", 320, 240, 320, 240},
		{"恰好在边界上", 480, 270, 480, 270},
		{"高度超上限，按高收缩", 352, 288, 330, 270},
		{"宽度超上限，按宽收缩", 640, 240, 480, 180},
		{"宽高都超上限，取更小比例", 1920, 1080, 480, 270},
		{"16:9 源", 1280, 720, 480, 270},
		{"帧过小，等比放大到下限", 96, 72, 160, 120},
		{"极端宽高比：下限与上限撞车时上限优先", 900, 100, 480, 53.333332},
	}
	for _, c := range cases {
		w, h := videoFitSize(c.fw, c.fh, maxW, maxH)
		if math.Abs(float64(w-c.w)) > 0.01 || math.Abs(float64(h-c.h)) > 0.01 {
			t.Errorf("%s：videoFitSize(%d,%d) = %.2f×%.2f，期望 %.2f×%.2f",
				c.name, c.fw, c.fh, w, h, c.w, c.h)
		}
		// 规则 2 必须恒成立
		if w > maxW+0.01 || h > maxH+0.01 {
			t.Errorf("%s：结果 %.2f×%.2f 超出上限 %.0f×%.0f", c.name, w, h, maxW, maxH)
		}
		// 等比（不改宽高比）
		if math.Abs(float64(w/h)-float64(c.fw)/float64(c.fh)) > 0.001 {
			t.Errorf("%s：宽高比 %.4f 与原视频 %.4f 不一致", c.name, w/h, float64(c.fw)/float64(c.fh))
		}
	}
}

// TestTrayRegisteredState 托盘图标颜色状态机：续注册（registering）
// 期间保持原色——已注册保持绿色、失败/未注册为红色；只有注册失败
// 才由绿转红（用户要求：定时续注册不应让图标闪红）。
func TestTrayRegisteredState(t *testing.T) {
	a := &App{}
	steps := []struct {
		state string
		want  bool // trayRegistered（true=托盘显示绿色）
	}{
		{"unregistered", false},
		{"registering", false}, // 首次注册中：还没成功过，保持红色
		{"registered", true},
		{"registering", true}, // 续注册中：保持绿色
		{"registered", true},
		{"registering", true},
		{"failed", false}, // 续注册失败：转红
		{"registering", false},
		{"registered", true},
	}
	for _, s := range steps {
		a.setRegState(s.state)
		if a.trayRegistered != s.want {
			t.Errorf("状态 %q 后 trayRegistered = %v，期望 %v", s.state, a.trayRegistered, s.want)
		}
		if a.regState != s.state {
			t.Errorf("状态 %q 未记录，regState = %q", s.state, a.regState)
		}
	}
}
