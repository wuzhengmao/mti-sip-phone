//go:build windows

package ui

import (
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
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

// moveCallWindowToCorner 把通话面板移动到屏幕右下角（留 16px 边距），
// 并去除最小化/最大化按钮。Fyne Show 后窗口异步创建，带重试。
// 注意：必须按"通话面板"前缀查找（此前用精确标题"通话"，标题改为
// "通话面板 - 号码"后永远匹配不到，定位从未生效——bug 修复）。
func moveCallWindowToCorner(_ any) {
	for i := 0; i < 20; i++ {
		if hwnd := findWindowByTitlePrefix("通话面板"); hwnd != 0 {
			positionCallWindow(hwnd)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// positionCallWindow 定位通话面板：收紧窗口样式 + 移动到工作区右下角 + 置顶。
func positionCallWindow(hwnd uintptr) {
	stripMinMaxButtons(hwnd)

	user32 := syscall.NewLazyDLL("user32.dll")
	getWindowRect := user32.NewProc("GetWindowRect")
	setWindowPos := user32.NewProc("SetWindowPos")

	type rect struct{ left, top, right, bottom int32 }
	var winRect, workRect rect
	getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&winRect)))

	// 工作区尺寸（不含任务栏）：SystemParametersInfo SPI_GETWORKAREA
	systemParametersInfo := user32.NewProc("SystemParametersInfoW")
	const spiGetWorkArea = 0x0030
	systemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&workRect)), 0)

	w := winRect.right - winRect.left
	h := winRect.bottom - winRect.top
	x := workRect.right - w - 16
	y := workRect.bottom - h - 16
	const (
		hwndTopMost = ^uintptr(0)
		swpNoSize   = 0x0001
		swpShowWin  = 0x0040
	)
	setWindowPos.Call(hwnd, hwndTopMost, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpShowWin)
}

// stripMinMaxButtons 从窗口样式中移除最小化/最大化按钮（通话面板不需要）。
func stripMinMaxButtons(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")
	getWindowLong := user32.NewProc("GetWindowLongW")
	setWindowLong := user32.NewProc("SetWindowLongW")
	setWindowPos := user32.NewProc("SetWindowPos")

	const (
		gwlStyle       = ^uintptr(15) // GWL_STYLE = -16
		wsMinimizeBox  = 0x00020000
		wsMaximizeBox  = 0x00010000
		swpFrameChange = 0x0020
		swpNoMove      = 0x0002
		swpNoSize      = 0x0001
		swpNoZOrder    = 0x0004
	)
	style, _, _ := getWindowLong.Call(hwnd, gwlStyle)
	setWindowLong.Call(hwnd, gwlStyle, style&^(uintptr(wsMinimizeBox)|uintptr(wsMaximizeBox)))
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

// 振铃边框轮换的颜色序列（COLORREF 即 0x00BBGGRR）：红、橙、绿、青、蓝、紫。
var ringBorderColors = []uint32{
	0x000000FF, // 红
	0x0000A5FF, // 橙
	0x0000FF00, // 绿
	0x00FFFF00, // 青
	0x00FF0000, // 蓝
	0x00FF00FF, // 紫
}

// setWindowBorderColor 设置窗口边框颜色（DWM，Win11）。失败静默——
// 老系统无边框着色能力，振铃提醒退化为任务栏闪烁。
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
