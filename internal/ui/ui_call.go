package ui

import (
	"fmt"
	"image"
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

	// 对端名称：自绘控件（号码 + 通信录名称 + ★ + 备注悬停，REQ-UI-037）。
	// 面板是固定 260px 宽的无边框窗口，widget.Label 在本项目主题下还带
	// 12px 内边距（MinSize 高 43px），所以用 plainText 系列自绘。
	a.callTip = newTooltipLayer()
	a.callName = newCallNameBox(a.callTip)
	// 状态/时长用**自绘单行文本**（不是 widget.Label）：本项目主题把
	// SizeNameInnerPadding 提到 12，widget.Label 的 MinSize 是"文字 + 上下各 12"
	// （高 43px），与第一行的自绘名称（高 19px、左边缘 0）既对不齐左边缘、
	// 也让每行高度不一致（用户实测"第一行和第二行左边没对齐、第一行高度变小"）。
	textSize := fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText)
	a.callStateLabel = newThemedText("", textSize, false)
	a.callTimerLabel = newThemedText("00:00", textSize, false)

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

	// 扬声器开关（REQ-UI-023）+ 音量（REQ-MEDIA-006）：
	// **滚轮**调节音量、底色横向填充，0% 即禁用（等同「关」）。
	a.speakerBtn = newLevelButton(IconSpeakerOn, callPanelButtonSize,
		func(steps int, pos fyne.Position) { a.onCallWheel(false, steps, pos) }, a.toggleSpeaker)

	// 麦克风开关（REQ-UI-023）+ 增益（REQ-MEDIA-006）
	a.micBtn = newLevelButton(IconMicOn, callPanelButtonSize,
		func(steps int, pos fyne.Position) { a.onCallWheel(true, steps, pos) }, a.toggleMic)

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
	// 顶层再叠一个提示图层（备注悬停用，见 ui_tooltip.go）：提示只是绘制在
	// 最上层、不参与命中测试，所以不会挡住面板上的按钮
	w.SetContent(container.NewStack(a.callRows, a.callTip))
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

	row1 := container.New(panelRowLayout{}, a.callName)
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

	// 音量按钮的外观每次都按会话内记忆刷新（REQ-MEDIA-006）：面板首次创建时
	// 靠它把填充条画出来（新按钮的 level 默认 0），网页改过开关后重开面板
	// 也能立刻对上。
	a.applySpeakerButton()
	a.applyMicButton()

	// 三行纵向排列（行距 panelRowGap）、整体在窗口内纵向居中；四周 4px 窗口留白
	// 固定行高：第一行（只有号码/名称）与有按钮的行高度一致，整体才不会"跳"；
	// gapAfterFirst=4 → 第 2、3 行整体比第一行多下移 4px（用户要求，分两次各 2px 调出来的）
	rows := container.New(panelRowsLayout{rowHeight: callPanelRowHeight, gapAfterFirst: 4}, row1, row2, row3)
	// 左内边距 6：三行文字比原先整体右移 2px（用户要求）。右侧仍是 4，
	// 所以右对齐的按钮位置不受影响
	border := container.NewBorder(padRect(4), padRect(4), padRect(6), padRect(4), rows)

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
	// 首元素靠左（纵向居中：行高固定时文字才不会顶在上沿）
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
//
// rowHeight > 0 时每行**固定这么高**（内容不足也占满）：通话面板三行里只有
// 第二行有挂断按钮，若按内容高度排，没按钮的行会比有按钮的行矮一截，
// 视觉上"第一行矮一截"（用户实测）。0 = 按内容高度（通信录/通话记录行用）。
type panelRowsLayout struct {
	rowHeight float32
	// gapAfterFirst 第一行之后的**额外**行距：通话面板用它把第 2、3 行整体
	// 再往下挪一点（用户要求，行距上疏下密反而不行——这里只是让第二行起更透气）。
	gapAfterFirst float32
}

