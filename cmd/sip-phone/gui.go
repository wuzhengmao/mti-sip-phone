//go:build !headless

package main

import (
	"context"

	"mti-sip-phone/internal/audio"
	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/ui"
)

// runGUI 启动并运行 GUI 主循环（阻塞直到退出）。
// Fyne 要求 UI 初始化与事件循环运行在主线程（goroutine 中运行会导致
// 托盘失效与线程安全检查告警），因此 GUI 模式直接在主线程调用 Run。
//
// 该文件按构建标签 !headless 编译——headless 构建不含界面层，供无
// CGO 交叉工具链的环境做纯 Go 构建（fyne 的 OpenGL 绑定依赖 CGO，
// 交叉编译 GUI 产物需要目标平台的 gcc 工具链）。
func runGUI(app *core.App, audioMgr *audio.Manager, cfg *config.Config, cfgPath string, ctx context.Context, restart func() error) {
	// 重启回调（REQ-UI-017 本地端口修改 / REQ-UPD-005 自动更新）：
	// 标记重启并取消 ctx → 本函数返回 → main 的退出收尾释放单实例锁、
	// 拉起新进程（见 main.go 的 spawnNewProcessIfRequested）。
	// 修复历史缺陷：旧实现"释放锁 + 拉起新进程"后不退出当前进程，
	// 会出现新旧两个实例并存、WS 端口漂移。
	onRestart := func() {
		logx.Logger().Info("用户确认重启，正在重启程序")
		if err := restart(); err != nil {
			logx.Logger().Error("重启失败", "err", err)
		}
	}
	gui := ui.New(app, audioMgr, cfg, cfgPath, onRestart)
	// 退出信号 → 请求 Fyne 退出（Run 返回后主流程收尾）
	go func() {
		<-ctx.Done()
		logx.Logger().Info("收到退出信号，正在关闭")
		gui.Quit()
	}()
	gui.Run()
}
