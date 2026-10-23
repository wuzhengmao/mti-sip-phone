package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

// TestParseAgentEntries listAgents 的 MSP_RESULT.data 解析：字段照抄、按座席号
// 排序、没有座席号的项丢弃、非数组返回 nil。
func TestParseAgentEntries(t *testing.T) {
	data := []any{
		map[string]any{"agent": "2002", "displayName": "李四", "acd": "110:1",
			"agentMode": "normal", "agentStatus": "free"},
		map[string]any{"agent": "2001", "displayName": "张三", "agentStatus": "talking"},
		map[string]any{"displayName": "没有座席号"},
	}
	got := parseAgentEntries(data)
	if len(got) != 2 {
		t.Fatalf("应解析出 2 个坐席，实际 %d：%+v", len(got), got)
	}
	if got[0].No != "2001" || got[1].No != "2002" {
		t.Fatalf("应按座席号排序，实际 %q, %q", got[0].No, got[1].No)
	}
	if got[1].ACD != "110:1" || got[1].DisplayName != "李四" || got[1].Status != "free" {
		t.Fatalf("字段未正确解析：%+v", got[1])
	}
	if parseAgentEntries(nil) != nil {
		t.Fatal("非数组结果应返回 nil")
	}
}

// TestAgentStatusColor 座席状态配色：绿=空闲、黄=呼叫/振铃/通话中、
// 红=置忙或异常（含未知状态）。
func TestAgentStatusColor(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"free", "green"},
		{"dialing", "yellow"},
		{"ringing", "yellow"},
		{"talking", "yellow"},
		{"busy", "red"},
		{"free_offhook", "red"},
		{"offline", "red"},
		{"", "red"},
	}
	for _, c := range cases {
		got := agentStatusColor(c.status)
		switch c.want {
		case "green":
			if got != colorSuccessGreen {
				t.Errorf("状态 %q 应为绿色，实际 %v", c.status, got)
			}
		case "yellow":
			if got != colorHoldYellow {
				t.Errorf("状态 %q 应为黄色，实际 %v", c.status, got)
			}
		default:
			if got != colorDangerRed {
				t.Errorf("状态 %q 应为红色，实际 %v", c.status, got)
			}
		}
	}
}

