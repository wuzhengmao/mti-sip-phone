//go:build windows

package ui

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/fyne/v2"

	"mti-sip-phone/internal/logx"
)

// screenWorkArea 屏幕工作区尺寸（**物理像素**）——需求 6 用它把视频窗口
// 限制在屏幕的 1/2 以内；调用方按 Canvas().Scale() 换算成 DIP。
// SPI_GETWORKAREA 返回的是工作区（不含任务栏），取不到时回退整屏指标。
func screenWorkArea() (float32, float32) {
	user32 := syscall.NewLazyDLL("user32.dll")
	spi := user32.NewProc("SystemParametersInfoW")
	if spi.Find() == nil {
		var r struct{ Left, Top, Right, Bottom int32 }
		const spiGetWorkArea = 0x0030
		ret, _, _ := spi.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
		if ret != 0 {
			w, h := r.Right-r.Left, r.Bottom-r.Top
			if w > 0 && h > 0 {
				return float32(w), float32(h)
			}
		}
	}
	// 回退：整屏指标（SM_CXSCREEN=0 / SM_CYSCREEN=1）
	get := user32.NewProc("GetSystemMetrics")
	if get.Find() != nil {
		return 1280, 800
	}
	cx, _, _ := get.Call(0)
	cy, _, _ := get.Call(1)
	if cx > 0 && cy > 0 {
		return float32(cx), float32(cy)
	}
	return 1280, 800
}

// setTopMost 设置主窗口是否置顶（REQ-UI-008）。
// Fyne 的 RequestAlwaysOnTop 只有置顶没有取消，这里直接用 Win32
// SetWindowPos 按窗口标题查找句柄，实现可撤销的置顶控制。
// 主窗口标题为 "mti-sip-phone - 号码"，按前缀匹配。
func setTopMost(on bool) {
	hwnd := findWindowByTitlePrefix("mti-sip-phone")
	if hwnd == 0 {
		return
	}

	const (
		hwndTopMost    = ^uintptr(0) // HWND_TOPMOST = -1
		hwndNotTopMost = ^uintptr(1) // HWND_NOTOPMOST = -2
		swpNoMove      = 0x0002
		swpNoSize      = 0x0001
	)
	insertAfter := hwndNotTopMost
	if on {
		insertAfter = hwndTopMost
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	setWindowPos := user32.NewProc("SetWindowPos")
	setWindowPos.Call(hwnd, insertAfter, 0, 0, 0, 0, swpNoMove|swpNoSize)
}

// windowTitleOf 取窗口标题（诊断用）。
func windowTitleOf(hwnd uintptr) string {
	if hwnd == 0 {
		return ""
	}
	buf := make([]uint16, 256)
	user32 := syscall.NewLazyDLL("user32.dll")
	user32.NewProc("GetWindowTextW").Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), 256)
	return syscall.UTF16ToString(buf)
}

