// Package config 负责应用配置的加载、保存与密码加密存储。
//
// 配置文件位置遵循各平台规范（os.UserConfigDir）：
//   - Windows：%APPDATA%\mti-sip-phone\config.json
//   - Linux：  ~/.config/mti-sip-phone/config.json
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	Transport   string `json:"transport"`   // 传输协议：udp / tcp / tls（REQ-ACC-006）
	TLSVerify   bool   `json:"tlsVerify"`   // 校验服务器证书（仅 transport=tls 生效）。默认 false = 跳过校验（REQ-ACC-006：内网自签证书场景）
	SRTP        string `json:"srtp"`        // 媒体加密（SDES-SRTP，REQ-MEDIA-009）：off / supported / preferred / required，默认 supported（空值按默认）
	Domain      string `json:"domain"`      // SIP 域名（可选）：From/To/R-URI 里的域。留空 = 用 Server（REQ-ACC-022）
	PublicIP    string `json:"publicIP"`    // 对外声明的 IP（可选）：Via/Contact/SDP 里写的地址。留空 = 自动（STUN → 本机出口 IP），REQ-ACC-023
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
	Level    string `json:"level"`    // debug / info / warn / error
	Dir      string `json:"dir"`      // 日志目录，为空时使用 <配置目录>/logs
	File     bool   `json:"file"`     // 是否写入文件，默认 true
	Console  bool   `json:"console"`  // 是否输出控制台，默认 false（写文件时不输出控制台）
	SIPTrace bool   `json:"sipTrace"` // 是否记录 SIP 信令（收发报文全文），默认 false
}

// AudioConfig 音频设备配置。
type AudioConfig struct {
	MicID         string `json:"micId"`         // 麦克风设备 ID，空 = 系统默认
	SpeakerID     string `json:"speakerId"`     // 扬声器设备 ID，空 = 系统默认
	RingSpeakerID string `json:"ringSpeakerId"` // 振铃音专用扬声器 ID，空 = 系统默认

	// —— 音频预处理（REQ-MEDIA-007，webrtc-audio-processing）——
	// 三个开关都走"默认值由 Default() 给、Load 先取 Default 再反序列化覆盖"的
	// 老路，所以旧配置缺字段时仍然拿到默认值。
	//
	// 默认：AEC（回声消除）与 NS（降噪）**开**——外放时的自激回声是软电话
	// 最常见的投诉，降噪基本无损；AGC（自动增益）**关**：它会和 REQ-MEDIA-006
	// 的手动麦克风增益"较劲"，先让用户按需开启。
	AEC bool `json:"aec"` // 回声消除
	NS  bool `json:"ns"`  // 降噪
	AGC bool `json:"agc"` // 自动增益
}

// 摄像头禁用标志：CameraID 取该值时表示不使用摄像头。
// 语义（REQ-VIDEO-005）：发起视频呼叫不发送本地视频，来电也不提供「视频接听」，
// 但**仍可接听/接收对方的视频**（SDP 方向为 recvonly）。
const CameraDisabled = "disabled"

// VideoConfig 视频配置。
type VideoConfig struct {
	// Disabled 关闭「启用视频通话」总开关（REQ-VIDEO-014，默认 false = 启用）。
	//
	// 这里用**反向字段**而不是 Enabled：零值必须等于"启用"——旧配置没有该
	// 字段、沙箱实例的空白配置、任何 VideoConfig{} 字面量都不该把视频关掉
	//（与 core.Call.micGain 的「0 有专有含义」是同一类坑）。
	Disabled bool `json:"disabled"`

	// CameraID 摄像头设备 ID；空 = 系统默认设备；CameraDisabled = 禁用。
	// Windows 侧驱动 ID 每次启动都可能变化，同时保存 CameraName 做兜底匹配。
	CameraID    string `json:"cameraId"`
	CameraName  string `json:"cameraName"`
	Width       int    `json:"width"`       // 采集宽，默认 640
	Height      int    `json:"height"`      // 采集高，默认 480
	FPS         int    `json:"fps"`         // 帧率，默认 25
	BitrateKbps int    `json:"bitrateKbps"` // 码率（kbps），默认 512
}

// Enabled 视频通话功能是否启用（总开关，默认启用，REQ-VIDEO-014）。
func (v VideoConfig) Enabled() bool { return !v.Disabled }

