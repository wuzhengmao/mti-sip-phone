package core

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/logx"
)

// 配置类能力（网页控制台用，REQ-WS-016 起）：设备枚举、配置读写、重启。
//
// 这些能力都不属于"电话"，core 也拿不到 audio.Manager（它 import 了 core，
// 反向依赖会循环），所以按既有 Updater / CallLog / ContactStore 的同一套写法：
// **core 定义窄接口、main 注入实现、nil = 该能力不可用**。

// 常见错误，供接口层映射为错误码。
var (
	// ErrConfigInvalid 配置项校验失败（不落盘、不改内存）。
	ErrConfigInvalid = errors.New("配置项非法")
	// ErrAudioUnavailable 音频能力未接入（无音频后端的构建/初始化失败）。
	ErrAudioUnavailable = errors.New("音频设备不可用")
	// ErrConfigUnavailable 该配置项依赖的外部能力未接入（重启/自启/信令开关）。
	ErrConfigUnavailable = errors.New("该配置能力未接入")
)

// AudioPreprocess 音频预处理开关（REQ-MEDIA-007）。
type AudioPreprocess struct {
	AEC bool // 回声消除
	NS  bool // 降噪
	AGC bool // 自动增益
}

// AudioControl 音频设备能力（internal/audio 提供适配器，main 注入）。
// nil = 音频不可用（纯 Go 构建或后端初始化失败）。
type AudioControl interface {
	// Ready 后端是否可用（枚举与选择只在就绪后有意义）。
	Ready() bool
	// Devices 枚举麦克风与扬声器（**可能阻塞数百毫秒到十几秒**：音频服务
	// 不可达时 miniaudio 会长时间等待，调用方必须放后台 goroutine）。
	Devices() (mics, speakers []AudioDevice)
	// SetMic / SetSpeaker / SetRingSpeaker 选择设备（空 = 系统默认）。
	SetMic(deviceID string)
	SetSpeaker(deviceID string)
	SetRingSpeaker(deviceID string)
	// SetPreprocess 设置音频预处理（**从下一通电话生效**：音频流在通话开始
	// 时按当时的设置重建）。
	SetPreprocess(p AudioPreprocess)
	// PreprocessSupported 本构建是否带音频预处理（缺静态库时降级为无）。
	PreprocessSupported() bool
}

// SetAudioControl 注入音频能力（main 在组装阶段调用）。
func (a *App) SetAudioControl(ac AudioControl) { a.audioCtl = ac }

// AudioReady 音频后端是否就绪。
func (a *App) AudioReady() bool { return a.audioCtl != nil && a.audioCtl.Ready() }

// AudioPreprocessSupported 本构建是否带音频预处理。
func (a *App) AudioPreprocessSupported() bool {
	return a.audioCtl != nil && a.audioCtl.PreprocessSupported()
}

// AudioDevices 枚举音频设备（返回副本；未接入时返回空列表，不报错——
// 网页据此把设备下拉显示为"不可用"，而不是弹错误）。
func (a *App) AudioDevices() (mics, speakers []AudioDevice) {
	if a.audioCtl == nil {
		return nil, nil
	}
	return a.audioCtl.Devices()
}

// NotifyAudioReady 广播"音频后端就绪"（REQ-WS-016）：音频后端是后台初始化的
// （最多等 2 秒，可能更久），网页可能在就绪前就打开了配置页、设备列表是空的。
// 桌面版靠界面自己的 OnReady 回调刷新，无 UI 版没有界面，只能靠这条事件。
func (a *App) NotifyAudioReady() {
	a.bus.Publish("audio.ready", map[string]any{"ready": a.AudioReady()})
}

// ---------------------------------------------------------------------------
// 配置读写（脱敏快照 + 只改传入字段的补丁）
// ---------------------------------------------------------------------------