// raiseVideoWindow 把视频窗口带到最前面（用户要求：每次打开视频窗口都放最前）。
// 与"置顶"不同——只是这一次显示时抢到前台，之后仍可被其他窗口盖住。
// 窗口刚创建时句柄还没出来，所以带重试（同 placeVideoWindow 的做法）。
func raiseVideoWindow(_ fyne.Window, keepTopmost bool) {
	go func() {
		var hwnd uintptr
		for i := 0; i < 20; i++ {
			if hwnd = findWindowByTitlePrefix(videoWindowTitlePrefix); hwnd != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if hwnd == 0 {
			logx.Logger().Debug("未找到视频窗口句柄，跳过带到前台")
			return
		}
		// 1) 先试激活到前台（用户正在别的程序里打字时，Windows 前台锁多半会拒绝）
		forceForegroundWindow(hwnd)
		// 2) 再抬 Z 序——**这一步才是"不被浏览器挡住"的关键**：
		//    后台进程无法把窗口抬到前台窗口之上，但"临时置顶再取消"可以
		//    （先 HWND_TOPMOST 抬到最顶，随即 HWND_NOTOPMOST 复位成普通窗口，
		//    抬高的 Z 序保留下来）。用户自己开了置顶时就不要取消。
		const (
			hwndTopMost    = ^uintptr(0) // HWND_TOPMOST = -1
			hwndNotTopMost = ^uintptr(1) // HWND_NOTOPMOST = -2
			swpNoMove      = 0x0002
			swpNoSize      = 0x0001
			swpNoActivate  = 0x0010
		)
		user32 := syscall.NewLazyDLL("user32.dll")
		setWindowPos := user32.NewProc("SetWindowPos")
		flags := uintptr(swpNoMove | swpNoSize | swpNoActivate)
		r1, _, _ := setWindowPos.Call(hwnd, hwndTopMost, 0, 0, 0, 0, flags)
		r2 := uintptr(0)
		if !keepTopmost {
			r2, _, _ = setWindowPos.Call(hwnd, hwndNotTopMost, 0, 0, 0, 0, flags)
		}
		// 诊断：置顶调用是否被系统接受、窗口此刻的上一层邻居是谁
		getWindowLong := user32.NewProc("GetWindowLongW")
		getWindow := user32.NewProc("GetWindow")
		ex, _, _ := getWindowLong.Call(hwnd, ^uintptr(19)) // GWL_EXSTYLE = -20
		prev, _, _ := getWindow.Call(hwnd, 3)              // GW_HWNDPREV = 3
		logx.Logger().Info("视频窗口已带到最前", "hwnd", hwnd, "保持置顶", keepTopmost,
			"topmostRet", r1, "notTopmostRet", r2,
			"仍带topmost样式", ex&0x8 != 0, "上方窗口", windowTitleOf(prev))
	}()
}

// setVideoWindowTopmost 视频窗口置顶开关（用户要求，可撤销）。
// 与主窗口的 setTopMost 同套路：按「视频通话」标题前缀找句柄 + SetWindowPos。
// 视频窗口保留系统标题栏，句柄一直在，开关随时生效。
func setVideoWindowTopmost(_ fyne.Window, on bool) {
	hwnd := findWindowByTitlePrefix(videoWindowTitlePrefix)
	if hwnd == 0 {
		logx.Logger().Debug("未找到视频窗口句柄，跳过置顶")
		return
	}
	const (
		hwndTopMost    = ^uintptr(0) // HWND_TOPMOST = -1
		hwndNotTopMost = ^uintptr(1) // HWND_NOTOPMOST = -2
		swpNoMove      = 0x0002
		swpNoSize      = 0x0001
	)
	insertAfter := hwndNotTopMost
	if on {
		insertAfter = hwndTopMost
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	user32.NewProc("SetWindowPos").Call(hwnd, insertAfter, 0, 0, 0, 0, swpNoMove|swpNoSize)
}

// callWindowTitle 通话面板标题（中文）：findWindowByTitlePrefix 依赖
// 「通话面板」前缀定位窗口，Windows 下保持原样。
func callWindowTitle(account string) string {
	title := callWindowTitlePrefix
	if account != "" {
		title += " - " + account
	}
	return title
}

// updateWindowTitle 更新提示窗标题：findWindowByTitlePrefix 依赖
// 「更新提示」前缀定位窗口，必须与 updateWindowTitlePrefix 一致。
func updateWindowTitle() string { return updateWindowTitlePrefix }

// videoWindowTitle 视频窗口标题（显示标题栏、可拖动，故与面板区分前缀）。
func videoWindowTitle(account string) string {
	title := videoWindowTitlePrefix
	if account != "" {
		title += " - " + account
	}
	return title
}

// markTrayQuit Windows 侧不打 IsQuit 标记（保持现状：Fyne 会补 Quit 项）。
func markTrayQuit(item *fyne.MenuItem) {}

// 通话面板固定尺寸（DIP，与 buildCallPanelIfNeeded 的 Resize 保持一致）：
// 126 = 上下 4px 留白 + 行距 2×2px + 三行各 38px（36 紧凑按钮 + 2 行距垫）。
const (
	callPanelWidth = 260
	// callPanelHeight 142 = 上下留白 8 + 三行各 36 + 行间 2 + 2（第 2、3 行整体
	// 下移 2px，用户要求）+ 第一行后的额外 2px。126 时行距被压成 0，按钮上下相连
	// （历史实测）；140 时三行贴得偏上，加 2px 后更透气。
	callPanelHeight     = 144
	callPanelButtonSize = 36
)

// 更新提示窗尺寸（DIP，与 buildUpdatePanelIfNeeded 的 Resize 保持一致）。
const (
	updatePanelWidth  = 340
	updatePanelHeight = 240
)

// 窗口标题前缀：findWindowByTitlePrefix 按前缀定位本进程窗口，
// 必须在标题函数中同样以该前缀开头（见 callWindowTitle / updateWindowTitle）。
const (
	callWindowTitlePrefix   = "通话面板"
	updateWindowTitlePrefix = "更新提示"
	// videoWindowTitlePrefix 视频窗口标题前缀。
	// ⚠️ **必须与 callWindowTitlePrefix 不同**：Windows 靠 strings.HasPrefix
	// 查找窗口，若视频窗口标题也以「通话面板」开头，moveCallWindowToCorner
	// 会把它当成通话面板——去标题栏、钳到 260×126、拉到右下角压在面板上。
	videoWindowTitlePrefix = "视频通话"

	// callPanelMargin 面板距工作区右下角的留白（物理像素）。
	callPanelMargin = 16
)

// moveCallWindowToCorner 把通话面板移动到屏幕右下角（留 16px 边距），
// 并去除窗口装饰。Fyne Show 后窗口异步创建，带重试。
// 注意：必须按"通话面板"前缀查找（此前用精确标题"通话"，标题改为
// "通话面板 - 号码"后永远匹配不到，定位从未生效——bug 修复）。
func moveCallWindowToCorner(w fyne.Window) {
	moveWindowToCorner(w, callWindowTitlePrefix, callPanelWidth, callPanelHeight, positionCallWindow)
}

// placeVideoWindow 把视频窗口摆到**通话面板正上方、右边缘对齐**（只移动）。
//
// 尺寸**不在这里设**：窗口有标题栏，SetWindowPos 的宽高是**窗口矩形**，
// 按客户区尺寸设下去会让客户区少掉一条标题栏的高度——宽高比随之改变，
// canvas.Image 的 ImageFillContain 就在左右补出黑边（用户实测"左右各一条黑边"）。
// 尺寸归 Fyne 的 Window.Resize（GLFW 语义是设客户区），本函数只负责位置。
func placeVideoWindow(w fyne.Window) {
	if w == nil {
		return
	}
	scale := w.Canvas().Scale()
	go func() {
		var hwnd uintptr
		for i := 0; i < 20; i++ {
			if hwnd = findWindowByTitlePrefix(videoWindowTitlePrefix); hwnd != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if hwnd == 0 {
			logx.Logger().Debug("未找到视频窗口句柄，跳过定位")
			return
		}
		user32 := syscall.NewLazyDLL("user32.dll")
		type rect struct{ left, top, right, bottom int32 }

		// 目标可见右边缘 / 可见底边；面板优先，找不到时按工作区兜底
		var targetRight, targetBottom int32
		gap := int32(8 * scale)
		if panel := findWindowByTitlePrefix(callWindowTitlePrefix); panel != 0 {
			if pv, pok := visibleRect(panel); pok {
				targetRight = pv.right
				targetBottom = pv.top - gap
			}
		}
		if targetRight == 0 {
			var work rect
			user32.NewProc("SystemParametersInfoW").Call(0x0030, 0, uintptr(unsafe.Pointer(&work)), 0)
			if work.right == 0 {
				return
			}
			targetRight = work.right - callPanelMargin
			targetBottom = work.bottom - int32(callPanelHeight*scale) - callPanelMargin - gap
		}

		move := func() {
			h := findWindowByTitlePrefix(videoWindowTitlePrefix)
			if h == 0 {
				return
			}
			vis, ok := visibleRect(h)
			if !ok {
				return
			}
			var wr rect
			user32.NewProc("GetWindowRect").Call(h, uintptr(unsafe.Pointer(&wr)))
			// 只移动（SWP_NOSIZE）：移动会同时平移窗口矩形与可见矩形，用差值修正
			// 即可，与边框宽度无关。
			x := wr.left + (targetRight - vis.right)
			y := wr.top + (targetBottom - vis.bottom)
			const swpNoSize, swpNoZOrder, swpNoActivate = 0x0001, 0x0004, 0x0010
			user32.NewProc("SetWindowPos").Call(h, 0, uintptr(x), uintptr(y), 0, 0,
				swpNoSize|swpNoZOrder|swpNoActivate)
			logx.Logger().Debug("视频窗口已摆放",
				"targetRight", targetRight, "targetBottom", targetBottom,
				"vis", fmt.Sprintf("%dx%d@%d,%d", vis.right-vis.left, vis.bottom-vis.top, vis.left, vis.top),
				"pos", fmt.Sprintf("%d,%d", x, y))
		}
		move()
		// 尺寸由 Fyne 异步生效（GLFW 可能稍后再应用一次），延后多次校正位置，
		// 节奏与 moveWindowToCorner 一致。
		for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond, 800 * time.Millisecond} {
			time.Sleep(delay)
			if findWindowByTitlePrefix(videoWindowTitlePrefix) == 0 {
				return
			}
			move()
		}
	}()
}

// visibleRect 窗口的**可见**边界。
// GetWindowRect 含 Windows 10+ 不可见的调整边框（约 8px），带标题栏的窗口
// 比无边框的通话面板在右边多出这一圈——直接按窗口矩形定位就会"右边缘对不齐"。
// 优先用 DWM 的扩展框架边界；取不到（老系统/dwmapi 不可用）时退回窗口矩形。
type winRect struct{ left, top, right, bottom int32 }

func visibleRect(hwnd uintptr) (winRect, bool) {
	var wr winRect
	user32 := syscall.NewLazyDLL("user32.dll")
	if r1, _, _ := user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&wr))); r1 == 0 {
		return wr, false
	}
	dwm := syscall.NewLazyDLL("dwmapi.dll")
	if dwm.Load() == nil {
		fn := dwm.NewProc("DwmGetWindowAttribute")
		if fn.Find() == nil {
			var vis winRect
			const dwmwaExtendedFrameBounds = 9
			if r1, _, _ := fn.Call(hwnd, dwmwaExtendedFrameBounds,
				uintptr(unsafe.Pointer(&vis)), unsafe.Sizeof(vis)); r1 == 0 {
				return vis, true
			}
		}
	}
	return wr, true
}

