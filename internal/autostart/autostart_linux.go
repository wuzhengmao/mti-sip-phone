//go:build linux

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

// desktopPath XDG autostart 文件路径：~/.config/autostart/mti-sip-phone.desktop。
func desktopPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("获取用户配置目录失败: %w", err)
	}
	return filepath.Join(dir, "autostart", "mti-sip-phone.desktop"), nil
}

// Enable 注册开机自动启动（写入 XDG autostart 文件）。
func Enable(exePath string) error {
	path, err := desktopPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 autostart 目录失败: %w", err)
	}
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=mti-sip-phone
Comment=SIP 软电话
Exec="%s"
Terminal=false
X-GNOME-Autostart-enabled=true
`, exePath)
	return os.WriteFile(path, []byte(content), 0o644)
}

// Disable 移除开机自动启动（文件不存在时视为已移除）。
func Disable() error {
	path, err := desktopPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 autostart 文件失败: %w", err)
	}
	return nil
}
