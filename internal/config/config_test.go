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