// CameraUsable 摄像头是否可用（总开关启用且未被禁用）。
func (v VideoConfig) CameraUsable() bool { return v.Enabled() && v.CameraID != CameraDisabled }

// WindowPlacement 窗口位置记忆（物理像素 = 窗口矩形左上角）。
//
// Moved=false 表示用户**从未移动过**该窗口，位置一律由程序按默认口径摆放
// （通话面板：工作区右下角 + 多实例列偏移；视频窗口：通话面板正上方、右边缘对齐），
// 此时 X/Y 无意义。用户拖动过一次之后默认摆放不再覆盖，改用这里记下的位置
// （重启后依然生效）。保存值越界（显示器拔掉/分辨率变化）时摆放会回退默认，
// 但**不清掉**配置值，屏幕回来还能用。
type WindowPlacement struct {
	Moved bool `json:"moved,omitempty"`
	X     int  `json:"x,omitempty"`
	Y     int  `json:"y,omitempty"`
}

// UIConfig 界面配置。
type UIConfig struct {
	IncomingAlwaysOnTop bool `json:"incomingAlwaysOnTop"` // 来电窗口置顶
	CallAlwaysOnTop     bool `json:"callAlwaysOnTop"`     // 通话面板置顶

	// 通话面板 / 视频窗口的位置记忆（REQ-UI-040 / REQ-VIDEO-015）。
	CallPanel   WindowPlacement `json:"callPanel,omitempty"`
	VideoWindow WindowPlacement `json:"videoWindow,omitempty"`

	// VideoTopmost 视频窗口置顶（REQ-VIDEO-015）：原为「会话内记忆」，
	// 现在随窗口位置一并持久化（默认 false = 不置顶）。
	VideoTopmost bool `json:"videoTopmost,omitempty"`

	// LocalPreviewRotation / LocalPreviewMirror 本地回显的旋转（0/90/180/270）与
	// 水平镜像（REQ-VIDEO-015）。**只记本地回显**——远端画面的旋转/镜像按用户
	// 要求仍是会话内状态，不落盘（每次启动归零）。
	LocalPreviewRotation int  `json:"localPreviewRotation,omitempty"`
	LocalPreviewMirror   bool `json:"localPreviewMirror,omitempty"`
}

// NormalizeVideoRotation 规范化画面旋转角：先取 360 的模，非 90 的整倍数归 0
// （手改配置写坏时不留半吊子角度）。与 core.SetVideoRotation 的口径一致。
func NormalizeVideoRotation(deg int) int {
	deg = ((deg % 360) + 360) % 360
	if deg%90 != 0 {
		return 0
	}
	return deg
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
	Version    int          `json:"version"` // 配置结构版本
	Account    Account      `json:"account"`
	WS         WSConfig     `json:"ws"`
	Log        LogConfig    `json:"log"`
	Audio      AudioConfig  `json:"audio"`
	Video      VideoConfig  `json:"video"`
	UI         UIConfig     `json:"ui"`
	Update     UpdateConfig `json:"update"`
	AutoStart  bool         `json:"autoStart"`  // 开机自动启动，默认 true
	RecentDial []string     `json:"recentDial"` // 最近呼叫号码（去重，最多 5 个，供拨号下拉）
}

// InstanceWSBaseOffset 沙箱实例的 WS 端口起点相对默认端口的偏移（用户要求 +10）：
// 槽位 ≥1 的实例默认从 8765+10 起找空闲端口（找不到再 +1），避免与主实例抢 8765。
const InstanceWSBaseOffset = 10

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
		// 音频预处理（REQ-MEDIA-007）：AEC/NS 默认开，AGC 默认关
		Audio: AudioConfig{AEC: true, NS: true},
		Video: VideoConfig{
			Width:  640,
			Height: 480,
			FPS:    25,
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

// SIP 传输协议（REQ-ACC-002/006）：udp / tcp / tls。
const (
	TransportUDP = "udp"
	TransportTCP = "tcp"
	TransportTLS = "tls"
)

// NormalizeTransport 规范化传输协议字符串：统一小写；空值或非法值回退 udp
// （旧配置没有 tls 字段，零值行为与从前一致）。配置文件、界面下拉、sip:// / sips://
// 链接解析三处共用这一份判定。
func NormalizeTransport(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case TransportTCP:
		return TransportTCP
	case TransportTLS:
		return TransportTLS
	default:
		return TransportUDP
	}
}

