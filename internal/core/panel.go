package core

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"mti-sip-phone/internal/logx"
)

// 通话面板快照（REQ-WS-023 / REQ-SDK-010）：给页面/SDK 直接据此构建自己的
// 通话面板，不必解析 call.* 与 cti.* 的明细事件。核心每次话务/坐席状态变化都
// 发布一次 `call.panel` 事件；没有进行中的通话时 callPanel / conferencePanel
// 为空（null）。
//
// 与桌面通话面板的口径一致：
//   - title 是面板顶部显示的状态文案（拨号中/振铃中/…）；
//   - callList 是通话各方（通常只有对端；监听时 peer=主叫、target=被叫）；
//   - 会议时成员在 conferencePanel 里，callPanel.callList 为空。

// PanelParty 面板里的一个参与方：号码 + 显示名 + 通信录备注（字段与
// SipContactJSON 一致：number / name / note）。
type PanelParty struct {
	Number string `json:"number"`
	Name   string `json:"name,omitempty"`
	Note   string `json:"note,omitempty"` // 通信录备注
}

// CallPanelLeg 通话面板里的一方（子话务）。
//
// **字段与 CTI 协议 §16.2 的 callList 项保持一致**（不重命名、不丢弃）：SDK
// 原样交给应用，应用可能要用到话机自己没用的属性（callerNo/calledNo/intercom/
// ringingTime/offhookTime…）。唯一的改写是 peer：协议里是号码字符串（名称另给
// peerName），这里按用户口径合并成 {number,name,note} 对象。
type CallPanelLeg struct {
	CallerNo    string     `json:"callerNo"`
	CalledNo    string     `json:"calledNo"`
	Peer        PanelParty `json:"peer"`
	Intercom    bool       `json:"intercom"`
	SkillGroup  string     `json:"skillGroup"`
	CallType    string     `json:"callType"`
	CallStatus  string     `json:"callStatus"` // incoming/dialing/ringing/talking/holding
	StartTime   int64      `json:"startTime"`
	RingingTime int64      `json:"ringingTime"`
	OffhookTime int64      `json:"offhookTime"`
	// Target 本端补充字段（非协议）：仅监听话务使用，peer=主叫、target=被叫。
	Target *PanelParty `json:"target,omitempty"`
}

// CallPanel 通话面板（两方/咨询/三方/监听；会议时仍带本通话的信息）。
type CallPanel struct {
	CallID string `json:"callId"`
	// Agent 坐席号（协议原样）。
	Agent string `json:"agent"`
	// CallType / CallMode 服务端面板的原始口径（incoming/outgoing/transfer/
	// monitor 与 normal/consult/three-way/monitor），应用可据此细化按钮。
	CallType string `json:"callType"`
	CallMode string `json:"callMode"`
	// Owner 当前操作的发起方分机（协议原样；callMode=normal 时为空）：咨询是
	// 咨询方，三方是发起方，监听是班长。应用据此区分发起方与其他参与方——
	// 例如咨询接通后只有 owner 显示三方/转移，三方的移除按钮只有 owner 显示。
	Owner string `json:"owner,omitempty"`
	// State 面板状态（应用据此决定显示哪些功能按钮）：
	// dialing / ringing / incoming / talking / consult-dialing / consult-ringing /
	// consult-talking / three-way / monitoring / whispering。
	State     string `json:"state"`
	StartTime int64  `json:"startTime"`
	// FailCause 失败原因（仅失败时）：cancel/refuse/takeover/fail。
	FailCause string `json:"failCause,omitempty"`
	// Title 本端补充：面板状态文案（拨号中/振铃中/来电/通话中/咨询呼叫中/
	// 咨询振铃中/咨询通话中/三方通话中/监听中/监听耳语中/会议中/通话保持中）。
	Title    string         `json:"title"`
	CallList []CallPanelLeg `json:"callList"`
}

// ConferenceMemberPanel 会议成员（字段与协议 §16.3 的 memberList 项一致）。
// member 与 callList 的 peer 同样按用户口径合并成 {number,name,note} 对象
// （协议里是 member 号码 + memberName 名称）。
type ConferenceMemberPanel struct {
	Member   PanelParty `json:"member"`
	Intercom bool       `json:"intercom"`
	Owner    bool       `json:"owner"`
	// State 成员状态（透传 CTI：dialing / ringing / talking / holding / hangup，
	// 以及会控后缀 muted / deafened / vmuted，可用 "+" 组合）。
	State     string `json:"state"`
	Timestamp int64  `json:"timestamp"` // 成员加入时间（毫秒时间戳）
}

