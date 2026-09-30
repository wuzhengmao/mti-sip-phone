//go:build linux

package ui

// Linux（X11）下的通话面板窗口控制：吸附工作区右下角、去标题栏、
// 不进任务栏、保持置顶。与 topmost_windows.go 的 Win32 实现对称，
// 两者互不影响（该文件仅 linux 构建编译）。
//
// 手段说明：
//   - _MOTIF_WM_HINTS 去装饰：运行时去标题栏/边框的传统做法，
//     GNOME/KDE/XFCE 等主流 WM 均支持（GLFW 无运行时改装饰的 API）；
//   - _NET_WM_STATE 加 SKIP_TASKBAR：按 EWMH 规范向根窗口发
//     ClientMessage，任务栏/分页器不再列出该窗口（同时消除中文标题
//     被部分面板按单字节读取造成的乱码）；
//   - _NET_WORKAREA 取工作区（不含任务栏/面板），缺失时回退整屏。
//
// cgo 说明：每次调用独立打开并关闭一条 X 连接，不与其他 goroutine
// 共享 Display，因此无需 XInitThreads（Xlib 的线程安全问题只出现在
// 多线程共享同一 Display 时）。libX11 本就是 GUI 构建的既有依赖
// （go-gl/glfw 的 -lX11），此处不引入新的构建依赖。

/*
#cgo LDFLAGS: -lX11
#include <X11/Xlib.h>
#include <X11/Xutil.h>  // XSizeHints / PMinSize / PMaxSize（Xlib.h 不含）
#include <X11/Xatom.h>
#include <string.h>

static Display *sp_open_display(void) {
	return XOpenDisplay(NULL);
}

static void sp_close_display(Display *d) {
	if (d != NULL) {
		XCloseDisplay(d);
	}
}

// 工作区（EWMH _NET_WORKAREA 的第一个桌面）：取到返回 1；
// 属性缺失（非 EWMH WM）时回退整屏尺寸并返回 0。
static int sp_workarea(Display *d, int *x, int *y, int *w, int *h) {
	Atom prop = XInternAtom(d, "_NET_WORKAREA", True);
	if (prop != None) {
		Atom type;
		int format;
		unsigned long nitems, after;
		unsigned char *data = NULL;
		if (XGetWindowProperty(d, DefaultRootWindow(d), prop, 0, 4, False,
		                       XA_CARDINAL, &type, &format, &nitems, &after,
		                       &data) == Success && data != NULL && nitems >= 4) {
			long *v = (long *)data;
			*x = (int)v[0];
			*y = (int)v[1];
			*w = (int)v[2];
			*h = (int)v[3];
			XFree(data);
			return 1;
		}
		if (data != NULL) {
			XFree(data);
		}
	}
	*x = 0;
	*y = 0;
	*w = DisplayWidth(d, DefaultScreen(d));
	*h = DisplayHeight(d, DefaultScreen(d));
	return 0;
}

// 窗口当前像素尺寸（客户区），供定位时避开窗口边框误差
static int sp_geometry(Display *d, Window win, int *w, int *h) {
	XWindowAttributes attr;
	if (!XGetWindowAttributes(d, win, &attr)) {
		return 0;
	}
	*w = attr.width;
	*h = attr.height;
	return 1;
}

// 去装饰：_MOTIF_WM_HINTS 的 MWM_HINTS_DECORATIONS 位
static void sp_undecorate(Display *d, Window win) {
	struct {
		unsigned long flags;
		unsigned long functions;
		unsigned long decorations;
		long input_mode;
		unsigned long status;
	} hints;
	hints.flags = 2;       // MWM_HINTS_DECORATIONS
	hints.functions = 0;
	hints.decorations = 0; // 不要装饰（标题栏/边框）
	hints.input_mode = 0;
	hints.status = 0;
	Atom prop = XInternAtom(d, "_MOTIF_WM_HINTS", False);
	XChangeProperty(d, win, prop, prop, 32, PropModeReplace,
	                (unsigned char *)&hints, 5);
	XFlush(d);
}

// 向根窗口发 _NET_WM_STATE ClientMessage（action=1 添加状态）
static void sp_add_state(Display *d, Window win, const char *name) {
	Atom state = XInternAtom(d, "_NET_WM_STATE", False);
	Atom atom = XInternAtom(d, name, False);
	XEvent ev;
	memset(&ev, 0, sizeof(ev));
	ev.xclient.type = ClientMessage;
	ev.xclient.window = win;
	ev.xclient.message_type = state;
	ev.xclient.format = 32;
	ev.xclient.data.l[0] = 1; // _NET_WM_STATE_ADD
	ev.xclient.data.l[1] = (long)atom;
	ev.xclient.data.l[2] = 0;
	ev.xclient.data.l[3] = 1; // 来源：应用
	ev.xclient.data.l[4] = 0;
	XSendEvent(d, DefaultRootWindow(d), False,
	           SubstructureRedirectMask | SubstructureNotifyMask, &ev);
	XFlush(d);
}

static void sp_skip_taskbar(Display *d, Window win) {
	sp_add_state(d, win, "_NET_WM_STATE_SKIP_TASKBAR");
}

static void sp_above(Display *d, Window win) {
	sp_add_state(d, win, "_NET_WM_STATE_ABOVE");
}

static void sp_move(Display *d, Window win, int x, int y) {
	XMoveWindow(d, win, x, y);
	XFlush(d);
}

// 锁定窗口尺寸：把 WM_NORMAL_HINTS 的 min/max 都设为目标值并立即拉到
// 该尺寸，阻止 fyne 按内容最小尺寸把窗口撑大（X11 侧等价于 Windows
// 的 setCallWindowClamp）。WM 会拒绝其它尺寸的 ConfigureRequest，
// 因此不会与 fyne 反复拉锯。
static void sp_lock_size(Display *d, Window win, int w, int h) {
	XSizeHints hints;
	long supplied = 0;
	memset(&hints, 0, sizeof(hints));
	XGetWMNormalHints(d, win, &hints, &supplied);
	hints.flags |= PMinSize | PMaxSize;
	hints.min_width = w;
	hints.max_width = w;
	hints.min_height = h;
	hints.max_height = h;
	XSetWMNormalHints(d, win, &hints);
	XResizeWindow(d, win, w, h);
	XFlush(d);
}
*/
import "C"

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"

	"mti-sip-phone/internal/logx"
)