// NormalizeDomain 规范化 SIP 域名：去掉空白、`sip:`/`sips:` 前缀与 `user@`，
// 并丢掉端口部分（**注册/呼叫的网络目的地始终是 Server:Port**，域只是 URI 里的
// 名字，见 REQ-ACC-022）。空串 = 不配置（用 Server 作域）。
func NormalizeDomain(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	for _, p := range []string{"sips:", "sip:"} {
		if len(s) >= len(p) && strings.EqualFold(s[:len(p)], p) {
			s = s[len(p):]
		}
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[i:], "]") {
		s = s[:i] // host:port → host（IPv6 字面量有 ']'，不动）
	}
	return strings.Trim(s, "[] ")
}

// NormalizePublicIP 规范化"对外声明的 IP"：只接受 IP 字面量（SDP 的 c= 行必须
// 是 IP，域名会被拒），非法值一律当未配置（返回空 = 自动）。
func NormalizePublicIP(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "[]")
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return ""
}

// 媒体加密（SDES-SRTP，REQ-MEDIA-009）策略取值。
const (
	SRTPOff       = "off"       // 关闭：不协商 SRTP，对端要求也按明文应答
	SRTPSupported = "supported" // 支持（默认）：本端不主动声明，对端要求则配合加密
	SRTPPreferred = "preferred" // 建议：本端 offer 声明 SRTP，对端不接受则明文继续
	SRTPRequired  = "required"  // 必要：本端要求加密，对端不支持则拒绝本次通话
)

// NormalizeSRTP 规范化媒体加密策略字符串：统一小写；空值或非法值回退默认值
// **supported**（旧配置没有该字段，零值即默认"支持"）。配置页、配置文件与
// 协议链接共用这一份判定。
func NormalizeSRTP(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case SRTPOff:
		return SRTPOff
	case SRTPPreferred:
		return SRTPPreferred
	case SRTPRequired:
		return SRTPRequired
	default:
		return SRTPSupported
	}
}

// DefaultPortForTransport 传输协议对应的服务端默认端口：tls 用 5061（sips），
// 其余 5060。仅在用户没写端口时使用。
func DefaultPortForTransport(t string) int {
	if NormalizeTransport(t) == TransportTLS {
		return 5061
	}
	return 5060
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
	// 传输协议：空或非法值一律回退 udp（旧配置没有 tls，零值行为不变）
	cfg.Account.Transport = NormalizeTransport(cfg.Account.Transport)
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
	// 窗口位置记忆：旋转角规范化（0/90/180/270），Moved/坐标零值即默认。
	cfg.UI.LocalPreviewRotation = NormalizeVideoRotation(cfg.UI.LocalPreviewRotation)
	return cfg, nil
}

// Save 保存配置到磁盘（原子写入，密码加密后落盘）。
func (c *Config) Save(path string) error { return save(c, path) }

// saveMu 串行化落盘。
//
// 写盘本身已经是"临时文件 + rename"的原子写，但**临时文件名固定**
// （path + ".tmp"）且整个过程没有锁：两个 goroutine 并发保存时，会出现
// "A 把 tmp rename 走、B 的 fd 继续往同一个 inode 写"——落盘内容成了两份
// 配置拼起来的半截 JSON。写点本来就多（设置页、WS 侧、参数转发、拨号历史），
// 窗口拖动结束/画面变换又新增了两个，加一把锁最省心（写盘是低频快操作，
// 串行化的代价可忽略）。
var saveMu sync.Mutex

func save(c *Config, path string) error {
	saveMu.Lock()
	defer saveMu.Unlock()

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

// InstanceDir 指定"实例槽位"的数据目录（多实例沙箱）：
//
//	槽位 0（主实例）→ 原来的目录（**位置与内容都不变**，向后兼容）；
//	槽位 ≥1（沙箱实例）→ <主目录>/instances/<N>/（配置、通话记录、通信录、
//	日志、单实例锁全在这份目录里，各实例互不影响）。
//
// 沙箱目录不存在时创建（空白默认配置由调用方落盘）。
func InstanceDir(slot int) (string, error) {
	base, err := ConfigDir()
	if err != nil {
		return "", err
	}
	if slot <= 0 {
		return base, nil
	}
	dir := filepath.Join(base, "instances", strconv.Itoa(slot))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建实例目录失败: %w", err)
	}
	return dir, nil
}
