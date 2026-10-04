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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"fyne.io/systray"

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
	mainContent  *fyne.Container
	dialPage     *fyne.Container
	settingsPage *fyne.Container

	// 拨号盘控件
	dialEntry  *widget.Entry // 已废弃：由 dialSelect 替代（保留字段避免大改）
	dialSelect *widget.SelectEntry

	// 按键音出口（测试可替换；nil = 走音频管理器）
	onKeyTone func(key string)

	regDot        *statusDot
	regLabel      *plainText // 注册状态文字（颜色跟圆点一致；本机号码见窗口标题）
	autoAnswerBtn *widget.Button
	callBtn       *compactButton
	settingsBtn   *widget.Button
	contactsBtn   *widget.Button // 通信录（REQ-UI-032，位于通话记录按钮右侧）
	historyBtn    *widget.Button // 通话记录（REQ-UI-031，位于设置按钮左侧）

	// ---- 通话记录（REQ-CALL-007，与配置页一样切在主窗体内）----
	historyPage *fyne.Container // 记录页内容（buildHistoryPage 构建）
	history     *historyState   // 列表状态（见 ui_history.go）

	// ---- 通信录（REQ-UI-032，同样切在主窗体内）----
	contactsPage *fyne.Container // 通信录页内容（buildContactsPage 构建）
	contacts     *contactsState  // 页面状态（见 ui_contacts.go）
	mainTip      *tooltipLayer   // 备注悬停提示图层（主窗体内容顶层，见 ui_tooltip.go）
	callTip      *tooltipLayer   // 通话面板的提示图层

	// 设置页控件
	settingsScroll    *container.Scroll
	accServerAddr     *widget.Entry // SIP 服务器（含端口，如 10.168.4.40:15060）
	accExpires        *widget.Entry // 注册到期时间（秒）
	accProxy          *widget.Entry // 出站代理（可选）
	accStunCheck      *widget.Check
	accStunServer     *widget.Entry
	rportCheck        *widget.Check
	chkOptCheck       *widget.Check // 服务端心跳检测（User-Agent 带 optchk，REQ-ACC-020）
	accTransport      *widget.Select
	accAccount        *widget.Entry
	accAuthUser       *widget.Entry
	accPassword       *widget.Entry
	accDisplay        *widget.Entry
	accEnabled        *widget.Check
	micSelect         *widget.Select
	speakerSelect     *widget.Select
	ringSpeakerSelect *widget.Select
	logFileCheck      *widget.Check
	sipTraceCheck     *widget.Check
	autoStartCheck    *widget.Check // 开机自动启动（REQ-UI-022）
	logLevelSel       *widget.Select
	logDirLabel       *widget.Label
	wsPortEntry       *widget.Entry // 本地 WS 监听端口（默认 8765，修改后重启生效）

	// 通话面板控件（呼叫/振铃/通话三态共用）
	callName       *callNameBox // 对端号码/名称（含 ★ 与备注悬停，REQ-UI-037）
	callStateLabel *plainText   // 自绘单行文本（widget.Label 在本主题下带 12px 内边距，会与名称行错位）
	callTimerLabel *plainText
	callRows       *fyne.Container // 通话面板三行内容容器（按状态重建）
	hangupBtn      *compactButton
	answerBtn      *compactButton
	holdBtn        *compactButton
	speakerBtn     *compactButton    // 扬声器开关（REQ-UI-023）
	micBtn         *compactButton    // 麦克风开关（REQ-UI-023）
	pulseStop      chan struct{}     // 振铃脉动动画停止通道
	ringFlashRect  *canvas.Rectangle // 振铃边框闪烁描边（无边框窗口用，REQ-UI-025 修订）

	// 主窗口呼叫状态控件
	keypadBtns []*widget.Button // 3×4 拨号键（空闲输入号码 / 通话中发 DTMF）

	// 通话状态
	activeCallID string
	activeCall   *core.Call
	uiState      int // uiIdle / uiDialing / uiRinging / uiInCall
	savedNumber  string
	ringHasVideo bool // 本次来电是否含视频（决定是否显示「视频接听」，REQ-VIDEO-004）
	callStartAt  time.Time
	timerStop    chan struct{}
	regState     string
	// trayRegistered 托盘图标当前是否为"已注册"绿色。续注册（registering）
	// 期间不改变它——每次续注册图标都闪一下红色观感很差；只有注册失败
	// 或未注册才转红（界面圆点仍按 黄=注册中 显示，REQ-UI-018）。
	trayRegistered bool
	autoAnswer     bool // 自动接听开关

	// 扬声器音量 / 麦克风增益（REQ-UI-023 / REQ-MEDIA-006）：会话内记忆——
	// 通话面板再次弹出时沿用上次的值；不持久化，应用重启后恢复默认 100%。
	// 0% 即"禁用"，等同原来开关的「关」。见 ui_call_volume.go。
	speakerLevel levelState
	micLevel     levelState
	pctTimer     *time.Timer // 百分比数字的隐藏定时器（滚动时重置）
	pctGen       uint64      // 百分比数字的世代号（作废已触发但还没执行的旧回调）

	// 主窗口可见性跟踪（Fyne 无 Visible() 查询，自行维护）：
	// mainShown 当前是否显示；mainWasVisible 通话前是否可见
	//（通话结束后仅当通话前可见时恢复显示主窗口）
	mainShown      bool
	mainWasVisible bool

	// 自动更新（REQ-UPD-003）：提示行 + 右下角提示窗
	updateBar          *fyne.Container // 呼叫按钮上方的提示行容器
	updateBarBtn       *pointerButton  // 提示行按钮：更新 vX 来源 host / 更新中...（手型光标）
	updW               fyne.Window     // 更新提示窗（懒创建）
	updTitleLabel      *widget.Label
	updInfoLabel       *widget.Label
	updStateLabel      *widget.Label
	updStateRow        *fyne.Container   // 状态行/进度条共用槽位（高度固定为状态标签高度）
	updStatePad        *canvas.Rectangle // 槽位占位矩形（撑住行高）
	updProgress        *widget.ProgressBar
	updActionBtn       *compactButton
	updLaterBtn        *compactButton
	lastUpdate         core.UpdateStatus // 最近一次更新状态（按钮动作按它分派）
	notifiedVersion    string            // 本次运行已弹窗提醒过的版本（同版本不重复弹）
	updatePopupPending bool              // 提示窗被通话/通话中收到通知挂起：稍后补弹
	updShown           bool              // 提示窗当前是否显示（Fyne 无 Visible 查询）

	// ---- 视频（REQ-VIDEO-*）----
	videoCallBtn   *compactButton // 主窗体「视频呼叫」按钮（需求 3）
	videoAnswerBtn *compactButton // 来电弹窗「视频接听」按钮（需求 4）
	videoBtn       *compactButton // 通话面板「关闭视频」开关（需求 8）
	camSelect      *widget.Select // 设置页摄像头选择（需求 2）
	camHint        *widget.Label  // 摄像头说明行（本构建不支持时改成提示文字）
	camIDs         map[string]string

	videoW          fyne.Window   // 视频窗口（懒创建）
	videoSurface    *videoSurface // 视频画面 + 本地回显叠加
	videoOn         bool          // 需求 8 的视频总开关（会话内记忆）
	previewOn       bool          // 需求 7 的本地回显开关（只影响本地显示，不影响推送）
	callHasVideo    bool          // 本次通话视频已协商成功（决定面板是否显示视频开关）
	callSending     bool          // 本次通话要推送本地视频（决定回显区是否显示）
	remoteHasVideo  bool          // 已收到过对端画面（决定视频窗口是否显示）
	localVideoEver  bool          // 已收到过本地帧（仅用于日志）
	videoSized      bool          // 窗口尺寸是否已按对端比例初始化
	videoFullscreen bool
	webVideoOn      bool // 有网页正在接收视频画面（REQ-VIDEO-009，此时不显示视频窗口）
	ringbackOn      bool // 正在放本地回铃音（没有早期媒体时自己放，避免重复放）
	videoTopmost    bool // 视频窗口是否被用户置顶（默认不置顶，手动开启）
	videoShown      bool // 视频窗口当前是否显示（Fyne 无 Visible() 查询 API，只能自己记）

	// 帧投递：媒体层 goroutine 写、主线程读（合并刷新，见 ui_video.go）
	remoteFrame   atomic.Pointer[frameRef]
	localFrame    atomic.Pointer[frameRef]
	remotePending atomic.Bool
	localPending  atomic.Bool

	// 旋转/水平镜像（显示用，媒体层 goroutine 逐帧读取，故用原子量）。
	// 会话内记忆：不随通话结束复位（同一路摄像头/同一个对端的摆法通常不变）。
	remoteSrcW   atomic.Int32 // 远端帧的**原始**宽（旋转前，用于算窗口形状）
	remoteSrcH   atomic.Int32 // 远端帧的原始高
	remoteRot    atomic.Int32 // 远端画面旋转角度（0/90/180/270）
	remoteMirror atomic.Bool  // 远端画面水平镜像
	localRot     atomic.Int32 // 本地回显旋转角度
	localMirror  atomic.Bool  // 本地回显水平镜像

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
		// 扬声器/麦克风默认 100%（REQ-UI-023 / REQ-MEDIA-006，会话内记忆，
		// 不持久化：重启回默认开）
		speakerLevel: newLevelState(speakerMaxPercent),
		micLevel:     newLevelState(micMaxPercent),
		// 视频：默认开（会话内记忆，不持久化）
		videoOn:   true,
		previewOn: true,
	}
	if desk, ok := ui.fyne.(desktop.App); ok {
		ui.desk = desk
	}
	// 注册视频帧出口（REQ-VIDEO-006/007）：帧走这条直连通道，
	// **不走事件总线**——总线容量 64、满即丢，视频帧会把它打满。
	c.SetVideoSink(ui)
	return ui
}