// TestListRowActionsByState 列表行右侧按钮随状态变化（用户定的规则）：
//   - 在线座席行按**座席自身状态**：空闲=呼叫/视频、通话中=监听（仅班长模式）、
//     振铃中=代接（两种模式）、其余状态不显示；
//   - 咨询/转移复用只有空闲座席可用（咨询+三方 / 直接转移+咨询转移）；
//   - 会议复用=加入会议（仅空闲）；
//   - 本机通话中全部隐藏。
func TestListRowActionsByState(t *testing.T) {
	test.NewApp()
	a := &App{}
	call := func() {}
	count := func(obj fyne.CanvasObject) int {
		c, ok := obj.(*fyne.Container)
		if !ok {
			t.Fatalf("应返回容器，实际 %T", obj)
		}
		return len(c.Objects)
	}
	agent := func(status string, video bool) int {
		var videoAct func()
		if video {
			videoAct = call
		}
		return count(a.listRowActions("2001", 24, 2, 14, call, videoAct, true, status))
	}

	// 通信录 / 通话记录行（无座席状态）：空闲=呼叫+视频，视频不可用只有呼叫
	if n := count(a.listRowActions("2001", 24, 2, 14, call, call, false, "")); n != 2 {
		t.Fatalf("空闲时应有 2 个按钮（呼叫/视频），实际 %d", n)
	}
	if n := count(a.listRowActions("2001", 24, 2, 14, call, nil, false, "")); n != 1 {
		t.Fatalf("视频不可用时应只有 1 个按钮，实际 %d", n)
	}

	// 在线座席 - 空闲：普通/班长模式都是呼叫+视频（不再多监听）
	a.agentMode = core.AgentModeNormal
	if n := agent("free", true); n != 2 {
		t.Fatalf("空闲座席应有 2 个按钮（呼叫/视频），实际 %d", n)
	}
	a.agentMode = core.AgentModeMonitor
	if n := agent("free", true); n != 2 {
		t.Fatalf("班长模式下空闲座席也应是 2 个按钮（呼叫/视频），实际 %d", n)
	}

	// 在线座席 - 通话中：只有班长模式给监听
	if n := agent("talking", true); n != 1 {
		t.Fatalf("班长模式下通话中座席应有 1 个按钮（监听），实际 %d", n)
	}
	a.agentMode = core.AgentModeNormal
	if n := agent("talking", true); n != 0 {
		t.Fatalf("普通模式下通话中座席不应有按钮，实际 %d", n)
	}

	// 在线座席 - 振铃中：普通模式与班长模式都给代接
	if n := agent("ringing", true); n != 1 {
		t.Fatalf("普通模式下振铃中座席应有 1 个按钮（代接），实际 %d", n)
	}
	a.agentMode = core.AgentModeMonitor
	if n := agent("ringing", true); n != 1 {
		t.Fatalf("班长模式下振铃中座席应有 1 个按钮（代接），实际 %d", n)
	}

	// 在线座席 - 其它状态（呼叫中/置忙/离线…）：不显示
	a.agentMode = core.AgentModeNormal
	for _, st := range []string{"dialing", "busy", "offline", "miss_phone", ""} {
		if n := agent(st, true); n != 0 {
			t.Fatalf("状态 %q 的座席不应有按钮，实际 %d", st, n)
		}
	}

	// 本机通话中（未进入拨号复用）：全部隐藏
	a.activeCallID = "c1"
	a.uiState = uiInCall
	if n := agent("free", true); n != 0 {
		t.Fatalf("通话中不应显示操作按钮，实际 %d", n)
	}
	if n := count(a.listRowActions("2001", 24, 2, 14, call, call, false, "")); n != 0 {
		t.Fatalf("通话中的通信录行不应显示操作按钮，实际 %d", n)
	}

	// 咨询复用：只有空闲座席 → 咨询 + 三方
	a.ctiDialMode = "consult"
	if n := agent("free", true); n != 2 {
		t.Fatalf("咨询复用下空闲座席应有 2 个按钮，实际 %d", n)
	}
	if n := agent("talking", true); n != 0 {
		t.Fatalf("咨询复用下非空闲座席不应有按钮，实际 %d", n)
	}
	// 转移复用：只有空闲座席 → 直接转移 + 咨询转移
	a.ctiDialMode = "transfer"
	if n := agent("free", true); n != 2 {
		t.Fatalf("转移复用下空闲座席应有 2 个按钮，实际 %d", n)
	}
	if n := agent("ringing", true); n != 0 {
		t.Fatalf("转移复用下非空闲座席不应有按钮，实际 %d", n)
	}
	// 会议复用：只有空闲座席 → 加入会议
	a.ctiDialMode = "conference"
	if n := agent("free", true); n != 1 {
		t.Fatalf("会议复用下空闲座席应有 1 个按钮，实际 %d", n)
	}
	if n := agent("busy", true); n != 0 {
		t.Fatalf("会议复用下非空闲座席不应有按钮，实际 %d", n)
	}
}

// TestAgentsFilterSelf 在线座席列表过滤掉本机座席（用户要求）：本机分机号
// 即使在 listAgents 结果里也不显示。
func TestAgentsFilterSelf(t *testing.T) {
	cfg := config.Default()
	cfg.Account.Account = "2001"
	c := core.New(cfg, nil, core.NewBus())
	c.OnRegisterState(core.RegRegistered, "") // 注册快照里才有本机分机号
	st := &agentsState{a: &App{core: c}}
	st.all = []agentEntry{{No: "2001", Status: "free"}, {No: "2002", Status: "talking"}}
	st.applyFilter()
	if len(st.rows) != 1 || st.rows[0].No != "2002" {
		t.Fatalf("本机座席应被过滤，实际 %+v", st.rows)
	}
}

// TestSkillGroups 技能组拆分（tag 展示用）：去空白、去空项、保持顺序。
func TestSkillGroups(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"110:1", []string{"110:1"}},
		{"110:1,122:1", []string{"110:1", "122:1"}},
		{" 110:1 , 122:1 ", []string{"110:1", "122:1"}},
	}
	for _, c := range cases {
		got := skillGroups(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("skillGroups(%q) = %v，期望 %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("skillGroups(%q) = %v，期望 %v", c.in, got, c.want)
			}
		}
	}
}