// 通话面板尺寸（DIP）：与 Windows 保持一致。
//
// 注意：面板内容的**最小高度**约 162（行高被主题内边距撑大，实测
// vPad 每行 +10、行间 +4、标签每行 +8），fyne 会把窗口撑到内容最小
// 尺寸——所以「只调窗口高度」不起作用（实测被撑到 184→221，行距因此
// 显得很大）。Windows 侧靠 setCallWindowClamp 把窗口强制在 260×126
// （内容被压缩，观感才正确）；Linux 侧由 placeCallWindow 的
// sp_lock_size（X11 尺寸提示 min=max）做同样的事。改这两个常量时两边
// 一起改，否则两平台观感会分叉。
const (
	callPanelWidth      = 260
	callPanelHeight     = 126
	callPanelButtonSize = 36
)

// callPanelMargin 面板距工作区右下角的留白（物理像素），与 Windows 一致。
const callPanelMargin = 16

// moveCallWindowToCorner 把通话面板吸附到工作区右下角，并去标题栏、
// 移出任务栏、保持置顶。窗口句柄在 Show 后异步出现，带重试；
// 去装饰后边框几何变化，再延后校正两次（节奏同 Windows 实现）。
func moveCallWindowToCorner(w fyne.Window) {
	// 目标物理尺寸 = DIP 尺寸 × 缩放（与 Windows 实现同一算法）
	scale := w.Canvas().Scale()
	targetW := int(float32(callPanelWidth) * scale)
	targetH := int(float32(callPanelHeight) * scale)
	var contentMin fyne.Size // 仅用于日志：内容最小尺寸解释了窗口为何会被撑高
	if c := w.Content(); c != nil {
		contentMin = c.MinSize()
	}

	// 放到后台 goroutine：窗口句柄重试最长 1 秒，不能阻塞调用方
	go func() {
		var handle uintptr
		for i := 0; i < 20; i++ {
			if handle = callPanelHandle(w); handle != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if handle == 0 {
			logx.Logger().Warn("通话面板未取得 X11 窗口句柄，跳过定位")
			return
		}
		placeCallWindow(handle, targetW, targetH, scale, contentMin)
		for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond} {
			time.Sleep(delay)
			placeCallWindow(handle, targetW, targetH, scale, contentMin)
		}
	}()
}

