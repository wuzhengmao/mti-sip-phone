package ui

import (
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

	"mti-sip-phone/internal/config"
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

	// 顶部拖动条（REQ-UI-040）：无边框面板的"抓手"，悬停变可移动光标、拖动即移窗。
	a.callGrip = newCallGrip(a.onCallPanelDragStart, a.onCallPanelDragEnd)
	// 状态/时长用**自绘单行文本**（不是 widget.Label）：本项目主题把
	// SizeNameInnerPadding 提到 12，widget.Label 的 MinSize 是"文字 + 上下各 12"
	// （高 43px），与第一行的自绘名称（高 19px、左边缘 0）既对不齐左边缘、
	// 也让每行高度不一致（用户实测"第一行和第二行左边没对齐、第一行高度变小"）。
	textSize := fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText)
	a.callStateLabel = newThemedText("", textSize, false)
	a.callTimerLabel = newThemedText("00:00", textSize, false)

	// 紧凑按钮（尺寸取平台常量，REQ-UI-025 修订）：fyne Button 跟随主题
	// 内边距高达 48px，用自定义 compactButton 缩小面板按钮
	a.hangupBtn = newPanelButton(IconStopWhite, colorDangerRed, callPanelButtonSize, func() {
		if a.activeCallID != "" {
			_ = a.core.Hangup(a.activeCallID)
		}
	})
	a.hangupBtn.setTip(a.callTip, "挂断")

	// 绿色电话：接听
	a.answerBtn = newPanelButton(IconPhoneWhite, colorSuccessGreen, callPanelButtonSize, func() {
		if a.activeCallID != "" {
			if err := a.core.Answer(a.activeCallID); err != nil {
				logx.Logger().Warn("接听失败", "err", err)
			}
		}
	})
	a.answerBtn.setTip(a.callTip, "接听")
	a.answerBtn.SetIconSize(16) // 稍微缩小（用户要求）

	// 黄色 ||：通话保持切换（保持语义：本端停止发送音频，再次点击恢复）
	a.holdBtn = newPanelButton(IconPauseWhite, colorHoldYellow, callPanelButtonSize, func() {
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
	a.holdBtn.setTip(a.callTip, "保持")

	// 扬声器开关（REQ-UI-023）+ 音量（REQ-MEDIA-006）：
	// **滚轮**调节音量、底色横向填充，0% 即禁用（等同「关」）。
	a.speakerBtn = newLevelButton(IconSpeakerOn, callPanelButtonSize,
		func(steps int, pos fyne.Position) { a.onCallWheel(false, steps, pos) }, a.toggleSpeaker)
	a.speakerBtn.setTip(a.callTip, "扬声器（滚轮调音量，0% 关闭）")

	// 麦克风开关（REQ-UI-023）+ 增益（REQ-MEDIA-006）
	a.micBtn = newLevelButton(IconMicOn, callPanelButtonSize,
		func(steps int, pos fyne.Position) { a.onCallWheel(true, steps, pos) }, a.toggleMic)
	a.micBtn.setTip(a.callTip, "麦克风（滚轮调音量，0% 静音）")

	// 视频接听（REQ-VIDEO-004）：来电含视频时显示在「接听」左侧
	a.videoAnswerBtn = newPanelButton(IconVideoAnswer, colorSuccessGreen, callPanelButtonSize, func() {
		if a.activeCallID == "" {
			return
		}
		if err := a.core.AnswerVideo(a.activeCallID); err != nil {
			logx.Logger().Warn("视频接听失败", "err", err)
		}
	})
	a.videoAnswerBtn.setTip(a.callTip, "视频接听")
	a.videoAnswerBtn.SetIconSize(16) // 稍微缩小（用户要求）

	// 关闭视频开关（REQ-VIDEO-008）：位于扬声器左侧
	a.videoBtn = newPanelButton(IconVideoOn, colorHoldBlue, callPanelButtonSize, func() {
		a.setVideoEnabled(!a.videoOn)
	})
	a.videoBtn.setTip(a.callTip, "视频")

	// CTI 话务按钮（REQ-ACC-026）：咨询/转移/会议入口 + 各话务状态按钮。
	a.buildCTIButtons()

	// 三行内容容器：内容在每次进入状态时由 rebuildCallRows 重建。
	// 用 Stack：振铃底色闪烁矩形与内容层平铺为兄弟、撑满整个窗口
	//（REQ-UI-025 修订）；内容层为 Border（窗口
	// 内容会撑满画布，Border 的中心区→网格→三行等分填满窗口）
	a.callRows = container.NewStack()
	// 顶层再叠一个提示图层（备注悬停用，见 ui_tooltip.go）：提示只是绘制在
	// 最上层、不参与命中测试，所以不会挡住面板上的按钮。
	// 拖动条（REQ-UI-040）夹在中间：定位层本身不参与命中测试，只有顶部那
	// 一小条"抓手"参与（见 callGrip/callGripLayer）。
	w.SetContent(container.NewStack(a.callRows, container.New(callGripLayer{}, a.callGrip), a.callTip))
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

// rebuildCallRows 按状态重建通话面板行内容（REQ-UI-008 + REQ-ACC-026 修订）：
//   - 行1：状态文字（"通话中/来电/呼叫中"）+ 右侧按钮（振铃=接听；通话=咨询/转移/会议/保持）
//   - 行2：对端号码（呼出时号码后跟绿色动态箭头）+ 挂断按钮
//   - 行3：时长（通话中加扬声器/麦克风开关，靠右，REQ-UI-023）
//
// 注：REQ-ACC-026 第 4 点——号码与状态两行**对换**（只换文字，按钮位置不变）。
func (a *App) rebuildCallRows(mode int) {
	if a.callRows == nil {
		return
	}
	a.callRows.Objects = nil
	// 两方通话/拨号/振铃都是 3 行：从复杂话务（4 行及以上）回到这里要复位高度。
	// **必须先清空 Objects 再调高度**：Fyne 的 SetFixedSize(true) 会按当前内容的
	// 最小高把窗口顶回去，旧 4 行内容（最小高 170）还在时，144 的 3 行高度设不进去
	// （用户实测：4 行面板关闭后重新来电，窗口停在 170 高、三行内容居中留白）。
	a.setCallPanelHeight(3)
	// 挂断按钮的提示与图标回到通用样式（话务面板会改提示/图标）
	if a.hangupBtn != nil {
		a.hangupBtn.setTip(a.callTip, "挂断")
		a.hangupBtn.SetIcon(IconStopWhite)
	}

	// 行1：状态文字（原来放号码，现换过来）；右侧按钮按状态在 switch 里加。
	row1 := container.New(panelRowLayout{}, a.callStateLabel)
	// 行3：时长 + 音量。视频开关**只在本次通话确实协商了视频时**才出现
	//（纯音频通话放一个点了没反应的按钮没有意义）。
	row3 := container.New(panelRowLayout{}, a.callTimerLabel)
	if a.callHasVideo {
		row3.Add(a.videoBtn)
	}
	// 顺序按用户定的布局：摄像头 → 麦克风 → 扬声器
	row3.Add(a.micBtn)
	row3.Add(a.speakerBtn)

	switch mode {
	case dialingButtons:
		a.stopRingShake()
		a.ringFlashRect = nil
	case ringingButtons:
		// 来电时第二行的红色按钮是「拒接」（挂断即 603 拒接）
		if a.hangupBtn != nil {
			a.hangupBtn.setTip(a.callTip, "拒接")
		}
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
		// 两方通话的状态文案（从咨询/三方/监听/会议回到两方时也要复位）
		a.setTwoPartyStateLabel()
		// REQ-ACC-028：**只有坐席模式**（CTI 推过 MSP_EVENT_AGENT_ONLINE）
		// 才显示高级话务按钮；且只在这一分支（两方通话已接通）显示，
		// 拨号中/振铃中都不显示。保持按钮不受影响。
		// REQ-ACC-029：本通若是服务端推来的**咨询腿**（Call-Info call-type=consult），
		// 第一排的咨询/转移/会议入口一律不显示——这通本身就是别人咨询的一方。
		// REQ-ACC-031：另有咨询正在进行、而本机不是咨询发起方（CONSULT_STARTED
		// 也发给原通话双方与被咨询方）时同样隐藏，CONSULT_FINISHED 后恢复。
		noAgentButtons := a.activeCall != nil &&
			(a.activeCall.Consult || a.activeCall.CTI.ConsultInProgress)
		if a.agentOnline && !noAgentButtons {
			row1.Add(a.consultBtn)
			row1.Add(a.transferBtn)
			row1.Add(a.conferenceBtn)
			// 会议按钮图标随通话类型：普通通话 = 会议人员，视频通话 = 多人视频
			if a.callHasVideo {
				a.conferenceBtn.SetIcon(IconCTIVideoConference)
			} else {
				a.conferenceBtn.SetIcon(IconCTIConference)
			}
		}
		row1.Add(a.holdBtn)
		// 通话建立：把会话内记忆的开关状态下发到当前通话引擎
		//（引擎按通话创建，每次通话建立需重新下发）
		a.applySessionAudioState()
		a.applyVideoButton()
	}

	row2 := container.New(panelRowLayout{}, a.callName, a.hangupBtn)

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
	// 首元素靠左（纵向居中：行高固定时文字才不会顶在上沿）。
	// 右侧按钮多到放不下时（如会议成员行有 4 个按钮），首元素按剩余宽度收窄——
	// 不能让文字原样画到按钮底下（文字类控件在 Resize 时自行截断补省略号）。
	if o := objs[0]; o.Visible() {
		m := o.MinSize()
		if x > 0 && m.Width > x {
			m.Width = x
		}
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
	colorDangerRed    = color.NRGBA{R: 0xe5, G: 0x39, B: 0x35, A: 0xff} // 挂断
	colorDangerDark   = color.NRGBA{R: 0xa8, G: 0x18, B: 0x18, A: 0xff} // 深红（通话记录页「清空通话记录」按钮）
	colorSuccessGreen = color.NRGBA{R: 0x43, G: 0xa0, B: 0x47, A: 0xff} // 接听/呼叫
	// colorCallGreen 主窗体底部按钮（呼叫 / 视频呼叫 / 拨号复用的两个动作）
	// 的**暗绿色**：比 colorSuccessGreen 深一档（Material Green 800），
	// 白字白图标在上面更稳。
	colorCallGreen     = color.NRGBA{R: 0x2e, G: 0x7d, B: 0x32, A: 0xff}
	colorHoldYellow    = color.NRGBA{R: 0xfb, G: 0xc0, B: 0x2d, A: 0xff} // 保持
	colorHoldBlue      = color.NRGBA{R: 0x21, G: 0x96, B: 0xf3, A: 0xff} // 取消保持/开关开
	colorAgentIsWork   = color.NRGBA{R: 0x21, G: 0x96, B: 0xf3, A: 0xff} // 坐席整理中（与 colorHoldBlue 同值）
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
	bg         *canvas.Rectangle
	icon       *canvas.Image
	label      *canvas.Text // 可选文字
	labelColor color.NRGBA  // 文字正常色（禁用/恢复时切换）
	onTap      func()
	baseBg     color.NRGBA // 普通样式：正常态背景色；outline 样式：图标（强调）色
	disabled   bool

	// ---- 无底色图标按钮样式（outline，通话面板用；REQ-UI-043）----
	// 平时背景完全透明（= 窗体背景），鼠标移上去才淡填充 + 1px 轮廓；
	// 图标用 baseBg（原来的按钮底色）着色，禁用时变灰。
	outline bool
	hovered bool
	iconSrc fyne.Resource // 原始图标（着色前的资源）
	// iconDX / iconDY 图标位置微调（像素，正值右/下移）：个别图标在原始图片里
	// 画得偏了，用它们在**显示层**对齐。
	iconDX, iconDY float32

	// ---- 音量按钮（REQ-MEDIA-006；普通按钮这些字段为 nil）----
	// bar 竖直进度条：贴在按钮右缘内侧，高度 = 图标高度，从底部按百分比填充
	//（麦克风 100%→200% 的增益段橙色叠在蓝色之上）。
	bar *levelBar
	// scroll 滚轮中继（只有音量按钮有，见 scrollRelay）
	scroll *scrollRelay

	// ---- 悬停提示（通话面板的每个按钮都有文字说明）----
	// tip 是所在窗口的提示图层（通话面板用 a.callTip）；主窗体的按钮不设，
	// 也就没有提示（那套按钮旁边都有文字）。
	tip      *tooltipLayer
	tipText  string
	tipTimer *time.Timer
}

func newCompactButton(iconRes fyne.Resource, bgColor color.NRGBA, size float32, onTap func()) *compactButton {
	b := &compactButton{onTap: onTap, baseBg: bgColor, iconSrc: iconRes}
	b.bg = canvas.NewRectangle(bgColor)
	b.bg.CornerRadius = 6
	b.bg.SetMinSize(fyne.NewSize(size, size))
	b.icon = canvas.NewImageFromResource(iconRes)
	b.icon.FillMode = canvas.ImageFillContain
	// 图标按比例缩放（36 → 18px；PNG 图标细节多，视觉上偏大，故收小）
	iconSize := float32(math.Round(float64(size) * 0.5))
	b.icon.SetMinSize(fyne.NewSize(iconSize, iconSize))
	b.ExtendBaseWidget(b)
	return b
}

// newPanelButton 通话面板的图标按钮：**无底色**样式（透明底、hover 才显轮廓），
// accent 即图标颜色（沿用原来的按钮底色，如挂断红、接听绿、功能蓝）。
func newPanelButton(icon fyne.Resource, accent color.NRGBA, size float32, onTap func()) *compactButton {
	b := newCompactButton(icon, accent, size, onTap)
	b.SetOutline(true)
	return b
}

// SetOutline 切换成无底色图标按钮样式（通话面板）；图标按 baseBg 着色。
func (b *compactButton) SetOutline(on bool) {
	b.outline = on
	if on {
		b.bg.FillColor = color.NRGBA{}
		b.bg.StrokeWidth = 0
	} else {
		b.bg.FillColor = b.baseBg
	}
	b.applyIconColor()
	b.updateHoverBg()
	b.bg.Refresh()
	b.Refresh()
}

// SetAccent 更新强调色（outline 样式下即图标颜色，如静音=红、正常=蓝）。
func (b *compactButton) SetAccent(c color.NRGBA) {
	b.baseBg = c
	if b.outline {
		b.applyIconColor()
		return
	}
	b.bg.FillColor = c
	b.bg.Refresh()
}

// applyIconColor 按样式给图标着色：outline 用强调色（禁用时用主题禁用色），
// 普通样式保持原图标（白图标画在彩色底上）。
func (b *compactButton) applyIconColor() {
	if b.icon == nil || b.iconSrc == nil {
		return
	}
	switch {
	case b.disabled:
		// 禁用样式**两个窗口统一**：图标置灰、背景透明（同主窗体的配置按钮）
		b.icon.Resource = tintIcon(b.iconSrc, disabledIconColor())
	case b.outline:
		b.icon.Resource = tintIcon(b.iconSrc, b.baseBg)
	default:
		b.icon.Resource = b.iconSrc
	}
	b.icon.Refresh()
	b.rerasterIcon()
}

// rerasterIcon 强制 canvas.Image 重新栅格化当前图标。
//
// Fyne 的 canvas.Image 在**只换 Resource** 时 Refresh 不一定会重新渲染 SVG
// （实测：视频图标从蓝换成红，Resource 已经变了、画布上却仍是蓝色），而
// Image.Resize 到新尺寸会走 renderSVG 重新栅格化。图标尺寸微调 0.01 再改回来，
// 对布局无影响（随后布局会按 MinSize 归一）。转圈动画每帧换资源也要靠它，
// 否则"转了但画面不变"。
func (b *compactButton) rerasterIcon() {
	if b.icon == nil {
		return
	}
	w, h := b.icon.Size().Width, b.icon.Size().Height
	if w <= 0 || h <= 0 {
		return
	}
	// 必须改变**整数像素**尺寸：renderSVG 取整后尺寸相同会命中旧的缓存位图
	b.icon.Resize(fyne.NewSize(w+1, h+1))
	b.icon.Resize(fyne.NewSize(w, h))
}

// updateHoverBg outline 样式：hover 时淡填充 + 1px 轮廓，平时全透明。
func (b *compactButton) updateHoverBg() {
	if !b.outline {
		return
	}
	if b.hovered && !b.disabled {
		b.bg.FillColor = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x12}
		b.bg.StrokeColor = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x3c}
		b.bg.StrokeWidth = 1
	} else {
		b.bg.FillColor = color.NRGBA{}
		b.bg.StrokeColor = color.NRGBA{}
		b.bg.StrokeWidth = 0
	}
	b.bg.Refresh()
}

// disabledIconColor 禁用态图标/文字颜色：**深色主题用浅灰、浅色主题用深灰**
// （直接用主题的 ColorNameDisabled 在深色主题下是近黑的深灰，根本看不清）。
func disabledIconColor() color.NRGBA {
	if app := fyne.CurrentApp(); app != nil &&
		app.Settings().ThemeVariant() == theme.VariantLight {
		return color.NRGBA{R: 0x55, G: 0x55, B: 0x55, A: 0xff} // 浅色主题：深灰
	}
	return color.NRGBA{R: 0xb0, G: 0xb0, B: 0xb0, A: 0xff} // 深色主题：浅灰
}

// newLevelButton 音量按钮（通话面板的扬声器/麦克风，REQ-MEDIA-006 修订）：
// 无底色图标按钮 + **竖直进度条**（贴在按钮右缘内侧，高度 = 图标高度，
// 从底部按百分比填充；麦克风 100%→200% 的增益段橙色叠在蓝色之上）。
// 鼠标滚轮悬停其上可调值（onScroll 收到的是**格数**，已按 wheelNotch 折算）。
func newLevelButton(iconRes fyne.Resource, size float32, onScroll func(steps int, pos fyne.Position), onTap func()) *compactButton {
	b := newCompactButton(iconRes, colorHoldBlue, size, onTap)
	iconSize := float32(math.Round(float64(size) * 0.5))
	b.bar = newLevelBar(iconSize)
	b.scroll = newScrollRelay(onScroll)
	// 样式**最后**再切：SetOutline 里的 Refresh 会立即创建并缓存渲染器
	//（BaseWidget.Refresh 无条件 cache.Renderer），此时 bar 必须已经就位，
	// 否则缓存下来的是"没有进度条"的那套渲染对象（滚轮也一起丢）。
	b.SetOutline(true)
	return b
}

// levelBarW 竖直进度条宽度。
const levelBarW float32 = 3

// levelBar 竖直进度条：轨道 + 蓝色填充 + 橙色增益段（麦克风 100%→200%）。
// 高度由调用方给（= 按钮图标高度），从底部向上填充。
type levelBar struct {
	widget.BaseWidget
	level      float32 // 0–1：蓝色段
	boost      float32 // 0–1：橙色段（叠在蓝色之上）
	trackColor color.NRGBA
	w, h       float32
}

func newLevelBar(h float32) *levelBar {
	l := &levelBar{w: levelBarW, h: h, trackColor: colorLevelTrack}
	l.ExtendBaseWidget(l)
	return l
}

func (l *levelBar) MinSize() fyne.Size { return fyne.NewSize(l.w, l.h) }

func (l *levelBar) set(level, boost float32) {
	level, boost = clampLevel(level), clampLevel(boost)
	if l.level == level && l.boost == boost {
		return
	}
	l.level, l.boost = level, boost
	l.Refresh()
}

func (l *levelBar) setTrack(c color.NRGBA) {
	if l.trackColor == c {
		return
	}
	l.trackColor = c
	l.Refresh()
}

func (l *levelBar) CreateRenderer() fyne.WidgetRenderer {
	r := &levelBarRenderer{
		bar:    l,
		track:  canvas.NewRectangle(l.trackColor),
		blue:   canvas.NewRectangle(colorHoldBlue),
		orange: canvas.NewRectangle(colorLevelBoost),
	}
	for _, rect := range []*canvas.Rectangle{r.track, r.blue, r.orange} {
		rect.CornerRadius = levelBarW / 2
	}
	r.Layout(l.Size())
	return r
}

type levelBarRenderer struct {
	bar                 *levelBar
	track, blue, orange *canvas.Rectangle
}

func (r *levelBarRenderer) Layout(size fyne.Size) {
	w := size.Width
	if w <= 0 {
		w = levelBarW
	}
	h := size.Height
	r.track.Resize(fyne.NewSize(w, h))
	r.track.Move(fyne.NewPos(0, 0))
	bh := h * r.bar.level
	r.blue.Resize(fyne.NewSize(w, bh))
	r.blue.Move(fyne.NewPos(0, h-bh))
	oh := h * r.bar.boost
	r.orange.Resize(fyne.NewSize(w, oh))
	r.orange.Move(fyne.NewPos(0, h-oh))
}

func (r *levelBarRenderer) MinSize() fyne.Size { return r.bar.MinSize() }

func (r *levelBarRenderer) Refresh() {
	r.track.FillColor = r.bar.trackColor
	r.Layout(r.bar.Size())
	r.track.Refresh()
	r.blue.Refresh()
	r.orange.Refresh()
}

func (r *levelBarRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.blue, r.orange}
}

