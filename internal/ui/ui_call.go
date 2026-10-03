package ui

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
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
//
// 行内容按状态动态重建（rebuildCallRows）。
func (a *App) buildCallPanelIfNeeded() {
	if a.callW != nil {
		return
	}
	// 通话面板用 Fyne 的 **splash 窗口**（无边框），而不是先建普通窗口再
	// 用 Win32 `stripWindowChrome` 硬抹样式。
	//
	// 后者是**绕过 GLFW** 改窗口的：GLFW 内部缓存的边框尺寸因此永久失准，
	// 它仍按"有标题栏"换算客户区，Fyne 于是认为画布比真实客户区高出一整条
	// 标题栏（"幽灵标题栏"），内容整体下移、**第一行被顶出可视区**——且现象
	// 时有时无（来电振铃时 `ringFlashRect` 每 75ms 重绘会掩盖，一点接听就露馅）。
	// splash 窗口由 GLFW 在**创建时**就按无边框处理（WS_POPUP，frameRect 归零），
	// 缓存始终自洽，画布 == 客户区。
	w := a.newBorderlessWindow("通话")
	a.callW = w
	// 窗口图标固定绿色话机（不继承应用红色图标）；面板在 Windows 上按工具窗口
	// 显示、不占任务栏，见 topmost_windows.go 的 showCallWindow。
	w.SetIcon(IconPhoneGreen)
	// 无标题栏、无关闭按钮，仅显示窗体内容（REQ-UI-024 修订）：
	// fyne 公开 API 无 SetDecorated，无装饰由 splash 窗口保证（见上）。
	// 关闭面板（Alt+F4 等）= 挂断
	w.SetCloseIntercept(func() {
		if a.activeCallID != "" {
			_ = a.core.Hangup(a.activeCallID)
		}
	})
	// 通话面板上也支持敲键盘数字：向对端发 DTMF + 本机回显按键音
	//（REQ-CALL-006 / REQ-UI-029）。与主窗体同理，只有"没有控件获得焦点"时
	// 才会走到 canvas 回调（面板上没有输入框，正常都走这里）。
	w.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if isDialDigit(ev.Name) {
			a.onKeypadDigit(string(ev.Name))
		}
	})

	a.callNameLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	a.callStateLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{})
	a.callTimerLabel = widget.NewLabelWithStyle("00:00", fyne.TextAlignLeading, fyne.TextStyle{})

	// 紧凑按钮（尺寸取平台常量，REQ-UI-025 修订）：fyne Button 跟随主题
	// 内边距高达 48px，用自定义 compactButton 缩小面板按钮
	a.hangupBtn = newCompactButton(IconStopWhite, colorDangerRed, callPanelButtonSize, func() {
		if a.activeCallID != "" {
			_ = a.core.Hangup(a.activeCallID)
		}
	})

	// 绿色电话：接听
	a.answerBtn = newCompactButton(IconPhoneWhite, colorSuccessGreen, callPanelButtonSize, func() {
		if a.activeCallID != "" {
			if err := a.core.Answer(a.activeCallID); err != nil {
				logx.Logger().Warn("接听失败", "err", err)
			}
		}
	})

	// 黄色 ||：通话保持切换（保持语义：本端停止发送音频，再次点击恢复）
	a.holdBtn = newCompactButton(IconPauseWhite, colorHoldYellow, callPanelButtonSize, func() {
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
	a.speakerBtn = newCompactButton(IconSpeakerOn, colorHoldBlue, callPanelButtonSize, a.toggleSpeaker)

	// 麦克风开关（REQ-UI-023）
	a.micBtn = newCompactButton(IconMicOn, colorHoldBlue, callPanelButtonSize, a.toggleMic)

	// 视频接听（REQ-VIDEO-004）：来电含视频时显示在「接听」左侧
	a.videoAnswerBtn = newCompactButton(IconVideoAnswer, colorSuccessGreen, callPanelButtonSize, func() {
		if a.activeCallID == "" {
			return
		}
		if err := a.core.AnswerVideo(a.activeCallID); err != nil {
			logx.Logger().Warn("视频接听失败", "err", err)
		}
	})

	// 关闭视频开关（REQ-VIDEO-008）：位于扬声器左侧
	a.videoBtn = newCompactButton(IconVideoOn, colorHoldBlue, callPanelButtonSize, func() {
		a.setVideoEnabled(!a.videoOn)
	})

	// 三行内容容器：内容在每次进入状态时由 rebuildCallRows 重建。
	// 用 Stack：振铃底色闪烁矩形与内容层平铺为兄弟、撑满整个窗口
	//（REQ-UI-025 修订）；内容层为 Border（窗口
	// 内容会撑满画布，Border 的中心区→网格→三行等分填满窗口）
	a.callRows = container.NewStack()
	w.SetContent(a.callRows)
	// 关闭画布内边距：四周留白由 rebuildCallRows 统一控制，
	// 避免与主题 padding 叠加导致视觉不一致（REQ-UI-025 修订）
	w.SetPadded(false)
	// 尺寸取平台常量（Windows 见 topmost_windows.go，Linux 见
	// topmost_linux.go）；Windows 下窗口尺寸再由 positionCallWindow
	// 通过窗口过程钳制精确固定
	// 尺寸固定（用户要求："把宽度和高度固定之后就不要让它变化了"）：
	// 保留标题栏 + Fyne 的 SetFixedSize，不再用 Win32 去装饰 + 尺寸钳制那套
	// （那套绕过 GLFW 改样式，会让 GLFW 的边框缓存失准、内容被顶掉第一行）。
	w.Resize(fyne.NewSize(callPanelWidth, callPanelHeight))
	w.SetFixedSize(true)

	// 通话面板始终置顶
	if dw, ok := w.(desktop.Window); ok {
		dw.RequestAlwaysOnTop()
	}
}

// startRingShake 振铃提醒：FlashWindowEx 任务栏闪烁 + SetForegroundWindow
// 抢前台 + 底色灰度轮换闪烁（内容不随动）。
// 标题保持 "通话面板 - 号码"不变（各状态一致）。
func (a *App) startRingShake(grays []color.NRGBA) {
	a.stopRingShake()
	a.pulseStop = make(chan struct{})
	stop := a.pulseStop

	// 无边框窗口底色灰度闪烁（DWM 着色边框不可见）：
	// 16 档往返脉动（1→16→15→…→1），75ms 一档，平滑无跳跃感
	//（REQ-UI-025 修订）
	go func() {
		ticker := time.NewTicker(75 * time.Millisecond)
		defer ticker.Stop()
		// 往返索引序列：0,1,…,15,14,…,1 循环
		order := make([]int, 0, 30)
		for i := 0; i < len(grays); i++ {
			order = append(order, i)
		}
		for i := len(grays) - 2; i >= 1; i-- {
			order = append(order, i)
		}
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
					a.ringFlashRect.FillColor = grays[order[i%len(order)]]
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

// updateCallWindowTitle 通话面板标题：文案按平台生成（见各平台
// topmost_*.go 的 callWindowTitle）——Windows 用「通话面板 - 号码」，
// Linux 用 ASCII 的「SIP - 号码」。
func (a *App) updateCallWindowTitle() {
	if a.callW == nil {
		return
	}
	a.callW.SetTitle(callWindowTitle(a.core.State().Register.Account))
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

	row1 := container.New(panelRowLayout{}, a.callNameLabel)
	row2 := container.New(panelRowLayout{}, a.callStateLabel, a.hangupBtn)
	// 视频开关**只在本次通话确实协商了视频时**才出现（纯音频通话放一个点了
	// 没反应的按钮没有意义）。
	row3 := container.New(panelRowLayout{}, a.callTimerLabel)
	if a.callHasVideo {
		row3.Add(a.videoBtn)
	}
	row3.Add(a.speakerBtn)
	row3.Add(a.micBtn)

	switch mode {
	case dialingButtons:
		a.stopRingShake()
		a.ringFlashRect = nil
	case ringingButtons:
		// REQ-VIDEO-004/005：来电 offer 含视频就显示「视频接听」——
		// **摄像头禁用时同样要显示**：那样接听照样能看远端视频，只是不推送
		// 本地（answer 里视频段为 recvonly）。
		if a.ringHasVideo {
			row1.Add(a.videoAnswerBtn)
		}
		row1.Add(a.answerBtn)
		// 无边框窗口的 DWM 着色边框不可见：改为整窗底色灰度闪烁
		//（REQ-UI-025 修订）
		grays := a.ringFlashGrays()
		a.ringFlashRect = newRingFlashRect(grays)
		a.startRingShake(grays)
	case incallButtons:
		a.stopRingShake()
		a.ringFlashRect = nil
		row1.Add(a.holdBtn)
		// 通话建立：把会话内记忆的开关状态下发到当前通话引擎
		//（引擎按通话创建，每次通话建立需重新下发）
		a.applySessionAudioState()
		a.applyVideoButton()
	}

	// 三行纵向排列（行距 panelRowGap）、整体在窗口内纵向居中；四周 4px 窗口留白
	rows := container.New(panelRowsLayout{}, row1, row2, row3)
	border := container.NewBorder(padRect(4), padRect(4), padRect(4), padRect(4), rows)

	// 振铃态：底色闪烁矩形与内容层平铺为兄弟（矩形撑满整个窗口，
	// 必须放在内容层**下方**——在上方会盖住文字与按钮）
	if a.ringFlashRect != nil {
		a.callRows.Objects = []fyne.CanvasObject{a.ringFlashRect, border}
	} else {
		a.callRows.Objects = []fyne.CanvasObject{border}
	}
	a.callRows.Refresh()
}

// panelRowGap 通话面板的行距（用户指定 2px）。
const panelRowGap = 2

// panelColGap 通话面板同一行内元素之间的间距（4px，与 Fyne 主题 theme.Padding 一致）。
const panelColGap = 4

// panelRowLayout 一行内容：首元素靠左，其余元素靠右、两两间隔 panelColGap，
// 全部纵向居中。**替代 container.NewHBox**——后者的子项间距固定取
// theme.Padding（本项目主题为 4），无法改成 2。
type panelRowLayout struct{}

func (panelRowLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var sz fyne.Size
	first := true
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		if !first {
			sz.Width += panelColGap
		}
		sz.Width += m.Width
		sz.Height = fyne.Max(sz.Height, m.Height)
		first = false
	}
	return sz
}

func (panelRowLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	// 其余元素从右往左摆
	x := size.Width
	for i := len(objs) - 1; i >= 1; i-- {
		o := objs[i]
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		x -= m.Width
		o.Resize(m)
		o.Move(fyne.NewPos(x, (size.Height-m.Height)/2))
		x -= panelColGap
	}
	// 首元素靠左
	if o := objs[0]; o.Visible() {
		m := o.MinSize()
		o.Resize(m)
		o.Move(fyne.NewPos(0, (size.Height-m.Height)/2))
	}
}

// panelRowsLayout 纵向排列若干行：行距 panelRowGap、横向拉满、整体纵向居中。
//
// **替代 GridWithRows**：后者会把格子拉伸填满可用高度，格子高出内容的部分
// 变成行间富余（实测行距 12px 而列距 4px，用户反馈行距偏大且不均匀），
// 且其内部间距同样固定取 theme.Padding。
type panelRowsLayout struct{}

func (panelRowsLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var sz fyne.Size
	first := true
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		if !first {
			sz.Height += panelRowGap
		}
		sz.Height += m.Height
		sz.Width = fyne.Max(sz.Width, m.Width)
		first = false
	}
	return sz
}

func (panelRowsLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	total := float32(0)
	first := true
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		if !first {
			total += panelRowGap
		}
		total += o.MinSize().Height
		first = false
	}
	y := (size.Height - total) / 2
	if y < 0 {
		y = 0
	}
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		h := o.MinSize().Height
		o.Resize(fyne.NewSize(size.Width, h))
		o.Move(fyne.NewPos(0, y))
		y += h + panelRowGap
	}
}

