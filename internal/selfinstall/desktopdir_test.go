package selfinstall

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExpandHome user-dirs.dirs 的 $HOME / ${HOME} 前缀展开。
func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("取不到用户主目录")
	}
	if got := expandHome("$HOME/桌面"); got != filepath.Join(home, "桌面") {
		t.Fatalf("$HOME 展开应为 %q，实际 %q", filepath.Join(home, "桌面"), got)
	}
	if got := expandHome("${HOME}/Desktop"); got != filepath.Join(home, "Desktop") {
		t.Fatalf("${HOME} 展开应为 %q，实际 %q", filepath.Join(home, "Desktop"), got)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Fatalf("绝对路径应原样返回，实际 %q", got)
	}
}

// TestDesktopDirFromUserDirs 本地化桌面目录（"$HOME/桌面"）能解析出来。
func TestDesktopDirFromUserDirs(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("取不到用户主目录")
	}
	content := "XDG_DESKTOP_DIR=\"$HOME/桌面\"\nXDG_DOWNLOAD_DIR=\"$HOME/下载\"\n"
	if err := os.WriteFile(filepath.Join(base, "user-dirs.dirs"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "桌面")
	if got := desktopDirFromUserDirs(); got != want {
		t.Fatalf("解析结果应为 %q，实际 %q", want, got)
	}
}

// TestDesktopDirFromUserDirsDisabled 显式禁用（值为空）→ 空串。
func TestDesktopDirFromUserDirsDisabled(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	if err := os.WriteFile(filepath.Join(base, "user-dirs.dirs"),
		[]byte("XDG_DESKTOP_DIR=\"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := desktopDirFromUserDirs(); got != "" {
		t.Fatalf("禁用时应为空串，实际 %q", got)
	}
}

// TestDesktopDirFromUserDirsMissing 没有 user-dirs.dirs → 空串。
func TestDesktopDirFromUserDirsMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := desktopDirFromUserDirs(); got != "" {
		t.Fatalf("无文件时应为空串，实际 %q", got)
	}
}

// TestResolveDesktopDir user-dirs.dirs 指向一个真实存在的目录 → 命中。
// （Windows 上 xdg-user-dir 不存在会自然落到解析分支，结果一致。）
func TestResolveDesktopDir(t *testing.T) {
	desk := t.TempDir() // 真实存在的"桌面目录"
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	if err := os.WriteFile(filepath.Join(base, "user-dirs.dirs"),
		[]byte("XDG_DESKTOP_DIR=\""+desk+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveDesktopDir(); got != desk {
		t.Fatalf("桌面目录应为 %q，实际 %q", desk, got)
	}
}

// TestIsDir 目录判定。
func TestIsDir(t *testing.T) {
	if !isDir(t.TempDir()) {
		t.Fatal("临时目录应判定为目录")
	}
	if isDir(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("不存在的路径不应判定为目录")
	}
}
