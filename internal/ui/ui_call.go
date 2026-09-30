package ui

import (
	"errors"
	"fmt"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
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
	w.SetIcon(IconPhoneGreen) // 任务栏绿色话机（不继承应用红色图标）
	// 无标题栏、无关闭按钮，仅显示窗体内容（REQ-UI-024 修订）：
	// fyne 公开 API 无 SetDecorated，窗口装饰在 Show 后由
	// positionCallWindow（stripCallWindowChrome）通过 Win32 样式移除
	// 关闭面板（Alt+F4 等）= 挂断
	w.SetCloseIntercept(func() {
		if a.activeCallID != "" {
			_ = a.core.Hangup(a.activeCallID)
		}
	})

	a.callNameLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	a.callStateLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{})
	a.callTimerLabel = widget.NewLabelWithStyle("00:00", fyne.TextAlignLeading, fyne.TextStyle{})

	// 紧凑按钮（36×36，REQ-UI-025 修订）：fyne Button 跟随主题内边距
	// 高达 48px，用自定义 compactButton 缩小面板按钮
	a.hangupBtn = newCompactButton(IconStopWhite, colorDangerRed, func() {
		if a.activeCallID != "" {
			_ = a.core.Hangup(a.activeCallID)
		}
	})

	// 绿色电话：接听
	a.answerBtn = newCompactButton(IconPhoneWhite, colorSuccessGreen, func() {
		if a.activeCallID != "" {
			if err := a.core.Answer(a.activeCallID); err != nil {
				logx.Logger().Warn("接听失败", "err", err)
			}
		}
	})

	// 黄色 ||：通话保持切换（保持语义：本端停止发送音频，再次点击恢复）
	a.holdBtn = newCompactButton(IconPauseWhite, colorHoldYellow, func() {
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

	// 扬声器开关（REQ-UI-023）：默认开=蓝底白图标，关=灰底红图标
	a.speakerBtn = newCompactButton(IconSpeakerOn, colorHoldBlue, a.toggleSpeaker)

	// 麦克风开关（REQ-UI-023）
	a.micBtn = newCompactButton(IconMicOn, colorHoldBlue, a.toggleMic)

	// 三行内容容器：内容在每次进入状态时由 rebuildCallRows 重建。
	// 用 Stack：振铃闪烁描边矩形与内容平铺为兄弟、撑满整个窗口，
	// 描边贴窗口最外缘（REQ-UI-025 修订）；内容层为 Border（窗口
	// 内容会撑满画布，Border 的中心区→网格→三行等分填满窗口）
	a.callRows = container.NewStack()
	w.SetContent(a.callRows)
	// 关闭画布内边距：四周留白由 rebuildCallRows 统一控制，
	// 避免与主题 padding 叠加导致视觉不一致（REQ-UI-025 修订）
	w.SetPadded(false)
	// 尺寸与 topmost_windows.go 的 callPanelWidth/Height 保持一致；
	// 窗口尺寸由 positionCallWindow 通过窗口过程钳制精确固定
	w.Resize(fyne.NewSize(260, 126))

	// 通话面板始终置顶
	if dw, ok := w.(desktop.Window); ok {
		dw.RequestAlwaysOnTop()
	}
}

// startRingShake 振铃提醒：FlashWindowEx 任务栏闪烁 + SetForegroundWindow
// 抢前台 + 边框多色轮换闪烁（无边框窗口为内容描边闪烁，内容不随动）。
// 标题保持 "通话面板 - 号码"不变（各状态一致）。
func (a *App) startRingShake() {
	a.stopRingShake()
	a.pulseStop = make(chan struct{})
	stop := a.pulseStop

	// 无边框窗口内容描边闪烁：DWM 着色边框在无边框窗口上不可见，
	// 改为轮换 ringFlashRect 的描边颜色（REQ-UI-025 修订）
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fyne.Do(func() {
					if a.ringFlashRect == nil {
						return
					}
					a.ringFlashRect.StrokeColor = colorRefToNRGBA(ringBorderColors[i%len(ringBorderColors)])
					a.ringFlashRect.Refresh()
				})
				i++
			}
		}
	}()

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
//   - 行3：时长（通话中加扬声器/麦克风开关，靠右，REQ-UI-023）
func (a *App) rebuildCallRows(mode int) {
	if a.callRows == nil {
		return
	}
	a.callRows.Objects = nil

	row1 := container.NewHBox(centerBox(a.callNameLabel), layout.NewSpacer())
	row2 := container.NewHBox(centerBox(a.callStateLabel), layout.NewSpacer(), centerBox(a.hangupBtn))
	// 行3：时长 + 扬声器/麦克风开关（三态都显示，REQ-UI-023 修订）
	row3 := container.NewHBox(centerBox(a.callTimerLabel), layout.NewSpacer(),
		centerBox(a.speakerBtn), centerBox(a.micBtn))

	switch mode {
	case dialingButtons:
		a.stopRingShake()
		a.ringFlashRect = nil
	case ringingButtons:
		row1.Add(centerBox(a.answerBtn))
		// 无边框窗口的 DWM 着色边框不可见：改为在内容上叠加描边矩形
		// 做边框闪烁（REQ-UI-025 修订）
		a.ringFlashRect = newRingFlashRect()
		a.startRingShake()
	case incallButtons:
		a.stopRingShake()
		a.ringFlashRect = nil
		row1.Add(centerBox(a.holdBtn))
		// 通话建立：把会话内记忆的开关状态下发到当前通话引擎
		//（引擎按通话创建，每次通话建立需重新下发）
		a.applySessionAudioState()
	}

	// 布局（REQ-UI-025 修订）：左右 8px 留白、行间 2px 空隙、上下
	// 0px——三行由 GridWithRows 三等分撑满窗口（fyne/GLFW 附加的
	// "幽灵标题栏高度"吸收进行高），行内容垂直居中余量 ≈7px，
	// 加上下 1px 行距垫 = 8px，与左右留白视觉一致。
	row1P := vPad(row1, 1)
	row2P := vPad(row2, 1)
	row3P := vPad(row3, 1)
	grid := container.NewGridWithRows(3, row1P, row2P, row3P)

	// 内容层：四周 4px 留白的 Border，中心为三行网格
	border := container.NewBorder(padRect(4), padRect(4), padRect(4), padRect(4), grid)

	// 振铃态：闪烁描边矩形与内容层平铺为兄弟（矩形撑满整个窗口，
	// 描边贴最外缘；点击命中会跳过非 Tappable 对象，不挡按钮）
	if a.ringFlashRect != nil {
		a.callRows.Objects = []fyne.CanvasObject{border, a.ringFlashRect}
	} else {
		a.callRows.Objects = []fyne.CanvasObject{border}
	}
	a.callRows.Refresh()
}