// ConferencePanel 会议面板（字段与协议 §16.3 一致）。
type ConferencePanel struct {
	ConfID string `json:"confId"`
	// Owner 主持人号码。
	Owner      string                  `json:"owner"`
	MemberList []ConferenceMemberPanel `json:"memberList"`
}

// CallPanelState call.panel 事件的负载（REQ-WS-023）。
// 只含通话/会议面板快照；坐席状态（agentMode/isBusy/isWork/isLeave）统一走
// cti.agent 事件，不在这里重复（用户要求去重）。
type CallPanelState struct {
	CallPanel       *CallPanel       `json:"callPanel"` // 无通话时为 null
	ConferencePanel *ConferencePanel `json:"conferencePanel"`
}

// stampPanelTimes 记录各话务腿的起始时间（call.panel 的 startTime 用）。
// 与 applyMSPEvent 一样在 updateCall 的临界区里调用。
func stampPanelTimes(c *Call, event string) {
	now := time.Now()
	switch event {
	case "consult_started":
		if c.CTI.consultAt.IsZero() {
			c.CTI.consultAt = now
		}
	case "three_way_started":
		if c.CTI.threeWayAt.IsZero() {
			c.CTI.threeWayAt = now
		}
	case "monitor_started":
		if c.CTI.monitorAt.IsZero() {
			c.CTI.monitorAt = now
		}
	case "conference_started":
		if c.CTI.conferenceAt.IsZero() {
			c.CTI.conferenceAt = now
		}
		c.CTI.memberAt = map[string]time.Time{}
	case "conference_member_changed", "conference_members_changed":
		if c.CTI.conferenceAt.IsZero() {
			c.CTI.conferenceAt = now
		}
		if c.CTI.memberAt == nil {
			c.CTI.memberAt = map[string]time.Time{}
		}
		for _, m := range c.CTI.Members {
			if _, ok := c.CTI.memberAt[m.TelNo]; !ok {
				c.CTI.memberAt[m.TelNo] = now
			}
		}
	}
}

// publishCallPanel 发布通话面板快照（每次话务/坐席状态变化后调用）。
// 无通话时 callPanel / conferencePanel 为 null——页面据此关闭面板。
// **相同快照只推一次**：挂断时「坐席进入整理态」与「通话结束」两条路径可能先后
// 触发，快照逐字节相同时跳过第二次（用户要求，避免网页/SDK 收到两条一样的）。
func (a *App) publishCallPanel() {
	st := a.PanelState()
	b, err := json.Marshal(st)
	if err != nil {
		a.bus.Publish("call.panel", st)
		return
	}
	key := string(b)
	a.mu.Lock()
	same := a.lastPanelJSON == key
	a.lastPanelJSON = key
	a.mu.Unlock()
	if same {
		return
	}
	a.bus.Publish("call.panel", st)
}

// PanelState 当前通话面板快照（wsapi 连接建立时也主动推一份）。
//
// **服务端面板优先**（协议 2026-10 修订）：收到过 MSP_CALL_PANEL_UPDATED /
// MSP_CONFERENCE_PANEL_UPDATED 之后直接返回服务端下发的整份面板；只有从没收到过
// 面板事件的老服务端才回退到本地按话务状态推算（下面的兜底逻辑）。
func (a *App) PanelState() *CallPanelState {
	a.mu.RLock()
	self := a.cfg.Account.Account
	seen, srvCall, srvConf := a.srvPanelSeen, a.srvCallPanel, a.srvConfPanel
	a.mu.RUnlock()

	out := &CallPanelState{}
	if seen {
		out.CallPanel = srvCall
		out.ConferencePanel = srvConf
		return out
	}

	c := a.activeCall()
	if c == nil {
		return out
	}
	start := c.startedAt
	if start.IsZero() {
		start = time.Now()
	}
	// 会议面板只在这一通已经**接通**后出现：被叫 B 收到会议邀请的振铃阶段
	// （CTI 可能已经下发了 CONFERENCE_STARTED/MEMBERS_CHANGED）仍然只有普通
	// callPanel（来电），接通后才切换成 conferencePanel。两个面板不会同时存在。
	if c.CTI.TalkState == TalkConference && c.State == CallEstablished {
		out.ConferencePanel = a.conferencePanel(c, self, start)
		return out
	}

	cp := &CallPanel{CallID: c.ID, StartTime: start.UnixMilli()}
	cp.State, cp.Title = panelStateTitle(c)

	switch c.CTI.TalkState {
	case TalkMonitor:
		// 监听：本机就是发起方（班长）
		cp.Owner = self
		cp.CallList = []CallPanelLeg{a.monitorLeg(c)}
	case TalkThreeWay:
		cp.Owner = c.CTI.Owner
		if cp.Owner == "" {
			cp.Owner = self
		}
		cp.CallList = a.threeWayLegs(c, self, start)
	case TalkConsult:
		// 本地兜底口径里进入咨询面板的只有咨询发起方
		cp.Owner = self
		cp.CallList = a.consultLegs(c, self, start)
	default:
		// 两方通话；也包括"会议邀请尚未接通"（CONTEXT: TalkConference 但未接通）
		leg := CallPanelLeg{
			Peer:       a.party(c.Remote, a.peerDisplayName(c)),
			CallStatus: twoPartyLegState(c),
			StartTime:  start.UnixMilli(),
		}
		if c.Direction == "incoming" {
			leg.CallerNo, leg.CalledNo = c.Remote, self
		} else {
			leg.CallerNo, leg.CalledNo = self, c.Remote
		}
		cp.CallList = []CallPanelLeg{leg}
	}
	// 技能组挂在腿上（协议的 callList[].skillGroup）：本地兜底时取 CTI 的值
	if len(cp.CallList) > 0 && cp.CallList[0].SkillGroup == "" {
		cp.CallList[0].SkillGroup = c.CTI.SkillGroup
	}
	out.CallPanel = cp
	return out
}