func (r *levelBarRenderer) Destroy() {}

// barRightLayout 音量按钮的内容布局：进度条贴右缘内侧、纵向居中（高度由进度条
// 自己的 MinSize 决定 = 图标高度），图标**紧挨着进度条左侧**（gap，用户要求两者
// 再靠近一点），纵向居中。
type barRightLayout struct{ inset, gap float32 }

func (l barRightLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	ic, bar := objs[0].MinSize(), objs[1].MinSize()
	return fyne.NewSize(ic.Width+l.gap+bar.Width+l.inset, maxf(ic.Height, bar.Height))
}

func (l barRightLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	ic, bar := objs[0], objs[1]
	bw := bar.MinSize().Width
	bh := bar.MinSize().Height
	bar.Resize(fyne.NewSize(bw, bh))
	bar.Move(fyne.NewPos(size.Width-bw-l.inset, (size.Height-bh)/2))
	iw := ic.MinSize().Width
	ih := ic.MinSize().Height
	ic.Resize(fyne.NewSize(iw, ih))
	ic.Move(fyne.NewPos(size.Width-bw-l.inset-l.gap-iw, (size.Height-ih)/2))
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
	b := newCompactButton(iconRes, bgColor, 32, onTap)
	b.label = canvas.NewText(text, labelColor)
	b.label.TextSize = 14
	b.labelColor = labelColor
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
	b.labelColor = labelColor
	b.ExtendBaseWidget(b)
	return b
}

