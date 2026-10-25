package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
)

// 音量状态与滚轮折算的纯逻辑测试（REQ-MEDIA-006）。
//
// **不做 Fyne 渲染测试**：测试驱动里 fyne.Do 是内联执行，后台 goroutine 会与
// 测试主线程并发跑同一棵控件树，表现为字体整形库随机 panic（项目既有约定）。

func TestLevelStateDefaults(t *testing.T) {
	mic := newLevelState(micMaxPercent)
	if mic.pct != 100 || !mic.on() {
		t.Fatalf("麦克风默认应为 100%% 且为开，实际 %d", mic.pct)
	}
	if got := mic.value(); got != 1 {
		t.Fatalf("100%% 的下发值应为 1，实际 %v", got)
	}
	if got := mic.level(); got != 0.5 {
		t.Fatalf("麦克风量程 200%%，100%% 应显示半格，实际 %v", got)
	}
	spk := newLevelState(speakerMaxPercent)
	if got := spk.level(); got != 1 {
		t.Fatalf("扬声器量程 100%%，100%% 应满格，实际 %v", got)
	}
}

func TestLevelStateWheelClamp(t *testing.T) {
	mic := newLevelState(micMaxPercent)
	// 步进 5%：向上 3 格 = 115%
	if !mic.byWheel(3) || mic.pct != 115 {
		t.Fatalf("向上 3 格应为 115%%，实际 %d", mic.pct)
	}
	// 上限夹取
	mic.byWheel(100)
	if mic.pct != micMaxPercent {
		t.Fatalf("应夹取到 %d%%，实际 %d", micMaxPercent, mic.pct)
	}
	// 下限夹取
	mic.byWheel(-1000)
	if mic.pct != 0 || mic.on() {
		t.Fatalf("应夹取到 0%%（禁用），实际 %d", mic.pct)
	}
	// 0 格不算变化
	if mic.byWheel(0) {
		t.Fatal("0 格不应算作变化")
	}
}

func TestLevelStateZeroAndToggle(t *testing.T) {
	spk := newLevelState(speakerMaxPercent)
	spk.byWheel(-4) // 100% → 80%
	// 滚到 0：记住的是上一次非零值 80%
	spk.byWheel(-16)
	if spk.pct != 0 || spk.savedPct != 80 {
		t.Fatalf("滚到 0%% 应记住 80%%，实际 pct=%d saved=%d", spk.pct, spk.savedPct)
	}
	// 从 0 往上滚一档：回到 5%（而不是上一档 80%）
	spk.byWheel(1)
	if spk.pct != 5 {
		t.Fatalf("从 0%% 上滚一档应为 5%%，实际 %d", spk.pct)
	}
	// 点击开关：0 ↔ 上次非零值
	spk.pct = 60
	spk.toggle()
	if spk.pct != 0 {
		t.Fatalf("点击应关闭（0%%），实际 %d", spk.pct)
	}
	spk.toggle()
	if spk.pct != 60 {
		t.Fatalf("再次点击应恢复到 60%%，实际 %d", spk.pct)
	}
}