// panelStateTitle 面板状态与文案（口径与桌面通话面板一致）。
func panelStateTitle(c *Call) (state, title string) {
	st := c.CTI
	if st.TalkState == TalkConference {
		if c.State == CallEstablished {
			return "talking", "会议中"
		}
		// 会议邀请还没接通（被叫 B 振铃中）：按普通来电/振铃显示
		st = CTIState{}
	}
	switch st.TalkState {
	case TalkConsult:
		switch st.ConsultPhase {
		case "ringing":
			return "consult-ringing", "咨询振铃中"
		case "talking":
			return "consult-talking", "咨询通话中"
		}
		return "consult-dialing", "咨询呼叫中"
	case TalkThreeWay:
		return "three-way", "三方通话中"
	case TalkMonitor:
		if st.MonitorPhase == "whisper" {
			return "whispering", "监听耳语中"
		}
		return "monitoring", "监听中"
	}
	if c.State == CallEstablished {
		if c.Muted {
			return "talking", "通话保持中"
		}
		return "talking", "通话中"
	}
	if c.Direction == "incoming" {
		return "incoming", "来电"
	}
	if st.CallPhase == "ringing" {
		return "ringing", "振铃中"
	}
	if st.CallPhase == "talking" {
		return "talking", "通话中"
	}
	return "dialing", "拨号中"
}

// twoPartyLegState 两方通话这一方的子状态。
func twoPartyLegState(c *Call) string {
	if c.State == CallEstablished {
		if c.Muted {
			return "holding"
		}
		return "talking"
	}
	if c.Direction == "incoming" {
		return "incoming"
	}
	if c.CTI.CallPhase == "ringing" {
		return "ringing"
	}
	return "dialing"
}

// consultLegs 咨询面板的两方：原通话对端 + 被咨询方。
func (a *App) consultLegs(c *Call, self string, start time.Time) []CallPanelLeg {
	st := c.CTI
	legs := []CallPanelLeg{{
		Peer:       a.party(c.Remote, a.peerDisplayName(c)),
		CallStatus: holdOrTalking(c),
		StartTime:  start.UnixMilli(),
	}}
	if c.Direction == "incoming" {
		legs[0].CallerNo, legs[0].CalledNo = c.Remote, self
	} else {
		legs[0].CallerNo, legs[0].CalledNo = self, c.Remote
	}
	if st.Consultee != "" {
		at := st.consultAt
		if at.IsZero() {
			at = time.Now()
		}
		legs = append(legs, CallPanelLeg{
			CallerNo:   self,
			CalledNo:   st.Consultee,
			Peer:       a.party(st.Consultee, st.ConsulteeName),
			CallType:   "consult",
			CallStatus: legStateFromPhase(st.ConsultPhase),
			StartTime:  at.UnixMilli(),
		})
	}
	return legs
}

