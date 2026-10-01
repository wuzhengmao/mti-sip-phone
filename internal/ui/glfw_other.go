//go:build !linux

package ui

// prepareGLFW 非 Linux 平台无需预设 GLFW 提示：Windows 用 Win32 托盘与
// 窗口装饰，不涉及 Wayland/libdecor（Linux 侧实现见 glfw_linux.go）。
func prepareGLFW() {}