// TestLevelStateValue 下发给引擎的值：pct/100（0% 就下发 0——由 core 解释为
// 禁用，见 internal/core/audio.go）。
// TestLevelStateFillLevels 按钮底色的分段（用户要求）：0–100% 是蓝色，
// 100% 就是**满蓝**；超过 100% 的部分是叠在蓝色上的**橙色增益段**（200% 整块橙）。
func TestLevelStateFillLevels(t *testing.T) {
	mic := newLevelState(micMaxPercent)
	cases := []struct {
		pct         int
		blue, boost float32
	}{
		{0, 0, 0},
		{50, 0.5, 0},
		{100, 1, 0},   // 满蓝
		{150, 1, 0.5}, // 半圈橙
		{200, 1, 1},   // 整块橙
	}
	for _, c := range cases {
		mic.pct = c.pct
		if got := mic.blueLevel(); got != c.blue {
			t.Fatalf("%d%% 蓝色应为 %v，实际 %v", c.pct, c.blue, got)
		}
		if got := mic.boostLevel(); got != c.boost {
			t.Fatalf("%d%% 橙色应为 %v，实际 %v", c.pct, c.boost, got)
		}
	}
	// 扬声器量程只到 100%：永远没有增益段
	spk := newLevelState(speakerMaxPercent)
	for _, pct := range []int{0, 50, 100} {
		spk.pct = pct
		if got := spk.boostLevel(); got != 0 {
			t.Fatalf("扬声器 %d%% 不应有橙色增益段，实际 %v", pct, got)
		}
		if got := spk.blueLevel(); got != float32(pct)/100 {
			t.Fatalf("扬声器 %d%% 蓝色应为 %v，实际 %v", pct, float32(pct)/100, got)
		}
	}
}

func TestLevelStateValue(t *testing.T) {
	mic := newLevelState(micMaxPercent)
	mic.byWheel(20) // 100% → 200%
	if mic.value() != 2 {
		t.Fatalf("200%% 时下发值应为 2，实际 %v", mic.value())
	}
	mic.byWheel(-40) // 200% → 0%
	if mic.pct != 0 {
		t.Fatalf("应滚到 0%%，实际 %d", mic.pct)
	}
	if mic.value() != 0 {
		t.Fatalf("0%% 时下发值应为 0，实际 %v", mic.value())
	}
}

// TestLevelStateSyncFromEvent 网页改了开关/拖了滑块（call.audio 事件）：
// 以快照百分比为准；禁用时置 0 并记住原值。幂等——本机自己滚轮触发的调节
// 也会收到这条事件，不能把刚滚到的值覆盖掉。
func TestLevelStateSyncFromEvent(t *testing.T) {
	mic := newLevelState(micMaxPercent)
	mic.byWheel(10) // 150%
	// 网页把开关关掉：快照里仍留着恢复值 150，micMuted=true
	mic.syncFrom(150, true)
	if mic.pct != 0 || mic.savedPct != 150 {
		t.Fatalf("禁用后应为 0%%、记住 150%%，实际 pct=%d saved=%d", mic.pct, mic.savedPct)
	}
	// 再收到同一个事件（幂等）：不能把 saved 冲成 0
	mic.syncFrom(150, true)
	if mic.pct != 0 || mic.savedPct != 150 {
		t.Fatalf("幂等：实际 pct=%d saved=%d", mic.pct, mic.savedPct)
	}
	// 网页重新打开/把滑块拖到 180%：以快照为准
	mic.syncFrom(180, false)
	if mic.pct != 180 || mic.savedPct != 180 {
		t.Fatalf("应同步到 180%%，实际 pct=%d saved=%d", mic.pct, mic.savedPct)
	}
	// 超量程的快照被夹取
	mic.syncFrom(999, false)
	if mic.pct != micMaxPercent {
		t.Fatalf("应夹取到 %d%%，实际 %d", micMaxPercent, mic.pct)
	}
	// 快照里 0（禁用）但 muted 标志没置位：也按禁用处理
	mic.syncFrom(0, false)
	if mic.pct != 0 || mic.savedPct != micMaxPercent {
		t.Fatalf("0%% 应视为禁用并记住原值，实际 pct=%d saved=%d", mic.pct, mic.savedPct)
	}
}

