// Package config 负责应用配置的加载、保存与密码加密存储。
//
// 配置文件位置遵循各平台规范（os.UserConfigDir）：
//   - Windows：%APPDATA%\mti-sip-phone\config.json
//   - Linux：  ~/.config/mti-sip-phone/config.json
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Account SIP 账户配置。
// Password 为运行时明文，仅存在于内存；落盘时加密为 PasswordEnc，不写明文。
type Account struct {
	Enabled     bool   `json:"enabled"`     // 是否启用该账户
	Server      string `json:"server"`      // SIP 服务器地址，如 10.168.4.40
	Port        int    `json:"port"`        // SIP 服务器端口，默认 5060
	Expires     int    `json:"expires"`     // 注册到期时间（秒），默认 300（REQ-ACC-018）
	RPort       bool   `json:"rport"`       // 启用 rport（NAT 穿透），默认 true（REQ-ACC-016）
	ChkOpt      bool   `json:"optchk"`      // 启用服务端心跳检测：注册的 User-Agent 带 optchk，服务端据此发 OPTIONS，默认 true（REQ-ACC-020）
	STUNEnabled bool   `json:"stunEnabled"` // 启用 STUN 公网地址探测（REQ-ACC-017）
	STUNServer  string `json:"stunServer"`  // STUN 服务器地址，默认 stun.l.google.com:19302
	Proxy       string `json:"proxy"`       // 出站 SIP 代理（host:port，可选，REQ-ACC-019）
	Transport   string `json:"transport"`   // 传输协议：udp / tcp
	Account     string `json:"account"`     // 账号（AOR 用户部分）
	AuthUser    string `json:"authUser"`    // 认证 ID，为空时使用 Account
	DisplayName string `json:"displayName"` // 显示名
	PasswordEnc string `json:"passwordEnc"` // 加密后的密码（base64，AES-256-GCM）
	Password    string `json:"-"`           // 明文密码，仅内存，不落盘
}

// WSConfig 本地 WebSocket 服务配置。
type WSConfig struct {
	Port int `json:"port"` // 监听端口，默认 8765，仅绑定 127.0.0.1；被占用时自动 +1
}

// LogConfig 日志配置。
type LogConfig struct {
	Level   string `json:"level"`   // debug / info / warn / error
	Dir     string `json:"dir"`     // 日志目录，为空时使用 <配置目录>/logs
	File    bool   `json:"file"`    // 是否写入文件，默认 true
	Console bool   `json:"console"` // 是否输出控制台，默认 false（写文件时不输出控制台）
	SIPTrace bool  `json:"sipTrace"` // 是否记录 SIP 信令（收发报文全文），默认 false
}

// AudioConfig 音频设备配置。
type AudioConfig struct {
	MicID         string `json:"micId"`         // 麦克风设备 ID，空 = 系统默认
	SpeakerID     string `json:"speakerId"`     // 扬声器设备 ID，空 = 系统默认
	RingSpeakerID string `json:"ringSpeakerId"` // 振铃音专用扬声器 ID，空 = 系统默认
}

// 摄像头禁用标志：CameraID 取该值时表示不使用摄像头。
// 语义（REQ-VIDEO-005）：发起视频呼叫不发送本地视频，来电也不提供「视频接听」，
// 但**仍可接听/接收对方的视频**（SDP 方向为 recvonly）。
const CameraDisabled = "disabled"

// VideoConfig 视频配置。
type VideoConfig struct {
	// CameraID 摄像头设备 ID；空 = 系统默认设备；CameraDisabled = 禁用。
	// Windows 侧驱动 ID 每次启动都可能变化，同时保存 CameraName 做兜底匹配。
	CameraID   string `json:"cameraId"`
	CameraName string `json:"cameraName"`
	Width      int    `json:"width"`       // 采集宽，默认 640
	Height     int    `json:"height"`      // 采集高，默认 480
	FPS        int    `json:"fps"`         // 帧率，默认 25
	BitrateKbps int   `json:"bitrateKbps"` // 码率（kbps），默认 512
}

