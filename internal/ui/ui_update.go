package ui

import (
	"fmt"
	"image/color"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// ---------------------------------------------------------------------------
// 主窗体提示行（呼叫按钮上方，REQ-UPD-003）
// ---------------------------------------------------------------------------

// pointerButton 悬停显示手型光标的按钮：Fyne 的 widget.Button 悬停是箭头，
// 点击态控件应提示可点（与紧凑按钮的 Cursor 行为一致）。
type pointerButton struct {
	widget.Button
}

func newPointerButton(tapped func()) *pointerButton {
	b := &pointerButton{}
	b.ExtendBaseWidget(b)
	b.OnTapped = tapped
	b.Importance = widget.LowImportance
	return b
}

func (b *pointerButton) Cursor() desktop.Cursor { return desktop.PointerCursor }

// buildUpdateBar 构造更新提示行：单行按钮（最初的形式），文案形如
// 「更新 v1.0.3 来源 10.168.4.160」；点击开始下载，下载期间变为
// 「更新中...」并禁用。按钮下方留白与呼叫按钮拉开距离。
func (a *App) buildUpdateBar() {
	a.updateBarBtn = newPointerButton(a.onUpdateBarTap)
	a.updateBar = container.NewVBox(a.updateBarBtn, padRect(4))
	a.updateBar.Hide()
}

// setUpdateBar 按更新状态刷新提示行文案、可点击状态与显隐。
func (a *App) setUpdateBar(st core.UpdateStatus) {
	if a.updateBarBtn == nil {
		return
	}
	text, enabled, show := updateBarText(st)
	if !show {
		a.updateBar.Hide()
		if a.mainContent != nil {
			a.mainContent.Refresh()
		}
		return
	}
	a.updateBarBtn.SetText(text)
	if enabled {
		a.updateBarBtn.Enable()
	} else {
		a.updateBarBtn.Disable()
	}
	a.updateBar.Show()
	// 显示/隐藏后必须让父容器重新布局：否则提示行要等到下一次
	// 整体重排（如关闭弹窗）才出现，表现为"收到通知后按钮迟迟不显示"
	//（与切换拨号盘/设置页同一手法）
	if a.mainContent != nil {
		a.mainContent.Refresh()
	}
}

// updateBarText 提示行文案：返回（文案, 是否可点击, 是否显示）。
// 文案必须简短——主窗体固定 320px 宽，来源主机名过长时截断；
// 失败详情只放在更新提示窗里。
func updateBarText(st core.UpdateStatus) (string, bool, bool) {
	host := st.Host
	if len(host) > 24 {
		host = host[:24] + "…"
	}
	switch st.Phase {
	case core.UpdateAvailable, core.UpdateFailed:
		// 可点击开始/重新开始下载（失败后点击=重试）
		return fmt.Sprintf("更新 v%s 来源 %s", st.Info.Version, host), true, true
	case core.UpdateDownloading, core.UpdateReady:
		// 下载中/已就绪待重启：按钮禁用，进度看右下角提示窗
		return "更新中...", false, true
	}
	return "", false, false
}

// onUpdateBarTap 提示行点击：按当前状态开始下载/重试、查看进度或重启安装。
func (a *App) onUpdateBarTap() {
	switch a.core.UpdateStatus().Phase {
	case core.UpdateAvailable, core.UpdateFailed:
		a.startUpdateDownload()
	case core.UpdateReady:
		a.applyUpdate()
	default: // downloading 等：弹出提示窗查看进度
		a.showUpdatePopup()
	}
}

// startUpdateDownload 开始（或重试）下载并弹出提示窗展示进度。
func (a *App) startUpdateDownload() {
	if err := a.core.StartUpdate(); err != nil {
		logx.Logger().Warn("开始下载更新失败", "err", err)
	}
	a.showUpdatePopup()
}

// applyUpdate 替换可执行文件并重启（通话中挂起，通话结束后自动执行）。
func (a *App) applyUpdate() {
	if err := a.core.ApplyUpdate(); err != nil {
		logx.Logger().Warn("应用更新失败", "err", err)
	}
	a.showUpdatePopup()
}

// ---------------------------------------------------------------------------
// 右下角更新提示窗（REQ-UPD-003）
// ---------------------------------------------------------------------------

// progressBarHeight 下载进度条高度（DIP）：高度需容下进度条中间的
// 百分比文字（12px 太细，文字包不住）。
const progressBarHeight = 24

// thinProgressLayout 进度条布局：左右与按钮行同样的内缩进（与按钮左边缘
// 对齐、右侧对称）、固定高度并垂直居中（container.NewCenter 会让进度条
// 按最小宽度收缩，显得很短）。
type thinProgressLayout struct{}

func (thinProgressLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, progressBarHeight)
}

