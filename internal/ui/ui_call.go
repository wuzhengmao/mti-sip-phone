package ui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 通话面板按钮组合（三态）。
const (
	dialingButtons = iota // 呼叫中：仅红色挂断
	ringingButtons        // 振铃：绿色接听 + 红色挂断
	incallButtons         // 通话中：黄色 || + 红色挂断
)

// buildCallPanelIfNeeded 创建通话面板（小窗口、置顶）。
// 布局（三行，REQ-UI-008 再次修订）：
//   - 呼叫：行1 对端号码；行2 "呼叫中"+挂断；行3 拨号时长
//   - 振铃：行1 对端号码+接听；行2 "来电"+挂断；行3 振铃时长
//   - 通话：行1 对端号码+保持；行2 "通话中"+挂断；行3 通话时长
// 行内容按状态动态重建（rebuildCallRows）。
func (a *App) buildCallPanelIfNeeded() {
	if a.callW != nil {
		return
	}
	w := a.fyne.NewWindow("通话")
	a.callW = w
	w.SetIcon(IconPhoneGreen) // 标题栏绿色话机（不继承应用红色图标）
	// 关闭面板 = 挂断
	w.SetCloseIntercept(func() {
		if a.activeCallID != "" {
			_ = a.core.Hangup(a.activeCallID)
		}
	})

	a.callNameLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	a.callStateLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{})
	a.callTimerLabel = widget.NewLabelWithStyle("00:00", fyne.TextAlignLeading, fyne.TextStyle{})

	// 红色方块：挂断（DangerImportance 红色填充 + 停止图标）
	a.hangupBtn = widget.NewButtonWithIcon("", theme.MediaStopIcon(), func() {
		if a.activeCallID != "" {
			_ = a.core.Hangup(a.activeCallID)
		}
	})
	a.hangupBtn.Importance = widget.DangerImportance

	// 绿色电话：接听
	a.answerBtn = widget.NewButtonWithIcon("", IconPhoneBlack, func() {
		if a.activeCallID != "" {
			if err := a.core.Answer(a.activeCallID); err != nil {
				logx.Logger().Warn("接听失败", "err", err)
			}
		}
	})
	a.answerBtn.Importance = widget.SuccessImportance

	// 黄色 ||：通话保持切换（保持语义：本端停止发送音频，再次点击恢复）
	a.holdBtn = widget.NewButtonWithIcon("", theme.MediaPauseIcon(), func() {
		if a.activeCallID == "" {
			return
		}
		// 以核心状态为准判断当前是否保持（此前按按钮局部状态计算恒为 true，
		// 导致再次点击无法取消保持——bug 修复）
		muted := a.core.IsCallMuted(a.activeCallID)
		if err := a.core.Mute(a.activeCallID, !muted); err != nil {
			logx.Logger().Warn("保持切换失败", "err", err)
		}
	})
	a.holdBtn.Importance = widget.WarningImportance

	// 三行内容容器：内容在每次进入状态时由 rebuildCallRows 重建
	a.callRows = container.NewVBox()
	w.SetContent(a.callRows)
	// 宽度需容纳完整标题"通话面板 - 1001"（最小化/最大化按钮移除后
	// 释放非客户区空间，标题不再被截断）
	w.Resize(fyne.NewSize(260, 156))

	// 通话面板始终置顶
	if dw, ok := w.(desktop.Window); ok {
		dw.RequestAlwaysOnTop()
	}
}

// startRingShake 振铃提醒：FlashWindowEx 任务栏闪烁 + SetForegroundWindow
// 抢前台 + DWM 边框多色轮换闪烁（内容不随动）。标题保持
// "通话面板 - 号码"不变（各状态一致）。
func (a *App) startRingShake() {
	a.stopRingShake()
	a.pulseStop = make(chan struct{})
	// 窗口句柄可能在 Show 后异步创建，带重试
	go func() {
		var hwnd uintptr
		for i := 0; i < 20; i++ {
			hwnd = findWindowByTitlePrefix("通话面板")
			if hwnd != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if hwnd == 0 {
			return
		}
		flashWindowEx(hwnd)
		setForegroundWindow(hwnd)
		ringBorderFlash(hwnd, a.pulseStop) // 阻塞至停止，结束后恢复边框默认色
		stopFlashWindowEx(hwnd)
	}()
}

// stopRingShake 停止窗口闪烁。
func (a *App) stopRingShake() {
	if a.pulseStop != nil {
		close(a.pulseStop)
		a.pulseStop = nil
	}
}

// updateCallWindowTitle 通话面板标题："通话面板 - 本机号码"。
func (a *App) updateCallWindowTitle() {
	if a.callW == nil {
		return
	}
	account := a.core.State().Register.Account
	title := "通话面板"
	if account != "" {
		title += " - " + account
	}
	a.callW.SetTitle(title)
}

// rebuildCallRows 按状态重建通话面板三行内容：
//   - 行1：对端号码（振铃加接听按钮、通话加保持按钮，靠右）
//   - 行2：状态文字 + 挂断按钮（挂断恒在行2最右）
//   - 行3：时长
func (a *App) rebuildCallRows(mode int) {
	if a.callRows == nil {
		return
	}
	a.callRows.Objects = nil

	row1 := container.NewHBox(a.callNameLabel, layout.NewSpacer())
	row2 := container.NewHBox(a.callStateLabel, layout.NewSpacer(), a.hangupBtn)
	row3 := container.NewHBox(a.callTimerLabel)

	switch mode {
	case dialingButtons:
		a.stopRingShake()
	case ringingButtons:
		row1.Add(a.answerBtn)
		a.startRingShake()
	case incallButtons:
		a.stopRingShake()
		row1.Add(a.holdBtn)
	}

	a.callRows.Add(row1)
	a.callRows.Add(row2)
	a.callRows.Add(row3)
	a.callRows.Refresh()
}

// setHoldButton 保持状态变化时更新黄色 || 按钮外观与状态文案。
func (a *App) setHoldButton(muted bool) {
	if a.holdBtn == nil {
		return
	}
	if muted {
		a.holdBtn.SetIcon(theme.MediaReplayIcon()) // 保持中：恢复图标
		a.holdBtn.Importance = widget.HighImportance
		if a.callStateLabel != nil {
			a.callStateLabel.SetText("通话保持中")
		}
	} else {
		a.holdBtn.SetIcon(theme.MediaPauseIcon())
		a.holdBtn.Importance = widget.WarningImportance
		if a.callStateLabel != nil {
			a.callStateLabel.SetText("通话中")
		}
	}
	a.holdBtn.Refresh()
}

// startTimer 启动计时：更新通话面板的时长标签。
func (a *App) startTimer() {
	a.stopTimer()
	a.callStartAt = time.Now()
	a.timerStop = make(chan struct{})
	if a.callTimerLabel != nil {
		a.callTimerLabel.SetText("00:00")
	}
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-a.timerStop:
				return
			case <-ticker.C:
				sec := int(time.Since(a.callStartAt).Seconds())
				fyne.Do(func() {
					if a.callTimerLabel != nil && a.uiState != uiIdle {
						a.callTimerLabel.SetText(fmt.Sprintf("%02d:%02d", sec/60, sec%60))
					}
				})
			}
		}
	}()
}