type vPadLayout struct{ pad float32 }

func (l vPadLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var sz fyne.Size
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		sz.Width = fyne.Max(sz.Width, m.Width)
		sz.Height = fyne.Max(sz.Height, m.Height)
	}
	sz.Height += 2 * l.pad
	return sz
}

func (l vPadLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		h := size.Height - 2*l.pad
		if h < 0 {
			h = 0
		}
		o.Move(fyne.NewPos(0, l.pad))
		o.Resize(fyne.NewSize(size.Width, h))
	}
}

// dialCallButtonHeight 主窗体「呼叫 / 视频呼叫」按钮高度（DIP）。
// REQ-VIDEO-003：为给视频按钮腾位置，呼叫按钮比原 36 略矮。
const dialCallButtonHeight = 32

// 通话面板按钮配色（与主界面主题色观感一致）。
var (
	colorDangerRed     = color.NRGBA{R: 0xe5, G: 0x39, B: 0x35, A: 0xff} // 挂断
	colorSuccessGreen  = color.NRGBA{R: 0x43, G: 0xa0, B: 0x47, A: 0xff} // 接听/呼叫
	colorHoldYellow    = color.NRGBA{R: 0xfb, G: 0xc0, B: 0x2d, A: 0xff} // 保持
	colorHoldBlue      = color.NRGBA{R: 0x21, G: 0x96, B: 0xf3, A: 0xff} // 取消保持/开关开
	colorSwitchGray    = color.NRGBA{R: 0xbd, G: 0xbd, B: 0xbd, A: 0xff} // 开关关
	colorTransformGray = color.NRGBA{R: 0x44, G: 0x48, B: 0x4f, A: 0xf2} // 旋转/镜像未生效（深灰，压在画面上）
	colorIconBlack     = color.NRGBA{R: 0x1a, G: 0x1a, B: 0x1a, A: 0xff} // 呼叫按钮文字（固定黑）
	colorIconWhite     = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // 挂断态文字（红底白字）
)

