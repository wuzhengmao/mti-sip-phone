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
	ID         string `json:"id"`
	Direction  string `json:"direction"`  // incoming 呼入 / outgoing 呼出
	Remote     string `json:"remote"`     // 对方号码或 URI
	RemoteName string `json:"remoteName"` // 对方显示名（From 的 display-name）
	// PeerName INVITE 的 Call-Info 里 display-name 参数（服务端下发的对端名称）：
	// **优先于通信录名称显示**（REQ-ACC-029）；为空时回退通信录/RemoteName。
	PeerName string `json:"peerName,omitempty"`
	// Consult Call-Info 的 call-type=consult：服务端推来的咨询呼入——接通后
	// 通话面板第一行不显示咨询/转移/会议入口（REQ-ACC-029）。
	Consult bool      `json:"consult,omitempty"`
	State   CallState `json:"state"`
	Muted   bool      `json:"muted"`
	// AutoAnswer 立即自动接听（本地开关 / answer-after=0）：界面据此不播振铃音。
	AutoAnswer bool `json:"autoAnswer,omitempty"`
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
	// CTI 复杂话务状态（咨询/三方/监听/会议，REQ-ACC-026）：双方通话时为零值
	// （TalkState 为 twoParty）。由服务端 MSP_EVENT_* 驱动，UI 据此重建面板。
	CTI CTIState `json:"cti,omitempty"`

	// startedAt 通话创建时间：call.panel 事件的 startTime 用（内部字段，不下发）。
	startedAt time.Time
}

// RegisterInfo 注册状态快照。
type RegisterInfo struct {
	State   RegisterState `json:"state"`
	Account string        `json:"account"`
	Reason  string        `json:"reason"` // 失败原因
	// Registrars 各注册目标（主账户 + 备用服务器）的注册状态（REQ-ACC-038）：
	// 界面/网页据此悬浮显示「哪些服务器注册上了」。单服务器时只有一条。
	Registrars []RegistrarStatus `json:"registrars,omitempty"`
}

// RegistrarStatus 单个注册目标的注册状态（REQ-ACC-038）。
type RegistrarStatus struct {
	Server string `json:"server"` // 展示用服务器地址（host，或 host:port 当端口非默认）
	Domain string `json:"domain,omitempty"`
	OK     bool   `json:"ok"`               // 是否已注册
	Reason string `json:"reason,omitempty"` // 失败原因
}

// State 应用状态快照（供 WS 查询接口返回）。
type State struct {
	Version   string        `json:"version"`
	OS        string        `json:"os"`                  // 编译目标平台（如 windows），前端据此查询对应产物
	Arch      string        `json:"arch"`                // 编译目标架构（如 amd64）
	BuildTime string        `json:"buildTime,omitempty"` // 构建时间（ISO 8601，SDK 动态加载指纹用，未注入时为空）
	Register  RegisterInfo  `json:"register"`
	Calls     []Call        `json:"calls"`
	Update    *UpdateStatus `json:"update,omitempty"` // 更新状态（无更新时省略）
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
	// AgentMode 坐席模式（none/normal/dial_out/monitor，协议 §16.1）。
	// **none 表示不在坐席模式（座席不在线）**。详细的坐席状态（displayName/
	// isBusy/isWork/isLeave）统一走 `cti.agent` 事件，这里不再重复（用户要求去重）。
	AgentMode string `json:"agentMode,omitempty"`
}

