package core

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/logx"
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
	// HasVideo 本次通话涉及视频（呼入时对端 offer 含视频段；呼出时本端发起了视频呼叫）。
	HasVideo bool `json:"hasVideo"`
	// Video 视频状态快照（未协商成功时为零值）。
	Video VideoState `json:"video"`
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
}

// 常见错误，供接口层映射为错误码。
var (
	ErrTelephonyNotReady = errors.New("电话能力尚未接入")
	ErrCallNotFound      = errors.New("通话不存在")
	ErrNoActiveCall      = errors.New("没有进行中的通话")
)

// App 应用核心：维护注册与通话状态机，向上层发布事件。
type App struct {
	cfg     *config.Config
	cfgPath string
	tel     Telephony
	upd     Updater // 自动更新能力（nil = 未接入）
	bus     *Bus

	mu        sync.RWMutex
	reg       RegisterInfo
	calls     map[string]*Call
	callOrder []string   // 保持创建顺序
	videoSink VideoSink  // 视频帧出口（界面层注册；nil = 无人接收，直接丢帧）
}

// New 创建 App。tel 为 nil 时表示电话能力未接入（调用会返回 ErrTelephonyNotReady）。
func New(cfg *config.Config, tel Telephony, bus *Bus) *App {
	return &App{
		cfg:   cfg,
		tel:   tel,
		bus:   bus,
		reg:   RegisterInfo{State: RegUnregistered},
		calls: make(map[string]*Call),
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
		Version:  Version,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Register: a.reg,
		Calls:    make([]Call, 0, len(a.callOrder)),
		Update:   upd,
	}
	for _, id := range a.callOrder {
		st.Calls = append(st.Calls, *a.calls[id])
	}
	return st
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
func (a *App) SetAutoAnswer(enabled bool) error {
	if a.tel == nil {
		return ErrTelephonyNotReady
	}
	return a.tel.SetAutoAnswer(enabled)
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
	return nil
}

// MuteSpeaker 扬声器静音（纯本地，继续接收；通话面板扬声器开关用）。
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
	return nil
}

// ---------------------------------------------------------------------------
// 视频（REQ-VIDEO-*）
// ---------------------------------------------------------------------------

// ErrVideoUnsupported 当前构建不支持视频。
var ErrVideoUnsupported = errors.New("当前构建未启用视频支持")

// SetVideoSink 注册视频帧出口（界面层启动时调用一次）。
func (a *App) SetVideoSink(s VideoSink) {
	a.mu.Lock()
	a.videoSink = s
	a.mu.Unlock()
}

// VideoFrameSink 电话层用来投递视频帧的出口（未注册时返回 nil）。
// sip 层在建立/接听时取一次并接到视频引擎上。
func (a *App) VideoFrameSink() VideoSink {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.videoSink
}

// VideoSupported 当前构建是否具备视频能力。
func (a *App) VideoSupported() bool {
	return a.tel != nil && a.tel.VideoSupported()
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
	a.addRecentDial(target)
	a.publishCall("call.dialing", callID)
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
	logx.Logger().Info("收到来电", "callId", callID, "remote", remote, "hasVideo", hasVideo)
	a.publishCall("call.incoming", callID)
}

// OnVideoState 视频状态变化（协商成功、开关切换、拿到对端分辨率）。
func (a *App) OnVideoState(callID string, st VideoState) {
	if a.updateCall(callID, func(c *Call) {
		c.Video = st
		c.HasVideo = c.HasVideo || st.Negotiated
	}) {
		a.publishCall("call.video", callID)
	}
}

// OnRinging 呼出振铃。
func (a *App) OnRinging(callID string) {
	if a.updateCall(callID, func(c *Call) { c.State = CallRinging }) {
		a.publishCall("call.ringing", callID)
	}
}

// OnEstablished 通话建立。
func (a *App) OnEstablished(callID string) {
	if a.updateCall(callID, func(c *Call) { c.State = CallEstablished }) {
		logx.Logger().Info("通话建立", "callId", callID)
		a.publishCall("call.established", callID)
	}
}

// OnTerminated 通话结束。
func (a *App) OnTerminated(callID, reason string) {
	if a.updateCall(callID, func(c *Call) { c.State = CallTerminated }) {
		logx.Logger().Info("通话结束", "callId", callID, "reason", reason)
		a.publishCall("call.terminated", callID, reason)
	}
}

// OnFailed 呼叫失败。
func (a *App) OnFailed(callID, reason string) {
	if a.updateCall(callID, func(c *Call) { c.State = CallTerminated }) {
		logx.Logger().Info("呼叫失败", "callId", callID, "reason", reason)
		a.publishCall("call.failed", callID, reason)
	}
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
func (a *App) upsertCall(c *Call) {
	a.mu.Lock()
	if _, ok := a.calls[c.ID]; !ok {
		a.callOrder = append(a.callOrder, c.ID)
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
		data["call"] = *c
	}
	// 附加字段：如 terminated 事件的 reason
	for i := 0; i+1 < len(extra); i += 2 {
		if k, ok := extra[i].(string); ok {
			data[k] = extra[i+1]
		}
	}
	a.bus.Publish(event, data)
}
