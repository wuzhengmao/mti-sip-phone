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

// X 显示是否真的可连接：Wayland 会话里 DISPLAY 常被无条件导出，但 Xwayland
// 未必在跑，此时若强行选 X11 后端会让应用完全起不来，故先探测一次。
static int sp_display_available(void) {
	Display *d = XOpenDisplay(NULL);
	if (d == NULL) {
		return 0;
	}
	XCloseDisplay(d);
	return 1;
}

// 工作区（EWMH _NET_WORKAREA 的第一个桌面）：取到返回 1；
// 属性缺失（非 EWMH WM）时回退整屏尺寸并返回 0。
static void sp_move_resize(Display *d, Window w, int x, int y, int cw, int ch) {
	XMoveResizeWindow(d, w, x, y, (unsigned int)cw, (unsigned int)ch);
	XFlush(d);
}

static void sp_screen(Display *d, int *w, int *h) {
	*w = DisplayWidth(d, DefaultScreen(d));
	*h = DisplayHeight(d, DefaultScreen(d));
}

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

// 向窗口添加一个 _NET_WM_STATE 状态：既写属性（EWMH 标准形式，XA_ATOM
// 列表）又发 ClientMessage。
//
// 两种形式都要发：ClientMessage 是 X11 窗口管理器处理「运行中变更」的
// 路径；而 Xwayland 下没有独立的 X11 窗口管理器，消息无人处理（实测任务
// 栏仍列出通话面板），只能靠属性——合成器（labwc/wlroots 的 XWM）会在
// 属性变更时读取它。
static void sp_add_state(Display *d, Window win, const char *name) {
	Atom state = XInternAtom(d, "_NET_WM_STATE", False);
	Atom atom = XInternAtom(d, name, False);

	// 1) 属性形式：读出已有列表（最多 8 项）后追加，避免覆盖其它状态
	Atom list[9];
	int n = 0;
	int found = 0;
	Atom type;
	int format;
	unsigned long nitems, after;
	unsigned char *data = NULL;
	if (XGetWindowProperty(d, win, state, 0, 8, False, XA_ATOM, &type, &format,
	                       &nitems, &after, &data) == Success && data != NULL) {
		unsigned long *v = (unsigned long *)data; // format=32 时按 long 数组返回
		for (unsigned long i = 0; i < nitems && n < 8; i++) {
			list[n++] = (Atom)v[i];
			if ((Atom)v[i] == atom) found = 1;
		}
		XFree(data);
	}
	if (!found) {
		list[n++] = atom;
		XChangeProperty(d, win, state, XA_ATOM, 32, PropModeReplace,
		                (unsigned char *)list, n);
	}

	// 2) ClientMessage 形式（action=1 添加状态），发往根窗口
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

// 从窗口的 _NET_WM_STATE 里移除一个状态（属性与 ClientMessage 两种形式都做）。
static void sp_remove_state(Display *d, Window win, const char *name) {
	Atom state = XInternAtom(d, "_NET_WM_STATE", False);
	Atom atom = XInternAtom(d, name, False);

	// 1) 属性形式：读出列表，剔除目标后写回
	Atom list[9];
	int n = 0;
	Atom type;
	int format;
	unsigned long nitems, after;
	unsigned char *data = NULL;
	if (XGetWindowProperty(d, win, state, 0, 8, False, XA_ATOM, &type, &format,
	                       &nitems, &after, &data) == Success && data != NULL) {
		unsigned long *v = (unsigned long *)data;
		for (unsigned long i = 0; i < nitems && n < 8; i++) {
			if ((Atom)v[i] != atom) {
				list[n++] = (Atom)v[i];
			}
		}
		XFree(data);
		XChangeProperty(d, win, state, XA_ATOM, 32, PropModeReplace,
		                (unsigned char *)list, n);
	}

	// 2) ClientMessage 形式（action=0 移除状态），发往根窗口
	XEvent ev;
	memset(&ev, 0, sizeof(ev));
	ev.xclient.type = ClientMessage;
	ev.xclient.window = win;
	ev.xclient.message_type = state;
	ev.xclient.format = 32;
	ev.xclient.data.l[0] = 0; // _NET_WM_STATE_REMOVE
	ev.xclient.data.l[1] = (long)atom;
	ev.xclient.data.l[2] = 0;
	ev.xclient.data.l[3] = 1; // 来源：应用
	ev.xclient.data.l[4] = 0;
	XSendEvent(d, DefaultRootWindow(d), False,
	           SubstructureRedirectMask | SubstructureNotifyMask, &ev);
	XFlush(d);
}

// 把窗口提到栈顶（XRaiseWindow）。X11 下"提到最前"就是这一件事，
// 是否获得输入焦点由窗口管理器按自己的策略决定。
static void sp_raise(Display *d, Window win) {
	XRaiseWindow(d, win);
	XFlush(d);
}

// 置顶开关：_NET_WM_STATE_ABOVE（EWMH 的"置顶"状态）。
// on=0 时移除——Wayland 合成器下能否生效由合成器决定（Xwayland 下 labwc
// 等会读属性；纯 Wayland 会话本就用不了 X11 手段）。
static void sp_set_above(Display *d, Window win, int on) {
	if (on) {
		sp_add_state(d, win, "_NET_WM_STATE_ABOVE");
	} else {
		sp_remove_state(d, win, "_NET_WM_STATE_ABOVE");
	}
}

static void sp_move(Display *d, Window win, int x, int y) {
	XMoveWindow(d, win, x, y);
	XFlush(d);
}

// 锁定窗口尺寸：把 WM_NORMAL_HINTS 的 min/max 都设为目标值并立即拉到
// 该尺寸，阻止 fyne 按内容最小尺寸把窗口撑大（X11 侧等价于 Windows
// 的 sp_lock_size）。WM 会拒绝其它尺寸的 ConfigureRequest，
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

// 解除尺寸锁定（清掉 sp_lock_size 设的 PMinSize/PMaxSize）。
static void sp_unlock_size(Display *d, Window win) {
	XSizeHints hints;
	long supplied = 0;
	memset(&hints, 0, sizeof(hints));
	XGetWMNormalHints(d, win, &hints, &supplied);
	hints.flags &= ~(PMinSize | PMaxSize);
	XSetWMNormalHints(d, win, &hints);
	XFlush(d);
}

// 屏幕绝对光标位置（根窗口坐标系）——拖动窗口用。窗口跟着光标移动时，
// 相对坐标会自反馈，只有根坐标是稳的。
static int sp_cursor(Display *d, int *x, int *y) {
	Window root = DefaultRootWindow(d);
	Window rr, child;
	int rx, ry, wx, wy;
	unsigned int mask;
	if (!XQueryPointer(d, root, &rr, &child, &rx, &ry, &wx, &wy, &mask)) {
		return 0;
	}
	*x = rx;
	*y = ry;
	return 1;
}

// 窗口左上角在根窗口坐标系里的位置。
// **不要**用 XGetWindowAttributes 的 x/y：装饰窗口被 WM reparent 到 frame 里，
// 那个 x/y 是相对父窗口（frame）的，读回来会差一条标题栏。
static int sp_abs_pos(Display *d, Window win, int *x, int *y) {
	Window root = DefaultRootWindow(d);
	Window child;
	int rx, ry;
	if (!XTranslateCoordinates(d, win, root, 0, 0, &rx, &ry, &child)) {
		return 0;
	}
	*x = rx;
	*y = ry;
	return 1;
}

// 一次取回窗口的绝对位置与尺寸（视频窗口守卫每拍要用）。
static int sp_geometry_abs(Display *d, Window win, int *x, int *y, int *w, int *h) {
	XWindowAttributes attr;
	if (!XGetWindowAttributes(d, win, &attr)) {
		return 0;
	}
	if (!sp_abs_pos(d, win, x, y)) {
		return 0;
	}
	*w = attr.width;
	*h = attr.height;
	return 1;
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
// （内容被压缩，观感才正确）；Linux 侧由 placeCornerWindow 的
// sp_lock_size（X11 尺寸提示 min=max）做同样的事。改这两个常量时两边
// 一起改，否则两平台观感会分叉。
const (
	callPanelWidth      = 260
	callPanelHeight     = 144
	callPanelButtonSize = 36
)

// 更新提示窗尺寸（DIP）：与 Windows 保持一致，改时两边一起改。
const (
	updatePanelWidth  = 340
	updatePanelHeight = 240
)

// callPanelMargin 面板距工作区右下角的留白（物理像素），与 Windows 一致。
const callPanelMargin = 16

// moveCallWindowToCorner 把通话面板吸附到工作区右下角，并去标题栏、
// 移出任务栏、保持置顶。窗口句柄在 Show 后异步出现，带重试；
// 去装饰后边框几何变化，再延后校正两次（节奏同 Windows 实现）。
func moveCallWindowToCorner(w fyne.Window) {
	moveWindowToCorner(w, callPanelWidth, callPanelHeight, "通话面板")
}

// raiseVideoWindow 把视频窗口提到最前面（用户要求：每次打开视频窗口都放最前）。
// X11 下用 XRaiseWindow；窗口号经 RunNative 取（同 placeVideoWindow）。
func raiseVideoWindow(w fyne.Window, keepTopmost bool) {
	if w == nil {
		return
	}
	go func() {
		var handle uintptr
		for i := 0; i < 20; i++ {
			if handle = callPanelHandle(w); handle != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if handle == 0 {
			logx.Logger().Debug("未取得视频窗口 X11 句柄，跳过提到最前")
			return
		}
		d := C.sp_open_display()
		if d == nil {
			return
		}
		defer C.sp_close_display(d)
		C.sp_raise(d, C.Window(handle))
		logx.Logger().Info("视频窗口已提到最前", "x11", handle, "保持置顶", keepTopmost)
	}()
}

// setVideoWindowTopmost 视频窗口置顶开关（用户要求，可撤销）。
// X11 下用 EWMH 的 _NET_WM_STATE_ABOVE；窗口号经 RunNative 取（同 placeVideoWindow）。
func setVideoWindowTopmost(w fyne.Window, on bool) {
	if w == nil {
		return
	}
	go func() { // fyne.DoAndWait 不可在主线程调用（本函数由 UI 事件回调触发）
		handle := callPanelHandle(w)
		if handle == 0 {
			logx.Logger().Debug("未取得视频窗口 X11 句柄，跳过置顶")
			return
		}
		d := C.sp_open_display()
		if d == nil {
			return
		}
		defer C.sp_close_display(d)
		v := C.int(0)
		if on {
			v = 1
		}
		C.sp_set_above(d, C.Window(handle), v)
	}()
}

// placeVideoWindow 把视频窗口摆到通话面板正上方、右边缘对齐（**只移动**）。
// 尺寸归 Fyne 的 Window.Resize——SetWindowPos/XMoveResizeWindow 按窗口矩形设尺寸
// 会把带标题栏的客户区压小一圈，宽高比变化后在左右补出黑边。
func placeVideoWindow(w fyne.Window) {
	if w == nil {
		return
	}
	// 程序化摆放：位置守卫的静默期（不把它当成"用户拖动"，锁定期间也不回拉）
	markVideoProgMove()
	scale := w.Canvas().Scale()
	go func() {
		var handle uintptr
		for i := 0; i < 20; i++ {
			if handle = callPanelHandle(w); handle != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if handle == 0 {
			logx.Logger().Debug("未取得视频窗口 X11 句柄，跳过定位")
			return
		}
		d := C.sp_open_display()
		if d == nil {
			return
		}
		defer C.sp_close_display(d)
		win := C.Window(handle)

		move := func() {
			var vw, vh C.int
			if C.sp_geometry(d, win, &vw, &vh) == 0 || vw <= 0 || vh <= 0 {
				return
			}
			var wx, wy, ww, wh C.int
			C.sp_workarea(d, &wx, &wy, &ww, &wh)
			panelH := C.int(float32(callPanelHeight) * scale)
			margin := C.int(float32(callPanelMargin) * scale)
			gap := C.int(8 * scale)
			x := wx + ww - vw - margin
			y := wy + wh - panelH - margin - gap - vh
			if y < wy {
				y = wy
			}
			C.sp_move(d, win, x, y)
		}
		move()
		for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond, 800 * time.Millisecond} {
			time.Sleep(delay)
			move()
		}
	}()
}

// showUpdateWindow 显示更新提示窗。Windows 侧要在"第一次显示"的同一步里
// 把窗口改成工具窗口（否则外壳会建出任务栏按钮，关掉后还残留）；X11/Wayland
// 没有这个问题，直接显示即可。
func showUpdateWindow(w fyne.Window) { w.Show() }

// showCallWindow 显示通话面板（X11/Wayland 不需要为任务栏做额外处理，
// 面板的去任务栏条目由 X11 属性负责，见本文件内的 moveCallWindowToCorner）。
func showCallWindow(w fyne.Window) { w.Show() }

// moveUpdateWindowToCorner 把更新提示窗吸附到工作区右下角（同通话面板：
// 去标题栏、移出任务栏、置顶、尺寸锁定）。尺寸取窗口当前值
// （内容自适应调整过），而不是固定常量。
func moveUpdateWindowToCorner(w fyne.Window) {
	size := w.Canvas().Size()
	moveWindowToCorner(w, size.Width, size.Height, "更新提示窗")
}

// moveWindowToCorner 通用"吸附右下角"实现（name 仅用于日志）。
func moveWindowToCorner(w fyne.Window, wDip, hDip float32, name string) {
	// 目标物理尺寸 = DIP 尺寸 × 缩放（与 Windows 实现同一算法）
	scale := w.Canvas().Scale()
	targetW := int(wDip * scale)
	targetH := int(hDip * scale)
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
			logx.Logger().Warn(name + "未取得 X11 窗口句柄，跳过定位")
			return
		}
		placeCornerWindow(handle, targetW, targetH, scale, contentMin, name)
		// 与 Windows 同理：去装饰是绕过 GLFW 改窗口的，GLFW 缓存的边框尺寸会过期，
		// 导致 Fyne 认为画布比真实客户区高一条标题栏、内容整体下移顶掉第一行。
		// 重新 Resize 一次触发客户区重算（尺寸与锁定的目标一致，不冲突）。
		fyne.Do(func() { w.Resize(fyne.NewSize(wDip, hDip)) })
		for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond} {
			time.Sleep(delay)
			placeCornerWindow(handle, targetW, targetH, scale, contentMin, name)
		}
	}()
}

