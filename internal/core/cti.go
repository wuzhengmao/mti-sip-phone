package core

import (
	"errors"
	"strings"
	"sync"
	"time"

	"mti-sip-phone/internal/logx"
)

// agentState 坐席模式状态（MSP_EVENT_AGENT_ONLINE/OFFLINE，应用级、与通话无关）。
type agentState struct {
	mu          sync.Mutex
	online      bool
	mode        string // none / normal / dial_out / monitor（协议 §16.1）
	busy        bool   // 置忙（isBusy）
	displayName string // 坐席显示名（AGENT_ONLINE 的 displayName，可能为空）
}

// 坐席模式取值（协议 §10 / §16.1）。
const (
	AgentModeNone    = "none"     // 未登录坐席
	AgentModeNormal  = "normal"   // 普通模式
	AgentModeDialOut = "dial_out" // 外拨模式
	AgentModeMonitor = "monitor"  // 班长模式
)

// heartbeatState 心跳循环的停止信号（注册成功时启动、注销时关闭）。
// 心跳**与坐席模式无关**：注册着就发，报文里的 agentMode 如实上报——CTI 靠
// 它判断"CTI 认为在线但话机已退出坐席"之类的状态不一致（协议 §10 第 3 点）。
type heartbeatState struct {
	mu   sync.Mutex
	stop chan struct{}
}

// 复杂话务状态（REQ-ACC-026，docs/cti-sip-phone-protocol.md 第三部分）：
// CTI 在咨询/三方/监听/会议等话务的各个节点向话机推 MSP_EVENT_*，话机据此
// 更新通话面板。这些状态叠加在"双方通话"之上，挂在 Call.CTI 上。
type TalkState string

const (
	TalkTwoParty   TalkState = "twoParty"   // 双方通话（默认）
	TalkConsult    TalkState = "consult"    // 咨询
	TalkThreeWay   TalkState = "threeWay"   // 三方
	TalkMonitor    TalkState = "monitor"    // 监听
	TalkConference TalkState = "conference" // 会议
)

// ConferenceMember 会议成员（MSP_EVENT_CONFERENCE_MEMBERS_CHANGED 的 members 项）。
type ConferenceMember struct {
	TelNo string `json:"telNo"`
	// DisplayName 成员显示名（协议 §16 名称字段约定：仅已登录坐席才有值）。
	DisplayName string `json:"displayName,omitempty"`
	State       string `json:"state"` // dialing/ringing/talking/holding/hangup，以及会控 muted/deafened/vmuted（+ 组合）
	SkillGroup  string `json:"skillGroup,omitempty"`
}

// CTIState 复杂话务的运行时详情，挂在 Call 上随 MSP_EVENT_* 更新。
// 双方通话时为零值（TalkState == TalkTwoParty）。
type CTIState struct {
	TalkState TalkState `json:"talkState"`

	// ConsultInProgress 有一通与本通相关的咨询正在进行、而**本机不是咨询发起方**
	// （CONSULT_STARTED 也发给原通话双方与被咨询方，协议 §16.2）：面板保持双方
	// 通话不变，只隐藏咨询/转移/会议入口，收到 CONSULT_FINISHED 时随 reset 恢复。
	ConsultInProgress bool `json:"consultInProgress,omitempty"`

	// 名称字段（协议 §16 约定）：每个号码字段 xxx 都伴随 xxxName，仅当该号码是
	// 已登录坐席时才有值。面板显示号码时**优先用它**，为空再回退通信录。

	// ---- 咨询 ----
	Consulter     string `json:"consulter,omitempty"`     // 咨询方（本机坐席）
	ConsulterName string `json:"consulterName,omitempty"` // 咨询方显示名
	Consultee     string `json:"consultee,omitempty"`     // 被咨询方
	ConsulteeName string `json:"consulteeName,omitempty"` // 被咨询方显示名
	ConsultPhase  string `json:"consultPhase,omitempty"`  // dialing / ringing / talking

	// ---- 三方 ----
	// 协议 §16.3：firstParty = 三方建立时**原通话**里发起方一侧（咨询方或被监听
	// 坐席）；secondParty = 原通话的另一方；thirdParty = 新加入的一方。
	Owner           string `json:"owner,omitempty"` // 发起三方通话的分机（咨询方或班长）
	OwnerName       string `json:"ownerName,omitempty"`
	FirstParty      string `json:"firstParty,omitempty"`
	FirstPartyName  string `json:"firstPartyName,omitempty"`
	SecondParty     string `json:"secondParty,omitempty"`
	SecondPartyName string `json:"secondPartyName,omitempty"`
	ThirdParty      string `json:"thirdParty,omitempty"`
	ThirdPartyName  string `json:"thirdPartyName,omitempty"`

	// ---- 监听 ----
	Monitor      string `json:"monitor,omitempty"`      // 班长（本机）
	MonitorName  string `json:"monitorName,omitempty"`  // 班长显示名
	Target       string `json:"target,omitempty"`       // 被监听的坐席
	TargetName   string `json:"targetName,omitempty"`   // 被监听坐席显示名
	MonitorPhase string `json:"monitorPhase,omitempty"` // listening / whisper
	// 被监听话务的主被叫（MONITOR_STARTED 携带，协议 §16.4；WHISPER/INTERCEPT
	// 不重复带，沿用 STARTED 的值）。面板据此显示"主叫 → 被叫"。
	CallerNo   string `json:"callerNo,omitempty"`
	CallerName string `json:"callerName,omitempty"`
	CalledNo   string `json:"calledNo,omitempty"`
	CalledName string `json:"calledName,omitempty"`

	// ---- 会议 ----
	ConfID  string             `json:"confId,omitempty"`
	Members []ConferenceMember `json:"members,omitempty"`
}

