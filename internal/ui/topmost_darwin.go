//go:build darwin

package ui

// macOS（AppKit）下的通话面板窗口控制：吸附工作区右下角、悬浮在最前、
// 不进 Mission Control / 窗口切换器、可拖动并记住位置（REQ-UI-040）。
//
// 与 Windows/Linux 的差异：
//   - 通话面板是 Fyne 的 splash 窗口（GLFW 创建时就无边框），不需要再去装饰、
//     也不能改 styleMask（GLFW 的几何缓存会失准）；这里只调整 AppKit 的
//     窗口层级与 collection behavior。
//   - **AppKit 只能在主线程调用**：所有 C 调用都包在 fyne.DoAndWait 里，并且
//     一律由后台 goroutine 发起（DoAndWait 在主线程调用会 panic）。例外是
//     屏幕尺寸：screenBounds/screenWorkArea 是同步接口、由主线程调用，所以
//     改为"主线程刷新到原子量缓存 + 查询只读缓存"（见下方屏幕信息一节）。
//   - 坐标口径与 Windows/Linux 一致：**主屏左上角为原点的物理像素**；
//     Cocoa 是左下原点、单位是 point，换算都在 topmost_darwin.m 里做。
//     多显示器下以主屏为基准（跨屏拖动的位置可能不精确，面板默认在主屏）。

/*
#cgo darwin LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include <stdint.h>

// 窗口指针按 uintptr_t 传：Go 侧只有 uintptr（driver.MacWindowContext.NSWindow），
// 转 unsafe.Pointer 会被 go vet 的 unsafeptr 检查判为 "possible misuse"。
void mti_style_panel(uintptr_t win, int floating);
void mti_set_level(uintptr_t win, int floating);
void mti_corner(uintptr_t win, int dx);
void mti_frame(uintptr_t win, int *x, int *y, int *w, int *h);
void mti_move(uintptr_t win, int x, int y);
int  mti_cursor(int *x, int *y);
void mti_screen(int *x, int *y, int *w, int *h);
void mti_workarea(int *x, int *y, int *w, int *h);
*/
import "C"

import (
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"

	"mti-sip-phone/internal/logx"
)

// 通话面板与更新提示窗尺寸（DIP）：与 Windows/Linux 一致，改时三处一起改。
const (
	callPanelWidth      = 260
	callPanelHeight     = 144
	callPanelButtonSize = 36
)

const (
	updatePanelWidth  = 340
	updatePanelHeight = 240
)

// callPanelMargin 面板距工作区右下角的留白在 .m 里（Cocoa point；16pt 与
// Windows 的 16 物理像素观感一致）。

// ---- 窗口句柄与主线程调用 ----

var (
	darwinPanelMu sync.Mutex
	darwinPanelH  uintptr
)

// darwinHandle 在后台 goroutine 里取窗口的 NSWindow（RunNative 要主线程）。
func darwinHandle(w fyne.Window) uintptr {
	var h uintptr
	fyne.DoAndWait(func() {
		nw, ok := w.(driver.NativeWindow)
		if !ok {
			return
		}
		nw.RunNative(func(ctx any) {
			if mac, ok := ctx.(driver.MacWindowContext); ok {
				h = mac.NSWindow
			}
		})
	})
	return h
}

