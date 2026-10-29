// Package tray 仅托盘无 UI 版（-tags noui，产物 mti-sip-phone-no-ui）的系统托盘。
//
// 这一版**不创建任何窗口**：主界面、通话面板、视频窗口、更新提示窗全都不存在，
// 所有操作都在网页控制台（http://127.0.0.1:<WS 端口>/）里做。托盘只做三件事：
//
//   - 左键点击图标 / 菜单「打开控制台」→ 用系统默认浏览器打开控制台；
//   - 右键菜单「登录账号 / 退出账号」（REQ-ACC-024，与完整版托盘一致）→ 登录或
//     退出 SIP 账号（共用「启用账户」开关）；
//   - 右键菜单「退出」→ 退出程序（走 main 的正常退出收尾：SIP 注销、释放锁）；
//   - 图标颜色反映注册状态（REQ-UI-011：已注册绿、其余红）。
//
// 实现直接用 fyne.io/systray（Fyne 托盘就是它，已是直接依赖），**不引入 Fyne
// driver**：没有窗口就不需要 GLFW/OpenGL，二进制更小、也没有"UI 必须在主线程"
// 的约束。注意不要调用（Fyne 驱动才有的）SetSystemTrayWindow——那是给 Fyne
// 窗口用的，这里没有窗口。
package tray

import (
	"sync"
	"time"

	"fyne.io/systray"

	"mti-sip-phone/internal/appicon"
	"mti-sip-phone/internal/logx"
)

// openDebounce 两次"打开控制台"之间的最小间隔：托盘左键双击会来两次点击，
// 不去重就会开出两个浏览器标签（用户要求"点一次开一个"）。
const openDebounce = time.Second

// Options 托盘运行参数。
type Options struct {
	// Title 悬停提示的基础文案（与完整版一致，形如 "MTI 软电话"）。
	Title string
	// OnOpen 打开控制台（左键点击图标或菜单项）。默认实现见 OpenBrowser。
	OnOpen func()
	// OnQuit 菜单「退出」。在 systray 的消息循环里调用，实现要尽快返回
	// （一般是 systray.Quit() 或取消 ctx，让 Run 返回）。
	OnQuit func()
	// OnAccount 菜单「登录账号 / 退出账号」的回调（REQ-ACC-024）。参数
	// signedIn 为当前是否在"已注册侧"（registered 或 registering）：
	// true 表示本次点击应"退出账号"、false 表示应"登录账号"——由调用方按
	// 这个布尔取反调用 core.App.SetRegistered。为 nil 时不显示该项。
	OnAccount func(signedIn bool)
}

// state 托盘运行期状态。图标与状态在 Run 之前就可能被设置（main 里先取
// app.State() 快照再进消息循环），因此这里缓存下来，就绪时一并应用。
var state struct {
	mu          sync.Mutex
	opts        Options
	ready       bool   // systray 就绪（onReady 之后才能 SetIcon/SetTooltip）
	registered  bool   // 图标颜色：registered 才可能是绿/黄/蓝（否则红）
	agentStatus string // 坐席状态 free/busy/acw/leave（busy/leave→黄、acw→蓝）
	signedIn    bool   // 菜单：registered 或 registering → 「退出账号」，其余 → 「登录账号」
	detail      string // 附加到提示里的信息（如本机号码）
	lastOpen    time.Time
	greenIcon   []byte
	redIcon     []byte
	yellowIcon  []byte
	blueIcon    []byte

	accountItem *systray.MenuItem // 「登录账号 / 退出账号」菜单项（OnAccount 为 nil 时恒为 nil）
}

// Run 启动托盘并阻塞，直到 Quit（或用户点菜单「退出」）。
//
// ⚠️ 必须在主 goroutine 调用：Windows 侧 systray 需要一条带消息循环的线程，
// 内部自己 LockOSThread；Linux 侧是 DBus/StatusNotifier，同样以调用线程为准。
func Run(o Options) {
	state.mu.Lock()
	state.opts = o
	state.greenIcon = loadIcon(appicon.PhoneGreenPNG(), appicon.ColorGreen)
	state.redIcon = loadIcon(appicon.PhoneRedPNG(), appicon.ColorRed)
	state.yellowIcon = loadIcon(appicon.PhoneYellowPNG(), appicon.ColorYellow)
	state.blueIcon = loadIcon(appicon.PhoneBluePNG(), appicon.ColorBlue)
	state.mu.Unlock()

	// 左键点击：直接开控制台（**不弹菜单**）。必须在 systray.Run 之前设置：
	// Linux 侧在注册 StatusNotifierItem 时会按"有没有左键回调"决定
	// ItemIsMenu 属性（没有回调才用左键弹菜单）。
	systray.SetOnTapped(openControlPage)

	systray.Run(onReady, func() {
		logx.Logger().Info("托盘已退出")
	})
}