// callPanelHandle 取通话面板的 X11 窗口号（未创建时返回 0）。
// RunNative 需在主线程执行（读取 GLFW 窗口状态），故经 fyne.DoAndWait
// 排队；本函数只在后台 goroutine 中调用（DoAndWait 不可在主线程调用）。
func callPanelHandle(w fyne.Window) uintptr {
	var handle uintptr
	fyne.DoAndWait(func() {
		nw, ok := w.(driver.NativeWindow)
		if !ok {
			return
		}
		nw.RunNative(func(ctx any) {
			if x11ctx, ok := ctx.(driver.X11WindowContext); ok {
				handle = x11ctx.WindowHandle
			}
		})
	})
	return handle
}

// placeCallWindow 对已存在的窗口施加：去装饰 + 移出任务栏 + 置顶 +
// 定位到工作区右下角。targetW/targetH 为期望的物理像素尺寸，仅用于
// 诊断——实际尺寸被 fyne/WM 改动时打 Warn（该日志是排查"面板行距
// 不符预期"的第一手线索）。
func placeCallWindow(handle uintptr, targetW, targetH int, scale float32, contentMin fyne.Size) {
	d := C.sp_open_display()
	if d == nil {
		logx.Logger().Warn("无法连接 X 显示，通话面板定位跳过")
		return
	}
	defer C.sp_close_display(d)

	win := C.Window(handle)
	C.sp_undecorate(d, win)
	C.sp_skip_taskbar(d, win)
	C.sp_above(d, win)                                     // fyne 侧的置顶兜底：去装饰后重述一次
	C.sp_lock_size(d, win, C.int(targetW), C.int(targetH)) // 尺寸锁定（内容最小尺寸更大时会被 fyne 撑高，见常量注释）

	var w, h C.int
	if C.sp_geometry(d, win, &w, &h) == 0 {
		logx.Logger().Warn("读取通话面板窗口尺寸失败，跳过定位")
		return
	}
	if int(w) != targetW || int(h) != targetH {
		logx.Logger().Warn("通话面板实际尺寸与目标不一致",
			"actual", fmt.Sprintf("%dx%d", int(w), int(h)),
			"target", fmt.Sprintf("%dx%d", targetW, targetH),
			"scale", scale)
	}
	var wx, wy, ww, wh C.int
	C.sp_workarea(d, &wx, &wy, &ww, &wh)

	x := wx + ww - w - callPanelMargin
	y := wy + wh - h - callPanelMargin
	C.sp_move(d, win, x, y)
	logx.Logger().Debug("通话面板已吸附右下角",
		"x", int(x), "y", int(y), "w", int(w), "h", int(h),
		"contentMin", fmt.Sprintf("%.0fx%.0f", contentMin.Width, contentMin.Height))
}

// callWindowTitle 通话面板标题：Linux 用 ASCII——面板已不进任务栏，
// 万一被 WM 忽略 skip-taskbar 而显示出来，也不会出现中文乱码
// （部分任务栏按 WM_NAME 单字节读取）。
func callWindowTitle(account string) string {
	title := "SIP"
	if account != "" {
		title += " - " + account
	}
	return title
}

// markTrayQuit 标记托盘「退出」项：Fyne 要求菜单最后一项带 IsQuit，
// 否则会自动补一个本地化的 Quit 项（详见 driver_desktop.go 的
// addMissingQuitForMenu）。仅 Linux 打标记，Windows 托盘保持现状。
func markTrayQuit(item *fyne.MenuItem) {
	item.IsQuit = true
}

// 以下函数在 X11 下无对应实现，保持空实现以对齐 Windows 侧的调用点。

// setTopMost 可撤销置顶：Linux 下置顶由 WM 决定，暂不实现。
func setTopMost(on bool) {}

func findWindowByTitlePrefix(prefix string) uintptr { return 0 }

func flashWindowEx(hwnd uintptr)         {}
func stopFlashWindowEx(hwnd uintptr)     {}
func setForegroundWindow(hwnd uintptr)   {}
func forceForegroundWindow(hwnd uintptr) {}

func ringBorderFlash(hwnd uintptr, stop <-chan struct{}) {}