// darwinWaitHandle 等窗口真正建出来（首次 Show 是异步的），最多 1 秒。
func darwinWaitHandle(stop <-chan struct{}, w fyne.Window) uintptr {
	for i := 0; i < 20; i++ {
		if stop != nil {
			select {
			case <-stop:
				return 0
			default:
			}
		}
		if h := darwinHandle(w); h != 0 {
			return h
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

func darwinStylePanel(h uintptr, floating bool) {
	fyne.DoAndWait(func() {
		f := C.int(0)
		if floating {
			f = 1
		}
		C.mti_style_panel(C.uintptr_t(h), f)
	})
}

func darwinSetLevel(h uintptr, floating bool) {
	fyne.DoAndWait(func() {
		f := C.int(0)
		if floating {
			f = 1
		}
		C.mti_set_level(C.uintptr_t(h), f)
	})
}

func darwinCorner(h uintptr, dx int) {
	fyne.DoAndWait(func() { C.mti_corner(C.uintptr_t(h), C.int(dx)) })
}

func darwinFrame(h uintptr) (x, y, w, hh int, ok bool) {
	fyne.DoAndWait(func() {
		var cx, cy, cw, ch C.int
		C.mti_frame(C.uintptr_t(h), &cx, &cy, &cw, &ch)
		if cw > 0 && ch > 0 {
			x, y, w, hh, ok = int(cx), int(cy), int(cw), int(ch), true
		}
	})
	return
}

func darwinMove(h uintptr, x, y int) {
	fyne.DoAndWait(func() { C.mti_move(C.uintptr_t(h), C.int(x), C.int(y)) })
}

func darwinCursor() (x, y int, ok bool) {
	fyne.DoAndWait(func() {
		var cx, cy C.int
		if C.mti_cursor(&cx, &cy) != 0 {
			x, y, ok = int(cx), int(cy), true
		}
	})
	return
}

// ---- 面板摆位 ----

// applyCallPanelWindow 把面板设成悬浮工具窗并放到工作区右下角。
// 在后台 goroutine 中执行（AppKit 调用要排队到主线程）。
func applyCallPanelWindow(w fyne.Window, xShift int) {
	refreshScreenMetricsAsync()
	h := darwinWaitHandle(nil, w)
	if h == 0 {
		logx.Logger().Debug("未取得通话面板 NSWindow，跳过窗口布置")
		return
	}
	darwinPanelMu.Lock()
	darwinPanelH = h
	darwinPanelMu.Unlock()
	darwinStylePanel(h, true)
	darwinCorner(h, xShift)
	// 窗口刚 Show 时尺寸可能还没由 Fyne 应用到位，延后再对一次（同 Windows/Linux）
	for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond, 800 * time.Millisecond} {
		time.Sleep(delay)
		darwinCorner(h, xShift)
	}
}

func showCallWindow(w fyne.Window) {
	// 主线程直接刷新屏幕尺寸：紧接着 placeCallWindow 就要用 screenBounds
	// 校验保存的位置，缓存必须是新的（DoAndWait 在主线程会 panic，这里直接调）。
	refreshScreenMetrics()
	w.Show()
	go applyCallPanelWindow(w, int(instanceColumnShiftPx(w.Canvas().Scale())))
}

func moveCallWindowToCorner(w fyne.Window) {
	if w == nil {
		return
	}
	xShift := int(instanceColumnShiftPx(w.Canvas().Scale()))
	go applyCallPanelWindow(w, xShift)
}

// placeVideoWindow 视频窗口：macOS 没有视频（摄像头采集未实现），保持空实现。
func placeVideoWindow(w fyne.Window) {}

// raiseVideoWindow / setVideoWindowTopmost 同上（没有视频窗口）。
func raiseVideoWindow(_ fyne.Window, _ bool)      {}
func setVideoWindowTopmost(_ fyne.Window, _ bool) {}

// ---- 更新提示窗 ----

func showUpdateWindow(w fyne.Window) {
	w.Show()
	go func() {
		if h := darwinWaitHandle(nil, w); h != 0 {
			darwinStylePanel(h, true)
		}
	}()
}

func moveUpdateWindowToCorner(w fyne.Window) {
	if w == nil {
		return
	}
	xShift := int(instanceColumnShiftPx(w.Canvas().Scale()))
	go func() {
		h := darwinWaitHandle(nil, w)
		if h == 0 {
			logx.Logger().Warn("未找到更新提示窗，跳过右下角定位")
			return
		}
		darwinStylePanel(h, true)
		darwinCorner(h, xShift)
		for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond} {
			time.Sleep(delay)
			darwinCorner(h, xShift)
		}
	}()
}

// ---- 标题 ----

// updateWindowTitle 更新提示窗标题（macOS 的窗口标题只在窗口菜单里出现）。
func updateWindowTitle() string { return "更新提示" }

// callWindowTitle 通话面板标题：用 ASCII 前缀，避免与视频窗口混淆
// （Windows 侧按 "通话面板" 前缀找窗口；macOS 不靠标题找窗口）。
func callWindowTitle(account string) string {
	title := "SIP"
	if account != "" {
		title += " - " + account
	}
	return title
}

func videoWindowTitle(account string) string {
	title := "视频通话"
	if account != "" {
		title += " - " + account
	}
	return title
}

// markTrayQuit 标记托盘「退出」项：Fyne 要求菜单最后一项带 IsQuit，否则会
// 自动补一个本地化的 Quit 项（macOS 中文环境下会出现两个「退出」）。
func markTrayQuit(item *fyne.MenuItem) { item.IsQuit = true }

// ---- 屏幕信息（缓存；AppKit 只在主线程读） ----
//
// screenBounds/screenWorkArea 是**同步接口**，调用点可能在主线程（ui.go 的
// placeCallWindow），不能在那里用 DoAndWait（会 panic）。所以屏幕尺寸只在
// 主线程路径（showCallWindow）与后台 goroutine 里刷新到原子量，查询只读缓存。

var (
	dScreenX, dScreenY, dScreenW, dScreenH atomic.Int32
	dWorkW, dWorkH                         atomic.Int32
)

// refreshScreenMetrics 直接读主屏尺寸：**只能在主线程调用**（不经 DoAndWait）。
func refreshScreenMetrics() {
	var x, y, w, h, wx, wy, ww, wh C.int
	C.mti_screen(&x, &y, &w, &h)
	C.mti_workarea(&wx, &wy, &ww, &wh)
	dScreenX.Store(int32(x))
	dScreenY.Store(int32(y))
	dScreenW.Store(int32(w))
	dScreenH.Store(int32(h))
	dWorkW.Store(int32(ww))
	dWorkH.Store(int32(wh))
}

// refreshScreenMetricsAsync 后台 goroutine 用的版本（排队到主线程执行）。
func refreshScreenMetricsAsync() { fyne.DoAndWait(refreshScreenMetrics) }

// screenBounds 屏幕边界（主屏，物理像素，左上原点）。读缓存，未刷新过时返回全零
// （调用方 windowPosUsable 对 sw<=0 不拦）。
func screenBounds() (x, y, w, h int) {
	return int(dScreenX.Load()), int(dScreenY.Load()), int(dScreenW.Load()), int(dScreenH.Load())
}

// screenWorkArea 工作区尺寸（物理像素，扣掉菜单栏与 Dock）。
func screenWorkArea() (float32, float32) {
	w, h := dWorkW.Load(), dWorkH.Load()
	if w <= 0 || h <= 0 {
		return 1280, 800
	}
	return float32(w), float32(h)
}

// ---- 通话面板拖动（REQ-UI-040）----

type callPanelDragSession struct {
	stop     chan struct{}
	stopOnce sync.Once
	mu       sync.Mutex
	moved    bool
	posX     int
	posY     int
}

var (
	callPanelDragMu  sync.Mutex
	callPanelDragCur *callPanelDragSession
)

// beginCallPanelDrag 开始拖动：起一条会话 goroutine 以约 60Hz 跟随光标移动窗口。
// 主线程只做非阻塞的启动/停止（取句柄要 DoAndWait，绝不能在这里同步做）。
func beginCallPanelDrag(w fyne.Window) {
	if w == nil {
		return
	}
	cancelCallPanelDrag(w)
	d := &callPanelDragSession{stop: make(chan struct{})}
	callPanelDragMu.Lock()
	callPanelDragCur = d
	callPanelDragMu.Unlock()
	go d.run(w)
}

func (d *callPanelDragSession) run(w fyne.Window) {
	h := darwinWaitHandle(d.stop, w)
	if h == 0 {
		logx.Logger().Debug("未取得通话面板 NSWindow，跳过拖动")
		return
	}
	winX, winY, _, _, ok := darwinFrame(h)
	if !ok {
		return
	}
	curX, curY, ok := darwinCursor()
	if !ok {
		return
	}
	anchorWinX, anchorWinY := winX, winY
	anchorCurX, anchorCurY := curX, curY

	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			// 最终位置就是"最后一次设过的目标"，已记录在 d.posX/d.posY 里
			return
		case <-ticker.C:
			cx, cy, ok := darwinCursor()
			if !ok {
				continue
			}
			x, y := dragTarget(anchorWinX, anchorWinY, cx, cy, anchorCurX, anchorCurY)
			darwinMove(h, x, y)
			d.mu.Lock()
			d.moved, d.posX, d.posY = true, x, y
			d.mu.Unlock()
		}
	}
}

