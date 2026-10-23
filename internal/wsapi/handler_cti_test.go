package wsapi

// CTI 网页入口（cti.action / cti.listAgents）的回归测试：动作与参数校验、
// 等待 MSP_RESULT 的同步回包、失败结果与非坐席模式的错误码。

import (
	"encoding/json"
	"strings"
	"testing"

	"mti-sip-phone/internal/core"
)

// TestCTIListAgents cti.listAgents：等 MSP_RESULT 后把 data 归一化成
// {agent,displayName,acd,agentMode,agentStatus} 并按座席号排序。
func TestCTIListAgents(t *testing.T) {
	ts, app, tel := newTestServerAll(t)
	app.OnMSPEvent("agent_online", map[string]any{"displayName": "张三", "agentMode": "monitor"})
	tel.setCTIReply(core.CTIResult{Seq: "1", Success: true, Data: []any{
		map[string]any{"agent": "2002", "displayName": "李四", "acd": "110:1", "agentStatus": "free"},
		map[string]any{"agent": "2001", "displayName": "张三", "agentStatus": "talking"},
		map[string]any{"displayName": "没有座席号"},
	}})
	conn := dialWS(t, ts.URL)
	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: MethodCTIListAgents})
	resp := waitResponse(t, conn)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("cti.listAgents 应成功，实际 %+v", resp)
	}
	data, _ := resp.Data.(map[string]any)
	list, _ := data["agents"].([]any)
	if len(list) != 2 {
		t.Fatalf("应归一化出 2 个座席，实际 %+v", data)
	}
	first, _ := list[0].(map[string]any)
	if first["agent"] != "2001" || first["agentStatus"] != "talking" {
		t.Fatalf("座席应按座席号排序且字段照抄，实际 %+v", first)
	}
}

// TestCTIAction 带 target / members 的动作参数组装，以及结果回包。
func TestCTIAction(t *testing.T) {
	ts, app, tel := newTestServerAll(t)
	app.OnMSPEvent("agent_online", map[string]any{"displayName": "张三", "agentMode": "normal"})
	tel.setCTIReply(core.CTIResult{Seq: "1", Success: true})
	conn := dialWS(t, ts.URL)

	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: MethodCTIAction,
		Params: json.RawMessage(`{"action":"takeover","target":"2002"}`)})
	resp := waitResponse(t, conn)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("takeover 应成功，实际 %+v", resp)
	}
	action, params := tel.lastCTI()
	if action != "takeover" || params["target"] != "2002" {
		t.Fatalf("应当下发 takeover(target=2002)，实际 action=%q params=%+v", action, params)
	}

	// addConferenceMembers 允许空成员（= 直接把当前两方通话转成会议，桌面 UI 行为）
	sendRequest(t, conn, Message{ID: json.RawMessage("2"), Method: MethodCTIAction,
		Params: json.RawMessage(`{"action":"addConferenceMembers","members":[]}`)})
	resp = waitResponse(t, conn)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("空成员的 addConferenceMembers 应成功，实际 %+v", resp)
	}
	if action, params := tel.lastCTI(); action != "addConferenceMembers" {
		t.Fatalf("应下发 addConferenceMembers，实际 action=%q", action)
	} else if list, _ := params["members"].([]string); len(list) != 0 {
		t.Fatalf("空成员应原样下发空列表，实际 %+v", params)
	}

	// removeConferenceMembers 不允许空成员
	sendRequest(t, conn, Message{ID: json.RawMessage("23"), Method: MethodCTIAction,
		Params: json.RawMessage(`{"action":"removeConferenceMembers","members":["", "  "]}`)})
	resp = waitResponse(t, conn)
	if resp.OK == nil || *resp.OK || resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("removeConferenceMembers 空 members 应报 invalid_params，实际 %+v", resp)
	}

	// consultThreeWay 的 target 可选（缺省用当前咨询，协议 §9.2）
	sendRequest(t, conn, Message{ID: json.RawMessage("21"), Method: MethodCTIAction,
		Params: json.RawMessage(`{"action":"consultThreeWay"}`)})
	resp = waitResponse(t, conn)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("consultThreeWay 不带 target 应成功，实际 %+v", resp)
	}
	if action, params := tel.lastCTI(); action != "consultThreeWay" || params != nil {
		t.Fatalf("consultThreeWay 应不带参数，实际 action=%q params=%+v", action, params)
	}

	// 会议成员会控：member 参数
	sendRequest(t, conn, Message{ID: json.RawMessage("22"), Method: MethodCTIAction,
		Params: json.RawMessage(`{"action":"muteConferenceMember","member":"2002"}`)})
	resp = waitResponse(t, conn)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("muteConferenceMember 应成功，实际 %+v", resp)
	}
	if action, params := tel.lastCTI(); action != "muteConferenceMember" || params["member"] != "2002" {
		t.Fatalf("会议会控应下发 member=2002，实际 action=%q params=%+v", action, params)
	}

	// 未知动作
	sendRequest(t, conn, Message{ID: json.RawMessage("3"), Method: MethodCTIAction,
		Params: json.RawMessage(`{"action":"nope"}`)})
	resp = waitResponse(t, conn)
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("未知动作应报 invalid_params，实际 %+v", resp)
	}
}

// TestCTIActionNotAgent 非坐席模式：拒绝话务动作（桌面端同样只对坐席显示入口）。
func TestCTIActionNotAgent(t *testing.T) {
	ts, _, _ := newTestServerAll(t)
	conn := dialWS(t, ts.URL)
	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: MethodCTIAction,
		Params: json.RawMessage(`{"action":"setBusy"}`)})
	resp := waitResponse(t, conn)
	if resp.Error == nil || resp.Error.Code != CodeCTINotAgent {
		t.Fatalf("非坐席模式应报 cti_not_agent，实际 %+v", resp)
	}
}

// TestCTIActionFailed CTI 平台回失败结果：错误码 cti_failed，消息带平台错误码。
func TestCTIActionFailed(t *testing.T) {
	ts, app, tel := newTestServerAll(t)
	app.OnMSPEvent("agent_online", map[string]any{"agentMode": "normal"})
	tel.setCTIReply(core.CTIResult{Seq: "1", Success: false, Code: "AGENT_NOT_IN_CALL",
		Message: "坐席不在通话中"})
	conn := dialWS(t, ts.URL)
	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: MethodCTIAction,
		Params: json.RawMessage(`{"action":"consult","target":"2002"}`)})
	resp := waitResponse(t, conn)
	if resp.Error == nil || resp.Error.Code != CodeCTIFailed {
		t.Fatalf("应报 cti_failed，实际 %+v", resp)
	}
	if !strings.Contains(resp.Error.Message, "AGENT_NOT_IN_CALL") {
		t.Fatalf("错误信息里应带平台错误码，实际 %q", resp.Error.Message)
	}
}