func (b *compactButton) CreateRenderer() fyne.WidgetRenderer {
	if b.bar != nil {
		// 音量按钮：透明底 + 图标（紧挨进度条）+ 右缘竖直进度条 + 滚轮中继
		//（中继必须排在**最后**：命中测试取遍历里最后一个匹配者）
		return widget.NewSimpleRenderer(container.NewStack(b.bg,
			container.New(barRightLayout{inset: 3, gap: 4}, container.NewCenter(b.icon), b.bar),
			b.scroll))
	}
	if b.label != nil && b.icon != nil {
		return widget.NewSimpleRenderer(container.NewStack(b.bg,
			container.NewCenter(container.New(iconTextLayout{},
				b.icon, b.label))))
	}
	if b.label != nil {
		return widget.NewSimpleRenderer(container.NewStack(b.bg,
			container.NewBorder(padRect(3), padRect(3), padRect(8), padRect(8), b.label)))
	}
	return widget.NewSimpleRenderer(container.NewStack(b.bg,
		container.New(centerShiftLayout{dx: b.iconDX, dy: b.iconDY}, b.icon)))
}

// centerShiftLayout 单个子对象**水平居中**、纵向居中后偏移 dy（图标微调用）。
// 不能用 shiftedLayout（那个是 x=0 靠左对齐，用于整行内容）。
type centerShiftLayout struct {
	dx, dy float32
}

