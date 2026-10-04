package ui

// 音量控制（REQ-MEDIA-006）：通话面板上的扬声器/麦克风按钮，鼠标滚轮悬停其上
// 上下滚动即调音量，按钮底色按百分比**横向填充**，0% 就是禁用（等同开关的「关」）。
//
// 约定：
//   - 数值内部按**百分比整数**存（0–maxPct），不存 float——0.05 累加 20 次不再
//     等于 1.0，填充条与百分比数字会逐渐漂移。传给媒体引擎的 0–2 / 0–1 由
//     pct/100 现算。
//   - 0% 用**静音标志**表达（麦克风停发、扬声器停播，REQ-UI-023 的既有语义），
//     而不是往引擎里下发增益 0——下发的始终是「非 0 的那个值」，否则之后别人
//     单独解除静音（例如网页侧改开关）会得到一个没有任何提示的静音。
//   - 状态是**会话内记忆**：通话之间保留，应用重启回默认 100%（同麦克风/扬声器
//     开关，不写配置文件）。
//
// 数值范围与步进（用户确认）：扬声器 0–100%、麦克风 0–200%，每格滚轮 5%。

import (
	"errors"
	"fmt"
	"time"

	"fyne.io/fyne/v2"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

const (
	// volumeStepPercent 一格滚轮的步进（百分比）。
	volumeStepPercent = 5
	// speakerMaxPercent / micMaxPercent 两个按钮的量程上限。
	// 麦克风到 200%（可额外放大，安静话筒有用），扬声器到 100%。
	speakerMaxPercent = 100
	micMaxPercent     = 200
	// pctShowDuration 滚动调节后，百分比数字显示多久再恢复图标。
	pctShowDuration = 900 * time.Millisecond
)

// levelState 可滚轮调节的音量状态（会话内记忆，Fyne 主线程读写）。
type levelState struct {
	pct      int // 当前百分比（0 = 禁用）
	maxPct   int // 上限百分比
	savedPct int // 上次非零百分比（点击开关的恢复值，恒 > 0）
}

func newLevelState(maxPct int) levelState {
	return levelState{pct: 100, maxPct: maxPct, savedPct: 100}
}

// value 当前值（传给媒体引擎）：麦克风 0–2、扬声器 0–1。
// **0 也照实下发**：`core.SetMicGain/SetSpeakerVolume` 把 0 解释为禁用
//（置静音标志、引擎里保留原增益值），见 internal/core/audio.go。
func (s *levelState) value() float32 { return float32(s.pct) / 100 }

// level 填充条比例（0–1，相对各自量程）。**只用于判断满格**，
// 按钮底色请用 blueLevel/boostLevel（用户要求：100% 就是满蓝，
// 超过 100% 的部分是叠在蓝色上的橙色增益段）。
func (s *levelState) level() float32 { return float32(s.pct) / float32(s.maxPct) }

// blueLevel 蓝色部分比例（0–1）：0–100%。
func (s *levelState) blueLevel() float32 {
	if s.pct >= 100 {
		return 1
	}
	return float32(s.pct) / 100
}

// boostLevel 橙色增益部分比例（0–1）：只有超过 100% 的麦克风才有（100%→200%）。
func (s *levelState) boostLevel() float32 {
	if s.pct <= 100 {
		return 0
	}
	return float32(s.pct-100) / 100
}

// on 是否处于开启状态（非 0%）。
func (s *levelState) on() bool { return s.pct > 0 }

// byWheel 按滚轮格数调整（正 = 调大），夹取到 [0, maxPct]；返回是否有变化。
func (s *levelState) byWheel(steps int) bool {
	if steps == 0 {
		return false
	}
	old := s.pct
	s.pct = clampPct(s.pct+steps*volumeStepPercent, s.maxPct)
	if s.pct > 0 {
		s.savedPct = s.pct
	}
	return s.pct != old
}

// toggle 点击开关：在 0 与上次非零值之间切换。
func (s *levelState) toggle() {
	if s.pct > 0 {
		s.savedPct = s.pct
		s.pct = 0
		return
	}
	s.pct = s.savedPct
	if s.pct <= 0 {
		s.pct = 100
	}
}

// syncFrom 外部来源（网页拖了滑块、点了开关 → call.audio 事件）改变了音量：
// **以快照里的百分比为准**（这样网页把增益拖到 150% 时桌面显示也对得上），
// 禁用（或 0%）时置 0 并记住原值。幂等——本机自己滚轮触发的调节也会收到这条
// 事件，不能把用户刚滚到的值覆盖掉。
func (s *levelState) syncFrom(pct int, muted bool) {
	pct = clampPct(pct, s.maxPct)
	if muted || pct == 0 {
		if s.pct > 0 {
			s.savedPct = s.pct
		}
		s.pct = 0
		return
	}
	s.pct = pct
	s.savedPct = pct
}

// clampPct 把百分比夹取到 [0, max]。
func clampPct(v, max int) int {
	if v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// ---------------------------------------------------------------------------
// 面板逻辑
// ---------------------------------------------------------------------------

// onCallWheel 面板按钮上的滚轮：调值 → 刷新外观 → 下发引擎 → 在指针处弹百分比提示。
// mic=true 调麦克风增益，否则调扬声器音量。
func (a *App) onCallWheel(mic bool, steps int, pos fyne.Position) {
	st, btn := &a.speakerLevel, a.speakerBtn
	if mic {
		st, btn = &a.micLevel, a.micBtn
	}
	if btn == nil {
		return
	}
	if st.byWheel(steps) {
		a.applyLevelButton(mic)
		a.pushLevel(mic)
	}
	// 撞到边界（0% / 上限）时值没变，但**照样弹提示**：不然用户以为滚轮没反应
	a.showLevelTip(mic, pos)
}

// pushLevel 把当前值下发给当前通话（引擎未建立时静默忽略，通话建立时由
// applySessionAudioState 统一下发）。
func (a *App) pushLevel(mic bool) {
	if a.activeCallID == "" {
		return
	}
	if mic {
		a.pushMicLevel()
		return
	}
	a.pushSpeakerLevel()
}

// pushMicLevel 下发麦克风增益：**0% 就是禁用**，由 core.SetMicGain 内部置静音
// 标志（不再单独调 MuteMic——那会在每格滚轮上都发一条 call.audio 事件，把网页
// 的日志与事件总线刷屏）。
func (a *App) pushMicLevel() {
	if a.activeCallID == "" {
		return
	}
	if err := a.core.SetMicGain(a.activeCallID, a.micLevel.value()); err != nil && !errors.Is(err, core.ErrCallNotFound) {
		logx.Logger().Warn("麦克风增益下发失败", "err", err)
	}
}

// pushSpeakerLevel 下发扬声器音量（0% = 禁用，同 pushMicLevel）。
func (a *App) pushSpeakerLevel() {
	if a.activeCallID == "" {
		return
	}
	if err := a.core.SetSpeakerVolume(a.activeCallID, a.speakerLevel.value()); err != nil && !errors.Is(err, core.ErrCallNotFound) {
		logx.Logger().Warn("扬声器音量下发失败", "err", err)
	}
}

// applyLevelButton 按当前值刷新按钮外观（mic 选择哪一个）。
func (a *App) applyLevelButton(mic bool) {
	if mic {
		a.applyMicButton()
		return
	}
	a.applySpeakerButton()
}

// applySpeakerButton 扬声器按钮外观（REQ-UI-023 / REQ-MEDIA-006）：
// 非 0% = 深灰轨道 + 蓝色圆周填充 + 白色开图标；0% = 浅灰底 + 红色关图标（禁用样式）。
func (a *App) applySpeakerButton() {
	if a.speakerBtn == nil {
		return
	}
	if a.speakerLevel.on() {
		a.speakerBtn.SetTrack(colorLevelTrack)
		a.speakerBtn.SetIcon(IconSpeakerOn)
		a.speakerBtn.SetLevels(a.speakerLevel.blueLevel(), a.speakerLevel.boostLevel())
	} else {
		a.speakerBtn.SetTrack(colorSwitchGray)
		a.speakerBtn.SetIcon(IconSpeakerOff)
		a.speakerBtn.SetLevels(0, 0)
	}
	a.speakerBtn.Refresh()
}

// applyMicButton 麦克风按钮外观（同扬声器）：100% 满蓝，100%→200% 的增益段
// 在蓝色之上叠橙色（200% 整块橙）。
func (a *App) applyMicButton() {
	if a.micBtn == nil {
		return
	}
	if a.micLevel.on() {
		a.micBtn.SetTrack(colorLevelTrack)
		a.micBtn.SetIcon(IconMicOn)
		a.micBtn.SetLevels(a.micLevel.blueLevel(), a.micLevel.boostLevel())
	} else {
		a.micBtn.SetTrack(colorSwitchGray)
		a.micBtn.SetIcon(IconMicOff)
		a.micBtn.SetLevels(0, 0)
	}
	a.micBtn.Refresh()
}

// showLevelTip 在**指针处**弹一条百分比提示（复用通话面板的提示图层），
// pctShowDuration 后自动收起。
//
// 提示内容带上下限/禁用状态：0% 就是禁用（等同「关」）。
// 定时器用一个世代号作废过期回调——`Timer.Stop()` 对"已触发、回调还在队列里"
// 的定时器返回 false，那种残留回调会把新一轮刚弹出的提示立刻收起（闪一下）。
func (a *App) showLevelTip(mic bool, pos fyne.Position) {
	if a.callTip == nil {
		return // 面板未创建（单测里也会是 nil）
	}
	st := &a.speakerLevel
	name := "扬声器音量"
	if mic {
		st = &a.micLevel
		name = "麦克风增益"
	}
	text := fmt.Sprintf("%s %d%%", name, st.pct)
	if !st.on() {
		text += "（已关闭）"
	}
	a.callTip.show(text, pos)
	if a.pctTimer != nil {
		a.pctTimer.Stop()
	}
	a.pctGen++
	gen := a.pctGen
	a.pctTimer = time.AfterFunc(pctShowDuration, func() {
		fyne.Do(func() {
			if a.pctGen != gen {
				return // 已被新一轮滚动作废
			}
			if a.callTip != nil {
				a.callTip.hide()
			}
		})
	})
}

// hideLevelTip 收起音量提示（通话结束、点击开关、面板收起时用）。
func (a *App) hideLevelTip() {
	if a.callTip != nil {
		a.callTip.hide()
	}
	if a.pctTimer != nil {
		a.pctTimer.Stop()
		a.pctTimer = nil
	}
	a.pctGen++ // 作废可能在队列里的旧回调
}

// toggleSpeaker 扬声器开关（REQ-UI-023）：0% ↔ 上次非零值。
// 点击时收起可能还挂着的百分比数字（图标要立刻回来）。
func (a *App) toggleSpeaker() {
	a.speakerLevel.toggle()
	a.hideLevelTip()
	a.applySpeakerButton()
	a.pushSpeakerLevel()
}

// toggleMic 麦克风开关（REQ-UI-023）：0% ↔ 上次非零值。
func (a *App) toggleMic() {
	a.micLevel.toggle()
	a.hideLevelTip()
	a.applyMicButton()
	a.pushMicLevel()
}

// applySessionAudioState 把会话内记忆的扬声器/麦克风状态应用到当前通话。
// 通话结束后引擎随之销毁，新通话建立时（以及 183 早期媒体建立引擎时）重新下发。
// 引擎尚未创建（呼入振铃）时静默跳过，接通后再次调用即可生效。
func (a *App) applySessionAudioState() {
	if a.activeCallID == "" {
		return
	}
	a.pushSpeakerLevel()
	a.pushMicLevel()
}

// syncAudioButtonsFromEvent 网页改了麦克风/扬声器（call.audio 事件：点开关，
// 或拖测试页的音量滑块）：同步会话内状态与按钮外观。以快照里的百分比为准，
// 这样网页把增益拖到 150% 时桌面上显示也是 150%。幂等，本机自己滚轮触发的
// 调节不会被打乱。
func (a *App) syncAudioButtonsFromEvent(call *core.Call) {
	if call == nil {
		return
	}
	a.micLevel.syncFrom(call.MicGain, call.MicMuted)
	a.speakerLevel.syncFrom(call.SpeakerVolume, call.SpeakerMuted)
	a.applyMicButton()
	a.applySpeakerButton()
}