// vPad 给对象上下加指定透明留白（两行各 1px 拼出 2px 行距）。
func vPad(obj fyne.CanvasObject, s float32) fyne.CanvasObject {
	return container.NewBorder(padRect(s), padRect(s), nil, nil, obj)
}

// centerBox 内容保持自然尺寸并在行内垂直居中——Grid 撑高行时
// HBox 会把子组件纵向拉伸到行高，包一层 Center 避免按钮被拉长
//（REQ-UI-025 修订）。
func centerBox(obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewCenter(obj)
}

// 通话面板按钮配色（与主界面主题色观感一致）。
var (
	colorDangerRed    = color.NRGBA{R: 0xe5, G: 0x39, B: 0x35, A: 0xff} // 挂断
	colorSuccessGreen = color.NRGBA{R: 0x43, G: 0xa0, B: 0x47, A: 0xff} // 接听
	colorHoldYellow   = color.NRGBA{R: 0xfb, G: 0xc0, B: 0x2d, A: 0xff} // 保持
	colorHoldBlue     = color.NRGBA{R: 0x21, G: 0x96, B: 0xf3, A: 0xff} // 取消保持/开关开
	colorSwitchGray   = color.NRGBA{R: 0xbd, G: 0xbd, B: 0xbd, A: 0xff} // 开关关
)

// compactButton 通话面板紧凑按钮（REQ-UI-025 修订）：36×36
//（20px 图标 + 8px 内边距），圆角底色 + 居中图标。
// fyne Button 的尺寸跟随主题内边距（拨号盘主题为 12px，按钮高达
// 48px），无法单独缩小，故用自定义组件。
type compactButton struct {
	widget.BaseWidget
	bg    *canvas.Rectangle
	icon  *canvas.Image
	onTap func()
}

func newCompactButton(iconRes fyne.Resource, bgColor color.NRGBA, onTap func()) *compactButton {
	b := &compactButton{onTap: onTap}
	b.bg = canvas.NewRectangle(bgColor)
	b.bg.CornerRadius = 6
	b.bg.SetMinSize(fyne.NewSize(36, 36))
	b.icon = canvas.NewImageFromResource(iconRes)
	b.icon.FillMode = canvas.ImageFillContain
	b.icon.SetMinSize(fyne.NewSize(20, 20))
	b.ExtendBaseWidget(b)
	return b
}

func (b *compactButton) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewStack(b.bg, container.NewCenter(b.icon)))
}

// SetIcon 更新图标。
func (b *compactButton) SetIcon(res fyne.Resource) {
	b.icon.Resource = res
	b.icon.Refresh()
}

// SetBg 更新底色。
func (b *compactButton) SetBg(c color.NRGBA) {
	b.bg.FillColor = c
	b.bg.Refresh()
}

func (b *compactButton) Tapped(*fyne.PointEvent) {
	if b.onTap != nil {
		b.onTap()
	}
}

func (b *compactButton) Cursor() desktop.Cursor { return desktop.PointerCursor }