// Run 启动界面主循环（阻塞直到退出）。
// 必须在主 goroutine 调用：Fyne 的 UI 初始化与事件循环要求主线程。
func (a *App) Run() {
	// 平台相关的 GLFW 预设置，必须早于第一个窗口创建（Fyne 在
	// buildMainWindow 时初始化 GLFW）。Linux 侧关闭 libdecor，原因见
	// glfw_linux.go。
	prepareGLFW()
	a.buildMainWindow()
	a.buildTray()
	a.subscribeEvents()
	a.watchWSPortFocus() // WS 端口输入框失焦保存与重启提示
	// 音频后端在后台初始化（音频服务不可达时可能耗时数十秒）：就绪后
	// 刷新设置页的设备列表。回调在后台 goroutine，需 fyne.Do 回主线程。
	if a.audioMgr != nil {
		a.audioMgr.OnReady(func() {
			fyne.Do(a.refreshDevices)
		})
	}
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
	a.buildHistoryPage()
	a.buildContactsPage()

	// 窗口内容 = 当前页 + 顶层提示图层（备注悬停用，见 ui_tooltip.go）。
	// 切页只换 Objects[0]，提示图层恒为 Objects[1]：它不含 Hoverable/Tappable，
	// 命中测试会穿透到下面的页面，绘制顺序上又在最上层、不受滚动裁剪。
	a.mainTip = newTooltipLayer()
	a.mainContent = container.NewStack(a.dialPage, a.mainTip)
	w.SetContent(a.mainContent)
	w.Resize(fyne.NewSize(320, 620)) // 更窄的宽度 + 按键增高后的总高
	w.CenterOnScreen()
	// 固定窗口尺寸：切换设置页（内容多）时窗口不被撑大
	w.SetFixedSize(true)

	// 主窗体任意位置按键输入（无需聚焦输入框，REQ-UI-024）：
	// 数字/星号/井号直接输入拨号框，退格删除末尾字符。
	// 输入框获得焦点时按键由其自身消费，此处只处理未被消费的按键。
	w.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if a.dialSelect == nil {
			return
		}
		// 仅在拨号盘页生效（设置页展示时不处理）
		if len(a.mainContent.Objects) == 0 || a.mainContent.Objects[0] != a.dialPage {
			return
		}
		// 通话中（REQ-UI-030）：只把数字键透传成 DTMF，其余按键一律不管——
		// 此时输入框是禁用的，键盘事件不会被任何控件消费，正好落到这里
		if a.uiState != uiIdle {
			if a.uiState == uiInCall && isDialDigit(ev.Name) {
				a.onKeypadDigit(string(ev.Name))
			}
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
			a.appendDialKey(string(ev.Name))
			a.onKeypadDigit(string(ev.Name)) // 按键音（空闲态不会发 DTMF）
		}
	})

	// 启动默认隐藏主窗口，仅显示托盘图标（REQ-UI 修订）；
	// 单击托盘图标或菜单「显示主界面」唤出
}

// appendDialKey 空闲态把按键字符追加进拨号框：呼出过的号码视为已完成输入，
// 再次输入时先清空（REQ-UI-024，与号码盘一致）。
func (a *App) appendDialKey(key string) {
	if a.dialSelect == nil {
		return
	}
	if a.savedNumber != "" && a.dialSelect.Text == a.savedNumber {
		a.dialSelect.SetText(key) // 整段替换：重新输入新号码
		return
	}
	a.dialSelect.SetText(a.dialSelect.Text + key)
}

