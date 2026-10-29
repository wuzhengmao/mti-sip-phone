package core

import (
	"encoding/json"
	"testing"
	"time"

	"mti-sip-phone/internal/config"
)

// newPanelApp 造一个坐席号 2001 的空应用（无通话）。
func newPanelApp() *App {
	cfg := config.Default()
	cfg.Account.Account = "2001"
	return New(cfg, nil, NewBus())
}

// TestPanelEmpty 没有进行中的通话：callPanel / conferencePanel 为空。
func TestPanelEmpty(t *testing.T) {
	app := newPanelApp()
	st := app.PanelState()
	if st.CallPanel != nil || st.ConferencePanel != nil {
		t.Fatalf("无通话时两个面板都应为空：%+v", st)
	}
}

// TestPanelTwoParty 两方通话：状态/文案/对端与子状态。
func TestPanelTwoParty(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "1381003", RemoteName: "外线"})
	st := app.PanelState()
	if st.CallPanel == nil {
		t.Fatal("来电时应有通话面板")
	}
	cp := st.CallPanel
	if cp.State != "incoming" || cp.Title != "来电" || cp.CallID != "c1" || cp.StartTime <= 0 {
		t.Fatalf("来电面板不符：%+v", cp)
	}
	if len(cp.CallList) != 1 || cp.CallList[0].Peer.Number != "1381003" ||
		cp.CallList[0].Peer.Name != "外线" || cp.CallList[0].CallStatus != "incoming" {
		t.Fatalf("来电 callList 不符：%+v", cp.CallList)
	}

	app.OnEstablished("c1")
	cp = app.PanelState().CallPanel
	if cp.State != "talking" || cp.Title != "通话中" || cp.CallList[0].CallStatus != "talking" {
		t.Fatalf("接通后面板不符：%+v", cp)
	}

	// 外线呼入分配的技能组（CALL_RINGING 的 skillGroup）带到面板上
	app.OnMSPEvent("call_ringing", map[string]any{
		"callId": "CTI-1", "callerNo": "1381003", "calledNo": "01108899",
		"agent": "2001", "agentName": "张三", "skillGroup": "110"})
	if cp := app.PanelState().CallPanel; cp == nil || cp.CallList[0].SkillGroup != "110" {
		t.Fatalf("面板的腿应带技能组 110：%+v", cp)
	}

	// 结束后两个面板都清空
	app.OnTerminated("c1", "remote_hangup")
	if p := app.PanelState(); p.CallPanel != nil || p.ConferencePanel != nil {
		t.Fatalf("通话结束后应清空面板：%+v", p)
	}
}

// TestPanelConsultAndThreeWay 咨询/三方：状态、双方顺序与时间戳。
func TestPanelConsultAndThreeWay(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "1381003"})
	app.OnEstablished("c1")
	app.OnMSPEvent("consult_started", map[string]any{
		"consulter": "2001", "consultee": "2002", "consulteeName": "李四"})
	cp := app.PanelState().CallPanel
	if cp.State != "consult-dialing" || cp.Title != "咨询呼叫中" || len(cp.CallList) != 2 {
		t.Fatalf("咨询面板不符：%+v", cp)
	}
	if cp.CallList[0].Peer.Number != "1381003" || cp.CallList[1].Peer.Number != "2002" ||
		cp.CallList[1].Peer.Name != "李四" || cp.CallList[1].CallStatus != "dialing" ||
		cp.CallList[1].StartTime <= 0 {
		t.Fatalf("咨询 callList 不符：%+v", cp.CallList)
	}

	app.OnMSPEvent("consult_connected", nil)
	cp = app.PanelState().CallPanel
	if cp.State != "consult-talking" || cp.Title != "咨询通话中" || cp.CallList[1].CallStatus != "talking" {
		t.Fatalf("咨询接通后面板不符：%+v", cp)
	}

	app.OnMSPEvent("three_way_started", map[string]any{
		"owner": "2001", "firstParty": "2001", "secondParty": "1381003",
		"thirdParty": "2002", "thirdPartyName": "李四"})
	cp = app.PanelState().CallPanel
	if cp.State != "three-way" || cp.Title != "三方通话中" || len(cp.CallList) != 2 {
		t.Fatalf("三方面板不符：%+v", cp)
	}
	for _, leg := range cp.CallList {
		if leg.Peer.Number == "2001" {
			t.Fatalf("三方列表不应包含自己：%+v", cp.CallList)
		}
		if leg.CallStatus != "talking" {
			t.Fatalf("三方各方的子状态应为 talking：%+v", leg)
		}
	}
}

