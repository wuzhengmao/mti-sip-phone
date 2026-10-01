//go:build linux

package ui

import (
	"os"

	"github.com/go-gl/glfw/v3.4/glfw"

	"mti-sip-phone/internal/logx"
)

// prepareGLFW 在 Fyne 初始化 GLFW 之前做平台预设（调用点见 ui.go 的 Run，
// 必须早于第一个窗口创建——平台提示只在 glfw.Init 之前有效）。
//
//  1. Wayland 会话 + X 显示可用时固定走 X11（Xwayland）。原因：
//     Wayland 协议不允许客户端自定位窗口、去装饰、自行置顶，通话面板只能
//     居中带标题栏；更要命的是新映射的窗口拿不到指针焦点（合成器防抢
//     焦点，RequestFocus 会被拒绝），这段空窗期里 fyne 的指针位置停留在
//     旧值上，用户点任何位置都按旧坐标命中控件——实测表现为刚弹出时点
//     「接听」却挂断。走 X11 后这些都是 X11 语义，工作正常。
//  2. 关闭 libdecor：GLFW 在 Wayland 下默认优先用它画装饰，而 libdecor
//     释放 frame 时先销毁 xdg_toplevel、后销毁 decoration，违反
//     xdg-decoration 协议，wlroots 0.17+ 的合成器（labwc/sway）会据此判定
//     协议错误并**断开整个客户端连接**（stderr 可见 “xdg_toplevel destroyed
//     before xdg_toplevel_decoration”）。连接一断，后续新建窗口的 EGL 建面
//     必然失败（且因连接已死，EGL 报错文案是假失败的 “Success”），而 go-gl
//     绑定会吞掉 PlatformError，Fyne 因此留下没有 viewport 的僵尸窗口：
//     面板显示不出来、之后主窗体也拉不起来，同时主循环卡死在
//     glfwPollEvents，托盘退出与 SIGTERM 全部失效（只能 kill -9）。
//     关闭后 GLFW 改走 zxdg-decoration（服务端装饰，GLFW 销毁顺序正确）。
func prepareGLFW() {
	preferX11OnWayland()
	glfw.InitHint(glfw.WaylandLibdecor, glfw.WaylandDisableLibdecor)
}

// preferX11OnWayland 在「Wayland 会话 + X 显示确实可用」时把 Fyne 的平台
// 选择固定为 X11（FYNE_PLATFORM 是 Fyne 自带的开关，会转成 GLFW 的平台
// 提示）。以下情况不干预：
//   - 用户已显式设置 FYNE_PLATFORM（FYNE_PLATFORM=wayland 可强制回原生
//     Wayland）；
//   - 非 Wayland 会话（纯 Xorg 本来就选 X11）；
//   - 没有 DISPLAY，或 DISPLAY 存在但连不上（Wayland 会话里 DISPLAY 常被
//     无条件导出，Xwayland 未必在跑——强行选 X11 会让应用完全起不来）。
func preferX11OnWayland() {
	if os.Getenv("FYNE_PLATFORM") != "" {
		return // 用户显式指定，尊重
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" || os.Getenv("DISPLAY") == "" {
		return
	}
	if !x11DisplayAvailable() {
		return
	}
	if err := os.Setenv("FYNE_PLATFORM", "x11"); err == nil {
		logx.Logger().Info("Wayland 会话下检测到可用 X 显示，改用 X11 后端",
			"hint", "设 FYNE_PLATFORM=wayland 可强制使用原生 Wayland")
	}
}
