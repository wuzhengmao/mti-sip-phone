//go:build !headless

package main

import (
	"context"
	"os"
	"os/exec"

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
func runGUI(app *core.App, audioMgr *audio.Manager, cfg *config.Config, cfgPath string, ctx context.Context, releaseLock func()) {
	// 重启回调（REQ-UI-017）：释放单实例锁 → 拉起新进程 → 退出当前。
	// 必须先释放锁再启动，否则新进程因锁冲突而退出。
	onRestart := func() {
		logx.Logger().Info("用户确认重启，正在重启程序")
		if releaseLock != nil {
			releaseLock()
		}
		exe, err := os.Executable()
		if err == nil {
			cmd := exec.Command(exe, os.Args[1:]...)
			if err := cmd.Start(); err != nil {
				logx.Logger().Error("拉起新进程失败", "err", err)
			}
		} else {
			logx.Logger().Error("获取可执行文件路径失败", "err", err)
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