// compactButton 通话面板紧凑按钮（REQ-UI-025 修订）：默认 36×36
// （20px 图标 + 8px 内边距），圆角底色 + 居中图标，可选文字
// （主窗体呼叫按钮用，文字颜色固定不随主题变化）。
// fyne Button 的尺寸跟随主题内边距（拨号盘主题为 12px，按钮高达
// 48px），且文字颜色随主题（浅色主题下 Success 按钮文字为白色），
// 均无法单独控制，故用自定义组件。
// 尺寸由调用方指定：通话面板取平台常量 callPanelButtonSize（面板行高
// 由它决定），主窗体呼叫按钮固定 36。
type compactButton struct {
	widget.BaseWidget
	bg       *canvas.Rectangle
	icon     *canvas.Image
	label    *canvas.Text // 可选文字
	onTap    func()
	baseBg   color.NRGBA // 正常态背景色（禁用时置灰，恢复时还原）
	disabled bool
}

func newCompactButton(iconRes fyne.Resource, bgColor color.NRGBA, size float32, onTap func()) *compactButton {
	b := &compactButton{onTap: onTap, baseBg: bgColor}
	b.bg = canvas.NewRectangle(bgColor)
	b.bg.CornerRadius = 6
	b.bg.SetMinSize(fyne.NewSize(size, size))
	b.icon = canvas.NewImageFromResource(iconRes)
	b.icon.FillMode = canvas.ImageFillContain
	// 图标按比例缩放（36 → 20px，与原值一致）
	iconSize := float32(math.Round(float64(size) * 0.55))
	b.icon.SetMinSize(fyne.NewSize(iconSize, iconSize))
	b.ExtendBaseWidget(b)
	return b
}