// threeWayLegs 三方通话：除本机外的两方（owner 与两方都用 talking）。
func (a *App) threeWayLegs(c *Call, self string, start time.Time) []CallPanelLeg {
	st := c.CTI
	parties := []struct{ no, name string }{
		{st.FirstParty, st.FirstPartyName},
		{st.SecondParty, st.SecondPartyName},
		{st.ThirdParty, st.ThirdPartyName},
	}
	legs := make([]CallPanelLeg, 0, 2)
	seen := map[string]bool{}
	for _, p := range parties {
		if p.no == "" || isSelfNumber(p.no, self) || seen[NormalizeNumber(p.no)] {
			continue
		}
		seen[NormalizeNumber(p.no)] = true
		at := start
		if p.no == st.ThirdParty && !st.threeWayAt.IsZero() {
			at = st.threeWayAt
		}
		legs = append(legs, CallPanelLeg{
			Peer:       a.party(p.no, p.name),
			CallStatus: "talking",
			StartTime:  at.UnixMilli(),
		})
	}
	return legs
}

// monitorLeg 监听：peer=被监听话务的主叫，target=被叫。
func (a *App) monitorLeg(c *Call) CallPanelLeg {
	st := c.CTI
	peerNo, peerName := st.CallerNo, st.CallerName
	if peerNo == "" {
		peerNo, peerName = c.Remote, a.peerDisplayName(c)
	}
	targetNo, targetName := st.CalledNo, st.CalledName
	if targetNo == "" && st.Target != "" && !sameNumber(st.Target, peerNo) {
		targetNo, targetName = st.Target, st.TargetName
	}
	at := st.monitorAt
	if at.IsZero() {
		at = c.startedAt
	}
	if at.IsZero() {
		at = time.Now()
	}
	leg := CallPanelLeg{
		CallerNo:   peerNo,
		CalledNo:   targetNo,
		Peer:       a.party(peerNo, peerName),
		CallStatus: "talking",
		StartTime:  at.UnixMilli(),
	}
	if targetNo != "" {
		tp := a.party(targetNo, targetName)
		leg.Target = &tp
	}
	return leg
}

// conferencePanel 会议面板：主持人第一行，其余保持服务端顺序。
func (a *App) conferencePanel(c *Call, self string, start time.Time) *ConferencePanel {
	st := c.CTI
	hostNo := st.Owner
	if hostNo == "" {
		hostNo = self
	}
	hostName := st.OwnerName
	if hostName == "" {
		// 主持人没带名称时用成员列表里的 displayName（成员事件里通常有）
		for _, m := range st.Members {
			if isSelfNumber(m.TelNo, hostNo) {
				hostName = m.DisplayName
				break
			}
		}
	}
	if hostName == "" && isSelfNumber(hostNo, self) {
		hostName = a.AgentDisplayName()
	}
	at := st.conferenceAt
	if at.IsZero() {
		at = start
	}
	panel := &ConferencePanel{
		ConfID:     st.ConfID,
		Owner:      hostNo,
		MemberList: make([]ConferenceMemberPanel, 0, len(st.Members)),
	}
	members := append([]ConferenceMember(nil), st.Members...)
	// 主持人永远第一行（与桌面面板一致）
	for i, m := range members {
		if isSelfNumber(m.TelNo, hostNo) {
			copy(members[1:i+1], members[0:i])
			members[0] = m
			break
		}
	}
	for _, m := range members {
		since := st.memberAt[m.TelNo]
		if since.IsZero() {
			since = at
		}
		isHost := isSelfNumber(m.TelNo, hostNo)
		name := m.DisplayName
		if name == "" && isHost {
			name = hostName
		}
		p := a.party(m.TelNo, name)
		panel.MemberList = append(panel.MemberList, ConferenceMemberPanel{
			Member:    p,
			Owner:     isHost,
			State:     m.State,
			Timestamp: since.UnixMilli(),
		})
	}
	return panel
}

// party 构造面板参与方：协议/SIP 已经给了名称就直接用（**不再附带通信录备注**，
// 用户口径）；只有名称为空、回退到通信录时才同时带出通信录的名称与备注。
func (a *App) party(no, name string) PanelParty {
	if name != "" {
		return PanelParty{Number: no, Name: name}
	}
	p := PanelParty{Number: no}
	if info, ok := a.LookupContact(no); ok {
		p.Name = info.Name
		p.Note = info.Note
	}
	return p
}

// peerDisplayName 对端的显示名（Call-Info 名称 > SIP 显示名）。
func (a *App) peerDisplayName(c *Call) string {
	if c.PeerName != "" {
		return c.PeerName
	}
	return c.RemoteName
}

func holdOrTalking(c *Call) string {
	if c.Muted {
		return "holding"
	}
	return "talking"
}

