package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mti-sip-phone/internal/variant"
)

// 自动更新配置（REQ-UPD-017）的默认值与旧配置回补。

// TestUpdateDefaults 默认：站点留空（不检查）、应用 ID 与周期给默认、不接受预览版。
func TestUpdateDefaults(t *testing.T) {
	u := Default().Update
	if u.URL != "" {
		t.Fatalf("默认不应配置更新站点，实际 %q", u.URL)
	}
	if u.AppID != variant.UpdateAppID || u.IntervalMins != 5 || u.Prerelease {
		t.Fatalf("默认值不对: %+v", u)
	}
	if u.Enabled() {
		t.Fatal("未配站点时 Enabled 应为 false")
	}
	if got := u.Interval(); got != 5*time.Minute {
		t.Fatalf("周期应为 5 分钟，实际 %s", got)
	}
}

// TestUpdateLoadLegacy 旧配置里没有 update 段：整段取默认，而不是留成零值
//（零值会让 AppID 为空、周期为 0）。
func TestUpdateLoadLegacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"version":1,"account":{"server":"10.0.0.1","account":"1001"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Update.AppID != variant.UpdateAppID || cfg.Update.IntervalMins != 5 {
		t.Fatalf("旧配置应回补默认值，实际 %+v", cfg.Update)
	}

	// 配置里写了 update 段（哪怕只写了站点）时，其它字段各自回补 + 周期钳制
	part := `{"version":1,"update":{"url":"https://update.example.com","intervalMinutes":0}}`
	if err := os.WriteFile(path, []byte(part), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Update.URL != "https://update.example.com" {
		t.Fatalf("站点应保留，实际 %q", cfg.Update.URL)
	}
	if cfg.Update.AppID != variant.UpdateAppID {
		t.Fatalf("缺 AppID 时应回补默认，实际 %q", cfg.Update.AppID)
	}
	if cfg.Update.IntervalMins != 5 {
		t.Fatalf("周期为 0 时应回补默认 5，实际 %d", cfg.Update.IntervalMins)
	}
}

// TestClampUpdateIntervalMins 周期钳制：<=0 取默认 5，越界取边界。
func TestClampUpdateIntervalMins(t *testing.T) {
	cases := map[int]int{
		0: 5, -3: 5,
		1: 1, 30: 30, 1440: 1440,
		1441: 1440, 99999: 1440,
	}
	for in, want := range cases {
		if got := ClampUpdateIntervalMins(in); got != want {
			t.Fatalf("Clamp(%d) = %d，期望 %d", in, got, want)
		}
	}
}

// TestUpdateAppIDVariantSwap 变体应用 ID（REQ-UPD-021）：两个变体共用一份配置，
// 配置里残留"另一个变体的默认应用 ID"时按未设置处理、改回本变体默认；用户手填的
// 其它应用 ID 一律保留。
func TestUpdateAppIDVariantSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// ① 另一个变体留下的默认值 → 改写成本变体默认
	other := fmt.Sprintf(`{"version":1,"update":{"appId":%q}}`, variant.OtherUpdateAppID)
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Update.AppID != variant.UpdateAppID {
		t.Fatalf("另一个变体的默认应用 ID 应改写为 %q，实际 %q", variant.UpdateAppID, cfg.Update.AppID)
	}

	// ② 用户手填的第三方应用 ID → 原样保留
	custom := `{"version":1,"update":{"appId":"my-own-app"}}`
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Update.AppID != "my-own-app" {
		t.Fatalf("自定义应用 ID 应保留，实际 %q", cfg.Update.AppID)
	}
}