func (centerShiftLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	if len(objs) == 0 {
		return fyne.Size{}
	}
	return objs[0].MinSize()
}

func (l centerShiftLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	m := objs[0].MinSize()
	objs[0].Resize(m)
	objs[0].Move(fyne.NewPos((size.Width-m.Width)/2+l.dx, (size.Height-m.Height)/2+l.dy))
}

// SetIconOffset 图标位置微调（像素，正值右/下移）：个别图标原始画得偏了，用它对齐。
func (b *compactButton) SetIconOffset(dx, dy float32) {
	b.iconDX, b.iconDY = dx, dy
	b.Refresh()
}

// SetIconOffsetY 只调纵向（等价 SetIconOffset(0, dy)）。
func (b *compactButton) SetIconOffsetY(dy float32) { b.SetIconOffset(b.iconDX, dy) }

// SetTrack 更新进度条的轨道色（音量按钮：0% 用浅灰=禁用样式，非 0 用深灰轨道）。
func (b *compactButton) SetTrack(c color.NRGBA) {
	if b.bar != nil {
		b.bar.setTrack(c)
	}
}

// SetLevel 更新蓝色填充比例（0–1），不叠增益橙（扬声器用）。
func (b *compactButton) SetLevel(level float32) { b.SetLevels(level, 0) }