func legStateFromPhase(phase string) string {
	switch phase {
	case "ringing":
		return "ringing"
	case "talking":
		return "talking"
	}
	return "dialing"
}

// ---------------------------------------------------------------------------
// 服务端下发的面板（协议 §16.2/§16.3，2026-10 修订）
//
// CTI 不再下发细粒度的 MSP_EVENT_CALL_*/CONSULT_*/MONITOR_*/THREE_WAY_* 事件，
// 改为把计算好的完整面板推给话机：
//   MSP_CALL_PANEL_UPDATED         → applyServerCallPanel
//   MSP_CONFERENCE_PANEL_UPDATED   → applyServerConferencePanel
// 话机整份覆盖本地面板（srvCallPanel / srvConfPanel），同时把这些信息翻译进
// Call.CTI，供桌面通话面板直接复用既有的面板布局（咨询/三方/监听/会议）。
// ---------------------------------------------------------------------------

// srvCallPanelBody MSP_CALL_PANEL_UPDATED 的 body。
type srvCallPanelBody struct {
	CallID    string       `json:"callId"`
	Agent     string       `json:"agent"`
	CallType  string       `json:"callType"`
	CallMode  string       `json:"callMode"`
	Owner     string       `json:"owner"`
	State     string       `json:"state"`
	StartTime int64        `json:"startTime"`
	FailCause string       `json:"failCause"`
	CallList  []srvCallLeg `json:"callList"`
}

type srvCallLeg struct {
	CallerNo    string `json:"callerNo"`
	CalledNo    string `json:"calledNo"`
	Peer        string `json:"peer"`
	PeerName    string `json:"peerName"`
	Intercom    bool   `json:"intercom"`
	SkillGroup  string `json:"skillGroup"`
	CallType    string `json:"callType"`
	CallStatus  string `json:"callStatus"`
	StartTime   int64  `json:"startTime"`
	RingingTime int64  `json:"ringingTime"`
	OffhookTime int64  `json:"offhookTime"`
}

// srvConfPanelBody MSP_CONFERENCE_PANEL_UPDATED 的 body。
type srvConfPanelBody struct {
	ConfID     string          `json:"confId"`
	Owner      string          `json:"owner"`
	MemberList []srvConfMember `json:"memberList"`
}

type srvConfMember struct {
	Member     string `json:"member"`
	MemberName string `json:"memberName"`
	Intercom   bool   `json:"intercom"`
	Owner      bool   `json:"owner"`
	State      string `json:"state"`
	Timestamp  int64  `json:"timestamp"`
}

// decodePanelBody map → 结构体（sip 层用 map[string]any 透传 body）。
func decodePanelBody(body map[string]any, out any) {
	raw, err := json.Marshal(body)
	if err != nil {
		return
	}
	_ = json.Unmarshal(raw, out)
}

// serverPanelsActive 是否已经在用服务端下发的面板（收到过面板事件）。
func (a *App) serverPanelsActive() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.srvPanelSeen
}

// clearServerPanels 通话结束时清掉服务端面板（下一条 call.panel 随之为 null）。
func (a *App) clearServerPanels() {
	a.mu.Lock()
	a.srvCallPanel, a.srvConfPanel, a.srvPanelSeen = nil, nil, false
	a.mu.Unlock()
}

// applyServerCallPanel 处理 MSP_CALL_PANEL_UPDATED：整份覆盖通话面板。
func (a *App) applyServerCallPanel(body map[string]any) {
	var b srvCallPanelBody
	decodePanelBody(body, &b)
	a.mu.RLock()
	self := a.cfg.Account.Account
	a.mu.RUnlock()

	// callId 为空 = 面板空闲：清掉面板与话务状态（通话本身由 SIP 层结束）
	if strings.TrimSpace(b.CallID) == "" {
		a.mu.Lock()
		a.srvPanelSeen = true
		a.srvCallPanel, a.srvConfPanel = nil, nil
		a.mu.Unlock()
		if c := a.activeCall(); c != nil {
			a.updateCall(c.ID, func(cc *Call) { cc.CTI = CTIState{} })
			a.publishCall("cti.state", c.ID) // 末尾会推 call.panel
		} else {
			a.publishCallPanel()
		}
		return
	}

	cp := a.serverCallPanel(&b, self)
	a.mu.Lock()
	a.srvPanelSeen = true
	a.srvCallPanel = cp
	a.mu.Unlock()

	if c := a.activeCall(); c != nil {
		a.updateCall(c.ID, func(cc *Call) { applyServerCallState(cc, self, &b) })
		logx.Logger().Info("应用服务端通话面板", "callId", b.CallID, "mode", b.CallMode,
			"state", b.State, "legs", len(b.CallList))
		a.publishCall("cti.state", c.ID) // 驱动界面重建 + 推 call.panel
	} else {
		a.publishCallPanel()
	}
	if b.FailCause != "" {
		a.noticePanelFailure(b.CallID, b.FailCause)
	}
}

