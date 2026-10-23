package wsapi

// CTI 反向控制（REQ-ACC-026 / REQ-ACC-035）的网页入口：
//
//	cti.action     {action, target?, members?}  发起一条 MSP_ACTION 指令
//	cti.listAgents {}                           拉取在线座席（listAgents）
//
// 两个方法都**等 MSP_RESULT 回来后同步回包**：core 的 SendCTICommand 只返回
// seq，结果经总线 cti.result 异步到达，这里按 seq 对齐后再回复——网页/SDK
// 因此可以像普通请求一样 await，不必自己维护 seq 与超时。结果事件照旧会广播
// 给所有连接（其它页面/桌面界面同步刷新）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ctiResultTimeout 等 MSP_RESULT 的上限。信令层本身 2 秒判超时（并会回一条
// TIMEOUT 结果），这里再留一点余量；两边的超时不会互相打架。
const ctiResultTimeout = 3 * time.Second

// errCTITimeout 连信令层的 TIMEOUT 结果都没等到（理论上级别更低的异常）。
var errCTITimeout = errors.New("CTI 指令超时（未收到 MSP_RESULT）")

// ctiOutcome cti.result 事件里本端关心的字段。
type ctiOutcome struct {
	seq     string
	success bool
	code    string
	message string
	data    any
}

// ctiAgent 在线座席列表的一项（MSP_RESULT.data 的归一化字段）。
type ctiAgent struct {
	Agent       string `json:"agent"`
	DisplayName string `json:"displayName"`
	ACD         string `json:"acd"`
	AgentMode   string `json:"agentMode"`
	AgentStatus string `json:"agentStatus"`
}