// 常见错误，供接口层映射为错误码。
var (
	ErrTelephonyNotReady = errors.New("电话能力尚未接入")
	ErrCallNotFound      = errors.New("通话不存在")
	ErrNoActiveCall      = errors.New("没有进行中的通话")
	ErrCTIBusy           = errors.New("已有 CTI 指令在途，请稍候")
	// ErrCTINotifyUnsupported 服务端不支持 CTI 的 NOTIFY 通道：收到
	// MSP_UNSUPPORTED，或 MSP_HEARTBEAT 的 SIP 响应不是 200。话机据此停止
	// 心跳并退出坐席模式（协议要求）。
	ErrCTINotifyUnsupported = errors.New("服务端不支持 CTI NOTIFY")
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
	// lastCallStatus 上一次上报的心跳 status：话务状态变化时据此**立即补发**
	// 一条心跳（否则最多要等一个周期才同步给 CTI，见 notifyCallStatus）。
	lastCallStatus string
	// pendingVideo 早到的视频状态（通话尚未登记，如视频呼叫拨出即开摄像头）
	pendingVideo map[string]VideoState
	// videoTx 显示用的旋转/镜像（桌面与网页共用一份，见 video.go）
	videoTx VideoTransforms
	// autoAnswer 自动接听开关（桌面按钮与网页开关共用一份，
	// 变更后广播 system.autoAnswer，见 SetAutoAnswer）
	autoAnswer bool

	// agent 坐席模式（MSP_EVENT_AGENT_ONLINE/OFFLINE，见 cti.go）：
	// 只有坐席模式才在通话面板显示高级话务按钮。
	agent agentState
	// 服务端下发的面板（协议 §16.2/§16.3，2026-10 修订）：CTI 统一下发计算好的
	// 完整通话/会议面板（MSP_CALL_PANEL_UPDATED / MSP_CONFERENCE_PANEL_UPDATED），
	// 话机直接整份覆盖。收到过面板事件后（srvPanelSeen）就以它为准，不再用本地
	// 事件推算面板（本地推算仅作为老服务端的兜底）。
	srvPanelSeen  bool
	srvCallPanel  *CallPanel
	srvConfPanel  *ConferencePanel
	lastPanelFail string // 已经提示过的 failCause（callId|failCause，去重）
	// lastPanelJSON 上一次 call.panel 快照的 JSON 串：相同快照只推一次（去重）。
	lastPanelJSON string
	// heartbeat CTI 心跳循环（协议 §10）：注册成功启动、注销停止，
	// 报文里如实上报坐席模式与显示名（见 cti.go）。
	heartbeat heartbeatState

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
func (a *App) SetTel(tel Telephony) {
	a.tel = tel
	// 呼出 INVITE 的 Call-Info 要带被叫的显示名（REQ-ACC-029）：通信录在
	// core 里、sip 层拿不到，这里把查名回调注入过去（sip.Phone 实现该方法；
	// 其它实现没有就跳过，不影响功能）。
	if s, ok := tel.(interface{ SetPeerNameLookup(func(string) string) }); ok {
		s.SetPeerNameLookup(a.PeerDisplayName)
	}
}

// PeerDisplayName 号码对应的显示名（通信录里的名称；查不到返回空串）。
// 供电话层拼呼出 INVITE 的 Call-Info 用。
func (a *App) PeerDisplayName(number string) string {
	if c, ok := a.LookupContact(number); ok {
		return c.Name
	}
	return ""
}

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
	// 账户配置变化统一从这里广播（URI 拉起 / 网页 PatchAccount / 托盘登录注销 /
	// 桌面保存都走 UpdateAccount）：网页配置页与桌面配置页据此同步表单。
	a.bus.Publish("system.config", map[string]any{
		"section": "account", "values": a.ConfigSnapshot().Account,
	})
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
		// 登出账号（REQ-ACC-028 补充）：坐席模式一并退出——坐席会话绑定在
		// 登录态上，退出账号后不应再显示"座席已登录"（托盘菜单/网页徽章/
		// SDK unregister/服务端 MSP_UNREGISTER/断线自动注销都走这里）。
		a.setAgentOnline(false, "", AgentModeNone, false, false, false)
		a.setRegister(RegUnregistered, "", nil)
		a.bus.Publish("register.state", map[string]any{
			"state":   string(RegUnregistered),
			"account": acc.Account,
			"reason":  "",
		})
		return a.tel.Unregister(context.Background())
	}
	go func() {
		if err := a.tel.Register(context.Background(), acc); err != nil {
			a.setRegister(RegFailed, err.Error(), nil)
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
				a.setRegister(RegFailed, err.Error(), nil)
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
		BuildTime:    BuildTime,
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
		AgentMode:         a.AgentMode(),
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
	return a.dial(ctx, target, "", false)
}

// DialNamed 带对端显示名的呼出：displayName 来自座席列表等界面已知的名称，
// 呼叫期间**优先于通信录名称**显示（为空时行为与 Dial 完全一致）。
func (a *App) DialNamed(ctx context.Context, target, displayName string) (string, error) {
	return a.dial(ctx, target, displayName, false)
}

// dial 呼出/视频呼出的公共流程；displayName 非空时写进 Call.PeerName。
func (a *App) dial(ctx context.Context, target, displayName string, video bool) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("号码不能为空")
	}
	if a.tel == nil {
		return "", ErrTelephonyNotReady
	}
	if a.HasActiveCall() {
		return "", errors.New("已有通话进行中，无法发起新的呼叫")
	}
	var (
		callID string
		err    error
	)
	if video {
		callID, err = a.tel.DialVideo(ctx, target)
	} else {
		callID, err = a.tel.Dial(ctx, target)
	}
	if err != nil {
		return "", err
	}
	a.upsertCall(&Call{
		ID:        callID,
		Direction: "outgoing",
		Remote:    target,
		PeerName:  strings.TrimSpace(displayName),
		State:     CallRinging,
		HasVideo:  video,
	})
	// 通话记录（REQ-CALL-007）：必须在 addRecentDial（会同步写 config.json）
	// 与 publishCall 之前入队——前者是与结束事件抢时间的最大窗口，后者是
	// 界面收到事件后立刻查库刷新能看到这条记录的前提。
	a.logStart(CallRecord{
		CallID:      callID,
		Direction:   "outgoing",
		Remote:      target,
		DisplayName: strings.TrimSpace(displayName),
		StartedAt:   time.Now(),
		HasVideo:    video,
	})
	a.addRecentDial(target)
	// 发布"呼出中"事件：GUI 进入呼叫状态（通话面板），WS 客户端
	//（如测试页）收到后可显示挂断按钮中止呼叫
	a.publishCall("call.dialing", callID)
	a.notifyCallStatus() // 心跳 status：dialing
	logx.Logger().Info("发起呼叫", "callId", callID, "target", target, "video", video,
		"displayName", displayName)
	// 视频呼叫拨出即开摄像头/回显（sip 侧），其首个状态可能早于本次登记到达，
	// 已被 upsertCall 并入通话——这里补发一次事件，界面才会立刻显示本地画面
	//（顺序必须在 call.dialing 之后：界面要先有 activeCallID 才认这条事件）。
	if video {
		if c := a.getCall(callID); c != nil && (c.Video.Enabled || c.Video.Sending || c.Video.Negotiated) {
			a.publishCall("call.video", callID)
		}
	}
	return callID, nil
}

