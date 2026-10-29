//go:build !darwin

package protocol

import "fmt"

// registerDarwin 非 macOS 平台没有 .app 应用包 / LaunchServices
//（实现见 register_darwin.go）。
func registerDarwin(exePath string) error {
	return fmt.Errorf("当前平台不支持 macOS 应用包协议注册")
}
