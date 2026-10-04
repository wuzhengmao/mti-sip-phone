package core

// 自动更新配置（REQ-UPD-017/018）：校验、钳制、落盘、下发给查询器、广播事件。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mti-sip-phone/internal/config"
)

// fakeUpdater 记录下发与查询调用（其他方法只为满足接口）。
type fakeUpdater struct {
	site     SiteConfigSnapshot
	hasSite  bool
	checkRes CheckResult
	checkErr error
}

func (f *fakeUpdater) Notify(UpdateInfo) error              { return nil }
func (f *fakeUpdater) Download() error                      { return nil }
func (f *fakeUpdater) Cancel()                              {}
func (f *fakeUpdater) Apply() error                         { return nil }
func (f *fakeUpdater) Status() UpdateStatus                 { return UpdateStatus{Phase: UpdateIdle} }
func (f *fakeUpdater) SetUpdateConfig(s SiteConfigSnapshot) { f.site, f.hasSite = s, true }
func (f *fakeUpdater) CheckNow(ctx context.Context) (CheckResult, error) {
	return f.checkRes, f.checkErr
}

func TestSetUpdateConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := config.Default()
	app := New(cfg, nil, NewBus())
	app.SetConfigPath(path)
	sub := app.Bus().Subscribe()
	upd := &fakeUpdater{}
	app.SetUpdater(upd)

	// 站点地址非法 → 拒绝且不写盘
	if err := app.SetUpdateConfig(config.UpdateConfig{URL: "ftp://x.example"}); err == nil {
		t.Fatal("非 http/https 站点应被拒绝")
	}

	// 正常保存：周期越界被钳制、应用 ID 空则补默认
	if err := app.SetUpdateConfig(config.UpdateConfig{
		URL: "https://update.example.com/", AppID: "  ", IntervalMins: 99999,
	}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got := app.UpdateConfig()
	if got.AppID != "mti-sip-phone" || got.IntervalMins != config.UpdateIntervalMaxMins {
		t.Fatalf("应补默认/钳制，实际 %+v", got)
	}

	// 落盘（重新加载能看到）
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if loaded.Update.URL != "https://update.example.com/" {
		t.Fatalf("配置未落盘，实际 %+v", loaded.Update)
	}

	// 下发给查询器（core 不直接查站点，交给 updater）
	if !upd.hasSite || upd.site.URL != "https://update.example.com/" ||
		upd.site.Interval != time.Duration(config.UpdateIntervalMaxMins)*time.Minute {
		t.Fatalf("未正确下发给查询器: %+v", upd.site)
	}

	// 广播 system.updateConfig
	select {
	case ev := <-sub:
		if ev.Name != "system.updateConfig" {
			t.Fatalf("期望 system.updateConfig，实际 %s", ev.Name)
		}
		c, ok := ev.Data.(config.UpdateConfig)
		if !ok || c.URL != "https://update.example.com/" {
			t.Fatalf("事件负载不对: %#v", ev.Data)
		}
	default:
		t.Fatal("未收到 system.updateConfig 事件")
	}

	// State() 带上当前配置（网页重连即可拿到）
	if st := app.State(); st.UpdateConfig.URL != "https://update.example.com/" {
		t.Fatalf("State 应包含 updateConfig，实际 %+v", st.UpdateConfig)
	}
}

// TestSetUpdateConfigEmptyURLOK 站点清空是合法操作（= 停止自动检查）。
func TestSetUpdateConfigEmptyURLOK(t *testing.T) {
	app := New(config.Default(), nil, NewBus())
	upd := &fakeUpdater{}
	app.SetUpdater(upd)
	if err := app.SetUpdateConfig(config.UpdateConfig{URL: "", AppID: "x", IntervalMins: 5}); err != nil {
		t.Fatalf("清空站点应允许: %v", err)
	}
	if upd.site.URL != "" {
		t.Fatalf("应把空站点下发（停表），实际 %+v", upd.site)
	}
}

// TestSetUpdateConfigNoUpdater 未接入更新能力时也应能保存（只是不能查询/提示）。
func TestSetUpdateConfigNoUpdater(t *testing.T) {
	app := New(config.Default(), nil, NewBus())
	if err := app.SetUpdateConfig(config.UpdateConfig{URL: "https://x.example", IntervalMins: 1}); err != nil {
		t.Fatalf("未接入 updater 时保存不应报错: %v", err)
	}
	if _, err := app.CheckUpdateNow(context.Background()); err == nil {
		t.Fatal("未接入 updater 时立即检查应报错")
	}
}

// TestCheckUpdateNow 转发给 updater 的立即检查。
func TestCheckUpdateNow(t *testing.T) {
	app := New(config.Default(), nil, NewBus())
	app.SetUpdater(&fakeUpdater{checkRes: CheckResult{UpToDate: true, LatestVersion: "1.0.9"}})
	res, err := app.CheckUpdateNow(context.Background())
	if err != nil || !res.UpToDate || res.LatestVersion != "1.0.9" {
		t.Fatalf("应立即检查并返回结果，实际 %+v err=%v", res, err)
	}
	if res.Text() != "已是最新版本" {
		t.Fatalf("文案不对: %s", res.Text())
	}
}