// NameOf 某个号码在本话务里的显示名（MSP_EVENT_* 的 xxxName 字段，仅已登录
// 坐席才有)；查不到返回空串（调用方回退通信录/号码本身）。
func (st *CTIState) NameOf(no string) string {
	if no == "" {
		return ""
	}
	switch no {
	case st.FirstParty:
		return st.FirstPartyName
	case st.SecondParty:
		return st.SecondPartyName
	case st.ThirdParty:
		return st.ThirdPartyName
	case st.Owner:
		return st.OwnerName
	case st.Consulter:
		return st.ConsulterName
	case st.Consultee:
		return st.ConsulteeName
	case st.Monitor:
		return st.MonitorName
	case st.CallerNo:
		return st.CallerName
	case st.CalledNo:
		return st.CalledName
	case st.Target:
		return st.TargetName
	}
	return ""
}

// SendCTICommand 发送一条 CTI 控制指令（REQ-ACC-026），转发给电话层。
// 返回指令序号 seq，结果经 OnCTIResult 异步回传。
func (a *App) SendCTICommand(action string, params map[string]any) (string, error) {
	if a.tel == nil {
		return "", ErrTelephonyNotReady
	}
	return a.tel.SendCTICommand(action, params)
}

// OnCTIResult CTI 指令结果（MSP_RESULT，REQ-ACC-026）：发布 cti.result 事件，
// 由发起方（UI）按 seq 对齐。
func (a *App) OnCTIResult(res CTIResult) {
	logx.Logger().Info("CTI 指令结果", "seq", res.Seq, "success", res.Success,
		"code", res.Code, "message", res.Message)
	payload := map[string]any{
		"seq":     res.Seq,
		"success": res.Success,
		"code":    res.Code,
		"message": res.Message,
		"action":  res.Action,
	}
	if res.Data != nil {
		payload["data"] = res.Data
	}
	a.bus.Publish("cti.result", payload)
}

