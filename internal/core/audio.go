package core

// 音量/增益的对外接口与事件合并发布（REQ-MEDIA-006）。
//
// 语义（桌面面板与网页共用一份）：
//   - `MicGain`（0–200）/`SpeakerVolume`（0–100）是**百分比**，**0 就是禁用**
//     ——等价于点开关的「关」：麦克风停发、扬声器停播（纯本地，不发 SIP 信令）。
//     引擎里保留上次的增益值，解除静音即恢复。
//   - 快照里的数值始终是"非 0 的那个值"（禁用时它就是恢复值），当前是否禁用由
//     `micMuted`/`speakerMuted` 表达。网页的滑块据此显示：禁用时显示 0。
//
// 事件合并：滚轮 / 拖滑块会连续触发（一秒十几下），每次都广播会把事件总线
// （容量 64、满即丢）刷爆、把通话状态事件挤掉——所以这里合并成「首次立即发 +
// 窗口内最多补发一条」，150ms 的延迟肉眼无感。

import (
	"sync"
	"time"

	"mti-sip-phone/internal/logx"
)

// audioPublishInterval 音量/增益事件的合并发布间隔。
const audioPublishInterval = 150 * time.Millisecond

// audioPublisher 合并发布 call.audio（见文件头注释）。
// 字段由 mu 保护；定时器回调在**后台 goroutine**，publishCall 本身是并发安全的。
type audioPublisher struct {
	mu      sync.Mutex
	last    time.Time
	pending string // 待补发的 callID（空 = 无）
	timer   *time.Timer
}

// publish 发布一条音量变化事件：距上次发布超过一个窗口就立即发，否则记下来
// 由窗口结束时的补发兜底（保证最后一次改动一定发得出去）。
func (p *audioPublisher) publish(a *App, callID string) {
	p.mu.Lock()
	if time.Since(p.last) >= audioPublishInterval {
		p.last = time.Now()
		p.pending = ""
		p.mu.Unlock()
		a.publishCall("call.audio", callID)
		return
	}
	p.pending = callID
	if p.timer == nil {
		p.timer = time.AfterFunc(audioPublishInterval, func() { p.flush(a) })
	}
	p.mu.Unlock()
}

// flush 补发窗口内最后一次改动。
func (p *audioPublisher) flush(a *App) {
	p.mu.Lock()
	callID := p.pending
	p.pending = ""
	p.last = time.Now()
	p.timer = nil
	p.mu.Unlock()
	if callID != "" {
		a.publishCall("call.audio", callID)
	}
}

// percentOf 把引擎用的增益/音量（1 = 原始电平）换算成夹取后的百分比。
// 四舍五入，避免 0.999… 掉一档。
func percentOf(v float32, max int) int {
	p := int(v*100 + 0.5)
	if p < 0 {
		return 0
	}
	if p > max {
		return max
	}
	return p
}

// SetMicGain 麦克风增益（REQ-MEDIA-006）：gain 0–2（界面 0–200%）。
// **0 = 禁用**：等价于 MuteMic(callID, true)（引擎里保留原增益值，解除静音即
// 恢复）；非 0 时下发音量，若此前处于禁用状态则同时解除静音。纯本地，不发 SIP 信令。
func (a *App) SetMicGain(callID string, gain float32) error {
	if a.getCall(callID) == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	pct := percentOf(gain, 200)
	if pct == 0 {
		return a.MuteMic(callID, true)
	}
	if err := a.tel.SetMicGain(callID, float32(pct)/100); err != nil {
		return err
	}
	wasMuted := false
	if !a.updateCall(callID, func(c *Call) {
		c.MicGain = pct
		wasMuted = c.MicMuted
		c.MicMuted = false
	}) {
		return ErrCallNotFound
	}
	if wasMuted {
		// 从禁用状态恢复：先下发增益再解除静音（反过来会有一瞬间按旧音量出声）
		if err := a.tel.MuteMic(callID, false); err != nil {
			return err
		}
	}
	logx.Logger().Debug("麦克风增益", "callId", callID, "percent", pct)
	a.audioPub.publish(a, callID)
	return nil
}

// SetSpeakerVolume 扬声器音量（REQ-MEDIA-006）：vol 0–1（界面 0–100%）。
// **0 = 禁用**：等价于 MuteSpeaker(callID, true)。状态与事件口径同 SetMicGain。
func (a *App) SetSpeakerVolume(callID string, vol float32) error {
	if a.getCall(callID) == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	pct := percentOf(vol, 100)
	if pct == 0 {
		return a.MuteSpeaker(callID, true)
	}
	if err := a.tel.SetSpeakerVolume(callID, float32(pct)/100); err != nil {
		return err
	}
	wasMuted := false
	if !a.updateCall(callID, func(c *Call) {
		c.SpeakerVolume = pct
		wasMuted = c.SpeakerMuted
		c.SpeakerMuted = false
	}) {
		return ErrCallNotFound
	}
	if wasMuted {
		if err := a.tel.MuteSpeaker(callID, false); err != nil {
			return err
		}
	}
	logx.Logger().Debug("扬声器音量", "callId", callID, "percent", pct)
	a.audioPub.publish(a, callID)
	return nil
}
