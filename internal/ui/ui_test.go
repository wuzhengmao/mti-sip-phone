package ui

import "testing"

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
