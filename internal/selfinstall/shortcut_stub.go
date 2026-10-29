//go:build !windows && !linux && !darwin

package selfinstall

// createDesktopShortcut 其它平台无桌面快捷方式实现（本项目只发布
// Windows / Linux / macOS 产物，这里仅为编译完整性）。
func createDesktopShortcut(string) error { return nil }