// onReady 托盘就绪：设图标、提示与菜单。
func onReady() {
	state.mu.Lock()
	state.ready = true
	o := state.opts
	signedIn := state.signedIn
	state.mu.Unlock()

	// 菜单：打开控制台 / 登录账号或退出账号 / 分隔 / 退出。右键出菜单
	//（左键被 SetOnTapped 占用了）。
	open := systray.AddMenuItem("打开控制台", "在浏览器中打开网页控制台")
	// 「登录账号 / 退出账号」（REQ-ACC-024，与完整版托盘菜单一致）：只在
	// 提供 OnAccount 回调时显示；标签按注册状态（registered/registering →
	// 退出账号，其余 → 登录账号），状态变化时经 SetTitle 刷新。
	var account *systray.MenuItem
	if o.OnAccount != nil {
		account = systray.AddMenuItem(accountLabel(signedIn), "登录或退出 SIP 账号")
		state.mu.Lock()
		state.accountItem = account
		state.mu.Unlock()
	}
	systray.AddSeparator()
	quit := systray.AddMenuItem("退出", "退出 mti-sip-phone-no-ui")
	go func() {
		for range open.ClickedCh {
			openControlPage()
		}
	}()
	if account != nil {
		go func() {
			for range account.ClickedCh {
				state.mu.Lock()
				signedIn := state.signedIn
				state.mu.Unlock()
				o.OnAccount(signedIn)
			}
		}()
	}
	go func() {
		for range quit.ClickedCh {
			logx.Logger().Info("托盘菜单：退出")
			if o.OnQuit != nil {
				o.OnQuit()
			} else {
				systray.Quit()
			}
		}
	}()

	applyIcon()
	applyTooltip()
	logx.Logger().Info("系统托盘已就绪", "title", o.Title)
}

// SetState 更新托盘状态（REQ-UI-011 / REQ-ACC-024）：
//   - regState：注册状态字符串（"registered"/"registering"/"unregistered"/
//     "failed"）。决定图标颜色（只有 registered 变绿，registering 保持原色，
//     避免启动/重注册过程中的红绿闪烁）与「登录账号 / 退出账号」菜单项标签
//     （registered 与 registering → 退出账号，其余 → 登录账号）。
//   - detail：追加到悬停提示（一般是 " - <本机号码>"）。
//
// Run 之前调用也有效（状态缓存，就绪时统一应用）。
func SetState(regState, detail string) {
	state.mu.Lock()
	// 图标颜色：只有 registered 明确置绿；unregistered/failed 明确置红；
	// registering 不动（保持上一个确定状态的颜色）。
	switch regState {
	case "registered":
		state.registered = true
	case "unregistered", "failed":
		state.registered = false
	}
	// 菜单：registered 与 registering 都是"已注册侧"（点它 = 退出账号）。
	state.signedIn = regState == "registered" || regState == "registering"
	state.detail = detail
	state.mu.Unlock()
	applyIcon()
	applyTooltip()
	applyAccountItem()
}

// SetAgentStatus 坐席状态（REQ-UI-011 修订）：free/busy/acw/leave，已注册时
// busy/leave 托盘图标变黄、acw 变蓝。Run 之前调用也有效（状态缓存，就绪时
// 统一应用）。
func SetAgentStatus(status string) {
	state.mu.Lock()
	state.agentStatus = status
	state.mu.Unlock()
	applyIcon()
}

// accountLabel 「登录账号 / 退出账号」菜单项标签：已注册侧显示「退出账号」，
// 未注册侧显示「登录账号」。
func accountLabel(signedIn bool) string {
	if signedIn {
		return "退出账号"
	}
	return "登录账号"
}

// applyAccountItem 按当前注册状态刷新「登录账号 / 退出账号」菜单项标签。
// 托盘未就绪（accountItem 为 nil）时什么都不做；onReady 建项时已按缓存状态
// 给了正确标签，之后每次 SetState 用 SetTitle 就地刷新（不必重建菜单）。
func applyAccountItem() {
	state.mu.Lock()
	item, signedIn := state.accountItem, state.signedIn
	state.mu.Unlock()
	if item == nil {
		return
	}
	item.SetTitle(accountLabel(signedIn))
}

// Quit 退出托盘消息循环（Run 随即返回）。
func Quit() {
	systray.Quit()
}

// applyIcon 按当前注册状态换图标（托盘未就绪时只记状态，就绪时统一应用）。
func applyIcon() {
	state.mu.Lock()
	ready, registered, status := state.ready, state.registered, state.agentStatus
	icon := state.redIcon
	if registered {
		icon = state.greenIcon
		switch status {
		case "busy", "leave":
			icon = state.yellowIcon
		case "acw":
			icon = state.blueIcon
		}
	}
	state.mu.Unlock()
	if !ready || len(icon) == 0 {
		return
	}
	systray.SetIcon(icon)
}

// tooltipText 悬停提示文案：标题 + 附加信息（一般是本机号码），
// 与完整版的托盘提示完全一致（形如 "MTI 软电话 - 2999"）——
// 不带版本号、也不加状态后缀：注册状态由图标颜色表达（REQ-UI-011）。
func tooltipText() string {
	state.mu.Lock()
	detail, title := state.detail, state.opts.Title
	state.mu.Unlock()
	return title + detail
}

// applyTooltip 刷新悬停提示（托盘未就绪时只记状态，就绪时统一应用）。
func applyTooltip() {
	state.mu.Lock()
	ready := state.ready
	state.mu.Unlock()
	if !ready {
		return
	}
	systray.SetTooltip(tooltipText())
}

// openControlPage 打开控制台（左键点击与菜单项共用），带 1 秒防抖：
// 左键双击会连来两次点击，不去重会开出两个标签。
func openControlPage() {
	state.mu.Lock()
	o := state.opts
	if !state.lastOpen.IsZero() && time.Since(state.lastOpen) < openDebounce {
		state.mu.Unlock()
		return
	}
	state.lastOpen = time.Now()
	state.mu.Unlock()

	if o.OnOpen == nil {
		return
	}
	o.OnOpen()
}
