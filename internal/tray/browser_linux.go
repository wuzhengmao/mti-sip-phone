//go:build linux

package tray

import (
	"fmt"
	"os/exec"
)

// OpenBrowser 用系统默认浏览器打开 url（xdg-open）。
func OpenBrowser(url string) error {
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return fmt.Errorf("找不到 xdg-open，无法打开浏览器: %w", err)
	}
	return exec.Command("xdg-open", url).Start()
}