// TestPanelMonitor 监听：peer=主叫、target=被叫，耳语态文案。
func TestPanelMonitor(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "2002"})
	app.OnEstablished("c1")
	app.OnMSPEvent("monitor_started", map[string]any{
		"monitor": "2001", "target": "2002", "targetName": "李四",
		"callerNo": "1381003", "calledNo": "2002", "calledName": "李四"})
	cp := app.PanelState().CallPanel
	if cp.State != "monitoring" || cp.Title != "监听中" || len(cp.CallList) != 1 {
		t.Fatalf("监听面板不符：%+v", cp)
	}
	leg := cp.CallList[0]
	if leg.Peer.Number != "1381003" || leg.Target == nil || leg.Target.Number != "2002" ||
		leg.Target.Name != "李四" {
		t.Fatalf("监听的主叫/被叫不符：%+v", leg)
	}
	app.OnMSPEvent("monitor_whisper", nil)
	cp = app.PanelState().CallPanel
	if cp.State != "whispering" || cp.Title != "监听耳语中" {
		t.Fatalf("耳语态面板不符：%+v", cp)
	}
}

// TestPanelConference 会议面板：主持人第一行、成员状态与加入时间。
func TestPanelConference(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "1381003"})
	app.OnEstablished("c1")
	app.OnMSPEvent("conference_started", map[string]any{"confId": "conf-1", "owner": "2002"})
	app.OnMSPEvent("conference_members_changed", map[string]any{
		"confId": "conf-1", "owner": "2002",
		"members": []any{
			map[string]any{"telNo": "2001", "state": "talking"},
			map[string]any{"telNo": "2002", "displayName": "李四", "state": "talking"},
			map[string]any{"telNo": "2003", "state": "dialing"},
		},
	})
	st := app.PanelState()
	if st.ConferencePanel == nil {
		t.Fatal("会议时应带会议面板")
	}
	conf := st.ConferencePanel
	if conf.ConfID != "conf-1" || conf.Owner != "2002" || len(conf.MemberList) != 3 {
		t.Fatalf("会议面板头部不符：%+v", conf)
	}
	if conf.MemberList[0].Member.Number != "2002" || conf.MemberList[0].Member.Name != "李四" ||
		!conf.MemberList[0].Owner {
		t.Fatalf("主持人应排第一行：%+v", conf.MemberList)
	}
	for _, m := range conf.MemberList {
		if m.Timestamp <= 0 {
			t.Fatalf("成员应有加入时间：%+v", m)
		}
	}
	if st.CallPanel != nil {
		t.Fatalf("会议时不应同时有 callPanel（两个面板互斥）：%+v", st.CallPanel)
	}
}

// TestPanelConferenceInvitee 会议呼叫被叫 B：振铃阶段只有普通 callPanel（来电），
// 收到会议事件也不出现 conferencePanel；接通后才切换成 conferencePanel。
func TestPanelConferenceInvitee(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "2001", RemoteName: "张三"})
	// 会议事件先到（CTI 可能不等接通就推 CONFERENCE_STARTED/MEMBERS_CHANGED）
	app.OnMSPEvent("conference_started", map[string]any{"confId": "conf-1", "owner": "2001"})
	app.OnMSPEvent("conference_members_changed", map[string]any{
		"confId": "conf-1", "owner": "2001",
		"members": []any{
			map[string]any{"telNo": "2001", "displayName": "张三", "state": "talking"},
			map[string]any{"telNo": "2002", "state": "dialing"},
		},
	})
	st := app.PanelState()
	if st.ConferencePanel != nil {
		t.Fatalf("被叫接通前不应有会议面板：%+v", st.ConferencePanel)
	}
	if st.CallPanel == nil || st.CallPanel.State != "incoming" || st.CallPanel.Title != "来电" {
		t.Fatalf("被叫振铃时应有普通来电面板：%+v", st.CallPanel)
	}

	// 接通后：切换成会议面板，callPanel 消失（两者互斥）
	app.OnEstablished("c1")
	st = app.PanelState()
	if st.CallPanel != nil {
		t.Fatalf("接通后不应再保留 callPanel：%+v", st.CallPanel)
	}
	if st.ConferencePanel == nil || st.ConferencePanel.ConfID != "conf-1" {
		t.Fatalf("接通后应有会议面板：%+v", st.ConferencePanel)
	}
}

