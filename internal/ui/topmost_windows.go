//go:build windows

package ui

import (
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/fyne/v2"

	"mti-sip-phone/internal/logx"
)

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

// markTrayQuit Windows 侧不打 IsQuit 标记（保持现状：Fyne 会补 Quit 项）。
func markTrayQuit(item *fyne.MenuItem) {}

// 通话面板固定尺寸（DIP，与 buildCallPanelIfNeeded 的 Resize 保持一致）：
// 126 = 上下 4px 留白 + 行距 2×2px + 三行各 38px（36 紧凑按钮 + 2 行距垫）。
const (
	callPanelWidth      = 260
	callPanelHeight     = 126
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
)

// moveCallWindowToCorner 把通话面板移动到屏幕右下角（留 16px 边距），
// 并去除窗口装饰。Fyne Show 后窗口异步创建，带重试。
// 注意：必须按"通话面板"前缀查找（此前用精确标题"通话"，标题改为
// "通话面板 - 号码"后永远匹配不到，定位从未生效——bug 修复）。
func moveCallWindowToCorner(w fyne.Window) {
	moveWindowToCorner(w, callWindowTitlePrefix, callPanelWidth, callPanelHeight, positionCallWindow)
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
func positionCallWindow(hwnd, width, height uintptr) {
	positionCornerWindow(hwnd, width, height, setCallWindowClamp)
}

// positionUpdateWindow 定位更新提示窗（同通话面板，尺寸另配），
// 并把窗口从任务栏隐藏（临时浮层不占任务栏按钮，与通话面板的
// 有意保留不同）。
func positionUpdateWindow(hwnd, width, height uintptr) {
	hideFromTaskbar(hwnd)
	deleteTaskbarTab(hwnd) // WS_EX_TOOLWINDOW 在窗口已显示后才改样式时可能不生效，兜底
	positionCornerWindow(hwnd, width, height, setUpdateWindowClamp)
}

// guid Win32 GUID 布局。
type guid struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

// deleteTaskbarTab 通过 Shell 官方接口 ITaskbarList::DeleteTab 移除窗口的
// 任务栏按钮。WS_EX_TOOLWINDOW 对"已显示后才改样式"的窗口不一定生效
// （实测任务栏按钮残留），这里用接口兜底；窗口销毁后按钮会自动回收。
func deleteTaskbarTab(hwnd uintptr) {
	// COM 按线程初始化/调用，先固定到当前 OS 线程（Go 的 goroutine 会漂移）
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ole32 := syscall.NewLazyDLL("ole32.dll")
	coCreateInstance := ole32.NewProc("CoCreateInstance")
	coInitializeEx := ole32.NewProc("CoInitializeEx")
	coInitializeEx.Call(0, 0x2) // COINIT_APARTMENTTHREADED；已初始化则返回错误码，忽略

	// CLSID_TaskbarList {56FDF344-FD6D-11d0-958A-006097C9A090}
	clsid := &guid{0x56FDF344, 0xFD6D, 0x11d0, [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90}}
	// IID_ITaskbarList {56FDF342-FD6D-11d0-958A-006097C9A090}
	iid := &guid{0x56FDF342, 0xFD6D, 0x11d0, [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90}}
	var taskbar uintptr
	hr, _, _ := coCreateInstance.Call(
		uintptr(unsafe.Pointer(clsid)), 0, 0x1 /* CLSCTX_INPROC_SERVER */,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&taskbar)))
	if int32(hr) < 0 || taskbar == 0 {
		return
	}
	// 接口指针的首字段才是 vtable 地址：先取出 vtable，再按槽位取方法
	// （此前直接把接口指针当成 vtable 起点，索引整体错位，取到空指针
	// 导致进程访问违例崩溃）
	vtablePtr := *(*uintptr)(unsafe.Pointer(taskbar))
	methods := unsafe.Slice((*uintptr)(unsafe.Pointer(vtablePtr)), 6)
	// 槽位：0 QueryInterface / 1 AddRef / 2 Release / 3 HrInit / 4 AddTab / 5 DeleteTab
	syscall.SyscallN(methods[3], taskbar)       // HrInit
	syscall.SyscallN(methods[5], taskbar, hwnd) // DeleteTab
	syscall.SyscallN(methods[2], taskbar)       // Release
}