// gapBefore 第 rowIdx 行（按可见行数从 0 数）之前的行距。
func (l panelRowsLayout) gapBefore(rowIdx int) float32 {
	g := float32(panelRowGap)
	if rowIdx == 1 {
		g += l.gapAfterFirst
	}
	return g
}

func (l panelRowsLayout) rowHeightOf(o fyne.CanvasObject) float32 {
	h := o.MinSize().Height
	if l.rowHeight > h {
		h = l.rowHeight
	}
	return h
}

func (l panelRowsLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var sz fyne.Size
	rowIdx := -1
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		rowIdx++
		if rowIdx > 0 {
			sz.Height += l.gapBefore(rowIdx)
		}
		sz.Height += l.rowHeightOf(o)
		sz.Width = fyne.Max(sz.Width, o.MinSize().Width)
	}
	return sz
}

func (l panelRowsLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	total := float32(0)
	rowIdx := -1
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		rowIdx++
		if rowIdx > 0 {
			total += l.gapBefore(rowIdx)
		}
		total += l.rowHeightOf(o)
	}
	y := (size.Height - total) / 2
	if y < 0 {
		y = 0
	}
	rowIdx = -1
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		rowIdx++
		h := l.rowHeightOf(o)
		o.Resize(fyne.NewSize(size.Width, h))
		o.Move(fyne.NewPos(0, y))
		y += h + l.gapBefore(rowIdx+1)
	}
}

// callPanelRowHeight 通话面板的固定行高（= 按钮尺寸）：三行等高，行内内容
// 由 panelRowLayout 纵向居中。
const callPanelRowHeight = callPanelButtonSize

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
	colorDangerDark    = color.NRGBA{R: 0xa8, G: 0x18, B: 0x18, A: 0xff} // 深红（通话记录页「清空通话记录」按钮）
	colorSuccessGreen  = color.NRGBA{R: 0x43, G: 0xa0, B: 0x47, A: 0xff} // 接听/呼叫
	colorHoldYellow    = color.NRGBA{R: 0xfb, G: 0xc0, B: 0x2d, A: 0xff} // 保持
	colorHoldBlue      = color.NRGBA{R: 0x21, G: 0x96, B: 0xf3, A: 0xff} // 取消保持/开关开
	colorSwitchGray    = color.NRGBA{R: 0xbd, G: 0xbd, B: 0xbd, A: 0xff} // 开关关
	colorTransformGray = color.NRGBA{R: 0x44, G: 0x48, B: 0x4f, A: 0xf2} // 旋转/镜像未生效（深灰，压在画面上）
	colorIconBlack     = color.NRGBA{R: 0x1a, G: 0x1a, B: 0x1a, A: 0xff} // 呼叫按钮文字（固定黑）
	colorIconWhite     = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // 挂断态文字（红底白字）
	// colorLevelTrack 音量按钮未填充部分的底色（REQ-MEDIA-006）：比填充色深，
	// 白色开图标压在轨道与填充条上都看得清（用浅灰做轨道的话图标会"消失"）。
	colorLevelTrack = color.NRGBA{R: 0x3a, G: 0x3f, B: 0x46, A: 0xff}
	// colorLevelBoost 增益段（麦克风 100%→200%）的橙色：叠在蓝色之上，
	// 200% 时整块橙（用户要求：上调时蓝色上再覆盖成橙色）。
	colorLevelBoost = color.NRGBA{R: 0xf5, G: 0x7c, B: 0x00, A: 0xff}
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

	// ---- 音量按钮（REQ-MEDIA-006；普通按钮这些字段为 nil）----
	// sweep 圆周填充层：按钮外观不变，只是底色按"从正上方顺时针"的角度填充
	//（见 sweepImage）。level 蓝色部分比例 0–1，boost 橙色（增益）部分 0–1。
	sweep *canvas.Raster
	level float32
	boost float32
	// scroll 滚轮中继（只有音量按钮有，见 scrollRelay）
	scroll *scrollRelay
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

