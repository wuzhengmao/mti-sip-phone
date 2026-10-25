//go:build !windows && !linux

package ui

import "fyne.io/fyne/v2"

// 通话面板尺寸（DIP）：各平台文件各自定义（Windows/Linux 见各自文件），
// 便于按平台观感独立微调。

const (
	callPanelWidth      = 260
	callPanelHeight     = 144
	callPanelButtonSize = 36
)

// 更新提示窗尺寸（DIP）。
const (
	updatePanelWidth  = 340
	updatePanelHeight = 240
)

// setTopMost 其他平台的置顶控制：暂用 Fyne 的置顶能力（不可撤销），
// 后续版本按窗口管理器实现可撤销置顶。
func setTopMost(on bool) {
	// 空实现：窗口置顶依赖 WM 支持，后续版本补齐
}

func moveCallWindowToCorner(w fyne.Window) {}

// placeVideoWindow 其他平台不做定位/尺寸控制（窗口保持默认）。
func placeVideoWindow(w fyne.Window) {}

// moveUpdateWindowToCorner 其他平台不做定位（窗口保持默认居中）。
func moveUpdateWindowToCorner(w fyne.Window) {}

// showUpdateWindow 显示更新提示窗（其他平台无任务栏按钮问题，直接显示）。
func showUpdateWindow(w fyne.Window) { w.Show() }

// showCallWindow 显示通话面板（其他平台无需为任务栏做额外处理）。
func showCallWindow(w fyne.Window) { w.Show() }

// updateWindowTitle 更新提示窗标题（与 Windows 一致，中文）。
func updateWindowTitle() string { return "更新提示" }

// callWindowTitle 通话面板标题（与 Windows 一致，中文）。
func callWindowTitle(account string) string {
	title := "通话面板"
	if account != "" {
		title += " - " + account
	}
	return title
}

// videoWindowTitle 视频窗口标题。
func videoWindowTitle(account string) string {
	title := "视频通话"
	if account != "" {
		title += " - " + account
	}
	return title
}

// raiseVideoWindow 其他平台不做窗口提升（同 setTopMost 的取舍）。
func raiseVideoWindow(fyne.Window, bool) {}

// setVideoWindowTopmost 其他平台不做窗口置顶（同 setTopMost 的取舍）。
func setVideoWindowTopmost(fyne.Window, bool) {}

// markTrayQuit 其他平台不打 IsQuit 标记（Fyne 会自动补 Quit 项）。
func markTrayQuit(item *fyne.MenuItem) {}

func findWindowByTitlePrefix(prefix string) uintptr { return 0 }

// screenWorkArea 屏幕工作区尺寸（物理像素）：其他平台取不到，
// 用一个保守默认值（视频窗口仍会按对端比例摆放）。
func screenWorkArea() (float32, float32) { return 1280, 800 }

// ---- 窗口位置记忆与锁定（REQ-UI-040 / REQ-VIDEO-015）：其他平台全部空实现 ----
// 锁按钮照常显示与切换（只影响观感），位置一律按默认摆放。

// beginCallPanelDrag 其他平台不支持拖动窗口。
func beginCallPanelDrag(w fyne.Window) {}

// endCallPanelDrag 其他平台没有拖动会话，读不到位置。
func endCallPanelDrag(w fyne.Window) (x, y int, ok bool) { return 0, 0, false }

// cancelCallPanelDrag 其他平台无拖动会话可取消。
func cancelCallPanelDrag(w fyne.Window) {}

// moveCallPanelToSaved 其他平台不做定位（窗口保持默认）。
func moveCallPanelToSaved(w fyne.Window, x, y int) {}

// moveVideoWindowToSaved 其他平台不做定位（窗口保持默认）。
func moveVideoWindowToSaved(w fyne.Window, x, y int) {}

// setVideoWindowLock 其他平台无法拦截 WM 的移动/缩放（同 setTopMost 的取舍）。
func setVideoWindowLock(w fyne.Window, locked bool) {}

// SetVideoWindowMoveHook 其他平台不会报告用户拖动，存下也只是占位（保持接口一致）。
func SetVideoWindowMoveHook(fn func(x, y int)) {}

// startVideoWindowWatch / stopVideoWindowWatch 其他平台没有位置守卫。
func startVideoWindowWatch(w fyne.Window) {}
func stopVideoWindowWatch(w fyne.Window)  {}

// suspendVideoWindowLockClamp / resumeVideoWindowLockClamp 其他平台没有锁定钳制。
func suspendVideoWindowLockClamp() {}
func resumeVideoWindowLockClamp()  {}

// screenBounds 屏幕边界（物理像素）：其他平台取不到，给一个保守默认值，
// windowPosUsable 会据此判断保存的位置是否可用。
func screenBounds() (x, y, w, h int) { return 0, 0, 1280, 800 }

func flashWindowEx(hwnd uintptr)         {}
func stopFlashWindowEx(hwnd uintptr)     {}
func setForegroundWindow(hwnd uintptr)   {}
func forceForegroundWindow(hwnd uintptr) {}

func ringBorderFlash(hwnd uintptr, stop <-chan struct{}) {}

// startTrayIconPromotion 其他平台没有"通知区常显"的设置项（托盘由桌面环境管理）。
func startTrayIconPromotion() {}
