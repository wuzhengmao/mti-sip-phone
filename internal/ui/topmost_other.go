//go:build !windows && !linux

package ui

import "fyne.io/fyne/v2"

// 通话面板尺寸（DIP）：各平台文件各自定义（Windows/Linux 见各自文件），
// 便于按平台观感独立微调。

const (
	callPanelWidth      = 260
	callPanelHeight     = 126
	callPanelButtonSize = 36
)

// setTopMost 其他平台的置顶控制：暂用 Fyne 的置顶能力（不可撤销），
// 后续版本按窗口管理器实现可撤销置顶。
func setTopMost(on bool) {
	// 空实现：窗口置顶依赖 WM 支持，后续版本补齐
}

func moveCallWindowToCorner(w fyne.Window) {}

// callWindowTitle 通话面板标题（与 Windows 一致，中文）。
func callWindowTitle(account string) string {
	title := "通话面板"
	if account != "" {
		title += " - " + account
	}
	return title
}

// markTrayQuit 其他平台不打 IsQuit 标记（Fyne 会自动补 Quit 项）。
func markTrayQuit(item *fyne.MenuItem) {}

func findWindowByTitlePrefix(prefix string) uintptr { return 0 }

func flashWindowEx(hwnd uintptr)         {}
func stopFlashWindowEx(hwnd uintptr)     {}
func setForegroundWindow(hwnd uintptr)   {}
func forceForegroundWindow(hwnd uintptr) {}

func ringBorderFlash(hwnd uintptr, stop <-chan struct{}) {}