func (thinProgressLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	inset := updateButtonsInset()
	w := size.Width - inset*2
	if w < 0 {
		w = 0
	}
	for _, o := range objs {
		o.Move(fyne.NewPos(inset, (size.Height-progressBarHeight)/2))
		o.Resize(fyne.NewSize(w, progressBarHeight))
	}
}

// updateButtonsLayout 更新提示窗按钮行布局：左边缘与上方文字的第一个字
// 对齐——标签文本的内缩进等于主题的 InnerPadding（本项目主题为 12px），
// 所以按钮从这里起排；按钮间距固定。
type updateButtonsLayout struct{}

func (updateButtonsLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	w := updateButtonsInset()
	h := float32(0)
	first := true
	for _, o := range objs {
		if !o.Visible() {
			continue // 隐藏的按钮不占位（如下载中只剩「取消下载」）
		}
		m := o.MinSize()
		if !first {
			w += updateButtonGap
		}
		first = false
		w += m.Width
		if m.Height > h {
			h = m.Height
		}
	}
	return fyne.NewSize(w, h)
}

func (updateButtonsLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	x := updateButtonsInset()
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(m.Width, size.Height))
		x += m.Width + updateButtonGap
	}
}

// updateButtonGap 按钮间距（DIP）。
const updateButtonGap = 12

// updateButtonsInset 按钮行左侧内缩进：取主题 InnerPadding（标签文本的
// 起始位置），取不到时退回 12。
func updateButtonsInset() float32 {
	app := fyne.CurrentApp()
	if app == nil || app.Settings() == nil || app.Settings().Theme() == nil {
		return 12
	}
	return app.Settings().Theme().Size(theme.SizeNameInnerPadding)
}