// moveUpdateWindowToCorner 把更新提示窗移动到屏幕右下角（留 16px 边距），
// 并去除窗口装饰；节奏与通话面板一致（句柄异步重试 + 多次延时校正）。
// 尺寸取窗口当前值（内容自适应调整过），而不是固定常量。
func moveUpdateWindowToCorner(w fyne.Window) {
	size := w.Canvas().Size()
	moveWindowToCorner(w, updateWindowTitlePrefix, size.Width, size.Height, positionUpdateWindow)
}

// moveWindowToCorner 通用的"置右下角"实现：按标题前缀找句柄（Show 后
// 窗口异步创建，带重试），定位后再延后多次校正（fyne/GLFW 可能异步
// 再调窗口尺寸，一次校正不够，REQ-UI-025 修订）。
//
// 重试必须放在后台 goroutine：调用方是 UI 线程（事件回调里 Show 之后
// 立即定位），同步 sleep 重试会把 UI 线程连同 Fyne 事件循环一起卡住——
// 窗口尚未创建时永远等不到，去装饰与定位全部失效（历史上表现为
// 更新提示窗带标题栏、尺寸不对、未吸附右下角）。
func moveWindowToCorner(w fyne.Window, titlePrefix string, wDip, hDip float32, position func(hwnd, width, height uintptr)) {
	// 目标物理尺寸 = DIP 尺寸 × 系统缩放
	scale := w.Canvas().Scale()
	width := uintptr(wDip * scale)
	height := uintptr(hDip * scale)

	go func() {
		var hwnd uintptr
		for i := 0; i < 40; i++ {
			if hwnd = findWindowByTitlePrefix(titlePrefix); hwnd != 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if hwnd == 0 {
			logx.Logger().Warn("未找到窗口句柄，跳过右下角定位", "title", titlePrefix)
			return
		}
		position(hwnd, width, height)
		for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond, 800 * time.Millisecond} {
			time.Sleep(delay)
			if h := findWindowByTitlePrefix(titlePrefix); h != 0 {
				position(h, width, height)
			}
		}
	}()
}

