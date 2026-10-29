#import <Cocoa/Cocoa.h>
#include <math.h>
#include <stdint.h>

// macOS 通话面板/更新提示窗的窗口控制（AppKit）。全部函数都必须**在主线程**
// 调用——Go 侧统一用 fyne.DoAndWait 排队（见 topmost_darwin.go）。
//
// 坐标口径：Go 侧统一用「主屏左上角为原点、物理像素」的坐标（与 Windows/Linux
// 保存的位置一致）；Cocoa 的屏幕坐标是「主屏左下角为原点、point」。换算集中
// 在这里，Go 侧不需要关心 Retina 缩放。

// 主屏（带菜单栏的那块）与应用窗口的缩放系数。
static NSScreen *mti_primary_screen(void) {
    NSArray<NSScreen *> *screens = [NSScreen screens];
    return screens.count > 0 ? screens[0] : [NSScreen mainScreen];
}

static double mti_scale_for(NSWindow *win) {
    NSScreen *s = win.screen ?: mti_primary_screen();
    return s.backingScaleFactor > 0 ? s.backingScaleFactor : 1.0;
}

// mti_style_panel 设成悬浮工具窗：置顶、所有桌面可见、不进 Mission Control
// 与 Cmd+` 窗口切换；floating=0 时恢复普通层级。
void mti_style_panel(uintptr_t w, int floating) {
    NSWindow *win = (NSWindow *)w;  // uintptr_t → NSWindow*（Go 侧传 uintptr，避免 unsafe.Pointer 转换）
    if (win == nil) {
        return;
    }
    [win setLevel:floating ? NSFloatingWindowLevel : NSNormalWindowLevel];
    [win setCollectionBehavior:(NSWindowCollectionBehaviorCanJoinAllSpaces |
                                NSWindowCollectionBehaviorTransient |
                                NSWindowCollectionBehaviorIgnoresCycle)];
    [win setHidesOnDeactivate:NO];
    [win setReleasedWhenClosed:NO];
    [win setHasShadow:YES];
}

void mti_set_level(uintptr_t w, int floating) {
    NSWindow *win = (NSWindow *)w;  // uintptr_t → NSWindow*（Go 侧传 uintptr，避免 unsafe.Pointer 转换）
    if (win == nil) {
        return;
    }
    [win setLevel:floating ? NSFloatingWindowLevel : NSNormalWindowLevel];
}

// mti_corner 把窗口放到工作区右下角（visibleFrame 已扣掉菜单栏与 Dock），
// dx 是多实例槽位带来的整列左移量（point）。
void mti_corner(uintptr_t w, int dx) {
    NSWindow *win = (NSWindow *)w;  // uintptr_t → NSWindow*（Go 侧传 uintptr，避免 unsafe.Pointer 转换）
    if (win == nil) {
        return;
    }
    NSScreen *s = win.screen ?: mti_primary_screen();
    NSRect vf = s.visibleFrame;
    NSRect f = win.frame;
    double margin = 16.0;
    double x = NSMaxX(vf) - f.size.width - margin - (double)dx;
    double y = NSMinY(vf) + margin;
    [win setFrameOrigin:NSMakePoint(x, y)];
}

// mti_frame 读窗口矩形，换算成「主屏左上原点、物理像素」。
void mti_frame(uintptr_t w, int *x, int *y, int *wpx, int *hpx) {
    NSWindow *win = (NSWindow *)w;  // uintptr_t → NSWindow*（Go 侧传 uintptr，避免 unsafe.Pointer 转换）
    if (win == nil) {
        return;
    }
    NSScreen *primary = mti_primary_screen();
    double sc = mti_scale_for(win);
    double ph = primary.frame.size.height; // point
    NSRect f = win.frame;
    *x = (int)lround(f.origin.x * sc);
    *wpx = (int)lround(f.size.width * sc);
    *hpx = (int)lround(f.size.height * sc);
    *y = (int)lround((ph - f.origin.y - f.size.height) * sc);
}

// mti_move 按「主屏左上原点、物理像素」移动窗口。
void mti_move(uintptr_t w, int x, int y) {
    NSWindow *win = (NSWindow *)w;  // uintptr_t → NSWindow*（Go 侧传 uintptr，避免 unsafe.Pointer 转换）
    if (win == nil) {
        return;
    }
    NSScreen *primary = mti_primary_screen();
    double sc = mti_scale_for(win);
    double ph = primary.frame.size.height; // point
    NSRect f = win.frame;
    double ox = (double)x / sc;
    double oy = ph - (double)y / sc - f.size.height;
    [win setFrameOrigin:NSMakePoint(ox, oy)];
}

// mti_cursor 全局鼠标位置（同上坐标系）。
int mti_cursor(int *x, int *y) {
    NSScreen *primary = mti_primary_screen();
    if (primary == nil) {
        return 0;
    }
    NSPoint p = [NSEvent mouseLocation];
    double sc = primary.backingScaleFactor > 0 ? primary.backingScaleFactor : 1.0;
    *x = (int)lround(p.x * sc);
    *y = (int)lround((primary.frame.size.height - p.y) * sc);
    return 1;
}

// mti_screen 主屏整屏（物理像素，左上原点）。
void mti_screen(int *x, int *y, int *w, int *h) {
    NSScreen *s = mti_primary_screen();
    if (s == nil) {
        return;
    }
    double sc = s.backingScaleFactor > 0 ? s.backingScaleFactor : 1.0;
    NSRect f = s.frame;
    *x = 0;
    *y = 0;
    *w = (int)lround(f.size.width * sc);
    *h = (int)lround(f.size.height * sc);
}

// mti_workarea 工作区（物理像素，左上原点；已扣掉菜单栏与 Dock）。
void mti_workarea(int *x, int *y, int *w, int *h) {
    NSScreen *s = mti_primary_screen();
    if (s == nil) {
        return;
    }
    double sc = s.backingScaleFactor > 0 ? s.backingScaleFactor : 1.0;
    NSRect vf = s.visibleFrame;
    NSRect pf = s.frame;
    *x = (int)lround(vf.origin.x * sc);
    *w = (int)lround(vf.size.width * sc);
    *h = (int)lround(vf.size.height * sc);
    *y = (int)lround((pf.size.height - vf.origin.y - vf.size.height) * sc);
}