// AddRecentDial 把号码记进"最近呼叫"（主窗体拨号下拉的数据源，持久化）。
// 普通拨号在 Dial/DialVideo 里自动记；CTI 拨号复用（咨询/转移/加会）没有走
// Dial，由界面在指令发出后调这里补记。
func (a *App) AddRecentDial(number string) {
	a.addRecentDial(number)
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

// HangupOrReject 统一挂断（网页 call.hangup，REQ-SDK 简化）：单通话限制下
// 按该通话状态自动选择——呼入振铃中 → 拒接（486）；呼出振铃中 → 取消（CANCEL）；
// 已接通 → 挂断（BYE）。与 Reject/Hangup 相比，调用方无需判断来电/呼出/通话中。
func (a *App) HangupOrReject(callID string) error {
	c := a.getCall(callID)
	if c == nil {
		return ErrCallNotFound
	}
	if c.Direction == "incoming" && c.State == CallRinging {
		return a.Reject(callID, 486)
	}
	return a.Hangup(callID)
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
	a.notifyCallStatus() // 心跳 status：holding / talking
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
	return a.dial(ctx, target, "", true)
}

// DialVideoNamed 带对端显示名的视频呼叫（见 DialNamed）。
func (a *App) DialVideoNamed(ctx context.Context, target, displayName string) (string, error) {
	return a.dial(ctx, target, displayName, true)
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
func (a *App) OnIncoming(callID string, info IncomingCall) {
	// From 的 display-name 可能只是网关的占位（如 FreeSWITCH 的
	// "Outbound Call"）：过滤掉，显示回退到号码/通信录名称。
	remoteName := CleanDisplayName(info.RemoteName)
	a.upsertCall(&Call{
		ID:         callID,
		Direction:  "incoming",
		Remote:     info.Remote,
		RemoteName: remoteName,
		PeerName:   info.PeerName,
		Consult:    info.Consult,
		State:      CallRinging,
		HasVideo:   info.HasVideo,
		AutoAnswer: info.AutoAnswer,
	})
	// 通话记录（REQ-CALL-007）：来电的起点即振铃开始时刻。
	// 主叫就是本机分机号的通话不记（平台监听/会议等场景会以本机号码回呼话机，
	// 记下来只会把通话记录刷满自己呼自己的条目）。
	if a.selfCaller(info.Remote) {
		logx.Logger().Info("主叫为本机的通话不记入通话记录",
			"callId", callID, "remote", info.Remote)
	} else {
		a.logStart(CallRecord{
			CallID:     callID,
			Direction:  "incoming",
			Remote:     info.Remote,
			RemoteName: remoteName,
			StartedAt:  time.Now(),
			HasVideo:   info.HasVideo,
		})
	}
	logx.Logger().Info("收到来电", "callId", callID, "remote", info.Remote,
		"peerName", info.PeerName, "consult", info.Consult, "hasVideo", info.HasVideo)
	a.publishCall("call.incoming", callID)
	a.notifyCallStatus() // 心跳 status：ringing
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
		a.notifyCallStatus() // 心跳 status：talking / holding
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
	// 服务端面板随通话结束清空（页面据此关闭面板）
	a.clearServerPanels()
	a.publishCall("call.terminated", callID, "reason", reason, "missed", missed)
	a.notifyCallStatus() // 心跳 status：idle
}

// OnFailed 呼叫失败。
//
// **CTI 接管的话务**（收到过 `CALL_RINGING`，`CTI.CallPhase` 为 ringing/talking）
// 在事件里带 `ctiManaged=true`：SIP 侧的原因（如「603 Decline」）不再由界面
// 弹提示——CTI 随后会下发 `CALL_FAILED`，由 `cti.failed` 给出统一的中文消息
// （「呼叫未接通」等）。否则用户会同时看到两条失败消息（用户实测）。
func (a *App) OnFailed(callID, reason string) {
	var missed, ctiManaged bool
	if !a.updateCall(callID, func(c *Call) {
		missed = missedOnEnd(c, reason)
		ctiManaged = c.CTI.CallPhase == "ringing" || c.CTI.CallPhase == "talking"
		c.State = CallTerminated
	}) {
		return
	}
	logx.Logger().Info("呼叫失败", "callId", callID, "reason", reason,
		"missed", missed, "ctiManaged", ctiManaged)
	a.logFinish(callID, reason, missed) // 通话记录：先写记录再发事件
	a.clearServerPanels()               // 服务端面板随通话结束清空
	a.publishCall("call.failed", callID, "reason", reason, "missed", missed,
		"ctiManaged", ctiManaged)
	a.notifyCallStatus() // 心跳 status：idle
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
	regs := a.registrarStates()
	a.setRegister(state, reason, regs)
	logx.Logger().Info("注册状态变化", "state", string(state), "reason", reason)
	a.bus.Publish("register.state", map[string]any{
		"state":      string(state),
		"account":    a.cfg.Account.Account,
		"reason":     reason,
		"registrars": regs,
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

// ActiveCallID 当前进行中通话的 ID（单通话限制：振铃或已接通）；无则空串。
// 供 WS 层在「不要求 callId」的接口里自动定位当前通话。
func (a *App) ActiveCallID() string {
	if c := a.activeCall(); c != nil {
		return c.ID
	}
	return ""
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

func (a *App) setRegister(state RegisterState, reason string, regs []RegistrarStatus) {
	a.mu.Lock()
	a.reg = RegisterInfo{State: state, Account: a.cfg.Account.Account, Reason: reason, Registrars: regs}
	a.mu.Unlock()

	// CTI 心跳（协议 §10）：注册成功就开始周期发，注销就停。
	switch state {
	case RegRegistered:
		a.startHeartbeat()
	case RegUnregistered:
		a.stopHeartbeat()
	}
}

// registrarStates 查询电话层各注册目标的注册状态（REQ-ACC-038）。电话层未实现
// 该能力（非 SIP 后端）时返回 nil，界面据此隐藏「多服务器」悬浮信息。
func (a *App) registrarStates() []RegistrarStatus {
	if a.tel == nil {
		return nil
	}
	if r, ok := a.tel.(interface{ RegistrarStates() []RegistrarStatus }); ok {
		return r.RegistrarStates()
	}
	return nil
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
		if c.startedAt.IsZero() {
			c.startedAt = time.Now()
		}
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
	// 通话面板快照随之更新（REQ-WS-023）：页面/SDK 基于 call.panel 构建面板，
	// 不必自己解析 call.* / cti.* 明细；面板关闭时两个 panel 为 null。
	a.publishCallPanel()
}
