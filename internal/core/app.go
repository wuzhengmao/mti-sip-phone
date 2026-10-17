package core

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/variant"
)

// RegisterState 注册状态。
type RegisterState string

const (
	RegUnregistered RegisterState = "unregistered" // 未注册
	RegRegistering  RegisterState = "registering"  // 注册中
	RegRegistered   RegisterState = "registered"   // 已注册
	RegFailed       RegisterState = "failed"       // 注册失败
)

// CallState 通话状态。
type CallState string

const (
	CallRinging     CallState = "ringing"     // 振铃（呼入或呼出）
	CallEstablished CallState = "established" // 已接通
	CallTerminated  CallState = "terminated"  // 已结束
)

// Call 一次通话的运行时信息。
type Call struct {
	ID         string    `json:"id"`
	Direction  string    `json:"direction"`  // incoming 呼入 / outgoing 呼出
	Remote     string    `json:"remote"`     // 对方号码或 URI
	RemoteName string    `json:"remoteName"` // 对方显示名
	State      CallState `json:"state"`
	Muted      bool      `json:"muted"`
	// MicMuted / SpeakerMuted 麦克风与扬声器静音（纯本地动作，REQ-UI-023；
	// 桌面通话面板与网页控制共用同一份状态，变更时发 call.audio 事件）。
	MicMuted     bool `json:"micMuted"`
	SpeakerMuted bool `json:"speakerMuted"`

	// MicGain / SpeakerVolume 麦克风增益与扬声器音量（百分比，REQ-MEDIA-006）：
	// 麦克风 0–200、扬声器 0–100，**0 表示禁用**（同 MicMuted/SpeakerMuted）。
	// 禁用时这里保留"恢复值"（解禁后回到的音量），是否禁用看上面两个标志。
	MicGain       int `json:"micGain"`
	SpeakerVolume int `json:"speakerVolume"`
	// HasVideo 本次通话涉及视频（呼入时对端 offer 含视频段；呼出时本端发起了视频呼叫）。
	HasVideo bool `json:"hasVideo"`
	// Video 视频状态快照（未协商成功时为零值）。
	Video VideoState `json:"video"`
	// Contact 对端号码在通信录里的条目（命中才有，REQ-UI-037）：网页据此在
	// 号码旁显示名称与 ★、悬停显示备注。**发布事件时即时解析**（见 publishCall），
	// 所以通话中改通信录，后续事件的 contact 立刻是新的。
	Contact *ContactInfo `json:"contact,omitempty"`
}

// RegisterInfo 注册状态快照。
type RegisterInfo struct {
	State   RegisterState `json:"state"`
	Account string        `json:"account"`
	Reason  string        `json:"reason"` // 失败原因
}

// State 应用状态快照（供 WS 查询接口返回）。
type State struct {
	Version  string        `json:"version"`
	OS       string        `json:"os"`   // 编译目标平台（如 windows），前端据此查询对应产物
	Arch     string        `json:"arch"` // 编译目标架构（如 amd64）
	Register RegisterInfo  `json:"register"`
	Calls    []Call        `json:"calls"`
	Update   *UpdateStatus `json:"update,omitempty"` // 更新状态（无更新时省略）
	// AutoAnswer 自动接听开关（桌面按钮与网页开关共用一份状态）
	AutoAnswer bool `json:"autoAnswer"`
	// Video 显示用的旋转/镜像（远端 + 本地回显），网页连上就能拿到并与桌面同步
	Video VideoTransforms `json:"video"`
	// UpdateConfig 自动更新配置（站点/应用 ID/周期/预览版，REQ-UPD-017）：
	// 网页连上就能拿到当前值，也可用 system.setUpdateConfig 改写
	UpdateConfig config.UpdateConfig `json:"updateConfig"`
	// UpdatePrimarySite 官方更新站点（网页配置页「使用主站点」按钮一键填入用，
	// REQ-UPD-023）：网页不硬编码、由 system.state 下发
	UpdatePrimarySite string `json:"updatePrimarySite,omitempty"`
	// RecentDial 最近呼叫号码（最多 5 个，最新的在前）：网页控制台的拨号下拉用
	RecentDial []string `json:"recentDial"`
	// NoUI 当前进程是「仅托盘无 UI 版」（REQ-UI-041）：网页控制台据此调整——
	// 没有桌面视频窗口，所以自动订阅视频、隐藏「保留软电话窗口」、不允许取消订阅。
	NoUI bool `json:"noUI,omitempty"`
	// Instance 本进程的实例槽位（0 = 主实例，≥1 = 沙箱实例，REQ-NFR-007）：
	// 网页控制台在诊断页拼 sip:// 拉起链接时要带上它，否则拉起来的是主实例。
	Instance int `json:"instance"`
}