// dispatchCTI 分发 cti.* 方法（未认领返回 ok=false）。
func (s *Server) dispatchCTI(ctx context.Context, msg Message) (Message, bool) {
	switch msg.Method {
	case MethodCTIAction:
		var p struct {
			Action  string   `json:"action"`
			Target  string   `json:"target"`
			Members []string `json:"members"`
			Member  string   `json:"member"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误")), true
		}
		action := strings.TrimSpace(p.Action)
		params, err := ctiParams(action, strings.TrimSpace(p.Target), p.Members, strings.TrimSpace(p.Member))
		if err != nil {
			return errMessage(msg.ID, CodeInvalidParams, err), true
		}
		return s.replyCTIAction(ctx, msg.ID, action, params), true

	case MethodCTIListAgents:
		return s.replyCTIListAgents(ctx, msg.ID), true
	}
	return Message{}, false
}

// ctiParams 按动作校验并组装 MSP_ACTION 的参数：
//   - 必带 target：consult / transfer / monitor / takeover / exitThreeWay；
//   - target 可选（缺省用当前咨询）：consultThreeWay / consultTransfer；
//   - 带 members：addConferenceMembers / removeConferenceMembers；
//   - 带 member：会议成员会控（禁音/禁听/禁视频的开关）；
//   - 无参数：finishConsult / finishConference / whisper / intervene / intercept /
//     setBusy / setIdle。
func ctiParams(action, target string, members []string, member string) (map[string]any, error) {
	switch action {
	case "consult", "transfer", "monitor", "takeover", "exitThreeWay":
		if target == "" {
			return nil, fmt.Errorf("动作 %s 需要参数 target", action)
		}
		return map[string]any{"target": target}, nil
	case "consultThreeWay", "consultTransfer":
		if target == "" {
			return nil, nil // 缺省用当前咨询（协议 §9.2）
		}
		return map[string]any{"target": target}, nil
	case "addConferenceMembers":
		// members 可以为空：与桌面 UI 的「会议」按钮一致——直接把当前两方
		// 通话转成会议（空成员），之后再「添加成员」。
		clean := make([]string, 0, len(members))
		for _, m := range members {
			if m = strings.TrimSpace(m); m != "" {
				clean = append(clean, m)
			}
		}
		return map[string]any{"members": clean}, nil
	case "removeConferenceMembers":
		clean := make([]string, 0, len(members))
		for _, m := range members {
			if m = strings.TrimSpace(m); m != "" {
				clean = append(clean, m)
			}
		}
		if len(clean) == 0 {
			return nil, fmt.Errorf("动作 %s 需要参数 members（至少一个号码）", action)
		}
		return map[string]any{"members": clean}, nil
	case "muteConferenceMember", "unmuteConferenceMember",
		"deafConferenceMember", "undeafConferenceMember",
		"vmuteConferenceMember", "unvmuteConferenceMember":
		if member == "" {
			return nil, fmt.Errorf("动作 %s 需要参数 member", action)
		}
		return map[string]any{"member": member}, nil
	case "finishConsult", "finishConference", "whisper", "intervene", "intercept",
		"setBusy", "setIdle":
		return nil, nil
	}
	return nil, fmt.Errorf("不支持的动作: %s", action)
}

// replyCTIAction 发送指令并等结果。
func (s *Server) replyCTIAction(ctx context.Context, id json.RawMessage, action string, params map[string]any) Message {
	// 没有 CTI 后端（agentMode=none，普通 SIP 话机）时这些指令发出去也没人处理：
	// 与桌面端一致，不在坐席模式就直接拒绝，错误提示更明确。
	if !s.app.AgentOnline() {
		return errMessage(id, CodeCTINotAgent, errors.New("当前不在坐席模式，无法执行话务动作"))
	}
	out, err := s.awaitCTI(ctx, action, params)
	if err != nil {
		return errMessage(id, codeOf(err), err)
	}
	if !out.success {
		return errMessage(id, CodeCTIFailed, ctiFailErr(out))
	}
	data := map[string]any{"action": action, "seq": out.seq}
	if out.data != nil {
		data["data"] = out.data
	}
	return okMessage(id, data)
}

// replyCTIListAgents 拉在线座席并把 MSP_RESULT.data 归一化成固定字段。
func (s *Server) replyCTIListAgents(ctx context.Context, id json.RawMessage) Message {
	if !s.app.AgentOnline() {
		return errMessage(id, CodeCTINotAgent, errors.New("当前不在坐席模式，无法获取在线座席"))
	}
	out, err := s.awaitCTI(ctx, "listAgents", nil)
	if err != nil {
		return errMessage(id, codeOf(err), err)
	}
	if !out.success {
		return errMessage(id, CodeCTIFailed, ctiFailErr(out))
	}
	agents := parseCTIAgents(out.data)
	return okMessage(id, map[string]any{"agents": agents, "count": len(agents)})
}

// awaitCTI 发送指令并等待 seq 对应的 cti.result（先订阅总线再发送，避免回执
// 早于订阅到达而丢失）。
func (s *Server) awaitCTI(ctx context.Context, action string, params map[string]any) (ctiOutcome, error) {
	sub := s.app.Bus().Subscribe()
	defer s.app.Bus().Unsubscribe(sub)

	seq, err := s.app.SendCTICommand(action, params)
	if err != nil {
		return ctiOutcome{}, err
	}
	timer := time.NewTimer(ctiResultTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctiOutcome{}, ctx.Err()
		case <-timer.C:
			return ctiOutcome{}, errCTITimeout
		case ev := <-sub:
			if ev.Name != "cti.result" {
				continue
			}
			m, ok := ev.Data.(map[string]any)
			if !ok {
				continue
			}
			if got, _ := m["seq"].(string); got != seq {
				continue // 别的指令的回执
			}
			success, _ := m["success"].(bool)
			code, _ := m["code"].(string)
			msg, _ := m["message"].(string)
			return ctiOutcome{seq: seq, success: success, code: code,
				message: msg, data: m["data"]}, nil
		}
	}
}

// ctiFailErr 把失败的 MSP_RESULT 拼成可读错误（平台的错误码放括号里）。
func ctiFailErr(out ctiOutcome) error {
	msg := out.message
	if msg == "" {
		msg = "CTI 操作失败"
	}
	if out.code != "" {
		msg = fmt.Sprintf("%s（%s）", msg, out.code)
	}
	return errors.New(msg)
}

// parseCTIAgents 解析 MSP_RESULT.data（[]any of map[string]any），按座席号排序。
func parseCTIAgents(data any) []ctiAgent {
	arr, ok := data.([]any)
	if !ok {
		return []ctiAgent{}
	}
	out := make([]ctiAgent, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		a := ctiAgent{
			Agent:       ctiStr(m, "agent"),
			DisplayName: ctiStr(m, "displayName"),
			ACD:         ctiStr(m, "acd"),
			AgentMode:   ctiStr(m, "agentMode"),
			AgentStatus: ctiStr(m, "agentStatus"),
		}
		if a.Agent == "" {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

func ctiStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