// buildUpdatePanelIfNeeded 创建更新提示窗（懒创建单例，范式同通话面板）：
// 无装饰、置顶、吸附右下角（Show 后由平台函数处理，见 topmost_*.go）。
func (a *App) buildUpdatePanelIfNeeded() {
	if a.updW != nil {
		return
	}
	// 同通话面板：用 splash 窗口做无边框，不走 Win32 硬改样式（见 newBorderlessWindow）
	w := a.newBorderlessWindow(updateWindowTitle())
	a.updW = w
	// 关闭（Alt+F4 等）：非下载态＝稍后（隐藏窗口，主窗体提示行保留）；
	// 下载中＝取消下载（不做"后台下载"——窗口关闭后进度不可见，
	// 出问题无从排查）
	w.SetCloseIntercept(func() {
		if a.core.UpdateStatus().Phase == core.UpdateDownloading {
			a.core.CancelUpdate()
		}
		a.hideUpdatePopup()
	})

	// 文本一律允许换行（TextWrapBreak：中文无空格也能断行），
	// 保证内容不会超出固定尺寸的窗口（此前长文本被裁切、
	// 底部按钮点不到——bug 修复）
	a.updTitleLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	a.updTitleLabel.Wrapping = fyne.TextWrapBreak
	a.updInfoLabel = widget.NewLabel("")
	a.updInfoLabel.Wrapping = fyne.TextWrapBreak
	a.updStateLabel = widget.NewLabel("")
	a.updStateLabel.Wrapping = fyne.TextWrapBreak
	a.updProgress = widget.NewProgressBar()

	// 按钮文字用黑色：白字在绿底/灰底上对比度差（用户反馈看不清）
	a.updActionBtn = newCompactTextOnlyButton("立即更新", colorSuccessGreen, colorIconBlack, a.onUpdateActionButton)
	a.updLaterBtn = newCompactTextOnlyButton("稍后", colorSwitchGray, colorIconBlack, func() { a.hideUpdatePopup() })

	// 按钮行左边缘与上方文字的第一个字对齐（布局内缩进 = 标签文本的
	// InnerPadding，见 updateButtonsLayout）
	buttons := container.New(updateButtonsLayout{}, a.updLaterBtn, a.updActionBtn)
	// 状态行与进度条共用固定高度的槽位：下载时只显示进度条（无文字），
	// 切换时按钮垂直位置与窗口高度都不变；进度条用 Center 保持自身较矮的高度
	// 状态行占位矩形的高度取"状态标签的实际高度"（本项目主题的
	// InnerPadding=12，标签比固定 35 高）：标签与进度条切换时行高不变，
	// 按钮在 Y 轴上就不会上下跳
	a.updStateLabel.SetText("正在下载更新") // 任意单行文案，仅用于量高度
	rowH := a.updStateLabel.MinSize().Height
	a.updStateLabel.SetText("")
	if rowH < progressBarHeight {
		rowH = progressBarHeight
	}
	a.updStatePad = canvas.NewRectangle(color.Transparent)
	a.updStatePad.SetMinSize(fyne.NewSize(1, rowH))
	a.updStateRow = container.NewStack(a.updStatePad, a.updStateLabel,
		container.New(thinProgressLayout{}, a.updProgress))
	body := container.NewVBox(
		a.updTitleLabel,
		a.updInfoLabel,
		widget.NewSeparator(),
		a.updStateRow,
		buttons,
	)
	w.SetContent(container.NewBorder(padRect(10), padRect(10), padRect(10), padRect(10), body))
	w.SetPadded(false)
	w.Resize(fyne.NewSize(updatePanelWidth, updatePanelHeight))
	if dw, ok := w.(desktop.Window); ok {
		dw.RequestAlwaysOnTop()
	}
}

// showUpdatePopup 显示提示窗（首次调用时创建）并吸附右下角，不抢焦点。
func (a *App) showUpdatePopup() {
	logx.Logger().Info("弹出更新提示窗", "version", a.lastUpdate.Info.Version)
	a.buildUpdatePanelIfNeeded()
	a.renderUpdateStatus(a.core.UpdateStatus())
	a.updW.Show()
	a.updShown = true
	moveUpdateWindowToCorner(a.updW)
}

// hideUpdatePopup 隐藏提示窗（状态保留，主窗体提示行仍在）。
func (a *App) hideUpdatePopup() {
	if a.updW != nil {
		a.updW.Hide()
	}
	a.updShown = false
}

