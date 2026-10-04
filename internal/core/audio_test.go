package core

// 音量/增益（REQ-MEDIA-006）的核心层测试：百分比换算、事件合并发布、
// 新通话的默认值。

import (
	"testing"
	"time"

	"mti-sip-phone/internal/config"
)

func TestPercentOf(t *testing.T) {
	cases := []struct {
		v    float32
		max  int
		want int
	}{
		{1, 200, 100},  // 单位增益
		{0, 200, 0},    // 0 = 禁用
		{2, 200, 200},  // 麦克风上限
		{1.5, 200, 150},
		{0.999, 200, 100}, // 四舍五入，不掉档
		{3, 200, 200},     // 越界夹取
		{-1, 200, 0},
		{1, 100, 100},
		{1.5, 100, 100}, // 扬声器上限 100%
	}
	for _, c := range cases {
		if got := percentOf(c.v, c.max); got != c.want {
			t.Fatalf("percentOf(%v, %d) = %d，期望 %d", c.v, c.max, got, c.want)
		}
	}
}

// TestAudioPublishCoalesced 音量事件合并发布：首次立即发，窗口内的连续改动
// 只补发一条（滚轮连滚十几下不会把事件总线刷爆）。
func TestAudioPublishCoalesced(t *testing.T) {
	app := New(config.Default(), nil, NewBus())
	ch := app.Bus().Subscribe()

	app.audioPub.publish(app, "c1") // 首次：立即
	if !hasEvent(ch, "call.audio") {
		t.Fatal("首次音量变化应立即发布")
	}

	// 窗口内的连续三次改动：只在窗口结束时补发一条
	app.audioPub.publish(app, "c1")
	app.audioPub.publish(app, "c1")
	app.audioPub.publish(app, "c1")
	if hasEvent(ch, "call.audio") {
		t.Fatal("窗口内的改动不应立即发布")
	}
	if !waitEvent(ch, "call.audio", time.Second) {
		t.Fatal("窗口结束后应补发一条")
	}
	// 只补发一条：稍等片刻不应再冒出来
	time.Sleep(audioPublishInterval + 50*time.Millisecond)
	if hasEvent(ch, "call.audio") {
		t.Fatal("同一窗口只应补发一条")
	}
}

// TestUpsertCallAudioDefaults 新通话的增益/音量默认 100%（0 在这两个字段里
// 表示"禁用"，不能靠零值默认）。
//
// 注意 upsertCall 是**整份覆盖**（不是合并），所以它只在通话创建时调用；
// 后续状态变化一律走 updateCall（否则静音标志、视频状态也会被清零）。
func TestUpsertCallAudioDefaults(t *testing.T) {
	app := New(config.Default(), nil, NewBus())
	app.upsertCall(&Call{ID: "c1"})
	c := app.getCall("c1")
	if c.MicGain != 100 || c.SpeakerVolume != 100 {
		t.Fatalf("新通话应默认 100%%，实际 mic=%d spk=%d", c.MicGain, c.SpeakerVolume)
	}
}

// hasEvent 队列里是否已有该事件（Bus 是同步投递）。
func hasEvent(ch chan Event, name string) bool {
	for {
		select {
		case ev := <-ch:
			if ev.Name == name {
				return true
			}
		default:
			return false
		}
	}
}

// waitEvent 等待该事件到达（最长 d）。
func waitEvent(ch chan Event, name string, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case ev := <-ch:
			if ev.Name == name {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