// OnMSPEvent 通话面板事件（MSP_EVENT_*，REQ-ACC-026）：把事件翻译成当前通话的
// CTI 状态变更，并发布 cti.state 事件（负载带完整 Call，UI 据此重建面板）。
//
// 坐席在线事件（MSP_EVENT_AGENT_ONLINE / AGENT_OFFLINE）是**应用级**状态
// （与是否有通话无关），先在这里处理掉。
func (a *App) OnMSPEvent(event string, body map[string]any) {
	switch event {
	case "agent_online":
		// 新协议（§16.1）：ONLINE 携带 displayName / agentMode / isBusy，
		// **不再过期**——坐席状态的保持靠话机自己的 MSP_HEARTBEAT，直到
		// OFFLINE 或本地登出。`agentMode=none` 表示"话机不该处于坐席模式"
		// （心跳不一致时 CTI 用它来纠正），按退出坐席处理。
		name := strOf(body, "displayName")
		mode := normalizeAgentMode(strOf(body, "agentMode"))
		busy, _ := body["isBusy"].(bool)
		if mode == AgentModeNone {
			a.setAgentOnline(false, "", AgentModeNone, false)
			return
		}
		a.setAgentOnline(true, name, mode, busy)
		return
	case "agent_offline":
		a.setAgentOnline(false, "", AgentModeNone, false)
		return
	case "unsupported":
		// MSP_UNSUPPORTED（协议 1.0.18 新增）：服务端明确表示不支持 NOTIFY 通道。
		// 与"MSP_HEARTBEAT 的 SIP 响应不是 200"同样处理——停止心跳、退出坐席模式。
		reason := strOf(body, "reason")
		if reason == "" {
			reason = "服务端下发 MSP_UNSUPPORTED"
		} else {
			reason = "MSP_UNSUPPORTED（" + reason + "）"
		}
		a.handleCTIUnsupported(reason)
		return
	}

	c := a.activeCall()
	if c == nil {
		logx.Logger().Warn("收到 MSP_EVENT 但当前无活动通话，忽略", "event", event)
		return
	}
	// 咨询转接完成（协议 §16.2）：事件同时发给转移方与被咨询方，动作不同——
	// **转移方**收到就挂机结束话务（转移完成后它退出通话，由服务端把原通话
	// 接到被咨询方），**被咨询方**则接管原通话（见 applyConsultTransfer）。
	a.mu.RLock()
	self := a.cfg.Account.Account
	a.mu.RUnlock()
	if event == "consult_transfer" && isSelfNumber(strOf(body, "transferor"), self) {
		logx.Logger().Info("咨询转移完成，转移方挂断本通话", "callId", c.ID, "self", self)
		go func(id string) {
			if err := a.Hangup(id); err != nil {
				logx.Logger().Debug("转移完成后挂断通话失败（可能已被服务端挂断）", "err", err)
			}
		}(c.ID)
		return
	}
	a.updateCall(c.ID, func(cc *Call) {
		switch event {
		case "consult_transfer":
			applyConsultTransfer(cc, self, body)
		case "three_way_finished":
			applyThreeWayFinished(cc, self, body)
		default:
			applyMSPEvent(&cc.CTI, event, body, self)
		}
	})
	logx.Logger().Info("应用 MSP_EVENT", "event", event, "callId", c.ID)
	a.publishCall("cti.state", c.ID)

	// 会议结束（MSP_EVENT_CONFERENCE_FINISHED）**等同于收到 BYE**：会议没了，
	// 这通电话就结束了——直接挂断本端，通话面板随 call.terminated 关闭
	//（用户明确要求：两者收到哪个都关面板）。放 goroutine：挂断要等信令。
	if event == "conference_finished" {
		go func(id string) {
			if err := a.Hangup(id); err != nil {
				logx.Logger().Debug("结束会议后挂断通话失败（可能已被服务端挂断）", "err", err)
			}
		}(c.ID)
	}
}

// ---- 坐席模式（MSP_EVENT_AGENT_ONLINE / AGENT_OFFLINE）----
//
// 新协议（§16.1）：ONLINE/OFFLINE 只在**坐席登录、登出、状态不一致**时推一次，
// 不再周期续期；坐席状态的保持改由话机周期性发送 MSP_HEARTBEAT（§10）维持。
// 所以 ONLINE **不过期**，直到收到 OFFLINE 或本地登出（UpdateAccount 注销分支）。
// **只有坐席模式才在通话面板显示高级话务按钮**（咨询/转移/会议）——普通 SIP
// 话机没有 CTI 后端，点了也没人处理。

const (
	// defaultHeartbeatInterval 心跳间隔的默认值（秒，协议建议 20s）。
	defaultHeartbeatInterval = 20 * time.Second
	// minHeartbeatInterval 心跳间隔下限，避免配错把 CTI 打爆。
	minHeartbeatInterval = 5 * time.Second
	// heartbeatExpiresFactor 心跳有效期系数：expires = 间隔 × 2.5（用户要求）。
	heartbeatExpiresFactor = 2.5
)

