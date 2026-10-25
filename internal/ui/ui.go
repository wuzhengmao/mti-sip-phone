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
	"fyne.io/fyne/v2/layout"
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

// themeColorNRGBA 取一个主题色并转成 color.NRGBA（自绘控件用；
// 主题色是 color.Color，自绘的 compactButton 等只收 NRGBA）。
func themeColorNRGBA(name fyne.ThemeColorName) color.NRGBA {
	app := fyne.CurrentApp()
	if app == nil {
		return color.NRGBA{R: 0xcc, G: 0xcc, B: 0xcc, A: 0xff}
	}
	th := app.Settings().Theme()
	r, g, b, a := th.Color(name, app.Settings().ThemeVariant()).RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

// 官方更新站点常量见 config.PrimaryUpdateSite（桌面与网页共用一份，REQ-UPD-023）。

// App GUI 应用：组装 Fyne 窗口与托盘，订阅核心事件驱动界面。
// appDisplayName 用户可见的应用名（窗口标题、托盘提示/菜单、通知抬头）：
// 统一文案「MTI 软电话」（用户要求，2026-10-21）。
const appDisplayName = "MTI 软电话"

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
	// callPanelRows 通话面板当前的行数（3 = 两方通话/拨号/振铃，4+ = 复杂话务）。
	// 记住它，重建面板时行数不同才 resize（用户实测：进过咨询后下次来电
	// 面板仍是 4 行高，内容却只有 3 行）。
	callPanelRows int

	// 主窗口：内容切换容器（拨号盘 / 设置页）
	mainContent  *fyne.Container
	dialPage     *fyne.Container
	settingsPage *fyne.Container

	// 拨号盘控件
	dialEntry  *widget.Entry // 已废弃：由 dialSelect 替代（保留字段避免大改）
	dialSelect *dialEntry    // 拨号输入框（普通 Entry：没有下拉箭头，建议走内容图层）

	// 按键音出口（测试可替换；nil = 走音频管理器）
	onKeyTone func(key string)

	regDot        *statusDot
	regLabel      *plainText // 注册状态文字（颜色跟圆点一致；本机号码见窗口标题）
	autoAnswerBtn *hoverIcon // 自动接听开关：图形按钮（icons/自动接听开启.png）
	callBtn       *compactButton
	settingsBtn   *widget.Button
	contactsBtn   *widget.Button // 通信录（REQ-UI-032，位于通话记录按钮右侧）
	historyBtn    *widget.Button // 通话记录（REQ-UI-031，位于设置按钮左侧）
	// 禁用态图标：PNG 图标 Fyne 不会自动变灰（只处理 SVG），禁用时手动换上
	// 这一份，观感才与设置按钮（齿轮，SVG）一致，见 setIconButtonEnabled。
	contactsIconOff fyne.Resource
	historyIconOff  fyne.Resource

	// ---- 通话记录（REQ-CALL-007，与配置页一样切在主窗体内）----
	historyPage *fyne.Container // 记录页内容（buildHistoryPage 构建）
	history     *historyState   // 列表状态（见 ui_history.go）

	// ---- 通信录（REQ-UI-032，同样切在主窗体内）----
	contactsPage *fyne.Container   // 通信录页内容（buildContactsPage 构建）
	contacts     *contactsState    // 页面状态（见 ui_contacts.go）
	agentsPage   *fyne.Container   // 在线座席页内容（buildAgentsPage 构建，REQ-ACC-035）
	agents       *agentsState      // 在线座席页状态（见 ui_agents.go）
	mainTip      *tooltipLayer     // 备注悬停提示图层（主窗体内容顶层，见 ui_tooltip.go）
	mainSuggest  *dialSuggestLayer // 拨号建议下拉图层（见 ui_dial_suggest.go）
	// hideSuggestTimer 输入框失焦后"延迟收起"下拉的定时器（点选建议需要这点时间）
	hideSuggestTimer *time.Timer
	callTip          *tooltipLayer // 通话面板的提示图层

	// 设置页控件
	settingsScroll    *container.Scroll
	accServerAddr     *widget.Entry // SIP 服务器（含端口，如 10.168.4.40:15060）
	accExpires        *widget.Entry // 注册到期时间（秒）
	accCTIHeartbeat   *widget.Entry // CTI 心跳间隔（秒，坐席模式）
	accProxy          *widget.Entry // 出站代理（可选）
	accStunCheck      *widget.Check
	accStunServer     *widget.Entry
	rportCheck        *widget.Check
	accTransport      *widget.Select
	tlsVerifyCheck    *widget.Check // 校验 TLS 服务器证书（REQ-ACC-006）
	tlsNote           *widget.Label // 「只加密信令」提示（仅传输=tls 时显示）
	accSRTP           *widget.Select
	srtpNote          *widget.Label // 媒体加密（SDES-SRTP，REQ-MEDIA-009）说明
	accDomain         *widget.Entry // SIP 域名（REQ-ACC-022，留空用服务器地址）
	accIPSelect       *widget.SelectEntry
	accAccount        *widget.Entry
	accAuthUser       *widget.Entry
	accPassword       *widget.Entry
	accDisplay        *widget.Entry
	accEnabled        *widget.Check
	micSelect         *widget.Select
	speakerSelect     *widget.Select
	aecCheck          *widget.Check // 音频预处理：回声消除（REQ-MEDIA-007）
	nsCheck           *widget.Check // 降噪
	agcCheck          *widget.Check // 自动增益
	preprocessHint    *widget.Label
	aecDesc           *widget.Label // 三个开关下面各自的说明（常驻）
	nsDesc            *widget.Label
	agcDesc           *widget.Label
	ringSpeakerSelect *widget.Select
	logFileCheck      *widget.Check
	sipTraceCheck     *widget.Check
	autoStartCheck    *widget.Check // 开机自动启动（REQ-UI-022）
	logLevelSel       *widget.Select
	logDirLabel       *widget.Label
	wsPortEntry       *widget.Entry // 本地 WS 监听端口（默认 8765，修改后重启生效）
	wsRemoteCheck     *widget.Check // 允许远程连接（监听 0.0.0.0，重启后生效；默认关）

	// 自动更新（REQ-UPD-017）：软电话自己按周期查更新站点
	updSiteEntry     *widget.Entry // 更新站点地址，空 = 不检查
	updAppIDEntry    *widget.Entry // 应用 ID
	updIntervalEntry *widget.Entry // 检查周期（分钟）
	updPrereleaseChk *widget.Check // 是否接受预览版
	updStatusLabel   *widget.Label // 最近一次检查结果/说明

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

	// 窗口拖动（REQ-UI-040）：面板顶部的拖动条（抓手）
	callGrip     *callGrip
	callDragging bool // 本次拖动会话是否已开始（DragEnd 前不再重复 begin）

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

	// ---- CTI 话务控制（REQ-ACC-026 / REQ-ACC-029）----
	// agentOnline 坐席模式（MSP_EVENT_AGENT_ONLINE/OFFLINE，REQ-ACC-028）：
	// **只有坐席模式才在通话面板显示高级话务按钮**（咨询/转移/会议）。
	agentOnline      bool
	agentMode        string          // none / normal / dial_out / monitor（协议 §16.1）
	agentBusy        bool            // 置忙（isBusy）：已注册时托盘/应用图标变黄
	agentDisplayName string          // 坐席显示名（AGENT_ONLINE 的 displayName）
	agentIcon        *hoverIcon      // 主窗体「已注册」后的坐席按钮（点击切换置忙/置闲）
	rosterIcon       *hoverIcon      // 坐席模式下的「在线座席」入口（座位表图标）
	statusRow        *fyne.Container // 顶栏「圆点 + 已注册 + 坐席/监听按钮」（图标显隐后要重排）
	// ctiDialMode 主窗体拨号盘被"咨询/转移/会议加人"复用的模式：""（普通拨号）/
	// "consult"（发起咨询+发起三方）/ "transfer"（直接转移+咨询转移）/ "conference"（加人）。
	ctiDialMode string
	// ctiCallBtn2 拨号复用模式的第二个动作按钮（如"发起三方"/"咨询转移"）。
	ctiCallBtn2 *compactButton
	// 两方通话第一行的三个入口按钮（咨询/转移/会议，位于保持左侧）
	consultBtn    *compactButton
	transferBtn   *compactButton
	conferenceBtn *compactButton
	// 咨询接通后的第一行按钮
	threeWayBtn        *compactButton // 三方
	consultTransferBtn *compactButton // 咨询转移
	finishConsultBtn   *compactButton // 结束咨询（被咨询方行右侧）
	exitThreeWayBtn    *compactButton // 三方：发起方强制挂断一方（X）
	// 监听第一行按钮
	whisperBtn   *compactButton // 耳语（耳语态时改为「监听」= 退出耳语）
	interceptBtn *compactButton // 拦截
	interveneBtn *compactButton // 强插（三方）
	// monitorWhisper 当前监听是否处于耳语态：决定 whisperBtn 的动作
	// （whisper / stopWhisper）与悬停提示（耳语 / 监听）。
	monitorWhisper bool
	// 会议按钮
	addMemberBtn        *compactButton // 添加成员（owner，结束会议左侧）
	finishConferenceBtn *compactButton // 结束会议（owner）

	// CTI 指令在途（REQ-ACC-026）：命令发出后锁住（协议只允许一条在途），
	// 点下去的那个按钮换成旋转圆圈（没有对应按钮时只锁不转），收到
	// MSP_RESULT（cti.result，含超时）后恢复；期间点其它话务按钮只提示。
	ctiInFlight   bool
	ctiPending    *compactButton
	ctiPendingRes fyne.Resource
	ctiSpinStop   chan struct{}
	ctiSpinAt     time.Time // 转圈开始时刻：结果来得太快时保证至少显示一小会儿

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
	callRow        *fyne.Container // 主窗体「呼叫 + 视频呼叫」按钮行（视频按钮显隐要重排）
	videoCallBtn   *compactButton  // 主窗体「视频呼叫」按钮（需求 3）
	videoAnswerBtn *compactButton  // 来电弹窗「视频接听」按钮（需求 4）
	videoBtn       *compactButton  // 通话面板「关闭视频」开关（需求 8）
	videoCallCheck *widget.Check   // 设置页「启用视频通话」总开关（REQ-VIDEO-014）
	camSelect      *widget.Select  // 设置页摄像头选择（需求 2）
	camHint        *widget.Label   // 摄像头说明行（本构建不支持时改成提示文字）
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
	videoSizedW     int           // 上次据此定尺寸的对端画面宽（分辨率变化时要重新适配）
	videoSizedH     int           // 同上，高
	videoFullscreen bool
	webVideoOn      bool // 有网页正在接收视频画面（REQ-VIDEO-009，此时不显示视频窗口）
	ringbackOn      bool // 正在放本地回铃音（没有早期媒体时自己放，避免重复放）
	videoTopmost    bool // 视频窗口是否被用户置顶（REQ-VIDEO-015：已持久化到配置）
	videoShown      bool // 视频窗口当前是否显示（Fyne 无 Visible() 查询 API，只能自己记）

	// 视频窗口的锁（REQ-VIDEO-015）：默认锁上（不能移动/改大小），点锁按钮
	// 解锁后可拖动。**会话内记忆、不持久化**（用户确认）：通话结束/窗口隐藏
	// 都不复位，程序重启回到默认「锁上」。
	//（通话面板没有锁：顶部拖动条随时可拖，见 REQ-UI-040）
	videoLocked bool

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
		// 视频窗口的锁：默认锁上（会话内记忆，不持久化，REQ-VIDEO-015）
		videoLocked: true,
	}
	// 视频窗口置顶与本地回显的旋转/镜像从配置恢复（REQ-VIDEO-015）。
	// 变换的权威状态在 core（桌面与网页共用）：这里播种，网页连上时
	// system.state 里的 video 字段就与桌面一致；**远端**变换按用户要求
	// 不持久化，显式归零。
	if cfg != nil {
		ui.videoTopmost = cfg.UI.VideoTopmost
		c.SetVideoRotation(core.VideoChannelLocal, cfg.UI.LocalPreviewRotation)
		c.SetVideoMirror(core.VideoChannelLocal, cfg.UI.LocalPreviewMirror)
		c.SetVideoRotation(core.VideoChannelRemote, 0)
		c.SetVideoMirror(core.VideoChannelRemote, false)
	}
	if desk, ok := ui.fyne.(desktop.App); ok {
		ui.desk = desk
	}
	// 坐席模式初值（MSP_EVENT_AGENT_ONLINE/OFFLINE，REQ-ACC-028）
	ui.agentOnline = c.AgentOnline()
	ui.agentDisplayName = c.AgentDisplayName()
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
	// **必须在 buildTray 之前**：托盘首份菜单（注册状态决定「登录账号/退出账号」）
	// 用它构建，而 Fyne 的就绪回调会拿这份菜单覆盖之后的更新。
	a.syncInitialState()
	a.buildTray()
	a.subscribeEvents()
	// 视频窗口"用户挪动了"回调（REQ-VIDEO-015）：Windows 是窗口过程的
	// WM_EXITSIZEMOVE、Linux 是守护 goroutine 的轮询，都可能不在主线程，
	// 统一 fyne.Do 回主线程再写配置。
	SetVideoWindowMoveHook(func(x, y int) {
		fyne.Do(func() { a.onVideoWindowUserMoved(x, y) })
	})
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
	w := a.fyne.NewWindow(appDisplayName)
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
	a.watchThemeChanges()

	// 顶层提示图层（备注/说明悬停用，见 ui_tooltip.go）**必须先建**：设置页的
	// ⓘ 图标（REQ-MEDIA-007）在构建时就把它捕获进去了，晚建会拿到 nil。
	a.mainTip = newTooltipLayer()
	a.mainSuggest = newDialSuggestLayer()
	a.buildDialPage()
	a.buildSettingsPage()
	// 音频预处理开关按配置回填（REQ-MEDIA-007；本构建没有 APM 时置灰）
	a.applyPreprocessForm()
	a.buildHistoryPage()
	a.buildContactsPage()
	a.buildAgentsPage()

	// 窗口内容 = 当前页 + 顶层提示图层（备注悬停用，见 ui_tooltip.go）。
	// 切页只换 Objects[0]，提示图层恒为 Objects[1]：它不含 Hoverable/Tappable，
	// 命中测试会穿透到下面的页面，绘制顺序上又在最上层、不受滚动裁剪。
	// 图层顺序：页面 → 拨号建议下拉 → 备注提示（提示永远在最上层）
	a.mainContent = container.NewStack(a.dialPage, a.mainSuggest, a.mainTip)
	w.SetContent(a.mainContent)
	w.Resize(fyne.NewSize(320, 620)) // 更窄的宽度 + 按键增高后的总高
	w.CenterOnScreen()
	// 固定窗口尺寸：切换设置页（内容多）时窗口不被撑大
	w.SetFixedSize(true)
	// 跨屏 DPI 修复（多显示器缩放不同时，用户实测）：窗口在缩放不同的显示器间
	// 移动时 Fyne（2.8.1）的 canvas scale 会变化（实测 200%↔150% 屏 → 2.0↔1.5），
	// 但**固定尺寸窗口的物理尺寸与画布重排不同步**——内容整体偏移（现象：移到
	// 另一块屏后内容挤在窗口下半部）。**注意 `Settings().Scale()` 在"自动"模式
	// 下恒为 0**（真实缩放在 `Canvas().Scale()`，也没有变化通知可监听），所以
	// 这里每秒轻量轮询 canvas scale：**首轮无条件重设一次**（纠正"窗口创建早于
	// scale 检测"导致的初始物理尺寸错误），之后只在真正变化时重设——先解固定
	// 再重设，逼 Fyne 按新 scale 重算窗口物理尺寸与画布布局。
	go func() {
		first := true
		var last float32
		for {
			time.Sleep(time.Second)
			fyne.Do(func() {
				s := w.Canvas().Scale()
				if !first && s == last {
					return
				}
				first = false
				last = s
				w.SetFixedSize(false)
				w.Resize(fyne.NewSize(320, 620))
				w.SetFixedSize(true)
			})
		}
	}()

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
			// CTI 拨号复用模式（咨询/转移/加会）：数字输入、退格删除、回车发起
			if a.ctiDialMode != "" {
				switch {
				case ev.Name == fyne.KeyBackspace:
					runes := []rune(a.dialSelect.Text)
					if len(runes) > 0 {
						a.dialSelect.SetText(string(runes[:len(runes)-1]))
					}
				case ev.Name == fyne.KeyEnter || ev.Name == fyne.KeyReturn:
					a.onCallButton()
				case isDialDigit(ev.Name):
					a.appendDialKey(string(ev.Name))
					a.onKeypadDigit(string(ev.Name))
				}
				return
			}
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
	// CTI 拨号复用模式：只输入、不发 DTMF（REQ-ACC-026 第 2 点"不再发 DTMP"）
	if a.ctiDialMode != "" {
		return
	}
	if a.uiState == uiInCall && a.activeCallID != "" {
		if err := a.core.SendDTMF(a.activeCallID, key); err != nil {
			logx.Logger().Debug("发送 DTMF 失败", "digit", key, "err", err)
		}
	}
}