// 振铃边框闪烁颜色序列（COLORREF 即 0x00BBGGRR）：红、橙、绿、青、蓝、紫。
// 无边框通话面板用内容描边闪烁（DWM 着色边框在无边框窗口上不可见）。
var ringBorderColors = []uint32{
	0x000000FF, // 红
	0x0000A5FF, // 橙
	0x0000FF00, // 绿
	0x00FFFF00, // 青
	0x00FF0000, // 蓝
	0x00FF00FF, // 紫
}

// colorRefToNRGBA COLORREF（0x00BBGGRR）转 color.NRGBA。
func colorRefToNRGBA(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c), G: uint8(c >> 8), B: uint8(c >> 16), A: 0xFF}
}

// newRingFlashRect 振铃边框闪烁描边矩形：透明填充 + 1px 彩色描边 +
// 2px 圆角，随网格铺满面板内缘（REQ-UI-025 修订：细边框+圆角）。
func newRingFlashRect() *canvas.Rectangle {
	r := canvas.NewRectangle(color.Transparent)
	r.StrokeColor = colorRefToNRGBA(ringBorderColors[0])
	r.StrokeWidth = 1
	r.CornerRadius = 2
	return r
}

// padRect 指定尺寸的透明占位矩形。
func padRect(s float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(s, s))
	return r
}

// toggleSpeaker 扬声器开关切换（REQ-UI-023）。
// 引擎存在则立即生效（呼叫状态 183 早期媒体下即可屏蔽回铃音）；
// 引擎未建立（呼入振铃/呼出 180 阶段）时 ErrCallNotFound 静默忽略，
// 状态在通话建立时由 applySessionAudioState 统一下发。
func (a *App) toggleSpeaker() {
	a.speakerOn = !a.speakerOn
	a.applySpeakerButton()
	if a.activeCallID != "" {
		if err := a.core.MuteSpeaker(a.activeCallID, !a.speakerOn); err != nil && !errors.Is(err, core.ErrCallNotFound) {
			logx.Logger().Warn("扬声器切换失败", "err", err)
		}
	}
}

// toggleMic 麦克风开关切换（REQ-UI-023）。下发时机同 toggleSpeaker。
func (a *App) toggleMic() {
	a.micOn = !a.micOn
	a.applyMicButton()
	if a.activeCallID != "" {
		if err := a.core.MuteMic(a.activeCallID, !a.micOn); err != nil && !errors.Is(err, core.ErrCallNotFound) {
			logx.Logger().Warn("麦克风切换失败", "err", err)
		}
	}
}

// applySessionAudioState 把会话内记忆的扬声器/麦克风状态应用到当前通话。
// 通话结束后引擎随之销毁，新通话建立时重新下发（状态本身不重置）。
// 引擎尚未创建（呼入振铃）时静默跳过，接通后再次调用即可生效。
func (a *App) applySessionAudioState() {
	if a.activeCallID == "" {
		return
	}
	if err := a.core.MuteSpeaker(a.activeCallID, !a.speakerOn); err != nil && !errors.Is(err, core.ErrCallNotFound) {
		logx.Logger().Warn("应用扬声器状态失败", "err", err)
	}
	if err := a.core.MuteMic(a.activeCallID, !a.micOn); err != nil && !errors.Is(err, core.ErrCallNotFound) {
		logx.Logger().Warn("应用麦克风状态失败", "err", err)
	}
}

// applySpeakerButton 按开关状态更新扬声器按钮外观（REQ-UI-023 修订）：
// 开 = 蓝色底 + 白色图标；关 = 灰色底 + 红色图标。
func (a *App) applySpeakerButton() {
	if a.speakerBtn == nil {
		return
	}
	if a.speakerOn {
		a.speakerBtn.SetIcon(IconSpeakerOn)
		a.speakerBtn.SetBg(colorHoldBlue)
	} else {
		a.speakerBtn.SetIcon(IconSpeakerOff)
		a.speakerBtn.SetBg(colorSwitchGray)
	}
	a.speakerBtn.Refresh()
}

// applyMicButton 按开关状态更新麦克风按钮外观（同扬声器：开蓝底白图标、关灰底红图标）。
func (a *App) applyMicButton() {
	if a.micBtn == nil {
		return
	}
	if a.micOn {
		a.micBtn.SetIcon(IconMicOn)
		a.micBtn.SetBg(colorHoldBlue)
	} else {
		a.micBtn.SetIcon(IconMicOff)
		a.micBtn.SetBg(colorSwitchGray)
	}
	a.micBtn.Refresh()
}

// setHoldButton 保持状态变化时更新保持按钮外观与状态文案。
func (a *App) setHoldButton(muted bool) {
	if a.holdBtn == nil {
		return
	}
	if muted {
		a.holdBtn.SetIcon(IconReplayWhite) // 保持中：恢复图标
		a.holdBtn.SetBg(colorHoldBlue)
		if a.callStateLabel != nil {
			a.callStateLabel.SetText("通话保持中")
		}
	} else {
		a.holdBtn.SetIcon(IconPauseWhite)
		a.holdBtn.SetBg(colorHoldYellow)
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