// AgentOnline 当前是否处于坐席模式（加锁读，供界面初始化快照）。
func (a *App) AgentOnline() bool {
	a.agent.mu.Lock()
	defer a.agent.mu.Unlock()
	return a.agent.online
}

// AgentDisplayName 坐席显示名（AGENT_ONLINE 的 displayName；离线/为空时返回 ""）。
func (a *App) AgentDisplayName() string {
	a.agent.mu.Lock()
	defer a.agent.mu.Unlock()
	if !a.agent.online {
		return ""
	}
	return a.agent.displayName
}

// AgentMode 坐席模式（none/normal/dial_out/monitor）；未登录坐席时返回 none。
func (a *App) AgentMode() string {
	a.agent.mu.Lock()
	defer a.agent.mu.Unlock()
	if !a.agent.online {
		return AgentModeNone
	}
	return a.agent.mode
}

// AgentBusy 坐席是否置忙（isBusy）；未登录坐席时恒 false。
func (a *App) AgentBusy() bool {
	a.agent.mu.Lock()
	defer a.agent.mu.Unlock()
	return a.agent.online && a.agent.busy
}

// setAgentOnline 更新坐席模式（心跳由注册状态驱动，见 startHeartbeat）。
// displayName / mode / busy 只在 online=true 时有意义；状态有变化才发布 cti.agent。
func (a *App) setAgentOnline(on bool, displayName, mode string, busy bool) {
	if mode == "" {
		mode = AgentModeNormal
	}
	if !on {
		mode, busy = AgentModeNone, false
	}
	a.agent.mu.Lock()
	changed := a.agent.online != on || a.agent.displayName != displayName ||
		a.agent.mode != mode || a.agent.busy != busy
	a.agent.online = on
	a.agent.displayName = displayName
	a.agent.mode = mode
	a.agent.busy = on && busy
	a.agent.mu.Unlock()

	if changed {
		logx.Logger().Info("坐席模式变化", "online", on, "mode", mode,
			"busy", on && busy, "displayName", displayName)
		a.bus.Publish("cti.agent", map[string]any{
			"online":      on,
			"displayName": displayName,
			"agentMode":   mode,
			"isBusy":      on && busy,
		})
	}
}

// normalizeAgentMode 协议给的 agentMode 归一化：缺省/未知按普通模式处理
// （老 CTI 不带该字段时按普通坐席处理，不能因此退出坐席模式）。
func normalizeAgentMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case AgentModeNone:
		return AgentModeNone
	case AgentModeNormal:
		return AgentModeNormal
	case AgentModeDialOut:
		return AgentModeDialOut
	case AgentModeMonitor:
		return AgentModeMonitor
	}
	return AgentModeNormal
}

// handleCTIUnsupported 服务端不支持 CTI 的 NOTIFY 通道（收到 MSP_UNSUPPORTED，
// 或 MSP_HEARTBEAT 的 SIP 响应不是 200）：按协议**停止发送心跳并退出坐席模式**。
// 心跳停到下次重新注册为止；坐席状态由 CTI 的下一次 ONLINE 重新拉起。
func (a *App) handleCTIUnsupported(reason string) {
	logx.Logger().Warn("服务端不支持 CTI NOTIFY，停止心跳并退出坐席模式", "reason", reason)
	a.stopHeartbeat()
	a.setAgentOnline(false, "", AgentModeNone, false)
}

// startHeartbeat 注册成功后启动心跳循环（幂等）；注销时由 stopHeartbeat 停止。
func (a *App) startHeartbeat() {
	if a.tel == nil {
		return
	}
	a.heartbeat.mu.Lock()
	defer a.heartbeat.mu.Unlock()
	if a.heartbeat.stop != nil {
		return
	}
	stop := make(chan struct{})
	a.heartbeat.stop = stop
	go a.heartbeatLoop(stop)
}

// stopHeartbeat 停止心跳循环（注销后不再发）。
func (a *App) stopHeartbeat() {
	a.heartbeat.mu.Lock()
	defer a.heartbeat.mu.Unlock()
	if a.heartbeat.stop != nil {
		close(a.heartbeat.stop)
		a.heartbeat.stop = nil
	}
}