// TestPanelAgentMode agent_online 更新坐席状态（isBusy/acw/isLeave 归纳为
// AgentStatus/AgentStatusText；call.panel 不再携带坐席字段）。
func TestPanelAgentMode(t *testing.T) {
	app := newPanelApp()
	app.OnMSPEvent("agent_online", map[string]any{
		"displayName": "张三", "agentMode": "monitor", "isBusy": true})
	if app.AgentMode() != AgentModeMonitor || !app.AgentBusy() {
		t.Fatalf("坐席模式/置忙不符：mode=%q busy=%v", app.AgentMode(), app.AgentBusy())
	}
	if app.AgentStatus() != "busy" || app.AgentStatusText() != "置忙" {
		t.Fatalf("置忙状态不符：status=%q text=%q", app.AgentStatus(), app.AgentStatusText())
	}
	app.OnMSPEvent("agent_online", map[string]any{
		"agentMode": "normal", "isBusy": true, "acw": true, "isLeave": false})
	if app.AgentStatus() != "acw" || app.AgentStatusText() != "整理中" {
		t.Fatalf("整理中状态不符：status=%q text=%q", app.AgentStatus(), app.AgentStatusText())
	}
	app.OnMSPEvent("agent_online", map[string]any{
		"agentMode": "normal", "isBusy": false, "acw": false, "isLeave": true})
	if app.AgentStatus() != "leave" || app.AgentStatusText() != "离席" {
		t.Fatalf("离席状态不符：status=%q text=%q", app.AgentStatus(), app.AgentStatusText())
	}
}

// TestPanelDedup 相同快照只推一次：挂断时「坐席进入整理态」与「通话结束」两条
// 路径先后触发 publishCallPanel，快照相同则跳过第二次（用户要求）。
func TestPanelDedup(t *testing.T) {
	app := newPanelApp()
	sub := app.bus.Subscribe()
	defer app.bus.Unsubscribe(sub)

	// 第一次：坐席状态变化 → 推一份 call.panel（agentMode=normal, isBusy=true, acw=true）
	app.OnMSPEvent("agent_online", map[string]any{
		"agentMode": "normal", "isBusy": true, "acw": true})
	// 之后两次（挂断的其它路径）状态没变 → 都应被去重跳过
	app.publishCallPanel()
	app.publishCallPanel()

	count := 0
	for {
		select {
		case ev := <-sub:
			if ev.Name == "call.panel" {
				count++
			}
		default:
			goto done
		}
	}
done:
	if count != 1 {
		t.Fatalf("相同快照应只推一次，实际 %d 次", count)
	}
}

// TestServerCallPanel 新协议（2026-10）：服务端直接下发完整通话面板
// （MSP_CALL_PANEL_UPDATED），话机整份覆盖本地面板，并翻译进 CTI 供桌面面板使用。
func TestServerCallPanel(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "91381003"})

	app.OnMSPEvent("call_panel_updated", map[string]any{
		"callId": "CTI-1", "agent": "2001", "callType": "incoming",
		"callMode": "normal", "state": "ringing", "startTime": int64(1792381839000),
		"callList": []any{map[string]any{
			"callerNo": "91381003", "calledNo": "2001", "peer": "91381003",
			"peerName": "外线", "intercom": true, "skillGroup": "110",
			"callType": "incoming", "callStatus": "ringing",
			"startTime": int64(1792381839000), "ringingTime": int64(1792381839500),
			"offhookTime": int64(1792381842000),
		}},
	})
	cp := app.PanelState().CallPanel
	if cp == nil || cp.State != "ringing" || cp.Title != "振铃中" ||
		cp.CallID != "CTI-1" || cp.Agent != "2001" || len(cp.CallList) != 1 {
		t.Fatalf("服务端通话面板未生效：%+v", cp)
	}
	leg := cp.CallList[0]
	if leg.Peer.Number != "91381003" || leg.Peer.Name != "外线" || leg.CallStatus != "ringing" {
		t.Fatalf("callList 映射不符：%+v", leg)
	}
	// 协议属性原样保留（SDK 交给应用，应用可能用到）：callerNo/calledNo/
	// intercom/skillGroup/callStatus/startTime/ringingTime/offhookTime。
	if leg.CallerNo != "91381003" || leg.CalledNo != "2001" || !leg.Intercom ||
		leg.SkillGroup != "110" || leg.RingingTime != 1792381839500 ||
		leg.OffhookTime != 1792381842000 {
		t.Fatalf("协议属性不应被丢弃/改名：%+v", leg)
	}
	// 翻译进 CTI：桌面面板使用（CallPhase=ringing）
	if c := app.activeCall(); c == nil || c.CTI.CallPhase != "ringing" {
		t.Fatalf("Call.CTI 未按服务端面板更新：%+v", c)
	}

	// 面板空闲（callId 为空）：两个面板都清空
	app.OnMSPEvent("call_panel_updated", map[string]any{"callId": ""})
	if p := app.PanelState(); p.CallPanel != nil || p.ConferencePanel != nil {
		t.Fatalf("面板空闲后应清空：%+v", p)
	}
}

