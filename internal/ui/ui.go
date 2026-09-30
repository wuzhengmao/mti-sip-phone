// Package ui 实现 Fyne 桌面界面：系统托盘、主界面（拨号盘 + 设置页）、
// 来电置顶窗口、通话置顶面板。
//
// 交互约定（调研确认）：Fyne 托盘底层无双击事件，SetSystemTrayWindow
// 为"单击托盘图标唤出窗口"，本软件采用单击唤出 + 托盘菜单（与多数
// Windows 软件的托盘交互一致）。
package ui

import (
	"context"
	"fmt"
	"image/color"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/systray"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/audio"
	"mti-sip-phone/internal/autostart"
	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/sip"
)

// App GUI 应用：组装 Fyne 窗口与托盘，订阅核心事件驱动界面。
type App struct {
	core      *core.App
	audioMgr  *audio.Manager
	cfg       *config.Config
	cfgPath   string
	onRestart func() // 重启回调（本地端口修改后触发，REQ-UI-017）

	fyne  fyne.App
	desk  desktop.App
	mainW fyne.Window // 主窗口：仅空闲状态显示
	callW fyne.Window // 通话面板：呼叫/振铃/通话中显示，置顶右下角

	// 主窗口：内容切换容器（拨号盘 / 设置页）
	mainContent *fyne.Container
	dialPage    *fyne.Container
	settingsPage *fyne.Container

	// 拨号盘控件
	dialEntry    *widget.Entry // 已废弃：由 dialSelect 替代（保留字段避免大改）
	dialSelect   *widget.SelectEntry
	regDot       *statusDot
	regLabel     *widget.Label // 本机号码显示
	autoAnswerBtn *widget.Button
	callBtn      *compactButton
	settingsBtn  *widget.Button

	// 设置页控件
	settingsScroll    *container.Scroll
	accServerAddr     *widget.Entry // SIP 服务器（含端口，如 10.168.4.40:15060）
	accExpires        *widget.Entry // 注册到期时间（秒）
	accProxy          *widget.Entry // 出站代理（可选）
	accStunCheck      *widget.Check
	accStunServer     *widget.Entry
	rportCheck        *widget.Check
	accTransport      *widget.Select
	accAccount   *widget.Entry
	accAuthUser  *widget.Entry
	accPassword  *widget.Entry
	accDisplay   *widget.Entry
	accEnabled   *widget.Check
	micSelect    *widget.Select
	speakerSelect *widget.Select
	ringSpeakerSelect *widget.Select
	logFileCheck *widget.Check
	sipTraceCheck *widget.Check
	autoStartCheck *widget.Check // 开机自动启动（REQ-UI-022）
	logLevelSel  *widget.Select
	logDirLabel  *widget.Label
	wsPortEntry  *widget.Entry // 本地 WS 监听端口（默认 8765，修改后重启生效）

	// 通话面板控件（呼叫/振铃/通话三态共用）
	callNameLabel  *widget.Label
	callStateLabel *widget.Label
	callTimerLabel *widget.Label
	callRows       *fyne.Container // 通话面板三行内容容器（按状态重建）
	hangupBtn      *compactButton
	answerBtn      *compactButton
	holdBtn        *compactButton
	speakerBtn     *compactButton // 扬声器开关（REQ-UI-023）
	micBtn         *compactButton // 麦克风开关（REQ-UI-023）
	pulseStop      chan struct{} // 振铃脉动动画停止通道
	ringFlashRect  *canvas.Rectangle // 振铃边框闪烁描边（无边框窗口用，REQ-UI-025 修订）

	// 主窗口呼叫状态控件
	keypadBtns []*widget.Button // 3×4 拨号键（空闲输入号码 / 通话中发 DTMF）

	// 通话状态
	activeCallID string
	activeCall   *core.Call
	uiState      int // uiIdle / uiDialing / uiRinging / uiInCall
	savedNumber  string
	callStartAt  time.Time
	timerStop    chan struct{}
	regState     string
	autoAnswer   bool // 自动接听开关

	// 扬声器/麦克风开关（REQ-UI-023）：会话内记忆——通话面板再次弹出时
	// 沿用上次状态；不持久化，应用重启后恢复默认都打开。
	speakerOn bool
	micOn     bool

	// 主窗口可见性跟踪（Fyne 无 Visible() 查询，自行维护）：
	// mainShown 当前是否显示；mainWasVisible 通话前是否可见
	//（通话结束后仅当通话前可见时恢复显示主窗口）
	mainShown      bool
	mainWasVisible bool

	// loadingForm 表单回填标志：回填期间屏蔽 OnChanged 自动保存，避免
	// 初始化把旧值覆盖进配置
	loadingForm bool
}

// 通话 UI 状态：主窗口仅在 uiIdle 显示，其余状态由通话面板承载。
const (
	uiIdle    = iota // 空闲：主窗口显示，拨号盘可输入
	uiDialing        // 呼出中：主窗口隐藏，通话面板显示"呼叫中"
	uiRinging        // 呼入振铃：主窗口隐藏，通话面板显示"来电"
	uiInCall         // 通话中：主窗口隐藏，通话面板显示"通话中"
)

// New 创建 GUI 应用。cfgPath 为配置文件路径（供保存配置使用）。
// audioMgr 为 nil 时（音频后端不可用）设备区显示占位；
// onRestart 为重启回调（可为 nil，本地端口修改后的重启提示将不生效）。
func New(c *core.App, audioMgr *audio.Manager, cfg *config.Config, cfgPath string, onRestart func()) *App {
	ui := &App{
		core:      c,
		audioMgr:  audioMgr,
		cfg:       cfg,
		cfgPath:   cfgPath,
		onRestart: onRestart,
		fyne:      app.NewWithID("com.mti.sip-phone"),
		regState:  "unregistered",
		speakerOn: true, // 扬声器默认开（REQ-UI-023）
		micOn:     true, // 麦克风默认开（REQ-UI-023）
	}
	if desk, ok := ui.fyne.(desktop.App); ok {
		ui.desk = desk
	}
	return ui
}