func stopCallPanelDrag() *callPanelDragSession {
	callPanelDragMu.Lock()
	d := callPanelDragCur
	callPanelDragCur = nil
	callPanelDragMu.Unlock()
	if d == nil {
		return nil
	}
	d.stopOnce.Do(func() { close(d.stop) })
	return d
}

// endCallPanelDrag 结束拖动并返回最终位置。
//
// **不等待会话 goroutine 退出**：会话最后一次移动是把目标坐标排到主线程执行
// 的（DoAndWait），而本函数就在主线程上——等它退出会和主线程互等，UI 卡 500ms。
// 位置在每次移动时已记下，直接读即可；会话自己会在下一个 select 退出。
func endCallPanelDrag(w fyne.Window) (x, y int, ok bool) {
	d := stopCallPanelDrag()
	if d == nil {
		return 0, 0, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.moved {
		return 0, 0, false
	}
	return d.posX, d.posY, true
}

// cancelCallPanelDrag 丢弃拖动会话（窗口隐藏/挂断时兜底：DragEnd 可能不来）。
// 同样不等待（理由见 endCallPanelDrag）。
func cancelCallPanelDrag(w fyne.Window) {
	stopCallPanelDrag()
}

// moveCallPanelToSaved 把通话面板移到保存的位置（异步重试 + 延迟校正）。
func moveCallPanelToSaved(w fyne.Window, x, y int) {
	if w == nil {
		return
	}
	go func() {
		h := darwinWaitHandle(nil, w)
		if h == 0 {
			logx.Logger().Warn("未取得通话面板 NSWindow，跳过位置恢复")
			return
		}
		darwinMove(h, x, y)
		for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond, 800 * time.Millisecond} {
			time.Sleep(delay)
			darwinMove(h, x, y)
		}
	}()
}

