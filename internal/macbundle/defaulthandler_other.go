//go:build !darwin || !cgo

package macbundle

import "fmt"

// setDefaultHandler 非 macOS（或未启用 cgo）没有 LaunchServices。
func setDefaultHandler(scheme, bundleID string) error {
	return fmt.Errorf("当前平台不支持设置默认 URL 处理器（%s://）", scheme)
}