// onKeypadDigit 拨号键与键盘数字的统一处理（REQ-UI-029）：
// 本机回显按键音；**通话中**同时向对端发 DTMF（RFC 4733，REQ-CALL-006）——
// 发送条件与界面状态绑定，空闲时只响一声、不发任何信令。
func (a *App) onKeypadDigit(key string) {
	switch {
	case a.onKeyTone != nil: // 测试注入
		a.onKeyTone(key)
	case a.audioMgr != nil:
		a.audioMgr.PlayDTMF(key)
	}
	if a.uiState == uiInCall && a.activeCallID != "" {
		if err := a.core.SendDTMF(a.activeCallID, key); err != nil {
			logx.Logger().Debug("发送 DTMF 失败", "digit", key, "err", err)
		}
	}
}

// buildDialPage 拨号盘页：号码输入框 + 3×4 拨号键 + 呼叫按钮（REQ-UI-005）。
func (a *App) buildDialPage() {
	// ---- 顶部栏：状态圆点 + 本机号码 + 设置按钮（右上角）----
	a.regDot = newStatusDot("unregistered")
	// 注册状态文字：与圆点同色、加粗、字号与「自动接听」按钮一致（用户要求）。
	// 用自绘文本才能直接指定颜色（widget.Label 只有 Importance 几档主题色，
	// 对不上圆点那四个自定颜色）。本机号码不在顶栏显示，见窗口标题。
	a.regLabel = newPlainText(regText("unregistered"),
		a.fyne.Settings().Theme().Size(theme.SizeNameText), dotColors["unregistered"], true)
	a.autoAnswerBtn = widget.NewButton("自动接听", a.toggleAutoAnswer)
	// 未选中时浅灰底（与通话面板开关关闭状态一致，REQ-UI-024 修订）
	a.autoAnswerBtn.Importance = widget.MediumImportance
	statusRow := fyne.NewContainerWithLayout(statusRowLayout{}, a.regDot, a.regLabel)
	// 顶栏图标按钮统一用**标准 Fyne 按钮**（低强调）：通话记录在左、通信录居中、
	// 设置在右（REQ-UI-031 / REQ-UI-032）。自绘紧凑版观感与其它按钮不一致，已回退。
	a.settingsBtn = widget.NewButtonWithIcon("", theme.SettingsIcon(), a.showSettings)
	a.settingsBtn.Importance = widget.LowImportance
	a.historyBtn = widget.NewButtonWithIcon("", theme.HistoryIcon(), a.showHistory)
	a.historyBtn.Importance = widget.LowImportance
	a.contactsBtn = widget.NewButtonWithIcon("",
		theme.NewColoredResource(IconContacts, theme.ColorNameForeground), a.showContacts)
	a.contactsBtn.Importance = widget.LowImportance

	// 顶栏：圆点+状态靠左，通话记录+通信录+设置按钮靠右，自动接听按钮居中
	topBar := container.NewBorder(nil, nil, statusRow,
		container.NewHBox(a.historyBtn, a.contactsBtn, a.settingsBtn),
		container.NewCenter(a.autoAnswerBtn))

	// ---- 号码输入框：可下拉（最近 5 次呼叫号码，去重）----
	a.dialEntry = newFormEntry()
	a.dialEntry.SetPlaceHolder("输入号码或 sip:xxx@主机")
	a.dialEntry.TextStyle = fyne.TextStyle{Bold: true}
	a.dialSelect = widget.NewSelectEntry(nil)
	a.dialSelect.SetPlaceHolder("输入号码或 sip:xxx@主机")
	// 注意：**输入框里敲数字不回显按键音、也不发 DTMF**（用户明确要求）——
	// 按键音只属于"点号码盘"与"未聚焦时在主窗体敲数字"这两条路径。
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
				if a.uiState == uiIdle {
					a.appendDialKey(key)
				}
				// 点号码盘一律回显按键音；通话中还会向对端发 DTMF
				//（REQ-UI-029 / REQ-CALL-006）；呼出/振铃中按键是禁用的
				a.onKeypadDigit(key)
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
	// REQ-VIDEO-003：呼叫按钮缩小，右侧加一个无文字的视频呼叫按钮
	a.callBtn.SetMinSize(0, dialCallButtonHeight)
	a.videoCallBtn = newCompactButton(IconVideoCall, colorSuccessGreen, dialCallButtonHeight, a.onVideoCallButton)

	// 呼叫按钮上方一行：版本更新提示（REQ-UPD-003，默认隐藏）
	a.buildUpdateBar()

	// 呼叫按钮占满剩余宽度，视频按钮固定宽停在右侧
	callRow := container.NewBorder(nil, nil, nil, a.videoCallBtn, a.callBtn)
	if !a.core.VideoSupported() {
		// 未启用视频的构建：隐藏视频入口，呼叫按钮恢复占满
		a.videoCallBtn.Hide()
		callRow = container.NewBorder(nil, nil, nil, nil, a.callBtn)
	}

	a.dialPage = container.NewBorder(
		topBar,
		container.NewVBox(a.updateBar, callRow),
		nil, nil,
		container.NewVBox(
			inputRow,
			padGrid,
		),
	)
}