// Run 启动界面主循环（阻塞直到退出）。
// 必须在主 goroutine 调用：Fyne 的 UI 初始化与事件循环要求主线程。
func (a *App) Run() {
	a.buildMainWindow()
	a.buildTray()
	a.subscribeEvents()
	a.watchWSPortFocus() // WS 端口输入框失焦保存与重启提示
	a.fyne.Run()
}

// Quit 请求退出界面主循环（可从任意 goroutine 调用）。
func (a *App) Quit() {
	a.fyne.Quit()
}

// ---------------------------------------------------------------------------
// 主窗口
// ---------------------------------------------------------------------------

// buildMainWindow 主窗口：内容可在「拨号盘」与「设置页」之间切换。
func (a *App) buildMainWindow() {
	w := a.fyne.NewWindow("mti-sip-phone")
	a.mainW = w
	// 任务栏/窗口图标（默认是控制台图标，这里设为话机）
	w.SetIcon(IconPhoneGreen)
	// 关闭 = 隐藏到托盘，不退出
	w.SetCloseIntercept(func() {
		w.Hide()
		a.mainShown = false
	})

	// 拨号盘定制主题：更大按键、更明显边框
	a.fyne.Settings().SetTheme(&dialTheme{})

	a.buildDialPage()
	a.buildSettingsPage()

	a.mainContent = container.NewStack(a.dialPage)
	w.SetContent(a.mainContent)
	w.Resize(fyne.NewSize(320, 620)) // 更窄的宽度 + 按键增高后的总高
	w.CenterOnScreen()
	// 固定窗口尺寸：切换设置页（内容多）时窗口不被撑大
	w.SetFixedSize(true)

	// 主窗体任意位置按键输入（无需聚焦输入框，REQ-UI-024）：
	// 数字/星号/井号直接输入拨号框，退格删除末尾字符。
	// 输入框获得焦点时按键由其自身消费，此处只处理未被消费的按键。
	w.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if a.uiState != uiIdle || a.dialSelect == nil {
			return
		}
		// 仅在拨号盘页生效（设置页展示时不处理）
		if len(a.mainContent.Objects) == 0 || a.mainContent.Objects[0] != a.dialPage {
			return
		}
		switch {
		case ev.Name == fyne.KeyBackspace:
			runes := []rune(a.dialSelect.Text)
			if len(runes) > 0 {
				a.dialSelect.SetText(string(runes[:len(runes)-1]))
			}
		case ev.Name == fyne.KeyEnter || ev.Name == fyne.KeyReturn:
			// 回车直接呼出（输入框聚焦时由 OnSubmitted 处理，不会重复）
			a.onCallButton()
		case isDialDigit(ev.Name):
			digit := string(ev.Name)
			// 与号码盘一致：呼出过的号码视为已完成输入，重新输入时先清空
			if a.savedNumber != "" && a.dialSelect.Text == a.savedNumber {
				a.dialSelect.SetText(digit)
			} else {
				a.dialSelect.SetText(a.dialSelect.Text + digit)
			}
		}
	})

	// 启动默认隐藏主窗口，仅显示托盘图标（REQ-UI 修订）；
	// 单击托盘图标或菜单「显示主界面」唤出
}

// buildDialPage 拨号盘页：号码输入框 + 3×4 拨号键 + 呼叫按钮（REQ-UI-005）。
func (a *App) buildDialPage() {
	// ---- 顶部栏：状态圆点 + 本机号码 + 设置按钮（右上角）----
	a.regDot = newStatusDot("unregistered")
	a.regLabel = widget.NewLabel("") // 本机号码（已配置时显示）
	a.autoAnswerBtn = widget.NewButton("自动接听", a.toggleAutoAnswer)
	// 未选中时浅灰底（与通话面板开关关闭状态一致，REQ-UI-024 修订）
	a.autoAnswerBtn.Importance = widget.MediumImportance
	statusRow := fyne.NewContainerWithLayout(statusRowLayout{}, a.regDot, a.regLabel)
	a.settingsBtn = widget.NewButtonWithIcon("", theme.SettingsIcon(), a.showSettings)
	a.settingsBtn.Importance = widget.LowImportance

	// 顶栏：圆点+号码靠左，设置按钮靠右，自动接听按钮居中
	topBar := container.NewBorder(nil, nil, statusRow, a.settingsBtn,
		container.NewCenter(a.autoAnswerBtn))

	// ---- 号码输入框：可下拉（最近 5 次呼叫号码，去重）----
	a.dialEntry = newFormEntry()
	a.dialEntry.SetPlaceHolder("输入号码或 sip:xxx@主机")
	a.dialEntry.TextStyle = fyne.TextStyle{Bold: true}
	a.dialSelect = widget.NewSelectEntry(nil)
	a.dialSelect.SetPlaceHolder("输入号码或 sip:xxx@主机")
	a.dialSelect.Wrapping = fyne.TextWrapOff
	a.dialSelect.Scroll = fyne.ScrollNone
	// 回车直接呼出（REQ-UI-024）
	a.dialSelect.OnSubmitted = func(string) {
		a.onCallButton()
	}
	a.refreshRecentDials()

	inputRow := container.NewBorder(nil, nil, nil, nil, a.dialSelect)

	// ---- 3×4 拨号键（空闲输入号码 / 呼出中禁用 / 通话中发送 DTMF）----
	keys := [][]string{
		{"1", "2", "3"},
		{"4", "5", "6"},
		{"7", "8", "9"},
		{"*", "0", "#"},
	}
	a.keypadBtns = nil
	padGrid := container.NewGridWithColumns(3)
	for _, row := range keys {
		for _, k := range row {
			key := k
			btn := widget.NewButton(key, func() {
				switch a.uiState {
				case uiIdle:
					// 呼出过的号码视为已完成输入：再次点号码盘时
					// 清空重新输入（REQ-UI-024）
					if a.savedNumber != "" && a.dialSelect.Text == a.savedNumber {
						a.dialSelect.SetText(key)
					} else {
						a.dialSelect.SetText(a.dialSelect.Text + key)
					}
				case uiInCall:
					// 通话中按键发送 DTMF（REQ-CALL-006）
					if a.activeCallID != "" {
						_ = a.core.SendDTMF(a.activeCallID, key)
					}
				}
			})
			btn.Importance = widget.LowImportance
			a.keypadBtns = append(a.keypadBtns, btn)
			padGrid.Add(btn)
		}
	}

	// ---- 呼叫按钮：绿色「呼叫」（黑色图标与文字一致，REQ-UI-024 修订），
	// 宽度拉满居中（Border 底部对象被拉伸全宽）----
	// 呼叫按钮用 compactTextButton：文字颜色固定（浅色主题下 fyne
	// Success 按钮文字为白色、看不清），黑色文字与黑色图标一致
	//（REQ-UI-024 修订）
	a.callBtn = newCompactTextButton("呼叫", IconPlayBlack, colorSuccessGreen, colorIconBlack, a.onCallButton)

	a.dialPage = container.NewBorder(
		topBar,
		a.callBtn,
		nil, nil,
		container.NewVBox(
			inputRow,
			padGrid,
		),
	)
}