// newLevelButton 带「圆周填充底色」的紧凑按钮（通话面板的扬声器/麦克风，
// REQ-MEDIA-006）：按钮外观不变，只是底色从**正上方**起按 level(0–1) **顺时针**
// 扫过（12→3→6→9）；未填充部分是深灰轨道色。鼠标滚轮悬停其上可调值
// （onScroll 收到的是**格数**，已按 wheelNotch 折算）。
func newLevelButton(iconRes fyne.Resource, size float32, onScroll func(steps int, pos fyne.Position), onTap func()) *compactButton {
	b := newCompactButton(iconRes, colorLevelTrack, size, onTap)
	b.sweep = canvas.NewRaster(b.sweepImage)
	b.sweep.ScaleMode = canvas.ImageScaleSmooth
	b.scroll = newScrollRelay(onScroll)
	return b
}

// sweepCorner 圆周填充层的圆角半径（与按钮底色一致，满格时才完全重合）。
const sweepCorner = 6

// sweepImage 逐像素画"从正上方顺时针"的底色填充（REQ-MEDIA-006）：
// 蓝色部分（level，0–1）打底，**橙色增益部分（boost，0–1）叠在蓝色之上**
// ——麦克风 100% 时满蓝，100%→200% 再顺时针压一层橙，200% 时整块橙。
//
// 为什么用 Raster 而不是 canvas.Arc：Arc 画的是圆/扇形，满格时是个**圆**，
// 方形按钮的四个角会露出轨道色（而按钮外观要求不变）。Fyne 没有"把图形裁剪到
// 圆角矩形"的能力，于是这里逐像素自己判定：圆角矩形之内、且落在扫过角度内的
// 像素填填充色，其余透明（透出下面的轨道色）。36×36 的小图 + 3×3 超采样，
// 开销可忽略；像素坐标是设备像素（驱动按 DPI 放大后调用），所以按比例换算。
func (b *compactButton) sweepImage(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if w <= 0 || h <= 0 || (b.level <= 0 && b.boost <= 0) {
		return img
	}
	dipW, dipH := b.Size().Width, b.Size().Height
	if dipW <= 0 || dipH <= 0 {
		dipW, dipH = float32(w), float32(h)
	}
	scale := float64(w) / float64(dipW)
	radius := sweepCorner * scale
	blueDeg := float64(b.level) * 360
	boostDeg := float64(b.boost) * 360
	cx, cy := float64(w)/2, float64(h)/2

	const ss = 3 // 每个像素 3×3 超采样，抹掉角度边界与圆角的锯齿
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			blueCover, boostCover := 0, 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					if !inRoundRect(px, py, float64(w), float64(h), radius) {
						continue
					}
					// 相对圆心的角度：正上方 0°，顺时针为正（atan2(右, 上)）
					deg := math.Atan2(px-cx, cy-py) * 180 / math.Pi
					if deg < 0 {
						deg += 360
					}
					if deg <= blueDeg {
						blueCover++
					}
					if deg <= boostDeg {
						boostCover++
					}
				}
			}
			if blueCover == 0 {
				continue
			}
			// 橙色只叠在蓝色之上（两者同起点、同方向，boost 段必在 blue 段内），
			// 边界像素按覆盖率在蓝橙之间插值
			col := colorHoldBlue
			if boostCover > 0 {
				t := float64(boostCover) / float64(blueCover)
				col = color.NRGBA{
					R: uint8(float64(colorHoldBlue.R)*(1-t) + float64(colorLevelBoost.R)*t + 0.5),
					G: uint8(float64(colorHoldBlue.G)*(1-t) + float64(colorLevelBoost.G)*t + 0.5),
					B: uint8(float64(colorHoldBlue.B)*(1-t) + float64(colorLevelBoost.B)*t + 0.5),
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{R: col.R, G: col.G, B: col.B,
				A: uint8(blueCover * 255 / (ss * ss))})
		}
	}
	return img
}

