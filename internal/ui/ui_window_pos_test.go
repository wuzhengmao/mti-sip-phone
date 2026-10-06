package ui

import "testing"

// TestWindowPosUsable 保存位置的可达性判定（REQ-UI-040 / REQ-VIDEO-015）：
// 只要还留出足够可见的一块就算可用（用户能自己拖回来），全出屏才回退默认摆放。
// 屏幕矩形取虚拟桌面（多显示器原点可以为负）。
func TestWindowPosUsable(t *testing.T) {
	// 单屏 1920×1080：窗口 260×144
	const (
		w, h           = 260, 144
		sx, sy, sw, sh = 0, 0, 1920, 1080
	)
	cases := []struct {
		name string
		x, y int
		want bool
	}{
		{"左上角原点", 0, 0, true},
		{"右下角贴边（只露一角）", 1920 - windowMinVisibleW, 1080 - windowMinVisibleH, true},
		{"完全在屏内靠右下", 1600, 900, true},
		{"右边只露不到下限", 1920 - windowMinVisibleW + 1, 500, false},
		{"下边只露不到下限", 500, 1080 - windowMinVisibleH + 1, false},
		{"完全在屏幕右侧外", 2000, 100, false},
		{"负坐标（屏幕左上外）", -300, -200, false},
	}
	for _, c := range cases {
		if got := windowPosUsable(c.x, c.y, w, h, sx, sy, sw, sh); got != c.want {
			t.Errorf("%s: windowPosUsable(%d,%d) = %v，期望 %v", c.name, c.x, c.y, got, c.want)
		}
	}

	// 副屏在左侧（虚拟桌面原点为负）：副屏上的位置必须判为可用
	if !windowPosUsable(-1500, 300, w, h, -1920, 0, 3840, 1080) {
		t.Error("左副屏上的位置应判为可用（虚拟桌面原点可为负）")
	}

	// 尺寸未知（窗口还没显示）时退化成"左上角落在屏内"
	if !windowPosUsable(100, 100, 0, 0, 0, 0, 1920, 1080) {
		t.Error("尺寸未知时左上角在屏内应判为可用")
	}
	if windowPosUsable(5000, 100, 0, 0, 0, 0, 1920, 1080) {
		t.Error("尺寸未知时左上角出屏应判为不可用")
	}

	// 拿不到屏幕信息（非桌面平台）时不拦
	if !windowPosUsable(99999, 99999, w, h, 0, 0, 0, 0) {
		t.Error("屏幕信息缺失时不该判为不可用")
	}
}

// TestDragTarget 拖动位移（锚点法）：目标位置 = 按下时的窗口左上角 +
// 光标相对按下时的位移，每次都从锚点重算（无累计漂移、无窗口跟随的自反馈）。
func TestDragTarget(t *testing.T) {
	// 窗口在 (800,600)，按下时光标 (900,700)：光标右移 30、下移 20
	if x, y := dragTarget(800, 600, 930, 720, 900, 700); x != 830 || y != 620 {
		t.Errorf("位移算错：得到 (%d,%d)，期望 (830,620)", x, y)
	}
	// 光标往左上拖：允许负方向（屏幕边缘由窗口管理器/系统自己限制）
	if x, y := dragTarget(800, 600, 700, 500, 900, 700); x != 600 || y != 400 {
		t.Errorf("负位移算错：得到 (%d,%d)，期望 (600,400)", x, y)
	}
	// 光标没动：位置不变
	if x, y := dragTarget(800, 600, 900, 700, 900, 700); x != 800 || y != 600 {
		t.Errorf("零位移算错：得到 (%d,%d)，期望 (800,600)", x, y)
	}
}