// newCompactTextButton 带文字的紧凑按钮（主窗体呼叫按钮用）：
// 文字颜色固定传入值，不随主题变化。尺寸固定 36，不随平台变化。
func newCompactTextButton(text string, iconRes fyne.Resource, bgColor, labelColor color.NRGBA, onTap func()) *compactButton {
	b := newCompactButton(iconRes, bgColor, 36, onTap)
	b.label = canvas.NewText(text, labelColor)
	b.label.TextSize = 14
	return b
}

// newCompactTextOnlyButton 纯文字紧凑按钮（更新提示窗用）：无图标，
// 文字居中、四周留主题内边距，宽度随文字自适应；圆角比图标按钮更小
// （提示窗按钮观感更方正）。
func newCompactTextOnlyButton(text string, bgColor, labelColor color.NRGBA, onTap func()) *compactButton {
	b := &compactButton{onTap: onTap}
	b.bg = canvas.NewRectangle(bgColor)
	b.bg.CornerRadius = 3
	b.label = canvas.NewText(text, labelColor)
	b.label.TextSize = 14
	b.ExtendBaseWidget(b)
	return b
}

func (b *compactButton) CreateRenderer() fyne.WidgetRenderer {
	if b.label != nil && b.icon != nil {
		return widget.NewSimpleRenderer(container.NewStack(b.bg,
			container.NewCenter(container.NewHBox(
				container.NewCenter(b.icon), container.NewCenter(b.label)))))
	}
	if b.label != nil {
		return widget.NewSimpleRenderer(container.NewStack(b.bg,
			container.NewBorder(padRect(3), padRect(3), padRect(8), padRect(8), b.label)))
	}
	return widget.NewSimpleRenderer(container.NewStack(b.bg, container.NewCenter(b.icon)))
}