// TestWheelSteps 滚轮量折算：一格 = wheelNotch，触摸板小量累积，余数留存，
// 反向对称。
func TestWheelSteps(t *testing.T) {
	var acc float32
	// 普通滚轮一格
	if got := wheelSteps(&acc, wheelNotch); got != 1 || acc != 0 {
		t.Fatalf("一格应为 1 步且余数为 0，实际 steps=%d acc=%v", got, acc)
	}
	// 一次事件里三格
	if got := wheelSteps(&acc, 3*wheelNotch); got != 3 || acc != 0 {
		t.Fatalf("三格应为 3 步，实际 steps=%d acc=%v", got, acc)
	}
	// 触摸板小量：10 + 10 + 10 = 一格（余 5）
	if got := wheelSteps(&acc, 10); got != 0 {
		t.Fatalf("半格不应触发，实际 %d", got)
	}
	if got := wheelSteps(&acc, 10); got != 0 {
		t.Fatalf("20 不应触发，实际 %d", got)
	}
	if got := wheelSteps(&acc, 10); got != 1 {
		t.Fatalf("满 25 应触发 1 步，实际 %d", got)
	}
	if acc != 5 {
		t.Fatalf("余数应为 5，实际 %v", acc)
	}
	// 反向：把余数冲掉再走一格
	if got := wheelSteps(&acc, -5); got != 0 || acc != 0 {
		t.Fatalf("冲掉余数不应触发，实际 steps=%d acc=%v", got, acc)
	}
	if got := wheelSteps(&acc, -wheelNotch); got != -1 || acc != 0 {
		t.Fatalf("反向一格应为 -1，实际 steps=%d acc=%v", got, acc)
	}
}

// TestCompactButtonIsNotScrollable 紧凑按钮**不能**实现 fyne.Scrollable
// （REQ-MEDIA-006 的回归护栏）。
//
// Fyne 的滚轮分发只把事件交给指针下**最深的一个** Scrollable，**不冒泡**
// （glfw 驱动的 processMouseScrolled → driver.FindObjectAtPositionMatching）。
// compactButton 到处都是——通话记录页/通信录页的行内「呼叫」「视频呼叫」按钮
// 就在可滚动列表里——它要是实现了 Scrolled，指针停在那些按钮上滚轮就被吞掉、
// 列表不滚。滚轮只挂在音量按钮的透明中继（scrollRelay）上。
func TestCompactButtonIsNotScrollable(t *testing.T) {
	if _, ok := any(newCompactButton(IconPlayWhite, colorSuccessGreen, 36, nil)).(fyne.Scrollable); ok {
		t.Fatal("紧凑按钮不应实现 fyne.Scrollable，否则列表行内按钮会吞掉滚轮")
	}
}

