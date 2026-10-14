package selfinstall

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// resolveDesktopDir 解析用户桌面目录（供 Linux 的 .desktop 快捷方式用）：
// 依次尝试 `xdg-user-dir DESKTOP`、~/.config/user-dirs.dirs 的
// XDG_DESKTOP_DIR、~/Desktop，取第一个**存在且是目录**的。
//
// 桌面目录不能只猜 ~/Desktop：本地化系统里它叫「桌面」/「Schreibtisch」等
// （只有 user-dirs.dirs 记录真实位置），且 XDG_DESKTOP_DIR 环境变量在桌面
// 环境里通常不存在。取不到返回空串（调用方静默跳过，不随便建目录）。
func resolveDesktopDir() string {
	// 1. xdg-user-dir DESKTOP（xdg-user-dirs 包，Debian/Ubuntu 桌面环境基本都有）
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	out, err := exec.CommandContext(ctx, "xdg-user-dir", "DESKTOP").Output()
	cancel()
	if err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" && isDir(dir) {
			return dir
		}
	}
	// 2. user-dirs.dirs（XDG_DESKTOP_DIR 可能是本地化目录名，如 "$HOME/桌面"）
	if dir := desktopDirFromUserDirs(); dir != "" && isDir(dir) {
		return dir
	}
	// 3. ~/Desktop（英文系统兜底）
	if home, err := os.UserHomeDir(); err == nil {
		if dir := filepath.Join(home, "Desktop"); isDir(dir) {
			return dir
		}
	}
	return ""
}

// desktopDirFromUserDirs 从 user-dirs.dirs 解析 XDG_DESKTOP_DIR。
// 解析失败或显式禁用（值为空）返回空串。
func desktopDirFromUserDirs() string {
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		cfgHome = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(cfgHome, "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "XDG_DESKTOP_DIR=") {
			continue
		}
		val := strings.TrimPrefix(line, "XDG_DESKTOP_DIR=")
		val = strings.Trim(strings.TrimSpace(val), `"`)
		if val == "" {
			return "" // 显式禁用桌面目录
		}
		return expandHome(val)
	}
	return ""
}

// expandHome 展开 user-dirs.dirs 取值里的 $HOME / ${HOME} 前缀。
func expandHome(s string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return s
	}
	if strings.HasPrefix(s, "$HOME/") {
		return filepath.Join(home, s[len("$HOME/"):])
	}
	if strings.HasPrefix(s, "${HOME}/") {
		return filepath.Join(home, s[len("${HOME}/"):])
	}
	return s
}

// isDir 判断路径存在且是目录。
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