// buildSettingsPage 设置页：账户、音频设备、日志、网页接入 token（REQ-UI-006/012）。
func (a *App) buildSettingsPage() {
	// ---- 顶部栏：返回按钮 ----
	backBtn := widget.NewButtonWithIcon("返回", theme.NavigateBackIcon(), a.showDialPage)
	backBtn.Importance = widget.LowImportance

	// ---- SIP 账户 ----
	a.accEnabled = widget.NewCheck("启用账户", nil)
	a.accServerAddr = newFormEntry()
	a.accServerAddr.SetPlaceHolder("SIP 服务器，如 10.168.4.40:15060（端口可省略，默认 5060）")
	a.accExpires = newFormEntry()
	a.accExpires.SetPlaceHolder("300")
	a.accTransport = widget.NewSelect([]string{"udp", "tcp"}, nil)
	a.accAccount = newFormEntry()
	a.accAccount.SetPlaceHolder("账号")
	a.accAuthUser = newFormEntry()
	a.accAuthUser.SetPlaceHolder("认证 ID（留空同账号）")
	a.accPassword = newFormEntry()
	a.accPassword.SetPlaceHolder("密码")
	a.accPassword.Password = true
	a.accDisplay = newFormEntry()
	a.accDisplay.SetPlaceHolder("显示名（可选）")
	saveBtn := widget.NewButtonWithIcon("保存并注册", theme.ConfirmIcon(), a.saveAccount)
	a.accProxy = newFormEntry()
	a.accProxy.SetPlaceHolder("出站代理（可选），如 10.168.4.40:5060")
	// rport 开关（默认开启，REQ-ACC-016）
	a.rportCheck = widget.NewCheck("启用 rport（NAT 穿透）", func(enabled bool) {
		if a.loadingForm {
			return
		}
		// 即时生效（无需重启，REQ-ACC-016）；配置持久化由「保存并注册」按钮统一完成
		if err := a.core.SetRPort(enabled); err != nil {
			logx.Logger().Warn("切换 rport 失败", "err", err)
		}
	})

	// 表单项采用「label 在上、输入框在下」的堆叠布局（与 token 一致），
	// 避免 Grid 双列在窄窗口下把主窗口撑宽（REQ-UI-019）
	// STUN（REQ-ACC-017）：默认 stun.l.google.com:19302，可修改；
	// 启用开关与地址输入框联动——勾选后才能编辑地址
	a.accStunCheck = widget.NewCheck("启用 STUN（NAT 公网地址探测）", func(enabled bool) {
		if a.loadingForm {
			return
		}
		if enabled {
			a.accStunServer.Enable()
		} else {
			a.accStunServer.Disable()
		}
	})
	a.accStunServer = newFormEntry()
	a.accStunServer.SetPlaceHolder("stun.l.google.com:19302")

	accCard := container.NewVBox(
		widget.NewLabelWithStyle("SIP 账户", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		a.accEnabled,
		formItem("SIP服务器", a.accServerAddr),
		formItem("代理服务器", a.accProxy),
		formItem("传输", a.accTransport),
		formItem("账号", a.accAccount),
		formItem("认证 ID", a.accAuthUser),
		formItem("密码", a.accPassword),
		formItem("显示名", a.accDisplay),
		formItem("注册到期时间（秒）", a.accExpires),
		a.rportCheck,
		a.accStunCheck,
		formItem("STUN 服务器", a.accStunServer),
		saveBtn,
	)

	// ---- 音频设备 ----
	a.micSelect = widget.NewSelect([]string{}, nil)
	a.speakerSelect = widget.NewSelect([]string{}, nil)
	a.ringSpeakerSelect = widget.NewSelect([]string{}, nil)
	refreshDevicesBtn := widget.NewButtonWithIcon("刷新设备", theme.ViewRefreshIcon(), a.refreshDevices)

	audioCard := container.NewVBox(
		widget.NewLabelWithStyle("音频设备", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		formItem("麦克风", a.micSelect),
		formItem("扬声器", a.speakerSelect),
		formItem("振铃设备", a.ringSpeakerSelect),
		widget.NewLabel("振铃音专用播放设备，可选其他扬声器"),
		refreshDevicesBtn,
	)

	// ---- 日志配置（REQ-LOG-006）----
	a.logFileCheck = widget.NewCheck("记录日志到文件", nil)
	a.sipTraceCheck = widget.NewCheck("记录 SIP 信令", a.saveSIPTrace)
	a.logLevelSel = widget.NewSelect([]string{"debug", "info", "warn", "error"}, nil)
	logDirBtn := widget.NewButtonWithIcon("选择目录...", theme.FolderOpenIcon(), a.chooseLogDir)
	a.logDirLabel = widget.NewLabel("")
	logSaveBtn := widget.NewButtonWithIcon("保存日志配置", theme.ConfirmIcon(), a.saveLogConfig)

	logCard := container.NewVBox(
		widget.NewLabelWithStyle("日志", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		a.logFileCheck,
		a.sipTraceCheck,
		formItem("日志级别", a.logLevelSel),
		formItem("日志目录", a.logDirLabel),
		logDirBtn,
		logSaveBtn,
	)

	// ---- 常规 ----
	// 开机自动启动（REQ-UI-022）：默认启用，切换立即保存并注册/移除系统自启项
	a.autoStartCheck = widget.NewCheck("开机自动启动", a.saveAutoStart)

	generalCard := container.NewVBox(
		widget.NewLabelWithStyle("常规", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		a.autoStartCheck,
	)

	// ---- 网页接入（WS 服务端口，无 token 鉴权）----
	a.wsPortEntry = newFormEntry()
	a.wsPortEntry.SetText(strconv.Itoa(a.cfg.WS.Port))
	wsHint := widget.NewLabel("本地网页控制服务端口（默认 8765，占用时自动 +1；修改后需重启生效）")
	wsHint.Wrapping = fyne.TextWrapWord

	wsCard := container.NewVBox(
		widget.NewLabelWithStyle("网页接入（WS 端口）", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		a.wsPortEntry,
		wsHint,
	)

	// 滚动容器容纳全部设置项；右侧留 16px 空白避免滚动条遮住输入框
	// 「常规」（开机自动启动）置于最上方
	scrollContent := container.NewVBox(
		generalCard, widget.NewSeparator(),
		accCard, widget.NewSeparator(),
		audioCard, widget.NewSeparator(),
		logCard, widget.NewSeparator(),
		wsCard,
	)
	// 右侧 14px 精确空隙（滚动条悬浮区），与左侧 padding 对称
	rightGap := canvas.NewRectangle(color.Transparent)
	rightGap.SetMinSize(fyne.NewSize(14, 1))
	scrollContentWithGap := container.NewBorder(nil, nil, nil, rightGap, scrollContent)
	scroll := container.NewVScroll(scrollContentWithGap)
	a.settingsScroll = scroll

	a.settingsPage = container.NewBorder(
		container.NewHBox(backBtn),
		nil, nil, nil,
		scroll,
	)

	// 回填当前配置
	a.loadAccountIntoForm()
	a.loadLogConfigIntoForm()
	a.refreshDevices()
}

// newFormEntry 表单输入框：关闭自身滚动（滚轮事件传递给外层 VScroll）。
func newFormEntry() *widget.Entry {
	e := widget.NewEntry()
	e.Scroll = fyne.ScrollNone
	return e
}

// parseHostPort 解析 host[:port]，端口省略时用默认值。
func parseHostPort(addr string, defaultPort int) (string, int) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", defaultPort
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// 无端口或格式为纯 host
		return addr, defaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port >= 65536 {
		port = defaultPort
	}
	return host, port
}

// formItem 表单项：label 在上、控件在下的堆叠布局（防止窄窗口被撑宽）。
// 控件包一层 HScroll：长内容（如 token、长地址）横向滚动而非撑宽窗口。
func formItem(label string, obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVBox(
		widget.NewLabel(label),
		container.NewHScroll(obj),
	)
}

// showSettings 切换到设置页（仅非通话状态可用，REQ-UI-012）。
func (a *App) showSettings() {
	if a.activeCallID != "" {
		a.showInfo("提示", "通话中无法进入设置")
		return
	}
	// 每次进入时刷新 WS 端口显示（端口可能在启动时被自动 +1 调整）
	if a.wsPortEntry != nil {
		a.wsPortEntry.SetText(strconv.Itoa(a.cfg.WS.Port))
	}
	a.mainContent.Objects[0] = a.settingsPage
	a.mainContent.Refresh()
	// 每次进入配置页回到顶部
	if a.settingsScroll != nil {
		a.settingsScroll.ScrollToTop()
	}
}

// watchWSPortFocus WS 端口输入框失焦检测（150ms 轮询画布焦点）：
// 失焦时校验端口 → 保存配置 → 提示重启（修改后需重启生效）。
func (a *App) watchWSPortFocus() {
	go func() {
		wasFocused := false
		for {
			time.Sleep(150 * time.Millisecond)
			if a.wsPortEntry == nil || a.mainW == nil {
				continue
			}
			focused := a.mainW.Canvas().Focused() == a.wsPortEntry
			if wasFocused && !focused {
				a.applyWSPort()
			}
			wasFocused = focused
		}
	}()
}

// applyWSPort 校验并保存 WS 端口修改，然后提示重启。
func (a *App) applyWSPort() {
	text := strings.TrimSpace(a.wsPortEntry.Text)
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		a.wsPortEntry.SetText(strconv.Itoa(a.cfg.WS.Port))
		return
	}
	if port == a.cfg.WS.Port {
		return
	}
	a.cfg.WS.Port = port
	a.persistConfig()
	logx.Logger().Info("WS 端口已修改", "port", port)
	a.showConfirm("重启", fmt.Sprintf("WS 端口已修改为 %d，重启后生效。是否立即重启？", port), func(ok bool) {
		if ok && a.onRestart != nil {
			a.onRestart()
		}
	})
}

// showDialPage 返回拨号盘页。
func (a *App) showDialPage() {
	a.mainContent.Objects[0] = a.dialPage
	a.mainContent.Refresh()
}

// onCallButton 呼叫按钮：无通话时拨号，呼叫/通话中挂断（REQ-UI-008）。
func (a *App) onCallButton() {
	if a.activeCallID != "" {
		_ = a.core.Hangup(a.activeCallID)
		return
	}
	target := a.dialSelect.Text
	if target == "" {
		a.showInfo("提示", "请输入号码")
		return
	}
	callID, err := a.core.Dial(context.Background(), target)
	if err != nil {
		a.showError(err)
		return
	}
	// 安卓式体验：拨号发起后主窗口立即进入呼叫状态（不弹独立窗口）
	a.activeCallID = callID
	a.activeCall = &core.Call{
		ID:        callID,
		Direction: "outgoing",
		Remote:    target,
		State:     core.CallRinging,
	}
	a.savedNumber = target
	a.setCallUI(uiDialing, target)
	a.refreshRecentDials()
}

// setCallUI 通话状态机（用户重新设计，REQ-UI-008 修订）：
//   - uiIdle：通话面板隐藏；主窗口仅当通话前可见时恢复显示（通话前
//     已隐藏到托盘则保持隐藏——按用户反馈修订）
//   - uiDialing：主窗口隐藏，通话面板置顶显示"呼叫中 + 号码 + 时长 + 红色挂断"
//   - uiRinging：主窗口隐藏，通话面板置顶显示"来电号码 + 振铃时长 + 绿色接听 + 红色挂断"
//   - uiInCall：主窗口隐藏，通话面板置顶显示"号码 + 通话时长 + 黄色 || + 红色挂断"
func (a *App) setCallUI(state int, remote string) {
	// 通话面板显示前记录主窗口是否显示（仅从空闲进入通话状态时
	// 记录一次——状态间切换或重复事件不会覆盖记录）
	if state != uiIdle && a.uiState == uiIdle {
		a.mainWasVisible = a.isMainWindowVisible()
	}
	a.uiState = state
	switch state {
	case uiIdle:
		a.stopTimer()
		if a.callW != nil {
			a.callW.Hide()
		}
		a.dialSelect.Enable()
		if a.savedNumber != "" {
			a.dialSelect.SetText(a.savedNumber)
		}
		a.setKeypadEnabled(true)
		a.updateCallButton()
		// 通话结束后不主动显示主窗体：仅当主窗体是被本次通话自动
		// 隐藏（通话前可见）时恢复显示
		if a.mainWasVisible {
			a.mainW.Show()
			a.mainShown = true
			a.mainW.RequestFocus()
			a.bringMainToFront() // 恢复显示时带到前台
		}

	case uiDialing:
		a.mainW.Hide()
		a.mainShown = false
		a.buildCallPanelIfNeeded()
		a.updateCallWindowTitle()
		a.callNameLabel.SetText(remote)
		a.callStateLabel.SetText("呼叫中")
		a.rebuildCallRows(dialingButtons)
		a.callW.Show()
		moveCallWindowToCorner(a.callW)
		a.startTimer()

	case uiRinging:
		a.mainW.Hide()
		a.mainShown = false
		a.buildCallPanelIfNeeded()
		a.updateCallWindowTitle()
		a.callNameLabel.SetText(remote)
		a.callStateLabel.SetText("来电")
		a.rebuildCallRows(ringingButtons)
		a.callW.Show()
		a.callW.RequestFocus()
		moveCallWindowToCorner(a.callW)
		a.startTimer() // 振铃时长

	case uiInCall:
		a.mainW.Hide()
		a.mainShown = false
		a.buildCallPanelIfNeeded()
		a.updateCallWindowTitle()
		a.callNameLabel.SetText(remote)
		a.callStateLabel.SetText("通话中")
		a.rebuildCallRows(incallButtons)
		a.callW.Show()
		moveCallWindowToCorner(a.callW)
		a.startTimer() // 通话时长从 0 开始
	}
}

// refreshRecentDials 刷新拨号下拉的最近呼叫号码（去重后最多 5 个）。
func (a *App) refreshRecentDials() {
	if a.dialSelect == nil {
		return
	}
	recent := a.core.RecentDials()
	a.dialSelect.SetOptions(recent)
}

// setKeypadEnabled 启用/禁用拨号按键。
func (a *App) setKeypadEnabled(enabled bool) {
	for _, btn := range a.keypadBtns {
		if enabled {
			btn.Enable()
		} else {
			btn.Disable()
		}
	}
}

// updateCallButton 按通话状态更新呼叫按钮外观与行为：
// 呼叫态=绿底黑字黑图标；挂断态=红底白字白图标
//（文字颜色固定，不随主题变化，REQ-UI-024 修订）。
func (a *App) updateCallButton() {
	if a.activeCallID != "" {
		a.callBtn.SetText("挂断")
		a.callBtn.SetIcon(IconStopWhite)
		a.callBtn.SetBg(colorDangerRed)
		a.callBtn.SetLabelColor(colorIconWhite)
	} else {
		a.callBtn.SetText("呼叫")
		a.callBtn.SetIcon(IconPlayBlack)
		a.callBtn.SetBg(colorSuccessGreen)
		a.callBtn.SetLabelColor(colorIconBlack)
	}
	a.callBtn.Refresh()
}

// ---------------------------------------------------------------------------
// 系统托盘
// ---------------------------------------------------------------------------

// buildTray 系统托盘：话机图标（注册状态红/绿）+ 菜单（显示主界面 / 退出）。
//
// 注意调用顺序（Fyne driver 实现约束）：
//   - SetSystemTrayMenu / SetSystemTrayWindow 会触发 systray 启动，其就绪
//     回调中自动将 fyne.App 的图标设为托盘图标；
//   - SetSystemTrayIcon 直接操作底层 systray，在 systray 启动前调用会报
//     "tray not ready yet" 且托盘不显示；启动后的状态切换（注册红/绿）
//     通过 SetSystemTrayIcon 完成（此时 systray 已就绪）。
func (a *App) buildTray() {
	if a.desk == nil {
		logx.Logger().Warn("当前驱动不支持系统托盘")
		return
	}
	showMain := fyne.NewMenuItem("显示主界面", func() {
		a.showMain()
	})
	quit := fyne.NewMenuItem("退出", func() {
		a.fyne.Quit()
	})
	menu := fyne.NewMenu("mti-sip-phone", showMain, fyne.NewMenuItemSeparator(), quit)

	// 初始图标按当前注册状态（默认红色，systray 就绪回调自动设置）
	a.fyne.SetIcon(a.trayIcon())
	// 单击托盘图标唤出主窗口（Fyne 托盘无原生双击事件，单击唤出）
	a.desk.SetSystemTrayMenu(menu)
	a.desk.SetSystemTrayWindow(a.mainW)
	// SetSystemTrayWindow 内部会覆盖窗口的 CloseIntercept（w.Hide）并
	// 注册单击回调（直接 Show）——两处均不经过应用代码，导致自维护的
	// mainShown 显示标志失去同步（点 X 后标志仍为 true，通话结束误判
	// "通话前可见"而拉起主窗体——bug 修复）。此处重新接管：
	a.mainW.SetCloseIntercept(func() {
		a.mainW.Hide()
		a.mainShown = false
	})
	systray.SetOnTapped(func() {
		fyne.Do(func() {
			a.showMain()
		})
	})
	logx.Logger().Info("系统托盘已就绪（单击图标唤出主界面）")
}

// trayIcon 按注册状态返回托盘图标：未注册红、已注册绿（REQ-UI-011）。
func (a *App) trayIcon() fyne.Resource {
	if a.regState == "registered" {
		return IconPhoneGreen
	}
	return IconPhoneRed
}

// updateWindowTitle 标题栏与托盘悬停提示："mti-sip-phone - 本机号码"。
func (a *App) updateWindowTitle() {
	account := a.core.State().Register.Account
	title := "mti-sip-phone"
	if account != "" {
		title += " - " + account
	}
	a.mainW.SetTitle(title)
	// 托盘悬停提示（systray 需已就绪；未就绪时由后续事件刷新）
	systray.SetTooltip(title)
}

// updateTrayIcon 注册状态变化时更新托盘图标颜色。
func (a *App) updateTrayIcon() {
	if a.desk == nil {
		return
	}
	a.desk.SetSystemTrayIcon(a.trayIcon())
}

// ---------------------------------------------------------------------------
// 事件订阅
// ---------------------------------------------------------------------------

// subscribeEvents 订阅核心事件，驱动界面状态。
func (a *App) subscribeEvents() {
	// 初始状态直接从核心快照读取（REQ-UI-015）：注册可能在 UI 订阅
	// 事件之前已完成（启动即注册），依赖事件流会导致界面状态不准确。
	// regLabel 显示本机号码（不是状态文本）；状态由圆点颜色表示。
	st := a.core.State()
	a.regState = string(st.Register.State)
	if a.regDot != nil {
		a.regDot.setState(a.regState)
	}
	if a.regLabel != nil {
		a.regLabel.SetText(st.Register.Account)
	}
	a.updateTrayIcon()
	a.updateWindowTitle()

	sub := a.core.Bus().Subscribe()
	go func() {
		for ev := range sub {
			fyne.Do(func() { a.onEvent(ev) })
		}
	}()
}

// onEvent 处理核心事件（在 Fyne 主线程执行）。
func (a *App) onEvent(ev core.Event) {
	switch ev.Name {
	case "register.state":
		if data, ok := ev.Data.(map[string]any); ok {
			if state, ok := data["state"].(string); ok {
				a.regState = state
				a.regDot.setState(state)
				if account, ok := data["account"].(string); ok {
					a.regLabel.SetText(account) // 本机号码
				}
				a.updateTrayIcon()
				a.updateWindowTitle()
			}
		}

	case "call.incoming":
		// 呼入振铃：主窗口隐藏，通话面板显示来电状态，循环播放振铃音
		if callID, call := callData(ev.Data); call != nil {
			a.activeCallID = callID
			a.activeCall = call
			a.savedNumber = call.Remote
			a.setCallUI(uiRinging, callRemoteName(call))
			if a.audioMgr != nil {
				a.audioMgr.PlayRing()
			}
		}

	case "call.dialing":
		// 呼出中：主窗口隐藏，通话面板显示"呼叫中"。
		// 本地拨号按钮与 WS 拨号都走此事件（WS 拨号此前不触发任何
		// 界面切换，呼叫窗口不显示——bug 修复）。
		// 本地拨号时 onCallButton 已同步切换过界面，此处跳过重复切换
		//（重复切换会用已隐藏的窗口覆盖"通话前可见"记录，导致通话
		// 结束后主窗口不恢复——bug 修复）。
		if callID, call := callData(ev.Data); call != nil {
			a.activeCallID = callID
			a.activeCall = call
			a.savedNumber = call.Remote
			if a.uiState != uiDialing {
				a.setCallUI(uiDialing, callRemoteName(call))
			}
		}

	case "call.ringing":
		// 呼出振铃：通话面板保持"呼叫中"（对方信息可能更新）。
		// 回铃音由 SIP 服务器以 183 早期媒体下发，媒体层直接接收播放。
		// 183 附 SDP 时媒体引擎已创建：把会话内记忆的扬声器/麦克风
		// 状态下发（呼叫阶段关闭扬声器即可屏蔽回铃音，REQ-UI-023）
		if callID, call := callData(ev.Data); call != nil && callID == a.activeCallID {
			a.activeCall = call
			a.applySessionAudioState()
		}

	case "call.established":
		if callID, call := callData(ev.Data); call != nil {
			a.activeCallID = callID
			a.activeCall = call
			a.savedNumber = call.Remote
			a.setCallUI(uiInCall, callRemoteName(call))
			// 通话建立：停止振铃音
			if a.audioMgr != nil {
				a.audioMgr.StopRing()
			}
		}

	case "call.terminated", "call.failed":
		// 停止振铃音并播放挂断提示音
		if a.audioMgr != nil {
			a.audioMgr.StopRing()
			a.audioMgr.PlayHangup()
		}
		if ev.Name == "call.failed" {
			reason := "未知原因"
			if m, ok := ev.Data.(map[string]any); ok {
				if r, ok := m["reason"].(string); ok {
					reason = r
				}
			}
			a.showCallFailed(reason)
			return
		}
		a.restoreIdle()

	case "call.muted":
		if data, ok := ev.Data.(map[string]any); ok {
			if muted, ok := data["muted"].(bool); ok {
				a.setHoldButton(muted)
			}
		}
	}
}

// regText 注册状态中文文案。
func regText(state string) string {
	switch state {
	case "registered":
		return "已注册"
	case "registering":
		return "注册中..."
	case "failed":
		return "注册失败"
	default:
		return "未注册"
	}
}

// toggleAutoAnswer 自动接听开关：开启后来电自动接听。
// 选中 = 蓝底；未选中 = 浅灰底（REQ-UI-024 修订）。
func (a *App) toggleAutoAnswer() {
	a.autoAnswer = !a.autoAnswer
	if a.autoAnswer {
		a.autoAnswerBtn.Importance = widget.HighImportance
	} else {
		a.autoAnswerBtn.Importance = widget.MediumImportance
	}
	a.autoAnswerBtn.Refresh()
	if err := a.core.SetAutoAnswer(a.autoAnswer); err != nil {
		logx.Logger().Warn("设置自动接听失败", "err", err)
	}
	logx.Logger().Info("自动接听开关", "enabled", a.autoAnswer)
}

// showMain 显示主窗口（托盘单击/菜单唤出）。
// 通话期间（呼叫/振铃/通话中）主窗口保持隐藏，通话面板关闭后
// 才可唤出（REQ-UI-025 修订）。
func (a *App) showMain() {
	if a.uiState != uiIdle {
		return
	}
	a.mainW.Show()
	a.mainShown = true
	a.mainW.RequestFocus()
	a.bringMainToFront() // 显示时带到前台（修复被其他窗体遮盖）
}

// isMainWindowVisible 主窗口显示标识（全局单一事实来源）：
//   - 程序启动：false（启动默认隐藏，仅托盘图标）
//   - 托盘单击 / 菜单"显示主界面"：true
//   - 通话面板关闭后拉起主窗口：true
//   - 点 X 关闭（隐藏到托盘）：false
// 通话面板显示前记录该标识，关闭后按标识决定是否拉起主窗口。
// 托盘单击与 X 关闭回调均已由应用接管（见 buildTray），标识可靠。
func (a *App) isMainWindowVisible() bool {
	return a.mainShown
}

// bringMainToFront 把主窗口带到前台（Windows 前台锁可能拒绝，
// 失败时仅任务栏闪烁——平台级限制）。
func (a *App) bringMainToFront() {
	if hwnd := findWindowByTitlePrefix("mti-sip-phone"); hwnd != 0 {
		forceForegroundWindow(hwnd)
	}
}

// ---------------------------------------------------------------------------
// 设置页操作
// ---------------------------------------------------------------------------

// saveAccount 保存账户配置并触发重新注册（REQ-ACC-011）。
func (a *App) saveAccount() {
	// 服务器与端口合并解析：host[:port]，端口省略默认 5060
	host, port := parseHostPort(a.accServerAddr.Text, 5060)
	expires := 300
	if v := a.accExpires.Text; v != "" {
		fmt.Sscanf(v, "%d", &expires)
	}
	if expires <= 0 {
		expires = 300
	}
	acc := config.Account{
		Enabled:     a.accEnabled.Checked,
		Server:      host,
		Port:        port,
		Expires:     expires,
		RPort:       a.rportCheck.Checked,
		STUNEnabled: a.accStunCheck.Checked,
		STUNServer:  a.accStunServer.Text,
		Proxy:       a.accProxy.Text,
		Transport:   a.accTransport.Selected,
		Account:     a.accAccount.Text,
		AuthUser:    a.accAuthUser.Text,
		Password:    a.accPassword.Text,
		DisplayName: a.accDisplay.Text,
	}
	if acc.Enabled && (acc.Server == "" || acc.Account == "") {
		a.showInfo("提示", "启用账户需填写服务器与账号")
		return
	}
	if err := a.core.UpdateAccount(acc); err != nil {
		a.showError(err)
		return
	}
	a.showInfo("已保存", "账户配置已保存，正在注册...")
}

// loadAccountIntoForm 回填账户表单（loadingForm 屏蔽自动保存回调）。
func (a *App) loadAccountIntoForm() {
	a.loadingForm = true
	defer func() { a.loadingForm = false }()
	acc := a.cfg.Account
	a.accEnabled.SetChecked(acc.Enabled)
	a.rportCheck.SetChecked(acc.RPort)
	a.accServerAddr.SetText(fmt.Sprintf("%s:%d", acc.Server, acc.Port))
	a.accExpires.SetText(fmt.Sprintf("%d", acc.Expires))
	a.accProxy.SetText(acc.Proxy)
	a.accStunCheck.SetChecked(acc.STUNEnabled)
	if acc.STUNServer == "" {
		acc.STUNServer = "stun.l.google.com:19302"
	}
	a.accStunServer.SetText(acc.STUNServer)
	// 联动：未启用 STUN 时地址输入框禁用
	if acc.STUNEnabled {
		a.accStunServer.Enable()
	} else {
		a.accStunServer.Disable()
	}
	a.accTransport.SetSelected(acc.Transport)
	a.accAccount.SetText(acc.Account)
	a.accAuthUser.SetText(acc.AuthUser)
	a.accPassword.SetText(acc.Password)
	a.accDisplay.SetText(acc.DisplayName)
}

// saveSIPTrace SIP 信令记录开关：保存配置并即时生效。
func (a *App) saveSIPTrace(enabled bool) {
	if a.loadingForm {
		return
	}
	a.cfg.Log.SIPTrace = enabled
	a.persistConfig()
	sip.SetTraceEnabled(enabled)
}

// saveAutoStart 开机自动启动开关：保存配置并立即注册/移除系统自启项。
func (a *App) saveAutoStart(enabled bool) {
	if a.loadingForm {
		return
	}
	a.cfg.AutoStart = enabled
	a.persistConfig()
	exe, err := os.Executable()
	if err != nil {
		logx.Logger().Warn("获取可执行文件路径失败，自动启动设置未应用", "err", err)
		return
	}
	if enabled {
		if err := autostart.Enable(exe); err != nil {
			logx.Logger().Warn("注册开机自启失败", "err", err)
			a.showError(fmt.Errorf("注册开机自启失败: %v", err))
			return
		}
		logx.Logger().Info("开机自启已启用")
	} else {
		if err := autostart.Disable(); err != nil {
			logx.Logger().Warn("移除开机自启失败", "err", err)
			a.showError(fmt.Errorf("移除开机自启失败: %v", err))
			return
		}
		logx.Logger().Info("开机自启已禁用")
	}
}

// saveLogConfig 保存日志配置并即时应用（REQ-LOG-006）。
func (a *App) saveLogConfig() {
	a.cfg.Log.File = a.logFileCheck.Checked
	a.cfg.Log.Level = a.logLevelSel.Selected
	a.persistConfig()
	// 级别即时生效；文件开关与目录重启后生效
	logx.SetLevel(a.cfg.Log.Level)
	a.showInfo("已保存", "日志配置已保存。级别已即时生效；\n文件开关与目录将在重启后生效。")
}

// loadLogConfigIntoForm 回填日志配置表单。
func (a *App) loadLogConfigIntoForm() {
	a.loadingForm = true
	defer func() { a.loadingForm = false }()
	a.logFileCheck.SetChecked(a.cfg.Log.File)
	a.sipTraceCheck.SetChecked(a.cfg.Log.SIPTrace)
	a.logLevelSel.SetSelected(a.cfg.Log.Level)
	a.autoStartCheck.SetChecked(a.cfg.AutoStart)
	if a.cfg.Log.Dir == "" {
		a.logDirLabel.SetText("默认（配置目录/logs）")
	} else {
		a.logDirLabel.SetText(a.cfg.Log.Dir)
	}
}

// chooseLogDir 选择日志目录。
func (a *App) chooseLogDir() {
	dlg := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil || uri == nil {
			return
		}
		a.cfg.Log.Dir = uri.Path()
		a.logDirLabel.SetText(a.cfg.Log.Dir)
	}, a.mainW)
	dlg.Show()
}

// refreshDevices 刷新音频设备列表（枚举真实系统设备，REQ-MEDIA-002）。
func (a *App) refreshDevices() {
	if a.audioMgr == nil {
		a.micSelect.Options = []string{"音频后端不可用"}
		a.speakerSelect.Options = []string{"音频后端不可用"}
		return
	}
	mics, speakers := a.audioMgr.Devices()
	micNames := make([]string, 0, len(mics))
	micIDs := make(map[string]string)
	for _, d := range mics {
		micNames = append(micNames, d.Name)
		micIDs[d.Name] = d.ID
	}
	spkNames := make([]string, 0, len(speakers))
	spkIDs := make(map[string]string)
	for _, d := range speakers {
		spkNames = append(spkNames, d.Name)
		spkIDs[d.Name] = d.ID
	}
	a.micSelect.Options = micNames
	a.speakerSelect.Options = spkNames
	a.ringSpeakerSelect.Options = spkNames // 振铃设备与扬声器同源枚举

	// 振铃设备：立即生效并保存
	a.ringSpeakerSelect.OnChanged = func(name string) {
		if a.loadingForm {
			return
		}
		if id, ok := spkIDs[name]; ok {
			if a.audioMgr != nil {
				a.audioMgr.SetRingSpeaker(id)
			}
			a.cfg.Audio.RingSpeakerID = id
			a.persistConfig()
		}
	}

	// 用户选择：立即生效并自动保存（REQ-UI-016）
	a.micSelect.OnChanged = func(name string) {
		if a.loadingForm {
			return // 回填阶段不触发保存
		}
		if id, ok := micIDs[name]; ok {
			_ = a.audioMgr.SetMic(id)
			a.cfg.Audio.MicID = id
			a.persistConfig()
		}
	}
	a.speakerSelect.OnChanged = func(name string) {
		if a.loadingForm {
			return
		}
		if id, ok := spkIDs[name]; ok {
			_ = a.audioMgr.SetSpeaker(id)
			a.cfg.Audio.SpeakerID = id
			a.persistConfig()
		}
	}

	// 按已保存的配置回选（回填阶段屏蔽保存回调，避免默认值覆盖已保存配置）
	a.loadingForm = true
	defer func() { a.loadingForm = false }()
	if len(micNames) > 0 {
		a.micSelect.SetSelected(micNames[0])
	}
	if len(spkNames) > 0 {
		a.speakerSelect.SetSelected(spkNames[0])
	}
	for name, id := range micIDs {
		if id == a.cfg.Audio.MicID {
			a.micSelect.SetSelected(name)
		}
	}
	for name, id := range spkIDs {
		if id == a.cfg.Audio.SpeakerID {
			a.speakerSelect.SetSelected(name)
		}
	}
	// 振铃设备回选（默认扬声器兜底）
	if len(spkNames) > 0 {
		a.ringSpeakerSelect.SetSelected(spkNames[0])
	}
	for name, id := range spkIDs {
		if id == a.cfg.Audio.RingSpeakerID {
			a.ringSpeakerSelect.SetSelected(name)
		}
	}
}

// persistConfig 保存当前配置到磁盘。
func (a *App) persistConfig() {
	if a.cfgPath != "" {
		if err := a.cfg.Save(a.cfgPath); err != nil {
			logx.Logger().Warn("保存配置失败", "err", err)
		}
	}
}

// callData 从事件负载中提取通话信息。
func callData(data any) (callID string, call *core.Call) {
	m, ok := data.(map[string]any)
	if !ok {
		return "", nil
	}
	if id, ok := m["callId"].(string); ok {
		callID = id
	}
	if c, ok := m["call"].(core.Call); ok {
		call = &c
	}
	return callID, call
}

// isDialDigit 判断按键是否为可拨号的数字键（0-9 / * / #，REQ-UI-024）。
func isDialDigit(name fyne.KeyName) bool {
	if len(name) != 1 {
		return false
	}
	c := name[0]
	return (c >= '0' && c <= '9') || c == '*' || c == '#'
}
