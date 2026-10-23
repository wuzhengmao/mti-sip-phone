package core

import (
	"testing"
	"time"

	"mti-sip-phone/internal/config"
)

// TestAgentMode 坐席模式（MSP_EVENT_AGENT_ONLINE/OFFLINE，REQ-ACC-028）：
// 新协议下 ONLINE **不再过期**（坐席状态的保持靠话机自己发心跳），只有
// OFFLINE 或本地登出会退出。事件与应用级状态有关，**不需要有通话**。
func TestAgentMode(t *testing.T) {
	app, _ := newRegTestApp(t)

	if app.AgentOnline() {
		t.Fatal("初始不应处于坐席模式")
	}

	// ONLINE → 立即进入，displayName 一并记录
	app.OnMSPEvent("agent_online", map[string]any{"displayName": "张三"})
	if !app.AgentOnline() {
		t.Fatal("收到 AGENT_ONLINE 后应进入坐席模式")
	}
	if got := app.AgentDisplayName(); got != "张三" {
		t.Fatalf("displayName 应为 张三，实际 %q", got)
	}
	// 新的协议里 ONLINE 不再带 expires，也不该自己过期（以前 50s 会掉线）
	time.Sleep(1200 * time.Millisecond)
	if !app.AgentOnline() {
		t.Fatal("ONLINE 不应自己过期（坐席状态由话机心跳维持）")
	}
	// 幂等：同一条 ONLINE 重传不改变状态
	app.OnMSPEvent("agent_online", map[string]any{"displayName": "张三"})
	if !app.AgentOnline() {
		t.Fatal("重复 ONLINE 应幂等")
	}

	// OFFLINE 立即退出
	app.OnMSPEvent("agent_offline", nil)
	if app.AgentOnline() {
		t.Fatal("收到 AGENT_OFFLINE 应立即退出坐席模式")
	}
	if got := app.AgentDisplayName(); got != "" {
		t.Fatalf("退出坐席模式后 displayName 应为空，实际 %q", got)
	}

	// 状态快照里也要带上（网页/界面初始化用）
	if app.State().AgentOnline {
		t.Fatal("退出后 State().AgentOnline 应为 false")
	}

	// 登出账号也要退出坐席模式（REQ-ACC-028 补充）
	app.OnMSPEvent("agent_online", map[string]any{"displayName": "张三"})
	if !app.AgentOnline() {
		t.Fatal("应重新进入坐席模式")
	}
	if err := app.SetRegistered(false); err != nil {
		t.Fatalf("SetRegistered(false) 失败: %v", err)
	}
	if app.AgentOnline() {
		t.Fatal("登出账号后应退出坐席模式")
	}
}

// TestAgentModeBusy 协议 §16.1 的 agentMode / isBusy：ONLINE 同步坐席模式与
// 置忙状态；agentMode=none 表示"话机不该处于坐席模式"，按退出坐席处理。
func TestAgentModeBusy(t *testing.T) {
	app, _ := newRegTestApp(t)

	app.OnMSPEvent("agent_online", map[string]any{
		"displayName": "张三", "agentMode": "monitor", "isBusy": true})
	if !app.AgentOnline() || app.AgentMode() != AgentModeMonitor || !app.AgentBusy() {
		t.Fatalf("应进入班长模式且置忙，实际 online=%v mode=%q busy=%v",
			app.AgentOnline(), app.AgentMode(), app.AgentBusy())
	}
	// 置闲 + 切回普通模式：ONLINE 再推一次
	app.OnMSPEvent("agent_online", map[string]any{"agentMode": "normal", "isBusy": false})
	if app.AgentMode() != AgentModeNormal || app.AgentBusy() {
		t.Fatalf("应切回普通模式且空闲，实际 mode=%q busy=%v", app.AgentMode(), app.AgentBusy())
	}
	// agentMode=none：退出坐席模式
	app.OnMSPEvent("agent_online", map[string]any{"agentMode": "none"})
	if app.AgentOnline() || app.AgentMode() != AgentModeNone || app.AgentBusy() {
		t.Fatalf("agentMode=none 应退出坐席，实际 online=%v mode=%q busy=%v",
			app.AgentOnline(), app.AgentMode(), app.AgentBusy())
	}
}