// positionCallWindow 定位通话面板：收紧窗口样式 + 固定尺寸 + 移动到
// 工作区右下角 + 置顶。尺寸通过窗口过程子类化钳制——GLFW 每次
// SetSize 附加的"幽灵标题栏高度"在 WM_WINDOWPOSCHANGING 中被强制
// 拉回目标值，窗口尺寸精确可控（REQ-UI-025 修订）。
// positionCallWindow 通话面板只做定位：**刻意不去装饰、不钳尺寸**。
//
// 面板改用「保留标题栏 + Fyne 的 SetFixedSize」来固定尺寸：
// 去装饰（stripWindowChrome）是**绕过 GLFW** 改窗口样式的，会让 GLFW 的边框
// 尺寸缓存永久失准——它仍按"有标题栏"换算客户区，Fyne 于是认为画布比真实
// 客户区高出一条标题栏（"幽灵标题栏"），内容整体下移、**第一行被顶出可视区**。
// 现象极具迷惑性：来电振铃时三行正常（ringFill 定时重绘掩盖了），一点接听就只剩两行。
// 保留标题栏后 GLFW 的缓存始终有效，画布 == 客户区，问题从源头消失。
func positionCallWindow(hwnd, width, height uintptr) {
	setToolWindowStyle(hwnd) // 兜底：面板同样不占任务栏（见 showWindowAsTool）
	positionCornerWindow(hwnd, width, height)
}

