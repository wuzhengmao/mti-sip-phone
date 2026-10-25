package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"

	"mti-sip-phone/internal/core"
)

// listRowActions 列表行右侧的操作按钮（在线座席 / 通信录 / 通话记录共用）。
//
// 在线座席行（isAgent）按**座席自身状态**显示（用户规则）：
//   - 非通话状态：座席空闲 → 呼叫、视频呼叫；座席**通话中** → 监听（仅班长模式）；
//     座席**振铃中** → 代接（普通模式、班长模式都有）；其余状态不显示；
//   - 咨询复用（主窗体拨号盘 = 发起咨询）：座席空闲 → 咨询、三方通话；
//   - 转移复用：座席空闲 → 直接转移、咨询转移；
//   - 会议复用：座席空闲 → 加入会议。
//
// 通信录/通话记录行维持原规则：空闲 → 呼叫/视频；通话中且没进入拨号复用 →
// 不显示任何按钮；复用模式下按对应动作显示（这些行没有座席状态，不看状态）。
//
// 点击与"主窗体输入号码后点动作按钮"完全一致：走 runCTI（在途互斥、按钮转圈、
// 失败提示），指令发出成功后再记入「最近呼叫」下拉。
//
// target 为行的号码/坐席号；call/video 是空闲时的拨号动作（video 为 nil 表示
// 视频不可用、不显示视频按钮）；agentStatus 为座席当前状态（空 = 非坐席行）。
func (a *App) listRowActions(target string, size, radius, videoIconSize float32,
	call, video func(), isAgent bool, agentStatus string) fyne.CanvasObject {

	empty := func() fyne.CanvasObject { return container.NewWithoutLayout() }

	cti := func(icon fyne.Resource, iconSize float32, tip, action string, params map[string]any) *compactButton {
		var b *compactButton
		b = newCompactButton(icon, colorHoldBlue, size, func() {
			if err := a.runCTI(b, action, params); err == nil {
				a.core.AddRecentDial(target) // 与主窗体拨号复用一致
			}
		})
		b.SetCornerRadius(radius)
		b.SetIconSize(iconSize)
		b.setTip(a.mainTip, tip)
		return b
	}

	// 复用动作只对**空闲**座席有意义（对方忙/振铃时咨询、转移、加会都做不了）
	agentFree := !isAgent || strings.EqualFold(agentStatus, "free")

	switch a.ctiDialMode {
	case "consult":
		if !agentFree {
			return empty()
		}
		return container.NewHBox(
			cti(IconCTIConsult, 14, "咨询", "consult", map[string]any{"target": target}),
			cti(IconCTIThreeWay, 16, "三方通话", "consultThreeWay", map[string]any{"target": target}),
		)
	case "transfer":
		if !agentFree {
			return empty()
		}
		return container.NewHBox(
			cti(IconCTITransfer, 14, "直接转移", "transfer", map[string]any{"target": target}),
			cti(IconCTIConsultTransfer, 14, "咨询转移", "consultTransfer", map[string]any{"target": target}),
		)
	case "conference":
		if !agentFree {
			return empty()
		}
		return container.NewHBox(
			cti(IconCTIAddMember, 14, "加入会议", "addConferenceMembers",
				map[string]any{"members": []string{target}}),
		)
	}

	// 通话进行中（且没在拨号复用）：不显示操作按钮
	if a.activeCallID != "" || a.uiState != uiIdle {
		return empty()
	}

	if isAgent {
		switch strings.ToLower(agentStatus) {
		case "free":
			// 空闲：落到下面统一给出呼叫/视频呼叫
		case "talking":
			// 通话中：只有班长（监听）能旁听
			if a.agentMode != core.AgentModeMonitor {
				return empty()
			}
			return container.NewHBox(
				cti(IconCTIMonitor, 16, "监听", "monitor", map[string]any{"target": target}),
			)
		case "ringing":
			// 振铃中：普通/班长模式都可以代接
			return container.NewHBox(
				cti(IconCTITakeover, 16, "代接", "takeover", map[string]any{"target": target}),
			)
		default: // 呼叫中 / 置忙 / 离席 / 示忙 / 话机未连接 / 离线…
			return empty()
		}
	}

	var btns []fyne.CanvasObject
	if call != nil {
		b := newCompactButton(IconPhoneCallWhite, colorHoldBlue, size, call)
		b.SetCornerRadius(radius)
		btns = append(btns, b)
	}
	if video != nil {
		b := newCompactButton(IconVideoCallWhite, colorHoldBlue, size, video)
		b.SetCornerRadius(radius)
		b.SetIconSize(videoIconSize)
		btns = append(btns, b)
	}
	return container.NewHBox(btns...)
}