// TestAgentHeartbeat CTI 心跳（MSP_HEARTBEAT，协议 §10）：**注册成功就发**（与
// 坐席模式无关，agentMode 如实上报），注销即停；首包立即发，之后按配置间隔
// （默认 20s）；expires = 间隔 × 2.5 = 50s。
func TestAgentHeartbeat(t *testing.T) {
	app, tel := newRegTestApp(t)

	app.OnRegisterState(RegRegistered, "")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n, _, _, _, _ := tel.heartbeat(); n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("注册成功后应立即发送一次心跳")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, mode, busy, expires, _ := tel.heartbeat()
	if mode != AgentModeNone {
		t.Fatalf("还没进坐席模式时 agentMode 应为 none，实际 %q", mode)
	}
	if busy {
		t.Fatal("还没进坐席模式时 isBusy 应为 false")
	}
	if expires != 50*time.Second {
		t.Fatalf("expires 应为间隔 20s × 2.5 = 50s，实际 %v", expires)
	}

	// 进入坐席模式后，心跳如实上报 agentMode / isBusy / displayName
	app.OnMSPEvent("agent_online", map[string]any{
		"displayName": "张三", "agentMode": "normal", "isBusy": false})
	app.sendHeartbeatOnce()
	_, mode, busy, expires, name := tel.heartbeat()
	if mode != AgentModeNormal {
		t.Fatalf("进入坐席模式后 agentMode 应为 normal，实际 %q", mode)
	}
	if busy {
		t.Fatal("空闲坐席的 isBusy 应为 false")
	}
	if name != "张三" {
		t.Fatalf("心跳应带上 displayName 张三，实际 %q", name)
	}
	if expires != 50*time.Second {
		t.Fatalf("expires 应为 50s，实际 %v", expires)
	}

	// 注销后心跳停止
	app.OnRegisterState(RegUnregistered, "")
	time.Sleep(100 * time.Millisecond)
	before, _, _, _, _ := tel.heartbeat()
	time.Sleep(300 * time.Millisecond)
	after, _, _, _, _ := tel.heartbeat()
	if after != before {
		t.Fatalf("注销后不应再发心跳（%d → %d）", before, after)
	}
}

// TestCTIUnsupported 服务端不支持 NOTIFY（协议 1.0.18）的两种表现——
// MSP_UNSUPPORTED 通知、MSP_HEARTBEAT 的 SIP 响应不是 200——都要**停止心跳
// 并退出坐席模式**。
func TestCTIUnsupported(t *testing.T) {
	app, tel := newRegTestApp(t)

	// ① MSP_UNSUPPORTED
	app.OnRegisterState(RegRegistered, "")
	app.OnMSPEvent("agent_online", map[string]any{"agentMode": "monitor"})
	if !app.AgentOnline() {
		t.Fatal("应先进入坐席模式")
	}
	app.OnMSPEvent("unsupported", map[string]any{"reason": "no support"})
	if app.AgentOnline() || app.AgentMode() != AgentModeNone {
		t.Fatalf("收到 MSP_UNSUPPORTED 后应退出坐席模式，实际 online=%v mode=%q",
			app.AgentOnline(), app.AgentMode())
	}
	app.heartbeat.mu.Lock()
	running := app.heartbeat.stop != nil
	app.heartbeat.mu.Unlock()
	if running {
		t.Fatal("收到 MSP_UNSUPPORTED 后应停止心跳")
	}

	// ② 心跳非 200（SendHeartbeat 返回 ErrCTINotifyUnsupported）
	app.OnRegisterState(RegUnregistered, "")
	tel.hbErr = ErrCTINotifyUnsupported
	app.OnRegisterState(RegRegistered, "")
	app.OnMSPEvent("agent_online", map[string]any{"agentMode": "normal"})
	deadline := time.Now().Add(2 * time.Second)
	for app.AgentOnline() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if app.AgentOnline() {
		t.Fatal("心跳返回非 200 时应退出坐席模式")
	}
	app.heartbeat.mu.Lock()
	running = app.heartbeat.stop != nil
	app.heartbeat.mu.Unlock()
	if running {
		t.Fatal("心跳返回非 200 后应停止心跳")
	}
}