// 常见错误，供接口层映射为错误码。
var (
	ErrTelephonyNotReady = errors.New("电话能力尚未接入")
	ErrCallNotFound      = errors.New("通话不存在")
	ErrNoActiveCall      = errors.New("没有进行中的通话")
)

// App 应用核心：维护注册与通话状态机，向上层发布事件。
type App struct {
	cfg      *config.Config
	cfgPath  string
	tel      Telephony
	upd      Updater      // 自动更新能力（nil = 未接入）
	callLog  CallLog      // 通话记录存储（nil = 不记录，见 calllog.go）
	contacts ContactStore // 通信录存储（nil = 不可用，见 contacts.go）
	bus      *Bus

	// 通信录内存快照（见 contacts.go）：contactMu 护 contactMap / contactList，
	// 界面线程逐行查找与 WS 事件负载都读它，永不阻塞。
	// contactWriteMu 串行化"写存储 + 重建快照"（多个写入方并发时快照不会互相覆盖）。
	contactMu      sync.RWMutex
	contactWriteMu sync.Mutex
	contactMap     map[string]Contact // 规范化号码 → 条目
	contactList    []Contact          // 全量（规范化号码序，结果稳定）

	mu         sync.RWMutex
	reg        RegisterInfo
	calls      map[string]*Call
	callOrder  []string             // 保持创建顺序
	video      *videoDemux          // 视频帧多播出口（恒定对象，见 video.go）
	webViewers map[uint64]webViewer // 网页视频订阅（连接级，REQ-VIDEO-009）
	// pendingVideo 早到的视频状态（通话尚未登记，如视频呼叫拨出即开摄像头）
	pendingVideo map[string]VideoState
	// videoTx 显示用的旋转/镜像（桌面与网页共用一份，见 video.go）
	videoTx VideoTransforms
	// autoAnswer 自动接听开关（桌面按钮与网页开关共用一份，
	// 变更后广播 system.autoAnswer，见 SetAutoAnswer）
	autoAnswer bool

	// instance 实例槽位（0 = 主实例），见 SetInstanceSlot
	instance int

	// audioPub 音量/增益事件的合并发布（REQ-MEDIA-006，见 audio.go）
	audioPub audioPublisher

	// 配置类能力的外部实现（见 settings.go；都是 nil = 未接入）：
	// audioCtl 音频设备枚举与选择；sipTraceCtl SIP 信令记录开关；
	// autoStartCtl 开机自启项注册/移除；restartFn 重启程序（标记 + 取消 ctx）。
	audioCtl     AudioControl
	sipTraceCtl  func(bool)
	autoStartCtl func(bool) error
	restartFn    func() error
}

// AutoAnswerState 自动接听开关状态（system.autoAnswer 事件负载）。
type AutoAnswerState struct {
	Enabled bool `json:"enabled"`
}

// New 创建 App。tel 为 nil 时表示电话能力未接入（调用会返回 ErrTelephonyNotReady）。
func New(cfg *config.Config, tel Telephony, bus *Bus) *App {
	return &App{
		cfg:   cfg,
		tel:   tel,
		bus:   bus,
		reg:   RegisterInfo{State: RegUnregistered},
		calls: make(map[string]*Call),
		video: &videoDemux{},
	}
}

// Bus 返回事件总线（供接口层订阅）。
func (a *App) Bus() *Bus { return a.bus }

// Tel 返回电话层实现（可能为 nil；演示模式下为 *FakeTelephony）。
func (a *App) Tel() Telephony { return a.tel }

// SetTel 注入电话层实现（用于 App 与电话层互相引用的场景，启动前调用）。
func (a *App) SetTel(tel Telephony) { a.tel = tel }

// Config 返回当前配置（只读用途，修改需走 UpdateAccount 等方法）。
func (a *App) Config() *config.Config { return a.cfg }

// UpdateAccount 更新账户配置：保存到磁盘并触发重新注册。
func (a *App) UpdateAccount(acc config.Account) error {
	a.mu.Lock()
	a.cfg.Account = acc
	a.mu.Unlock()
	if a.cfgPath != "" {
		if err := a.cfg.Save(a.cfgPath); err != nil {
			return fmt.Errorf("保存配置失败: %w", err)
		}
	}
	logx.Logger().Info("账户配置已更新", "account", acc.Account, "server", acc.Server)
	if a.tel == nil {
		return nil
	}
	// 注销条件（REQ-ACC-024）：账户未启用 / 缺字段——两者都走注销（发
	// expires=0、停注册周期）。**「启用账户」同时也是"是否注册"的总开关**：
	// 托盘菜单「退出账号」把它置 false（注销 + 停周期）、「登录账号」置回 true
	// （立即注册），服务端 NOTIFY 注销同样置 false；WS 断线自动注销（REQ-WS-020）
	// 用的也是它——重连恢复时若此时为 false 就不会偷偷注册回来。sip:// 拉起在
	// handleLaunchArgs 里强制置 true（显式要求注册）。
	if !acc.Enabled || acc.Server == "" || acc.Account == "" {
		a.setRegister(RegUnregistered, "")
		a.bus.Publish("register.state", map[string]any{
			"state":   string(RegUnregistered),
			"account": acc.Account,
			"reason":  "",
		})
		return a.tel.Unregister(context.Background())
	}
	go func() {
		if err := a.tel.Register(context.Background(), acc); err != nil {
			a.setRegister(RegFailed, err.Error())
		}
	}()
	return nil
}