// heartbeatInterval 当前配置的心跳间隔（`account.ctiHeartbeat`，默认 20s）。
func (a *App) heartbeatInterval() time.Duration {
	a.mu.RLock()
	sec := a.cfg.Account.CTIHeartbeat
	a.mu.RUnlock()
	d := time.Duration(sec) * time.Second
	if d <= 0 {
		d = defaultHeartbeatInterval
	}
	if d < minHeartbeatInterval {
		d = minHeartbeatInterval
	}
	return d
}

// heartbeatLoop 注册期间周期发送心跳（首包立即发一次）。每轮重新读配置，
// 改了心跳间隔下一轮就生效。
func (a *App) heartbeatLoop(stop <-chan struct{}) {
	for {
		a.sendHeartbeatOnce()
		select {
		case <-stop:
			return
		case <-time.After(a.heartbeatInterval()):
		}
	}
}

// sendHeartbeatOnce 发一次 MSP_HEARTBEAT（expires = 间隔 × 2.5）。body 按协议
// §10 上报 agentMode（字符串）与 isBusy；未登录坐席时 agentMode=none。
func (a *App) sendHeartbeatOnce() {
	if a.tel == nil {
		return
	}
	interval := a.heartbeatInterval()
	expires := time.Duration(float64(interval) * heartbeatExpiresFactor)
	mode := a.AgentMode()
	busy := a.AgentBusy()
	status := a.callStatus()
	a.mu.Lock()
	a.lastCallStatus = status
	a.mu.Unlock()
	if err := a.tel.SendHeartbeat(mode, busy, a.AgentDisplayName(), status, expires); err != nil {
		// 非 200 响应 = 服务端不支持 NOTIFY（协议 1.0.18）：与 MSP_UNSUPPORTED
		// 同样处理，停止心跳并退出坐席模式。
		if errors.Is(err, ErrCTINotifyUnsupported) {
			a.handleCTIUnsupported("MSP_HEARTBEAT 返回非 200")
			return
		}
		logx.Logger().Debug("发送 CTI 心跳失败", "err", err)
		return
	}
	logx.Logger().Debug("已发送 CTI 心跳", "expires", expires.String(),
		"agentMode", mode, "isBusy", busy, "status", status)
}

// strOf 读 MSP_EVENT body 里的字符串字段（缺失/类型不符返回空串）。
func strOf(body map[string]any, k string) string {
	if v, ok := body[k].(string); ok {
		return v
	}
	return ""
}

// isSelfNumber 号码是不是本机分机号（规范化后比较；任一为空恒 false）。
func isSelfNumber(no, self string) bool {
	return sameNumber(no, self)
}

// sameNumber 两个号码规范化后是否相同（空值恒 false）。
func sameNumber(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return NormalizeNumber(a) == NormalizeNumber(b)
}

// callStatus 当前话务状态（MSP_HEARTBEAT 的 status 字段，**协议约定的字符串**）：
//
//	free    空闲（没有进行中的通话）
//	dialing 呼出：呼叫中 / 回铃振铃中（未接通）
//	ringing 呼入振铃中（来电面板）
//	talking 通话中
//	holding 保持中（与通话面板的保持按钮状态同步）
func (a *App) callStatus() string {
	c := a.activeCall()
	if c == nil {
		return "free"
	}
	switch c.State {
	case CallEstablished:
		if c.Muted {
			return "holding"
		}
		return "talking"
	case CallRinging:
		if c.Direction == "outgoing" {
			return "dialing"
		}
		return "ringing"
	}
	return "free"
}

// notifyCallStatus 话务状态变化时**立即补发一条心跳**：否则 status 最多要等一个
// 心跳周期（默认 20 秒）才同步给 CTI。只在状态真的变了才发（幂等）。
func (a *App) notifyCallStatus() {
	if a.tel == nil {
		return
	}
	a.heartbeat.mu.Lock()
	running := a.heartbeat.stop != nil
	a.heartbeat.mu.Unlock()
	if !running {
		return // 未注册/心跳没在跑：等下一次心跳自然带出去
	}
	st := a.callStatus()
	a.mu.Lock()
	changed := a.lastCallStatus != st
	a.lastCallStatus = st
	a.mu.Unlock()
	if !changed {
		return
	}
	logx.Logger().Debug("话务状态变化，立即上报心跳", "status", st)
	go a.sendHeartbeatOnce()
}