// applyServerConferencePanel 处理 MSP_CONFERENCE_PANEL_UPDATED：整份覆盖会议面板。
func (a *App) applyServerConferencePanel(body map[string]any) {
	var b srvConfPanelBody
	decodePanelBody(body, &b)
	a.mu.RLock()
	self := a.cfg.Account.Account
	a.mu.RUnlock()

	// memberList 为空 = 会议结束/本机被移出：退出会议界面并挂断本端
	if len(b.MemberList) == 0 {
		a.mu.Lock()
		a.srvPanelSeen = true
		a.srvCallPanel, a.srvConfPanel = nil, nil
		a.mu.Unlock()
		if c := a.activeCall(); c != nil {
			a.updateCall(c.ID, func(cc *Call) { cc.CTI = CTIState{} })
			a.publishCall("cti.state", c.ID)
			go func(id string) {
				if err := a.Hangup(id); err != nil {
					logx.Logger().Debug("会议结束后挂断通话失败（可能已被服务端挂断）", "err", err)
				}
			}(c.ID)
		} else {
			a.publishCallPanel()
		}
		return
	}

	conf := a.serverConferencePanel(&b)
	a.mu.Lock()
	a.srvPanelSeen = true
	a.srvConfPanel = conf
	a.srvCallPanel = nil // 会议与通话面板互斥（用户要求）
	a.mu.Unlock()

	if c := a.activeCall(); c != nil {
		a.updateCall(c.ID, func(cc *Call) { applyServerConferenceState(cc, self, &b) })
		logx.Logger().Info("应用服务端会议面板", "confId", b.ConfID,
			"owner", b.Owner, "members", len(b.MemberList))
		a.publishCall("cti.state", c.ID)
	} else {
		a.publishCallPanel()
	}
}

// noticePanelFailure 面板带 failCause 时提示失败原因（同 callId+cause 只提示一次）。
func (a *App) noticePanelFailure(callID, cause string) {
	key := callID + "|" + cause
	a.mu.Lock()
	if a.lastPanelFail == key {
		a.mu.Unlock()
		return
	}
	a.lastPanelFail = key
	a.mu.Unlock()
	msg := callFailedText(cause)
	if msg == "" {
		return
	}
	a.bus.Publish("cti.failed", map[string]any{
		"callId": callID, "kind": "call", "reason": cause, "message": msg,
	})
}

// panelStateFromServer 服务端 state → 面板 state/title（口径与桌面一致）。
func panelStateFromServer(s string) (state, title string) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dialing":
		return "dialing", "拨号中"
	case "ringing":
		return "ringing", "振铃中"
	case "incoming":
		return "incoming", "来电"
	case "talking":
		return "talking", "通话中"
	case "holding":
		return "talking", "通话保持中"
	case "consult-dialing":
		return "consult-dialing", "咨询呼叫中"
	case "consult-ringing":
		return "consult-ringing", "咨询振铃中"
	case "consult-talking":
		return "consult-talking", "咨询通话中"
	case "three-way":
		return "three-way", "三方通话中"
	case "monitor", "listening":
		return "monitoring", "监听中"
	case "whisper", "whispering":
		return "whispering", "监听耳语中"
	}
	return "talking", "通话中"
}