// buildSettingsPage 设置页：账户、音频设备、日志、网页接入（REQ-UI-006/012）。
func (a *App) buildSettingsPage() {
	// ---- 顶部栏：返回图标 + 标题（与通话记录页同款，REQ-UI-031）----
	topBar := pageTopBar("配置", a.showDialPage, nil)

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
	// 服务端心跳检测（默认开启，REQ-ACC-020）：开启后注册报文的 User-Agent
	// 带 optchk 标记，服务端据此定期发 OPTIONS 探测；OPTIONS 用 sipgo 默认
	// 行为回 405 即可（实测服务端接受）。改变的是注册报文内容，随「保存并注册」生效。
	a.chkOptCheck = widget.NewCheck("启用服务端心跳检测", nil)

	// 表单项采用「label 在上、输入框在下」的堆叠布局，
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
		a.chkOptCheck,
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

	// ---- 视频设备（REQ-VIDEO-002/005）----
	a.camSelect = widget.NewSelect([]string{}, nil)
	a.camIDs = make(map[string]string)
	refreshCamBtn := widget.NewButtonWithIcon("刷新设备", theme.ViewRefreshIcon(), a.refreshDevices)
	// 说明文字必须开自动折行：主窗体是固定宽度，标签的 MinSize 按整行文字宽度
	// 计算会把窗口撑宽（与「网页接入」的 wsHint 同样处理）。
	a.camHint = widget.NewLabel("选择「禁用」后：发起视频呼叫不发送本地视频，来电也不显示「视频接听」按钮，但仍可查看对方的视频")
	a.camHint.Wrapping = fyne.TextWrapWord
	videoCard := container.NewVBox(
		widget.NewLabelWithStyle("视频设备", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		formItem("摄像头", a.camSelect),
		a.camHint,
		refreshCamBtn,
	)

	// ---- 日志配置（REQ-LOG-006）----
	a.logFileCheck = widget.NewCheck("记录日志到文件", nil)
	a.sipTraceCheck = widget.NewCheck("记录 SIP 信令", a.saveSIPTrace)
	a.logLevelSel = widget.NewSelect([]string{"debug", "info", "warn", "error"}, nil)
	logDirBtn := widget.NewButtonWithIcon("选择目录...", theme.FolderOpenIcon(), a.chooseLogDir)
	logDirOpenBtn := widget.NewButtonWithIcon("打开日志目录", theme.FolderIcon(), a.openLogDir)
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
		logDirOpenBtn, // 打开日志目录放在"保存日志配置"下方
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
		videoCard, widget.NewSeparator(),
		logCard, widget.NewSeparator(),
		wsCard,
	)
	// 右侧 14px 精确空隙（滚动条悬浮区），与左侧 padding 对称
	rightGap := canvas.NewRectangle(color.Transparent)
	rightGap.SetMinSize(fyne.NewSize(14, 1))
	scrollContentWithGap := container.NewBorder(nil, nil, nil, rightGap, scrollContent)
	scroll := container.NewVScroll(scrollContentWithGap)
	a.settingsScroll = scroll

	// 底部常驻一行版本信息（REQ-UI-026）：版本号、平台/架构、构建时间。
	// 主窗体固定 320px 宽，用标注字号（11px）的 canvas 文本避免整行超宽被截断
	versionText := canvas.NewText(fmt.Sprintf("版本 %s · %s · 构建于 %s",
		core.Version, core.Platform(), core.BuildTimeText()), theme.ForegroundColor())
	versionText.TextSize = a.fyne.Settings().Theme().Size(theme.SizeNameCaptionText)

	a.settingsPage = container.NewBorder(
		topBar,
		container.NewHBox(versionText),
		nil, nil,
		scroll,
	)

	// 回填当前配置
	a.loadAccountIntoForm()
	a.loadLogConfigIntoForm()
	a.refreshDevices()
	a.refreshCameras()
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
// 控件包一层 HScroll：长内容（如长地址）横向滚动而非撑宽窗口。
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
	a.hideTip() // 切页时收起备注提示（指针可能没离开行）
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
	a.hideTip() // 切页时收起备注提示
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
	a.setCallUI(uiDialing, a.activeCall)
	a.refreshRecentDials()
}

// onVideoCallButton 视频呼叫按钮（REQ-VIDEO-003）：无通话时发起视频呼叫，
// 呼叫/通话中与呼叫按钮一致地挂断。
func (a *App) onVideoCallButton() {
	if a.activeCallID != "" {
		_ = a.core.Hangup(a.activeCallID)
		return
	}
	target := strings.TrimSpace(a.dialSelect.Text)
	if target == "" {
		a.showInfo("提示", "请输入号码")
		return
	}
	callID, err := a.core.DialVideo(context.Background(), target)
	if err != nil {
		a.showError(err)
		return
	}
	a.activeCallID = callID
	a.activeCall = &core.Call{
		ID:        callID,
		Direction: "outgoing",
		Remote:    target,
		State:     core.CallRinging,
		HasVideo:  true,
	}
	a.savedNumber = target
	a.videoOn = true
	a.previewOn = true
	a.setCallUI(uiDialing, a.activeCall)
	a.refreshRecentDials()
}

// cameraUsable 摄像头当前是否可用于发送本地视频（REQ-VIDEO-005）。
func (a *App) cameraUsable() bool {
	// 没有摄像头后端的构建（Windows 386）也要算"不可用"：否则会显示
	//「视频接听」按钮（点了却没有本地画面）。
	if !a.core.VideoSupported() || !a.core.CameraSupported() {
		return false
	}
	return a.cfg.Video.CameraUsable()
}

// setCallUI 通话状态机（用户重新设计，REQ-UI-008 修订）：
//   - uiIdle：通话面板隐藏；主窗口仅当通话前可见时恢复显示（通话前
//     已隐藏到托盘则保持隐藏——按用户反馈修订）
//   - uiDialing：主窗口隐藏，通话面板置顶显示"呼叫中 + 号码 + 时长 + 红色挂断"
//   - uiRinging：主窗口隐藏，通话面板置顶显示"来电号码 + 振铃时长 + 绿色接听 + 红色挂断"
//   - uiInCall：主窗口隐藏，通话面板置顶显示"号码 + 通话时长 + 黄色 || + 红色挂断"
//
// call 为本次通话（uiIdle 传 nil）：对端展示文案由它算出——命中通信录时
// 号码右边显示联系人名称与 ★（REQ-UI-037）。
func (a *App) setCallUI(state int, call *core.Call) {
	// 通话面板显示前记录主窗口是否显示（仅从空闲进入通话状态时
	// 记录一次——状态间切换或重复事件不会覆盖记录）
	if state != uiIdle && a.uiState == uiIdle {
		a.mainWasVisible = a.isMainWindowVisible()
		// 更新提示窗与通话面板都吸附右下角，通话期间先收起提示窗
		//（通话结束后按 pending 补弹，REQ-UPD-003）
		if a.updShown {
			a.hideUpdatePopup()
			a.updatePopupPending = true
		}
	}
	a.uiState = state
	switch state {
	case uiIdle:
		a.stopTimer()
		if a.callW != nil {
			a.callW.Hide()
		}
		if a.callTip != nil {
			a.callTip.hide() // 面板藏了，提示不能留在屏幕上
		}
		a.applyMainCallMode(false) // 恢复输入框/设置/自动接听/呼叫等控件（REQ-UI-030）
		if a.savedNumber != "" {
			a.dialSelect.SetText(a.savedNumber)
		}
		a.setKeypadEnabled(true)
		a.updateCallButton()
		// 视频界面状态一并复位（视频窗口隐藏、帧缓冲清空、按钮复位）
		a.ringHasVideo = false
		a.resetVideoUI()
		// 主窗体可见性（REQ-UI-030 修订，用户定的规则）：
		//  - 通话前是打开的 → **无论通话中开关过没有，结束都显示**；
		//  - 通话前没开 → 看通话中的最后状态：自己打开又没关就保持，
		//    自己关掉了（或压根没开过）就不显示。
		if shouldShowMainAfterCall(a.mainWasVisible, a.mainShown) {
			a.mainW.Show()
			a.mainShown = true
			a.mainW.RequestFocus()
			a.bringMainToFront() // 恢复显示时带到前台
		}
		// 通话期间挂起的更新提示窗：通话结束后补弹
		if a.updatePopupPending {
			a.updatePopupPending = false
			a.showUpdatePopup()
		}

	case uiDialing:
		a.mainW.Hide()
		a.mainShown = false
		a.applyMainCallMode(true) // 通话中：主窗体只放开号码按键（REQ-UI-030）
		a.setKeypadEnabled(false) // 呼叫未接通，发不了 DTMF（REQ-CALL-006）
		a.buildCallPanelIfNeeded()
		a.updateCallWindowTitle()
		a.setCallName(call)
		a.callStateLabel.SetText("呼叫中")
		a.rebuildCallRows(dialingButtons)
		showCallWindow(a.callW) // 平台函数：Windows 下不占任务栏（见 showWindowAsTool）
		moveCallWindowToCorner(a.callW)
		a.startTimer()

	case uiRinging:
		a.mainW.Hide()
		a.mainShown = false
		a.applyMainCallMode(true)
		a.setKeypadEnabled(false) // 振铃中还不能发 DTMF
		a.buildCallPanelIfNeeded()
		a.updateCallWindowTitle()
		a.setCallName(call)
		a.callStateLabel.SetText("来电")
		a.rebuildCallRows(ringingButtons)
		showCallWindow(a.callW)
		a.callW.RequestFocus()
		moveCallWindowToCorner(a.callW)
		a.startTimer() // 振铃时长

	case uiInCall:
		a.mainW.Hide()
		a.mainShown = false
		a.applyMainCallMode(true)
		a.setKeypadEnabled(true) // 通话中：号码按键用来发 DTMF（REQ-CALL-006）
		a.buildCallPanelIfNeeded()
		a.updateCallWindowTitle()
		a.setCallName(call)
		a.callStateLabel.SetText("通话中")
		a.rebuildCallRows(incallButtons)
		showCallWindow(a.callW)
		moveCallWindowToCorner(a.callW)
		a.startTimer() // 通话时长从 0 开始
	}
}

