//go:build windows

package ui

import (
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/fyne/v2"

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

// 通话面板固定尺寸（DIP，与 buildCallPanelIfNeeded 的 Resize 保持一致）：
// 126 = 上下 4px 留白 + 行距 2×2px + 三行各 38px（36 紧凑按钮 + 2 行距垫）。
const (
	callPanelWidth  = 260
	callPanelHeight = 126
)

// moveCallWindowToCorner 把通话面板移动到屏幕右下角（留 16px 边距），
// 并去除窗口装饰。Fyne Show 后窗口异步创建，带重试。
// 注意：必须按"通话面板"前缀查找（此前用精确标题"通话"，标题改为
// "通话面板 - 号码"后永远匹配不到，定位从未生效——bug 修复）。
func moveCallWindowToCorner(w fyne.Window) {
	// 目标物理尺寸 = DIP 尺寸 × 系统缩放
	scale := w.Canvas().Scale()
	width := uintptr(callPanelWidth * scale)
	height := uintptr(callPanelHeight * scale)
	for i := 0; i < 20; i++ {
		if hwnd := findWindowByTitlePrefix("通话面板"); hwnd != 0 {
			positionCallWindow(hwnd, width, height)
			// Show 后 fyne/GLFW 可能异步再调窗口尺寸，一次校正不够——
			// 延后两次重新校正位置与尺寸（REQ-UI-025 修订）
			go func() {
				time.Sleep(150 * time.Millisecond)
				if h := findWindowByTitlePrefix("通话面板"); h != 0 {
					positionCallWindow(h, width, height)
				}
				time.Sleep(350 * time.Millisecond)
				if h := findWindowByTitlePrefix("通话面板"); h != 0 {
					positionCallWindow(h, width, height)
				}
			}()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// positionCallWindow 定位通话面板：收紧窗口样式 + 固定尺寸 + 移动到
// 工作区右下角 + 置顶。尺寸通过窗口过程子类化钳制——GLFW 每次
// SetSize 附加的"幽灵标题栏高度"在 WM_WINDOWPOSCHANGING 中被强制
// 拉回目标值，窗口尺寸精确可控（REQ-UI-025 修订）。
func positionCallWindow(hwnd, width, height uintptr) {
	stripCallWindowChrome(hwnd)
	setCallWindowClamp(hwnd, int32(width), int32(height))

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
// 仅需安装一次（窗口只创建一次，重复调用仅更新目标尺寸）。
var (
	callWndProcOriginal uintptr
	callClampWidth      int32
	callClampHeight     int32
)

// setCallWindowClamp 安装/更新通话面板窗口尺寸钳制。
func setCallWindowClamp(hwnd uintptr, width, height int32) {
	callClampWidth, callClampHeight = width, height
	if callWndProcOriginal != 0 {
		return
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	setWindowLongPtr := user32.NewProc("SetWindowLongPtrW")
	const gwlWndProc = ^uintptr(3) // GWLP_WNDPROC = -4
	callWndProcOriginal, _, _ = setWindowLongPtr.Call(
		hwnd, gwlWndProc, syscall.NewCallback(callWndProc))
}

// callWndProc 通话面板窗口过程：拦截 WM_WINDOWPOSCHANGING，
// 把带尺寸的请求钳制为目标尺寸，其余消息原样转发。
func callWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
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
		r, _, _ := callWindowProc.Call(callWndProcOriginal, hwnd, uintptr(msg), wParam, lParam)
		mmi := (*minMaxInfo)(unsafe.Pointer(lParam))
		mmi.maxSizeX = callClampWidth
		mmi.maxSizeY = callClampHeight
		mmi.minTrackX = callClampWidth
		mmi.minTrackY = callClampHeight
		mmi.maxTrackX = callClampWidth
		mmi.maxTrackY = callClampHeight
		return r
	}

	if msg == wmWindowPosChanging && lParam != 0 {
		wp := (*windowPos)(unsafe.Pointer(lParam))
		if wp.flags&swpNoSize == 0 {
			wp.cx = callClampWidth
			wp.cy = callClampHeight
		}
	}
	r, _, _ := callWindowProc.Call(callWndProcOriginal, hwnd, uintptr(msg), wParam, lParam)
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

// stripCallWindowChrome 移除通话面板的窗口装饰（REQ-UI-024 修订）：
// 标题栏、系统菜单（关闭按钮）、可调边框与最小化/最大化按钮——
// 面板只显示窗体内容，位置由程序自动放置右下角。
// fyne 公开 API 无 SetDecorated，这里直接改 Win32 窗口样式。
func stripCallWindowChrome(hwnd uintptr) {
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

// getWindowPos 获取窗口位置。
func getWindowPos(hwnd uintptr) (x, y int) {
	user32 := syscall.NewLazyDLL("user32.dll")
	getWindowRect := user32.NewProc("GetWindowRect")
	type rect struct{ left, top, right, bottom int32 }
	var r rect
	getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return int(r.left), int(r.top)
}

// setWindowPosXY 移动窗口到指定位置。
func setWindowPosXY(hwnd uintptr, x, y int) {
	user32 := syscall.NewLazyDLL("user32.dll")
	setWindowPos := user32.NewProc("SetWindowPos")
	const (
		hwndTopMost = ^uintptr(0)
		swpNoSize   = 0x0001
		swpShowWin  = 0x0040
	)
	setWindowPos.Call(hwnd, hwndTopMost, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpShowWin)
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