// hideFromTaskbar 设置 WS_EX_TOOLWINDOW 并清除 WS_EX_APPWINDOW，
// 使窗口不出现在任务栏与 Alt+Tab（改扩展样式后需隐藏再显示一次，
// 否则任务栏按钮可能残留）。已设置过则直接返回，避免重复隐藏/显示闪烁。
func hideFromTaskbar(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")
	getWindowLong := user32.NewProc("GetWindowLongW")
	setWindowLong := user32.NewProc("SetWindowLongW")
	setWindowPos := user32.NewProc("SetWindowPos")
	showWindow := user32.NewProc("ShowWindow")

	const (
		gwlExStyle     = ^uintptr(19) // GWL_EXSTYLE = -20
		wsExToolWindow = 0x00000080
		wsExAppWindow  = 0x00040000
		swpFrameChange = 0x0020
		swpNoMove      = 0x0002
		swpNoSize      = 0x0001
		swpNoZOrder    = 0x0004
		swHide         = 0
		swShowNoActive = 4 // SW_SHOWNA：不抢焦点
	)
	ex, _, _ := getWindowLong.Call(hwnd, gwlExStyle)
	if ex&wsExToolWindow != 0 && ex&wsExAppWindow == 0 {
		return // 已是工具窗口样式
	}
	// 官方顺序：先隐藏 → 改扩展样式 → 强制框架更新 → 再显示
	//（显示状态下改样式任务栏按钮可能残留）
	showWindow.Call(hwnd, swHide)
	ex = (ex | wsExToolWindow) &^ uintptr(wsExAppWindow)
	setWindowLong.Call(hwnd, gwlExStyle, ex)
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpFrameChange|swpNoMove|swpNoSize|swpNoZOrder)
	showWindow.Call(hwnd, swShowNoActive)
}

// positionCornerWindow 通用定位：收紧窗口样式 + 固定尺寸 + 移动到工作区
// 右下角 + 置顶。尺寸通过窗口过程子类化钳制——GLFW 每次 SetSize 附加的
// "幽灵标题栏高度"在 WM_WINDOWPOSCHANGING 中被强制拉回目标值。
func positionCornerWindow(hwnd, width, height uintptr, clamp func(hwnd uintptr, w, h int32)) {
	stripWindowChrome(hwnd)
	clamp(hwnd, int32(width), int32(height))

	user32 := syscall.NewLazyDLL("user32.dll")
	setWindowPos := user32.NewProc("SetWindowPos")

	// 工作区尺寸（不含任务栏）：SystemParametersInfo SPI_GETWORKAREA
	type rect struct{ left, top, right, bottom int32 }
	var workRect rect
	systemParametersInfo := user32.NewProc("SystemParametersInfoW")
	const spiGetWorkArea = 0x0030
	systemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&workRect)), 0)

	x := workRect.right - int32(width) - 16
	y := workRect.bottom - int32(height) - 16
	const (
		hwndTopMost = ^uintptr(0)
		swpShowWin  = 0x0040
	)
	setWindowPos.Call(hwnd, hwndTopMost, uintptr(x), uintptr(y), width, height, swpShowWin)
}

// 窗口过程子类化：钳制窗口尺寸（对抗 GLFW 的幽灵标题栏高度）。
// 每种窗口一份（通话面板与更新提示窗尺寸不同，不能共用一份目标尺寸），
// 每份只需安装一次，重复调用仅更新目标尺寸。
type windowClamp struct {
	orig   uintptr // 原窗口过程
	width  int32
	height int32
}

var (
	callClamp   windowClamp // 通话面板
	updateClamp windowClamp // 更新提示窗
)

// setCallWindowClamp 安装/更新通话面板窗口尺寸钳制。
func setCallWindowClamp(hwnd uintptr, width, height int32) {
	callClamp.width, callClamp.height = width, height
	if callClamp.orig != 0 {
		return
	}
	callClamp.orig = subclassWindow(hwnd, syscall.NewCallback(callClampWndProc))
}

// setUpdateWindowClamp 安装/更新更新提示窗尺寸钳制。
func setUpdateWindowClamp(hwnd uintptr, width, height int32) {
	updateClamp.width, updateClamp.height = width, height
	if updateClamp.orig != 0 {
		return
	}
	updateClamp.orig = subclassWindow(hwnd, syscall.NewCallback(updateClampWndProc))
}

// subclassWindow 安装窗口过程子类化，返回原过程地址。
// SetWindowLongPtrW 是 64 位专属导出：32 位 user32.dll 里没有这个
// 符号（32 位的 SetWindowLongPtr 只是指向 SetWindowLongW 的宏），
// LazyProc.Call 找不到过程会直接 panic——GUI 子系统无控制台，
// 表现为"呼出弹出通话面板即崩溃"（32 位构建历史 bug）。
// 32 位下 LONG_PTR 与 LONG 同宽，退化用 SetWindowLongW 语义等价。
func subclassWindow(hwnd, proc uintptr) uintptr {
	user32 := syscall.NewLazyDLL("user32.dll")
	setWindowLongPtr := user32.NewProc("SetWindowLongPtrW")
	if setWindowLongPtr.Find() != nil {
		setWindowLongPtr = user32.NewProc("SetWindowLongW")
	}
	const gwlWndProc = ^uintptr(3) // GWLP_WNDPROC = -4
	orig, _, _ := setWindowLongPtr.Call(hwnd, gwlWndProc, proc)
	return orig
}

func callClampWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	return clampWndProc(&callClamp, hwnd, msg, wParam, lParam)
}

func updateClampWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	return clampWndProc(&updateClamp, hwnd, msg, wParam, lParam)
}

// clampWndProc 窗口过程：拦截 WM_GETMINMAXINFO / WM_WINDOWPOSCHANGING，
// 把尺寸请求钳制为目标尺寸，其余消息原样转发。
func clampWndProc(reg *windowClamp, hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	const (
		wmWindowPosChanging = 0x0046
		wmGetMinMaxInfo     = 0x0024
		swpNoSize           = 0x0001
	)
	user32 := syscall.NewLazyDLL("user32.dll")
	callWindowProc := user32.NewProc("CallWindowProcW")

	// WM_GETMINMAXINFO 必须先转发给原过程（GLFW 会按自己的窗口状态
	// 写入最小/最大跟踪尺寸），转发后再覆盖为目标尺寸——否则 GLFW
	// 的写入会顶掉我们的钳制（此前窗口被顶回 335 的根因）
	if msg == wmGetMinMaxInfo && lParam != 0 {
		r, _, _ := callWindowProc.Call(reg.orig, hwnd, uintptr(msg), wParam, lParam)
		mmi := (*minMaxInfo)(unsafe.Pointer(lParam))
		mmi.maxSizeX = reg.width
		mmi.maxSizeY = reg.height
		mmi.minTrackX = reg.width
		mmi.minTrackY = reg.height
		mmi.maxTrackX = reg.width
		mmi.maxTrackY = reg.height
		return r
	}

	if msg == wmWindowPosChanging && lParam != 0 {
		wp := (*windowPos)(unsafe.Pointer(lParam))
		if wp.flags&swpNoSize == 0 {
			wp.cx = reg.width
			wp.cy = reg.height
		}
	}
	r, _, _ := callWindowProc.Call(reg.orig, hwnd, uintptr(msg), wParam, lParam)
	return r
}

// windowPos WINDOWPOS 结构（x64 布局，共 36 字节）。
type windowPos struct {
	hwnd            uintptr
	hwndInsertAfter uintptr
	x, y, cx, cy    int32
	flags           uint32
}

// minMaxInfo MINMAXINFO 结构（x64 布局）：五个 POINT（各两个 LONG）。
type minMaxInfo struct {
	reservedX, reservedY     int32
	maxSizeX, maxSizeY       int32
	maxPosX, maxPosY         int32
	minTrackX, minTrackY     int32
	maxTrackX, maxTrackY     int32
}

// stripWindowChrome 移除窗口装饰（REQ-UI-024 修订，通话面板与更新
// 提示窗共用）：标题栏、系统菜单（关闭按钮）、可调边框与最小化/最大化
// 按钮——窗口只显示内容，位置由程序自动放置右下角。
// fyne 公开 API 无 SetDecorated，这里直接改 Win32 窗口样式。
func stripWindowChrome(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")
	getWindowLong := user32.NewProc("GetWindowLongW")
	setWindowLong := user32.NewProc("SetWindowLongW")
	setWindowPos := user32.NewProc("SetWindowPos")

	const (
		gwlStyle       = ^uintptr(15) // GWL_STYLE = -16
		wsCaption      = 0x00C00000   // WS_BORDER | WS_DLGFRAME（标题栏）
		wsSysMenu      = 0x00080000   // 系统菜单（关闭按钮）
		wsThickFrame   = 0x00040000   // 可调边框
		wsMinimizeBox  = 0x00020000
		wsMaximizeBox  = 0x00010000
		swpFrameChange = 0x0020
		swpNoMove      = 0x0002
		swpNoSize      = 0x0001
		swpNoZOrder    = 0x0004
	)
	style, _, _ := getWindowLong.Call(hwnd, gwlStyle)
	style &^= uintptr(wsCaption) | uintptr(wsSysMenu) | uintptr(wsThickFrame) |
		uintptr(wsMinimizeBox) | uintptr(wsMaximizeBox)
	setWindowLong.Call(hwnd, gwlStyle, style)
	// 通知系统重绘非客户区，使样式立即生效
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpFrameChange|swpNoMove|swpNoSize|swpNoZOrder)
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
//（主窗口隐藏却被判为可见——bug 修复）。
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
