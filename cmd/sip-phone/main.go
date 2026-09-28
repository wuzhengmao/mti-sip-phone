// mti-sip-phone 跨平台 SIP 软电话入口。
//
// 启动流程：
//  1. 解析命令行（sip:// 协议拉起参数）
//  2. 加载配置、初始化日志
//  3. 单实例：第二实例把参数转发给第一实例后退出
//  4. 注册 sip:// 系统协议（幂等）
//  5. 组装核心：事件总线 + SIP 电话层 + 音频 + GUI + WS 服务
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mti-sip-phone/internal/audio"
	"mti-sip-phone/internal/autostart"
	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/protocol"
	"mti-sip-phone/internal/single"
	"mti-sip-phone/internal/sip"
	"mti-sip-phone/internal/ui"
	"mti-sip-phone/internal/wsapi"
)

// headless 无界面模式：跳过 GUI（仅 WS 服务），用于 Linux 服务器等无显示环境。
var headless = flag.Bool("headless", false, "无界面模式（不启动 GUI）")

func main() {
	flag.Parse()

	// 1. 解析 sip:// 协议拉起参数（Windows 下形如 "proto:sip://..."）
	sipURL := findSipURL(os.Args[1:])

	// 2. 配置与日志
	cfgDir, err := config.ConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取配置目录失败: %v\n", err)
		os.Exit(1)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	logDir := cfg.Log.Dir
	if logDir == "" {
		logDir = filepath.Join(cfgDir, "logs")
	}
	// 日志输出：写文件时不输出控制台（REQ-LOG-006），文件关闭时输出控制台
	if err := logx.Init(cfg.Log.Level, logDir, cfg.Log.File, cfg.Log.Console); err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		os.Exit(1)
	}
	defer logx.Close()

	// 3. 单实例：锁文件在配置目录
	lockPath := filepath.Join(cfgDir, "instance.lock")
	releaseLock, err := single.Run(lockPath, os.Args[1:])
	if err != nil {
		if err == single.ErrAlreadyRunning {
			fmt.Println("mti-sip-phone 已在运行，参数已转发给现有实例。")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "单实例检查失败: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logx.Logger().Info("mti-sip-phone 启动", "version", core.Version, "config", cfgPath)

	// 4. 注册 sip:// 系统协议（幂等；失败不阻断启动）
	if exe, err := os.Executable(); err == nil {
		if err := protocol.RegisterProtocol(exe); err != nil {
			logx.Logger().Warn("注册 sip:// 协议失败（可稍后重试）", "err", err)
		}
		// 开机自动启动（REQ-UI-022）：按配置同步注册/移除，默认启用
		if cfg.AutoStart {
			if err := autostart.Enable(exe); err != nil {
				logx.Logger().Warn("注册开机自启失败", "err", err)
			} else {
				logx.Logger().Debug("开机自启已注册", "exe", exe)
			}
		} else {
			if err := autostart.Disable(); err != nil {
				logx.Logger().Warn("移除开机自启失败", "err", err)
			}
		}
	} else {
		logx.Logger().Warn("无法获取可执行文件路径，跳过协议注册", "err", err)
	}

	// 5. 组装核心
	bus := core.NewBus()
	app := core.New(cfg, nil, bus)
	app.SetConfigPath(cfgPath)

	// 音频管理器：设备枚举与选择（REQ-MEDIA-002）
	audioMgr, err := audio.NewManager()
	if err != nil {
		logx.Logger().Warn("初始化音频管理器失败，设备枚举不可用", "err", err)
	}
	// SIP 软电话：本地监听端口 40000~50000 随机选择空闲端口（REQ-ACC-013 修订），
	// rport 按配置开关（REQ-ACC-016，默认开启）
	sip.SetTraceEnabled(cfg.Log.SIPTrace) // SIP 信令记录按配置开关（默认关）
	phone, err := sip.NewPhone(app, audioMgr, cfg.Account.RPort)
	if err != nil {
		logx.Logger().Error("初始化 SIP 软电话失败", "err", err)
		os.Exit(1)
	}
	// 监听本地 SIP 端口（呼入通道）：UDP 与 TCP 各起监听
	localPort := sip.FindFreeSIPPort()
	listenAddr := fmt.Sprintf("0.0.0.0:%d", localPort)
	go func() {
		if err := phone.ListenAndServe(ctx, "udp", listenAddr); err != nil && ctx.Err() == nil {
			logx.Logger().Error("UDP SIP 监听失败", "err", err)
		}
	}()
	go func() {
		if err := phone.ListenAndServe(ctx, "tcp", listenAddr); err != nil && ctx.Err() == nil {
			logx.Logger().Error("TCP SIP 监听失败", "err", err)
		}
	}()
	tel := core.Telephony(phone)
	defer phone.Close()
	// 按配置预选音频设备
	if audioMgr != nil {
		if cfg.Audio.MicID != "" {
			_ = audioMgr.SetMic(cfg.Audio.MicID)
		}
		if cfg.Audio.SpeakerID != "" {
			_ = audioMgr.SetSpeaker(cfg.Audio.SpeakerID)
		}
		if cfg.Audio.RingSpeakerID != "" {
			audioMgr.SetRingSpeaker(cfg.Audio.RingSpeakerID)
		}
	}
	app.SetTel(tel)
	app.Start(ctx)
	defer func() { _ = tel.Close() }()

	// 6. 单实例参数转发服务（接收第二实例的 sip:// 拉起）
	go func() {
		port, err := single.ServeForward(lockPath, func(args []string) {
			handleLaunchArgs(app, cfg, args, nil)
		})
		if err != nil {
			logx.Logger().Warn("启动参数转发服务失败", "err", err)
			return
		}
		logx.Logger().Debug("参数转发服务已启动", "port", port)
	}()

	// 7. 本地 WS 控制服务。
	// WS 端口可配置（默认 8765），被占用时自动 +1 选择空闲端口并保存。
	if wsPort := wsapi.FindFreeWSPort(cfg.WS.Port); wsPort != cfg.WS.Port {
		logx.Logger().Info("WS 端口被占用，自动选择空闲端口", "port", wsPort)
		cfg.WS.Port = wsPort
		_ = cfg.Save(cfgPath)
	}
	srv := wsapi.New(app)
	go func() {
		if err := srv.ListenAndServe(ctx, cfg.WS.Port); err != nil {
			logx.Logger().Error("WS 服务异常退出", "err", err)
			stop()
		}
	}()

	// 8. 处理启动时的 sip:// 参数
	if sipURL != "" {
		handleLaunchArgs(app, cfg, []string{sipURL}, nil)
	}

	// 启动信息：GUI 子系统构建下无控制台（REQ-UI-013），统一写入日志；
	// 仅在 headless 模式（控制台子系统）下打印到标准输出。
	logx.Logger().Info("mti-sip-phone 已启动",
		"测试页面", fmt.Sprintf("http://127.0.0.1:%d/", cfg.WS.Port),
		"WS地址", fmt.Sprintf("ws://127.0.0.1:%d/ws", cfg.WS.Port),
		"配置目录", cfgDir)
	if *headless {
		fmt.Println("mti-sip-phone 已启动（headless）")
		fmt.Printf("测试页面: http://127.0.0.1:%d/\n", cfg.WS.Port)
	}

	// 9. GUI（无界面模式跳过）。
	// Fyne 要求 UI 初始化与事件循环运行在主线程（goroutine 中运行会导致
	// 托盘失效与线程安全检查告警），因此 GUI 模式直接在主线程调用 Run。
	if !*headless {
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
		stop()
		shutdown(app, tel) // 退出前注销 SIP（REQ-ACC-009）
		return
	}

	<-ctx.Done()
	logx.Logger().Info("收到退出信号，正在关闭")
	shutdown(app, tel)
}

// shutdown 退出清理：注销 SIP 账户（发送 expires=0 的 REGISTER，REQ-ACC-009）。
// 给注销留出最多 3 秒发送时间，超时不影响退出。
func shutdown(app *core.App, tel core.Telephony) {
	if tel == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := tel.Unregister(ctx); err != nil {
		logx.Logger().Warn("退出注销失败", "err", err)
	}
}

// handleLaunchArgs 处理 sip:// 协议拉起：解析注册信息 → 更新配置 → 触发注册。
// onDone 在 GUI 就绪后显示主界面（由调用方注入）。
func handleLaunchArgs(app *core.App, cfg *config.Config, args []string, onDone func()) {
	for _, a := range args {
		i := strings.Index(a, "sip://")
		if i < 0 {
			continue
		}
		raw := a[i:]
		acc, err := protocol.ParseSipURL(raw)
		if err != nil {
			logx.Logger().Warn("sip:// 链接解析失败", "url", raw, "err", err)
			continue
		}
		logx.Logger().Info("sip:// 协议拉起", "server", acc.Server, "account", acc.Account)

		// 更新账户配置并触发注册
		newAcc := cfg.Account
		newAcc.Enabled = true
		newAcc.Server = acc.Server
		newAcc.Port = acc.Port
		newAcc.Transport = acc.Transport
		newAcc.Account = acc.Account
		newAcc.AuthUser = acc.Account
		newAcc.Password = acc.Password
		if err := app.UpdateAccount(newAcc); err != nil {
			logx.Logger().Error("应用 sip:// 配置失败", "err", err)
			continue
		}
		if onDone != nil {
			onDone()
		}
	}
}

// findSipURL 从命令行参数中提取 sip:// URL。
func findSipURL(args []string) string {
	for _, a := range args {
		if i := strings.Index(a, "sip://"); i >= 0 {
			return a[i:]
		}
	}
	return ""
}