// SetLevels 更新竖直进度条（0–1）：level 蓝色段、boost 橙色增益段
// （叠在蓝色之上，麦克风 100%→200% 的那一段）。
func (b *compactButton) SetLevels(level, boost float32) {
	if b.bar == nil {
		return
	}
	b.bar.set(level, boost)
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
	b.iconSrc = res
	b.applyIconColor()
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
// SetBg 更新底色。**同时更新 baseBg**：`Enable()` 按 baseBg 还原背景
// （禁用时置灰），不更新的话"改过底色 → 禁用 → 启用"会回到构造时的旧色
// （踩过：主窗体呼叫按钮设成蓝底后，`applyMainCallMode(false)` 的 Enable
// 又把它变回绿色）。
func (b *compactButton) SetBg(c color.NRGBA) {
	b.baseBg = c
	if b.disabled {
		return // 禁用中：保持"透明底 + 灰图标"，恢复时用新底色
	}
	if b.outline { // 无底色样式：底色即图标颜色
		b.applyIconColor()
		return
	}
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
	b.applyIconColor()
	if b.label != nil {
		if v {
			b.label.Color = disabledIconColor()
		} else {
			b.label.Color = b.labelColor
		}
		b.label.Refresh()
	}
	if b.outline {
		b.updateHoverBg()
		b.Refresh()
		return
	}
	if b.bg != nil {
		// 禁用时背景透明（与主窗体配置按钮一致）；恢复时还原底色
		if v {
			b.bg.FillColor = color.NRGBA{}
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

// setTwoPartyStateLabel 两方通话的状态文案（保持中 / 通话中）。
// 从咨询/三方/监听/会议回到两方时状态行必须复位（否则会一直停在
// "咨询中"/"三方通话中"…，用户实测）。
func (a *App) setTwoPartyStateLabel() {
	if a.callStateLabel == nil {
		return
	}
	if a.activeCallID != "" && a.core.IsCallMuted(a.activeCallID) {
		a.callStateLabel.SetText("通话保持中")
		return
	}
	a.callStateLabel.SetText("通话中")
}

// setHoldButton 保持状态变化时更新保持按钮外观与状态文案。
func (a *App) setHoldButton(muted bool) {
	if a.holdBtn == nil {
		return
	}
	if muted {
		a.holdBtn.SetIcon(IconReplayWhite) // 保持中：恢复图标
		// 取消保持仍用**黄色**（用户要求，不要变成蓝色）
		a.holdBtn.SetBg(colorHoldYellow)
		a.holdBtn.setTip(a.callTip, "取消保持")
	} else {
		a.holdBtn.SetIcon(IconPauseWhite)
		a.holdBtn.SetBg(colorHoldYellow)
		a.holdBtn.setTip(a.callTip, "保持")
	}
	// 状态文案只在**两方通话**时由保持状态决定：咨询/三方/监听/会议各有自己的
	// 文案（咨询中/三方通话中/监听中/会议中），不能被保持事件冲掉
	//（consult 期间通常要停掉原对端的媒体，call.muted 事件会把"咨询中"改成"通话中"）。
	if a.talkState() == core.TalkTwoParty && a.callStateLabel != nil {
		if muted {
			a.callStateLabel.SetText("通话保持中")
		} else {
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
	// CTI 状态复位（REQ-ACC-026）：话务结束清掉拨号复用模式与"在途"按钮
	a.stopCTISpin()
	if a.ctiDialMode != "" {
		a.ctiDialMode = ""
		a.updateCallButton()
	}
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

// ---- 通话面板顶部的拖动条（REQ-UI-040）----
//
// 面板是无边框窗口，没有标题栏可拖；顶部的这条细线就是它的"抓手"：
// 悬停变"可移动"光标，按住拖即可移动窗口，拖完记录位置（下次弹出、重启沿用）。
// 没有锁——面板本来就小，随时可拖比多一次"解锁"更顺手。
const (
	// callGripLineTop 细线距面板顶边的距离（DIP）。
	callGripLineTop = 4
	// callGripLineHeight 细线高度（DIP）。
	callGripLineHeight = 2
	// callGripHitH 可抓取区域的高度（DIP）：比细线高一截，2px 太难点中；
	// 取 12 也不会碰到第一行内容（三行内容从 y=14 才开始）。
	callGripHitH = 12
)

// 拖动条颜色：深色主题用深灰、浅色主题用浅灰（都由调用方按主题变体选）。
var (
	colorGripDark  = color.NRGBA{R: 0x55, G: 0x5a, B: 0x61, A: 0xff} // 深色主题：深灰
	colorGripLight = color.NRGBA{R: 0xc6, G: 0xca, B: 0xce, A: 0xff} // 浅色主题：浅灰
)

// callGripColor 按当前主题明暗取拖动条颜色。
func callGripColor() color.NRGBA {
	if fyne.CurrentApp() != nil && fyne.CurrentApp().Settings().ThemeVariant() == theme.VariantLight {
		return colorGripLight
	}
	return colorGripDark
}

// callGrip 通话面板顶部的拖动条：**只**在自身这一小块区域（面板宽度的一半、
// 12px 高）里可拖，不实现 Tappable/Hoverable，所以面板上的按钮点击、名称悬停
// 完全不受影响；实现了 Cursorable——指针停上来时鼠标变成"可移动"样式。
type callGrip struct {
	widget.BaseWidget
	line        *canvas.Rectangle
	onDragStart func()
	onDragEnd   func()
}

func newCallGrip(onStart, onEnd func()) *callGrip {
	g := &callGrip{onDragStart: onStart, onDragEnd: onEnd}
	g.line = canvas.NewRectangle(callGripColor())
	g.line.CornerRadius = callGripLineHeight / 2 // 两端小圆角，像一条"握把"
	g.ExtendBaseWidget(g)
	return g
}

// MinSize 拖动条自身的尺寸：宽度取面板宽度的一半（居中对齐见 callGripLayer），
// 高度是可抓取区域的高度（比细线高一截）。
func (g *callGrip) MinSize() fyne.Size {
	return fyne.NewSize(callPanelWidth/2, callGripHitH)
}

func (g *callGrip) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.New(callGripLineLayout{}, g.line))
}

// Refresh 重取细线颜色：主题明暗切换时 Fyne 会刷新控件树，颜色得跟着变
// （深色主题深灰、浅色主题浅灰）。
func (g *callGrip) Refresh() {
	g.line.FillColor = callGripColor()
	g.line.Refresh()
	g.BaseWidget.Refresh()
}

// Cursor 悬停到拖动条上时显示"可移动"光标。
func (g *callGrip) Cursor() desktop.Cursor { return moveCursor{} }

// Dragged 每帧都会被调，但只有第一次需要真正开始拖动会话（重复 begin 会
// 重新锚定，窗口反而跟不动）；会话自己按 ~60Hz 跟随光标，UI 线程零负担。
func (g *callGrip) Dragged(*fyne.DragEvent) {
	if g.onDragStart != nil {
		g.onDragStart()
	}
}

func (g *callGrip) DragEnd() {
	if g.onDragEnd != nil {
		g.onDragEnd()
	}
}

// callGripLineLayout 把细线摆在抓手区域里：满宽、距顶 callGripLineTop、
// 高 callGripLineHeight（其余是看不见的可抓取留白）。
type callGripLineLayout struct{}

func (callGripLineLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, callGripLineTop+callGripLineHeight)
}

func (callGripLineLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	o := objs[0]
	o.Resize(fyne.NewSize(size.Width, callGripLineHeight))
	o.Move(fyne.NewPos(0, callGripLineTop))
}

// callGripLayer 把拖动条摆在面板顶部正中的定位层。
// 本身不实现任何指针接口（点击/悬停/拖动都穿透），只有拖动条那一小块参与命中测试。
type callGripLayer struct{}

func (callGripLayer) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(0, 0) }

func (callGripLayer) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	o := objs[0]
	m := o.MinSize()
	o.Resize(m)
	o.Move(fyne.NewPos((size.Width-m.Width)/2, 0))
}

// onCallPanelDragStart 拖动开始（拖动条上按下并移动超过阈值才会到这里）。
func (a *App) onCallPanelDragStart() {
	if a.callW == nil || a.callDragging {
		return
	}
	a.callDragging = true
	beginCallPanelDrag(a.callW)
}

// onCallPanelDragEnd 拖动结束：读回窗口位置并持久化（下次弹出、重启后都用它）。
func (a *App) onCallPanelDragEnd() {
	a.callDragging = false
	if a.callW == nil {
		return
	}
	x, y, ok := endCallPanelDrag(a.callW)
	if !ok {
		return // 读不到位置（句柄/会话异常）就不写，别把默认摆放误存成"用户位置"
	}
	if p := a.cfg.UI.CallPanel; p.Moved && p.X == x && p.Y == y {
		return
	}
	a.cfg.UI.CallPanel = config.WindowPlacement{Moved: true, X: x, Y: y}
	a.persistConfig()
	logx.Logger().Info("通话面板位置已保存", "x", x, "y", y)
}

// callSkillTag 通话面板上的技能组小标签（协议 §16.2 CALL_RINGING 的
// skillGroup：外线呼入分配坐席时用的技能组），显示在对端号码右侧。
// 与在线座席页的 tag 同一套外观（tagLayout + 圆角底色 + 小字）。
type callSkillTag struct {
	widget.BaseWidget
	bg  *canvas.Rectangle
	txt *plainText
	box *fyne.Container
}

func newCallSkillTag() *callSkillTag {
	size := fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText)
	b := &callSkillTag{}
	b.txt = newThemedText("", float32(size)-3, false)
	b.bg = canvas.NewRectangle(theme.Color(theme.ColorNameButton))
	b.bg.CornerRadius = 3
	b.box = container.New(tagLayout{padX: 5, padY: 1}, b.bg, b.txt)
	b.ExtendBaseWidget(b)
	b.Hide()
	return b
}

func (b *callSkillTag) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(b.box)
}