// serverCallPanel 服务端通话面板 → 对外 CallPanel（SDK/网页用）。
func (a *App) serverCallPanel(b *srvCallPanelBody, self string) *CallPanel {
	state, title := panelStateFromServer(b.State)
	// callMode=consult 时，owner 是咨询发起方（服务端按 §16.2 指定）。owner 缺失
	// 的老服务端回退旧口径：callList 里有 callType=consult 的腿才算发起方。
	// 非发起方（被咨询方）的面板按普通通话显示，不能出现咨询行/咨询按钮。
	consulting := consultInitiator(b, self)
	if strings.EqualFold(b.CallMode, "consult") && !consulting {
		if strings.HasPrefix(state, "consult-") {
			state, title = "talking", "通话中"
		}
	}
	cp := &CallPanel{
		CallID:    b.CallID,
		Agent:     b.Agent,
		CallType:  b.CallType,
		CallMode:  b.CallMode,
		Owner:     b.Owner,
		State:     state,
		StartTime: b.StartTime,
		FailCause: b.FailCause,
		Title:     title,
	}
	for i := range b.CallList {
		leg := &b.CallList[i]
		no := legPeer(leg, self)
		if no == "" {
			continue
		}
		pl := CallPanelLeg{
			CallerNo:    leg.CallerNo,
			CalledNo:    leg.CalledNo,
			Peer:        a.party(no, leg.PeerName),
			Intercom:    leg.Intercom,
			SkillGroup:  leg.SkillGroup,
			CallType:    leg.CallType,
			CallStatus:  leg.CallStatus,
			StartTime:   leg.StartTime,
			RingingTime: leg.RingingTime,
			OffhookTime: leg.OffhookTime,
		}
		if pl.CallStatus == "" {
			pl.CallStatus = "talking"
		}
		// 监听：peer=主叫、target=被叫（取同一条腿的 caller/called 另一侧）
		if strings.EqualFold(b.CallMode, "monitor") {
			other := leg.CalledNo
			if sameNumber(no, leg.CalledNo) {
				other = leg.CallerNo
			}
			if other != "" && !sameNumber(other, no) {
				tp := a.party(other, "")
				pl.Target = &tp
			}
		}
		cp.CallList = append(cp.CallList, pl)
	}
	return cp
}

// legPeer 腿的对端号码：优先协议给的 peer，缺失时取 caller/called 里不是本机的一侧。
func legPeer(leg *srvCallLeg, self string) string {
	if no := leg.Peer; no != "" {
		return no
	}
	no := leg.CallerNo
	if isSelfNumber(no, self) {
		no = leg.CalledNo
	}
	return no
}

// consultInitiator 本机是否是咨询发起方：
//   - 协议 §16.2 明确用 owner 指定发起方（咨询方），owner 是谁就谁显示咨询控制；
//   - owner 缺失（老服务端）才回退旧口径——callList 里有 callType=consult 的腿。
func consultInitiator(b *srvCallPanelBody, self string) bool {
	if !strings.EqualFold(strings.TrimSpace(b.CallMode), "consult") {
		return false
	}
	if owner := strings.TrimSpace(b.Owner); owner != "" {
		return sameNumber(owner, self)
	}
	return hasConsultLeg(b)
}

// hasConsultLeg callMode=consult 的面板里是否有「咨询腿」（callType=consult）：
// 有 = 本机是咨询发起方；没有 = 本机是被咨询方（仅 owner 缺失时的兜底口径）。
func hasConsultLeg(b *srvCallPanelBody) bool {
	for _, leg := range b.CallList {
		if strings.EqualFold(strings.TrimSpace(leg.CallType), "consult") {
			return true
		}
	}
	return false
}

// serverConferencePanel 服务端会议面板 → 对外 ConferencePanel。
// 字段与协议 §16.3 一致（confId / owner / memberList），只把成员号码的名称
// 与通信录备注补进 memberName / note。
func (a *App) serverConferencePanel(b *srvConfPanelBody) *ConferencePanel {
	conf := &ConferencePanel{ConfID: b.ConfID, Owner: b.Owner}
	for _, m := range b.MemberList {
		if conf.Owner == "" && m.Owner {
			conf.Owner = m.Member
		}
		p := a.party(m.Member, m.MemberName)
		if p.Name == "" && m.Owner {
			// 主持人没带名称：本机时用坐席显示名
			p.Name = a.AgentDisplayName()
		}
		conf.MemberList = append(conf.MemberList, ConferenceMemberPanel{
			Member:    p,
			Intercom:  m.Intercom,
			Owner:     m.Owner,
			State:     m.State,
			Timestamp: m.Timestamp,
		})
	}
	// 主持人第一行（与桌面面板一致）
	sort.SliceStable(conf.MemberList, func(i, j int) bool {
		return isSelfNumber(conf.MemberList[i].Member.Number, conf.Owner) &&
			!isSelfNumber(conf.MemberList[j].Member.Number, conf.Owner)
	})
	return conf
}