// TestApplyMSPEvent 通话面板事件（MSP_EVENT_*）翻译成 CTI 状态（REQ-ACC-026）：
// 各状态进入/阶段推进/退出，以及未识别事件忽略。
func TestApplyMSPEvent(t *testing.T) {
	// 咨询：started → ringing → connected → finished 回到双方通话
	st := CTIState{}
	applyMSPEvent(&st, "consult_started", map[string]any{"consulter": "2001", "consultee": "2002"}, "2001")
	if st.TalkState != TalkConsult || st.Consulter != "2001" || st.Consultee != "2002" || st.ConsultPhase != "dialing" {
		t.Fatalf("consult_started 后状态不符：%+v", st)
	}
	applyMSPEvent(&st, "consult_ringing", nil, "2001")
	if st.ConsultPhase != "ringing" {
		t.Fatalf("consult_ringing 后阶段应为 ringing：%+v", st)
	}
	applyMSPEvent(&st, "consult_connected", nil, "2001")
	if st.ConsultPhase != "talking" {
		t.Fatalf("consult_connected 后阶段应为 talking：%+v", st)
	}
	applyMSPEvent(&st, "consult_finished", nil, "2001")
	if st.TalkState != TalkTwoParty || st.Consulter != "" {
		t.Fatalf("consult_finished 后应回到双方通话：%+v", st)
	}

	// 三方（协议 §16.3：firstParty/secondParty 是原通话双方，thirdParty 是新加入的一方）
	applyMSPEvent(&st, "three_way_started", map[string]any{
		"owner": "2001", "firstParty": "2901", "secondParty": "2001", "thirdParty": "2003"}, "2001")
	if st.TalkState != TalkThreeWay || st.Owner != "2001" || st.FirstParty != "2901" ||
		st.SecondParty != "2001" || st.ThirdParty != "2003" {
		t.Fatalf("three_way_started 后状态不符：%+v", st)
	}
	// 名称字段（协议 §16）：每个号码的 xxxName 都要解析，并按号码查得到
	applyMSPEvent(&st, "three_way_started", map[string]any{
		"owner": "2001", "ownerName": "张三",
		"firstParty": "2002", "firstPartyName": "李四",
		"secondParty": "1381003", "thirdParty": "2003", "thirdPartyName": "王五"}, "2001")
	if got := st.NameOf("2002"); got != "李四" {
		t.Fatalf("NameOf(2002) 应为 李四，实际 %q", got)
	}
	if got := st.NameOf("2003"); got != "王五" {
		t.Fatalf("NameOf(2003) 应为 王五，实际 %q", got)
	}
	// 外线号码没有名称：返回空串（调用方回退通信录/号码本身）
	if got := st.NameOf("1381003"); got != "" {
		t.Fatalf("外线号码不应有名称，实际 %q", got)
	}
	if got := st.NameOf(""); got != "" {
		t.Fatalf("空号码应返回空串，实际 %q", got)
	}
	applyMSPEvent(&st, "three_way_finished", nil, "2001")
	if st.TalkState != TalkTwoParty {
		t.Fatalf("three_way_finished 后应回到双方通话：%+v", st)
	}

	// 监听
	applyMSPEvent(&st, "monitor_started", map[string]any{"monitor": "2001", "target": "2002"}, "2001")
	if st.TalkState != TalkMonitor || st.Monitor != "2001" || st.Target != "2002" || st.MonitorPhase != "listening" {
		t.Fatalf("monitor_started 后状态不符：%+v", st)
	}
	applyMSPEvent(&st, "monitor_whisper", nil, "2001")
	if st.TalkState != TalkMonitor || st.MonitorPhase != "whisper" {
		t.Fatalf("monitor_whisper 后应为耳语：%+v", st)
	}
	applyMSPEvent(&st, "monitor_intercept", nil, "2001")
	if st.TalkState != TalkTwoParty {
		t.Fatalf("monitor_intercept 后应回到双方通话：%+v", st)
	}

	// 会议
	applyMSPEvent(&st, "conference_started", map[string]any{"confId": "c1", "owner": "2001"}, "2001")
	if st.TalkState != TalkConference || st.ConfID != "c1" || st.Owner != "2001" {
		t.Fatalf("conference_started 后状态不符：%+v", st)
	}
	applyMSPEvent(&st, "conference_members_changed", map[string]any{
		"confId": "c1", "owner": "2001",
		"members": []any{
			map[string]any{"telNo": "2001", "state": "talking"},
			map[string]any{"telNo": "2002", "state": "dialing", "skillGroup": "110"},
		},
	}, "2001")
	if st.TalkState != TalkConference || len(st.Members) != 2 {
		t.Fatalf("conference_members_changed 后成员列表不符：%+v", st)
	}
	if st.Members[0].TelNo != "2001" || st.Members[1].State != "dialing" || st.Members[1].SkillGroup != "110" {
		t.Fatalf("会议成员解析不符：%+v", st.Members)
	}
	applyMSPEvent(&st, "conference_finished", nil, "2001")
	if st.TalkState != TalkTwoParty || len(st.Members) != 0 {
		t.Fatalf("conference_finished 后应回到双方通话：%+v", st)
	}

	// 未识别事件忽略：不改变当前状态
	st = CTIState{TalkState: TalkConsult, Consultee: "2002"}
	applyMSPEvent(&st, "unknown_event", nil, "2001")
	if st.TalkState != TalkConsult {
		t.Fatalf("未识别事件不应改变状态：%+v", st)
	}
}

