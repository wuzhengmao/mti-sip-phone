//go:build !windows

package ui

import "fyne.io/fyne/v2"

// notifyPlatform 发送桌面通知（非 Windows）：走 Fyne 的 D-Bus 实现
// （org.freedesktop.Notifications）。没有通知守护进程时 Fyne 只记一条
// 日志，不影响其它功能。
//
// 注意 Fyne 这条路径是**同步**调用，调用方必须放后台 goroutine
// （见 notifyMissedCall 的说明）。
func notifyPlatform(title, body string) {
	fyne.CurrentApp().SendNotification(&fyne.Notification{Title: title, Content: body})
}
