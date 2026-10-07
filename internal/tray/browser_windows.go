//go:build windows

package tray

import (
	"golang.org/x/sys/windows"
)

// OpenBrowser 用系统默认浏览器打开 url。
//
// 走 shell32 的 ShellExecute（与资源管理器双击一个 URL 同一路径），而不是
// `cmd /c start`：托盘版是 GUI 子系统构建（-H=windowsgui），挂子进程会闪出
// 一个控制台窗口；ShellExecute 只投递一次打开请求给外壳，不留进程。
func OpenBrowser(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