// SetInstanceSlot 记录实例槽位（0 = 主实例，≥1 = 沙箱实例，REQ-NFR-007）。
// 仅供对外展示（State.Instance）：网页控制台在诊断页拼 sip:// 拉起链接时
// 要带上它，否则拉起来的是主实例而不是当前这个沙箱实例。
func (a *App) SetInstanceSlot(slot int) { a.instance = slot }

// SetConfigPath 设置配置保存路径（供 UpdateAccount 落盘）。
func (a *App) SetConfigPath(path string) { a.cfgPath = path }

// Start 启动应用：发布就绪事件；账户已启用且有电话能力时发起注册。
func (a *App) Start(ctx context.Context) {
	a.bus.Publish("system.ready", map[string]any{"version": Version})
	logx.Logger().Info("应用核心启动")

	if a.tel == nil {
		return
	}
	acc := a.cfg.Account
	// 启动注册：**只由「启用账户」控制**（REQ-ACC-024——取消勾选即"退出账号"，
	// 启动不注册；托盘菜单「登录账号」置回 true 并立即注册；sip:// 拉起强制启用）。
	if acc.Enabled && acc.Server != "" && acc.Account != "" {
		go func() {
			if err := a.tel.Register(ctx, acc); err != nil {
				a.setRegister(RegFailed, err.Error())
			}
		}()
	}
}

// State 返回当前状态快照。
func (a *App) State() State {
	// 更新状态先取快照（updater 自持锁），再进 a.mu——updater 的等待
	// goroutine 会回调 a.HasActiveCall 取 a.mu，若这里持 a.mu 再调
	// updater 会形成两把锁互等
	var upd *UpdateStatus
	if a.upd != nil {
		if st := a.upd.Status(); st.Phase != UpdateIdle {
			upd = &st
		}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	st := State{
		Version:      Version,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Register:     a.reg,
		Calls:        make([]Call, 0, len(a.callOrder)),
		Update:       upd,
		Video:        a.videoTx,
		AutoAnswer:   a.autoAnswer,
		UpdateConfig: a.cfg.Update,
		// 最近呼叫号码（网页控制台的拨号下拉用；桌面端读 RecentDials）
		RecentDial: append([]string(nil), a.cfg.RecentDial...),
		NoUI:       variant.NoUI,
		Instance:   a.instance,
		// 官方更新站点（网页「使用主站点」按钮用，REQ-UPD-023）
		UpdatePrimarySite: config.PrimaryUpdateSite,
	}
	for _, id := range a.callOrder {
		c := *a.calls[id]
		a.attachContact(&c) // 通信录信息即时解析（通话中改通信录，快照立刻是新的）
		st.Calls = append(st.Calls, c)
	}
	return st
}

// attachContact 按对端号码补上通信录信息（命中才有；REQ-UI-037）。
//
// 只改传入的**副本**，不动 a.calls 里的对象：State/publishCall 是在不同锁下
// 读通话的，就地写会与其它读路径形成数据竞争。
func (a *App) attachContact(c *Call) {
	if c == nil {
		return
	}
	if info, ok := a.LookupContact(c.Remote); ok {
		c.Contact = &ContactInfo{Name: info.Name, Note: info.Note, Starred: info.Starred}
		return
	}
	c.Contact = nil
}

// ---------------------------------------------------------------------------
// 上行：上层（GUI / WS）调用的控制方法
// ---------------------------------------------------------------------------

// Dial 呼出。target 为号码或 SIP URI。
// 单通话限制：呼叫/振铃/通话中不允许发起新的呼叫。
func (a *App) Dial(ctx context.Context, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("号码不能为空")
	}
	if a.tel == nil {
		return "", ErrTelephonyNotReady
	}
	if a.HasActiveCall() {
		return "", fmt.Errorf("已有通话进行中，无法发起新的呼叫")
	}
	callID, err := a.tel.Dial(ctx, target)
	if err != nil {
		return "", err
	}
	a.upsertCall(&Call{
		ID:        callID,
		Direction: "outgoing",
		Remote:    target,
		State:     CallRinging,
	})
	// 通话记录（REQ-CALL-007）：必须在 addRecentDial（会同步写 config.json）
	// 与 publishCall 之前入队——前者是与结束事件抢时间的最大窗口，后者是
	// 界面收到事件后立刻查库刷新能看到这条记录的前提。
	a.logStart(CallRecord{
		CallID:    callID,
		Direction: "outgoing",
		Remote:    target,
		StartedAt: time.Now(),
	})
	a.addRecentDial(target)
	// 发布"呼出中"事件：GUI 进入呼叫状态（通话面板），WS 客户端
	//（如测试页）收到后可显示挂断按钮中止呼叫
	a.publishCall("call.dialing", callID)
	logx.Logger().Info("发起呼叫", "callId", callID, "target", target)
	return callID, nil
}