// TestServerConsultPanel 咨询面板：state consult-* + 两条腿 → CTI 咨询状态。
func TestServerConsultPanel(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "1381003"})
	app.OnEstablished("c1")
	app.OnMSPEvent("call_panel_updated", map[string]any{
		"callId": "CTI-1", "agent": "2001", "callMode": "consult", "owner": "2001",
		"state": "consult-ringing", "startTime": int64(1792381839000),
		"callList": []any{
			map[string]any{"callerNo": "1381003", "calledNo": "2001", "peer": "1381003",
				"callStatus": "holding", "startTime": int64(1792381839000)},
			map[string]any{"callerNo": "2001", "calledNo": "2002", "peer": "2002",
				"peerName": "李四", "callType": "consult", "callStatus": "ringing",
				"startTime": int64(1792381839807)},
		},
	})
	cp := app.PanelState().CallPanel
	if cp == nil || cp.State != "consult-ringing" || cp.Title != "咨询振铃中" ||
		cp.Owner != "2001" || len(cp.CallList) != 2 {
		t.Fatalf("咨询面板不符：%+v", cp)
	}
	c := app.activeCall()
	if c == nil || c.CTI.TalkState != TalkConsult || c.CTI.ConsultPhase != "ringing" ||
		c.CTI.Consultee != "2002" || c.CTI.ConsulteeName != "李四" {
		t.Fatalf("咨询状态未翻译进 CTI：%+v", c)
	}
}

// TestServerConferencePanel 会议面板：整份覆盖成员；会议与通话面板互斥。
func TestServerConferencePanel(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "2001"})
	app.OnEstablished("c1")
	app.OnMSPEvent("conference_panel_updated", map[string]any{
		"confId": "conf-1", "owner": "2002",
		"memberList": []any{
			map[string]any{"member": "2001", "memberName": "我", "intercom": true,
				"owner": false, "state": "talking", "timestamp": int64(100)},
			map[string]any{"member": "2002", "memberName": "李四", "intercom": true,
				"owner": true, "state": "talking", "timestamp": int64(200)},
		},
	})
	st := app.PanelState()
	if st.CallPanel != nil || st.ConferencePanel == nil {
		t.Fatalf("会议时只应有会议面板：%+v", st)
	}
	conf := st.ConferencePanel
	if conf.ConfID != "conf-1" || conf.Owner != "2002" || len(conf.MemberList) != 2 {
		t.Fatalf("会议面板映射不符：%+v", conf)
	}
	host := conf.MemberList[0]
	if host.Member.Number != "2002" || host.Member.Name != "李四" || !host.Owner ||
		!host.Intercom || host.Timestamp != 200 {
		t.Fatalf("协议属性应原样保留、主持人排第一行：%+v", conf.MemberList)
	}
	if c := app.activeCall(); c == nil || c.CTI.TalkState != TalkConference ||
		c.CTI.Owner != "2002" || len(c.CTI.Members) != 2 {
		t.Fatalf("会议面板未翻译进 CTI：%+v", c)
	}
}