// applyServerCallState 把服务端面板翻译进 Call.CTI，供桌面通话面板沿用既有布局。
func applyServerCallState(c *Call, self string, b *srvCallPanelBody) {
	c.CTI = CTIState{}
	// 服务端面板是权威口径：这通电话是否"咨询腿"由面板的 callMode 决定，
	// 不再沿用 INVITE 的 Call-Info call-type=consult 本地标记——否则咨询转移后
	// 面板回到 normal 时，桌面面板仍会因为 Call.Consult 把咨询/转移/会议入口藏掉
	//（真机：2999 咨询转移后网页正常、桌面按钮不显示）。
	c.Consult = false
	// 对端显示名：第一条腿的 peerName（与本地 SIP 对端一致时采用）
	if len(b.CallList) > 0 {
		leg := b.CallList[0]
		if leg.PeerName != "" && (leg.Peer == "" || c.Remote == "" || sameNumber(leg.Peer, c.Remote)) {
			c.PeerName = leg.PeerName
		}
		if leg.SkillGroup != "" {
			c.CTI.SkillGroup = leg.SkillGroup
		}
	}
	switch strings.ToLower(strings.TrimSpace(b.CallMode)) {
	case "consult":
		if !consultInitiator(b, self) {
			// 本机不是发起方（被咨询方）：保持普通两方通话，只是隐藏咨询/
			// 转移/会议入口（协议用 owner 指定发起方，owner 不是本机即非发起方）。
			c.CTI.TalkState = TalkTwoParty
			c.CTI.ConsultInProgress = true
			return
		}
		c.CTI.TalkState = TalkConsult
		c.CTI.Consulter = b.Owner
		if c.CTI.Consulter == "" {
			c.CTI.Consulter = self
		}
		c.CTI.ConsultPhase = strings.TrimPrefix(strings.ToLower(b.State), "consult-")
		// 被咨询方：callType=consult 的腿（没有就取最后一条腿）
		var consultee *srvCallLeg
		for i := range b.CallList {
			if strings.EqualFold(b.CallList[i].CallType, "consult") {
				consultee = &b.CallList[i]
				break
			}
		}
		if consultee == nil && len(b.CallList) > 1 {
			consultee = &b.CallList[len(b.CallList)-1]
		}
		if consultee != nil {
			c.CTI.Consultee = legPeer(consultee, self)
			c.CTI.ConsulteeName = consultee.PeerName
		}
	case "three-way":
		c.CTI.TalkState = TalkThreeWay
		// owner = 三方发起方（协议 §16.2）：只有 owner 能看到强制挂断（X），
		// 其他人只看到号码。
		c.CTI.Owner = b.Owner
		if c.CTI.Owner == "" {
			c.CTI.Owner = self
		}
		idx := 0
		for i := range b.CallList {
			leg := &b.CallList[i]
			no := legPeer(leg, self)
			if no == "" || isSelfNumber(no, self) {
				continue
			}
			switch idx {
			case 0:
				c.CTI.FirstParty, c.CTI.FirstPartyName = no, leg.PeerName
			case 1:
				c.CTI.SecondParty, c.CTI.SecondPartyName = no, leg.PeerName
			case 2:
				c.CTI.ThirdParty, c.CTI.ThirdPartyName = no, leg.PeerName
			}
			idx++
		}
	case "monitor":
		c.CTI.TalkState = TalkMonitor
		c.CTI.Monitor = b.Owner
		if c.CTI.Monitor == "" {
			c.CTI.Monitor = self
		}
		if strings.EqualFold(b.State, "whisper") || strings.EqualFold(b.State, "whispering") {
			c.CTI.MonitorPhase = "whisper"
		} else {
			c.CTI.MonitorPhase = "listening"
		}
		if len(b.CallList) > 0 {
			leg := b.CallList[0]
			c.CTI.CallerNo, c.CTI.CalledNo = leg.CallerNo, leg.CalledNo
			if sameNumber(leg.Peer, leg.CallerNo) {
				c.CTI.CallerName = leg.PeerName
			} else if sameNumber(leg.Peer, leg.CalledNo) {
				c.CTI.CalledName = leg.PeerName
			}
			c.CTI.Target = leg.CalledNo
		}
	default:
		c.CTI.TalkState = TalkTwoParty
		switch strings.ToLower(b.State) {
		case "dialing", "ringing":
			c.CTI.CallPhase = strings.ToLower(b.State)
		case "talking", "holding":
			c.CTI.CallPhase = "talking"
		}
	}
}

// applyServerConferenceState 把服务端会议面板翻译进 Call.CTI。
func applyServerConferenceState(c *Call, self string, b *srvConfPanelBody) {
	c.CTI = CTIState{TalkState: TalkConference, ConfID: b.ConfID, Owner: b.Owner}
	for _, m := range b.MemberList {
		if m.Owner {
			c.CTI.Owner = m.Member
			c.CTI.OwnerName = m.MemberName
		}
		c.CTI.Members = append(c.CTI.Members, ConferenceMember{
			TelNo: m.Member, DisplayName: m.MemberName, State: m.State,
		})
	}
}