// positionUpdateWindow 定位更新提示窗（同通话面板，尺寸另配）：
// 尺寸交给 Fyne（对齐通话面板），任务栏样式已在显示时处理（见 showWindowAsTool）。
func positionUpdateWindow(hwnd, width, height uintptr) {
	setToolWindowStyle(hwnd) // 兜底：万一还没改过（正常已在显示前改好）
	positionCornerWindow(hwnd, width, height)
}
func showUpdateWindow(w fyne.Window) {
	showWindowAsTool(w, updateWindowTitlePrefix)
}

// showCallWindow 显示通话面板，同样按工具窗口显示（不占任务栏）。
func showCallWindow(w fyne.Window) {
	showWindowAsTool(w, callWindowTitlePrefix)
}

// showWindowAsTool 显示窗口，并保证它**不会被外壳建成任务栏按钮**。
//
// 为什么需要这么绕：GLFW 给每个顶层窗口都加了 WS_EX_APPWINDOW
// （glfw/src/win32_window.c 的 getWindowExStyle 写死），而该样式的作用就是
// "强制生成任务栏按钮"——它压过 WS_POPUP 本来不占任务栏的语义。
// 只在窗口显示之后异步改样式不够：外壳是**状态驱动**地扫描任务栏窗口，
// 往往已经按 APPWINDOW 把按钮建出来了，之后再改样式或隐藏都不会把这个按钮
// 收回去（用户实测：点「稍后」关掉更新提示窗后，任务栏还残留点不动的按钮）。
// 所以这里在**第一次显示的同一步内**走一遍 Hide → 改成 WS_EX_TOOLWINDOW →
// Show：外壳还没轮到处理"显示"这件事，窗口已经是工具窗口了，按钮根本不会建。
// 后续的重复显示（isToolWindow 已为真）直接 Show，不折腾、不闪。
func showWindowAsTool(w fyne.Window, titlePrefix string) {
	w.Show() // 首次 Show 才会创建 GLFW 窗口
	hwnd := findWindowByTitlePrefix(titlePrefix)
	if hwnd == 0 || isToolWindow(hwnd) {
		return
	}
	w.Hide()
	setToolWindowStyle(hwnd)
	w.Show()
	logx.Logger().Debug("窗口已按工具窗口显示（不占任务栏）", "title", titlePrefix, "hwnd", hwnd)
}

// exStyle 常量与读写的公共部分（32 位回退见下方注释）。
const (
	gwlExStyle    = ^uintptr(19) // GWL_EXSTYLE = -20
	wsExAppWindow = 0x00040000   // 强制任务栏按钮（GLFW 给每个窗口都加的）
	wsExToolWnd   = 0x00000080   // 工具窗口：不占任务栏
)

// exStyleProcs 返回读写扩展样式的两个导出。
//
// ⚠️ 32 位 user32.dll 没有 GetWindowLongPtrW / SetWindowLongPtrW 导出，
// LazyProc.Call 找不到过程会直接 panic（GUI 子系统无控制台，panic 毫无可见
// 输出）；32 位下 LONG_PTR 与 LONG 同宽，回退到 GetWindowLongW / SetWindowLongW
// 语义等价（同 CLAUDE.md「32 位 Win32 调用」条）。
func exStyleProcs() (getLong, setLong *syscall.LazyProc) {
	user32 := syscall.NewLazyDLL("user32.dll")
	getLong = user32.NewProc("GetWindowLongPtrW")
	setLong = user32.NewProc("SetWindowLongPtrW")
	if getLong.Find() != nil || setLong.Find() != nil {
		getLong = user32.NewProc("GetWindowLongW")
		setLong = user32.NewProc("SetWindowLongW")
	}
	return getLong, setLong
}