// Set 更新技能组；空串表示隐藏标签（该通话没有技能组）。
func (b *callSkillTag) Set(s string) {
	if s == "" {
		b.Hide()
		return
	}
	b.txt.SetText(s)
	b.Show()
	b.box.Refresh()
	b.Refresh()
}

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
	tag  *callSkillTag
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
	b.tag = newCallSkillTag()
	b.box = container.New(markAfterLayout{gap: 4}, b.note, b.star, b.tag)
	b.ExtendBaseWidget(b)
	return b
}

func (b *callNameBox) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(b.box)
}

// MinSize 限制宽度：面板窄，长号码/长名字只能截断显示。
//
// **宽度必须按原始文本算**（`note.fullWidth()`），不能拿 `box.MinSize()`：
// plainText 被截断后 MinSize 只剩省略号的宽度，面板行又按 MinSize 分配宽度，
// 于是"号码永远显示成 …"，再也恢复不了（用户实测：拨号时前面的号码变成 …）。
func (b *callNameBox) MinSize() fyne.Size {
	w := b.note.fullWidth()
	h := b.note.MinSize().Height
	if b.star.Visible() {
		w += 4 + b.star.MinSize().Width // 4 = newCallNameBox 里 markAfterLayout 的间距
		h = fyne.Max(h, b.star.MinSize().Height)
	}
	if b.tag.Visible() {
		w += 4 + b.tag.MinSize().Width
		h = fyne.Max(h, b.tag.MinSize().Height)
	}
	if w > callNameMaxW {
		w = callNameMaxW
	}
	return fyne.NewSize(w, h)
}

