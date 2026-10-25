package wsapi

import (
	"testing"
	"time"

	"github.com/coder/websocket"

	"mti-sip-phone/internal/core"
)

// waitFor 轮询直到条件成立或超时（事件链路是异步的，直接断言会竞态）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待条件超时（%v）", timeout)
}

// TestUnregisterOnDisconnect REQ-WS-020：断线自动取消注册的完整流程——
// ①断开 → 延迟确认 → 注销；②重连 → 恢复注册；③通话中断开 → 排队，
// 话务结束后才注销；④排队期间重连 → 取消排队任务，话务结束也不注销。
func TestUnregisterOnDisconnect(t *testing.T) {
	old := unregDelayValue()
	setUnregDelay(100 * time.Millisecond)
	t.Cleanup(func() { setUnregDelay(old) })

	conn, app, _, url := newControlServerURL(t)
	enabled := func() bool { return app.ConfigSnapshot().Account.Enabled }

	// 预置一个合法账户（启用空账户会被配置校验拦下，恢复注册就没法验证）
	if resp := request(t, conn, MethodSetAccount, map[string]any{
		"enabled": true, "server": "10.0.0.1", "account": "1001",
	}); resp.OK == nil || !*resp.OK {
		t.Fatalf("预置账户失败: %+v", resp.Error)
	}

	// 开启策略
	if resp := request(t, conn, MethodSetUnregisterOnDisconnect, map[string]any{"enabled": true}); resp.OK == nil || !*resp.OK {
		t.Fatalf("开启断线自动注销失败: %+v", resp.Error)
	}

	// ① 断开 → 延迟确认后自动注销
	_ = conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, 3*time.Second, func() bool { return !enabled() })

	// ② 重连 → 恢复注册
	conn2 := dialWS(t, url)
	waitFor(t, 3*time.Second, enabled)

	// ③ 通话中断开 → 排队，话务结束后才注销
	app.OnIncoming("c1", core.IncomingCall{Remote: "2901"})
	_ = conn2.Close(websocket.StatusNormalClosure, "")
	time.Sleep(400 * time.Millisecond) // 超过延迟窗口：应仍在排队（未注销）
	if !enabled() {
		t.Fatal("通话中不应立即注销（应在话务结束后）")
	}
	app.OnTerminated("c1", "normal")
	waitFor(t, 3*time.Second, func() bool { return !enabled() })

	// ④ 排队期间重连 → 取消排队任务，话务结束也不注销
	conn3 := dialWS(t, url)
	waitFor(t, 3*time.Second, enabled)
	app.OnIncoming("c2", core.IncomingCall{Remote: "2901"})
	_ = conn3.Close(websocket.StatusNormalClosure, "")
	time.Sleep(400 * time.Millisecond) // 排队中（通话未结束）
	if !enabled() {
		t.Fatal("通话中不应立即注销")
	}
	_ = dialWS(t, url) // 重连：取消排队
	waitFor(t, 3*time.Second, enabled)
	app.OnTerminated("c2", "normal")
	time.Sleep(400 * time.Millisecond)
	if !enabled() {
		t.Fatal("排队期间重连后，话务结束不应再注销")
	}
}

// TestSetUnregisterOnDisconnectInvalidParams 缺 enabled → invalid_params。
func TestSetUnregisterOnDisconnectInvalidParams(t *testing.T) {
	conn, _, _ := newControlServer(t)
	resp := request(t, conn, MethodSetUnregisterOnDisconnect, map[string]any{})
	if resp.OK != nil && *resp.OK {
		t.Fatal("缺 enabled 应报 invalid_params")
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("错误码应为 invalid_params，实际 %+v", resp.Error)
	}
}
