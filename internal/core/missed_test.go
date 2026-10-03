package core

// 未接来电判定（REQ-UI-010）的回归测试：判定在核心层，结果经
// call.terminated / call.failed 的 missed 字段广播给界面与网页，
// 两侧共用同一答案。

import (
	"testing"

	"mti-sip-phone/internal/config"
)

// newMissedTestApp 建一个无电话能力的 App 并订阅事件总线。
func newMissedTestApp() (*App, chan Event) {
	app := New(config.Default(), nil, NewBus())
	return app, app.Bus().Subscribe()
}

// endEvent 取指定结束事件的 payload（Publish 是同步投递，事件已在通道里）。
func endEvent(t *testing.T, ch chan Event, name string) map[string]any {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ev.Name != name {
				continue
			}
			m, ok := ev.Data.(map[string]any)
			if !ok {
				t.Fatalf("%s 事件负载类型应为 map，实际 %T", name, ev.Data)
			}
			return m
		default:
			t.Fatalf("未收到 %s 事件", name)
			return nil
		}
	}
}

// missedOf 结束事件的 missed 字段。
func missedOf(t *testing.T, m map[string]any) bool {
	t.Helper()
	v, ok := m["missed"].(bool)
	if !ok {
		t.Fatalf("结束事件缺少 missed 字段: %v", m)
	}
	return v
}

// TestMissedOnRemoteCancel 呼入振铃中对端取消 → 未接来电。
func TestMissedOnRemoteCancel(t *testing.T) {
	app, ch := newMissedTestApp()
	app.OnIncoming("c1", "1002", "张三", false)
	app.OnTerminated("c1", "remote_cancel")

	m := endEvent(t, ch, "call.terminated")
	if !missedOf(t, m) {
		t.Fatalf("对端取消应判为未接来电: %v", m)
	}
	if m["reason"] != "remote_cancel" {
		t.Fatalf("reason 应原样带上，实际 %v", m["reason"])
	}
}

// TestNotMissedWhenHandledLocally 本端拒接 / 振铃中本端挂断 → 不算未接
// （用户当场处理了，不该再弹桌面通知）。
func TestNotMissedWhenHandledLocally(t *testing.T) {
	for _, reason := range []string{"rejected", "local_hangup"} {
		app, ch := newMissedTestApp()
		app.OnIncoming("c1", "1002", "", false)
		app.OnTerminated("c1", reason)
		if missedOf(t, endEvent(t, ch, "call.terminated")) {
			t.Fatalf("reason=%s 是本端主动结束，不应判为未接", reason)
		}
	}
}

// TestNotMissedWhenEstablished 呼入接通后再挂断 → 不是未接。
func TestNotMissedWhenEstablished(t *testing.T) {
	app, ch := newMissedTestApp()
	app.OnIncoming("c1", "1002", "", false)
	app.OnEstablished("c1")
	app.OnTerminated("c1", "remote_hangup")
	if missedOf(t, endEvent(t, ch, "call.terminated")) {
		t.Fatal("已接通挂断不是未接来电")
	}
}

// TestNotMissedForOutgoing 呼出未接（对方没接）不是未接来电。
func TestNotMissedForOutgoing(t *testing.T) {
	app, ch := newMissedTestApp()
	app.upsertCall(&Call{ID: "o1", Direction: "outgoing", Remote: "1003", State: CallRinging})
	app.OnTerminated("o1", "remote_hangup")
	if missedOf(t, endEvent(t, ch, "call.terminated")) {
		t.Fatal("呼出未接通不是未接来电")
	}
}

// TestMissedIdempotent 同一通话重复上报结束（sip 层可能 OnFailed 后再 OnTerminated）
// 只有第一条判未接——否则界面会重复弹通知。
func TestMissedIdempotent(t *testing.T) {
	app, ch := newMissedTestApp()
	app.OnIncoming("c1", "1002", "", false)
	app.OnTerminated("c1", "remote_cancel")
	if !missedOf(t, endEvent(t, ch, "call.terminated")) {
		t.Fatal("第一条结束事件应判为未接")
	}
	app.OnTerminated("c1", "remote_cancel")
	if missedOf(t, endEvent(t, ch, "call.terminated")) {
		t.Fatal("重复的结束事件不应再判为未接")
	}
}

// TestMissedOnIncomingFailed 呼入未接通就失败（协商/媒体失败）→ 未接。
func TestMissedOnIncomingFailed(t *testing.T) {
	app, ch := newMissedTestApp()
	app.OnIncoming("c1", "1002", "", false)
	app.OnFailed("c1", "failed")
	if !missedOf(t, endEvent(t, ch, "call.failed")) {
		t.Fatal("呼入未接通失败应判为未接")
	}
}

// TestNotMissedAfterEstablishedFailed 接通后失败（如媒体异常）不是未接。
func TestNotMissedAfterEstablishedFailed(t *testing.T) {
	app, ch := newMissedTestApp()
	app.OnIncoming("c1", "1002", "", false)
	app.OnEstablished("c1")
	app.OnFailed("c1", "failed")
	if missedOf(t, endEvent(t, ch, "call.failed")) {
		t.Fatal("已接通后失败不是未接来电")
	}
}

// TestEndUnknownCallIgnored 未知通话的结束上报不产生事件（呼入早期失败发生在
// OnIncoming 之前，不能因此误报未接）。
func TestEndUnknownCallIgnored(t *testing.T) {
	app, ch := newMissedTestApp()
	app.OnTerminated("nope", "failed")
	app.OnFailed("nope", "failed")
	select {
	case ev := <-ch:
		t.Fatalf("未知通话不应产生事件，实际收到 %s", ev.Name)
	default:
	}
}