// TestConsultStartedOtherParty CONSULT_STARTED 现在也发给原通话双方与被咨询方
// （协议 §16.2）：**非咨询发起方**保持双方通话面板，只置 ConsultInProgress
// （界面据此隐藏咨询/转移/会议入口），CONSULT_FINISHED 后恢复；
// 咨询发起方仍照旧进入咨询面板。
func TestConsultStartedOtherParty(t *testing.T) {
	body := map[string]any{"consulter": "2001", "consultee": "2002"}

	st := CTIState{}
	applyMSPEvent(&st, "consult_started", body, "1381003") // 原通话的对端
	if st.TalkState != TalkTwoParty {
		t.Fatalf("非咨询方不应切换面板，实际 %q", st.TalkState)
	}
	if !st.ConsultInProgress {
		t.Fatal("非咨询方应标记 ConsultInProgress（隐藏话务入口）")
	}
	applyMSPEvent(&st, "consult_finished", nil, "1381003")
	if st.TalkState != TalkTwoParty || st.ConsultInProgress {
		t.Fatalf("咨询结束后应恢复话务入口：%+v", st)
	}

	st2 := CTIState{}
	applyMSPEvent(&st2, "consult_started", body, "2001") // 咨询发起方
	if st2.TalkState != TalkConsult || st2.ConsultInProgress {
		t.Fatalf("咨询方应进入咨询面板：%+v", st2)
	}
}

// ctiHangupTel 只关心 Hangup 的 Telephony 桩（其余方法用嵌入的 nil 接口，
// 测试里不会被调用）。
type ctiHangupTel struct {
	Telephony
	ch chan string
}

func (c *ctiHangupTel) Hangup(callID string) error {
	c.ch <- callID
	return nil
}

