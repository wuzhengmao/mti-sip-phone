package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 自动更新配置（REQ-UPD-017）的默认值与旧配置回补。

// TestUpdateDefaults 默认：站点留空（不检查）、应用 ID 与周期给默认、不接受预览版。
func TestUpdateDefaults(t *testing.T) {
	u := Default().Update
	if u.URL != "" {
		t.Fatalf("默认不应配置更新站点，实际 %q", u.URL)
	}
	if u.AppID != "mti-sip-phone" || u.IntervalMins != 5 || u.Prerelease {
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
	if cfg.Update.AppID != "mti-sip-phone" || cfg.Update.IntervalMins != 5 {
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
	if cfg.Update.AppID != "mti-sip-phone" {
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