// applyConsultTransfer 咨询转接完成（MSP_EVENT_CONSULT_TRANSFER，协议 §16.2）：
// 本机（被咨询方）接管原通话——面板回到双方通话，**对端换成原通话的另一方**
// （callerNo / calledNo 里不是本机的那一个，名称取对应的 xxxName），并清掉
// 「咨询腿」标记：转接完成后这通就是普通两方通话，咨询/转移/会议入口恢复显示。
func applyConsultTransfer(c *Call, self string, body map[string]any) {
	c.CTI = CTIState{TalkState: TalkTwoParty}
	c.Consult = false
	caller, callerName := strOf(body, "callerNo"), strOf(body, "callerName")
	called, calledName := strOf(body, "calledNo"), strOf(body, "calledName")
	peer, peerName := caller, callerName
	if peer == "" || isSelfNumber(caller, self) {
		peer, peerName = called, calledName
	}
	if peer == "" {
		return // 两个号码都没带：保持原对端不动
	}
	c.Remote = peer
	c.RemoteName = peerName
	c.PeerName = peerName // 协议给的名称优先于通信录（与 Call-Info 同口径）
}

// applyThreeWayFinished 三方结束（协议 §16.3）：回到双方通话。
//
// body 给的是**结束后的剩余两方**（callerNo / calledNo；agent 为该话务的接听
// 坐席，可能省略）——STARTED 里的三方中**不在这个集合里的那一方即被移除的一方**。
// 若本机当前对端正是被移除的那一方（例如发起方退出三方、另外两方继续通话），
// 把对端收敛成剩余的另一方；否则对端不变。
func applyThreeWayFinished(c *Call, self string, body map[string]any) {
	caller, called := strOf(body, "callerNo"), strOf(body, "calledNo")
	agent := strOf(body, "agent")
	// 三方面板里两行的显示名：复位后 CTI 字段就没了，先在"保留的一方"上取好
	nameOf := func(no string) string {
		if no == "" {
			return ""
		}
		return c.CTI.NameOf(no)
	}
	// 按协议 §16.3 的 caller/called/agent 判定**剩余的对端**（用户口径）：
	//   - 有 agent 且 agent = 本机 → 对端是 caller；
	//   - caller = 本机 → 对端是 called；
	//   - called = 本机 → 对端是 caller。
	peer := ""
	switch {
	case agent != "" && sameNumber(agent, self):
		peer = caller
	case sameNumber(caller, self):
		peer = called
	case sameNumber(called, self):
		peer = caller
	}
	if peer == "" {
		// 兼容老事件（不带 agent、本机也不在 caller/called 里）：取剩余两方中
		// 不是本机的那一方。
		switch {
		case caller != "" && !isSelfNumber(caller, self):
			peer = caller
		case called != "" && !isSelfNumber(called, self):
			peer = called
		}
	}
	peerName := nameOf(peer)

	applyMSPEvent(&c.CTI, "three_way_finished", body, self) // 复位成双方通话
	// 三方结束后这通就是普通两方通话，不再是"咨询腿"：坐席模式下咨询/转移/
	// 会议入口要恢复显示（用户要求）。
	c.Consult = false

	if peer == "" || sameNumber(peer, c.Remote) {
		return // 对端没变（或事件里没有可用的一方）：号码与名称保持原样
	}
	c.Remote = peer
	c.RemoteName = peerName // FINISHED 不带名称：优先沿用三方面板里这一方的名称
	c.PeerName = peerName
}

