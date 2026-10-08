package main

import (
	"path/filepath"
	"testing"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

// newTestApp 构造最小可用的 core.App：tel 为 nil（UpdateAccount 仍落盘、跳过注册），
// 配置保存路径指向临时目录（UpdateAccount / SetUpdateConfig 都写它）。
func newTestApp(t *testing.T) (*core.App, *config.Config) {
	t.Helper()
	cfg := config.Default()
	app := core.New(cfg, nil, core.NewBus())
	app.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	return app, cfg
}

// TestHandleLaunchArgsUpdateSite sip:// 链接的 updatesite 参数（REQ-UPD-022）：
// 带参数 → 只改更新站点 URL（应用 ID/周期/预览版保持原值）；省略或非法站点 →
// 更新配置不变、账户照常更新（非法只告警，不中断拉起）。
func TestHandleLaunchArgsUpdateSite(t *testing.T) {
	const site = "https://update.wuzm219.cn"

	// 带 updatesite：只改 URL，其余字段保持原值
	app, cfg := newTestApp(t)
	if err := app.SetUpdateConfig(config.UpdateConfig{URL: "", AppID: "x-app", IntervalMins: 30, Prerelease: true}); err != nil {
		t.Fatalf("预置更新配置失败: %v", err)
	}
	handleLaunchArgs(app, cfg, []string{"sip://1001:pwd@10.0.0.1:5060?updatesite=" + site}, nil)
	got := app.UpdateConfig()
	if got.URL != site {
		t.Fatalf("更新站点应为 %q，实际 %q", site, got.URL)
	}
	if got.AppID != "x-app" || got.IntervalMins != 30 || !got.Prerelease {
		t.Fatalf("updatesite 只应改 URL，实际 AppID=%q IntervalMins=%d Prerelease=%v", got.AppID, got.IntervalMins, got.Prerelease)
	}
	if cfg.Account.Server != "10.0.0.1" || cfg.Account.Account != "1001" || cfg.Account.Port != 5060 {
		t.Fatalf("账户应照常更新，实际 %+v", cfg.Account)
	}

	// 省略 updatesite：更新配置一字不变
	app2, cfg2 := newTestApp(t)
	if err := app2.SetUpdateConfig(config.UpdateConfig{URL: site, AppID: "x-app", IntervalMins: 30, Prerelease: true}); err != nil {
		t.Fatalf("预置更新配置失败: %v", err)
	}
	before := app2.UpdateConfig()
	handleLaunchArgs(app2, cfg2, []string{"sip://1001:pwd@10.0.0.1:5060"}, nil)
	if after := app2.UpdateConfig(); after != before {
		t.Fatalf("不带 updatesite 不应改更新配置：before=%+v after=%+v", before, after)
	}

	// 非法站点（ftp://）：更新配置不变，账户照常更新
	app3, cfg3 := newTestApp(t)
	if err := app3.SetUpdateConfig(config.UpdateConfig{URL: site, AppID: "x-app", IntervalMins: 30, Prerelease: true}); err != nil {
		t.Fatalf("预置更新配置失败: %v", err)
	}
	before3 := app3.UpdateConfig()
	handleLaunchArgs(app3, cfg3, []string{"sip://1001:pwd@10.0.0.1:5060?updatesite=ftp://x"}, nil)
	if after := app3.UpdateConfig(); after != before3 {
		t.Fatalf("非法站点不应改更新配置：before=%+v after=%+v", before3, after)
	}
	if cfg3.Account.Server != "10.0.0.1" || cfg3.Account.Account != "1001" {
		t.Fatalf("非法站点不应中断账户拉起，实际 %+v", cfg3.Account)
	}

	// 与 transport/instance 组合（带启动器前缀，模拟 Windows 协议拉起）
	app4, cfg4 := newTestApp(t)
	handleLaunchArgs(app4, cfg4,
		[]string{"mti-sip-phone:sips://2999:pwd@10.168.4.40?transport=udp&instance=2&updatesite=" + site}, nil)
	if got := app4.UpdateConfig(); got.URL != site {
		t.Fatalf("组合参数下更新站点应为 %q，实际 %q", site, got.URL)
	}
}