// CameraUsable 摄像头是否可用（未被禁用）。
func (v VideoConfig) CameraUsable() bool { return v.CameraID != CameraDisabled }

// UIConfig 界面配置。
type UIConfig struct {
	IncomingAlwaysOnTop bool `json:"incomingAlwaysOnTop"` // 来电窗口置顶
	CallAlwaysOnTop     bool `json:"callAlwaysOnTop"`     // 通话面板置顶
}

// 更新检查周期的取值范围（分钟）：1 分钟 ~ 24 小时。
// 与旧版网页 SDK 的钳制口径一致（60 秒 ~ 24 小时）。
const (
	UpdateIntervalMinMins = 1
	UpdateIntervalMaxMins = 24 * 60
)

// UpdateConfig 自动更新配置（REQ-UPD-017，查询由软电话自己发起）。
//
// 软电话**不查更新服务器**的旧约定已作废：配了 URL 就按周期查
// `GET {URL}/api/v1/apps/{AppID}/check`（不带 channel，站点用应用默认通道），
// 查到更高版本照旧走「提示 → 用户确认 → 下载替换重启」。
// 网页可以经 WS `system.setUpdateConfig` 写这份配置（业务前端统一管理更新策略）。
type UpdateConfig struct {
	URL          string `json:"url"`             // 更新站点地址，空 = 不自动检查
	AppID        string `json:"appId"`           // 应用 ID，默认 mti-sip-phone
	IntervalMins int    `json:"intervalMinutes"` // 检查周期（分钟），默认 5，钳制 1–1440
	Prerelease   bool   `json:"prerelease"`      // 是否接受预览版，默认 false
}

// ClampUpdateIntervalMins 把检查周期钳制到合法范围（越界取边界，<=0 取默认 5）。
func ClampUpdateIntervalMins(m int) int {
	if m <= 0 {
		return 5
	}
	if m < UpdateIntervalMinMins {
		return UpdateIntervalMinMins
	}
	if m > UpdateIntervalMaxMins {
		return UpdateIntervalMaxMins
	}
	return m
}

// Interval 检查周期（time.Duration）。
func (u UpdateConfig) Interval() time.Duration {
	return time.Duration(ClampUpdateIntervalMins(u.IntervalMins)) * time.Minute
}

// Enabled 是否配置了更新站点（空 = 不自动检查）。
func (u UpdateConfig) Enabled() bool { return strings.TrimSpace(u.URL) != "" }

// Config 应用配置。
type Config struct {
	Version    int         `json:"version"` // 配置结构版本
	Account    Account     `json:"account"`
	WS         WSConfig    `json:"ws"`
	Log        LogConfig   `json:"log"`
	Audio      AudioConfig `json:"audio"`
	Video      VideoConfig `json:"video"`
	UI         UIConfig    `json:"ui"`
	Update     UpdateConfig `json:"update"`
	AutoStart  bool         `json:"autoStart"`  // 开机自动启动，默认 true
	RecentDial []string     `json:"recentDial"` // 最近呼叫号码（去重，最多 5 个，供拨号下拉）
}

// Default 返回默认配置。
func Default() *Config {
	return &Config{
		Version:   1,
		AutoStart: true,
		Account: Account{
			Enabled:    true,
			Port:       5060,
			Expires:    300,
			RPort:      true,
			ChkOpt:     true,
			STUNServer: "stun.l.google.com:19302",
			Transport:  "udp",
		},
		WS: WSConfig{
			Port: 8765,
		},
		Log: LogConfig{
			Level: "info",
			File:  true,
		},
		Video: VideoConfig{
			Width:   640,
			Height:  480,
			FPS:     25,
			// 640×480@25 下 512kbps 偏紧，观感发糊；默认给 1Mbps
			BitrateKbps: 1024,
		},
		UI: UIConfig{
			IncomingAlwaysOnTop: true,
			CallAlwaysOnTop:     true,
		},
		Update: UpdateConfig{
			// URL 留空：默认不自动检查更新（配了站点才开始查，REQ-UPD-017）
			AppID:        "mti-sip-phone",
			IntervalMins: 5,
		},
	}
}