// moveVideoWindowToSaved 没有视频窗口，空实现（对齐调用点）。
func moveVideoWindowToSaved(w fyne.Window, x, y int) {}

// setVideoWindowLock 没有视频窗口可锁。
func setVideoWindowLock(w fyne.Window, locked bool) {}

// ---- 置顶 / 激活 ----

// setTopMost 可撤销置顶：作用于最近一次布置过的通话面板。
func setTopMost(on bool) {
	darwinPanelMu.Lock()
	h := darwinPanelH
	darwinPanelMu.Unlock()
	if h == 0 {
		return
	}
	go darwinSetLevel(h, on)
}

// ---- 其余调用点（Windows 专有能力的空实现） ----

func findWindowByTitlePrefix(prefix string) uintptr { return 0 }

func flashWindowEx(hwnd uintptr)                         {}
func stopFlashWindowEx(hwnd uintptr)                     {}
func setForegroundWindow(hwnd uintptr)                   {}
func forceForegroundWindow(hwnd uintptr)                 {}
func ringBorderFlash(hwnd uintptr, stop <-chan struct{}) {}

func startVideoWindowWatch(w fyne.Window) {}
func stopVideoWindowWatch(w fyne.Window)  {}

func suspendVideoWindowLockClamp() {}
func resumeVideoWindowLockClamp()  {}

// SetVideoWindowMoveHook 没有视频窗口，存下也不会有回调。
func SetVideoWindowMoveHook(fn func(x, y int)) {}

// startTrayIconPromotion macOS 没有"通知区常显"这类设置项。
func startTrayIconPromotion() {}
