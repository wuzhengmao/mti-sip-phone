package core

import (
	"context"
	"errors"
	"fmt"
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
	Register RegisterInfo  `json:"register"`
	Calls    []Call        `json:"calls"`
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
	bus     *Bus

	mu        sync.RWMutex
	reg       RegisterInfo
	calls     map[string]*Call
	callOrder []string // 保持创建顺序
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
	a.mu.RLock()
	defer a.mu.RUnlock()
	st := State{
		Version:  Version,
		Register: a.reg,
		Calls:    make([]Call, 0, len(a.callOrder)),
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
	if a.hasActiveCall() {
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

// ---------------------------------------------------------------------------
// 下行：电话层（Telephony 实现）通过 EventSink 回调
// ---------------------------------------------------------------------------

// OnIncoming 呼入。创建呼入通话并进入振铃状态。
func (a *App) OnIncoming(callID, remote, remoteName string) {
	a.upsertCall(&Call{
		ID:         callID,
		Direction:  "incoming",
		Remote:     remote,
		RemoteName: remoteName,
		State:      CallRinging,
	})
	logx.Logger().Info("收到来电", "callId", callID, "remote", remote)
	a.publishCall("call.incoming", callID)
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

// hasActiveCall 是否存在进行中的通话（振铃或已接通）。
func (a *App) hasActiveCall() bool {
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

// Version 应用版本号（构建时可通过 -ldflags 覆盖）。
var Version = "1.0.0"
