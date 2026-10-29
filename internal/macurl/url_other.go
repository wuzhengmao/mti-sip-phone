//go:build !darwin || !cgo

package macurl

// start 非 macOS 平台没有苹果事件（URL 走命令行参数，见 protocol.FindURL）。
func start() {}

// SetHandler 非 macOS 平台为空操作。
func SetHandler(func(string)) {}
