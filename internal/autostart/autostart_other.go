//go:build !windows && !linux && !darwin

package autostart

// Enable 其他平台暂不支持开机自动启动。
func Enable(exePath string) error { return nil }

// Disable 其他平台无操作。
func Disable() error { return nil }
