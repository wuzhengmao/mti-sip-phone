package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestChkOptDefaultOn 服务端心跳检测默认开启（REQ-ACC-020）：
// Load 是先取 Default() 再反序列化覆盖，所以旧配置文件里没有 optchk 字段时
// 必须仍为 true（升级上来的用户不用手动勾选）。
func TestChkOptDefaultOn(t *testing.T) {
	if !Default().Account.ChkOpt {
		t.Fatal("默认配置应启用服务端心跳检测")
	}

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
	if !cfg.Account.ChkOpt {
		t.Fatal("旧配置缺少 optchk 字段时应按默认开启")
	}

	off := `{"version":1,"account":{"server":"10.0.0.1","account":"1001","optchk":false}}`
	if err := os.WriteFile(path, []byte(off), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Account.ChkOpt {
		t.Fatal("配置里显式关闭 optchk 时不应被默认值覆盖")
	}
}

// TestInstanceDir 多实例沙箱目录（REQ-NFR-007）：
// 槽位 0 = 原目录（**位置不变**，向后兼容）；槽位 ≥1 = <原目录>/instances/<N>/ 且被创建。
func TestInstanceDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("APPDATA", base)         // Windows：os.UserConfigDir 读 APPDATA
	t.Setenv("XDG_CONFIG_HOME", base) // Linux

	root, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := InstanceDir(0); err != nil || got != root {
		t.Fatalf("槽位 0 应返回原目录 %q，实际 %q err=%v", root, got, err)
	}
	if got, err := InstanceDir(-1); err != nil || got != root {
		t.Fatalf("负槽位应按主实例处理，实际 %q err=%v", got, err)
	}
	dir, err := InstanceDir(2)
	if err != nil {
		t.Fatalf("创建沙箱目录失败: %v", err)
	}
	if want := filepath.Join(root, "instances", "2"); dir != want {
		t.Fatalf("槽位 2 目录应为 %q，实际 %q", want, dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("槽位目录应已创建: %v", err)
	}
}