// TestCallWheelThroughDispatch 滚轮事件经 **Fyne 的命中分发**（test.Scroll 走的
// 就是真实驱动那条 FindObjectAtPositionMatching）落到音量按钮上：调值 → 刷新
// 填充条/图标 → 切换禁用样式；点击仍然有效（中继不能吞掉点击）。
func TestCallWheelThroughDispatch(t *testing.T) {
	test.NewApp()

	a := &App{
		speakerLevel: newLevelState(speakerMaxPercent),
		micLevel:     newLevelState(micMaxPercent),
	}
	a.micBtn = newLevelButton(IconMicOn, callPanelButtonSize,
		func(steps int, pos fyne.Position) { a.onCallWheel(true, steps, pos) }, a.toggleMic)
	a.speakerBtn = newLevelButton(IconSpeakerOn, callPanelButtonSize,
		func(steps int, pos fyne.Position) { a.onCallWheel(false, steps, pos) }, a.toggleSpeaker)
	a.applyMicButton()
	a.applySpeakerButton()
	// 停掉百分比提示的定时器：测试驱动里 fyne.Do 是内联执行，超时回调会在
	// 测试结束后从定时器 goroutine 上碰控件树（项目里踩过的随机 panic）
	defer a.hideLevelTip()

	w := test.NewWindow(container.NewHBox(a.micBtn, a.speakerBtn))
	defer w.Close()
	w.Resize(fyne.NewSize(160, 60))

	center := func(o fyne.CanvasObject) fyne.Position {
		return fyne.CurrentApp().Driver().AbsolutePositionForObject(o).
			Add(fyne.NewPos(o.Size().Width/2, o.Size().Height/2))
	}

	// 向上滚一格（Fyne 桌面驱动把一格放大成 wheelNotch）：麦克风 100% → 105%
	test.Scroll(w.Canvas(), center(a.micBtn), 0, wheelNotch)
	if a.micLevel.pct != 105 {
		t.Fatalf("麦克风按钮上向上滚一格应为 105%%，实际 %d", a.micLevel.pct)
	}
	// 麦克风 105%：蓝色已满（100% 就是满蓝），超出的 5% 是橙色增益段
	if got, want := a.micBtn.bar.level, float32(1); got != want {
		t.Fatalf("105%% 时蓝色应满格 %v，实际 %v", want, got)
	}
	if got, want := a.micBtn.bar.boost, float32(0.05); got != want {
		t.Fatalf("105%% 时橙色增益段应为 %v，实际 %v", want, got)
	}
	// 向下滚扬声器：100% → 95%
	test.Scroll(w.Canvas(), center(a.speakerBtn), 0, -wheelNotch)
	if a.speakerLevel.pct != 95 {
		t.Fatalf("扬声器向下滚一格应为 95%%，实际 %d", a.speakerLevel.pct)
	}
	// 点击仍然有效（中继不实现 Tappable，不得挡住按钮）：点一下关掉扬声器
	test.TapCanvas(w.Canvas(), center(a.speakerBtn))
	if a.speakerLevel.pct != 0 || a.speakerLevel.on() {
		t.Fatalf("点击应关闭扬声器（0%%），实际 %d", a.speakerLevel.pct)
	}
	if a.speakerBtn.baseBg != colorDangerRed {
		t.Fatalf("0%% 时图标应变成红色（关闭状态），实际 %v", a.speakerBtn.baseBg)
	}
	if a.speakerBtn.iconSrc != IconSpeakerOff {
		t.Fatal("0% 应换成带斜杠的禁用图标（与开状态同一字形，只多一条斜杠）")
	}
	if a.speakerBtn.bar.level != 0 || a.speakerBtn.bar.boost != 0 {
		t.Fatalf("0%% 的填充比例应为 0，实际 level=%v boost=%v", a.speakerBtn.bar.level, a.speakerBtn.bar.boost)
	}
	// 从 0 往上滚一档：回到 5% 且恢复开图标
	test.Scroll(w.Canvas(), center(a.speakerBtn), 0, wheelNotch)
	if a.speakerLevel.pct != 5 {
		t.Fatalf("从 0%% 上滚一档应为 5%%，实际 %d", a.speakerLevel.pct)
	}
	if a.speakerBtn.baseBg != colorHoldBlue {
		t.Fatalf("非 0%% 时图标应恢复蓝色，实际 %v", a.speakerBtn.baseBg)
	}
}

// TestWheelStepsClamp 异常大的一次滚动（Fyne 在 |yoff| ≥ 10 时会乘 125，一次
// 事件可能是上万）只走上限步数，且**余量清零**——否则后续每个事件都会继续按
// 上限狂掉，音量直接到底。
func TestWheelStepsClamp(t *testing.T) {
	var acc float32
	if got := wheelSteps(&acc, 15000); got != maxWheelSteps || acc != 0 {
		t.Fatalf("一次超大滚动应只走 %d 步且清空余量，实际 steps=%d acc=%v", maxWheelSteps, got, acc)
	}
	if got := wheelSteps(&acc, -15000); got != -maxWheelSteps || acc != 0 {
		t.Fatalf("反向超大滚动应只走 -%d 步，实际 steps=%d acc=%v", maxWheelSteps, got, acc)
	}
	// 正常一格不受影响
	if got := wheelSteps(&acc, wheelNotch); got != 1 {
		t.Fatalf("正常一格应为 1 步，实际 %d", got)
	}
}