// TestServerConferenceEnded 空成员列表 = 会议结束/被移出：挂断本端。
func TestServerConferenceEnded(t *testing.T) {
	tel := &ctiHangupTel{ch: make(chan string, 1)}
	cfg := config.Default()
	cfg.Account.Account = "2001"
	app := New(cfg, tel, NewBus())
	app.OnIncoming("c1", IncomingCall{Remote: "2002"})
	app.OnEstablished("c1")
	app.OnMSPEvent("conference_panel_updated", map[string]any{
		"confId": "conf-1", "owner": "2002",
		"memberList": []any{map[string]any{"member": "2001", "state": "talking"}},
	})
	app.OnMSPEvent("conference_panel_updated", map[string]any{"confId": "conf-1", "owner": "2002"})
	select {
	case id := <-tel.ch:
		if id != "c1" {
			t.Fatalf("应挂断 c1，实际 %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("会议结束（空成员列表）应关闭通话面板（挂断）")
	}
}

// TestServerConsultConsultee 被咨询方的面板（callMode=consult，但 callList 里
// **没有** callType=consult 的腿）：按普通通话显示（不进入咨询面板、不显示
// 咨询行/咨询按钮），只置 ConsultInProgress 隐藏话务入口。
func TestServerConsultConsultee(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "2999"})
	app.OnEstablished("c1")
	app.OnMSPEvent("call_panel_updated", map[string]any{
		"callId": "CTI-9", "agent": "2001", "callMode": "consult", "owner": "2999",
		"state": "consult-talking", "startTime": int64(1792381839000),
		"callList": []any{map[string]any{
			"callerNo": "2999", "calledNo": "2001", "peer": "2999",
			"callStatus": "talking", "startTime": int64(1792381839000),
		}},
	})
	cp := app.PanelState().CallPanel
	if cp == nil || cp.State != "talking" || cp.Title != "通话中" {
		t.Fatalf("被咨询方面板应按普通通话显示：%+v", cp)
	}
	for _, leg := range cp.CallList {
		if leg.CallType == "consult" {
			t.Fatalf("被咨询方不应有咨询腿：%+v", cp.CallList)
		}
	}
	c := app.activeCall()
	if c == nil || c.CTI.TalkState != TalkTwoParty || !c.CTI.ConsultInProgress {
		t.Fatalf("被咨询方应保持双方通话 + ConsultInProgress：%+v", c)
	}
}

// TestServerPanelClearsConsultFlag 咨询转移后面板回到 callMode=normal：不能再用
// INVITE 的 Call-Info call-type=consult 本地标记（Call.Consult）把
// 咨询/转移/会议入口藏掉（真机：网页正常、桌面按钮不显示）。
func TestServerPanelClearsConsultFlag(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "1381003", Consult: true})
	app.OnEstablished("c1")
	app.OnMSPEvent("call_panel_updated", map[string]any{
		"callId": "CTI-1", "agent": "2001", "callMode": "normal",
		"state": "talking", "startTime": int64(1792381839000),
		"callList": []any{map[string]any{
			"callerNo": "1381003", "calledNo": "2001", "peer": "1381003",
			"callStatus": "talking", "startTime": int64(1792381839000),
		}},
	})
	c := app.activeCall()
	if c == nil || c.Consult {
		t.Fatalf("服务端面板为 normal 时应清掉 Call.Consult：%+v", c)
	}
	if c.CTI.TalkState != TalkTwoParty || c.CTI.ConsultInProgress {
		t.Fatalf("应为普通两方通话：%+v", c.CTI)
	}
}

// TestServerConsultOwnerWins 咨询角色以面板的 owner 为准（协议 §16.2）：owner 不是
// 本机时，即使 callList 里有 callType=consult 的腿，也按普通通话显示，不出现咨询
// 控制（三方/转移/结束咨询）。
func TestServerConsultOwnerWins(t *testing.T) {
	app := newPanelApp() // 本机 2001
	app.OnIncoming("c1", IncomingCall{Remote: "2999"})
	app.OnEstablished("c1")
	app.OnMSPEvent("call_panel_updated", map[string]any{
		"callId": "CTI-1", "agent": "2001", "callMode": "consult", "owner": "2999",
		"state": "consult-talking", "startTime": int64(1792381839000),
		"callList": []any{
			map[string]any{"callerNo": "2999", "calledNo": "2001", "peer": "2999",
				"callStatus": "talking", "startTime": int64(1792381839000)},
			map[string]any{"callerNo": "2999", "calledNo": "2002", "peer": "2002",
				"callType": "consult", "callStatus": "talking", "startTime": int64(1792381839807)},
		},
	})
	cp := app.PanelState().CallPanel
	if cp == nil || cp.Owner != "2999" || cp.State != "talking" || cp.Title != "通话中" {
		t.Fatalf("非 owner 的咨询面板应按普通通话显示：%+v", cp)
	}
	c := app.activeCall()
	if c == nil || c.CTI.TalkState != TalkTwoParty || !c.CTI.ConsultInProgress {
		t.Fatalf("非 owner 不应进入咨询面板：%+v", c)
	}
}