// inRoundRect 点 (x,y) 是否落在 w×h、圆角半径 r 的圆角矩形内。
func inRoundRect(x, y, w, h, r float64) bool {
	if x < 0 || y < 0 || x >= w || y >= h {
		return false
	}
	// 把点钳到"内矩形"上：角上距离圆心 > r 的在圆角之外
	nx := math.Min(math.Max(x, r), w-r)
	ny := math.Min(math.Max(y, r), h-r)
	dx, dy := x-nx, y-ny
	return dx*dx+dy*dy <= r*r
}

// scrollRelay 透明的滚轮中继（REQ-MEDIA-006）：**只**实现 fyne.Scrollable 并
// 把滚轮折算成格数转发出去，不实现 Tappable / Hoverable，所以点击照常落到下层
// 的按钮上。
//
// 为什么不把 Scrolled 直接加在 compactButton 上：Fyne 的滚轮分发（glfw 驱动的
// processMouseScrolled）只把事件交给指针下**最深的一个** Scrollable，**不冒泡**
// ——而 compactButton 到处都是（通话记录页/通信录页的行内「呼叫」「视频呼叫」
// 按钮就在可滚动列表里），指针停在那些按钮上滚轮会被吞掉、列表不滚。中继只挂在
// 音量按钮上，别处零影响。
type scrollRelay struct {
	canvas.Rectangle
	onScroll func(steps int, pos fyne.Position)
	acc      float32 // 未满一格的滚动余量
}

func newScrollRelay(onScroll func(steps int, pos fyne.Position)) *scrollRelay {
	r := &scrollRelay{onScroll: onScroll}
	r.FillColor = color.Transparent
	return r
}

// Scrolled 实现 fyne.Scrollable：把滚动量折算成格数（见 wheelSteps），
// 并把指针的绝对坐标一并带给回调（百分比提示要显示在指针附近）。
func (r *scrollRelay) Scrolled(ev *fyne.ScrollEvent) {
	if r.onScroll == nil {
		return
	}
	if steps := wheelSteps(&r.acc, ev.Scrolled.DY); steps != 0 {
		r.onScroll(steps, ev.AbsolutePosition)
	}
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
	// baseBg 必须一起设：Disable/Enable 会用它还原背景（早先漏设，禁用过
	// 再启用的纯文字按钮会变成透明底）
	b := &compactButton{onTap: onTap, baseBg: bgColor}
	b.bg = canvas.NewRectangle(bgColor)
	b.bg.CornerRadius = 3
	b.label = canvas.NewText(text, labelColor)
	b.label.TextSize = 14
	b.ExtendBaseWidget(b)
	return b
}

