package wsapi

import "testing"

// TestRegisterUnregisterMethods WS 的登录/退出账号（REQ-ACC-024 /
// REQ-WS-021）：与托盘菜单一致——共用「启用账户」配置项（落盘）并触发
// 注册/注销；返回参数确认当前配置状态。
func TestRegisterUnregisterMethods(t *testing.T) {
	conn, app, _ := newControlServer(t)

	// 预置一个有效账户（否则注册分支永远走"缺字段→注销"）
	if resp := request(t, conn, MethodSetAccount, map[string]any{
		"enabled": true, "server": "10.0.0.1", "account": "1001",
	}); resp.OK == nil || !*resp.OK {
		t.Fatalf("预置账户失败: %+v", resp.Error)
	}

	// 退出账号：启用账户=false + 触发注销
	resp := request(t, conn, MethodUnregister, map[string]any{})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("system.unregister 失败: %+v", resp.Error)
	}
	if app.Config().Account.Enabled {
		t.Fatal("退出账号后「启用账户」应为 false")
	}

	// 登录账号：启用账户=true（并立即注册）
	resp = request(t, conn, MethodRegister, map[string]any{})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("system.register 失败: %+v", resp.Error)
	}
	if !app.Config().Account.Enabled {
		t.Fatal("登录账号后「启用账户」应为 true")
	}
}