// Load 加载配置；文件不存在时创建默认配置并保存。
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := save(cfg, path); err != nil {
			return nil, fmt.Errorf("创建默认配置失败: %w", err)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("配置解析失败（JSON 格式错误）: %w", err)
	}
	// 兼容旧配置：无 autoStart 字段时按默认启用；无 update 段时整段取默认
	//（旧配置根本没有这一段——不能因为零值就把 AppID 空着、周期当 0 用）
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		if _, ok := raw["autoStart"]; !ok {
			cfg.AutoStart = true
		}
		if _, ok := raw["update"]; !ok {
			cfg.Update = Default().Update
		}
	}

	// 解密密码
	if cfg.Account.PasswordEnc != "" {
		plain, err := decryptPassword(cfg.Account.PasswordEnc)
		if err != nil {
			// 密码解密失败不阻断启动，但提示用户重新输入
			cfg.Account.Password = ""
			fmt.Fprintf(os.Stderr, "警告: 账户密码解密失败(%v)，请在设置中重新输入\n", err)
		} else {
			cfg.Account.Password = plain
		}
	}

	// 补默认值
	if cfg.Account.Port == 0 {
		cfg.Account.Port = 5060
	}
	if cfg.Account.Expires == 0 {
		cfg.Account.Expires = 300
	}
	if cfg.Account.STUNServer == "" {
		cfg.Account.STUNServer = "stun.l.google.com:19302"
	}
	if cfg.Account.Transport == "" {
		cfg.Account.Transport = "udp"
	}
	if cfg.WS.Port == 0 {
		cfg.WS.Port = 8765
	}
	// 自动更新：逐字段补零值 + 周期钳制（URL 空是合法值 = 不检查，不能补）
	if cfg.Update.AppID == "" {
		cfg.Update.AppID = "mti-sip-phone"
	}
	cfg.Update.IntervalMins = ClampUpdateIntervalMins(cfg.Update.IntervalMins)
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	// 旧配置兼容：file 与 console 均未设置时按默认（写文件）
	if !cfg.Log.File && !cfg.Log.Console {
		cfg.Log.File = true
	}
	// 视频默认值（1.0.3 之前没有 video 段）
	if cfg.Video.Width <= 0 {
		cfg.Video.Width = 640
	}
	if cfg.Video.Height <= 0 {
		cfg.Video.Height = 480
	}
	if cfg.Video.FPS <= 0 {
		cfg.Video.FPS = 25
	}
	// 视频码率：<=0 补默认；恰好等于旧默认值 512 的也一并升级到 1Mbps
	//（此前没有界面可设置码率，配置里出现 512 只可能是旧默认值）
	if cfg.Video.BitrateKbps <= 0 || cfg.Video.BitrateKbps == 512 {
		cfg.Video.BitrateKbps = 1024
	}
	return cfg, nil
}

// Save 保存配置到磁盘（原子写入，密码加密后落盘）。
func (c *Config) Save(path string) error { return save(c, path) }

func save(c *Config, path string) error {
	// 密码加密：仅内存明文参与序列化前加密
	if c.Account.Password != "" {
		enc, err := encryptPassword(c.Account.Password)
		if err != nil {
			return fmt.Errorf("密码加密失败: %w", err)
		}
		c.Account.PasswordEnc = enc
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 原子写：先写临时文件再重命名，避免中途断电损坏配置
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ConfigDir 返回配置目录（按平台规范）。
func ConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mti-sip-phone"), nil
}