// isToolWindow 报告窗口是否已经是"工具窗口"（WS_EX_TOOLWINDOW 已置、
// WS_EX_APPWINDOW 已清）——即已经处理过、不占任务栏。
func isToolWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return true
	}
	getLong, _ := exStyleProcs()
	ex, _, _ := getLong.Call(hwnd, gwlExStyle)
	return ex&wsExToolWnd != 0 && ex&wsExAppWindow == 0
}

// setToolWindowStyle 把 WS_EX_APPWINDOW 换成 WS_EX_TOOLWINDOW（工具窗口不占
// 任务栏）。幂等：已经改过就什么都不做。
func setToolWindowStyle(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	getLong, setLong := exStyleProcs()
	ex, _, _ := getLong.Call(hwnd, gwlExStyle)
	want := (ex | wsExToolWnd) &^ uintptr(wsExAppWindow)
	if want == ex {
		return
	}
	setLong.Call(hwnd, gwlExStyle, want)
}

// guid Win32 GUID 布局。
type guid struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

// positionCornerWindow 把窗口移到工作区右下角并置顶（留 callPanelMargin）。
//
// **只移动、不改尺寸**：通话面板与更新提示窗都已是 Fyne 的 splash 窗口
// （GLFW 从创建起就按无边框处理），尺寸由 Fyne 的 Resize/SetFixedSize 决定，
// 这里若再按客户区尺寸设窗口矩形，无边框下虽等价、却容易与 Fyne 的意图打架。
func positionCornerWindow(hwnd, width, height uintptr) {

	user32 := syscall.NewLazyDLL("user32.dll")
	setWindowPos := user32.NewProc("SetWindowPos")

	// 工作区尺寸（不含任务栏）：SystemParametersInfo SPI_GETWORKAREA
	type rect struct{ left, top, right, bottom int32 }
	var workRect rect
	systemParametersInfo := user32.NewProc("SystemParametersInfoW")
	const spiGetWorkArea = 0x0030
	systemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&workRect)), 0)

	const (
		hwndTopMost = ^uintptr(0)
		swpShowWin  = 0x0040
		swpNoSize   = 0x0001
	)
	// 用窗口实际尺寸摆位，并按 SWP_NOSIZE 只移动
	var wr rect
	user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	w, h := int32(width), int32(height)
	if wr.right > wr.left && wr.bottom > wr.top {
		w, h = wr.right-wr.left, wr.bottom-wr.top
	}
	flags := uintptr(swpShowWin) | swpNoSize
	x := workRect.right - w - callPanelMargin
	y := workRect.bottom - h - callPanelMargin
	setWindowPos.Call(hwnd, hwndTopMost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), flags)
}

// windowPos WINDOWPOS 结构（x64 布局，共 36 字节）。
type windowPos struct {
	hwnd            uintptr
	hwndInsertAfter uintptr
	x, y, cx, cy    int32
	flags           uint32
}

// flashWindowEx 开始循环闪烁窗口（任务栏+标题栏），内容不动。
func flashWindowEx(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")
	flashWindowEx := user32.NewProc("FlashWindowEx")
	type flashWInfo struct {
		cbSize    uint32
		hwnd      uintptr
		dwFlags   uint32
		uCount    uint32
		dwTimeout uint32
	}
	const (
		flashwAll       = 0x00000003 // 任务栏 + 标题栏
		flashwTimerNoFg = 0x0000000C // 连续闪烁直到 FlashWindowEx(0) 停止
	)
	info := flashWInfo{
		cbSize:    uint32(unsafe.Sizeof(flashWInfo{})),
		hwnd:      hwnd,
		dwFlags:   flashwAll | flashwTimerNoFg,
		uCount:    0,
		dwTimeout: 0,
	}
	flashWindowEx.Call(uintptr(unsafe.Pointer(&info)))
}

// stopFlashWindowEx 停止闪烁。
func stopFlashWindowEx(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")
	flashWindowEx := user32.NewProc("FlashWindowEx")
	type flashWInfo struct {
		cbSize    uint32
		hwnd      uintptr
		dwFlags   uint32
		uCount    uint32
		dwTimeout uint32
	}
	info := flashWInfo{cbSize: uint32(unsafe.Sizeof(flashWInfo{})), hwnd: hwnd}
	flashWindowEx.Call(uintptr(unsafe.Pointer(&info)))
}