// TestServerThreeWayOwner 三方以 owner 区分发起方：只有发起方能看到强制挂断
// （X）。非 owner 也要拿到 owner 字段（桌面/网页据此隐藏按钮）。
func TestServerThreeWayOwner(t *testing.T) {
	app := newPanelApp() // 本机 2001
	app.OnIncoming("c1", IncomingCall{Remote: "1381003"})
	app.OnEstablished("c1")
	app.OnMSPEvent("call_panel_updated", map[string]any{
		"callId": "CTI-1", "agent": "2001", "callMode": "three-way", "owner": "2999",
		"state": "three-way", "startTime": int64(1792381839000),
		"callList": []any{
			map[string]any{"callerNo": "1381003", "calledNo": "2001", "peer": "1381003",
				"callStatus": "talking", "startTime": int64(1792381839000)},
			map[string]any{"callerNo": "2999", "calledNo": "2002", "peer": "2002",
				"callStatus": "talking", "startTime": int64(1792381839000)},
			map[string]any{"callerNo": "2001", "calledNo": "2999", "peer": "2999",
				"callStatus": "talking", "startTime": int64(1792381839000)},
		},
	})
	cp := app.PanelState().CallPanel
	if cp == nil || cp.State != "three-way" || cp.Owner != "2999" || len(cp.CallList) != 3 {
		t.Fatalf("三方面板不符：%+v", cp)
	}
	c := app.activeCall()
	if c == nil || c.CTI.TalkState != TalkThreeWay || c.CTI.Owner != "2999" {
		t.Fatalf("非 owner 也必须拿到 owner（桌面据此隐藏强制挂断）：%+v", c)
	}
}

// TestServerMonitorOwner 监听：owner 是班长（发起方）。
func TestServerMonitorOwner(t *testing.T) {
	app := newPanelApp() // 本机 2001
	app.OnIncoming("c1", IncomingCall{Remote: "2002"})
	app.OnEstablished("c1")
	app.OnMSPEvent("call_panel_updated", map[string]any{
		"callId": "CTI-1", "agent": "2001", "callMode": "monitor", "owner": "2001",
		"state": "monitor", "startTime": int64(1792381839000),
		"callList": []any{map[string]any{
			"callerNo": "1381003", "calledNo": "2002", "peer": "1381003",
			"callStatus": "talking", "startTime": int64(1792381839000),
		}},
	})
	cp := app.PanelState().CallPanel
	if cp == nil || cp.Owner != "2001" || cp.State != "monitoring" || len(cp.CallList) != 1 {
		t.Fatalf("监听面板不符：%+v", cp)
	}
	if cp.CallList[0].CallerNo != "1381003" || cp.CallList[0].CalledNo != "2002" ||
		cp.CallList[0].Target == nil || cp.CallList[0].Target.Number != "2002" {
		t.Fatalf("监听腿的主被叫不符：%+v", cp.CallList[0])
	}
	if c := app.activeCall(); c == nil || c.CTI.TalkState != TalkMonitor || c.CTI.Monitor != "2001" {
		t.Fatalf("监听状态未翻译进 CTI：%+v", c)
	}
}

