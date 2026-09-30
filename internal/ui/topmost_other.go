//go:build !windows

package ui

import "fyne.io/fyne/v2"

// setTopMost Linux 等平台的置顶控制：暂用 Fyne 的置顶能力（不可撤销），
// 后续版本按窗口管理器实现可撤销置顶。
func setTopMost(on bool) {
	// 空实现：Linux 下窗口置顶依赖 WM 支持，后续版本补齐
}

func moveCallWindowToCorner(w fyne.Window) {}

func findWindowByTitlePrefix(prefix string) uintptr { return 0 }

func getWindowPos(hwnd uintptr) (x, y int) { return 0, 0 }

func setWindowPosXY(hwnd uintptr, x, y int) {}

func flashWindowEx(hwnd uintptr)     {}
func stopFlashWindowEx(hwnd uintptr) {}
func setForegroundWindow(hwnd uintptr) {}
func forceForegroundWindow(hwnd uintptr) {}

func ringBorderFlash(hwnd uintptr, stop <-chan struct{}) {}