// applyMSPEvent 按事件名更新 CTI 状态（纯函数，便于单测）。
// event 为去掉 MSP_EVENT_ 前缀、转小写的事件名（如 "consult_started"）。
// self 是本机分机号：咨询事件据此区分"咨询发起方"与"相关方"（协议 §16.2）。
// 未识别的事件名忽略（协议要求向前兼容，只回 200 不报错）。
func applyMSPEvent(st *CTIState, event string, body map[string]any, self string) {
	str := func(k string) string {
		if v, ok := body[k].(string); ok {
			return v
		}
		return ""
	}
	reset := func() { *st = CTIState{TalkState: TalkTwoParty} }

	switch event {
	case "consult_started", "consult_ringing", "consult_connected":
		// 协议 §16.2：CONSULT_STARTED 现在发给**原通话双方 + 被咨询方**三方，
		// 而 RINGING/CONNECTED 仍只发给咨询方。非咨询方（本机不是 consulter）
		// 的面板保持双方通话，只把咨询/转移/会议入口藏起来（避免咨询期间误操作），
		// CONSULT_FINISHED 的 reset 恢复。
		if event == "consult_started" && !isSelfNumber(str("consulter"), self) {
			st.TalkState = TalkTwoParty
			st.ConsultInProgress = true
			return
		}
		st.TalkState = TalkConsult
		st.Consulter, st.Consultee = str("consulter"), str("consultee")
		st.ConsulterName, st.ConsulteeName = str("consulterName"), str("consulteeName")
		switch event {
		case "consult_started":
			st.ConsultPhase = "dialing"
		case "consult_ringing":
			st.ConsultPhase = "ringing"
		case "consult_connected":
			st.ConsultPhase = "talking"
		}

	case "consult_finished", "consult_failed":
		reset()

	case "three_way_started":
		st.TalkState = TalkThreeWay
		st.Owner, st.OwnerName = str("owner"), str("ownerName")
		st.FirstParty, st.FirstPartyName = str("firstParty"), str("firstPartyName")
		st.SecondParty, st.SecondPartyName = str("secondParty"), str("secondPartyName")
		st.ThirdParty, st.ThirdPartyName = str("thirdParty"), str("thirdPartyName")
		// 进入三方就不再是咨询：清掉咨询阶段的字段，避免旧值残留
		//（面板按 TalkState 渲染，但状态快照要干净）
		st.Consulter, st.Consultee, st.ConsultPhase = "", "", ""
		st.ConsulterName, st.ConsulteeName = "", ""

	case "three_way_finished":
		// 纯状态复位；对端号码的收敛在 OnMSPEvent 里做（见 applyThreeWayFinished，
		// 需要 Call 上的对端字段与本机分机号）。
		reset()

	case "monitor_started":
		st.TalkState = TalkMonitor
		st.Monitor, st.Target = str("monitor"), str("target")
		st.MonitorName, st.TargetName = str("monitorName"), str("targetName")
		st.MonitorPhase = "listening"
		// 被监听话务的主被叫（协议 §16.4）：面板按这两方显示"主叫 → 被叫"
		st.CallerNo, st.CallerName = str("callerNo"), str("callerName")
		st.CalledNo, st.CalledName = str("calledNo"), str("calledName")

	case "monitor_whisper":
		st.TalkState = TalkMonitor
		st.MonitorPhase = "whisper"

	case "monitor_intercept":
		reset()

	case "conference_started":
		st.TalkState = TalkConference
		st.ConfID, st.Owner = str("confId"), str("owner")
		st.OwnerName = str("ownerName")
		st.Members = nil // 新会议：成员列表等 MEMBER(S)_CHANGED 补齐，不复用旧会议的

	case "conference_member_changed", "conference_members_changed":
		// 协议 §16.5 写的是 CONFERENCE_MEMBERS_CHANGED，实际下发偶见
		// CONFERENCE_MEMBER_CHANGED（单数），两种都收。
		// **整份覆盖本地成员列表**：多出来的条目消失、缺少的补齐、状态重设，
		// 面板据此整块重画（不做增量）。
		st.TalkState = TalkConference
		st.ConfID, st.Owner = str("confId"), str("owner")
		st.OwnerName = str("ownerName")
		if raw, ok := body["members"].([]any); ok {
			members := make([]ConferenceMember, 0, len(raw))
			for _, m := range raw {
				mm, ok := m.(map[string]any)
				if !ok {
					continue
				}
				cm := ConferenceMember{}
				if v, ok := mm["telNo"].(string); ok {
					cm.TelNo = v
				}
				if v, ok := mm["displayName"].(string); ok {
					cm.DisplayName = v
				}
				if v, ok := mm["state"].(string); ok {
					cm.State = v
				}
				if v, ok := mm["skillGroup"].(string); ok {
					cm.SkillGroup = v
				}
				members = append(members, cm)
			}
			st.Members = members
		}

	case "conference_finished":
		reset()

	default:
		// 未识别：忽略
	}
}