// AccountSnapshot 账户配置快照（REQ-WS-016）：**不含密码**，
// 只给一个 passwordSet 布尔值——网页永远拿不到明文，也不需要回显。
type AccountSnapshot struct {
	Enabled     bool   `json:"enabled"`
	Server      string `json:"server"`
	Port        int    `json:"port"`
	Expires     int    `json:"expires"`
	RPort       bool   `json:"rport"`
	ChkOpt      bool   `json:"optchk"`
	STUNEnabled bool   `json:"stunEnabled"`
	STUNServer  string `json:"stunServer"`
	Proxy       string `json:"proxy"`
	Transport   string `json:"transport"`
	TLSVerify   bool   `json:"tlsVerify"`
	SRTP        string `json:"srtp"`
	Domain      string `json:"domain"`
	PublicIP    string `json:"publicIP"`
	Account     string `json:"account"`
	AuthUser    string `json:"authUser"`
	DisplayName string `json:"displayName"`
	// PasswordSet 是否已设置密码（网页的密码框据此决定占位文案）
	PasswordSet bool `json:"passwordSet"`
}

// AudioSnapshot 音频配置快照（含设备后端状态）。
type AudioSnapshot struct {
	config.AudioConfig
	Ready               bool `json:"ready"`
	PreprocessSupported bool `json:"preprocessSupported"`
}