// Set 更新对端展示：文本、是否常用（★）、备注（悬停提示）、技能组标签。
func (b *callNameBox) Set(text string, starred bool, note, skill string) {
	b.note.SetText(text)
	b.note.SetNote(note)
	if starred {
		b.star.Show()
	} else {
		b.star.Hide()
	}
	b.tag.Set(skill)
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
//   - Call-Info 给了 display-name（`call.PeerName`）：**它优先于通信录**；
//   - 否则命中通信录：号码在前、名称在后（用户要求"号码右边显示名称"）；
//   - 都没有：沿用原行为（SIP 显示名优先，其次号码）。
//
// 同时返回是否常用（★）与备注（悬停提示用）。
func (a *App) callDisplay(call *core.Call) (text string, starred bool, note string) {
	if call == nil {
		return "", false, ""
	}
	remote := call.Remote
	c, hasContact := a.contactFor(remote)
	if hasContact {
		starred, note = c.Starred, c.Note
	}
	// 名字优先级：Call-Info 的 display-name > 通信录名称 > SIP 显示名
	name := call.PeerName
	if name == "" && hasContact {
		name = c.Name
	}
	if name == "" {
		name = call.RemoteName
	}
	switch {
	case name == "" || name == remote:
		text = remote
	case remote == "":
		text = name
	default:
		text = remote + " " + name
	}
	return text, starred, note
}

// setCallName 更新通话面板的对端展示。
func (a *App) setCallName(call *core.Call) {
	if a.callName == nil {
		return
	}
	text, starred, note := a.callDisplay(call)
	// 技能组（协议 §16.2 CALL_RINGING 的 skillGroup）：对端号码右侧的小标签
	skill := ""
	if call != nil {
		skill = call.CTI.SkillGroup
	}
	a.callName.Set(text, starred, note, skill)
}

// callRemoteName 通话对象的展示名称（Call-Info 名称优先，其次 SIP 显示名，最后号码）。
func callRemoteName(call *core.Call) string {
	if call.PeerName != "" {
		return call.PeerName
	}
	if call.RemoteName != "" {
		return call.RemoteName
	}
	return call.Remote
}

// iconTextGap 图标按钮里「图标 - 文字」的间距（用户要求比默认的 4px 再分开点）。
const iconTextGap = 7

// iconTextLayout 图标 + 文字的紧凑水平布局：图标在前、文字在后、纵向居中。
type iconTextLayout struct{}

func (iconTextLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	ic, lb := objs[0].MinSize(), objs[1].MinSize()
	return fyne.NewSize(ic.Width+iconTextGap+lb.Width, maxf(ic.Height, lb.Height))
}

func (l iconTextLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	ic, lb := objs[0], objs[1]
	im := ic.MinSize()
	lm := lb.MinSize()
	ic.Resize(im)
	ic.Move(fyne.NewPos(0, (size.Height-im.Height)/2))
	lb.Resize(lm)
	lb.Move(fyne.NewPos(im.Width+iconTextGap, (size.Height-lm.Height)/2))
}

func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
