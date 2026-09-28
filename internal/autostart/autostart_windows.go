//go:build windows

// Package autostart 开机自动启动（REQ-UI-022）：
// Windows 通过 HKCU Run 注册表项，Linux 通过 XDG autostart desktop 文件。
package autostart

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "mti-sip-phone"
)

// Enable 注册开机自动启动（当前用户）。
func Enable(exePath string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开注册表 Run 键失败: %w", err)
	}
	defer k.Close()
	return k.SetStringValue(valueName, `"`+exePath+`"`)
}

// Disable 移除开机自动启动（不存在时视为已移除）。
func Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	if err := k.DeleteValue(valueName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("删除注册表项失败: %w", err)
	}
	return nil
}