// TestConsultTransfer 咨询转接完成（MSP_EVENT_CONSULT_TRANSFER，协议 §16.2）：
// 被咨询方（本机）接管原通话——面板回到双方通话、对端换成原通话的另一方，
// 且这通不再是「咨询腿」（话务入口按钮恢复显示）。
func TestConsultTransfer(t *testing.T) {
	cfg := config.Default()
	cfg.Account.Account = "2002" // 本机分机：example 里 2002 是被咨询方
	app := New(cfg, nil, NewBus())
	// 咨询腿呼入（INVITE 的 Call-Info call-type=consult）
	app.OnIncoming("c1", IncomingCall{Remote: "2001", RemoteName: "张三", Consult: true})
	app.OnEstablished("c1")

	app.OnMSPEvent("consult_transfer", map[string]any{
		"callId":     "c2",
		"callerNo":   "1381003",
		"callerName": "王五",
		"calledNo":   "2002",
		"calledName": "李四",
		"transferor": "2001",
	})

	c := app.getCall("c1")
	if c == nil {
		t.Fatal("通话不存在")
	}
	if c.CTI.TalkState != TalkTwoParty {
		t.Fatalf("转接后应回到双方通话，实际 %q", c.CTI.TalkState)
	}
	if c.Remote != "1381003" {
		t.Fatalf("对端号码应换成原通话的另一方 1381003，实际 %q", c.Remote)
	}
	if c.PeerName != "王五" || c.RemoteName != "王五" {
		t.Fatalf("对端名称应更新为协议给的 王五，实际 peer=%q remote=%q", c.PeerName, c.RemoteName)
	}
	if c.Consult {
		t.Fatal("转接完成后不应再是咨询腿（话务入口按钮要恢复显示）")
	}

	// 本机是 callerNo 时，对端取 calledNo
	cfg2 := config.Default()
	cfg2.Account.Account = "3001"
	app2 := New(cfg2, nil, NewBus())
	app2.OnIncoming("d1", IncomingCall{Remote: "2001", Consult: true})
	app2.OnEstablished("d1")
	app2.OnMSPEvent("consult_transfer", map[string]any{
		"callerNo": "3001", "calledNo": "1390000", "calledName": "赵六",
	})
	if c2 := app2.getCall("d1"); c2 == nil || c2.Remote != "1390000" || c2.PeerName != "赵六" {
		t.Fatalf("本机是主叫时应取被叫为对端，实际 %+v", c2)
	}
}