func (b *compactButton) CreateRenderer() fyne.WidgetRenderer {
	if b.sweep != nil {
		// 音量按钮：轨道（bg）+ 圆周填充层 + 图标 +
		// 滚轮中继（必须排在**最后**：命中测试取遍历里最后一个匹配者）
		return widget.NewSimpleRenderer(container.NewStack(b.bg, b.sweep,
			container.NewCenter(b.icon), b.scroll))
	}
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

// SetTrack 更新未填充部分的底色（音量按钮：0% 用浅灰=禁用样式，非 0 用深灰轨道）。
func (b *compactButton) SetTrack(c color.NRGBA) {
	b.baseBg = c
	b.SetBg(c)
}

// SetLevel 更新蓝色填充比例（0–1），不叠增益橙（扬声器用）。
func (b *compactButton) SetLevel(level float32) { b.SetLevels(level, 0) }

// SetLevels 更新圆周填充（0–1）：level 蓝色部分、boost 橙色增益部分
// （叠在蓝色之上，麦克风 100%→200% 的那一段）。
// （`Raster.Refresh` 会让驱动丢掉缓存纹理、重新调用生成器，见 gl 的 freeTexture。）
func (b *compactButton) SetLevels(level, boost float32) {
	if b.sweep == nil {
		return
	}
	level = clampLevel(level)
	boost = clampLevel(boost)
	if b.level == level && b.boost == boost {
		return
	}
	b.level, b.boost = level, boost
	b.sweep.Refresh()
}

// clampLevel 把填充比例夹取到 0–1。
func clampLevel(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// wheelNotch 一格滚轮在 Fyne 里对应的滚动量（见 wheelSteps）。
const wheelNotch = 25

// maxWheelSteps 单个滚轮事件最多折算几步（**别去掉**）：Fyne 在 |yoff| ≥ 10 时会
// 把滚动量乘 125（scroll_speed_default.go 的 scrollAccelerateRate），一次事件就
// 可能是上万——不设上限的话音量会"瞬间到底"，用户完全没法微调。超出的部分直接
// 丢弃（不留在余量里，否则后续每格都会继续狂掉）。
const maxWheelSteps = 4

// wheelSteps 把滚动量折算成格数：累计到 acc 上，满一格算一步，余数留存到
// 下一次（触摸板的小量滚动会逐步累积，普通滚轮一格恰好一步）。纯函数，可单测。
func wheelSteps(acc *float32, dy float32) int {
	*acc += dy
	steps := int(*acc / wheelNotch)
	if steps > maxWheelSteps || steps < -maxWheelSteps {
		// 异常大的一次滚动：只走上限那么多步，余量清掉——若把它留在
		// 累计值里，之后每个事件都会继续按上限狂掉，音量直接到底。
		*acc = 0
		if steps > 0 {
			return maxWheelSteps
		}
		return -maxWheelSteps
	}
	if steps != 0 {
		*acc -= float32(steps) * wheelNotch
	}
	return steps
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

// SetIconSize 覆盖图标尺寸（默认是按钮尺寸的 0.55）。摄像头这类扁宽的图形
// 在同尺寸下看着偏小，可以单独放大一点（不要超过按钮尺寸，超出会被裁掉）。
func (b *compactButton) SetIconSize(size float32) {
	if b.icon == nil {
		return
	}
	b.icon.SetMinSize(fyne.NewSize(size, size))
	b.Refresh()
}

// SetCornerRadius 覆盖圆角半径（默认 6；通话记录行内按钮用小圆角）。
func (b *compactButton) SetCornerRadius(r float32) {
	b.bg.CornerRadius = r
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

// toggleSpeaker / toggleMic / applySpeakerButton / applyMicButton /
// applySessionAudioState 与音量（REQ-MEDIA-006）一起放在 ui_call_volume.go。

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
	a.hideLevelTip() // 收起可能还挂着的音量百分比数字（面板要收起来了）
	a.activeCallID = ""
	a.activeCall = nil
	a.setCallUI(uiIdle, nil)
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

// 弹窗一律显示在**父窗口**上（Fyne 把对话框画在父窗口的画布上）：父窗口
// 隐藏时弹窗用户根本看不见。本项目所有页面都在主窗体内，默认父窗口就是
// 主窗体；需要指定时用下面带 In 的变体。**永远不要传 nil**（会 panic）。
//
// **不用 `dialog.NewInformation` / `NewError` / `NewConfirm`**：Fyne 2.8 的这三个
// "文本对话框"会在右上角画一个**巨大的半透明图标水印**
//（dialog/base.go 的 dialogLayout：定位到 size.Width-iconHeight+4, -4，
// 尺寸按文字高度算），在本项目的小窗口里会压住标题、顶出边框，用户实测
// 观感是"图标没显示对、高度还高得离谱"。所以统一用 NewCustom 自拼内容：
// 标题栏 + 折行正文 + 按钮，尺寸由内容决定。

// confirmIconSize 确认框里警告图标的尺寸。
const confirmIconSize = 30

// dialogTextIconMaxW 带图标（确认框）时正文的宽度上限。弹窗总宽 ≈ 正文 + 图标 30 +
// 间距 6 + border 内边距 4 + 弹窗自身内边距 ≈ 正文 + 72：正文按 266 算出来接近满屏宽
// （用户实测"太宽、左右撑满窗口"），收到 182 → 弹窗约 254（占主窗体 320 的 ~80%）。
const dialogTextIconMaxW = 182

// dialogTextMaxW 不带图标（信息/错误框）的正文宽度：刻意让**整个弹窗的宽度**与
// 确认框一致——182 + 图标 30 + 间距 6 + border 内边距 4 = 222（两种弹窗看起来一样宽，
// 用户要求）。弹窗画在父窗体画布上，主窗体只有 320px 宽。
const dialogTextMaxW = dialogTextIconMaxW + confirmIconSize + 6 + 4

// newDialogText 弹窗正文：按固定宽度折行的多行文本。
//
// 两个坑：① widget.Label 开了 Wrapping 后 **必须先 Resize 过一次**，
// MinSize 才会报告"折行后的高度"（没折过时报的是单行高度，长文本会把弹窗
// 高度算少）；② 它的 MinSize.Width 是"最长不可断词"的宽度（可能只有十几像素），
// 直接当弹窗内容会把弹窗挤成一条窄柱，所以外面套 GridWrap 固定宽度。
func newDialogText(text string, maxW float32) fyne.CanvasObject {
	lbl := widget.NewLabel(text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.Resize(fyne.NewSize(maxW, 1)) // 先按目标宽度折一次
	return container.NewGridWrap(fyne.NewSize(maxW, lbl.MinSize().Height), lbl)
}

// showInfo 信息弹窗（按钮文案"确定"，覆盖 Fyne 中文 locale 的"好"）。
func (a *App) showInfo(title, content string) { a.showInfoIn(a.mainW, title, content) }

// showError 错误弹窗（按钮文案"确定"）。
func (a *App) showError(err error) { a.showErrorIn(a.mainW, err) }

// showConfirm 确认弹窗（确定 / 取消）。
func (a *App) showConfirm(title, content string, cb func(bool)) {
	a.showConfirmIn(a.mainW, title, content, cb)
}

// showInfoIn 在指定窗口弹信息框。
func (a *App) showInfoIn(parent fyne.Window, title, content string) {
	d := dialog.NewCustom(title, "确定", newDialogText(content, dialogTextMaxW), parent)
	a.withDialogTheme(d)
	d.Show()
}

// showErrorIn 在指定窗口弹错误框。
func (a *App) showErrorIn(parent fyne.Window, err error) {
	if err == nil {
		return
	}
	d := dialog.NewCustom("错误", "确定", newDialogText(err.Error(), dialogTextMaxW), parent)
	a.withDialogTheme(d)
	d.Show()
}

// showConfirmIn 在指定窗口弹确认框（确定 / 取消）。
//
// 正文左侧带一个**警告图标**：删除/清空这类操作不可恢复，给个醒目的提示
// （用户要求）。图标用主题的 WarningIcon，随主题明暗变色。
func (a *App) showConfirmIn(parent fyne.Window, title, content string, cb func(bool)) {
	icon := widget.NewIcon(theme.WarningIcon())
	iconBox := container.NewGridWrap(fyne.NewSize(confirmIconSize, confirmIconSize), icon)
	body := container.NewBorder(nil, nil,
		container.New(padRightLayout{pad: 6}, iconBox), nil, newDialogText(content, dialogTextIconMaxW))
	d := dialog.NewCustomWithoutButtons(title, body, parent)
	ok := widget.NewButton("确定", func() {
		d.Hide()
		if cb != nil {
			cb(true)
		}
	})
	ok.Importance = widget.HighImportance // 主要动作（原 Fyne NewConfirm 的样式）
	cancel := widget.NewButton("取消", func() {
		d.Hide()
		if cb != nil {
			cb(false)
		}
	})
	d.SetButtons([]fyne.CanvasObject{cancel, ok})
	a.withDialogTheme(d)
	d.Show()
}

// ---------------------------------------------------------------------------
// 对端展示：通信录名称联动（REQ-UI-037）
// ---------------------------------------------------------------------------

// callNameMaxW 通话面板上对端名称的最大宽度（面板只有 260px 宽，同一行右边
// 还要放接听/保持按钮，名字长了必须截断，不能把按钮挤出去）。
const callNameMaxW = 190

// callNameBox 通话面板行1的对端名称：号码 + 通信录名称 + ★（常用），
// 名称上可悬停出备注提示（REQ-UI-037）。
//
// 自绘（不用 widget.Label）的原因：本项目主题把 SizeNameInnerPadding 提到 12，
// widget.Label 的 MinSize 是"文字高 + 24"，会把面板撑变形；自绘也才好把 ★
// 紧跟在名字后面（markAfterLayout）而不是被行布局推到最右边。
type callNameBox struct {
	widget.BaseWidget

	note *hoverNote
	star *canvas.Image
	box  *fyne.Container
}

func newCallNameBox(tip *tooltipLayer) *callNameBox {
	size := fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText)
	b := &callNameBox{}
	b.note = newHoverNote("", size, "", tip)
	b.star = canvas.NewImageFromResource(IconStar)
	b.star.FillMode = canvas.ImageFillContain
	b.star.SetMinSize(fyne.NewSize(contactsStarSize, contactsStarSize))
	b.star.Hide()
	b.box = container.New(markAfterLayout{gap: 4}, b.note, b.star)
	b.ExtendBaseWidget(b)
	return b
}

func (b *callNameBox) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(b.box)
}

// MinSize 限制宽度：面板窄，长号码/长名字只能截断显示。
func (b *callNameBox) MinSize() fyne.Size {
	m := b.box.MinSize()
	if m.Width > callNameMaxW {
		m.Width = callNameMaxW
	}
	return m
}

// Set 更新对端展示：文本、是否常用（★）、备注（悬停提示）。
func (b *callNameBox) Set(text string, starred bool, note string) {
	b.note.SetText(text)
	b.note.SetNote(note)
	if starred {
		b.star.Show()
	} else {
		b.star.Hide()
	}
	b.box.Refresh()
	b.Refresh()
}

// contactFor 号码对应的通信录条目（核心层内存快照，O(1)，不碰数据库）。
func (a *App) contactFor(remote string) (core.Contact, bool) {
	if a.core == nil || remote == "" {
		return core.Contact{}, false
	}
	return a.core.LookupContact(remote)
}

// callDisplay 对端展示文案（通话面板与视频窗口标题共用，REQ-UI-037）：
//   - 命中通信录：号码在前、名称在后（用户要求"号码右边显示名称"）；
//   - 未命中：沿用原行为（SIP 显示名优先，其次号码）。
//
// 同时返回是否常用（★）与备注（悬停提示用）。
func (a *App) callDisplay(call *core.Call) (text string, starred bool, note string) {
	if call == nil {
		return "", false, ""
	}
	remote, sipName := call.Remote, call.RemoteName
	if c, ok := a.contactFor(remote); ok {
		name := c.Name
		if name == "" {
			name = sipName
		}
		switch {
		case name == "" || name == remote:
			text = remote
		case remote == "":
			text = name
		default:
			text = remote + " " + name
		}
		return text, c.Starred, c.Note
	}
	return callRemoteName(call), false, ""
}

// setCallName 更新通话面板的对端展示。
func (a *App) setCallName(call *core.Call) {
	if a.callName == nil {
		return
	}
	text, starred, note := a.callDisplay(call)
	a.callName.Set(text, starred, note)
}

// callRemoteName 通话对象的展示名称（显示名优先，其次号码）。
func callRemoteName(call *core.Call) string {
	if call.RemoteName != "" {
		return call.RemoteName
	}
	return call.Remote
}