// updateWindowTitle 更新提示窗标题：Linux 用 ASCII——窗口不进任务栏，
// 万一被 WM 忽略 skip-taskbar 而显示出来，也不会出现中文乱码。
func updateWindowTitle() string { return "Update" }

// x11DisplayAvailable 探测 $DISPLAY 是否真的可连接（调用点见 glfw_linux.go
// 的 preferX11OnWayland：Wayland 会话下是否能把后端固定到 X11）。
func x11DisplayAvailable() bool {
	return C.sp_display_available() != 0
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

// placeCornerWindow 对已存在的窗口施加：去装饰 + 移出任务栏 + 置顶 +
// 定位到工作区右下角。targetW/targetH 为期望的物理像素尺寸，仅用于
// 诊断——实际尺寸被 fyne/WM 改动时打 Warn（该日志是排查"面板行距
// 不符预期"的第一手线索）。
func placeCornerWindow(handle uintptr, targetW, targetH int, scale float32, contentMin fyne.Size, name string) {
	d := C.sp_open_display()
	if d == nil {
		logx.Logger().Warn("无法连接 X 显示，" + name + "定位跳过")
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
		logx.Logger().Warn("读取" + name + "窗口尺寸失败，跳过定位")
		return
	}
	if int(w) != targetW || int(h) != targetH {
		logx.Logger().Warn(name+"实际尺寸与目标不一致",
			"actual", fmt.Sprintf("%dx%d", int(w), int(h)),
			"target", fmt.Sprintf("%dx%d", targetW, targetH),
			"scale", scale)
	}
	var wx, wy, ww, wh C.int
	C.sp_workarea(d, &wx, &wy, &ww, &wh)

	// 多实例（沙箱槽位）：整列按槽位左移，实例之间不互相叠住（REQ-NFR-007）
	x := wx + ww - w - callPanelMargin - C.int(instanceColumnShiftPx(scale))
	y := wy + wh - h - callPanelMargin
	C.sp_move(d, win, x, y)
	logx.Logger().Debug(name+"已吸附右下角",
		"x", int(x), "y", int(y), "w", int(w), "h", int(h),
		"contentMin", fmt.Sprintf("%.0fx%.0f", contentMin.Width, contentMin.Height))
}

// videoWindowTitle 视频窗口标题（Windows 侧靠前缀区分窗口，这里保持一致）。
func videoWindowTitle(account string) string {
	title := "视频通话"
	if account != "" {
		title += " - " + account
	}
	return title
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

// screenWorkArea 屏幕工作区尺寸（**物理像素**）——需求 6 用它把视频窗口
// 限制在屏幕的 1/2 以内；调用方按 Canvas().Scale() 换算成 DIP。
// 取不到 _NET_WORKAREA 时回退整屏，最终回退一个保守默认值。
func screenWorkArea() (float32, float32) {
	d := C.sp_open_display()
	if d == nil {
		return 1280, 800
	}
	defer C.XCloseDisplay(d)
	var wx, wy, ww, wh C.int
	C.sp_workarea(d, &wx, &wy, &ww, &wh)
	if ww <= 0 || wh <= 0 {
		C.sp_screen(d, &ww, &wh)
	}
	if ww <= 0 || wh <= 0 {
		return 1280, 800
	}
	return float32(ww), float32(wh)
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

// ---------------------------------------------------------------------------
// 窗口位置记忆与锁定（REQ-UI-040 / REQ-VIDEO-015）
// ---------------------------------------------------------------------------
//
// 坐标空间：**像素、根窗口绝对坐标、窗口左上角**（XTranslateCoordinates 口径），
// 读（sp_abs_pos）与写（sp_move / XMoveWindow）同一空间，往返自洽。
//
// 与 Windows 侧的两处结构性差异（X11 的固有限制，不是简化）：
//   - 装饰窗口的移动/缩放由 WM 接管，客户端**拦不住**拖动：锁定只能"发现被
//     移动就挪回"（位置）+ 尺寸提示 min=max（尺寸，WM 一般会直接拒绝）；
//   - 取窗口句柄的 callPanelHandle 内部是 fyne.DoAndWait，**只能在后台
//     goroutine 调用**（主线程调会 panic），所以拖动/守卫都跑在自己的 goroutine 里，
//     各自持有一条 X 连接（本文件的前提：连接不跨 goroutine 共享）。

var (
	// videoLocked 视频窗口是否锁定（锁定时把被挪走的窗口拉回来）。
	videoLocked atomic.Bool
	// videoProgUntil 程序化摆放的"静默期"截止时间（unix 纳秒）：这段窗口期内
	// 的位置变化算程序化摆放（恢复保存位置/默认摆放的多次延迟校正），
	// 不算"用户拖动"，也不触发锁定回拉。
	videoProgUntil atomic.Int64
	// videoMoveHook 用户拖动结束回调（物理像素）。
	videoMoveHookMu sync.Mutex
	videoMoveHook   func(x, y int)
)

// markVideoProgMove 标记"接下来这一小段时间里的位置变化是程序化的"。
// 静默期要盖住默认摆放的三次延迟校正（150/350/800ms，最后一拍约 1.3s 生效、
// 守卫 250ms 一拍才看到），取 2s 留足余量。
func markVideoProgMove() {
	videoProgUntil.Store(time.Now().Add(2 * time.Second).UnixNano())
}

func videoProgRecent() bool { return time.Now().UnixNano() < videoProgUntil.Load() }

// SetVideoWindowMoveHook 注册"用户拖动结束"回调（由视频窗口守卫 goroutine 触发）。
func SetVideoWindowMoveHook(fn func(x, y int)) {
	videoMoveHookMu.Lock()
	videoMoveHook = fn
	videoMoveHookMu.Unlock()
}

func fireVideoMoveHook(x, y int) {
	videoMoveHookMu.Lock()
	fn := videoMoveHook
	videoMoveHookMu.Unlock()
	if fn != nil {
		fn(x, y) // 调用方（ui.go）自己用 fyne.Do 回主线程
	}
}

// screenBounds 屏幕边界（物理像素）：X11 下 DisplayWidth/Height 是整屏
// （多显示器为并集），原点 (0,0)。
func screenBounds() (x, y, w, h int) {
	d := C.sp_open_display()
	if d == nil {
		return 0, 0, 1280, 800
	}
	defer C.sp_close_display(d)
	var ww, hh C.int
	C.sp_screen(d, &ww, &hh)
	if ww <= 0 || hh <= 0 {
		return 0, 0, 1280, 800
	}
	return 0, 0, int(ww), int(hh)
}

// ---- 通话面板拖动会话（REQ-UI-040）----

type callPanelDragSession struct {
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	mu       sync.Mutex
	posX     int
	posY     int
	posOK    bool
}

var (
	callPanelDragMu  sync.Mutex
	callPanelDragCur *callPanelDragSession
)

// beginCallPanelDrag 开始拖动：起一条会话 goroutine 以 ~60Hz 跟随光标移动窗口。
// 主线程只做非阻塞的启动/停止（句柄解析要 DoAndWait，绝不能在这里同步做）。
func beginCallPanelDrag(w fyne.Window) {
	if w == nil {
		return
	}
	cancelCallPanelDrag(w) // 幂等：重复触发时重锚
	d := &callPanelDragSession{stop: make(chan struct{}), done: make(chan struct{})}
	callPanelDragMu.Lock()
	callPanelDragCur = d
	callPanelDragMu.Unlock()
	go d.run(w)
}

func (d *callPanelDragSession) run(w fyne.Window) {
	defer close(d.done)

	// 句柄：面板此刻必已显示，取不到就多试几次（首次 Show 的窗口异步创建）。
	// 等待期间要能被打断——取消会话时调用方在等 done，别让它干等一整秒。
	var handle uintptr
	for i := 0; i < 20; i++ {
		select {
		case <-d.stop:
			return
		default:
		}
		if handle = callPanelHandle(w); handle != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if handle == 0 {
		logx.Logger().Debug("未取得通话面板 X11 句柄，跳过拖动")
		return
	}
	disp := C.sp_open_display()
	if disp == nil {
		return
	}
	defer C.sp_close_display(disp)
	win := C.Window(handle)

	var winX, winY, curX, curY C.int
	if C.sp_abs_pos(disp, win, &winX, &winY) == 0 || C.sp_cursor(disp, &curX, &curY) == 0 {
		return
	}
	anchorWinX, anchorWinY := int(winX), int(winY)
	anchorCurX, anchorCurY := int(curX), int(curY)

	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			// 结束前读回最终位置（读不到就不落盘）
			var fx, fy C.int
			if C.sp_abs_pos(disp, win, &fx, &fy) != 0 {
				d.mu.Lock()
				d.posX, d.posY, d.posOK = int(fx), int(fy), true
				d.mu.Unlock()
			}
			return
		case <-ticker.C:
			var cx, cy C.int
			if C.sp_cursor(disp, &cx, &cy) == 0 {
				continue
			}
			x, y := dragTarget(anchorWinX, anchorWinY, int(cx), int(cy), anchorCurX, anchorCurY)
			C.sp_move(disp, win, C.int(x), C.int(y))
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

// endCallPanelDrag 结束拖动并读回最终位置（goroutine 里读的，带超时兜底）。
func endCallPanelDrag(w fyne.Window) (x, y int, ok bool) {
	d := stopCallPanelDrag()
	if d == nil {
		return 0, 0, false
	}
	select {
	case <-d.done:
	case <-time.After(500 * time.Millisecond): // 句柄重试最长 1s，超时就当没读到
		return 0, 0, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.posX, d.posY, d.posOK
}

// cancelCallPanelDrag 丢弃拖动会话（窗口隐藏/挂断时兜底：DragEnd 可能不来）。
func cancelCallPanelDrag(w fyne.Window) {
	d := stopCallPanelDrag()
	if d == nil {
		return
	}
	select {
	case <-d.done:
	case <-time.After(500 * time.Millisecond):
	}
}

// moveCallPanelToSaved 把通话面板移到保存的位置（异步重试 + 多次延迟校正）。
func moveCallPanelToSaved(w fyne.Window, x, y int) {
	if w == nil {
		return
	}
	go func() {
		var handle uintptr
		for i := 0; i < 20; i++ {
			if handle = callPanelHandle(w); handle != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if handle == 0 {
			logx.Logger().Warn("未取得通话面板 X11 句柄，跳过位置恢复")
			return
		}
		disp := C.sp_open_display()
		if disp == nil {
			return
		}
		defer C.sp_close_display(disp)
		win := C.Window(handle)
		C.sp_move(disp, win, C.int(x), C.int(y))
		for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond, 800 * time.Millisecond} {
			time.Sleep(delay)
			C.sp_move(disp, win, C.int(x), C.int(y))
		}
	}()
}

// ---- 视频窗口位置与锁定（REQ-VIDEO-015）----

// moveVideoWindowToSaved 把视频窗口移到保存的位置（异步重试 + 多次延迟校正）。
func moveVideoWindowToSaved(w fyne.Window, x, y int) {
	if w == nil {
		return
	}
	markVideoProgMove()
	go func() {
		var handle uintptr
		for i := 0; i < 20; i++ {
			if handle = callPanelHandle(w); handle != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if handle == 0 {
			return
		}
		disp := C.sp_open_display()
		if disp == nil {
			return
		}
		defer C.sp_close_display(disp)
		win := C.Window(handle)
		C.sp_move(disp, win, C.int(x), C.int(y))
		for _, delay := range []time.Duration{150 * time.Millisecond, 350 * time.Millisecond, 800 * time.Millisecond} {
			time.Sleep(delay)
			C.sp_move(disp, win, C.int(x), C.int(y))
		}
	}()
}

// setVideoWindowLock 切换视频窗口锁定：锁定时把 WM_NORMAL_HINTS 的 min/max
// 钉在窗口当前尺寸（WM 一般会直接拒绝用户的缩放），位置由守卫 goroutine 回拉。
// 句柄解析要 DoAndWait，所以只能扔给后台 goroutine。
func setVideoWindowLock(w fyne.Window, locked bool) {
	videoLocked.Store(locked)
	if w == nil {
		return
	}
	go func() {
		var handle uintptr
		for i := 0; i < 20; i++ {
			if handle = callPanelHandle(w); handle != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if handle == 0 {
			return
		}
		disp := C.sp_open_display()
		if disp == nil {
			return
		}
		defer C.sp_close_display(disp)
		win := C.Window(handle)
		if locked {
			var ww, hh C.int
			if C.sp_geometry(disp, win, &ww, &hh) != 0 && ww > 0 && hh > 0 {
				C.sp_lock_size(disp, win, ww, hh)
			}
			return
		}
		C.sp_unlock_size(disp, win)
	}()
}

// ---- 视频窗口位置守卫（显示期间轮询）----

type videoWatchSession struct {
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

var (
	videoWatchMu  sync.Mutex
	videoWatchCur *videoWatchSession
)

// startVideoWindowWatch 窗口显示期间的位置守卫（幂等）：
//   - 锁定：发现位置被改就挪回；尺寸由 WM_NORMAL_HINTS 钉住（这里兜底再钉一次）；
//   - 解锁：把位置变化当作"用户拖动结束"上报（连续两拍稳定后回调一次），
//     程序化摆放（恢复位置/默认摆放的延迟校正）靠静默期排除。
func startVideoWindowWatch(w fyne.Window) {
	if w == nil {
		return
	}
	videoWatchMu.Lock()
	if videoWatchCur != nil {
		videoWatchMu.Unlock()
		return
	}
	vw := &videoWatchSession{stop: make(chan struct{}), done: make(chan struct{})}
	videoWatchCur = vw
	videoWatchMu.Unlock()
	go vw.run(w)
}

// stopVideoWindowWatch 窗口隐藏时停掉守卫。
func stopVideoWindowWatch(w fyne.Window) {
	videoWatchMu.Lock()
	vw := videoWatchCur
	videoWatchCur = nil
	videoWatchMu.Unlock()
	if vw == nil {
		return
	}
	vw.stopOnce.Do(func() { close(vw.stop) })
	select {
	case <-vw.done:
	case <-time.After(300 * time.Millisecond):
	}
}

func (vw *videoWatchSession) run(w fyne.Window) {
	defer close(vw.done)

	var handle uintptr
	for i := 0; i < 20; i++ {
		if handle = callPanelHandle(w); handle != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if handle == 0 {
		logx.Logger().Debug("未取得视频窗口 X11 句柄，跳过位置守卫")
		return
	}
	disp := C.sp_open_display()
	if disp == nil {
		return
	}
	defer C.sp_close_display(disp)
	win := C.Window(handle)

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	var (
		have          bool
		lastX, lastY  int
		lastW, lastH  int
		lastLocked    bool
		stableTicks   int
		lastReportedX int
		lastReportedY int
	)
	for {
		select {
		case <-vw.stop:
			return
		case <-ticker.C:
			var x, y, ww, hh C.int
			if C.sp_geometry_abs(disp, win, &x, &y, &ww, &hh) == 0 {
				continue
			}
			cx, cy, cw, ch := int(x), int(y), int(ww), int(hh)
			locked := videoLocked.Load()
			if !have || locked != lastLocked {
				// 首次取到 / 锁定状态刚变化：只重锚，不做任何判定
				lastX, lastY, lastW, lastH, have = cx, cy, cw, ch, true
				lastLocked = locked
				stableTicks = 0
				continue
			}
			if videoProgRecent() {
				// 程序化摆放的延迟校正：只更新锚点
				lastX, lastY, lastW, lastH = cx, cy, cw, ch
				stableTicks = 0
				continue
			}
			if locked {
				if cx != lastX || cy != lastY {
					C.sp_move(disp, win, C.int(lastX), C.int(lastY))
					continue
				}
				if cw != lastW || ch != lastH {
					// WM 本该按尺寸提示拒绝；这里兜底拉回并重新钉住
					C.sp_lock_size(disp, win, C.int(lastW), C.int(lastH))
				}
				continue
			}
			// 解锁：位置变了要连续两拍稳定才算拖动结束（拖动中间不算）
			if cx != lastX || cy != lastY {
				if cx == lastReportedX && cy == lastReportedY {
					stableTicks++
				} else {
					stableTicks = 0
				}
				lastReportedX, lastReportedY = cx, cy
				if stableTicks >= 1 {
					lastX, lastY = cx, cy
					stableTicks = 0
					fireVideoMoveHook(cx, cy)
				}
			} else {
				stableTicks = 0
			}
			lastW, lastH = cw, ch
		}
	}
}

// suspendVideoWindowLockClamp / resumeVideoWindowLockClamp：X11 侧没有消息钳制
// （锁定靠轮询回拉），全屏切换期间标记静默期即可。
func suspendVideoWindowLockClamp() { markVideoProgMove() }
func resumeVideoWindowLockClamp()  {}