// dialCallRowLayout 主窗体的呼叫行：
//   - 普通拨号：[呼叫按钮（占满）] [视频按钮（可选，靠右）]
//   - 拨号复用（咨询/转移）：[第一动作][第二动作] **两个等宽按钮并排**
//
// 用自定义布局而不是 Border/VBox：第二动作按钮显隐时要重新分配主按钮的宽度。
type dialCallRowLayout struct{}

func (dialCallRowLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(160, dialCallButtonHeight)
}

func (dialCallRowLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	call, alt, video := objs[0], objs[1], objs[2]
	if alt.Visible() {
		w := (size.Width - panelColGap) / 2
		call.Resize(fyne.NewSize(w, size.Height))
		call.Move(fyne.NewPos(0, 0))
		alt.Resize(fyne.NewSize(size.Width-w-panelColGap, size.Height))
		alt.Move(fyne.NewPos(w+panelColGap, 0))
		return
	}
	vw := float32(0)
	if video.Visible() {
		vw = video.MinSize().Width + panelColGap
	}
	call.Resize(fyne.NewSize(size.Width-vw, size.Height))
	call.Move(fyne.NewPos(0, 0))
	if video.Visible() {
		m := video.MinSize()
		video.Resize(m)
		video.Move(fyne.NewPos(size.Width-m.Width, (size.Height-m.Height)/2))
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
	// 自动接听：**图形按钮**（icons/自动接听开启.png），文字走悬停提示（用户要求）。
	// 图标比坐席/监听那排小图标大一号（用户要求：按钮太小），命中区随之放大。
	iconSize := fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText) + 2
	autoAnswerSize := fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText) + 10
	a.autoAnswerBtn = newHoverIcon(IconAutoAnswerOn, autoAnswerSize, a.mainTip)
	a.autoAnswerBtn.SetOnTap(a.toggleAutoAnswer)
	a.autoAnswerBtn.SetDisabledResource(tintIcon(IconAutoAnswerOn, disabledIconColor()))
	a.autoAnswerBtn.SetIconOffsetY(2) // 图案在按钮里下移 2px（按钮位置不变，用户要求）
	a.setAutoAnswer(a.autoAnswer)
	// 坐席按钮（REQ-ACC-029）：icons/座席.png，**绿色 = 空闲、黄色 = 置忙**；
	// 非坐席模式（agentMode=none）隐藏。点击切换置忙/置闲（setBusy/setIdle），
	// 悬停提示显示坐席名与当前状态。
	a.agentIcon = newHoverIcon(tintIcon(IconAgentSeat, colorSuccessGreen), iconSize, a.mainTip)
	a.agentIcon.SetOnTap(a.toggleAgentBusy)
	a.agentIcon.Hide()
	// 在线座席入口（REQ-ACC-035）：坐席模式下显示在坐席按钮右侧，点击打开
	// 在线座席页（listAgents）。
	a.rosterIcon = newHoverIcon(tintIcon(IconAgentRoster, colorHoldBlue), iconSize+2, a.mainTip)
	a.rosterIcon.SetOnTap(a.showAgents)
	a.rosterIcon.SetNote("在线座席")
	a.rosterIcon.Hide()
	statusRow := fyne.NewContainerWithLayout(statusRowLayout{}, a.regDot, a.regLabel,
		a.agentIcon, a.rosterIcon)
	a.statusRow = statusRow
	// 顶栏图标按钮统一用**标准 Fyne 按钮**（低强调）：通话记录在左、通信录居中、
	// 设置在右（REQ-UI-031 / REQ-UI-032）。自绘紧凑版观感与其它按钮不一致，已回退。
	a.settingsBtn = widget.NewButtonWithIcon("", theme.SettingsIcon(), a.showSettings)
	a.settingsBtn.Importance = widget.LowImportance
	a.historyBtn = widget.NewButtonWithIcon("", IconHistory, a.showHistory)
	a.historyBtn.Importance = widget.LowImportance
	a.contactsBtn = widget.NewButtonWithIcon("",
		IconContacts, a.showContacts)
	a.contactsBtn.Importance = widget.LowImportance
	// 禁用态图标（灰色）：Fyne 只给 SVG 图标做禁用变灰，PNG 得自己备一份
	a.historyIconOff = disabledPNG("history", 0.8)
	a.contactsIconOff = disabledPNG("contacts", 0.8)

	// 顶栏：圆点+状态靠左，通话记录+通信录+设置按钮靠右，自动接听按钮居中。
	// 右侧三个图标按钮的内边距（用户要求，先后试过 2px、定为 6px）：视觉空隙
	// 几乎全部来自按钮自身的 SizeNameInnerPadding——本项目主题为拨号盘大按键
	// 把它提到 12，**光调容器间距几乎看不出变化**（实测）。这里用**局部主题
	// 覆盖**只把这三个按钮的 InnerPadding 压到 6，容器间距用 0；全局主题
	// （拨号盘等）不受影响。
	iconBtns := container.NewThemeOverride(
		container.New(layout.NewCustomPaddedHBoxLayout(0),
			a.historyBtn, a.contactsBtn, a.settingsBtn),
		compactIconButtonsTheme{pad: 6})
	topBar := container.NewBorder(nil, nil, statusRow, iconBtns,
		container.NewCenter(a.autoAnswerBtn))

	// ---- 号码输入框：可下拉（最近 5 次呼叫号码，去重）----
	a.dialEntry = newFormEntry()
	a.dialEntry.SetPlaceHolder("输入号码或 sip:xxx@主机")
	a.dialEntry.TextStyle = fyne.TextStyle{Bold: true}
	a.dialSelect = newDialEntry(a)
	a.dialSelect.SetPlaceHolder("输入号码或名称（支持拼音/首字母）")
	// 注意：**输入框里敲数字不回显按键音、也不发 DTMF**（用户明确要求）——
	// 按键音只属于"点号码盘"与"未聚焦时在主窗体敲数字"这两条路径。
	a.dialSelect.Wrapping = fyne.TextWrapOff
	a.dialSelect.Scroll = fyne.ScrollNone
	// 回车直接呼出（REQ-UI-024）
	a.dialSelect.OnSubmitted = func(string) {
		a.onCallButton()
	}
	// 边打边查（与通信录搜索同一套：拼音/音头/汉字/号码/备注）：**任何改动内容的
	// 路径都会走到这里**——直接敲字、点击拨号按键、主窗体按键（都走 SetText），
	// 结果和"最近呼叫"一起出现在输入框下方的下拉图层里（REQ-UI-024 修订）
	a.wireDialInput()

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
				if a.dialInputMode() {
					a.appendDialKey(key)
				}
				// 点号码盘一律回显按键音；通话中还会向对端发 DTMF
				//（REQ-UI-029 / REQ-CALL-006）；呼出/振铃中按键是禁用的。
				// CTI 拨号复用模式下只输入、不发 DTMF（onKeypadDigit 内部拦）。
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
	a.callBtn = newCompactTextButton("呼叫", IconPhoneCallWhite, colorSuccessGreen, colorIconWhite, a.onCallButton)
	// REQ-VIDEO-003：呼叫按钮缩小，右侧加一个无文字的视频呼叫按钮
	// （用户要求：蓝底白图标，与「呼叫」同一套配色）
	a.callBtn.SetMinSize(0, dialCallButtonHeight)
	a.videoCallBtn = newCompactButton(IconVideoCallWhite, colorCallGreen, dialCallButtonHeight, a.onVideoCallButton)
	// REQ-ACC-026：拨号复用模式的第二个动作按钮（咨询/转移/加会时显示，默认隐藏）
	a.ctiCallBtn2 = newCompactTextButton("", IconPlayBlack, colorCallGreen, colorIconWhite, a.onCTICallButton2)
	a.ctiCallBtn2.SetMinSize(0, dialCallButtonHeight)
	a.ctiCallBtn2.Hide()

	// 呼叫按钮上方一行：版本更新提示（REQ-UPD-003，默认隐藏）
	a.buildUpdateBar()

	// 呼叫按钮占满剩余宽度，视频按钮固定宽停在右侧（Border 布局跳过不可见
	// 对象，视频按钮隐藏时呼叫按钮自然恢复占满）。视频入口的显隐由
	// applyVideoFeatureUI 统一维护：本构建没有视频、或配置里关掉了视频通话
	//（REQ-VIDEO-014）都隐藏。
	// 呼叫行：平时 [呼叫][视频]；拨号复用模式（咨询/转移）时换成
	// [第一动作][第二动作] 两个**等宽并排**的按钮（用户要求，蓝底白字）。
	a.callRow = fyne.NewContainerWithLayout(dialCallRowLayout{}, a.callBtn, a.ctiCallBtn2, a.videoCallBtn)
	if !a.core.VideoUsable() {
		a.videoCallBtn.Hide()
	}

	a.dialPage = container.NewBorder(
		topBar,
		container.NewVBox(a.updateBar, a.callRow),
		nil, nil,
		container.NewVBox(
			inputRow,
			padGrid,
		),
	)
	// 空闲态按钮外观（蓝底白字）由 updateCallButton 统一决定：不在这里刷一次的话，
	// 启动后要等第一通电话才变色（按钮构造函数里的配色是旧的绿底黑字）。
	a.updateCallButton()
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
	// CTI 心跳（协议 §10）：坐席模式下每 N 秒发一次 MSP_HEARTBEAT，
	// expires = N × 2.5。默认 20 秒。
	a.accCTIHeartbeat = newFormEntry()
	a.accCTIHeartbeat.SetPlaceHolder("20")
	a.accTransport = widget.NewSelect([]string{"udp", "tcp", "tls"}, func(sel string) {
		if a.loadingForm {
			return
		}
		a.updateTLSRow(sel)
	})
	// TLS（REQ-ACC-006）：只加密信令，媒体是否加密由下面的「媒体加密」决定。
	// 提示**保持一行短句**：主窗体固定 320 DIP，长句会被折在难读的位置
	//（用户两次反馈"说明行太长"，两处提示都有回归测试卡着）。
	a.tlsVerifyCheck = widget.NewCheck("校验服务器证书（自签证书请勿勾选）", nil)
	a.tlsNote = widget.NewLabelWithStyle(tlsNoteText, fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	// 媒体加密（SDES-SRTP，REQ-MEDIA-009）
	a.accSRTP = widget.NewSelect(srtpLabels, nil)
	a.srtpNote = widget.NewLabelWithStyle(srtpHint, fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	a.srtpNote.Wrapping = fyne.TextWrapWord
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
	// 服务端心跳检测（optchk）已按用户要求取消：设置项、User-Agent 标记与
	// 超时重注册一并移除（REQ-ACC-020/021 废弃）。

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

	// 域名（REQ-ACC-022）：From/To/R-URI 里的域，可留空（留空 = 用 SIP 服务器地址）
	a.accDomain = newFormEntry()
	a.accDomain.SetPlaceHolder("可选，留空用 SIP 服务器地址")

	// 声明 IP（REQ-ACC-023）：可输入的下拉（与主窗体拨号框同款）——候选是本机
	// 可用网卡 IP（已滤掉回环与虚拟网卡，公网地址排在前面），也可以直接手填。
	// **不填 = 自动**（STUN → 本机出口 IP）——标题 + 占位提示已说清，不再单独加说明行。
	a.accIPSelect = widget.NewSelectEntry(sip.LocalIPv4s())
	a.accIPSelect.SetPlaceHolder("不填时自动（STUN/本机出口 IP）")
	a.accIPSelect.Wrapping = fyne.TextWrapOff
	a.accIPSelect.Scroll = fyne.ScrollNone

	accCard := container.NewVBox(
		widget.NewLabelWithStyle("SIP 账户", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		a.accEnabled,
		formItem("SIP服务器", a.accServerAddr),
		formItem("代理服务器", a.accProxy),
		formItem("域名", a.accDomain),
		formItem("传输", a.accTransport),
		a.tlsVerifyCheck,
		a.tlsNote,
		formItem("媒体加密", a.accSRTP),
		a.srtpNote,
		formItem("账号", a.accAccount),
		formItem("认证 ID", a.accAuthUser),
		formItem("密码", a.accPassword),
		formItem("显示名", a.accDisplay),
		formItem("注册到期时间（秒）", a.accExpires),
		formItem("CTI 心跳间隔（秒）", a.accCTIHeartbeat),
		a.rportCheck,
		formItem("声明 IP（Via/Contact/SDP）", a.accIPSelect),
		a.accStunCheck,
		formItem("STUN 服务器", a.accStunServer),
		saveBtn,
	)

	// ---- 音频设备 ----
	a.micSelect = widget.NewSelect([]string{}, nil)
	a.speakerSelect = widget.NewSelect([]string{}, nil)
	a.ringSpeakerSelect = widget.NewSelect([]string{}, nil)
	refreshDevicesBtn := widget.NewButtonWithIcon("刷新设备", theme.ViewRefreshIcon(), a.refreshDevices)

	// 音频预处理（REQ-MEDIA-007）：说明文字要自动折行（主窗体固定宽度，
	// 不折行会把窗口撑宽——与摄像头说明行同样的处理）。
	a.aecCheck = widget.NewCheck("回声消除（AEC）", a.onPreprocessChanged)
	a.nsCheck = widget.NewCheck("降噪（NS）", a.onPreprocessChanged)
	a.agcCheck = widget.NewCheck("自动增益（AGC）", a.onPreprocessChanged)
	// 每条说明常驻显示在对应勾选项下面（自动折行：主窗体固定 320 宽）
	a.aecDesc = widget.NewLabel(preprocessAECNote)
	a.aecDesc.Wrapping = fyne.TextWrapWord
	a.nsDesc = widget.NewLabel(preprocessNSNote)
	a.nsDesc.Wrapping = fyne.TextWrapWord
	a.agcDesc = widget.NewLabel(preprocessAGCNote)
	a.agcDesc.Wrapping = fyne.TextWrapWord
	a.preprocessHint = widget.NewLabel(preprocessUnsupportedText)
	a.preprocessHint.Wrapping = fyne.TextWrapWord
	a.preprocessHint.Hide() // 默认（支持时）不显示
	audioCard := container.NewVBox(
		widget.NewLabelWithStyle("音频设备", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		formItem("麦克风", a.micSelect),
		formItem("扬声器", a.speakerSelect),
		formItem("振铃设备", a.ringSpeakerSelect),
		widget.NewLabel("振铃音专用播放设备，可选其他扬声器"),
		refreshDevicesBtn,
		// 音频预处理（REQ-MEDIA-007）：三个开关，改动立即保存、**下一通电话生效**
		//（音频流在每次通话开始时重建）。
		widget.NewLabelWithStyle("音频预处理", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		a.aecCheck,
		a.aecDesc,
		a.nsCheck,
		a.nsDesc,
		a.agcCheck,
		a.agcDesc,
		a.preprocessHint,
	)

	// ---- 视频设备（REQ-VIDEO-002/005/014）----
	// 总开关「启用视频通话」（默认勾选）：取消后视频功能整体隐藏——主窗体的
	// 视频呼叫按钮、通话记录/通信录行内的视频按钮都不显示，来电不显示
	// 「视频接听」，SDP 不含 m=video，网页订阅视频返回失败。
	a.videoCallCheck = widget.NewCheck("启用视频通话", a.onVideoCallEnabledChanged)
	a.camSelect = widget.NewSelect([]string{}, nil)
	a.camIDs = make(map[string]string)
	refreshCamBtn := widget.NewButtonWithIcon("刷新设备", theme.ViewRefreshIcon(), a.refreshDevices)
	// 说明文字必须开自动折行：主窗体是固定宽度，标签的 MinSize 按整行文字宽度
	// 计算会把窗口撑宽（与「网页接入」的 wsHint 同样处理）。
	a.camHint = widget.NewLabel(cameraHintDefault)
	a.camHint.Wrapping = fyne.TextWrapWord
	videoCard := container.NewVBox(
		widget.NewLabelWithStyle("视频设备", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		a.videoCallCheck,
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
	// 多实例（沙箱槽位，REQ-NFR-007）没有配置开关：槽位只能由启动参数/
	// sip:// 链接显式指定（不带就是主实例），不存在"忘了关"的风险，故不放配置项。
	generalCard := container.NewVBox(
		widget.NewLabelWithStyle("常规", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		a.autoStartCheck,
	)

	// ---- 自动更新（REQ-UPD-017）----
	// 软电话自己按周期查询更新站点；站点**留空 = 不检查**，所以不默认填写
	//（「使用主站点」按钮一键填入官方地址）。网页也可经 WS
	// system.setUpdateConfig 写这份配置（业务前端统一管理更新策略）。
	a.updSiteEntry = newFormEntry()
	// 「使用主站点」：**小按钮**（自绘 compact 按钮——Fyne 标准按钮高度跟随主题
	// 内边距，在表单里显得过大），与「更新站点」标签同一排、右对齐；输入框仍是
	// 与其他输入项一样的整行长度
	updSiteBtn := newCompactTextOnlyButton("使用主站点",
		themeColorNRGBA(theme.ColorNameButton), themeColorNRGBA(theme.ColorNameForeground),
		func() { a.updSiteEntry.SetText(config.PrimaryUpdateSite) })
	updSiteBtn.label.TextSize = 12 // 比表单正文小一号，"小按钮"
	border := themeColorNRGBA(theme.ColorNameInputBorder)
	updSiteBtn.bg.StrokeColor = &border
	updSiteBtn.bg.StrokeWidth = 1
	// 行内布局用 panelRowLayout（首元素靠左、其余靠右且**保持自然尺寸**）：
	// container.NewBorder 会把右侧子项拉满行高，小按钮会被撑大
	updSiteItem := container.NewVBox(
		container.New(panelRowLayout{}, widget.NewLabel("更新站点"), updSiteBtn),
		container.NewHScroll(a.updSiteEntry),
	)
	a.updAppIDEntry = newFormEntry()
	a.updIntervalEntry = newFormEntry()
	a.updIntervalEntry.SetPlaceHolder("1–1440，默认 5")
	a.updPrereleaseChk = widget.NewCheck("接受预览版", nil)
	a.updStatusLabel = widget.NewLabel("")
	a.updStatusLabel.Wrapping = fyne.TextWrapWord

	updSaveBtn := widget.NewButtonWithIcon("保存并检查", theme.ConfirmIcon(), a.saveUpdateConfig)
	updCheckBtn := widget.NewButton("立即检查", a.checkUpdateNow)

	updateCard := container.NewVBox(
		widget.NewLabelWithStyle("自动更新", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		updSiteItem,
		formItem("应用 ID", a.updAppIDEntry),
		formItem("检查周期（分钟）", a.updIntervalEntry),
		a.updPrereleaseChk,
		container.NewHBox(updCheckBtn, updSaveBtn),
		a.updStatusLabel,
	)

	// ---- 网页接入（WS 服务端口，无 token 鉴权）----
	a.wsPortEntry = newFormEntry()
	a.wsPortEntry.SetText(strconv.Itoa(a.cfg.WS.Port))
	a.wsRemoteCheck = widget.NewCheck("允许远程连接（只在受信任网络使用）", func(on bool) {
		a.applyWSAllowRemote(on)
	})
	a.wsRemoteCheck.SetChecked(a.cfg.WS.AllowRemote)
	wsHint := widget.NewLabel("本地网页控制服务端口（默认 8765，占用时自动 +1；修改后需重启生效）")
	wsHint.Wrapping = fyne.TextWrapWord

	wsCard := container.NewVBox(
		widget.NewLabelWithStyle("网页接入（WS 端口）", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		a.wsPortEntry,
		a.wsRemoteCheck,
		wsHint,
	)

	// 滚动容器容纳全部设置项；右侧留 16px 空白避免滚动条遮住输入框
	// 「常规」（开机自动启动）置于最上方
	// 自动更新卡片放在**最下面**（更新配置是偶尔改一次的东西，不占前面的位置）
	scrollContent := container.NewVBox(
		generalCard, widget.NewSeparator(),
		accCard, widget.NewSeparator(),
		audioCard, widget.NewSeparator(),
		videoCard, widget.NewSeparator(),
		logCard, widget.NewSeparator(),
		wsCard, widget.NewSeparator(),
		updateCard,
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
	a.loadUpdateConfigIntoForm()
	a.refreshDevices()
	a.refreshCameras()
}

// loadUpdateConfigIntoForm 把自动更新配置回填到表单（REQ-UPD-017）。
func (a *App) loadUpdateConfigIntoForm() {
	if a.updSiteEntry == nil {
		return
	}
	a.loadingForm = true
	defer func() { a.loadingForm = false }()
	cfg := a.cfg.Update
	a.updSiteEntry.SetText(cfg.URL)
	a.updAppIDEntry.SetText(cfg.AppID)
	a.updIntervalEntry.SetText(strconv.Itoa(config.ClampUpdateIntervalMins(cfg.IntervalMins)))
	a.updPrereleaseChk.SetChecked(cfg.Prerelease)
	a.setUpdateStatusText("")
}

// setUpdateStatusText 设置"自动更新"卡片底部的状态文字（空 = 只显示说明）。
func (a *App) setUpdateStatusText(s string) {
	if a.updStatusLabel == nil {
		return
	}
	if s == "" && a.cfg.Update.Enabled() {
		s = fmt.Sprintf("已配置：每 %d 分钟检查一次",
			config.ClampUpdateIntervalMins(a.cfg.Update.IntervalMins))
	}
	a.updStatusLabel.SetText(s)
}

// saveUpdateConfig 保存自动更新配置并立即检查一次（配了站点才会查）。
// 校验口径与 WS 侧一致（core.SetUpdateConfig 里做），这里只做输入框层面的回填。
func (a *App) saveUpdateConfig() {
	if a.loadingForm {
		return
	}
	site := strings.TrimSpace(a.updSiteEntry.Text)
	appID := strings.TrimSpace(a.updAppIDEntry.Text)
	interval, err := strconv.Atoi(strings.TrimSpace(a.updIntervalEntry.Text))
	if err != nil || interval <= 0 {
		interval = config.ClampUpdateIntervalMins(a.cfg.Update.IntervalMins)
	}
	cfg := config.UpdateConfig{
		URL:          site,
		AppID:        appID,
		IntervalMins: interval,
		Prerelease:   a.updPrereleaseChk.Checked,
	}
	if err := a.core.SetUpdateConfig(cfg); err != nil {
		a.showError(err)
		return
	}
	// 回到钳制后的真实值（周期越界时给用户看见实际生效值）
	a.loadUpdateConfigIntoForm()
	if site == "" {
		a.setUpdateStatusText("已保存：更新站点为空，不自动检查")
	} else {
		a.checkUpdateNow()
	}
}

// checkUpdateNow 立即检查一次更新（后台 goroutine 里发请求，结果回主线程显示）。
func (a *App) checkUpdateNow() {
	a.setUpdateStatusText("正在检查更新…")
	go func() {
		res, err := a.core.CheckUpdateNow(context.Background())
		fyne.Do(func() {
			if err != nil {
				a.setUpdateStatusText("检查失败：" + err.Error())
				return
			}
			a.setUpdateStatusText(res.Text())
		})
	}()
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

// updateTLSRow 传输方式联动（REQ-ACC-006）：只有「传输 = tls」时才显示
// 「校验服务器证书」与「只加密信令」提示——其余传输下这两项没有意义。
func (a *App) updateTLSRow(transport string) {
	if config.NormalizeTransport(transport) == config.TransportTLS {
		a.tlsVerifyCheck.Show()
		a.tlsNote.Show()
		return
	}
	a.tlsVerifyCheck.Hide()
	a.tlsNote.Hide()
}

// showSettings 切换到设置页（仅非通话状态可用，REQ-UI-012）。
func (a *App) showSettings() {
	if a.activeCallID != "" {
		a.showInfo("提示", "通话中无法进入设置")
		return
	}
	// 每次进入时刷新"允许远程连接"（网页配置页也能改它）
	if a.wsRemoteCheck != nil {
		a.wsRemoteCheck.SetChecked(a.cfg.WS.AllowRemote)
	}
	// 每次进入时刷新 WS 端口显示（端口可能在启动时被自动 +1 调整），
	// 自动更新配置同样刷新（网页可能刚用 system.setUpdateConfig 改过）
	if a.wsPortEntry != nil {
		a.wsPortEntry.SetText(strconv.Itoa(a.cfg.WS.Port))
	}
	a.loadUpdateConfigIntoForm()
	a.hideTip()         // 切页时收起备注提示（指针可能没离开行）
	a.hideDialSuggest() // 拨号建议下拉同理
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

// applyWSAllowRemote 勾选/取消"允许远程连接"：落盘 + 提示重启
// （监听地址在启动时绑定，改完必须重启才生效）。
func (a *App) applyWSAllowRemote(on bool) {
	if a.cfg.WS.AllowRemote == on {
		return
	}
	a.cfg.WS.AllowRemote = on
	a.persistConfig()
	logx.Logger().Info("网页接入：允许远程连接已修改", "allowRemote", on)
	msg := "已关闭「允许远程连接」：只监听 127.0.0.1。重启后生效。"
	if on {
		msg = "已开启「允许远程连接」：网页服务将监听 0.0.0.0，" +
			"同网段任何设备都能控制本机软电话（接口没有鉴权）。重启后生效。"
	}
	a.showConfirm("重启", msg+"是否立即重启？", func(ok bool) {
		if ok && a.onRestart != nil {
			a.onRestart()
		}
	})
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
	a.hideTip()         // 切页时收起备注提示
	a.hideDialSuggest() // 拨号建议下拉同理
	a.hideDialSuggest()
	a.mainContent.Objects[0] = a.dialPage
	a.mainContent.Refresh()
}

// onCallButton 呼叫按钮：无通话时拨号，呼叫/通话中挂断（REQ-UI-008）。
func (a *App) onCallButton() { a.onCallButtonNamed("") }

// onCallButtonNamed 呼叫按钮的实际实现：displayName 非空时作为对端显示名带进
// 呼叫（座席列表直接呼出用——优先于通信录名称）。
func (a *App) onCallButtonNamed(displayName string) {
	// CTI 拨号复用模式：第一动作（发起咨询 / 直接转移 / 加入会议）
	if a.ctiDialMode != "" {
		a.onCTICallButton()
		return
	}
	if a.activeCallID != "" {
		_ = a.core.Hangup(a.activeCallID)
		return
	}
	target := a.dialSelect.Text
	if target == "" {
		a.showInfo("提示", "请输入号码")
		return
	}
	var (
		callID string
		err    error
	)
	if displayName != "" {
		callID, err = a.core.DialNamed(context.Background(), target, displayName)
	} else {
		callID, err = a.core.Dial(context.Background(), target)
	}
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
		PeerName:  displayName,
		State:     core.CallRinging,
	}
	a.savedNumber = target
	a.setCallUI(uiDialing, a.activeCall)
	a.refreshRecentDials()
}

// onVideoCallButton 视频呼叫按钮（REQ-VIDEO-003）：无通话时发起视频呼叫，
// 呼叫/通话中与呼叫按钮一致地挂断。
func (a *App) onVideoCallButton() { a.onVideoCallButtonNamed("") }

// onVideoCallButtonNamed 视频呼叫按钮的实际实现（displayName 语义同
// onCallButtonNamed）。
func (a *App) onVideoCallButtonNamed(displayName string) {
	if a.activeCallID != "" {
		_ = a.core.Hangup(a.activeCallID)
		return
	}
	target := strings.TrimSpace(a.dialSelect.Text)
	if target == "" {
		a.showInfo("提示", "请输入号码")
		return
	}
	var (
		callID string
		err    error
	)
	if displayName != "" {
		callID, err = a.core.DialVideoNamed(context.Background(), target, displayName)
	} else {
		callID, err = a.core.DialVideo(context.Background(), target)
	}
	if err != nil {
		a.showError(err)
		return
	}
	a.activeCallID = callID
	a.activeCall = &core.Call{
		ID:        callID,
		Direction: "outgoing",
		Remote:    target,
		PeerName:  displayName,
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
	// 没有摄像头后端的构建（darwin 这类目标）也要算"不可用"：否则会显示
	//「视频接听」按钮（点了却没有本地画面）。视频总开关关闭（REQ-VIDEO-014）
	// 同样算不可用——此时整个视频功能都不该出现。
	if !a.core.VideoUsable() || !a.core.CameraSupported() {
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
	if state != uiIdle {
		a.hideDialSuggest() // 进通话（面板要显示）：拨号建议下拉先收起
	}
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
		// 挂断/隐藏时拖动可能没等到 DragEnd（面板藏了就不再收指针事件）：
		// 收掉拖动会话，免得窗口在后台被继续拖着走（REQ-UI-040）
		cancelCallPanelDrag(a.callW)
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
		a.hideDialSuggest() // 主窗体要藏起来了，建议下拉别留在画布上
		a.mainShown = false
		a.applyMainCallMode(true) // 通话中：主窗体只放开号码按键（REQ-UI-030）
		a.setKeypadEnabled(false) // 呼叫未接通，发不了 DTMF（REQ-CALL-006）
		a.buildCallPanelIfNeeded()
		a.updateCallWindowTitle()
		a.setCallName(call)
		a.callStateLabel.SetText("呼叫中")
		a.rebuildPanel(dialingButtons)
		showCallWindow(a.callW) // 平台函数：Windows 下不占任务栏（见 showWindowAsTool）
		a.placeCallWindow()
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
		a.rebuildPanel(ringingButtons)
		showCallWindow(a.callW)
		a.callW.RequestFocus()
		a.placeCallWindow()
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
		a.rebuildPanel(incallButtons)
		showCallWindow(a.callW)
		a.placeCallWindow()
		a.startTimer() // 通话时长从 0 开始
	}
	// 视频回显摆位随通话阶段变化（振铃期铺满窗口 / 接通后回右上角小窗），
	// 每次状态切换都按新口径同步一次（用户要求）。
	a.applyLocalVideoVisibility()
}

// placeCallWindow 摆通话面板（每次状态切换都调，REQ-UI-040）：
//   - 用户**移动过**（配置里有位置）→ 移到保存的位置，默认摆放不再覆盖；
//     保存的位置越界（拔屏/换分辨率）时记一条日志并回退默认；
//   - 没移动过 → 沿用原来的"工作区右下角 + 多实例列偏移"。
func (a *App) placeCallWindow() {
	if a.callW == nil {
		return
	}
	u := a.cfg.UI.CallPanel
	if u.Moved {
		scale := a.callW.Canvas().Scale()
		w := int(callPanelWidth * scale)
		h := int(callPanelHeight * scale)
		if sx, sy, sw, sh := screenBounds(); windowPosUsable(u.X, u.Y, w, h, sx, sy, sw, sh) {
			moveCallPanelToSaved(a.callW, u.X, u.Y)
			return
		}
		logx.Logger().Info("通话面板保存的位置已不在屏幕上，回退默认摆放", "x", u.X, "y", u.Y)
	}
	moveCallWindowToCorner(a.callW)
}

// ---- 多实例（沙箱槽位）的窗口排布（REQ-NFR-007）----
//
// 右下角窗口（通话面板、更新提示窗、视频窗口）默认都吸附同一个角，多实例时
// 会互相叠住。所以**按槽位从右往左排**：槽位 N 的整列左移 N 列——实例 1 的
// 窗口显示在主实例左边（用户要求）。
var instanceSlot int

// SetInstanceSlot 设置本进程的实例槽位（main 启动时调用一次；0/负数等同主实例）。
func SetInstanceSlot(n int) {
	if n > 0 {
		instanceSlot = n
	}
}

// instanceColumnWidth 每个实例占的列宽（DIP）：**按通话面板宽 + 边距**（用户要求）。
// 通话面板因此紧挨着排（实例 1 的面板贴在主实例面板左边）；**视频窗口的右边缘
// 与自己的面板对齐**（跟着面板整列走），两个实例的视频窗口允许重叠——用户可自行拖动。
const instanceColumnWidth = 260 + 16 // 260 = callPanelWidth（见 topmost_*.go）

// instanceColumnShiftPx 槽位带来的整列左移量（物理像素；主实例为 0）。
func instanceColumnShiftPx(scale float32) int32 {
	if instanceSlot <= 0 {
		return 0
	}
	return int32(float32(instanceSlot) * instanceColumnWidth * scale)
}

// watchThemeChanges 主题（深/浅色）切换后，让**长驻的自绘文本**重新取色。
//
// 自绘文本把主题前景色缓存在 canvas.Text 里（见 plainText.themed）：通话面板、
// 悬停提示这些只建一次的控件不重新取色时，切换主题后文字仍是旧颜色，要退出
// 重启才恢复（用户实测）。这里在设置变化时主动刷新一次（设置变化的场景还包括
// 缩放变化等，多刷一次这几个文本开销可忽略）。
func (a *App) watchThemeChanges() {
	a.fyne.Settings().AddListener(func(fyne.Settings) {
		fyne.Do(func() {
			// 注意逐个判空：把 nil 指针放进 fyne.CanvasObject 会得到非 nil 接口，
			// 直接调 Refresh() 会崩（这些控件是懒创建的）。
			if a.callStateLabel != nil {
				a.callStateLabel.Refresh()
			}
			if a.callTimerLabel != nil {
				a.callTimerLabel.Refresh()
			}
			if a.callName != nil {
				a.callName.Refresh()
			}
			if a.callTip != nil {
				a.callTip.Refresh()
			}
		})
	})
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
	// 通信录/通话记录：**通话中也保持可用**（打开后行内的操作按钮按当前话务/
	// 拨号复用状态显示，见 listRowActions，REQ-ACC-035）。
	setIconButtonEnabled(a.historyBtn, IconHistory, a.historyIconOff, true)
	setIconButtonEnabled(a.contactsBtn, IconContacts, a.contactsIconOff, true)
	// 自动接听按钮**通话中也保持可点**（用户要求）；其余（设置/呼叫/视频呼叫）
	// 通话中禁用。
	setEnabled(a.autoAnswerBtn, true)
	for _, c := range []interface {
		Enable()
		Disable()
	}{a.settingsBtn, a.callBtn, a.videoCallBtn} {
		setEnabled(c, !inCall)
	}
}

// setIconButtonEnabled 启用/禁用图标按钮，并在禁用时把图标换成灰色版本。
//
// Fyne 的 buttonRenderer 只在 `Disabled() && svg.IsResourceSVG(icon)` 时换用
// theme.NewDisabledResource——**位图（PNG）图标不会自动变灰**（上游 TODO），
// 所以顶栏那三个按钮里，设置（SVG 齿轮）会自动变灰，而通信录/通话记录
// （PNG）仍是白的（用户实测）。这里在开关时手动换图标。
func setIconButtonEnabled(btn *widget.Button, normal, disabled fyne.Resource, enabled bool) {
	if btn == nil {
		return
	}
	if enabled {
		btn.Icon = normal
		btn.Enable()
	} else {
		btn.Icon = disabled
		btn.Disable()
	}
	btn.Refresh()
}

// refreshRecentDials 拨号框的"最近呼叫"现在由建议下拉图层呈现（获得焦点即列出），
// 不再往 SelectEntry 塞选项——这里保留一个空实现，避免散落的调用点全改
// （最近呼叫的数据源仍是 core.RecentDials()）。
func (a *App) refreshRecentDials() {}

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
	// CTI 拨号复用模式：主按钮 = 第一动作，第二按钮 = 第二动作（会议只有第一动作）。
	// 两个按钮**蓝底白字、左右并排**（用户要求），图标跟着动作走。
	if a.ctiDialMode != "" && a.callBtn != nil {
		a.callBtn.SetBg(colorCallGreen)
		a.callBtn.SetLabelColor(colorIconWhite)
		switch a.ctiDialMode {
		case "consult":
			a.callBtn.SetIcon(IconCTIConsult)
			a.callBtn.SetText("发起咨询")
			a.ctiCallBtn2.SetIcon(IconCTIThreeWay)
			a.ctiCallBtn2.SetText("发起三方")
			a.ctiCallBtn2.Show()
		case "transfer":
			a.callBtn.SetIcon(IconCTITransfer)
			a.callBtn.SetText("直接转移")
			a.ctiCallBtn2.SetIcon(IconCTIConsultTransfer)
			a.ctiCallBtn2.SetText("咨询转移")
			a.ctiCallBtn2.Show()
		case "conference":
			a.callBtn.SetIcon(IconCTIAddMember)
			a.callBtn.SetText("加入会议")
			a.ctiCallBtn2.Hide()
		}
		a.callBtn.Refresh()
		a.ctiCallBtn2.Refresh()
		if a.callRow != nil {
			a.callRow.Refresh() // 两个按钮并排/单按钮的切换要重排
		}
		return
	}
	if a.ctiCallBtn2 != nil {
		a.ctiCallBtn2.Hide()
	}
	if a.activeCallID != "" {
		a.callBtn.SetText("挂断")
		a.callBtn.SetIcon(IconStopWhite)
		a.callBtn.SetBg(colorDangerRed)
		a.callBtn.SetLabelColor(colorIconWhite)
	} else {
		// 空闲「呼叫」：蓝底白字（用户要求，与面板按钮同一套配色）
		a.callBtn.SetText("呼叫")
		a.callBtn.SetIcon(IconPhoneCallWhite)
		a.callBtn.SetBg(colorCallGreen)
		a.callBtn.SetLabelColor(colorIconWhite)
	}
	a.callBtn.Refresh()
	if a.callRow != nil {
		a.callRow.Refresh()
	}
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
//
// trayMenu 托盘右键菜单（REQ-ACC-024）：显示主界面 / **登录账号或退出账号**
// （按当前注册状态，位置在「显示主界面」下面）/ 分隔 / 退出。
// **注册状态变化时重建**（updateTrayMenu），保证菜单项与状态一致。
func (a *App) trayMenu() *fyne.Menu {
	showMain := fyne.NewMenuItem("显示主界面", func() {
		a.showMain()
	})
	// 已注册/注册中 → 「退出账号」：注销（发 expires=0）并停注册周期，
	//   同时把「启用账户」置 false（下次启动不再自动注册）；
	// 未注册/注册失败 → 「登录账号」：启用账户并立即注册。
	// 动作可能阻塞（等 REGISTER 响应），放 goroutine，不卡 UI 线程。
	registered := a.regState == "registered" || a.regState == "registering"
	var accItem *fyne.MenuItem
	if registered {
		accItem = fyne.NewMenuItem("退出账号", func() {
			go func() {
				if err := a.core.SetRegistered(false); err != nil {
					logx.Logger().Warn("退出账号失败", "err", err)
				}
			}()
		})
	} else {
		accItem = fyne.NewMenuItem("登录账号", func() {
			go func() {
				if err := a.core.SetRegistered(true); err != nil {
					logx.Logger().Warn("登录账号失败", "err", err)
				}
			}()
		})
	}
	quit := fyne.NewMenuItem("退出", func() {
		a.fyne.Quit()
	})
	// 标记为退出项（Linux 生效，见 markTrayQuit）：否则 Fyne 会在菜单
	// 末尾自动补一个本地化的 Quit 项（driver_desktop.go 的
	// addMissingQuitForMenu），托盘上会出现「退出」与「Quit」两项
	markTrayQuit(quit)
	return fyne.NewMenu(appDisplayName, showMain, accItem, fyne.NewMenuItemSeparator(), quit)
}

// updateTrayMenu 按当前注册状态重建托盘菜单（Fyne 的 SetSystemTrayMenu
// 可重复调用：首次会启动托盘并在就绪时应用，之后只刷新菜单项）。
func (a *App) updateTrayMenu() {
	if a.desk == nil {
		return
	}
	logx.Logger().Debug("重建托盘菜单", "state", a.regState)
	a.desk.SetSystemTrayMenu(a.trayMenu())
}

func (a *App) buildTray() {
	if a.desk == nil {
		logx.Logger().Warn("当前驱动不支持系统托盘")
		return
	}
	// 应用图标按状态着色（未注册/注册中红、已注册空闲绿、已注册置忙黄），
	// 同时是"没单独设图标的窗口"的默认值（applyIconColors 内统一设置）。
	a.fyne.SetIcon(IconPhoneGreen)
	// 单击托盘图标唤出主窗口（Fyne 托盘无原生双击事件，单击唤出）
	a.desk.SetSystemTrayMenu(a.trayMenu())
	a.desk.SetSystemTrayWindow(a.mainW)
	// 托盘/应用图标显式按当前状态给一次（REQ-UI-011）：Fyne 的托盘就绪回调
	// 只在"从未显式设置过托盘图标"时才退回应用图标，不设的话托盘会跟着应用
	// 图标一起变绿，丢掉未注册的红色提醒。
	a.applyIconColors()
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
	// 尽力让托盘图标常显在通知区（Windows 11 的 IsPromoted 设置；见平台实现）
	startTrayIconPromotion()
	logx.Logger().Info("系统托盘已就绪（单击图标唤出主界面）")
}

// trayIcon 按注册与坐席状态返回图标（REQ-UI-011 修订）：未注册/注册中红、
// 已注册且坐席置忙黄、已注册且空闲绿。
func (a *App) trayIcon() fyne.Resource {
	if !a.trayRegistered {
		return IconPhoneRed
	}
	if a.agentBusy {
		return IconPhoneYellow
	}
	return IconPhoneGreen
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

// appTitle 应用标题："MTI 软电话 - 本机号码"（未注册/未配置时只有应用名）。
// 主窗口标题栏、托盘悬停提示与未接来电通知共用。
func (a *App) appTitle() string {
	account := a.core.State().Register.Account
	title := appDisplayName
	if account != "" {
		title += " - " + account
	}
	// 坐席模式（REQ-ACC-028）：标题/托盘提示在号码后追加坐席显示名
	if a.agentOnline && a.agentDisplayName != "" {
		title += " " + a.agentDisplayName
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

// applyAgentState 坐席状态变化：刷新坐席按钮（颜色/悬停文案/显隐）、班长模式
// 的监听按钮、窗口标题，以及应用/托盘图标颜色（已注册且置忙 = 黄色）。
func (a *App) applyAgentState() {
	online := a.agentOnline && a.agentMode != core.AgentModeNone
	if a.agentIcon != nil {
		if online {
			iconColor := colorSuccessGreen
			state, action := "空闲", "点击置忙"
			if a.agentBusy {
				iconColor = colorHoldYellow
				state, action = "置忙", "点击置闲"
			}
			a.agentIcon.SetResource(tintIcon(IconAgentSeat, iconColor))
			note := "座席" + state
			if a.agentDisplayName != "" {
				note += " - " + a.agentDisplayName
			}
			a.agentIcon.SetNote(note + "（" + action + "）")
			a.agentIcon.Show()
		} else {
			a.agentIcon.SetNote("")
			a.agentIcon.Hide()
			// 图标消失时把提示一并收起（指针可能一直停在图标上，MouseOut 不会来）
			if a.mainTip != nil {
				a.mainTip.hide()
			}
		}
	}
	// 在线座席入口：坐席模式下才显示（非坐席模式没有 listAgents 可查）
	if a.rosterIcon != nil {
		if online {
			a.rosterIcon.Show()
		} else {
			a.rosterIcon.Hide()
		}
	}
	// 顶栏的注册圆点与「已注册」文字也跟着置忙/置闲变色（REQ-ACC-034）
	if a.regDot != nil {
		a.regDot.setBusy(a.agentBusy)
	}
	a.setRegLabel(a.regState)
	// 图标显隐改变状态行的最小宽度：必须重排，否则顶栏仍按旧宽度布局
	//（踩过：「已注册」被截成「...」、图标位置错乱）。
	if a.statusRow != nil {
		a.statusRow.Refresh()
	}
	a.updateWindowTitle()
	a.applyIconColors()
}

// toggleAgentBusy 点击坐席按钮：空闲 → 置忙（setBusy），置忙 → 置闲（setIdle）。
// 结果由 CTI 推送的 MSP_EVENT_AGENT_ONLINE（携带最新 isBusy）回到界面。
func (a *App) toggleAgentBusy() {
	if !a.agentOnline || a.agentMode == core.AgentModeNone {
		return
	}
	action := "setBusy"
	if a.agentBusy {
		action = "setIdle"
	}
	if err := a.runCTI(nil, action, nil); err != nil {
		return
	}
	logx.Logger().Info("坐席状态切换", "action", action, "原置忙", a.agentBusy)
}

// applyIconColors 应用图标 / 主窗口图标 / 托盘图标统一按状态着色
// （REQ-UI-011 修订）：未注册与注册中红色、已注册且坐席置忙黄色、
// 已注册且空闲绿色。
func (a *App) applyIconColors() {
	icon := a.trayIcon()
	if a.fyne != nil {
		a.fyne.SetIcon(icon)
	}
	if a.mainW != nil {
		a.mainW.SetIcon(icon)
	}
	a.updateTrayIcon()
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

// syncInitialState 从核心快照补取注册与坐席的初始状态（REQ-UI-015 / REQ-ACC-028）：
// 开机自动注册、CTI 推 AGENT_ONLINE 都可能发生在界面订阅事件之前，只靠事件
// 流界面会停在初始值（用户实测：注册完成了托盘菜单还是「登录账号」）。
//
// **必须在 buildTray 之前调一次**：Fyne 的托盘就绪回调会用 buildTray 传进去的
// 那份菜单覆盖此前所有更新（driver_desktop.go 的 runSystray 捕获了首次的 m），
// 所以首份菜单本身就得是对的。
func (a *App) syncInitialState() {
	st := a.core.State()
	a.setRegState(string(st.Register.State))
	if a.regDot != nil {
		a.regDot.setState(a.regState)
	}
	a.setRegLabel(a.regState)
	a.agentOnline = st.AgentOnline
	a.agentMode = st.AgentMode
	a.agentBusy = st.AgentBusy
	a.agentDisplayName = st.AgentDisplayName
	a.setAutoAnswer(st.AutoAnswer)
	a.applyAgentState()
	a.updateWindowTitle()
}

// subscribeEvents 订阅核心事件，驱动界面状态。
func (a *App) subscribeEvents() {
	// 初始状态直接从核心快照读取（REQ-UI-015）：注册可能在 UI 订阅
	// 事件之前已完成（启动即注册），依赖事件流会导致界面状态不准确。
	a.syncInitialState()
	a.applyIconColors()
	// 托盘菜单也按当前状态重建（首份菜单在 buildTray 里已是同步过的状态，
	// 这里覆盖"buildTray 之后、订阅之前"发生的注册变化）。
	a.updateTrayMenu()

	sub := a.core.Bus().Subscribe()
	// **订阅之后再取一次快照**：网页可能在 UI 订阅事件之前就连上并订阅了视频
	//（软电话重启时页面会自动重连重订阅），漏掉那个 video.web 事件的后果是
	//"网页在看画面，桌面视频窗口却也打开了"（用户实测）。旋转/镜像状态同理。
	a.webVideoOn = a.core.WebVideoState().HideWindow
	a.applyVideoTransforms(a.core.VideoTransforms())
	// 更新状态同理（REQ-UPD-017）：软电话启动后**立即**查一次更新站点，
	// 那次检查往往在界面订阅事件之前就完成——事件收不到，用户就永远看不到
	// 提示（同版本的重复检查还会被去重挡掉）。订阅后补取一次当前状态。
	if st := a.core.UpdateStatus(); st.Phase != core.UpdateIdle {
		a.onUpdateStatus(st)
	}
	// 注册/坐席状态同理：订阅前那次快照可能已经过时（订阅的空档里完成注册），
	// 再补一次，保证顶栏、托盘图标与菜单和核心状态一致。
	a.syncInitialState()
	a.updateTrayMenu()
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
	case "system.updateConfig":
		// 自动更新配置变化（本机保存或网页写入）：回填配置页表单（REQ-UPD-018）
		if _, ok := ev.Data.(config.UpdateConfig); ok {
			a.loadUpdateConfigIntoForm()
		}

	case "system.autoAnswer":
		// 自动接听开关变化（本地按钮点击或网页 system.setAutoAnswer 设置）
		if st, ok := ev.Data.(core.AutoAnswerState); ok {
			a.setAutoAnswer(st.Enabled)
		}

	case "contact.changed":
		// 通信录被写入（桌面这里改的、网页 contact.* 改的、CSV/JSON 导入都算）：
		// 页面开着就重载一次。多标签/两端同开时靠这条事件保持一致。
		a.contactsOnCallEnded()
		a.historyOnCallEnded() // 通话记录行要重算名称/★/备注

	case "calllog.changed":
		// 通话记录被删除/清空（一般来自网页）：开着记录页就重载
		a.historyOnCallEnded()

	case "register.state":
		if data, ok := ev.Data.(map[string]any); ok {
			if state, ok := data["state"].(string); ok {
				a.setRegState(state)
				a.regDot.setState(state)
				a.setRegLabel(state) // 顶栏显示状态文字（不再显示本机号码）
				a.applyIconColors()
				// 托盘菜单按注册状态切换「登录账号 / 退出账号」（REQ-ACC-024）
				a.updateTrayMenu()
				// 「退出账号/登录账号」改的是「启用账户」（REQ-ACC-024）：配置页
				// 的勾选随之同步（只动这一个勾选，表单其它未保存的输入不受影响）。
				if a.accEnabled != nil {
					a.accEnabled.SetChecked(a.core.AccountEnabled())
				}
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
		// 订阅方显式要求 keepWindow 时窗口保持显示（REQ-VIDEO-009 修订，
		// 由 core 汇总成 HideWindow：任一订阅者没要求保持就隐藏）。
		//
		// ⚠️ 负载是 core.WebVideoState 结构体——总线是进程内的，事件不经
		// JSON 序列化，**不能**断言成 map[string]any（那样断言恒失败，
		// 表现为"订阅了但桌面窗口不关"）。只有 WS 客户端收到的是 JSON 对象。
		st, ok := ev.Data.(core.WebVideoState)
		if ok && st.HideWindow != a.webVideoOn {
			a.webVideoOn = st.HideWindow
			logx.Logger().Info("网页视频订阅状态变化",
				"active", st.Active, "hideWindow", st.HideWindow, "local", st.Local,
				"remote", st.Remote, "viewers", st.Viewers)
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

	case "cti.agent":
		// 坐席模式变化（MSP_EVENT_AGENT_ONLINE/OFFLINE，REQ-ACC-028）：
		// 只有坐席模式才显示高级话务按钮。通话中就地重建面板（两方通话
		// 才显示，拨号/振铃不显示——rebuildCallRows 按状态分支）。
		if data, ok := ev.Data.(map[string]any); ok {
			on, _ := data["online"].(bool)
			if name, ok := data["displayName"].(string); ok {
				a.agentDisplayName = name
			}
			if mode, ok := data["agentMode"].(string); ok {
				a.agentMode = mode
			}
			if busy, ok := data["isBusy"].(bool); ok {
				a.agentBusy = busy && on
			}
			if !on {
				a.agentBusy = false
			}
			a.agentOnline = on
			// 这里不再打日志：core 的 setAgentOnline 已经打过一条「坐席模式变化」，
			// 两边同一句文案会让日志里出现两条一模一样的记录（用户实测）。
			a.applyAgentState()
			if a.uiState == uiInCall && a.talkState() == core.TalkTwoParty {
				a.rebuildPanel(incallButtons)
			}
			// 非坐席模式下不应停留在"拨号复用"（咨询/转移/加会）里
			if !on && a.ctiDialMode != "" {
				a.exitCTIDialMode()
				if a.mainShown {
					a.mainW.Hide()
					a.mainShown = false
				}
			}
		}

	case "cti.state":
		// 话务状态变化（MSP_EVENT_*，REQ-ACC-026）：重建通话面板。
		// 对端号码/名称也可能变（咨询转接完成后本机接管原通话，见
		// MSP_EVENT_CONSULT_TRANSFER），所以先按新通话刷新对端展示。
		//
		// **只在"通话中"重建**：话务事件也可能在振铃期间到达——被咨询方会先收到
		// CONSULT_STARTED、而咨询腿那通电话还在振铃，这时用 incallButtons 重建会把
		// 振铃面板错误地变成"通话中"（接听/拒接按钮都不见了）。状态字段照常更新，
		// 接通时 setCallUI(uiInCall) 会用新状态重建（届时入口按钮按 ConsultInProgress 隐藏）。
		if callID, call := callData(ev.Data); call != nil && callID == a.activeCallID {
			a.activeCall = call
			a.setCallName(call)
			a.applyCallPhase(call)
			if a.uiState == uiInCall {
				a.rebuildPanel(incallButtons)
			}
		}

	case "cti.failed":
		// CTI 报的话务失败（协议 §16.2/§16.3）：CALL_FAILED 的 refuse/cancel/
		// takeover、CONSULT_FAILED 的 refuse/cancel。消息由 core 翻好（cti.failed
		// 事件），这里就地弹提示条。
		if data, ok := ev.Data.(map[string]any); ok {
			msg, _ := data["message"].(string)
			if msg != "" {
				a.toastError(msg)
			}
		}

	case "cti.result":
		// 指令结果（REQ-ACC-026）：结束按钮的"旋转中"状态；失败要显示错误消息
		//（包括 2 秒没等到回执的超时），然后照旧把拨号复用模式收回去。
		a.stopCTISpin()
		if data, ok := ev.Data.(map[string]any); ok {
			action, _ := data["action"].(string)
			// listAgents：结果交给在线座席页（不参与拨号复用的收尾）
			if action == "listAgents" {
				if ok, _ := data["success"].(bool); ok {
					if a.agents != nil {
						a.agents.setAgents(parseAgentEntries(data["data"]))
					}
					return
				}
				msg, _ := data["message"].(string)
				if msg == "" {
					msg = "获取在线座席失败"
				}
				logx.Logger().Warn("获取在线座席失败", "code", data["code"], "message", msg)
				if a.agents != nil {
					a.agents.setError(msg)
				}
				a.toastError(msg)
				return
			}
			if success, _ := data["success"].(bool); success {
				// 拨号复用流程（发起咨询/转移/加会…）成功：收起主窗体、回到通话面板。
				// 只有**该复用模式自己的动作**才收起：复用期间用户在同一个主窗体上
				// 点坐席状态按钮（setBusy/setIdle）等，回执不能把主窗体关掉。
				if a.ctiDialMode != "" && ctiDialAction(a.ctiDialMode, action) {
					a.finishCTIDial()
				}
				return
			}
			msg, _ := data["message"].(string)
			if msg == "" {
				msg = "操作失败"
			}
			logx.Logger().Warn("CTI 操作失败", "code", data["code"], "message", msg)
			a.toastError(msg)
			if a.ctiDialMode != "" && ctiDialAction(a.ctiDialMode, action) {
				a.exitCTIDialMode()
				a.mainW.Hide()
				a.mainShown = false
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
		ctiManaged := false
		if m, ok := ev.Data.(map[string]any); ok {
			reason, _ = m["reason"].(string)
			missed, _ = m["missed"].(bool)
			ctiManaged, _ = m["ctiManaged"].(bool)
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
			if ctiManaged {
				// CTI 接管的话务（收到过 CALL_RINGING）：SIP 侧原因不弹窗——
				// CTI 随后下发的 CALL_FAILED 会经 cti.failed 给出中文消息
				//（否则「603 …」与「呼叫未接通」两条提示同时出现）。
				logx.Logger().Info("呼叫失败（CTI 接管，失败消息由 cti.failed 提示）",
					"reason", reason)
				a.restoreIdle()
				return
			}
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
	a.regLabel.SetColor(a.statusColor(state))
	a.regLabel.SetBold(true)
}

// statusColor 注册状态文字/圆点的颜色：与圆点同色；**已注册且坐席置忙时用
// 黄色**（与"注册中"同一个黄），让顶栏跟着置忙/置闲一起变（REQ-ACC-034）。
func (a *App) statusColor(state string) color.Color {
	if state == "registered" && a.agentBusy {
		return busyColor
	}
	if c, ok := dotColors[state]; ok {
		return c
	}
	return dotColors["unregistered"]
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

// setAutoAnswer 刷新自动接听按钮的外观（本地点击与网页开关共用）：
// 开启 = 绿色图标、关闭 = 灰色图标，文案走悬停提示（用户要求）。
func (a *App) setAutoAnswer(enabled bool) {
	a.autoAnswer = enabled
	logx.Logger().Info("自动接听开关", "enabled", enabled)
	if a.autoAnswerBtn == nil {
		return
	}
	if enabled {
		a.autoAnswerBtn.SetResource(tintIcon(IconAutoAnswerOn, colorSuccessGreen))
		a.autoAnswerBtn.SetNote("自动接听：已开启（点击关闭）")
	} else {
		a.autoAnswerBtn.SetResource(tintIcon(IconAutoAnswerOn, disabledIconColor()))
		a.autoAnswerBtn.SetNote("自动接听：已关闭（点击开启）")
	}
}

// showMain 显示主窗口（托盘单击/菜单唤出）。
// 通话期间（呼叫/振铃/通话中）主窗口保持隐藏，通话面板关闭后
// 才可唤出（REQ-UI-025 修订）。
func (a *App) showMain() {
	// 通话中也能从托盘唤出主窗体（REQ-UI-030）：此时主窗体只放开号码按键与
	// 键盘输入（发 DTMF），输入框/设置/呼叫按钮等一律禁用，见 applyMainCallMode
	a.applyMainCallMode(a.uiState != uiIdle)
	// 任务栏按钮在窗口显示时才创建（图标取自窗口图标）：按当前话机状态再设一次，
	// 避免"窗口隐藏期间状态变了、按钮建出来还是旧颜色"。
	a.mainW.SetIcon(a.trayIcon())
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
	if hwnd := findWindowByTitlePrefix(appDisplayName); hwnd != 0 {
		forceForegroundWindow(hwnd)
	}
}

// ---------------------------------------------------------------------------
// 设置页操作
// ---------------------------------------------------------------------------

// saveAccount 保存账户配置并触发重新注册（REQ-ACC-011）。
// srtpLabels 媒体加密策略的下拉显示名；与 srtpValues 一一对应
// （顺序即下拉顺序，默认项在第二个）。
var srtpLabels = []string{"关闭", "支持（默认）", "建议", "必要"}

var srtpValues = []string{
	config.SRTPOff, config.SRTPSupported, config.SRTPPreferred, config.SRTPRequired,
}

// declaredIPValue 表单 → 配置值：留空/非法一律当"自动"（空串）。
func (a *App) declaredIPValue() string {
	return config.NormalizePublicIP(a.accIPSelect.Text)
}

// tlsNoteText 「只加密信令」提示（仅传输=tls 时显示）。
// 一句话说完：媒体是否加密由下面的「媒体加密」单独决定，此处不必展开。
const tlsNoteText = "SIPS 只加密信令"

// srtpHint 媒体加密的说明文字。
// 每档**一行、每行都短**：主窗体固定 320 DIP 宽，长行会把内容撑出可视区
// （中文没有空格，整行会被当成一个"词"，靠折行也救不回来）。
const srtpHint = "媒体加密（SRTP）：\n" +
	"关闭 = 不加密\n" +
	"支持 = 对端要求才配合（默认）\n" +
	"建议 = 主动建议，不强制\n" +
	"必要 = 对端不支持则拒接\n" +
	"覆盖声音/视频/按键音\n" +
	"密钥随信令传，建议配 TLS"

// srtpValueOfLabel 下拉显示名 → 配置值（未命中按默认「支持」）。
func srtpValueOfLabel(label string) string {
	for i, l := range srtpLabels {
		if l == label {
			return srtpValues[i]
		}
	}
	return config.SRTPSupported
}

// srtpLabelOfValue 配置值 → 下拉显示名。
func srtpLabelOfValue(v string) string {
	v = config.NormalizeSRTP(v)
	for i, val := range srtpValues {
		if val == v {
			return srtpLabels[i]
		}
	}
	return srtpLabels[1]
}

func (a *App) saveAccount() {
	// 服务器与端口合并解析：host[:port]，端口省略按传输取默认
	//（tls → 5061，其余 5060；REQ-ACC-006）
	host, port := parseHostPort(a.accServerAddr.Text, config.DefaultPortForTransport(a.accTransport.Selected))
	expires := 300
	if v := a.accExpires.Text; v != "" {
		fmt.Sscanf(v, "%d", &expires)
	}
	if expires <= 0 {
		expires = 300
	}
	// 声明 IP（REQ-ACC-023）：填了内容就必须是合法 IP——SDP 的 c= 行只认
	// IP 字面量，写域名/写错会在协商时静默变成"自动"，不如当场拦下。
	declaredIP := a.declaredIPValue()
	if strings.TrimSpace(a.accIPSelect.Text) != "" && declaredIP == "" {
		a.showInfo("提示", "「声明 IP」请填合法 IP（如 10.168.5.10），或留空表示自动")
		return
	}
	heartbeat := 20
	if v := strings.TrimSpace(a.accCTIHeartbeat.Text); v != "" {
		fmt.Sscanf(v, "%d", &heartbeat)
	}
	if heartbeat <= 0 {
		heartbeat = 20
	}
	acc := config.Account{
		Enabled:      a.accEnabled.Checked,
		Server:       host,
		Port:         port,
		Expires:      expires,
		CTIHeartbeat: heartbeat,
		RPort:        a.rportCheck.Checked,
		STUNEnabled:  a.accStunCheck.Checked,
		STUNServer:   a.accStunServer.Text,
		Proxy:        a.accProxy.Text,
		Domain:       config.NormalizeDomain(a.accDomain.Text),
		PublicIP:     declaredIP,
		Transport:    config.NormalizeTransport(a.accTransport.Selected),
		TLSVerify:    a.tlsVerifyCheck.Checked,
		SRTP:         config.NormalizeSRTP(srtpValueOfLabel(a.accSRTP.Selected)),
		Account:      a.accAccount.Text,
		AuthUser:     a.accAuthUser.Text,
		Password:     a.accPassword.Text,
		DisplayName:  a.accDisplay.Text,
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
	hb := acc.CTIHeartbeat
	if hb <= 0 {
		hb = 20
	}
	a.accCTIHeartbeat.SetText(fmt.Sprintf("%d", hb))
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
	a.accTransport.SetSelected(config.NormalizeTransport(acc.Transport))
	a.tlsVerifyCheck.SetChecked(acc.TLSVerify)
	a.updateTLSRow(acc.Transport)
	a.accSRTP.SetSelected(srtpLabelOfValue(acc.SRTP))
	a.accDomain.SetText(acc.Domain)
	// 声明 IP：直接回填（留空 = 自动）。值不在候选里也能显示——它就是可输入的
	// 下拉，手填过的地址保存后要能原样回来
	a.accIPSelect.SetText(config.NormalizePublicIP(acc.PublicIP))
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

// 音频预处理的三条说明（REQ-MEDIA-007）：直接显示在各自勾选项的**下面**
// （用户要求：试过 ⓘ+提示那套，效果都差，常驻一行反而最省事最稳）。
// 文案只讲"作用"，不带 (AEC)/(NS)/(AGC) 后缀——勾选项标签上已经有缩写。
const (
	preprocessAECNote = "用正在播放的声音当参考，消掉麦克风里的扬声器回声（外放时防自激）"
	preprocessNSNote  = "压掉风扇、键盘这类稳态背景噪声"
	preprocessAGCNote = "自动调整麦克风音量，与通话面板的手动增益叠加"
	// preprocessUnsupportedText 本构建没编进 APM 时的说明（这种情况才常驻显示）。
	preprocessUnsupportedText = "本构建未包含音频预处理（缺 webrtc-audio-processing 静态库，" +
		"见 scripts/build-audio-libs.sh）。"
)

// applyPreprocessForm 把音频预处理开关回填到界面（REQ-MEDIA-007），
// 本构建没有 APM 时置灰并说明原因。
func (a *App) applyPreprocessForm() {
	if a.aecCheck == nil {
		return
	}
	a.loadingForm = true // 回填期间屏蔽 OnChanged 自动保存
	a.aecCheck.SetChecked(a.cfg.Audio.AEC)
	a.nsCheck.SetChecked(a.cfg.Audio.NS)
	a.agcCheck.SetChecked(a.cfg.Audio.AGC)
	a.loadingForm = false

	if audio.PreprocessSupported() {
		a.aecCheck.Enable()
		a.nsCheck.Enable()
		a.agcCheck.Enable()
		a.preprocessHint.Hide() // 支持时不需要"本构建未包含"那行
		return
	}
	a.aecCheck.Disable() // 本构建没编进 APM：开关无意义
	a.nsCheck.Disable()
	a.agcCheck.Disable()
	a.preprocessHint.Show()
}

// onPreprocessChanged 音频预处理开关变化：写配置 + 立即下发给音频管理器
// （音频流在每通电话开始时重建，因此从下一通生效）。
func (a *App) onPreprocessChanged(bool) {
	if a.loadingForm || a.aecCheck == nil {
		return
	}
	a.cfg.Audio.AEC = a.aecCheck.Checked
	a.cfg.Audio.NS = a.nsCheck.Checked
	a.cfg.Audio.AGC = a.agcCheck.Checked
	if a.audioMgr != nil {
		a.audioMgr.SetPreprocess(audio.Preprocess{
			AEC: a.cfg.Audio.AEC, NS: a.cfg.Audio.NS, AGC: a.cfg.Audio.AGC,
		})
	}
	a.persistConfig()
	logx.Logger().Info("音频预处理开关已更新",
		"回声消除", a.cfg.Audio.AEC, "降噪", a.cfg.Audio.NS, "自动增益", a.cfg.Audio.AGC)
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
//
// 同时负责「启用视频通话」总开关（REQ-VIDEO-014）在界面上的体现：
// 开关本身在构建无视频时置灰，摄像头选择在开关关闭时不可用。
func (a *App) refreshCameras() {
	if a.camSelect == nil {
		return
	}
	// 勾选框状态与配置同步（回填期间屏蔽 OnChanged 自动保存）
	if a.videoCallCheck != nil {
		a.loadingForm = true
		a.videoCallCheck.SetChecked(a.cfg.Video.Enabled())
		a.loadingForm = false
		if a.core.VideoSupported() {
			a.videoCallCheck.Enable()
		} else {
			a.videoCallCheck.Disable() // 本构建没有视频能力：开关无意义
		}
	}
	if !a.core.VideoSupported() {
		a.camSelect.Disable()
		a.applyCameraList(nil, "此构建未启用视频")
		a.camHint.SetText("")
		return
	}
	if !a.core.CameraSupported() {
		// 编解码可用但**本平台没有摄像头后端**（darwin 这类目标）。
		// 这里必须与"没插摄像头"区分开：否则用户会在空列表里选「禁用」，
		// 把共用配置写坏（64 位版也跟着不发送本地视频了——实测踩过）。
		a.camSelect.Disable()
		a.applyCameraList(nil, "此版本不支持摄像头")
		a.camHint.SetText("本版本不支持摄像头采集：视频通话只能看对方，不发送本地画面")
		return
	}
	if a.core.VideoCallEnabled() {
		a.camSelect.Enable()
		a.camHint.SetText("")
	} else {
		// 总开关关闭：摄像头不可选，但仍照常枚举并回显当前选择——
		// 关一下开关不该把用户选好的摄像头"弄丢"。
		a.camSelect.Disable()
		a.camHint.SetText(cameraHintDisabled)
	}
	go func() {
		cams := a.core.VideoDevices()
		fyne.Do(func() { a.applyCameraList(cams, "未检测到摄像头") })
	}()
}

// onVideoCallEnabledChanged 「启用视频通话」总开关（REQ-VIDEO-014）：
// 立即生效（不需要重启）并保存配置。
func (a *App) onVideoCallEnabledChanged(on bool) {
	if a.loadingForm {
		return
	}
	a.cfg.Video.Disabled = !on
	if err := a.core.SetVideoCallEnabled(on); err != nil {
		logx.Logger().Warn("切换视频通话总开关失败", "err", err)
	}
	a.persistConfig()
	a.applyVideoFeatureUI()
}

// applyVideoFeatureUI 按视频功能是否可用刷新视频入口：
// 主窗体的「视频呼叫」按钮显隐 + 配置页「视频设备」卡片的可用性。
//
// 通话记录 / 通信录列表行内的视频呼叫按钮**不在这里刷**：那两个页面与配置页
// 在主窗体里互斥，且它们的行在每次进页面时都会重建（reload），用户下一次
// 进入时自然就是新状态。
func (a *App) applyVideoFeatureUI() {
	if a.videoCallBtn != nil && a.callRow != nil {
		if a.core.VideoUsable() {
			a.videoCallBtn.Show()
		} else {
			a.videoCallBtn.Hide()
		}
		a.callRow.Refresh()
	}
	a.refreshCameras()
}

// cameraHintDisabled 总开关关闭时摄像头选择下方的提示（REQ-VIDEO-014）。
const cameraHintDisabled = "视频通话已关闭：勾选上方「启用视频通话」后可用"

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
	// cameraHintDefault 摄像头选择下方的说明文字（构建无视频 / 总开关关闭时
	// refreshCameras 会把它换成对应的提示，正常可用时清空）。
	cameraHintDefault = "选择「禁用」后：发起视频呼叫不发送本地视频，来电也不显示「视频接听」按钮，但仍可查看对方的视频"
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