// VideoSnapshot 视频配置快照（含构建/设备能力）。
type VideoSnapshot struct {
	Enabled         bool   `json:"enabled"` // 总开关（= !Disabled，网页读起来更直观）
	Disabled        bool   `json:"disabled"`
	CameraID        string `json:"cameraId"`
	CameraName      string `json:"cameraName"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	FPS             int    `json:"fps"`
	BitrateKbps     int    `json:"bitrateKbps"`
	Supported       bool   `json:"supported"`       // 本构建是否有视频能力
	CameraSupported bool   `json:"cameraSupported"` // 本平台是否有摄像头采集
	CameraDisabled  string `json:"cameraDisabled"`  // 禁用摄像头的取值（网页下拉的"禁用"项）
}

// ConfigSnapshot 配置快照（网页配置页一次取全，REQ-WS-016）。
type ConfigSnapshot struct {
	Account   AccountSnapshot     `json:"account"`
	Audio     AudioSnapshot       `json:"audio"`
	Video     VideoSnapshot       `json:"video"`
	Log       config.LogConfig    `json:"log"`
	WS        config.WSConfig     `json:"ws"`
	Update    config.UpdateConfig `json:"update"`
	AutoStart bool                `json:"autoStart"`
	Recent    []string            `json:"recentDial"`
}

// ConfigSnapshot 返回配置快照（持锁拷贝，脱敏）。
func (a *App) ConfigSnapshot() ConfigSnapshot {
	a.mu.RLock()
	acc := a.cfg.Account
	audio := a.cfg.Audio
	video := a.cfg.Video
	logCfg := a.cfg.Log
	wsCfg := a.cfg.WS
	upd := a.cfg.Update
	autoStart := a.cfg.AutoStart
	recent := append([]string(nil), a.cfg.RecentDial...)
	a.mu.RUnlock()

	return ConfigSnapshot{
		Account: AccountSnapshot{
			Enabled:     acc.Enabled,
			Server:      acc.Server,
			Port:        acc.Port,
			Expires:     acc.Expires,
			RPort:       acc.RPort,
			ChkOpt:      acc.ChkOpt,
			STUNEnabled: acc.STUNEnabled,
			STUNServer:  acc.STUNServer,
			Proxy:       acc.Proxy,
			Transport:   acc.Transport,
			TLSVerify:   acc.TLSVerify,
			SRTP:        acc.SRTP,
			Domain:      acc.Domain,
			PublicIP:    acc.PublicIP,
			Account:     acc.Account,
			AuthUser:    acc.AuthUser,
			DisplayName: acc.DisplayName,
			PasswordSet: acc.Password != "" || acc.PasswordEnc != "",
		},
		Audio: AudioSnapshot{
			AudioConfig:         audio,
			Ready:               a.AudioReady(),
			PreprocessSupported: a.AudioPreprocessSupported(),
		},
		Video: VideoSnapshot{
			Enabled:         video.Enabled(),
			Disabled:        video.Disabled,
			CameraID:        video.CameraID,
			CameraName:      video.CameraName,
			Width:           video.Width,
			Height:          video.Height,
			FPS:             video.FPS,
			BitrateKbps:     video.BitrateKbps,
			Supported:       a.VideoSupported(),
			CameraSupported: a.CameraSupported(),
			CameraDisabled:  config.CameraDisabled,
		},
		Log:       logCfg,
		WS:        wsCfg,
		Update:    upd,
		AutoStart: autoStart,
		Recent:    recent,
	}
}

// AccountPatch 账户配置补丁：字段为指针，nil = 不改（口径与
// system.setUpdateConfig 一致）。
type AccountPatch struct {
	Enabled     *bool
	Server      *string
	Port        *int
	Expires     *int
	RPort       *bool
	ChkOpt      *bool
	STUNEnabled *bool
	STUNServer  *string
	Proxy       *string
	Transport   *string
	TLSVerify   *bool
	SRTP        *string
	Domain      *string
	PublicIP    *string
	Account     *string
	AuthUser    *string
	DisplayName *string
	// Password 非 nil 时改密码（空串 = 清空密码）；nil = 保持原密码
	Password *string
	// ClearPassword 显式清空密码（与 Password 二选一，ClearPassword 优先）
	ClearPassword bool
}

// PatchAccount 按补丁改账户配置：校验 →（内存 + 落盘）→ 重新注册。
// 校验失败返回 ErrConfigInvalid，且**不改任何东西**。
func (a *App) PatchAccount(p AccountPatch) (AccountSnapshot, error) {
	a.mu.RLock()
	acc := a.cfg.Account
	a.mu.RUnlock()

	if p.Enabled != nil {
		acc.Enabled = *p.Enabled
	}
	if p.Server != nil {
		acc.Server = strings.TrimSpace(*p.Server)
	}
	if p.Port != nil {
		if *p.Port < 0 || *p.Port > 65535 {
			return AccountSnapshot{}, fmt.Errorf("%w: 端口必须在 0–65535", ErrConfigInvalid)
		}
		acc.Port = *p.Port
	}
	if p.Expires != nil {
		if *p.Expires < 0 || *p.Expires > 86400 {
			return AccountSnapshot{}, fmt.Errorf("%w: 注册有效期必须在 0–86400 秒", ErrConfigInvalid)
		}
		acc.Expires = *p.Expires
	}
	if p.RPort != nil {
		acc.RPort = *p.RPort
	}
	if p.ChkOpt != nil {
		acc.ChkOpt = *p.ChkOpt
	}
	if p.STUNEnabled != nil {
		acc.STUNEnabled = *p.STUNEnabled
	}
	if p.STUNServer != nil {
		acc.STUNServer = strings.TrimSpace(*p.STUNServer)
	}
	if p.Proxy != nil {
		acc.Proxy = strings.TrimSpace(*p.Proxy)
	}
	if p.Transport != nil {
		t := strings.ToLower(strings.TrimSpace(*p.Transport))
		if t != "udp" && t != "tcp" && t != "tls" {
			return AccountSnapshot{}, fmt.Errorf("%w: 传输方式只能是 udp/tcp/tls", ErrConfigInvalid)
		}
		acc.Transport = t
	}
	if p.TLSVerify != nil {
		acc.TLSVerify = *p.TLSVerify
	}
	if p.SRTP != nil {
		s := strings.ToLower(strings.TrimSpace(*p.SRTP))
		if s != "off" && s != "supported" && s != "preferred" && s != "required" {
			return AccountSnapshot{}, fmt.Errorf("%w: 媒体加密只能是 off/supported/preferred/required", ErrConfigInvalid)
		}
		acc.SRTP = s
	}
	if p.Domain != nil {
		acc.Domain = config.NormalizeDomain(*p.Domain)
	}
	if p.PublicIP != nil {
		ip := strings.TrimSpace(*p.PublicIP)
		// SDP 的 c= 行写域名非法，所以这里必须是 IP 字面量（桌面侧同一口径）
		if ip != "" && net.ParseIP(ip) == nil {
			return AccountSnapshot{}, fmt.Errorf("%w: 声明 IP 必须是 IP 地址（留空 = 自动）", ErrConfigInvalid)
		}
		acc.PublicIP = ip
	}
	if p.Account != nil {
		acc.Account = strings.TrimSpace(*p.Account)
	}
	if p.AuthUser != nil {
		acc.AuthUser = strings.TrimSpace(*p.AuthUser)
	}
	if p.DisplayName != nil {
		acc.DisplayName = strings.TrimSpace(*p.DisplayName)
	}
	if p.ClearPassword {
		acc.Password = ""
		acc.PasswordEnc = ""
	} else if p.Password != nil {
		acc.Password = *p.Password
		acc.PasswordEnc = "" // 由 config.Save 重新加密落盘
	}
	// 端口为 0 时按传输方式补默认（与桌面侧口径一致）
	if acc.Port == 0 {
		acc.Port = config.DefaultPortForTransport(acc.Transport)
	}
	if acc.Enabled && (acc.Server == "" || acc.Account == "") {
		return AccountSnapshot{}, fmt.Errorf("%w: 启用账户时必须填写服务器与账号", ErrConfigInvalid)
	}
	if acc.STUNEnabled && acc.STUNServer == "" {
		return AccountSnapshot{}, fmt.Errorf("%w: 启用 STUN 时必须填写 STUN 服务器", ErrConfigInvalid)
	}

	if err := a.UpdateAccount(acc); err != nil {
		return AccountSnapshot{}, err
	}
	snap := a.ConfigSnapshot()
	a.bus.Publish("system.config", map[string]any{"section": "account", "values": snap.Account})
	return snap.Account, nil
}

// AudioPatch 音频配置补丁（nil = 不改）。
type AudioPatch struct {
	MicID         *string
	SpeakerID     *string
	RingSpeakerID *string
	AEC           *bool
	NS            *bool
	AGC           *bool
}

// PatchAudio 按补丁改音频配置：立即下发到音频后端 + 落盘。
// 预处理三项**从下一通电话生效**（音频流在通话开始时重建），与桌面口径一致。
func (a *App) PatchAudio(p AudioPatch) (AudioSnapshot, error) {
	a.mu.Lock()
	if p.MicID != nil {
		a.cfg.Audio.MicID = strings.TrimSpace(*p.MicID)
	}
	if p.SpeakerID != nil {
		a.cfg.Audio.SpeakerID = strings.TrimSpace(*p.SpeakerID)
	}
	if p.RingSpeakerID != nil {
		a.cfg.Audio.RingSpeakerID = strings.TrimSpace(*p.RingSpeakerID)
	}
	if p.AEC != nil {
		a.cfg.Audio.AEC = *p.AEC
	}
	if p.NS != nil {
		a.cfg.Audio.NS = *p.NS
	}
	if p.AGC != nil {
		a.cfg.Audio.AGC = *p.AGC
	}
	audioCfg := a.cfg.Audio
	cfgPath := a.cfgPath
	a.mu.Unlock()

	if a.audioCtl != nil {
		a.audioCtl.SetMic(audioCfg.MicID)
		a.audioCtl.SetSpeaker(audioCfg.SpeakerID)
		a.audioCtl.SetRingSpeaker(audioCfg.RingSpeakerID)
		a.audioCtl.SetPreprocess(AudioPreprocess{AEC: audioCfg.AEC, NS: audioCfg.NS, AGC: audioCfg.AGC})
	}
	if cfgPath != "" {
		if err := a.cfg.Save(cfgPath); err != nil {
			return AudioSnapshot{}, fmt.Errorf("保存配置失败: %w", err)
		}
	}
	a.bus.Publish("system.config", map[string]any{"section": "audio", "values": audioCfg})
	snap := a.ConfigSnapshot()
	return snap.Audio, nil
}

// VideoPatch 视频配置补丁（nil = 不改）。
type VideoPatch struct {
	Disabled    *bool
	CameraID    *string
	CameraName  *string
	Width       *int
	Height      *int
	FPS         *int
	BitrateKbps *int
}

// PatchVideo 按补丁改视频配置：总开关与摄像头立即生效，采集参数（宽高/帧率/
// 码率）**下一通电话生效**（与桌面口径一致）。
func (a *App) PatchVideo(p VideoPatch) (VideoSnapshot, error) {
	a.mu.Lock()
	if p.Disabled != nil {
		a.cfg.Video.Disabled = *p.Disabled
	}
	if p.CameraID != nil {
		a.cfg.Video.CameraID = strings.TrimSpace(*p.CameraID)
	}
	if p.CameraName != nil {
		a.cfg.Video.CameraName = strings.TrimSpace(*p.CameraName)
	}
	if p.Width != nil {
		if *p.Width < 64 || *p.Width > 1920 {
			a.mu.Unlock()
			return VideoSnapshot{}, fmt.Errorf("%w: 采集宽度必须在 64–1920", ErrConfigInvalid)
		}
		a.cfg.Video.Width = *p.Width
	}
	if p.Height != nil {
		if *p.Height < 64 || *p.Height > 1920 {
			a.mu.Unlock()
			return VideoSnapshot{}, fmt.Errorf("%w: 采集高度必须在 64–1920", ErrConfigInvalid)
		}
		a.cfg.Video.Height = *p.Height
	}
	if p.FPS != nil {
		if *p.FPS < 1 || *p.FPS > 30 {
			a.mu.Unlock()
			return VideoSnapshot{}, fmt.Errorf("%w: 帧率必须在 1–30", ErrConfigInvalid)
		}
		a.cfg.Video.FPS = *p.FPS
	}
	if p.BitrateKbps != nil {
		if *p.BitrateKbps < 64 || *p.BitrateKbps > 20480 {
			a.mu.Unlock()
			return VideoSnapshot{}, fmt.Errorf("%w: 码率必须在 64–20480 kbps", ErrConfigInvalid)
		}
		a.cfg.Video.BitrateKbps = *p.BitrateKbps
	}
	videoCfg := a.cfg.Video
	cfgPath := a.cfgPath
	a.mu.Unlock()

	if a.tel != nil {
		if err := a.tel.SetVideoCallEnabled(videoCfg.Enabled()); err != nil {
			logx.Logger().Warn("下发视频总开关失败", "err", err)
		}
		if err := a.tel.SetCamera(videoCfg.CameraID); err != nil {
			logx.Logger().Warn("下发摄像头选择失败", "err", err)
		}
		// 采集参数：电话层自己持有 VideoConfig，重新下发一次让它下通生效
		if setter, ok := a.tel.(interface{ SetVideoConfig(config.VideoConfig) }); ok {
			setter.SetVideoConfig(videoCfg)
		}
	}
	if cfgPath != "" {
		if err := a.cfg.Save(cfgPath); err != nil {
			return VideoSnapshot{}, fmt.Errorf("保存配置失败: %w", err)
		}
	}
	a.bus.Publish("system.config", map[string]any{"section": "video", "values": videoCfg})
	return a.ConfigSnapshot().Video, nil
}

// LogPatch 日志配置补丁（nil = 不改）。
type LogPatch struct {
	Level    *string
	Dir      *string
	File     *bool
	Console  *bool
	SIPTrace *bool
}

// PatchLog 按补丁改日志配置：级别与 SIP 信令记录立即生效，
// 写文件/控制台两项**重启后生效**（桌面口径一致）。
func (a *App) PatchLog(p LogPatch) (config.LogConfig, error) {
	a.mu.Lock()
	if p.Level != nil {
		lv := strings.ToLower(strings.TrimSpace(*p.Level))
		switch lv {
		case "debug", "info", "warn", "error":
			a.cfg.Log.Level = lv
		default:
			a.mu.Unlock()
			return config.LogConfig{}, fmt.Errorf("%w: 日志级别只能是 debug/info/warn/error", ErrConfigInvalid)
		}
	}
	if p.Dir != nil {
		a.cfg.Log.Dir = strings.TrimSpace(*p.Dir)
	}
	if p.File != nil {
		a.cfg.Log.File = *p.File
	}
	if p.Console != nil {
		a.cfg.Log.Console = *p.Console
	}
	if p.SIPTrace != nil {
		a.cfg.Log.SIPTrace = *p.SIPTrace
	}
	logCfg := a.cfg.Log
	cfgPath := a.cfgPath
	a.mu.Unlock()

	logx.SetLevel(logCfg.Level)
	if p.SIPTrace != nil && a.sipTraceCtl != nil {
		a.sipTraceCtl(logCfg.SIPTrace)
	}
	if cfgPath != "" {
		if err := a.cfg.Save(cfgPath); err != nil {
			return config.LogConfig{}, fmt.Errorf("保存配置失败: %w", err)
		}
	}
	a.bus.Publish("system.config", map[string]any{"section": "log", "values": logCfg})
	return logCfg, nil
}

// SetSIPTraceController 注入"SIP 信令记录开关"的下发实现（main 里接
// sip.SetTraceEnabled；nil = 该能力未接入）。
func (a *App) SetSIPTraceController(fn func(bool)) { a.sipTraceCtl = fn }

// SetAutoStartController 注入开机自启的注册/移除实现（main 里接
// autostart.Enable/Disable）。
func (a *App) SetAutoStartController(fn func(bool) error) { a.autoStartCtl = fn }

// SetAutoStart 开/关开机自启：落盘 + 立即注册/移除系统项。
func (a *App) SetAutoStart(on bool) (bool, error) {
	if a.autoStartCtl == nil {
		return false, ErrConfigUnavailable
	}
	if err := a.autoStartCtl(on); err != nil {
		return false, fmt.Errorf("设置开机自启失败: %w", err)
	}
	a.mu.Lock()
	a.cfg.AutoStart = on
	cfgPath := a.cfgPath
	a.mu.Unlock()
	if cfgPath != "" {
		if err := a.cfg.Save(cfgPath); err != nil {
			return on, fmt.Errorf("保存配置失败: %w", err)
		}
	}
	a.bus.Publish("system.config", map[string]any{"section": "autoStart", "values": map[string]any{"enabled": on}})
	return on, nil
}

// WSPatch 网页接入配置补丁（字段可选，nil = 不改）。
type WSPatch struct {
	Port *int
	// AllowRemote 允许远程连接（监听 0.0.0.0、不拒绝非回环来源）。
	// ⚠️ WS 接口没有鉴权，放开后同网段任何设备都能控制本机软电话。
	AllowRemote *bool
}

// PatchWS 修改"网页接入"配置（**重启后生效**，与桌面的端口口径一致）。
// 返回是否真的需要重启（两项都没变就是 false）。
func (a *App) PatchWS(p WSPatch) (restartRequired bool, err error) {
	if p.Port == nil && p.AllowRemote == nil {
		return false, nil
	}
	if p.Port != nil && (*p.Port < 1 || *p.Port > 65535) {
		return false, fmt.Errorf("%w: 端口必须在 1–65535", ErrConfigInvalid)
	}
	a.mu.Lock()
	changed := (p.Port != nil && a.cfg.WS.Port != *p.Port) ||
		(p.AllowRemote != nil && a.cfg.WS.AllowRemote != *p.AllowRemote)
	if p.Port != nil {
		a.cfg.WS.Port = *p.Port
	}
	if p.AllowRemote != nil {
		a.cfg.WS.AllowRemote = *p.AllowRemote
	}
	ws := a.cfg.WS
	cfgPath := a.cfgPath
	a.mu.Unlock()
	if cfgPath != "" {
		if err := a.cfg.Save(cfgPath); err != nil {
			return changed, fmt.Errorf("保存配置失败: %w", err)
		}
	}
	a.bus.Publish("system.config", map[string]any{
		"section": "ws", "values": map[string]any{
			"port": ws.Port, "allowRemote": ws.AllowRemote, "restartRequired": changed,
		},
	})
	return changed, nil
}

// SetRestartFunc 注入"重启程序"的实现（main 的 restart：标记重启 + 取消 ctx，
// 真正的拉起新进程在退出收尾做，见 main.spawnNewProcessIfRequested）。
func (a *App) SetRestartFunc(fn func() error) { a.restartFn = fn }

// Restart 重启程序（REQ-UI-017 的网页路径）。
// ⚠️ 调用后本进程会很快退出：接口层必须**先回响应**再调用它。
func (a *App) Restart() error {
	if a.restartFn == nil {
		return ErrConfigUnavailable
	}
	logx.Logger().Info("收到重启请求（网页控制台）")
	return a.restartFn()
}
