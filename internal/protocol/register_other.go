//go:build !windows

package protocol

import "fmt"

// registerWindows 非 Windows 平台无注册表实现。
// RegisterProtocol 按 runtime.GOOS 分发，仅 Windows 分支会调用；
// 此桩仅为满足编译（协议注册的 Windows 常量与实现均在
// register_windows.go，避免非 Windows 平台导入 registry 包）。
func registerWindows(exePath string) error {
	return fmt.Errorf("当前平台不支持 Windows 注册表协议注册")
}

// registerDarwin 实现在 register_darwin.go（构建标签 darwin）。