// renderUpdateStatus 按更新状态刷新提示窗内容。
func (a *App) renderUpdateStatus(st core.UpdateStatus) {
	if a.updW == nil {
		return
	}
	a.updTitleLabel.SetText("发现新版本 v" + st.Info.Version)
	// 来源主机名过长时截断：信息行固定两行，否则会把下面的按钮挤下去
	host := st.Host
	if len(host) > 32 {
		host = host[:32] + "…"
	}
	a.updInfoLabel.SetText(fmt.Sprintf("当前 v%s · %s/%s\n来源 %s",
		core.Version, runtime.GOOS, runtime.GOARCH, host))
	// 默认显示状态文字、隐藏进度条（下载分支再覆盖；两者共用同一槽位，
	// 切换时按钮位置与窗口高度不变）；副按钮默认显示（下载中隐藏，
	// 不提供"后台下载"）
	a.updStateLabel.Show()
	a.updProgress.Hide()
	a.updLaterBtn.Show()

	switch st.Phase {
	case core.UpdateAvailable:
		a.updStateLabel.SetText("是否立即下载并安装？")
		a.updProgress.Hide()
		a.updActionBtn.SetText("立即更新")
		a.updActionBtn.SetBg(colorSuccessGreen)
		a.updLaterBtn.SetText("稍后")
	case core.UpdateDownloading:
		// 下载中只显示进度条，不显示任何文字（按钮位置与窗口高度不变）；
		// 仅保留「取消下载」，不提供"后台下载"（窗口隐藏后进度不可见）
		a.updStateLabel.Hide()
		a.updProgress.SetValue(st.Progress / 100)
		a.updProgress.Show()
		a.updActionBtn.SetText("取消下载")
		a.updActionBtn.SetBg(colorSwitchGray)
		a.updLaterBtn.Hide()
	case core.UpdateReady:
		if st.WaitingCall {
			a.updStateLabel.SetText("通话结束后将自动重启安装")
		} else {
			a.updStateLabel.SetText("下载完成，正在重启安装…")
		}
		a.updProgress.Hide()
		a.updActionBtn.SetText("重启安装")
		a.updActionBtn.SetBg(colorSuccessGreen)
		a.updLaterBtn.SetText("稍后")
	case core.UpdateFailed:
		a.updStateLabel.SetText(st.Error)
		a.updProgress.Hide()
		a.updActionBtn.SetText("重试")
		a.updActionBtn.SetBg(colorSuccessGreen)
		a.updLaterBtn.SetText("关闭")
	}
	// 窗口尺寸固定，不随状态变化（用户要求：下载时窗口不得被撑高）。
	// 内容最小尺寸仅用于日志：一旦超过窗口尺寸说明文案放不下会被裁切
	if c := a.updW.Content(); c != nil {
		min := c.MinSize()
		logx.Logger().Debug("更新提示窗内容尺寸",
			"min", fmt.Sprintf("%.0fx%.0f", min.Width, min.Height),
			"window", fmt.Sprintf("%dx%d", updatePanelWidth, updatePanelHeight))
		if min.Width > updatePanelWidth || min.Height > updatePanelHeight {
			logx.Logger().Warn("更新提示窗内容超出窗口尺寸，可能被裁切",
				"min", fmt.Sprintf("%.0fx%.0f", min.Width, min.Height))
		}
	}
}

// onUpdateActionButton 提示窗主按钮：按当前状态执行对应动作。
func (a *App) onUpdateActionButton() {
	switch a.lastUpdate.Phase {
	case core.UpdateAvailable, core.UpdateFailed:
		a.startUpdateDownload()
	case core.UpdateDownloading:
		a.core.CancelUpdate()
	case core.UpdateReady:
		a.applyUpdate()
	}
}

// onUpdateStatus 处理 system.update 事件（Fyne 主线程）。
// 弹窗策略（已确认的产品决策）：仅本次运行内首次收到某版本时弹出；
// 关闭后主窗体提示行保留；通话中不弹（两个置顶窗不抢右下角），
// 通话结束后补弹。
func (a *App) onUpdateStatus(st core.UpdateStatus) {
	a.lastUpdate = st
	logx.Logger().Debug("界面收到更新状态", "phase", st.Phase, "version", st.Info.Version,
		"uiState", a.uiState, "pending", a.updatePopupPending, "popup", a.updShown)
	// 本次运行内首次收到某版本 → 弹窗；通话中则挂起到通话结束（setCallUI）
	if st.Phase == core.UpdateAvailable && a.notifiedVersion != st.Info.Version {
		a.notifiedVersion = st.Info.Version
		a.updatePopupPending = true
	}
	if a.updatePopupPending && a.uiState == uiIdle {
		a.updatePopupPending = false
		a.showUpdatePopup()
	}
	// 窗口已创建（无论当前是否可见）都刷新内容，下次 Show 即最新状态
	a.renderUpdateStatus(st)
	a.setUpdateBar(st)
}
