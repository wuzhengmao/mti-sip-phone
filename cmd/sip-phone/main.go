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
	"sync/atomic"
	"syscall"
	"time"

	"mti-sip-phone/internal/audio"
	"mti-sip-phone/internal/autostart"
	"mti-sip-phone/internal/calllog"
	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/contacts"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/protocol"
	"mti-sip-phone/internal/single"
	"mti-sip-phone/internal/sip"
	"mti-sip-phone/internal/updater"
	"mti-sip-phone/internal/wsapi"
)

// headless 无界面模式：跳过 GUI（仅 WS 服务），用于 Linux 服务器等无显示环境。
var headless = flag.Bool("headless", false, "无界面模式（不启动 GUI）")

// instanceSlot 实例槽位（多实例沙箱）：0/缺省 = 主实例（用原目录）；
// ≥1 = 沙箱实例，用独立数据目录（见 config.InstanceDir）。sip:// 链接也可用
// 查询参数 instance=N 指定（见 internal/protocol）。
var instanceSlot = flag.Int("instance", 0, "实例槽位（多实例沙箱，0=主实例）")

func main() {
	flag.Parse()

	// 1. 解析 sip:// / sips:// 协议拉起参数（Windows 下形如 "proto:sips://..."）
	sipURL := protocol.FindURL(os.Args[1:])

	// 1.1 实例槽位：`-instance N` 优先，其次 sip:// 的 instance=N（缺省 0 = 主实例）。
	slot := *instanceSlot
	if slot <= 0 && sipURL != "" {
		if acc, err := protocol.ParseSipURL(sipURL); err == nil {
			slot = acc.Instance
		}
	}
	if slot < 0 || slot > 99 {
		fmt.Fprintf(os.Stderr, "实例槽位非法: %d（应为 0–99）\n", slot)
		os.Exit(1)
	}

	// 2. 配置与日志（沙箱实例用独立目录，配置/记录/通信录/日志互不影响）
	cfgDir, err := config.InstanceDir(slot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取配置目录失败: %v\n", err)
		os.Exit(1)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")

	// ⚠️ 要在 Load **之前**记下"配置文件是否存在"：config.Load 在文件缺失时会
	// 立刻落盘一份默认配置（WS 端口 8765），之后再判就来不及改沙箱的端口起点了。
	_, statErr := os.Stat(cfgPath)
	cfgExisted := !os.IsNotExist(statErr)

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

	// 崩溃现场记录：托盘版是 GUI 子系统构建（没有控制台），未捕获的 panic、
	// 运行期致命错误、cgo 里的非法访问都只会写 stderr——那里没人接，Windows
	// 事件日志也不会有记录（进程是「自己退出」的），表现为「软件凭空消失、
	// 日志停在半截」。这里把运行期崩溃输出另存一份，并把没有控制台时的
	// stderr 一并落到日志目录（有控制台时不动，控制台输出照旧）。
	crashPath, archivedCrash, crashErr := logx.InstallCrashHandler(logDir, fmt.Sprintf(
		"mti-sip-phone %s (%s) pid=%d 实例=%d 参数=%q",
		core.Version, core.Platform(), os.Getpid(), slot, os.Args[1:]))
	if crashErr != nil {
		logx.Logger().Warn("崩溃记录未启用（崩溃时可能没有现场可查）", "err", crashErr)
	} else {
		logx.Logger().Info("崩溃记录已启用", "file", crashPath)
		if archivedCrash != "" {
			logx.Logger().Warn("上次运行的崩溃现场已归档（上次是异常退出）", "file", archivedCrash)
		}
	}

	// 沙箱实例：首次启动（还没有配置文件）时按「8765 + 10×槽位」给 WS 端口一个
	// 起点（槽位 1 → 8775、槽位 2 → 8785，被占用仍会自动 +1 并存入本实例自己的
	// 配置），免得与主实例及其它槽位抢。已有配置（用户改过端口）时不覆盖。
	if slot >= 1 && !cfgExisted {
		cfg.WS.Port = config.SandboxWSPort(slot)
		// 沙箱实例本来就不做系统级自启注册（只由主实例负责），配置项也一并
		// 初始化为关，免得它的设置页显示"已开启"却其实没注册。
		cfg.AutoStart = false
		if err := cfg.Save(cfgPath); err != nil {
			logx.Logger().Warn("沙箱实例：改写 WS 端口起点失败", "err", err)
		} else {
			logx.Logger().Info("沙箱实例：首次启动，WS 端口起点已设为 8765+10×槽位",
				"instance", slot, "config", cfgPath, "wsPort", cfg.WS.Port)
		}
	}

	// 3. 单实例：锁文件在配置目录（沙箱实例有自己的锁，与其他槽位互不干扰）
	lockPath := filepath.Join(cfgDir, "instance.lock")
	releaseLock, err := single.Run(lockPath, os.Args[1:])
	if err != nil {
		if err == single.ErrAlreadyRunning {
			// 同时写日志：自更新重启时，新进程若因锁冲突走到这里会静默退出，
			// 日志是唯一线索（此前只打 stdout，无控制台时看不到）
			logx.Logger().Warn("检测到已有实例在运行，本进程转发参数后退出", "lock", lockPath, "args", os.Args[1:])
			fmt.Println("mti-sip-phone 已在运行，参数已转发给现有实例。")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "单实例检查失败: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 自动更新的重启请求：只做标记并取消 ctx，真正的"释放单实例锁 →
	// 拉起新进程"统一放在退出收尾（spawnNewProcessIfRequested）——必须等
	// WS/SIP 监听关闭、SIP 注销完成后再启动新进程，否则新进程会把被占用的
	// WS 端口 +1 写回配置，并与旧进程重复注册。
	// 可执行文件路径：协议/自启注册、自动更新与重启共用（先取到，
	// 重启回调与退出收尾都要用）
	exePath, exeErr := os.Executable()

	var restartRequested atomic.Bool
	restart := func() error {
		restartRequested.Store(true)
		// Unix 侧先起看门狗兜底：正常退出时它只是多起一个实例（会被
		// 单实例锁拦住并自行退出），若退出收尾卡住则由它强杀旧进程并拉起
		// 新进程（实测 Debian X11 上更新后曾卡在 UI 退出阶段）
		startRestartWatchdog(exePath, os.Args[1:])
		stop()
		return nil
	}

	logx.Logger().Info("mti-sip-phone 启动",
		"version", core.Version, "config", cfgPath, "instance", slot)

	// 4. 注册 sip:// 系统协议与开机自启：**只有主实例做**（沙箱实例的系统级
	// 注册会与主实例互相覆盖——尤其自启项：沙箱的空白配置默认开启自启，
	// 会把主实例注册的那条改写成不带槽位的启动项）。
	if slot != 0 {
		// 沙箱实例跳过系统级注册
	} else if exeErr != nil {
		logx.Logger().Warn("无法获取可执行文件路径，跳过协议注册", "err", exeErr)
	} else {
		if err := protocol.RegisterProtocol(exePath); err != nil {
			logx.Logger().Warn("注册 sip:// 协议失败（可稍后重试）", "err", err)
		}
		// 开机自动启动（REQ-UI-022）：按配置同步注册/移除，默认启用
		if cfg.AutoStart {
			if err := autostart.Enable(exePath); err != nil {
				logx.Logger().Warn("注册开机自启失败", "err", err)
			} else {
				logx.Logger().Debug("开机自启已注册", "exe", exePath)
			}
		} else {
			if err := autostart.Disable(); err != nil {
				logx.Logger().Warn("移除开机自启失败", "err", err)
			}
		}
	}

	// 5. 组装核心
	bus := core.NewBus()
	app := core.New(cfg, nil, bus)
	app.SetConfigPath(cfgPath)
	app.SetInstanceSlot(slot) // 诊断页拼 sip:// 拉起链接要用（沙箱实例要带 ?instance=N）
	// 网页控制台的配置能力（REQ-WS-016 起）：重启程序、开机自启项的注册/移除、
	// SIP 信令记录开关——core 拿不到这些（都在 main 里），按注入方式接进去。
	// 沙箱实例（slot != 0）本就不做系统级注册，这里传 nil 表示"该能力未接入"，
	// 网页端调用会得到明确的错误而不是静默生效。
	app.SetRestartFunc(restart)
	if slot == 0 {
		app.SetAutoStartController(func(on bool) error {
			if on {
				return autostart.Enable(exePath)
			}
			return autostart.Disable()
		})
	}
	app.SetSIPTraceController(sip.SetTraceEnabled)

	// 通话记录（REQ-CALL-007）：本地 SQLite（纯 Go 驱动，不影响交叉编译）。
	// 打不开只降级为"本次运行不记录"，不阻断启动。Close 幂等，退出收尾与
	// defer 都会调（见下方 spawnNewProcessIfRequested 之前那次）。
	var closeCallLog = func() {}
	if store, err := calllog.Open(filepath.Join(cfgDir, "call-log.db")); err != nil {
		logx.Logger().Warn("打开通话记录数据库失败，本次运行不记录通话记录", "err", err)
	} else {
		app.SetCallLog(store)
		closeCallLog = func() { _ = store.Close() }
	}
	defer closeCallLog()

	// 通信录（REQ-UI-032）：独立数据库文件（与通话记录分开，损坏互不影响）。
	// 只要打不开就降级为"本次运行没有通信录"，不阻断启动；Close 幂等，
	// 退出收尾与 defer 都会调（放在通话记录之后，退出时一并放掉）。
	var closeContacts = func() {}
	if store, err := contacts.Open(filepath.Join(cfgDir, "contacts.db")); err != nil {
		logx.Logger().Warn("打开通信录数据库失败，本次运行没有通信录", "err", err)
	} else {
		app.SetContacts(store) // 内部立刻载入内存快照（第一通来电就能显示名字）
		closeContacts = func() { _ = store.Close() }
	}
	defer closeContacts()

	// 自动更新（REQ-UPD-*）：**软电话自己**按配置的周期查询更新站点
	//（REQ-UPD-017，配了站点才查），查到新版本提示用户，用户确认后才下载、
	// 替换当前可执行文件并重启；业务前端仍可经 WS 强制推送。Cleanup 清理上次
	// 更新的残留文件，必须放在单实例锁之后（避免删到另一实例正在使用的文件）。
	upd := updater.New(updater.Options{
		Bus:     bus,
		ExePath: exePath,
		Restart: restart,
		IsBusy:  app.HasActiveCall,
	})
	upd.Cleanup()
	app.SetUpdater(upd)
	// 下发配置里的更新站点（空 = 不检查）并启动轮询；headless 模式同样生效
	upd.SetUpdateConfig(core.SiteConfigSnapshot{
		URL:        cfg.Update.URL,
		AppID:      cfg.Update.AppID,
		Interval:   cfg.Update.Interval(),
		Prerelease: cfg.Update.Prerelease,
	})
	upd.Start(ctx)

	// 音频管理器：设备枚举与选择（REQ-MEDIA-002）。
	// 后端初始化在后台进行（音频服务不可达时 miniaudio 会长时间阻塞），
	// 最多等 2 秒即继续启动，就绪后由界面刷新设备列表。
	audioMgr := audio.NewManager()
	defer audioMgr.Close()
	// 网页控制台的配置页要枚举/选择音频设备（REQ-WS-016）：core 不能 import
	// internal/audio（会循环），所以注入一个适配器。
	app.SetAudioControl(audio.NewControl(audioMgr))
	// 音频后端是后台初始化的：就绪后广播 audio.ready，网页据此刷新设备列表
	//（桌面版用自己的 OnReady 回调刷新下拉，两者互不顶掉，见 Manager.OnReady）
	audioMgr.OnReady(app.NotifyAudioReady)
	// SIP 软电话：本地监听端口 40000~50000 随机选择空闲端口（REQ-ACC-013 修订），
	// rport 按配置开关（REQ-ACC-016，默认开启）
	sip.SetTraceEnabled(cfg.Log.SIPTrace) // SIP 信令记录按配置开关（默认关）
	phone, err := sip.NewPhone(app, audioMgr, cfg.Account.RPort)
	if err != nil {
		logx.Logger().Error("初始化 SIP 软电话失败", "err", err)
		os.Exit(1)
	}
	// TLS（REQ-ACC-006）：自签证书与密钥放实例自己的数据目录（多实例各一份）；
	// 只在传输方式为 tls 时才生成/启动 TLS 监听（见 Phone.Register）
	phone.SetTLSDir(filepath.Join(cfgDir, "tls"))
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
	// 按配置预选音频设备（仅记录选择，音频流在通话时按选择启动）
	if cfg.Audio.MicID != "" {
		audioMgr.SetMic(cfg.Audio.MicID)
	}
	if cfg.Audio.SpeakerID != "" {
		audioMgr.SetSpeaker(cfg.Audio.SpeakerID)
	}
	if cfg.Audio.RingSpeakerID != "" {
		audioMgr.SetRingSpeaker(cfg.Audio.RingSpeakerID)
	}
	// 音频预处理（REQ-MEDIA-007）：开关随通话流创建时生效
	audioMgr.SetPreprocess(audio.Preprocess{AEC: cfg.Audio.AEC, NS: cfg.Audio.NS, AGC: cfg.Audio.AGC})
	// 视频配置（REQ-VIDEO-002/005）：摄像头选择与采集参数在通话建立时生效
	phone.SetVideoConfig(cfg.Video)
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
	if wsPort := wsapi.FindFreeWSPort(cfg.WS.Port, cfg.WS.AllowRemote); wsPort != cfg.WS.Port {
		logx.Logger().Info("WS 端口被占用，自动选择空闲端口", "port", wsPort)
		cfg.WS.Port = wsPort
		_ = cfg.Save(cfgPath)
	}
	srv := wsapi.New(app)
	go func() {
		if err := srv.ListenAndServe(ctx, cfg.WS.Port, cfg.WS.AllowRemote); err != nil {
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

	// 9. GUI（无界面模式跳过；runGUI 按构建标签拆分——
	// headless 构建不含界面层，供纯 Go 交叉编译）。
	if !*headless {
		runGUI(app, audioMgr, cfg, cfgPath, ctx, restart, slot)
		stop()
		shutdown(app, tel) // 退出前注销 SIP（REQ-ACC-009）
		closeCallLog()     // 先放掉数据库，避免新进程启动时撞上旧库的写锁
		closeContacts()
		spawnNewProcessIfRequested(&restartRequested, exePath, releaseLock)
		return
	}

	<-ctx.Done()
	logx.Logger().Info("收到退出信号，正在关闭")
	shutdown(app, tel)
	closeCallLog()
	closeContacts()
	spawnNewProcessIfRequested(&restartRequested, exePath, releaseLock)
}

// spawnNewProcessIfRequested 自动更新/重启的收尾：释放单实例锁并拉起新进程。
// 只能在退出收尾调用（WS/SIP 监听已关闭、SIP 已注销）：先释放锁再启动，
// 否则新进程因锁冲突转发参数后退出；放在 shutdown 之后则避免 WS 端口漂移
// 与 SIP 双注册。拉起失败时新版本文件已就位，用户手动启动即可。
func spawnNewProcessIfRequested(requested *atomic.Bool, exePath string, releaseLock func()) {
	if !requested.Load() {
		return
	}
	logx.Logger().Info("重启收尾：释放单实例锁", "exe", exePath)
	if releaseLock != nil {
		releaseLock()
	}
	if exePath == "" {
		logx.Logger().Error("无法确定可执行文件路径，重启失败，请手动启动程序")
		return
	}
	cmd := exec.Command(exePath, os.Args[1:]...)
	// Unix 侧脱离父进程会话（setsid），避免随父进程/终端退出被终止
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		logx.Logger().Error("拉起新进程失败，请手动启动程序", "exe", exePath, "err", err)
		return
	}
	logx.Logger().Info("已拉起新进程，当前进程退出", "pid", cmd.Process.Pid)
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

// handleLaunchArgs 处理 sip:// / sips:// 协议拉起：解析注册信息 → 更新配置 → 触发注册。
// onDone 在 GUI 就绪后显示主界面（由调用方注入）。
func handleLaunchArgs(app *core.App, cfg *config.Config, args []string, onDone func()) {
	for _, a := range args {
		raw, ok := protocol.SplitURL(a)
		if !ok {
			continue
		}
		acc, err := protocol.ParseSipURL(raw)
		if err != nil {
			logx.Logger().Warn("sip:// / sips:// 链接解析失败", "url", raw, "err", err)
			continue
		}
		logx.Logger().Info("协议拉起", "url", raw, "server", acc.Server, "account", acc.Account, "transport", acc.Transport)

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