// setForegroundWindow 尝试把窗口带到前台（Windows 前台锁可能拒绝，
// 失败时仅任务栏闪烁——平台级限制）。
func setForegroundWindow(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SetForegroundWindow")
	proc.Call(hwnd)
}

// forceForegroundWindow 强力带到前台：SetForegroundWindow 在应用非
// 前台时会被 Windows 前台锁拒绝，通过 AttachThreadInput 附加到当前
// 前台线程临时绕过限制（随后立即分离）。失败时回退普通调用。
func forceForegroundWindow(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setForegroundWindow := user32.NewProc("SetForegroundWindow")
	getForegroundWindow := user32.NewProc("GetForegroundWindow")
	getWindowThreadProcessID := user32.NewProc("GetWindowThreadProcessId")
	getCurrentThreadID := kernel32.NewProc("GetCurrentThreadId")
	attachThreadInput := user32.NewProc("AttachThreadInput")

	// 先直接尝试
	if r, _, _ := setForegroundWindow.Call(hwnd); r != 0 {
		return
	}
	// 前台锁拒绝：附加到前台线程再试（经典 Windows 技巧）
	fg, _, _ := getForegroundWindow.Call()
	if fg == 0 {
		return
	}
	fgThread, _, _ := getWindowThreadProcessID.Call(fg, 0)
	myThread, _, _ := getCurrentThreadID.Call()
	if fgThread == myThread {
		return
	}
	const attachTrue = 1
	if r, _, _ := attachThreadInput.Call(myThread, fgThread, attachTrue); r != 0 {
		setForegroundWindow.Call(hwnd)
		attachThreadInput.Call(myThread, fgThread, 0)
	}
}

// ---------------------------------------------------------------------------
// 振铃边框彩色闪烁（DWM 边框颜色，Windows 11）
// ---------------------------------------------------------------------------

// setWindowBorderColor 设置窗口边框颜色（DWM，Win11）。失败静默——
// 老系统无边框着色能力，振铃提醒退化为任务栏闪烁。
// 注意：无边框通话面板无 DWM 边框，可见闪烁由 ui_call.go 的内容
// 描边矩形实现（ringBorderColors 已移至该文件）。
func setWindowBorderColor(hwnd uintptr, color uint32) {
	dwmapi := syscall.NewLazyDLL("dwmapi.dll")
	proc := dwmapi.NewProc("DwmSetWindowAttribute")
	const dwmwaBorderColor = 34
	proc.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&color)), unsafe.Sizeof(color))
}

// ringBorderFlash 循环切换窗口边框颜色，直到 stop 关闭；结束时恢复默认色。
func ringBorderFlash(hwnd uintptr, stop <-chan struct{}) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-stop:
			setWindowBorderColor(hwnd, 0xFFFFFFFF) // DWMWA_COLOR_DEFAULT
			return
		case <-ticker.C:
			setWindowBorderColor(hwnd, ringBorderColors[i%len(ringBorderColors)])
			i++
		}
	}
}

// findWindowByTitlePrefix 按标题前缀查找**本进程**的顶层窗口。
// 进程过滤必不可少：其他进程窗口标题可能包含应用名（如浏览器标签页
// "mti-sip-phone 测试页面 - Edge"），误匹配会导致可见性误判
// （主窗口隐藏却被判为可见——bug 修复）。
func findWindowByTitlePrefix(prefix string) uintptr {
	user32 := syscall.NewLazyDLL("user32.dll")
	enumWindows := user32.NewProc("EnumWindows")
	getWindowText := user32.NewProc("GetWindowTextW")
	getWindowThreadProcessID := user32.NewProc("GetWindowThreadProcessId")
	currentPID := os.Getpid()

	var found uintptr
	cb := syscall.NewCallback(func(hwnd syscall.Handle, lParam uintptr) uintptr {
		// 仅匹配本进程窗口
		var pid uint32
		getWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))
		if int(pid) != currentPID {
			return 1 // 继续枚举
		}
		buf := make([]uint16, 256)
		n, _, _ := getWindowText.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buf[0])), 256)
		if n == 0 {
			return 1 // 继续枚举
		}
		if strings.HasPrefix(syscall.UTF16ToString(buf[:n]), prefix) {
			found = uintptr(hwnd)
			return 0 // 停止枚举
		}
		return 1
	})
	enumWindows.Call(cb, 0)
	return found
}
