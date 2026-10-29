//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// label LaunchAgents 里的任务标识（与 bundle 标识一致）。
const label = "cn.mti.sip-phone"

// plistPath 用户级 LaunchAgent 路径：~/Library/LaunchAgents/<label>.plist。
func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户主目录失败: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// Enable 注册开机自动启动（写 LaunchAgent plist，登录时运行）。
// 只写文件、不动 launchd 的已加载状态：下次登录由 launchd 自己拉起；
// 立即 bootstrap 会在程序运行时再拉起一份，没有意义（还会撞单实例锁）。
func Enable(exePath string) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 LaunchAgents 目录失败: %w", err)
	}
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`, label, xmlEscape(exePath))
	return os.WriteFile(path, []byte(content), 0o644)
}

// Disable 移除开机自动启动（文件不存在时视为已移除）。
func Disable() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 LaunchAgent 失败: %w", err)
	}
	return nil
}

// xmlEscape 转义 XML 文本（路径里可能有 & < >）。
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