// shouldShowMainAfterCall 通话结束时要不要显示主窗体（REQ-UI-030 的规则，
// 抽成纯函数便于测试）：
//   - 通话前是打开的（mainWasVisible）→ 一律显示，不管通话中开关过没有；
//   - 通话前没开 → 只有"通话中自己打开且没关"（mainShown 仍为 true）才显示。
func shouldShowMainAfterCall(mainWasVisible, mainShown bool) bool {
	return mainWasVisible || mainShown
}

// applyMainCallMode 按"是否在通话中"收紧/恢复主窗体控件（REQ-UI-030）。
//
// 通话期间主窗体可以从托盘再次唤出，但**只允许点号码按键、或用键盘输入数字**
// （两者都发 DTMF）：输入框（含最近号码下拉）、设置、自动接听、呼叫/视频呼叫
// 按钮全部禁用，并强制回到拨号盘页；号码按键的可用性由 setCallUI 按通话阶段管
// （只有通话中才可用）。通话结束后整体恢复。
func (a *App) applyMainCallMode(inCall bool) {
	if inCall {
		a.showDialPage()
	}
	setEnabled := func(c interface {
		Enable()
		Disable()
	}, on bool) {
		if c == nil {
			return
		}
		if on {
			c.Enable()
		} else {
			c.Disable()
		}
	}
	// 输入框禁用后不可聚焦，键盘数字因此落到 canvas 按键回调 → 发 DTMF
	setEnabled(a.dialSelect, !inCall)
	for _, c := range []interface {
		Enable()
		Disable()
	}{a.settingsBtn, a.historyBtn, a.contactsBtn, a.autoAnswerBtn, a.callBtn, a.videoCallBtn} {
		setEnabled(c, !inCall)
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
// （文字颜色固定，不随主题变化，REQ-UI-024 修订）。
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
	// 标记为退出项（Linux 生效，见 markTrayQuit）：否则 Fyne 会在菜单
	// 末尾自动补一个本地化的 Quit 项（driver_desktop.go 的
	// addMissingQuitForMenu），托盘上会出现「退出」与「Quit」两项
	markTrayQuit(quit)
	menu := fyne.NewMenu("mti-sip-phone", showMain, fyne.NewMenuItemSeparator(), quit)

	// 应用图标固定为绿色话机（用户要求：标准 app 图标是绿色话机，红色只作
	// 异常提醒——那个由托盘图标承担）。各窗口另有自己的 SetIcon，这里同时是
	// "没单独设图标的窗口"的默认值。
	a.fyne.SetIcon(IconPhoneGreen)
	// 单击托盘图标唤出主窗口（Fyne 托盘无原生双击事件，单击唤出）
	a.desk.SetSystemTrayMenu(menu)
	a.desk.SetSystemTrayWindow(a.mainW)
	// 托盘图标显式按注册状态给一次（未注册红 / 已注册绿，REQ-UI-011）：
	// Fyne 的托盘就绪回调只在"从未显式设置过托盘图标"时才退回应用图标，
	// 不设的话托盘会跟着应用图标一起变绿，丢掉未注册的红色提醒。
	a.updateTrayIcon()
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
	if a.trayRegistered {
		return IconPhoneGreen
	}
	return IconPhoneRed
}

// setRegState 记录注册状态：托盘图标只在"注册成功"与"失败/未注册"之间
// 切换，续注册（registering）保持原色。
func (a *App) setRegState(state string) {
	a.regState = state
	switch state {
	case "registered":
		a.trayRegistered = true
	case "failed", "unregistered":
		a.trayRegistered = false
	}
}

// appTitle 应用标题："mti-sip-phone - 本机号码"（未注册/未配置时只有应用名）。
// 主窗口标题栏、托盘悬停提示与未接来电通知共用。
func (a *App) appTitle() string {
	account := a.core.State().Register.Account
	title := "mti-sip-phone"
	if account != "" {
		title += " - " + account
	}
	return title
}

// updateWindowTitle 标题栏与托盘悬停提示。
func (a *App) updateWindowTitle() {
	title := a.appTitle()
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
	// regLabel 显示注册状态文字，颜色与圆点一致（本机号码见窗口标题/配置页）。
	st := a.core.State()
	a.setRegState(string(st.Register.State))
	if a.regDot != nil {
		a.regDot.setState(a.regState)
	}
	a.setRegLabel(a.regState)
	a.updateTrayIcon()
	a.updateWindowTitle()

	sub := a.core.Bus().Subscribe()
	// **订阅之后再取一次快照**：网页可能在 UI 订阅事件之前就连上并订阅了视频
	//（软电话重启时页面会自动重连重订阅），漏掉那个 video.web 事件的后果是
	//"网页在看画面，桌面视频窗口却也打开了"（用户实测）。旋转/镜像状态同理。
	a.webVideoOn = a.core.WebVideoState().Active
	a.applyVideoTransforms(a.core.VideoTransforms())
	go func() {
		for ev := range sub {
			fyne.Do(func() { a.onEvent(ev) })
		}
	}()
}

// onEvent 处理核心事件（在 Fyne 主线程执行）。
func (a *App) onEvent(ev core.Event) {
	switch ev.Name {
	case "system.update":
		if st, ok := ev.Data.(core.UpdateStatus); ok {
			a.onUpdateStatus(st)
		}
	case "system.autoAnswer":
		// 自动接听开关变化（本地按钮点击或网页 system.setAutoAnswer 设置）
		if st, ok := ev.Data.(core.AutoAnswerState); ok {
			a.setAutoAnswer(st.Enabled)
		}

	case "register.state":
		if data, ok := ev.Data.(map[string]any); ok {
			if state, ok := data["state"].(string); ok {
				a.setRegState(state)
				a.regDot.setState(state)
				a.setRegLabel(state) // 顶栏显示状态文字（不再显示本机号码）
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
			// REQ-VIDEO-004/005：对端带视频且摄像头可用时才显示「视频接听」
			a.ringHasVideo = call.HasVideo
			a.videoOn = true
			a.previewOn = true
			a.callHasVideo = false // 等 call.video 事件确认协商结果
			a.callSending = false
			a.remoteHasVideo = false
			a.localVideoEver = false
			a.videoSized = false
			a.setCallUI(uiRinging, call)
			if a.audioMgr != nil {
				a.audioMgr.PlayRing()
			}
		}

	case "call.video":
		// 视频状态变化（协商成功 / 开关切换 / 拿到对端分辨率）
		if callID, call := callData(ev.Data); call != nil && callID == a.activeCallID {
			a.activeCall = call
			a.videoOn = call.Video.Enabled
			a.previewOn = call.Video.PreviewOn
			a.callHasVideo = call.Video.Negotiated
			a.callSending = call.Video.Sending
			a.applyVideoButton()
			a.applyLocalVideoVisibility()
			if a.callHasVideo || a.callSending {
				// 把窗口**对象**建好（但**不 Show**）：帧要先有落点才能
				// 触发 remoteHasVideo，否则永远等不到"有内容"的那一刻——摄像头
				// 禁用时 callSending=false，窗口又只能靠 remoteHasVideo 触发，
				// 会形成死锁（表现为"远端画面收到了却没有视频窗口"）。
				// callSending（视频呼叫拨出/视频接听）同样要先建好窗口，
				// 本地回显才有地方落。
				a.ensureVideoWindow()
			}
			// 窗口显隐由「有没有内容可显示」决定，见 wantVideoWindow：
			// 只有音频（普通接听 / 对端无视频）时不留空窗口；
			// 本端要推流且回显开着时即使摄像头打不开也显示黑底回显区。
			a.syncVideoWindow()
			// 不在这里重建面板行：call.video 可能早于 call.established 到达
			//（呼出路径），此刻按"通话中"重建会显示错的按钮组。
			// 视频开关的显隐由 call.established 时的 rebuildCallRows 按
			// callHasVideo 决定，顺序上 call.video 一定先被处理。
		}

	case "video.transform":
		// 画面旋转/镜像变化（桌面与网页共用一份状态，谁改都广播）
		if st, ok := ev.Data.(core.VideoTransforms); ok {
			a.applyVideoTransforms(st)
		}

	case "video.web":
		// 网页端视频订阅状态变化（REQ-VIDEO-009）：有网页在看画面时
		// 软电话自动关闭视频窗口；网页退订后按当前内容恢复显示。
		//
		// ⚠️ 负载是 core.WebVideoState 结构体——总线是进程内的，事件不经
		// JSON 序列化，**不能**断言成 map[string]any（那样断言恒失败，
		// 表现为"订阅了但桌面窗口不关"）。只有 WS 客户端收到的是 JSON 对象。
		st, ok := ev.Data.(core.WebVideoState)
		if ok && st.Active != a.webVideoOn {
			a.webVideoOn = st.Active
			logx.Logger().Info("网页视频订阅状态变化",
				"active", st.Active, "local", st.Local, "remote", st.Remote, "viewers", st.Viewers)
			a.syncVideoWindow()
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
				a.setCallUI(uiDialing, call)
			}
		}

	case "call.ringing":
		// 呼出振铃：通话面板保持"呼叫中"（对方信息可能更新）。
		// 183 附 SDP 时媒体引擎已创建：把会话内记忆的扬声器/麦克风
		// 状态下发（呼叫阶段关闭扬声器即可屏蔽回铃音，REQ-UI-023）。
		//
		// 回铃音（用户要求）：**有早期媒体**（183+SDP）时由对端下发、媒体层
		// 直接播放，本端不放；**没有早期媒体**才放本地回铃音（ringback.wav）。
		if callID, call := callData(ev.Data); call != nil && callID == a.activeCallID {
			a.activeCall = call
			a.applySessionAudioState()
			early := false
			if m, ok := ev.Data.(map[string]any); ok {
				early, _ = m["earlyMedia"].(bool)
			}
			if a.audioMgr != nil {
				if early && a.ringbackOn {
					a.ringbackOn = false
					logx.Logger().Info("对端已下发早期媒体，停止本地回铃音")
					a.audioMgr.StopRing() // 对端开始送回铃音了，停掉本地的
				} else if !early && !a.ringbackOn {
					a.ringbackOn = true
					logx.Logger().Info("无早期媒体，播放本地回铃音")
					a.audioMgr.PlayRingback()
				}
			}
		}

	case "call.video-established":
		// 占位分支：视频窗口的显示由首帧到达驱动（renderVideoFrame）
	case "call.established":
		if callID, call := callData(ev.Data); call != nil {
			a.activeCallID = callID
			a.activeCall = call
			a.savedNumber = call.Remote
			a.setCallUI(uiInCall, call)
			// 通话建立：停止振铃音（含本地回铃音）
			a.ringbackOn = false
			if a.audioMgr != nil {
				a.audioMgr.StopRing()
			}
		}

	case "call.terminated", "call.failed":
		// 通话记录窗口开着就刷新（新记录在 core 侧先入队再发事件，这里查得到）。
		// 必须在下面 call.failed 的提前 return 之前。
		a.historyOnCallEnded()
		a.contactsOnCallEnded()
		// 停止振铃音，并按结果放提示音：遇忙放忙音，其余放挂断音
		reason := ""
		missed := false
		if m, ok := ev.Data.(map[string]any); ok {
			reason, _ = m["reason"].(string)
			missed, _ = m["missed"].(bool)
		}
		if missed {
			// 未接来电（REQ-UI-010）：是否未接由核心判定（core.missedOnEnd），
			// 这里只负责弹桌面通知。振铃时长在主线程读（callStartAt 由界面
			// 计时器维护，未接来电不会走到"通话时长"分支，所以它就是振铃起点）。
			if _, call := callData(ev.Data); call != nil {
				var ringFor time.Duration
				if !a.callStartAt.IsZero() {
					ringFor = time.Since(a.callStartAt)
				}
				a.notifyMissedCall(call, ringFor)
			}
		}
		if a.audioMgr != nil {
			a.ringbackOn = false
			a.audioMgr.StopRing()
			if isBusyReason(reason) {
				logx.Logger().Info("对端忙，播放遇忙音", "reason", reason)
				a.audioMgr.PlayBusy()
			} else {
				a.audioMgr.PlayHangup()
			}
		}
		if ev.Name == "call.failed" {
			if reason == "" {
				reason = "未知原因"
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

	case "call.audio":
		// 麦克风/扬声器开关变化（REQ-UI-023）：桌面按钮与网页控制共用一份状态，
		// 网页改了也要同步本机面板的按钮外观与"会话内记忆"（0% ↔ 上次非零值，
		// 幂等——本机自己滚轮触发的调节也会收到这条事件，不能把刚调的值冲掉）。
		if callID, call := callData(ev.Data); call != nil && callID == a.activeCallID {
			a.activeCall = call
			a.syncAudioButtonsFromEvent(call)
		}
	}
}

// isBusyReason 呼叫失败原因是否属于"忙"：486 Busy Here / 600 Busy Everywhere
// （sip 层给的 reason 形如 "486 Busy Here"）。
func isBusyReason(reason string) bool {
	code := 0
	if _, err := fmt.Sscanf(reason, "%d", &code); err != nil {
		return false
	}
	return code == 486 || code == 600
}

// regText 注册状态中文文案（顶栏显示的就是它）。
// 「注册中」不写省略号：顶栏空间有限，圆点的黄色已经表示"进行中"。
func regText(state string) string {
	switch state {
	case "registered":
		return "已注册"
	case "registering":
		return "注册中"
	case "failed":
		return "注册失败"
	default:
		return "未注册"
	}
}

// setRegLabel 更新顶栏的注册状态文字：颜色跟圆点一致（dotColors），加粗。
func (a *App) setRegLabel(state string) {
	if a.regLabel == nil {
		return
	}
	if _, ok := dotColors[state]; !ok {
		state = "unregistered"
	}
	a.regLabel.SetText(regText(state))
	a.regLabel.SetColor(dotColors[state])
	a.regLabel.SetBold(true)
}

// toggleAutoAnswer 自动接听开关：开启后来电自动接听。
// 选中 = 蓝底；未选中 = 浅灰底（REQ-UI-024 修订）。
func (a *App) toggleAutoAnswer() {
	if err := a.core.SetAutoAnswer(!a.autoAnswer); err != nil {
		logx.Logger().Warn("设置自动接听失败", "err", err)
	}
	// 按钮外观由 system.autoAnswer 事件回来时统一刷新（见 setAutoAnswer），
	// 这样桌面按钮与网页开关永远是同一份状态。
}

// setAutoAnswer 刷新自动接听开关的界面状态（本地点击与网页开关共用）。
func (a *App) setAutoAnswer(enabled bool) {
	a.autoAnswer = enabled
	logx.Logger().Info("自动接听开关", "enabled", enabled)
	if a.autoAnswerBtn == nil {
		return
	}
	if enabled {
		a.autoAnswerBtn.Importance = widget.HighImportance
	} else {
		a.autoAnswerBtn.Importance = widget.MediumImportance
	}
	a.autoAnswerBtn.Refresh()
}

// showMain 显示主窗口（托盘单击/菜单唤出）。
// 通话期间（呼叫/振铃/通话中）主窗口保持隐藏，通话面板关闭后
// 才可唤出（REQ-UI-025 修订）。
func (a *App) showMain() {
	// 通话中也能从托盘唤出主窗体（REQ-UI-030）：此时主窗体只放开号码按键与
	// 键盘输入（发 DTMF），输入框/设置/呼叫按钮等一律禁用，见 applyMainCallMode
	a.applyMainCallMode(a.uiState != uiIdle)
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
//
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
		ChkOpt:      a.chkOptCheck.Checked,
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
	a.chkOptCheck.SetChecked(acc.ChkOpt)
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

// fileDialogResult 系统文件/目录选择对话框的结果：必须把"用户取消"与
// "系统不可用"分开——取消后再弹一个内置对话框是明显的 bug。
type fileDialogResult int

const (
	fileDialogUnavailable fileDialogResult = iota // 系统不支持或打开失败：调用方回退
	fileDialogPicked                              // 用户选定路径
	fileDialogCancelled                           // 用户取消：不做任何事
)

// chooseLogDir 选择日志目录：优先用系统文件夹选择对话框（独立窗口、
// 可自由缩放）；系统不支持时回退到 Fyne 内置对话框（内嵌在主窗体画布，
// 空间受 320px 主窗体限制）；用户取消则什么都不做。
// 系统对话框自身带消息循环会阻塞，必须在后台 goroutine 调用，
// 结果经 fyne.Do 回主线程。
func (a *App) chooseLogDir() {
	go func() {
		dir, res := showSystemFolderDialog("选择日志目录", a.logDirPath())
		switch res {
		case fileDialogPicked:
			if dir != "" {
				fyne.Do(func() { a.applyLogDir(dir) })
			}
		case fileDialogCancelled:
			// 用户取消：保持原样
		default:
			fyne.Do(a.chooseLogDirFallback)
		}
	}()
}

// chooseLogDirFallback Fyne 内置目录选择对话框（主线程调用）。
func (a *App) chooseLogDirFallback() {
	dlg := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil || uri == nil {
			return
		}
		a.applyLogDir(uri.Path())
	}, a.mainW)
	dlg.Show()
}

// applyLogDir 应用选中的日志目录（主线程）。
func (a *App) applyLogDir(dir string) {
	a.cfg.Log.Dir = dir
	a.logDirLabel.SetText(dir)
}

// logDirPath 生效的日志目录：配置了用配置值，否则 <配置目录>/logs
// （与启动时 logx 的解析保持一致）。
func (a *App) logDirPath() string {
	if a.cfg.Log.Dir != "" {
		return a.cfg.Log.Dir
	}
	return filepath.Join(filepath.Dir(a.cfgPath), "logs")
}

// openLogDir 用系统文件管理器打开日志目录。
func (a *App) openLogDir() {
	dir := a.logDirPath()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.showInfo("提示", "日志目录不可用：\n"+dir)
		return
	}
	if err := openInFileManager(dir); err != nil {
		logx.Logger().Warn("打开日志目录失败", "dir", dir, "err", err)
		a.showInfo("提示", "打开日志目录失败：\n"+err.Error())
	}
}

// openInFileManager 在系统文件管理器中打开目录。
func openInFileManager(path string) error {
	switch runtime.GOOS {
	case "windows":
		// explorer 打开目录后自身会立即退出，忽略其退出码
		return exec.Command("explorer", path).Start()
	case "linux":
		return exec.Command("xdg-open", path).Start()
	}
	return fmt.Errorf("当前平台不支持打开文件管理器")
}

// refreshDevices 刷新音频设备列表（枚举真实系统设备，REQ-MEDIA-002）。
// 枚举可能耗时（ALSA 会逐个探测设备），放到后台 goroutine 执行、结果
// 回到主线程应用；音频后端尚未就绪时先显示占位，就绪后由 OnReady 再刷。
func (a *App) refreshDevices() {
	// 摄像头与音频设备共用两个「刷新设备」按钮，一并刷新
	//（放在最前面，避免下面音频分支提前 return 时漏刷）
	a.refreshCameras()
	if a.audioMgr == nil {
		a.applyDeviceLists(nil, nil, "音频后端不可用")
		return
	}
	if !a.audioMgr.Ready() {
		a.applyDeviceLists(nil, nil, "音频初始化中…")
	}
	go func() {
		mics, speakers := a.audioMgr.Devices()
		fyne.Do(func() {
			a.applyDeviceLists(mics, speakers, "未检测到音频设备")
		})
	}()
}

// applyDeviceLists 在 UI 线程应用设备枚举结果（placeholder 为列表为空时
// 的占位文案）。可重复调用：设备就绪后会再次调用以回填真实设备。
func (a *App) applyDeviceLists(mics, speakers []core.AudioDevice, placeholder string) {
	micNames := make([]string, 0, len(mics))
	micIDs := make(map[string]string)
	for _, d := range mics {
		micNames = append(micNames, d.Name)
		micIDs[d.Name] = d.ID
	}
	if len(micNames) == 0 {
		micNames = []string{placeholder}
	}
	spkNames := make([]string, 0, len(speakers))
	spkIDs := make(map[string]string)
	for _, d := range speakers {
		spkNames = append(spkNames, d.Name)
		spkIDs[d.Name] = d.ID
	}
	if len(spkNames) == 0 {
		spkNames = []string{placeholder}
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
			if a.audioMgr != nil {
				a.audioMgr.SetMic(id)
			}
			a.cfg.Audio.MicID = id
			a.persistConfig()
		}
	}
	a.speakerSelect.OnChanged = func(name string) {
		if a.loadingForm {
			return
		}
		if id, ok := spkIDs[name]; ok {
			if a.audioMgr != nil {
				a.audioMgr.SetSpeaker(id)
			}
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

// refreshCameras 后台枚举摄像头并回填下拉框（REQ-VIDEO-002）。
// 与 refreshDevices 同样的模式：枚举可能耗时（V4L2 逐个探测 /
// DirectShow 枚举），必须放到后台 goroutine，回主线程再更新控件。
func (a *App) refreshCameras() {
	if a.camSelect == nil {
		return
	}
	if !a.core.VideoSupported() {
		a.camSelect.Disable()
		a.applyCameraList(nil, "此构建未启用视频")
		return
	}
	if !a.core.CameraSupported() {
		// 编解码可用但**本平台没有摄像头后端**（Windows 386 这类目标）。
		// 这里必须与"没插摄像头"区分开：否则用户会在空列表里选「禁用」，
		// 把共用配置写坏（64 位版也跟着不发送本地视频了——实测踩过）。
		a.camSelect.Disable()
		a.applyCameraList(nil, "此版本不支持摄像头")
		a.camHint.SetText("本版本（32 位）不支持摄像头采集：视频通话只能看对方，不发送本地画面")
		return
	}
	a.camHint.SetText("")
	go func() {
		cams := a.core.VideoDevices()
		fyne.Do(func() { a.applyCameraList(cams, "未检测到摄像头") })
	}()
}

// applyCameraList 回填摄像头下拉框：列表首项为「系统默认」，末项为「禁用」。
func (a *App) applyCameraList(cams []core.VideoDevice, placeholder string) {
	names := []string{cameraDefaultLabel}
	ids := map[string]string{cameraDefaultLabel: ""}
	for _, c := range cams {
		names = append(names, c.Name)
		ids[c.Name] = c.ID
	}
	names = append(names, cameraDisabledLabel)
	if len(cams) == 0 && placeholder != "" {
		names = append(names, placeholder)
	}
	a.camSelect.Options = names
	a.camIDs = ids
	a.camSelect.OnChanged = func(name string) {
		if a.loadingForm {
			return
		}
		switch name {
		case cameraDisabledLabel:
			a.cfg.Video.CameraID = config.CameraDisabled
			a.cfg.Video.CameraName = ""
		case cameraDefaultLabel:
			a.cfg.Video.CameraID = ""
			a.cfg.Video.CameraName = ""
		default:
			id, ok := ids[name]
			if !ok {
				return
			}
			a.cfg.Video.CameraID = id
			a.cfg.Video.CameraName = name
		}
		_ = a.core.SetCamera(a.cfg.Video.CameraID)
		a.persistConfig()
	}

	// 回选：优先按名称（Windows 驱动 ID 每次启动都会变，名称才稳定）
	a.loadingForm = true
	defer func() { a.loadingForm = false }()
	sel := cameraDefaultLabel
	switch a.cfg.Video.CameraID {
	case config.CameraDisabled:
		sel = cameraDisabledLabel
	case "":
		sel = cameraDefaultLabel
	default:
		if a.cfg.Video.CameraName != "" {
			for _, n := range names {
				if n == a.cfg.Video.CameraName {
					sel = n
					break
				}
			}
		}
	}
	a.camSelect.SetSelected(sel)
}

// cameraDefaultLabel / cameraDisabledLabel 摄像头下拉里的两个特殊项。
const (
	cameraDefaultLabel  = "系统默认"
	cameraDisabledLabel = "禁用（不发送本地视频）"
)

// newBorderlessWindow 创建一个无边框窗口。
//
// 走 Fyne 的 splash 窗口（`desktop.Driver.CreateSplashWindow`）：它由 GLFW
// 在**创建时**就按无边框建立（Win32 下是 WS_POPUP、frameRect 归零），GLFW 的
// 几何计算始终自洽。**不要**改用"先建普通窗口再 Win32 改样式"——那会让 GLFW
// 的边框缓存失准，Fyne 认为画布比真实客户区高出整条标题栏，内容被顶掉第一行。
//
// 非桌面驱动（如测试驱动）退化为普通窗口，功能不受影响。
func (a *App) newBorderlessWindow(title string) fyne.Window {
	if dd, ok := a.fyne.Driver().(desktop.Driver); ok {
		w := dd.CreateSplashWindow()
		w.SetTitle(title)
		return w
	}
	return a.fyne.NewWindow(title)
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

// isDialChar 判断字符是否为可拨号的按键（0-9 / * / #，REQ-UI-024）
func isDialChar(s string) bool {
	if len(s) != 1 {
		return false
	}
	c := s[0]
	return (c >= '0' && c <= '9') || c == '*' || c == '#'
}

// isDialDigit 判断按键名是否为可拨号的数字键（0-9 / * / #，REQ-UI-024）。
func isDialDigit(name fyne.KeyName) bool { return isDialChar(string(name)) }