// stopTimer 停止计时。
func (a *App) stopTimer() {
	if a.timerStop != nil {
		close(a.timerStop)
		a.timerStop = nil
	}
}

// restoreIdle 通话结束：隐藏通话面板、显示主窗口、恢复空闲样式。
// 同时复原保持按钮（图标/颜色/状态文案）——保持是每通通话的临时
// 状态，通话结束后必须复位，否则下次通话沿用上次的保持态（bug 修复）。
func (a *App) restoreIdle() {
	a.stopTimer()
	a.stopRingShake()
	a.setHoldButton(false)
	a.activeCallID = ""
	a.activeCall = nil
	a.setCallUI(uiIdle, "")
}

// showCallFailed 呼叫失败提示。按用户约定：通话面板关闭后若主窗口
// 恢复显示，则在主窗口中弹出呼叫失败错误窗口；若主窗口保持隐藏
//（托盘常驻场景，从未显示），则不弹窗，失败原因仅记录日志——
// 避免以隐藏/未初始化的窗口为父窗口弹窗导致崩溃。
func (a *App) showCallFailed(reason string) {
	a.stopTimer()
	a.restoreIdle()

	if !a.mainShown {
		logx.Logger().Info("呼叫失败（主窗口保持隐藏，不弹窗）", "reason", reason)
		return
	}

	// 红色 ✕ 图标（RichText 着色，不依赖主题图标资源渲染）
	xMark := widget.NewRichText(
		&widget.TextSegment{
			Text: "✕",
			Style: widget.RichTextStyle{
				ColorName: theme.ColorNameError,
				TextStyle: fyne.TextStyle{Bold: true},
			},
		},
	)
	reasonLabel := widget.NewLabelWithStyle(reason, fyne.TextAlignLeading, fyne.TextStyle{})
	reasonLabel.Wrapping = fyne.TextWrapWord

	content := container.NewHBox(xMark, reasonLabel)
	// 此时主窗口已显示（mainShown 为真），以主窗口为父窗口弹窗安全
	d := dialog.NewCustom("呼叫失败", "确定", content, a.mainW)
	d.Resize(fyne.NewSize(260, 92))
	a.withDialogTheme(d)
	d.Show()
}

// withDialogTheme 弹窗显示期间切换为默认主题（弹窗 innerPadding 用默认值，
// 与主窗体的 dialTheme 分离），关闭后恢复。
// Fyne 主题为应用级，只能通过切换实现分离。
func (a *App) withDialogTheme(d dialog.Dialog) {
	a.fyne.Settings().SetTheme(theme.DefaultTheme())
	d.SetOnClosed(func() {
		a.fyne.Settings().SetTheme(&dialTheme{})
	})
}

// showInfo 信息弹窗（按钮文案"确定"，覆盖 Fyne 中文 locale 的"好"）。
func (a *App) showInfo(title, content string) {
	d := dialog.NewInformation(title, content, a.mainW)
	d.SetDismissText("确定")
	a.withDialogTheme(d)
	d.Show()
}

// showError 错误弹窗（按钮文案"确定"）。
func (a *App) showError(err error) {
	d := dialog.NewError(err, a.mainW)
	d.SetDismissText("确定")
	a.withDialogTheme(d)
	d.Show()
}

// showConfirm 确认弹窗（确定 / 取消）。
func (a *App) showConfirm(title, content string, cb func(bool)) {
	d := dialog.NewConfirm(title, content, cb, a.mainW)
	d.SetConfirmText("确定")
	d.SetDismissText("取消")
	a.withDialogTheme(d)
	d.Show()
}

// callRemoteName 通话对象的展示名称（显示名优先，其次号码）。
func callRemoteName(call *core.Call) string {
	if call.RemoteName != "" {
		return call.RemoteName
	}
	return call.Remote
}
