//go:build noui

package main

import (
	"context"
	"fmt"

	"mti-sip-phone/internal/audio"
	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/tray"
)

// runGUI 仅托盘无 UI 版（-tags noui，产物 mti-sip-phone-no-ui）：
// **只起系统托盘，不创建任何窗口**——主界面、通话面板、视频窗口、更新提示窗
// 都不存在（本构建根本不链接 internal/ui 与 fyne），所有操作都在网页控制台里做
// （托盘左键点击或菜单「打开控制台」→ 系统默认浏览器）。
//
// 参数表必须与 gui.go 的 !headless 版本逐一对齐（含最后的实例槽位 slot）——
// 历史踩坑：多实例那次改动漏了 headless 桩的参数，纯 Go 构建直接编不过。
// 这里前三个参数（audioMgr / cfgPath / slot）本变体用不到，用 _ 忽略：
// 音频设备经 main 注入 core（见 SetAudioControl），配置落盘也在 core 里。
func runGUI(app *core.App, _ *audio.Manager, cfg *config.Config, _ string, ctx context.Context, _ func() error, _ int) {
	url := fmt.Sprintf("http://127.0.0.1:%d/", cfg.WS.Port)

	// 托盘图标反映注册状态（REQ-UI-011）：先按核心状态快照置初值，
	// 之后每次 register.state 事件刷新（口径与桌面版 setRegState 一致：
	// 只有 registered 变绿，registering 保持原色）。
	applyReg := func(state string) {
		switch state {
		case string(core.RegRegistered):
			tray.SetState(true, accountDetail(cfg))
		case string(core.RegFailed), string(core.RegUnregistered):
			tray.SetState(false, accountDetail(cfg))
		}
	}
	snapshot := app.State()
	applyReg(string(snapshot.Register.State))

	// 只订阅注册状态：call.* / video.* / system.update 一律不处理
	// （没有窗口可弹，网页侧自己收这些事件）
	sub := app.Bus().Subscribe()
	defer app.Bus().Unsubscribe(sub)
	go func() {
		for ev := range sub {
			if ev.Name != "register.state" {
				continue
			}
			if data, ok := ev.Data.(map[string]any); ok {
				if state, ok := data["state"].(string); ok {
					applyReg(state)
				}
			}
		}
	}()

	// 退出信号（SIGTERM / 自动更新重启）→ 结束托盘消息循环，回到 main 的
	// 退出收尾（SIP 注销、关库、释放单实例锁、必要时拉起新进程）
	go func() {
		<-ctx.Done()
		logx.Logger().Info("收到退出信号，正在关闭托盘")
		tray.Quit()
	}()

	logx.Logger().Info("仅托盘无 UI 版已启动（无界面，全部操作走网页控制台）",
		"控制台", url, "版本", core.Version)
	tray.Run(tray.Options{
		// 托盘提示与完整版**完全一致**（形如 "mti-sip-phone - 2999"）：
		// 不带版本号、也不区分变体名——两个版本是同一个应用（用户要求）
		Title: "mti-sip-phone",
		OnOpen: func() {
			logx.Logger().Info("打开网页控制台", "url", url)
			if err := tray.OpenBrowser(url); err != nil {
				// 托盘点不开浏览器时用户会以为"点了没反应"，日志是唯一线索
				logx.Logger().Warn("打开浏览器失败", "url", url, "err", err)
			}
		},
		OnQuit: func() {
			// 菜单「退出」：结束消息循环，main 照常走退出收尾
			tray.Quit()
		},
	})
}

// accountDetail 托盘提示里的附加信息（" - 2999"），未配置账号时为空。
func accountDetail(cfg *config.Config) string {
	if cfg == nil || cfg.Account.Account == "" {
		return ""
	}
	return " - " + cfg.Account.Account
}