// addRecentDial 记录最近呼叫号码（去重、最多 5 个、持久化）。
func (a *App) addRecentDial(target string) {
	a.mu.Lock()
	recent := make([]string, 0, 6)
	recent = append(recent, target)
	for _, n := range a.cfg.RecentDial {
		if n != target {
			recent = append(recent, n)
		}
	}
	if len(recent) > 5 {
		recent = recent[:5]
	}
	a.cfg.RecentDial = recent
	cfgPath := a.cfgPath
	a.mu.Unlock()
	if cfgPath != "" {
		_ = a.cfg.Save(cfgPath)
	}
}

// RecentDials 最近呼叫号码列表（拨号下拉用）。
func (a *App) RecentDials() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, len(a.cfg.RecentDial))
	copy(out, a.cfg.RecentDial)
	return out
}

// Answer 接听。
func (a *App) Answer(callID string) error {
	c := a.getCall(callID)
	if c == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	if err := a.tel.Answer(callID); err != nil {
		return err
	}
	logx.Logger().Info("接听呼叫", "callId", callID)
	return nil
}

// Hangup 挂断。
func (a *App) Hangup(callID string) error {
	c := a.getCall(callID)
	if c == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	if err := a.tel.Hangup(callID); err != nil {
		return err
	}
	logx.Logger().Info("挂断呼叫", "callId", callID)
	return nil
}

// Reject 拒接，code 默认 486（忙）。
func (a *App) Reject(callID string, code int) error {
	c := a.getCall(callID)
	if c == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	if code == 0 {
		code = 486
	}
	if err := a.tel.Reject(callID, code); err != nil {
		return err
	}
	logx.Logger().Info("拒接呼叫", "callId", callID, "code", code)
	return nil
}

// SetAutoAnswer 开关自动接听。
//
// 状态存在 core（而不是各界面各存一份）：桌面「自动接听」按钮与网页
// system.setAutoAnswer 都改这里，变更后广播 system.autoAnswer，两边据此
// 同步显示（否则网页一开，桌面按钮还显示旧状态）。
// 来电自动接听时的视频策略见 internal/sip（视频呼叫按视频接听处理）。
func (a *App) SetAutoAnswer(enabled bool) error {
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	if err := a.tel.SetAutoAnswer(enabled); err != nil {
		return err
	}
	a.mu.Lock()
	a.autoAnswer = enabled
	a.mu.Unlock()
	a.bus.Publish("system.autoAnswer", AutoAnswerState{Enabled: enabled})
	return nil
}

// AutoAnswer 当前自动接听开关状态。
func (a *App) AutoAnswer() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.autoAnswer
}

// SetRPort 动态切换 rport（运行中立即生效）。
func (a *App) SetRPort(enabled bool) error {
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	return a.tel.SetRPort(enabled)
}

