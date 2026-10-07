//go:build !windows && !linux

package tray

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser 其它平台：macOS 用 open，其余尝试 xdg-open。
// （本项目只发布 Windows 与 Linux 产物，这里只为编译完整性。）
func OpenBrowser(url string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", url).Start()
	}
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return fmt.Errorf("当前平台不支持自动打开浏览器: %w", err)
	}
	return exec.Command("xdg-open", url).Start()
}