// TestConsultTransferTransferorHangsUp 转移方收到 MSP_EVENT_CONSULT_TRANSFER：
// **立即挂断本通话**结束话务（转移完成后由服务端把原通话接到被咨询方）。
func TestConsultTransferTransferorHangsUp(t *testing.T) {
	cfg := config.Default()
	cfg.Account.Account = "2001" // 本机是转移方（transferor）
	tel := &ctiHangupTel{ch: make(chan string, 1)}
	app := New(cfg, tel, NewBus())
	app.OnIncoming("c1", IncomingCall{Remote: "1381003"})
	app.OnEstablished("c1")
	app.OnMSPEvent("consult_started", map[string]any{"consulter": "2001", "consultee": "2002"})

	app.OnMSPEvent("consult_transfer", map[string]any{
		"callId":         "c2",
		"callerNo":       "1381003",
		"calledNo":       "2002",
		"calledName":     "李四",
		"transferor":     "2001",
		"transferorName": "张三",
	})

	select {
	case id := <-tel.ch:
		if id != "c1" {
			t.Fatalf("应挂断当前通话 c1，实际 %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("转移方收到 CONSULT_TRANSFER 后应立即挂机结束话务")
	}
	// 转移方不接管原通话：对端号码保持原样
	if c := app.getCall("c1"); c != nil && c.Remote != "1381003" {
		t.Fatalf("转移方不应改对端号码，实际 %q", c.Remote)
	}
}

// TestThreeWayFinishedConvergesPeer 三方结束（协议 §16.3）：FINISHED 的
// callerNo/calledNo/agent 用来确定**保留的对端**（用户口径）：
//   - 有 agent 且 agent = 本机 → 对端是 caller；
//   - caller = 本机 → 对端是 called；
//   - called = 本机 → 对端是 caller。
//
// 对端换了时沿用三方面板里该方显示名；对端没变则号码与名称都保持原样。
func TestThreeWayFinishedConvergesPeer(t *testing.T) {
	cfg := config.Default()
	cfg.Account.Account = "2002" // 本机是第三方（被咨询方）
	app := New(cfg, nil, NewBus())
	app.OnIncoming("c1", IncomingCall{Remote: "2001", PeerName: "张三", Consult: true})
	app.OnEstablished("c1")
	app.OnMSPEvent("three_way_started", map[string]any{
		"owner": "2001", "firstParty": "2001", "secondParty": "1381003", "thirdParty": "2002",
	})
	if c := app.getCall("c1"); c == nil || c.CTI.TalkState != TalkThreeWay {
		t.Fatalf("应进入三方：%+v", c)
	}

	// 发起方 2001 退出三方：剩余 1381003 与 2002
	app.OnMSPEvent("three_way_finished", map[string]any{
		"callerNo": "1381003", "calledNo": "2002",
	})
	c := app.getCall("c1")
	if c.CTI.TalkState != TalkTwoParty {
		t.Fatalf("三方结束后应回到双方通话：%+v", c.CTI)
	}
	if c.Remote != "1381003" {
		t.Fatalf("对端应收敛为剩余的另一方 1381003，实际 %q", c.Remote)
	}
	if c.PeerName != "" || c.RemoteName != "" {
		t.Fatalf("换了对端后应清掉旧名称，实际 peer=%q remote=%q", c.PeerName, c.RemoteName)
	}
	if c.Consult {
		t.Fatal("三方结束后不应再是咨询腿（坐席模式下咨询/转移/会议入口要恢复）")
	}

	// 对端还在剩余集合里的情形：号码不变（caller = 本机 → 对端取 called）
	cfg2 := config.Default()
	cfg2.Account.Account = "2001"
	app2 := New(cfg2, nil, NewBus())
	app2.OnIncoming("d1", IncomingCall{Remote: "1381003", PeerName: "外线"})
	app2.OnEstablished("d1")
	app2.OnMSPEvent("three_way_started", map[string]any{
		"owner": "2001", "firstParty": "2001", "secondParty": "1381003", "thirdParty": "2002",
	})
	app2.OnMSPEvent("three_way_finished", map[string]any{
		"callerNo": "2001", "calledNo": "1381003",
	})
	if c2 := app2.getCall("d1"); c2 == nil || c2.Remote != "1381003" || c2.PeerName != "外线" {
		t.Fatalf("对端还在剩余集合里时不应改动，实际 %+v", c2)
	}

	// agent = 本机：对端取 caller；名称沿用三方面板里这一方的显示名
	cfg3 := config.Default()
	cfg3.Account.Account = "2002"
	app3 := New(cfg3, nil, NewBus())
	app3.OnIncoming("e1", IncomingCall{Remote: "1381003", PeerName: "外线"})
	app3.OnEstablished("e1")
	app3.OnMSPEvent("three_way_started", map[string]any{
		"owner": "2001", "firstParty": "2001", "firstPartyName": "张三",
		"secondParty": "1381003", "thirdParty": "2002",
	})
	app3.OnMSPEvent("three_way_finished", map[string]any{
		"callerNo": "2001", "calledNo": "1381003", "agent": "2002",
	})
	if c3 := app3.getCall("e1"); c3 == nil || c3.Remote != "2001" || c3.PeerName != "张三" {
		t.Fatalf("agent 是本机时对端应为 caller=2001（名称沿用 张三），实际 %+v", c3)
	}
}

// TestConferenceFinishedHangsUp 会议结束等同于收到 BYE：MSP_EVENT_CONFERENCE_FINISHED
// 必须把本端通话挂断（通话面板随之关闭），两个事件收到哪个都关面板。
func TestConferenceFinishedHangsUp(t *testing.T) {
	tel := &ctiHangupTel{ch: make(chan string, 1)}
	app := New(config.Default(), tel, NewBus())
	app.OnIncoming("c1", IncomingCall{Remote: "1001", RemoteName: "", HasVideo: false})
	app.OnEstablished("c1")

	app.OnMSPEvent("conference_started", map[string]any{"confId": "c1", "owner": "1001"})
	app.OnMSPEvent("conference_finished", nil)

	select {
	case id := <-tel.ch:
		if id != "c1" {
			t.Fatalf("应挂断会议这通电话 c1，实际 %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("会议结束后应挂断通话（等同于收到 BYE）")
	}
}

// TestConferenceMembersChangedReplacesList 会议成员列表事件（协议 §16.5；
// 实际下发偶见单数形式 CONFERENCE_MEMBER_CHANGED）**整份覆盖**本地列表：
// 多出来的条目消失、缺少的补齐、状态重设，面板据此整块重画（不做增量）。
func TestConferenceMembersChangedReplacesList(t *testing.T) {
	st := &CTIState{}
	members := func(items ...map[string]any) map[string]any {
		list := make([]any, 0, len(items))
		for _, it := range items {
			list = append(list, it)
		}
		return map[string]any{"confId": "c1", "owner": "2001", "members": list}
	}
	m := func(no, state string) map[string]any {
		return map[string]any{"telNo": no, "state": state}
	}

	// 复数形式：三个成员
	applyMSPEvent(st, "conference_members_changed",
		members(m("2001", "talking"), m("2002", "dialing"), m("2003", "talking")), "2001")
	if got := len(st.Members); got != 3 {
		t.Fatalf("应载入 3 个成员，实际 %d：%+v", got, st.Members)
	}
	if st.Members[1].TelNo != "2002" || st.Members[1].State != "dialing" {
		t.Fatalf("成员号码/状态未载入：%+v", st.Members[1])
	}

	// 单数形式：2002 离开、2003 变为静音
	applyMSPEvent(st, "conference_member_changed",
		members(m("2001", "talking"), m("2003", "talking+muted")), "2001")
	if got := len(st.Members); got != 2 {
		t.Fatalf("多余成员应被移除（剩 2 个），实际 %d：%+v", got, st.Members)
	}
	if st.Members[1].TelNo != "2003" || st.Members[1].State != "talking+muted" {
		t.Fatalf("成员状态应重设，实际 %+v", st.Members[1])
	}

	// 空列表：全部清空（缺少的不再保留）
	applyMSPEvent(st, "conference_members_changed", members(), "2001")
	if got := len(st.Members); got != 0 {
		t.Fatalf("空成员列表应清空，实际 %d：%+v", got, st.Members)
	}
}

// TestCallStatus 心跳 status 的取值口径（协议约定的字符串）：
// free / dialing（呼出） / ringing（呼入） / talking / holding（保持）。
func TestCallStatus(t *testing.T) {
	app := New(config.Default(), nil, NewBus())
	if got := app.callStatus(); got != "free" {
		t.Fatalf("无通话应为 free，实际 %q", got)
	}
	// 呼出（含 180 回铃）：dialing
	app.upsertCall(&Call{ID: "o1", Direction: "outgoing", Remote: "1002", State: CallRinging})
	if got := app.callStatus(); got != "dialing" {
		t.Fatalf("呼出振铃应为 dialing，实际 %q", got)
	}
	// 接通：talking
	app.OnEstablished("o1")
	if got := app.callStatus(); got != "talking" {
		t.Fatalf("接通后应为 talking，实际 %q", got)
	}
	// 保持：holding
	app.updateCall("o1", func(c *Call) { c.Muted = true })
	if got := app.callStatus(); got != "holding" {
		t.Fatalf("保持中应为 holding，实际 %q", got)
	}
	// 结束：idle
	app.OnTerminated("o1", "remote_hangup")
	if got := app.callStatus(); got != "free" {
		t.Fatalf("通话结束后应为 free，实际 %q", got)
	}
	// 呼入振铃：ringing
	app.OnIncoming("c1", IncomingCall{Remote: "1001"})
	if got := app.callStatus(); got != "ringing" {
		t.Fatalf("来电振铃应为 ringing，实际 %q", got)
	}
}

// TestMonitorCallerCalled 监听的主被叫（协议 §16.4）：MONITOR_STARTED 携带被监听话务
// 的 callerNo/calledNo；WHISPER/INTERCEPT 不重复携带，必须沿用 STARTED 的值。
func TestMonitorCallerCalled(t *testing.T) {
	st := &CTIState{}
	applyMSPEvent(st, "monitor_started", map[string]any{
		"monitor": "2001", "monitorName": "张三",
		"target": "2002", "targetName": "李四",
		"callerNo": "1381003", "calledNo": "2002", "calledName": "李四",
	}, "2001")
	if st.CallerNo != "1381003" || st.CalledNo != "2002" || st.CalledName != "李四" {
		t.Fatalf("主被叫未解析：%+v", st)
	}
	if got := st.NameOf("2002"); got != "李四" {
		t.Fatalf("NameOf(2002) 应为李四，实际 %q", got)
	}
	applyMSPEvent(st, "monitor_whisper", map[string]any{
		"monitor": "2001", "target": "2002",
	}, "2001")
	if st.CallerNo != "1381003" || st.CalledNo != "2002" {
		t.Fatalf("耳语后主被叫应沿用 STARTED 的值：%+v", st)
	}
	if st.MonitorPhase != "whisper" {
		t.Fatalf("耳语阶段应为 whisper，实际 %q", st.MonitorPhase)
	}
}