// SetIcon 更新图标（纯文字按钮忽略）。
func (b *compactButton) SetIcon(res fyne.Resource) {
	if b.icon == nil {
		return
	}
	b.icon.Resource = res
	b.icon.Refresh()
}

// SetText 更新按钮文字（无文字按钮忽略）。
func (b *compactButton) SetText(text string) {
	if b.label != nil {
		b.label.Text = text
		b.label.Refresh()
	}
}

// SetLabelColor 更新文字颜色（呼叫/挂断态切换用）。
func (b *compactButton) SetLabelColor(c color.NRGBA) {
	if b.label != nil {
		b.label.Color = c
		b.label.Refresh()
	}
}

// SetBg 更新底色。
func (b *compactButton) SetBg(c color.NRGBA) {
	b.bg.FillColor = c
	b.bg.Refresh()
}

// SetMinSize 覆盖按钮最小尺寸（主窗体呼叫按钮需要更扁的样式，REQ-VIDEO-003）。
func (b *compactButton) SetMinSize(w, h float32) {
	b.bg.SetMinSize(fyne.NewSize(w, h))
	b.Refresh()
}

func (b *compactButton) Tapped(*fyne.PointEvent) {
	if b.disabled || b.onTap == nil {
		return
	}
	b.onTap()
}

// Disable / Enable / Disabled 让紧凑按钮也能参与"通话中禁用主窗体控件"那套
// 状态管理（widget.Button 自带，这里是自绘按钮，得自己实现：拦点击 + 背景置灰）。
func (b *compactButton) Disable() { b.setDisabled(true) }

// Enable 恢复可点击与正常背景。
func (b *compactButton) Enable() { b.setDisabled(false) }

// Disabled 当前是否禁用。
func (b *compactButton) Disabled() bool { return b.disabled }

func (b *compactButton) setDisabled(v bool) {
	b.disabled = v
	if b.bg != nil {
		if v {
			b.bg.FillColor = colorSwitchGray
		} else {
			b.bg.FillColor = b.baseBg
		}
		b.bg.Refresh()
	}
}

func (b *compactButton) Cursor() desktop.Cursor { return desktop.PointerCursor }

// 振铃边框闪烁颜色序列（COLORREF 即 0x00BBGGRR）：红、橙、绿、青、蓝、紫。
// 保留给 Win32 DWM 边框路径（无边框窗口上不可见，已由底色闪烁替代）。
var ringBorderColors = []uint32{
	0x000000FF, // 红
	0x0000A5FF, // 橙
	0x0000FF00, // 绿
	0x00FFFF00, // 青
	0x00FF0000, // 蓝
	0x00FF00FF, // 紫
}

// ringFlashGrays 从当前主题背景色推导振铃闪烁灰度序列（REQ-UI-025 修订）：
// 16 档、每档 4 色阶——平滑往返脉动。
// 方向按背景亮度决定：深色主题（白字）向**更亮**方向走，浅色主题
// （黑字）向**更暗**方向走；若固定向亮方向走，浅色背景（近白）会
// 全部封顶在 255 导致不闪烁（历史 bug）。
func (a *App) ringFlashGrays() []color.NRGBA {
	bg := a.fyne.Settings().Theme().Color(theme.ColorNameBackground, a.fyne.Settings().ThemeVariant())
	r, g, b, _ := bg.RGBA()
	base := color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xff}
	light := (int(base.R) + int(base.G) + int(base.B)) > 382 // 浅色背景：调暗
	grays := make([]color.NRGBA, 16)
	for i := range grays {
		d := i * 4
		if light {
			d = -d
		}
		grays[i] = color.NRGBA{
			R: clampByte(int(base.R) + d),
			G: clampByte(int(base.G) + d),
			B: clampByte(int(base.B) + d),
			A: 0xff,
		}
	}
	return grays
}

// clampByte 把颜色通道值限制在 0-255。
func clampByte(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// newRingFlashRect 振铃底色闪烁矩形：实心填充（序列首色），置于
// 内容层下方撑满整个窗口，随闪烁序列轮换填充色（REQ-UI-025 修订）。
func newRingFlashRect(grays []color.NRGBA) *canvas.Rectangle {
	return canvas.NewRectangle(grays[0])
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
// （托盘常驻场景，从未显示），则不弹窗，失败原因仅记录日志——
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