// SendDTMF 通话中发送 DTMF 音。
func (a *App) SendDTMF(callID, digit string) error {
	c := a.getCall(callID)
	if c == nil {
		return ErrCallNotFound
	}
	if c.State != CallEstablished {
		return fmt.Errorf("通话未接通，无法发送 DTMF")
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	return a.tel.SendDTMF(callID, digit)
}

// IsCallMuted 查询通话当前是否保持/静音。
func (a *App) IsCallMuted(callID string) bool {
	c := a.getCall(callID)
	return c != nil && c.Muted
}

// Mute 静音切换。
func (a *App) Mute(callID string, muted bool) error {
	c := a.getCall(callID)
	if c == nil {
		return ErrCallNotFound
	}
	if c.State != CallEstablished {
		return fmt.Errorf("通话未接通，无法静音")
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	if err := a.tel.Mute(callID, muted); err != nil {
		return err
	}
	a.updateCall(callID, func(c *Call) { c.Muted = muted })
	a.bus.Publish("call.muted", map[string]any{"callId": callID, "muted": muted})
	return nil
}

// MuteMic 麦克风静音（纯本地，无 SIP 信令；通话面板麦克风开关用）。
// 记入通话状态并广播 call.audio——桌面面板与网页都要据此同步开关外观。
func (a *App) MuteMic(callID string, muted bool) error {
	if a.getCall(callID) == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	if err := a.tel.MuteMic(callID, muted); err != nil {
		return err
	}
	logx.Logger().Info("麦克风静音", "callId", callID, "muted", muted)
	if a.updateCall(callID, func(c *Call) { c.MicMuted = muted }) {
		a.publishCall("call.audio", callID)
	}
	return nil
}

// MuteSpeaker 扬声器静音（纯本地，继续接收；通话面板扬声器开关用）。
// 状态与事件同 MuteMic。
func (a *App) MuteSpeaker(callID string, muted bool) error {
	if a.getCall(callID) == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	if err := a.tel.MuteSpeaker(callID, muted); err != nil {
		return err
	}
	logx.Logger().Info("扬声器静音", "callId", callID, "muted", muted)
	if a.updateCall(callID, func(c *Call) { c.SpeakerMuted = muted }) {
		a.publishCall("call.audio", callID)
	}
	return nil
}

// SetMicGain / SetSpeakerVolume 见 audio.go（REQ-MEDIA-006）。

// ---------------------------------------------------------------------------
// 视频（REQ-VIDEO-*）
// ---------------------------------------------------------------------------

// ErrVideoUnsupported 当前构建不支持视频。
var ErrVideoUnsupported = errors.New("当前构建未启用视频支持")

// ErrVideoDisabled 视频通话总开关已关闭（配置页的「启用视频通话」，REQ-VIDEO-014）。
var ErrVideoDisabled = errors.New("视频通话已关闭")

// SetVideoSink 注册视频帧出口的主槽（界面层启动时调用一次）。
func (a *App) SetVideoSink(s VideoSink) { a.video.setPrimary(s) }

// AddVideoSink 追加一个视频帧订阅者（网页视频中枢等；帧会同时投递给主槽）。
func (a *App) AddVideoSink(s VideoSink) { a.video.add(s) }

// RemoveVideoSink 移除附加订阅者（主槽用 SetVideoSink 覆盖）。
func (a *App) RemoveVideoSink(s VideoSink) { a.video.remove(s) }

// VideoFrameSink 电话层用来投递视频帧的出口，**恒不为 nil**。
// sip 层在建立/接听时取一次并接到视频引擎上；出口是多播对象，
// 之后增删订阅者无需重新取（见 videoDemux）。
func (a *App) VideoFrameSink() VideoSink { return a.video }

// VideoSupported 本构建/平台是否具备视频能力（不含用户的开关）。
func (a *App) VideoSupported() bool {
	return a.tel != nil && a.tel.VideoSupported()
}

// VideoCallEnabled 用户是否启用了视频通话（配置总开关，默认启用）。
func (a *App) VideoCallEnabled() bool {
	return a.tel != nil && a.tel.VideoCallEnabled()
}

// VideoUsable 视频功能整体是否可用（构建支持 + 总开关启用，REQ-VIDEO-014）。
// **视频入口的显隐、SDP 是否带 m=video、网页能否订阅视频都以它为准。**
func (a *App) VideoUsable() bool {
	return a.VideoSupported() && a.VideoCallEnabled()
}

// SetVideoCallEnabled 开/关视频通话总开关（配置页勾选项，立即生效）。
func (a *App) SetVideoCallEnabled(on bool) error {
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	return a.tel.SetVideoCallEnabled(on)
}

// CameraSupported 本构建/平台是否有摄像头采集能力。
func (a *App) CameraSupported() bool {
	return a.tel != nil && a.tel.CameraSupported()
}

// DialVideo 发起视频呼叫。
func (a *App) DialVideo(ctx context.Context, target string) (string, error) {
	if a.tel == nil {
		return "", ErrTelephonyNotReady
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("呼叫目标不能为空")
	}
	if a.HasActiveCall() {
		return "", errors.New("已有进行中的通话")
	}
	callID, err := a.tel.DialVideo(ctx, target)
	if err != nil {
		return "", err
	}
	a.upsertCall(&Call{
		ID:        callID,
		Direction: "outgoing",
		Remote:    target,
		State:     CallRinging,
		HasVideo:  true,
	})
	a.logStart(CallRecord{
		CallID:    callID,
		Direction: "outgoing",
		Remote:    target,
		StartedAt: time.Now(),
		HasVideo:  true,
	})
	a.addRecentDial(target)
	a.publishCall("call.dialing", callID)
	// 视频呼叫拨出即开摄像头/回显（sip 侧），其首个状态可能早于本次登记到达，
	// 已被 upsertCall 并入通话——这里补发一次事件，界面才会立刻显示本地画面
	//（顺序必须在 call.dialing 之后：界面要先有 activeCallID 才认这条事件）。
	// 条件带上 Enabled：摄像头禁用时 Sending=false，界面同样要拿到"视频已开启"
	// 才能把远端画面显示出来。
	if c := a.getCall(callID); c != nil && (c.Video.Enabled || c.Video.Sending || c.Video.Negotiated) {
		a.publishCall("call.video", callID)
	}
	return callID, nil
}

// AnswerVideo 视频接听（打开摄像头，双向视频）。
func (a *App) AnswerVideo(callID string) error {
	if a.getCall(callID) == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	return a.tel.AnswerVideo(callID)
}

// IncomingHasVideo 该来电的 offer 是否含可用视频段。
func (a *App) IncomingHasVideo(callID string) bool {
	if a.tel == nil {
		return false
	}
	return a.tel.IncomingHasVideo(callID)
}

// VideoActive 该通话的视频是否已协商成功且处于开启状态。
func (a *App) VideoActive(callID string) bool {
	if a.tel == nil {
		return false
	}
	return a.tel.VideoActive(callID)
}

// SetVideoEnabled 需求 8：关闭/恢复视频（不显示远端、不推本地、关闭视频窗口）。
func (a *App) SetVideoEnabled(callID string, on bool) error {
	if a.getCall(callID) == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	if err := a.tel.SetVideoEnabled(callID, on); err != nil {
		return err
	}
	logx.Logger().Info("视频开关", "callId", callID, "on", on)
	return nil
}

// SetVideoPreview 需求 7：本地视频回显/推送开关。
func (a *App) SetVideoPreview(callID string, on bool) error {
	if a.getCall(callID) == nil {
		return ErrCallNotFound
	}
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	if err := a.tel.SetVideoPreview(callID, on); err != nil {
		return err
	}
	logx.Logger().Info("本地视频开关", "callId", callID, "on", on)
	return nil
}

// SetCamera 选择摄像头（空 = 系统默认，config.CameraDisabled = 禁用）。
func (a *App) SetCamera(deviceID string) error {
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	return a.tel.SetCamera(deviceID)
}

// VideoDevices 枚举摄像头（可能耗时，调用方放后台 goroutine）。
func (a *App) VideoDevices() []VideoDevice {
	if a.tel == nil {
		return nil
	}
	return a.tel.VideoDevices()
}

// ---------------------------------------------------------------------------
// 下行：电话层（Telephony 实现）通过 EventSink 回调
// ---------------------------------------------------------------------------

// OnIncoming 呼入。创建呼入通话并进入振铃状态。
func (a *App) OnIncoming(callID, remote, remoteName string, hasVideo bool) {
	a.upsertCall(&Call{
		ID:         callID,
		Direction:  "incoming",
		Remote:     remote,
		RemoteName: remoteName,
		State:      CallRinging,
		HasVideo:   hasVideo,
	})
	// 通话记录（REQ-CALL-007）：来电的起点即振铃开始时刻
	a.logStart(CallRecord{
		CallID:     callID,
		Direction:  "incoming",
		Remote:     remote,
		RemoteName: remoteName,
		StartedAt:  time.Now(),
		HasVideo:   hasVideo,
	})
	logx.Logger().Info("收到来电", "callId", callID, "remote", remote, "hasVideo", hasVideo)
	a.publishCall("call.incoming", callID)
}

// OnVideoState 视频状态变化（协商成功、开关切换、拿到对端分辨率）。
//
// 视频呼叫在 sip 侧是**拨出即开摄像头/回显**的，状态可能在本 App 登记通话
// 之前就到达（Dial 返回后调用方才 upsertCall）。此时先存进 pendingVideo，
// 由 upsertCall 并入通话，并由 Dial/DialVideo 补发一次事件——否则首个
// "发送中" 状态会被丢掉，界面一直不显示视频窗口。
func (a *App) OnVideoState(callID string, st VideoState) {
	a.mu.Lock()
	c, ok := a.calls[callID]
	if !ok {
		if a.pendingVideo == nil {
			a.pendingVideo = make(map[string]VideoState)
		}
		a.pendingVideo[callID] = st
		a.mu.Unlock()
		return
	}
	c.Video = st
	c.HasVideo = c.HasVideo || st.Negotiated
	a.mu.Unlock()
	a.publishCall("call.video", callID)
}

// OnRinging 呼出振铃（earlyMedia 见 EventSink.OnRinging）。
func (a *App) OnRinging(callID string, earlyMedia bool) {
	if a.updateCall(callID, func(c *Call) { c.State = CallRinging }) {
		a.publishCall("call.ringing", callID, "earlyMedia", earlyMedia)
	}
}

// OnEstablished 通话建立。
func (a *App) OnEstablished(callID string) {
	if a.updateCall(callID, func(c *Call) { c.State = CallEstablished }) {
		logx.Logger().Info("通话建立", "callId", callID)
		a.logAnswered(callID) // 通话记录：接通时刻（先写记录再发事件）
		a.publishCall("call.established", callID)
	}
}

// OnTerminated 通话结束。
func (a *App) OnTerminated(callID, reason string) {
	var missed bool
	if !a.updateCall(callID, func(c *Call) {
		missed = missedOnEnd(c, reason)
		c.State = CallTerminated
	}) {
		return
	}
	logx.Logger().Info("通话结束", "callId", callID, "reason", reason, "missed", missed)
	a.logFinish(callID, reason, missed) // 通话记录：先写记录再发事件
	a.publishCall("call.terminated", callID, "reason", reason, "missed", missed)
}

// OnFailed 呼叫失败。
func (a *App) OnFailed(callID, reason string) {
	var missed bool
	if !a.updateCall(callID, func(c *Call) {
		missed = missedOnEnd(c, reason)
		c.State = CallTerminated
	}) {
		return
	}
	logx.Logger().Info("呼叫失败", "callId", callID, "reason", reason, "missed", missed)
	a.logFinish(callID, reason, missed) // 通话记录：先写记录再发事件
	a.publishCall("call.failed", callID, "reason", reason, "missed", missed)
}

// isHandledLocally 结束原因是否属于「本端当场处理」：振铃中拒接（603，
// sip 层 reason=rejected）与本端挂断（local_hangup）都算用户已处理，
// 不算未接来电（REQ-UI-010：用户当场看到并处理了，不该再弹桌面通知）。
func isHandledLocally(reason string) bool {
	switch reason {
	case "rejected", "local_hangup":
		return true
	}
	return false
}

// missedOnEnd 是否未接来电：呼入 + 结束前仍在振铃（从未接通）+ 非本端当场处理。
//
// ⚠️ 必须在 updateCall 的回调内调用：① 与状态更新同一临界区，不会与并发的
// OnEstablished 交错；② a.calls 从不删除已结束通话、sip 层也可能对同一通话
// 二次上报结束，用 State == CallRinging 判定天然幂等（第二次进来时状态已是
// CallTerminated），用 "!= CallEstablished" 则会重复判未接、重复弹通知。
func missedOnEnd(c *Call, reason string) bool {
	return c.Direction == "incoming" && c.State == CallRinging && !isHandledLocally(reason)
}

// OnRegisterState 注册状态变化。
func (a *App) OnRegisterState(state RegisterState, reason string) {
	a.setRegister(state, reason)
	logx.Logger().Info("注册状态变化", "state", string(state), "reason", reason)
	a.bus.Publish("register.state", map[string]any{
		"state":   string(state),
		"account": a.cfg.Account.Account,
		"reason":  reason,
	})
}

// OnRemoteCommand 处理服务端 NOTIFY 控制指令（REQ-ACC-025，协议见
// docs/cti-sip-phone-protocol.md）。由 sip 层的 NOTIFY handler 在独立
// goroutine 里调用（不阻塞信令事务；200 OK 已在 handler 内发出）。
//
// **视频能力一律"静默降级"**：withVideo=true 但话机未开启视频功能
// （VideoUsable：构建支持 + 配置总开关）或呼入本身不含视频（Call.HasVideo）
// 时，改为纯音频拨号/接听，只记一条 Info 日志，不报错、不中断指令。
func (a *App) OnRemoteCommand(cmd RemoteCommand) {
	logx.Logger().Info("收到服务端控制指令", "kind", cmd.Kind,
		"dest", cmd.DestNo, "withVideo", cmd.WithVideo, "reason", cmd.Reason)
	switch cmd.Kind {
	case "unregister":
		// 坐席登出通知注销（与托盘「退出账号」一致：禁用账户并立即注销）
		logx.Logger().Warn("服务端要求注销（MSP_UNREGISTER），退出账号并停止注册周期",
			"reason", cmd.Reason, "agent", cmd.Agent)
		if err := a.SetRegistered(false); err != nil {
			logx.Logger().Warn("按服务端通知注销失败", "reason", cmd.Reason, "err", err)
		}

	case "dial":
		if strings.TrimSpace(cmd.DestNo) == "" {
			logx.Logger().Warn("服务端拨号指令缺少 destNo，忽略")
			return
		}
		video := cmd.WithVideo && a.VideoUsable()
		if cmd.WithVideo && !video {
			logx.Logger().Info("服务端要求视频呼叫，但视频功能不可用，降级为纯音频呼叫",
				"dest", cmd.DestNo)
		}
		var (
			callID string
			err    error
		)
		if video {
			callID, err = a.DialVideo(context.Background(), cmd.DestNo)
		} else {
			callID, err = a.Dial(context.Background(), cmd.DestNo)
		}
		if err != nil {
			logx.Logger().Warn("服务端拨号失败", "dest", cmd.DestNo, "video", video, "err", err)
			return
		}
		logx.Logger().Info("服务端拨号已发起", "dest", cmd.DestNo, "video", video, "callId", callID)

	case "answer":
		c := a.ringingIncomingCall()
		if c == nil {
			logx.Logger().Warn("服务端接听指令：当前没有等待接听的来电，忽略")
			return
		}
		video := cmd.WithVideo && a.VideoUsable() && c.HasVideo
		if cmd.WithVideo && !video {
			logx.Logger().Info("服务端要求视频接听，但视频功能或本呼入视频不可用，降级为纯音频接听",
				"callId", c.ID)
		}
		if err := a.AnswerOrVideo(c.ID, video); err != nil {
			logx.Logger().Warn("服务端接听失败", "callId", c.ID, "video", video, "err", err)
		}

	case "hangup":
		c := a.activeCall()
		if c == nil {
			logx.Logger().Warn("服务端挂断指令：当前没有进行中的通话，忽略")
			return
		}
		if err := a.Hangup(c.ID); err != nil {
			logx.Logger().Warn("服务端挂断失败", "callId", c.ID, "err", err)
		}

	case "autoanswer":
		if cmd.AutoAnswer == nil {
			logx.Logger().Warn("服务端自动接听指令缺少 autoAnswer，忽略")
			return
		}
		if err := a.SetAutoAnswer(*cmd.AutoAnswer); err != nil {
			logx.Logger().Warn("服务端设置自动接听失败", "enabled", *cmd.AutoAnswer, "err", err)
		}

	default:
		logx.Logger().Warn("未知的服务端控制指令，忽略", "kind", cmd.Kind)
	}
}

// AnswerOrVideo 按是否视频接听当前来电（NOTIFY 接听指令用）：video=true 走
// 视频接听（未接通前可打开摄像头），否则普通接听（answer 里 m=video 0，
// 不接受视频）。
func (a *App) AnswerOrVideo(callID string, video bool) error {
	if video {
		return a.AnswerVideo(callID)
	}
	return a.Answer(callID)
}

// ringingIncomingCall 当前振铃中的**呼入**（服务端接听指令的目标）。
// 单通话限制下最多一条；没有则返回 nil。
func (a *App) ringingIncomingCall() *Call {
	for _, c := range a.State().Calls {
		if c.Direction == "incoming" && c.State == CallRinging {
			return &c
		}
	}
	return nil
}

// activeCall 当前进行中的通话（振铃或已接通，服务端挂断指令的目标）。
func (a *App) activeCall() *Call {
	for _, c := range a.State().Calls {
		if c.State == CallRinging || c.State == CallEstablished {
			return &c
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

func (a *App) setRegister(state RegisterState, reason string) {
	a.mu.Lock()
	a.reg = RegisterInfo{State: state, Account: a.cfg.Account.Account, Reason: reason}
	a.mu.Unlock()
}

// HasActiveCall 是否存在进行中的通话（振铃或已接通）。
func (a *App) HasActiveCall() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, c := range a.calls {
		if c.State == CallRinging || c.State == CallEstablished {
			return true
		}
	}
	return false
}

func (a *App) getCall(callID string) *Call {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.calls[callID]
}

// upsertCall 插入或覆盖通话记录，并记录创建顺序。
// 顺带并入早到的视频状态（见 OnVideoState 的说明）。
func (a *App) upsertCall(c *Call) {
	a.mu.Lock()
	if _, ok := a.calls[c.ID]; !ok {
		a.callOrder = append(a.callOrder, c.ID)
		// 新通话的音量/增益默认 100%（0 在这两个字段里表示"禁用"，不能靠零值默认；
		// 界面在通话建立时会按会话内记忆再下发一次真实值）
		if c.MicGain == 0 {
			c.MicGain = 100
		}
		if c.SpeakerVolume == 0 {
			c.SpeakerVolume = 100
		}
	}
	if st, ok := a.pendingVideo[c.ID]; ok {
		c.Video = st
		c.HasVideo = c.HasVideo || st.Negotiated
		delete(a.pendingVideo, c.ID)
	}
	a.calls[c.ID] = c
	a.mu.Unlock()
}

// updateCall 原子更新通话，返回通话是否存在。
func (a *App) updateCall(callID string, fn func(*Call)) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.calls[callID]
	if !ok {
		return false
	}
	fn(c)
	return true
}

// publishCall 发布带通话信息的呼叫事件。
func (a *App) publishCall(event, callID string, extra ...any) {
	data := map[string]any{"callId": callID}
	if c := a.getCall(callID); c != nil {
		cc := *c
		a.attachContact(&cc) // 副本上补通信录信息（REQ-UI-037）
		data["call"] = cc
	}
	// 附加字段：如 terminated 事件的 reason
	for i := 0; i+1 < len(extra); i += 2 {
		if k, ok := extra[i].(string); ok {
			data[k] = extra[i+1]
		}
	}
	a.bus.Publish(event, data)
}