// TestPanelJSONKeepsProtocolFields 对外快照（SDK/网页拿到的 JSON）必须保留协议
// 属性名、不丢弃话机未用到的属性：callList 用 `callStatus`（不是 state），
// callerNo/calledNo/intercom/ringingTime/offhookTime 原样存在；会议面板用
// `{confId, owner, memberList:[{member, memberName, intercom, owner, state, timestamp}]}`。
func TestPanelJSONKeepsProtocolFields(t *testing.T) {
	app := newPanelApp()
	app.OnIncoming("c1", IncomingCall{Remote: "91381003"})
	app.OnMSPEvent("call_panel_updated", map[string]any{
		"callId": "CTI-1", "agent": "2001", "callType": "incoming",
		"callMode": "consult", "owner": "2001", "state": "consult-talking",
		"startTime": int64(1000),
		"callList": []any{
			map[string]any{"callerNo": "91381003", "calledNo": "2001", "peer": "91381003",
				"peerName": "外线", "intercom": true, "skillGroup": "110",
				"callType": "incoming", "callStatus": "holding",
				"startTime": int64(1000), "ringingTime": int64(1100), "offhookTime": int64(1200)},
			map[string]any{"callerNo": "2001", "calledNo": "2002", "peer": "2002",
				"callType": "consult", "callStatus": "talking", "startTime": int64(1300)},
		},
	})
	cp := marshalPanel(t, app)["callPanel"].(map[string]any)
	for _, k := range []string{"callId", "agent", "callType", "callMode", "owner",
		"state", "startTime", "title", "callList"} {
		if _, ok := cp[k]; !ok {
			t.Fatalf("callPanel 缺少协议字段 %q：%v", k, cp)
		}
	}
	legs := cp["callList"].([]any)
	if len(legs) != 2 {
		t.Fatalf("callList 数量不符：%v", legs)
	}
	leg := legs[0].(map[string]any)
	for _, k := range []string{"callerNo", "calledNo", "peer", "intercom", "skillGroup",
		"callType", "callStatus", "startTime", "ringingTime", "offhookTime"} {
		if _, ok := leg[k]; !ok {
			t.Fatalf("callList 项缺少协议字段 %q：%v", k, leg)
		}
	}
	if _, ok := leg["state"]; ok {
		t.Fatalf("callStatus 不应改名为 state：%v", leg)
	}
	peer := leg["peer"].(map[string]any)
	if peer["number"] != "91381003" || peer["name"] != "外线" {
		t.Fatalf("peer 对象不符：%v", peer)
	}

	app.OnMSPEvent("conference_panel_updated", map[string]any{
		"confId": "conf-1", "owner": "2002",
		"memberList": []any{map[string]any{
			"member": "2002", "memberName": "李四", "intercom": true,
			"owner": true, "state": "talking", "timestamp": int64(500),
		}},
	})
	conf := marshalPanel(t, app)["conferencePanel"].(map[string]any)
	if conf["confId"] != "conf-1" || conf["owner"] != "2002" {
		t.Fatalf("会议面板头部不符：%v", conf)
	}
	m := conf["memberList"].([]any)[0].(map[string]any)
	for _, k := range []string{"member", "intercom", "owner", "state", "timestamp"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("memberList 项缺少协议字段 %q：%v", k, m)
		}
	}
	mem := m["member"].(map[string]any)
	if mem["number"] != "2002" || mem["name"] != "李四" {
		t.Fatalf("member 对象不符：%v", mem)
	}
}

// TestPanelPartyNoteOnlyOnFallback 协议已经给了名称时不再附带通信录备注；
// 只有名称为空、回退到通信录时才同时给出 name 与 note（用户口径）。
func TestPanelPartyNoteOnlyOnFallback(t *testing.T) {
	app, f := newContactsApp(t)
	f.list = []Contact{{Number: "91381003", Name: "张三", Note: "前台"}}
	if err := app.ReloadContacts(); err != nil {
		t.Fatal(err)
	}
	p := app.party("91381003", "外线")
	if p.Name != "外线" || p.Note != "" {
		t.Fatalf("协议已给名称时不应带通信录备注：%+v", p)
	}
	p = app.party("91381003", "")
	if p.Name != "张三" || p.Note != "前台" {
		t.Fatalf("回退通信录时应同时给出名称与备注：%+v", p)
	}
}

// marshalPanel 把当前面板快照序列化成 map（模拟 SDK/网页收到的 JSON）。
func marshalPanel(t *testing.T, app *App) map[string]any {
	t.Helper()
	raw, err := json.Marshal(app.PanelState())
	if err != nil {
		t.Fatalf("序列化面板失败：%v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("反序列化面板失败：%v", err)
	}
	return m
}
