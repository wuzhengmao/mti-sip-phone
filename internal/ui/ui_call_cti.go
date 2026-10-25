package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 通话面板的 CTI 复杂话务渲染（REQ-ACC-026，docs/cti-sip-phone-protocol.md
// 第三部分）：咨询/三方/监听/会议四种话务状态叠加在双方通话之上，由服务端
// MSP_EVENT_* 驱动（core 侧翻译成 Call.CTI 后经 cti.state 事件下发），这里按
// Call.CTI.TalkState 重建面板行与按钮。

// ---- 矢量图标紧凑按钮（CTI 入口按钮：咨询/转移/会议/耳语/拦截…）----
//
// **不用文字/汉字**：Linux 精简环境缺字形时汉字会渲染成方框（与 ★ 同一个坑），
// 统一走 SVG 图标。

func newIconButton(iconRes fyne.Resource, bg color.NRGBA, onTap func()) *compactButton {
	return newPanelButton(iconRes, bg, callPanelButtonSize, onTap)
}

// ---- CTI 按钮创建（buildCallPanelIfNeeded 里调用一次）----

// buildCTIButtons 创建 CTI 话务相关按钮（两方通话入口 + 各话务状态按钮）。
// 每个按钮都挂上悬停提示（面板上只有图标，用户要求"移动上去给文字说明"）。
func (a *App) buildCTIButtons() {
	// 两方通话第一行：咨询 / 转移 / 会议（位于保持左侧）。
	// 咨询/转移只是把主窗体拨号框切到复用模式（不发指令），会议直接下发。
	a.consultBtn = newIconButton(IconCTIConsult, colorHoldBlue, func() { a.enterCTIDialMode("consult") })
	a.transferBtn = newIconButton(IconCTITransfer, colorHoldBlue, func() { a.enterCTIDialMode("transfer") })
	// 咨询/转移图标稍微缩小（用户要求，与其它图标视觉一致）
	a.consultBtn.SetIconSize(16)
	a.transferBtn.SetIconSize(16)
	a.conferenceBtn = newIconButton(IconCTIConference, colorHoldBlue, func() {
		a.runCTI(a.conferenceBtn, "addConferenceMembers", map[string]any{"members": []string{}})
	})
	// 以下都是**直接下发的指令**：统一走 runCTI（按钮转圈 + 在途互斥 + 失败提示）
	a.threeWayBtn = newIconButton(IconCTIThreeWay, colorHoldBlue, func() {
		a.runCTI(a.threeWayBtn, "consultThreeWay", nil)
	})
	// 三方图标原图本身就偏小（宽扁），图标再放大一档，与其它按钮视觉一致
	a.threeWayBtn.SetIconSize(22)
	a.consultTransferBtn = newIconButton(IconCTIConsultTransfer, colorHoldBlue, func() {
		a.runCTI(a.consultTransferBtn, "consultTransfer", nil)
	})
	// 取消/结束咨询（被咨询方行右侧）：用**方形挂断图标**（与挂断按钮同款，
	// 用户要求），红色图标、标准 36px，不再单独调偏移/尺寸
	a.finishConsultBtn = newIconButton(IconStopWhite, colorDangerRed, func() {
		a.runCTI(a.finishConsultBtn, "finishConsult", nil)
	})
	// 监听：耳语 / 插话（强插）/ 强拦。耳语态下同一个按钮改为「监听」
	// （动作 stopWhisper，恢复静默监听）。
	a.whisperBtn = newIconButton(IconCTIWhisper, colorHoldBlue, func() {
		action := "whisper"
		if a.monitorWhisper {
			action = "stopWhisper"
		}
		a.runCTI(a.whisperBtn, action, nil)
	})
	a.interveneBtn = newIconButton(IconCTIIntervene, colorHoldBlue, func() {
		a.runCTI(a.interveneBtn, "intervene", nil)
	})
	a.interceptBtn = newIconButton(IconCTIIntercept, colorHoldBlue, func() {
		a.runCTI(a.interceptBtn, "intercept", nil)
	})
	// 会议：添加成员（owner，结束会议左侧）——复用主窗体拨号框加人
	a.addMemberBtn = newIconButton(IconCTIAddMember, colorHoldBlue, func() {
		a.enterCTIDialMode("conference")
	})
	// 会议：结束会议（owner）
	a.finishConferenceBtn = newIconButton(IconCTIEndConference, colorDangerRed, func() {
		a.runCTI(a.finishConferenceBtn, "finishConference", nil)
	})
	a.finishConferenceBtn.SetIconOffset(1, 2) // 与退出咨询同款图标偏移（右 1、下 2）

	for _, t := range []struct {
		btn *compactButton
		tip string
	}{
		{a.consultBtn, "咨询"},
		{a.transferBtn, "转移"},
		{a.conferenceBtn, "会议"},
		{a.threeWayBtn, "三方通话"},
		{a.consultTransferBtn, "转移"},
		{a.finishConsultBtn, "结束咨询"},
		{a.whisperBtn, "耳语"},
		{a.interveneBtn, "插话"},
		{a.interceptBtn, "强拦"},
		{a.addMemberBtn, "添加成员"},
		{a.finishConferenceBtn, "结束会议"},
	} {
		t.btn.setTip(a.callTip, t.tip)
	}
}

// ---- CTI 指令在途状态（按钮转圈 + 互斥 + 错误反馈）----

// ctiSpinFrames 「在途」时按钮上的旋转圆圈：8 帧、每帧转 45°，**程序生成**
// （不往 assets 里塞 8 张图）。只换按钮的图标资源，尺寸沿用按钮自己的设置。
var ctiSpinFrames = buildCTISpinFrames()

func buildCTISpinFrames() []fyne.Resource {
	const (
		n     = 8
		size  = 48
		sweep = 4.2 // 圆弧张角（弧度，约 240°）
	)
	frames := make([]fyne.Resource, 0, n)
	for i := 0; i < n; i++ {
		img := image.NewNRGBA(image.Rect(0, 0, size, size))
		start := float64(i)*(2*math.Pi/n) - math.Pi/2
		c := float64(size) / 2
		rOut, rIn := c-2, c-9
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				cov := 0
				for sy := 0; sy < 2; sy++ { // 2×2 超采样：小图标也顺滑
					for sx := 0; sx < 2; sx++ {
						dx := float64(x) + (float64(sx)+0.5)/2 - c
						dy := float64(y) + (float64(sy)+0.5)/2 - c
						r := math.Hypot(dx, dy)
						if r < rIn || r > rOut {
							continue
						}
						d := math.Mod(math.Atan2(dy, dx)-start+8*math.Pi, 2*math.Pi)
						if d <= sweep {
							cov++
						}
					}
				}
				if cov > 0 {
					img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: uint8(cov * 63)})
				}
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			continue
		}
		frames = append(frames, fyne.NewStaticResource(fmt.Sprintf("cti-spin-%d.png", i), buf.Bytes()))
	}
	return frames
}

// ctiMinSpin 转圈至少显示这么久：服务端常常几十毫秒就回结果，不等一帧就恢复
// 的话用户根本看不到任何反馈（"点了没反应"）。
const ctiMinSpin = 500 * time.Millisecond

// startCTISpin 把按钮图标换成旋转圆圈（记下原图标，收到结果后恢复）。
// **第一帧同步换上**：等 ticker 的第一拍（90ms）再换的话，快回执时一帧都看不到。
func (a *App) startCTISpin(btn *compactButton) {
	if btn == nil || btn.icon == nil || len(ctiSpinFrames) == 0 {
		return
	}
	a.ctiPending = btn
	a.ctiPendingRes = btn.icon.Resource
	btn.SetIcon(ctiSpinFrames[0])
	logx.Logger().Debug("按钮进入转圈", "frames", len(ctiSpinFrames))
	stop := make(chan struct{})
	a.ctiSpinStop = stop
	go func() {
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		i := 1
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				f := ctiSpinFrames[i%len(ctiSpinFrames)]
				i++
				fyne.Do(func() { btn.SetIcon(f) })
			}
		}
	}()
}

// stopCTISpin 结束"在途"状态并（如有按钮）恢复图标：结果、超时、通话结束都要调。
// 结果来得比 ctiMinSpin 还快时，保持"在途"到满 ctiMinSpin 再恢复——期间点击
// 仍被拦下，用户也能看到转圈。
//
// **恢复的不是点击前的快照**：在途期间面板可能已经重建并更新了按钮图标
// （耳语→监听），所以恢复时取 ctiPendingRes 的最新值（见 setCTIButtonIcon）。
func (a *App) stopCTISpin() {
	if !a.ctiInFlight {
		return
	}
	logx.Logger().Debug("CTI 在途结束", "hasButton", a.ctiPending != nil)
	if a.ctiSpinStop != nil {
		close(a.ctiSpinStop)
		a.ctiSpinStop = nil
	}
	restore := func() {
		a.ctiInFlight = false
		btn, res := a.ctiPending, a.ctiPendingRes
		a.ctiPending, a.ctiPendingRes = nil, nil
		if btn != nil {
			btn.SetIcon(res)
		}
	}
	wait := ctiMinSpin - time.Since(a.ctiSpinAt)
	if a.ctiPending == nil || wait <= 0 {
		restore()
		return
	}
	go func() {
		time.Sleep(wait)
		fyne.Do(restore)
	}()
}

// setCTIButtonIcon 设置按钮图标；若这个按钮正在"在途"（转圈），同步更新
// 恢复图标——面板在转圈期间重建时（如耳语态把图标换成监听），回执到达时
// 必须恢复到**最新**图标，不能恢复成点击前的旧图标（真机缺陷：点耳语后
// 按钮转圈，转回来又变成耳语）。
func (a *App) setCTIButtonIcon(btn *compactButton, res fyne.Resource) {
	if btn == nil {
		return
	}
	btn.SetIcon(res)
	if a.ctiPending == btn {
		a.ctiPendingRes = res
	}
}

// runCTI 发起一条 CTI 指令（btn = 触发它的按钮，没有就传 nil）：
// 命令发出后进入"在途"状态（按钮转圈，没有按钮时只是锁住），直到收到
// MSP_RESULT（成功 / 失败 / 2 秒超时）；在途期间点其它按钮只提示
// 「上一个操作尚未完成」，点同一个按钮静默忽略（"未收到时这个按钮不能点击"）。
func (a *App) runCTI(btn *compactButton, action string, params map[string]any) error {
	if a.ctiInFlight {
		if btn == nil || btn != a.ctiPending {
			a.toast("上一个操作尚未完成")
		}
		return core.ErrCTIBusy
	}
	if _, err := a.core.SendCTICommand(action, params); err != nil {
		logx.Logger().Warn("发送 CTI 指令失败", "action", action, "err", err)
		a.toastError("指令发送失败：" + err.Error())
		return err
	}
	a.ctiInFlight = true
	a.ctiSpinAt = time.Now()
	logx.Logger().Info("CTI 指令已发出（在途）", "action", action, "withButton", btn != nil)
	a.startCTISpin(btn)
	return nil
}

// sendCTI 没有对应按钮的 CTI 指令（主窗体拨号复用流程等），同样受在途互斥约束。
func (a *App) sendCTI(action string, params map[string]any) error {
	return a.runCTI(nil, action, params)
}

// toast 停留时长：普通提示（如"上一个操作尚未完成"）1 秒，错误消息 2 秒。
const (
	toastInfoDuration  = time.Second
	toastErrorDuration = 2 * time.Second
)

// 消息条底色：与窗体深色背景明显区分（用户要求）——普通提示中性深灰蓝，
// 错误用**淡一点的红色**（原来暗红太沉）。
var (
	toastInfoBg  = color.NRGBA{R: 0x4a, G: 0x50, B: 0x5a, A: 0xf2}
	toastErrorBg = color.NRGBA{R: 0xd9, G: 0x53, B: 0x4f, A: 0xf2}
)

// toast 在**当前可见**窗口的**下缘居中**弹一条消息条（1 秒）：用于
// "上一个操作尚未完成"这类瞬时提示。
func (a *App) toast(msg string) { a.showToast(msg, toastInfoBg, toastInfoDuration) }

// toastError 错误消息（2 秒、暗红底）：CTI 指令失败/超时等。
func (a *App) toastError(msg string) { a.showToast(msg, toastErrorBg, toastErrorDuration) }

// showToast 选当前可见窗口的提示图层显示消息条：通话中弹在通话面板下缘，
// 否则弹在主窗体下缘。
func (a *App) showToast(msg string, bg color.NRGBA, d time.Duration) {
	if msg == "" {
		return
	}
	layer := a.callTip
	if a.uiState == uiIdle || a.callW == nil || a.callTip == nil {
		layer = a.mainTip
	}
	if layer == nil {
		return
	}
	layer.showBar(msg, bg, d)
}

// talkState 当前通话的话务状态（无通话/双方通话时返回 twoParty）。
func (a *App) talkState() core.TalkState {
	if a.activeCall == nil || a.activeCall.CTI.TalkState == "" {
		return core.TalkTwoParty
	}
	return a.activeCall.CTI.TalkState
}

// applyCallPhase 把 CALL_* 事件（协议 §16.2）的话务阶段反映到面板状态文字：
// CALL_RINGING 表示服务端侧已振铃（服务端外呼时 SIP 信令不一定有 180），
// 用户要求此刻"呼叫中"改成"振铃中"。只在拨号阶段生效——接通后由
// setCallUI(uiInCall) 的"通话中"负责，不能被迟到的事件改回去。
func (a *App) applyCallPhase(call *core.Call) {
	if a.callStateLabel == nil || call == nil || a.uiState != uiDialing {
		return
	}
	switch call.CTI.CallPhase {
	case "ringing":
		a.callStateLabel.SetText("振铃中")
	case "dialing":
		a.callStateLabel.SetText("呼叫中")
	}
}

// rebuildPanel 按当前通话状态重建面板：复杂话务走 rebuildTalkRows，
// 普通双方通话走 rebuildCallRows（并把面板高度恢复成三行）。
func (a *App) rebuildPanel(mode int) {
	if a.talkState() != core.TalkTwoParty {
		a.rebuildTalkRows()
		return
	}
	// 高度复位不在这里做：此刻旧行（可能 4 行）还挂在面板上，Fyne 的
	// SetFixedSize 会按旧内容的最小高把窗口顶回去（见 rebuildCallRows）。
	a.rebuildCallRows(mode)
}

// themeForegroundNRGBA 主题前景色（图标着色用）。
func themeForegroundNRGBA() color.NRGBA {
	return color.NRGBAModel.Convert(theme.Color(theme.ColorNameForeground)).(color.NRGBA)
}

// ctiText 用主题前景色创建的单行文本（短命行内文本，重建即弃）。
func ctiText(s string) *plainText {
	return newThemedText(s, fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText), false)
}

// rebuildTalkRows 按话务状态重建面板（咨询/三方/监听/会议）。
func (a *App) rebuildTalkRows() {
	if a.callRows == nil || a.activeCall == nil {
		return
	}
	st := &a.activeCall.CTI
	a.stopRingShake()
	a.ringFlashRect = nil
	// **先卸掉旧行再定高新行**：Fyne 的 SetFixedSize(true) 会按当前内容的最小
	// 尺寸把窗口顶回去——4 行内容最小高 170 高于 3 行面板的 144，缩不下来就会
	// 停在旧高度上（用户实测：4 行面板关闭后重新来电，面板仍是 170 高、
	// 三行内容居中、上下大片留白）。行内容在下面重新构造后才挂上去。
	a.callRows.Objects = nil
	// 挂断按钮的提示与图标复位（三方模式改提示、会议成员视角换「结束咨询」图标）
	if a.hangupBtn != nil {
		a.hangupBtn.setTip(a.callTip, "挂断")
		a.hangupBtn.SetIcon(IconStopWhite)
	}
	// 底行的扬声器/麦克风/摄像头按钮外观按会话状态刷新（与两方通话一致）
	a.applySpeakerButton()
	a.applyMicButton()
	a.applyVideoButton()

	var content fyne.CanvasObject
	switch st.TalkState {
	case core.TalkConsult:
		content = a.panelRows(a.consultRows(st))
	case core.TalkThreeWay:
		content = a.panelRows(a.threeWayRows(st))
	case core.TalkMonitor:
		content = a.panelRows(a.monitorRows(st))
	case core.TalkConference:
		// 会议自带滚动容器（成员数不定），不再套固定行布局
		content = a.conferenceRows(st)
	default:
		a.rebuildCallRows(incallButtons)
		return
	}
	a.callRows.Objects = []fyne.CanvasObject{
		container.NewBorder(padRect(4), padRect(4), padRect(6), padRect(4), content),
	}
	a.callRows.Refresh()
}

// talkBottomRow 话务面板（咨询/三方/监听/会议）的底行：时长 + 摄像头/麦克风/
// 扬声器——与两方通话面板一致（用户要求）。
func (a *App) talkBottomRow() fyne.CanvasObject {
	row := container.New(panelRowLayout{}, a.callTimerLabel)
	if a.callHasVideo {
		row.Add(a.videoBtn)
	}
	row.Add(a.micBtn)
	row.Add(a.speakerBtn)
	return row
}

// holdIndicator 只读的「保持中」指示：沿用保持按钮按下后的图标（取消保持），
// 但底色用禁用态灰、不可点击——咨询面板第二行表示原对端正在被保持（用户要求）。
// 视觉上比所在行**上移 2px**：与下一行文字拉开一点距离（用户要求）。
func holdIndicator(size float32) fyne.CanvasObject {
	// 禁用样式与主窗体按钮一致：**只把图标置灰、不留背景块**（用户要求）
	img := canvas.NewImageFromResource(tintIcon(IconReplayWhite, disabledIconColor()))
	img.FillMode = canvas.ImageFillContain
	iconSize := float32(math.Round(float64(size) * 0.5))
	img.SetMinSize(fyne.NewSize(iconSize, iconSize))
	btn := container.NewGridWrap(fyne.NewSize(size, size), container.NewCenter(img))
	return container.New(shiftedLayout{dy: -2}, btn)
}

// shiftedLayout 单个子对象纵向居中后再偏移 dy（负值上移）。
type shiftedLayout struct{ dy float32 }

func (l shiftedLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	if len(objs) == 0 {
		return fyne.Size{}
	}
	return objs[0].MinSize()
}

func (l shiftedLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	m := objs[0].MinSize()
	objs[0].Resize(m)
	objs[0].Move(fyne.NewPos(0, (size.Height-m.Height)/2+l.dy))
}

// peerText 号码的展示文本：**协议给的名称优先**（MSP_EVENT_* 的 xxxName /
// 成员 displayName，仅已登录坐席才有值），没有才回退通信录的名称；都没有就
// 只显示号码。与两方通话面板「号码 名称」的口径一致。
func (a *App) peerText(no, name string) string {
	if no == "" {
		return name
	}
	if name != "" && name != no {
		return no + " " + name
	}
	if c, ok := a.contactFor(no); ok && c.Name != "" && c.Name != no {
		return no + " " + c.Name
	}
	return no
}

// peerLabel 号码的展示控件：文本 + ★（常用）+ 备注悬停。话务面板里显示号码的
// 地方（被咨询方、三方对端、会议成员）都用它，与普通通话面板口径一致。
func (a *App) peerLabel(no, name string) fyne.CanvasObject {
	text := a.peerText(no, name)
	starred, note := false, ""
	if c, ok := a.contactFor(no); ok {
		starred, note = c.Starred, c.Note
	}
	var obj fyne.CanvasObject
	if note != "" {
		obj = newHoverNote(text, fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText), note, a.callTip)
	} else {
		obj = ctiText(text)
	}
	return container.New(markAfterLayout{gap: 4}, obj, starMark(starred))
}

// panelRows 固定行高的纵向排列（咨询/三方/监听用）。
func (a *App) panelRows(rows []fyne.CanvasObject) fyne.CanvasObject {
	return container.New(panelRowsLayout{rowHeight: callPanelRowHeight, gapAfterFirst: 4}, rows...)
}

// ---- 咨询 ----
//
// 4 行：状态 / 原对端 / 被咨询方 + 取消·结束咨询 / 时长。
// 接通后行 1 右侧出现「三方通话」「转移」两个按钮。
func (a *App) consultRows(st *core.CTIState) []fyne.CanvasObject {
	row1 := container.New(panelRowLayout{}, a.callStateLabel)
	switch st.ConsultPhase {
	case "talking":
		a.callStateLabel.SetText("咨询通话中")
		a.finishConsultBtn.setTip(a.callTip, "结束咨询")
		row1.Add(a.threeWayBtn)
		row1.Add(a.consultTransferBtn)
	case "ringing":
		a.callStateLabel.SetText("咨询振铃中")
		a.finishConsultBtn.setTip(a.callTip, "取消咨询")
	default: // dialing
		a.callStateLabel.SetText("咨询呼叫中")
		a.finishConsultBtn.setTip(a.callTip, "取消咨询")
	}

	// 行 2：原对端（号码）。右侧固定一个**只读的「保持中」指示**（外观同保持
	// 按钮按下后的样子，不可点击）：表示这一方正在被保持（用户要求）。
	row2 := container.New(panelRowLayout{}, a.callName, holdIndicator(callPanelButtonSize))

	// 行 3：被咨询方 + 取消/结束咨询
	row3 := container.New(panelRowLayout{}, a.peerLabel(st.Consultee, st.ConsulteeName), a.finishConsultBtn)

	row4 := a.talkBottomRow()

	a.setCallPanelHeight(4)
	return []fyne.CanvasObject{row1, row2, row3, row4}
}

// ---- 三方 ----
//
// 4 行：状态+挂断 / 对端1 + X / 对端2 + X / 时长。
// **自己不出现在列表里**（用户要求）：只显示另外两个对端的行，按本机号码
// 从 callerNo/calledNo/thirdParty 里剔掉自己。
func (a *App) threeWayRows(st *core.CTIState) []fyne.CanvasObject {
	a.callStateLabel.SetText("三方通话中")
	isOwner := a.isCTIOwner(st.Owner)

	remotes := a.threeWayRemotes(st)
	// 第一行只有「退出三方」这一个按钮（用户要求）：三方模式下不再显示
	// 其它入口，退出语义写在悬停提示里。图标与「退出咨询/退出会议」一致
	//（用户要求：都是"离开当前话务"）。
	a.hangupBtn.setTip(a.callTip, "退出三方")
	a.hangupBtn.SetIcon(IconCTIExitConference)
	row1 := container.New(panelRowLayout{}, a.callStateLabel, a.hangupBtn)
	rows := []fyne.CanvasObject{row1}
	for _, no := range remotes {
		row := container.New(panelRowLayout{}, a.peerLabel(no, st.NameOf(no)))
		// **owner** 才有强制某一方退出的 X（exitThreeWay）；普通成员只看到号码
		if isOwner {
			row.Add(a.newExitThreeWayBtn(no))
		}
		rows = append(rows, row)
	}
	rows = append(rows, a.talkBottomRow())

	a.setCallPanelHeight(4)
	return rows
}

// threeWayRemotes 三方通话里"除自己以外"的两个号码（最多两个、去重）。
// 协议 §16.4：firstParty/secondParty 是原通话的双方，thirdParty 是新加入的一方。
func (a *App) threeWayRemotes(st *core.CTIState) []string {
	self := a.core.State().Register.Account
	all := []string{st.FirstParty, st.SecondParty, st.ThirdParty}
	out := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, no := range all {
		if no == "" || no == self || seen[no] {
			continue
		}
		seen[no] = true
		out = append(out, no)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// newSmallHangupBtn 强制挂断一方 / 移除会议成员：与其它话务按钮完全同款
// （无底色、红色图标、标准 36px）——用户要求"小图标也别搞了、样式统一"。
// onTap 收到按钮本身，调用方据此把点击接到 runCTI（旋转/在途互斥）。
func (a *App) newSmallHangupBtn(tip string, onTap func(b *compactButton)) fyne.CanvasObject {
	var b *compactButton
	b = newPanelButton(IconHangup, colorHoldYellow, callPanelButtonSize, func() { onTap(b) })
	b.SetIconSize(16) // 稍微缩小（用户要求）
	b.setTip(a.callTip, tip)
	return b
}

// newExitThreeWayBtn 为某个通话方生成「强制挂断」按钮（仅 owner）。
func (a *App) newExitThreeWayBtn(target string) fyne.CanvasObject {
	return a.newSmallHangupBtn("强制挂断", func(b *compactButton) {
		a.runCTI(b, "exitThreeWay", map[string]any{"target": target})
	})
}

// ---- 监听 ----
//
// 3 行：状态+耳语/插话/强拦 / 「监听方 >>> 对端」+ 结束监听 / 时长。
func (a *App) monitorRows(st *core.CTIState) []fyne.CanvasObject {
	row1 := container.New(panelRowLayout{}, a.callStateLabel)
	// 耳语态：耳语按钮改为「监听」（stopWhisper 回到静默监听）；插话（强插）
	// 与强拦两种状态都保留（用户要求）。
	a.monitorWhisper = st.MonitorPhase == "whisper"
	if a.monitorWhisper {
		a.callStateLabel.SetText("监听耳语中")
		a.whisperBtn.setTip(a.callTip, "监听")
		a.setCTIButtonIcon(a.whisperBtn, IconCTIMonitor)
	} else {
		a.callStateLabel.SetText("监听中")
		a.whisperBtn.setTip(a.callTip, "耳语")
		a.setCTIButtonIcon(a.whisperBtn, IconCTIWhisper)
	}
	row1.Add(a.whisperBtn)
	row1.Add(a.interveneBtn) // 插话（强插）
	row1.Add(a.interceptBtn) // 强拦

	a.hangupBtn.setTip(a.callTip, "结束监听")
	// 行 2：被监听话务的主被叫（协议 §16.5 的 callerNo/calledNo）：
	// 主叫是 target 时显示 callerNo → calledNo，否则 callerNo → target；
	// 老协议没带 caller/called 时退回"监听方 → 被监听方"。箭头用图标，不用字符。
	//
	// 号码与名称**分成独立文本**（用户要求）：号码永远完整显示，空间不够时
	// 只截断名称（显示 …），被截断的名称悬停可看完整内容。
	leftNo, leftName := st.CallerNo, st.CallerName
	rightNo, rightName := st.Target, st.TargetName
	if leftNo == "" {
		leftNo, leftName = st.Monitor, st.MonitorName
	} else if core.NormalizeNumber(leftNo) == core.NormalizeNumber(st.Target) {
		rightNo, rightName = st.CalledNo, st.CalledName
	}
	textSize := fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameText)
	nameText := func(name, no string) *hoverNote {
		n := newHoverClipNote(name, textSize, a.callTip)
		if name == "" || name == no {
			n.Hide() // 没有名称 / 与号码重复：不占位
		}
		return n
	}
	arrow := canvas.NewImageFromResource(tintIcon(IconArrowRight, themeForegroundNRGBA()))
	arrow.FillMode = canvas.ImageFillContain
	arrow.SetMinSize(fyne.NewSize(14, 14))
	row2 := container.New(panelRowLayout{},
		container.New(monitorPeerLayout{gap: 4},
			ctiText(leftNo), nameText(leftName, leftNo), arrow,
			ctiText(rightNo), nameText(rightName, rightNo),
		), a.hangupBtn)
	row3 := a.talkBottomRow()

	a.setCallPanelHeight(3)
	return []fyne.CanvasObject{row1, row2, row3}
}

// monitorPeerLayout 监听面板第二行：[左号码][左名称…] → [右号码][右名称…]。
// **号码始终按完整宽度显示**（用户要求：内容太长时优先保证号码完整），名称
// 只在剩余空间里分配、放不下就截断（plainText.Resize 自行补省略号，悬停提示
// 由 hoverNote 负责）。名称不可见时不占位。
type monitorPeerLayout struct{ gap float32 }

func (l monitorPeerLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var w, h float32
	visible := 0
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		if visible > 0 {
			w += l.gap
		}
		w += naturalWidth(o)
		h = fyne.Max(h, o.MinSize().Height)
		visible++
	}
	return fyne.NewSize(w, h)
}

func (l monitorPeerLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	// 名称可见时才计入间距；号码与箭头是固定宽度。
	visible := 0
	for _, o := range objs {
		if o.Visible() {
			visible++
		}
	}
	fixed := naturalWidth(objs[0]) + naturalWidth(objs[3]) + naturalWidth(objs[2])
	if visible > 1 {
		fixed += l.gap * float32(visible-1)
	}
	rem := size.Width - fixed
	if rem < 0 {
		rem = 0
	}
	// 两个名称按各自需要的宽度等比分配剩余空间
	needL, needR := float32(0), float32(0)
	if objs[1].Visible() {
		needL = naturalWidth(objs[1])
	}
	if objs[4].Visible() {
		needR = naturalWidth(objs[4])
	}
	wL, wR := needL, needR
	if total := needL + needR; total > rem {
		if total == 0 {
			wL, wR = 0, 0
		} else {
			wL = rem * needL / total
			wR = rem * needR / total
		}
	}
	x := float32(0)
	place := func(o fyne.CanvasObject, width float32) {
		if !o.Visible() {
			return
		}
		if x > 0 {
			x += l.gap
		}
		m := o.MinSize()
		width = fyne.Max(width, 1)
		o.Resize(fyne.NewSize(width, size.Height))
		o.Move(fyne.NewPos(x, (size.Height-m.Height)/2))
		x += width
	}
	place(objs[0], naturalWidth(objs[0])) // 左号码：完整
	place(objs[1], wL)                    // 左名称：可截断
	place(objs[2], naturalWidth(objs[2])) // 箭头
	place(objs[3], naturalWidth(objs[3])) // 右号码：完整
	place(objs[4], wR)                    // 右名称：可截断
}

// ---- 会议 ----
//
// 行1 = 状态 + 添加成员/结束会议 + 退出会议（owner）/ 退出会议（成员）；
// 其后为成员列表（滚动）；最后一行 = 时长。
//
// 「结束会议」是结束整个会议（CTI 动作），「退出会议」只是挂断本端这通电话，
// 两者并存（owner 也有退出会议，用户要求）。
//
// 成员行：[移除] 或 [主持人标记]（二者同列）号码 …… 右侧 [禁视频][禁音][禁听]。
// 首列恒定占位（没有按钮/不是主持人时留空），各行号码左边缘对齐。
// **每一行都显示会控（含自己那一行）**：owner 可点击切换，普通成员只读。
// 正在呼入的成员（`>>>`）：右侧只有「取消呼叫」（方形挂断图标，动作同样是
// removeConferenceMembers）。**非视频会议不显示「禁视频」**（用户要求）：本通
// 通话没有协商视频时不提供该项。
func (a *App) conferenceRows(st *core.CTIState) fyne.CanvasObject {
	a.callStateLabel.SetText("会议中")
	isOwner := a.isCTIOwner(st.Owner)

	row1 := container.New(panelRowLayout{}, a.callStateLabel)
	if isOwner {
		a.addMemberBtn.setTip(a.callTip, "添加成员")
		row1.Add(a.addMemberBtn)
		a.finishConferenceBtn.setTip(a.callTip, "结束会议")
		row1.Add(a.finishConferenceBtn)
	}
	// 退出会议（owner 与成员都有）：只挂断本端通话，不结束会议
	a.hangupBtn.setTip(a.callTip, "退出会议")
	a.hangupBtn.SetIcon(IconCTIExitConference)
	row1.Add(a.hangupBtn)

	rows := []fyne.CanvasObject{row1}
	// 主持人永远排第一行（其余保持服务端顺序，用户要求）
	members := append([]core.ConferenceMember(nil), st.Members...)
	sort.SliceStable(members, func(i, j int) bool {
		return members[i].TelNo == st.Owner && members[j].TelNo != st.Owner
	})
	for _, m := range members {
		rows = append(rows, a.conferenceMemberRow(m, st.Owner, isOwner))
	}
	rows = append(rows, a.talkBottomRow())

	content := container.NewVBox(rows...)
	scroll := container.NewVScroll(content)

	// 高度：标题 1 行 + 成员 n 行 + 底行 1 行，封顶 6 行
	n := len(st.Members) + 2
	a.setCallPanelHeightDynamic(n)
	return scroll
}

// conferenceMemberRow 会议成员行（布局见 conferenceRows 注释）。
func (a *App) conferenceMemberRow(m core.ConferenceMember, owner string, isOwner bool) fyne.CanvasObject {
	self := a.core.State().Register.Account
	isSelf := m.TelNo == self

	// 正在呼入的成员：还没接通，不显示会控，右侧只有「取消呼叫」
	dialing := hasFlag(m.State, "dialing")

	// 左侧首列：[移除成员] 或 [主持人标记]，**二者同列**（用户要求：移除按钮
	// 与主持人图标对齐）。都没有的行（成员视角、正在呼入的行）留同宽空位，
	// 这样所有行号码左边缘对齐。
	var slotObj fyne.CanvasObject
	switch {
	case isOwner && !isSelf && !dialing:
		slotObj = a.newRemoveMemberBtn(m.TelNo)
	case owner != "" && m.TelNo == owner:
		slotObj = a.hostMark()
	default:
		spacer := canvas.NewRectangle(color.Transparent)
		spacer.SetMinSize(fyne.NewSize(callPanelButtonSize, callPanelButtonSize))
		slotObj = spacer
	}
	left := container.New(conferenceLeadLayout{slot: callPanelButtonSize, gap: 4},
		slotObj, a.peerLabel(m.TelNo, m.DisplayName))

	row := container.New(panelRowLayout{}, left)
	if dialing {
		if isOwner {
			row.Add(a.newCancelCallBtn(m.TelNo))
		}
		return row
	}
	// 顺序：禁视频 / 禁音 / 禁听。**自己那一行也显示会控**（用户要求）：
	// owner 视角可点击切换（对自己也生效），成员视角只读。
	// 非视频会议没有视频可禁，不显示「禁视频」（用户要求）。
	clickable := isOwner
	if a.callHasVideo {
		row.Add(a.memberStateControl(m.TelNo, "vmuted", hasFlag(m.State, "vmuted"), clickable))
	}
	row.Add(a.memberStateControl(m.TelNo, "muted", hasFlag(m.State, "muted"), clickable))
	row.Add(a.memberStateControl(m.TelNo, "deafened", hasFlag(m.State, "deafened"), clickable))
	return row
}

// hostMark 主持人标记（会议 owner 的号码前面）：14px 图标，悬停提示「主持人」。
func (a *App) hostMark() fyne.CanvasObject {
	h := newHoverIcon(IconHost, 14, a.callTip)
	h.SetNote("主持人")
	return h
}

// conferenceLeadLayout 会议成员行的左侧块：[首列][号码]。首列宽 = 面板按钮
// 尺寸，里面放「移除成员」按钮（owner 视角）、主持人标记（主持人那一行）或
// 空位——三者同列，所以**移除按钮与主持人图标对齐**，各行号码也左对齐。
type conferenceLeadLayout struct{ slot, gap float32 }

func (l conferenceLeadLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	m := objs[1].MinSize()
	return fyne.NewSize(l.slot+l.gap+m.Width, fyne.Max(m.Height, l.slot))
}

func (l conferenceLeadLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	slot, text := objs[0], objs[1]
	if slot.Visible() {
		m := slot.MinSize()
		slot.Resize(m)
		slot.Move(fyne.NewPos((l.slot-m.Width)/2, (size.Height-m.Height)/2))
	}
	x := l.slot + l.gap
	w := size.Width - x
	if w < 0 {
		w = 0
	}
	text.Resize(fyne.NewSize(w, size.Height))
	text.Move(fyne.NewPos(x, 0))
}

// newCancelCallBtn 取消对某个会议成员的呼叫：用**方形挂断图标**（与挂断按钮
// 同款，用户要求），动作是 removeConferenceMembers。
func (a *App) newCancelCallBtn(telNo string) fyne.CanvasObject {
	var b *compactButton
	b = newPanelButton(IconStopWhite, colorDangerRed, callPanelButtonSize, func() {
		a.runCTI(b, "removeConferenceMembers", map[string]any{"members": []string{telNo}})
	})
	b.setTip(a.callTip, "取消呼叫")
	return b
}

// memberStateControl 成员会控状态：
//   - clickable=true（owner）：可点击切换的状态按钮；
//   - clickable=false（普通成员）：只读图标。
//
// 尺寸与图标大小和「移除成员」按钮**一致**（36px 按钮、16px 图标，用户要求）；
// 样式也与其它话务按钮一致：无底色（背景就是面板底色），图标本身用状态色——
// 已静音/已禁听/已禁视频 = 红 + **带斜杠的图标**，未生效 = 灰 + **普通图标**
// （未禁的成员不该显示斜杠，用户反馈）。
func (a *App) memberStateControl(telNo, flag string, on, clickable bool) fyne.CanvasObject {
	accent := colorSwitchGray
	if on {
		accent = colorDangerRed
	}
	raw := memberStateIcon(flag, on)
	size := float32(callPanelButtonSize)
	iconSize := memberStateIconSize
	if !clickable {
		// 只读：同一个着色图标，没有点击/悬停反馈
		img := canvas.NewImageFromResource(tintIcon(raw, accent))
		img.FillMode = canvas.ImageFillContain
		img.SetMinSize(fyne.NewSize(iconSize, iconSize))
		return container.NewGridWrap(fyne.NewSize(size, size), container.NewCenter(img))
	}
	b := newPanelButton(raw, accent, size, nil)
	b.SetIconSize(iconSize)
	b.onTap = func() {
		action := "muteConferenceMember"
		if on {
			action = "unmuteConferenceMember"
		}
		switch flag {
		case "deafened":
			action = "deafConferenceMember"
			if on {
				action = "undeafConferenceMember"
			}
		case "vmuted":
			action = "vmuteConferenceMember"
			if on {
				action = "unvmuteConferenceMember"
			}
		}
		a.runCTI(b, action, map[string]any{"member": telNo})
	}
	b.setTip(a.callTip, memberControlTip(flag, on))
	return b
}

// memberStateIcon 会控按钮的图标：未生效时是普通图标，生效时换成带斜杠的版本。
func memberStateIcon(flag string, on bool) fyne.Resource {
	switch flag {
	case "deafened":
		if on {
			return IconCTISpeakerOff
		}
		return IconCTISpeaker
	case "vmuted":
		if on {
			return IconCTIVideoOff
		}
		return IconCTIVideo
	default:
		if on {
			return IconCTIMicOff
		}
		return IconCTIMic
	}
}

// memberControlTip 会议成员会控按钮的悬停提示（已生效时提示"取消…"）。
func memberControlTip(flag string, on bool) string {
	switch flag {
	case "deafened":
		if on {
			return "恢复收听"
		}
		return "禁听"
	case "vmuted":
		if on {
			return "恢复视频"
		}
		return "禁视频"
	default:
		if on {
			return "恢复声音"
		}
		return "禁音"
	}
}

// newRemoveMemberBtn 移除会议成员（小号挂断图标，见 newSmallHangupBtn）。
func (a *App) newRemoveMemberBtn(telNo string) fyne.CanvasObject {
	return a.newSmallHangupBtn("移除成员", func(b *compactButton) {
		a.runCTI(b, "removeConferenceMembers", map[string]any{"members": []string{telNo}})
	})
}

// memberStateIconSize 会议成员会控按钮的图标尺寸（与「移除成员」一致：
// 36px 按钮 + 16px 图标）。
const memberStateIconSize float32 = 16

// hasFlag 成员 state 是否包含某状态（state 用 + 组合，如 "muted+deafened"）。
func hasFlag(state, flag string) bool {
	for _, f := range splitPlus(state) {
		if f == flag {
			return true
		}
	}
	return false
}

func splitPlus(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '+' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// ---- 主窗体拨号复用（咨询/转移/会议加人）----

// dialInputMode 当前是否处于"输入号码"模式：空闲拨号，或 CTI 拨号复用
// （咨询/转移/会议加人）。CTI 复用模式下号码按键只输入、不发送 DTMF。
func (a *App) dialInputMode() bool {
	return a.uiState == uiIdle || a.ctiDialMode != ""
}

// enterCTIDialMode 进入"主窗体拨号复用"模式：显示主窗体、清空拨号框、
// 允许输入（不再发 DTMF），呼叫按钮换成对应的两行动作。
func (a *App) enterCTIDialMode(mode string) {
	a.ctiDialMode = mode
	a.mainW.Show()
	a.mainShown = true
	a.mainW.RequestFocus()
	a.bringMainToFront()
	if a.dialSelect != nil {
		a.dialSelect.SetText("")
		a.dialSelect.Enable()
	}
	a.setKeypadEnabled(true)
	// 呼叫按钮可用；视频按钮隐藏（复用模式下没有"视频呼叫"语义）
	if a.callBtn != nil {
		a.callBtn.Enable()
	}
	if a.videoCallBtn != nil {
		a.videoCallBtn.Hide()
	}
	a.updateCallButton()
	logx.Logger().Info("进入 CTI 拨号模式", "mode", mode)
}

// ctiDialAction 该 MSP_RESULT 的 action 是否属于当前拨号复用模式自己的动作。
// 只有它才收尾（收起主窗体、回到通话面板）；复用期间在同一主窗体上点其它按钮
// （例如坐席状态按钮的 setBusy/setIdle）的回执不能把主窗体关掉。
func ctiDialAction(mode, action string) bool {
	switch mode {
	case "consult":
		return action == "consult" || action == "consultThreeWay"
	case "transfer":
		return action == "transfer" || action == "consultTransfer"
	case "conference":
		return action == "addConferenceMembers"
	}
	return false
}

// exitCTIDialMode 退出拨号复用模式，恢复普通拨号/通话面板。
func (a *App) exitCTIDialMode() {
	a.ctiDialMode = ""
	if a.ctiCallBtn2 != nil {
		a.ctiCallBtn2.Hide()
	}
	if a.videoCallBtn != nil {
		if a.core.VideoUsable() {
			a.videoCallBtn.Show()
		} else {
			a.videoCallBtn.Hide()
		}
	}
	a.updateCallButton()
}

// onCTICallButton 拨号复用模式的第一动作：发起咨询(consult) / 直接转移(transfer) /
// 加入会议(addConferenceMembers)。
func (a *App) onCTICallButton() {
	target := strings.TrimSpace(a.dialSelect.Text)
	if target == "" {
		a.showInfo("提示", "请输入号码")
		return
	}
	var err error
	// 用主窗体上的动作按钮当"在途"按钮：它转圈直到收到 MSP_RESULT，
	// 成功后由 cti.result 收尾（收起主窗体、回到通话面板），失败则停在拨号页。
	switch a.ctiDialMode {
	case "consult":
		err = a.runCTI(a.callBtn, "consult", map[string]any{"target": target})
	case "transfer":
		err = a.runCTI(a.callBtn, "transfer", map[string]any{"target": target})
	case "conference":
		err = a.runCTI(a.callBtn, "addConferenceMembers", map[string]any{"members": []string{target}})
	}
	if err == nil {
		// 与普通拨号一致：复用拨号框发出的动作也记进「最近呼叫」（拨号下拉）
		a.core.AddRecentDial(target)
	}
}

// onCTICallButton2 拨号复用模式的第二动作：发起三方(consultThreeWay) / 咨询转移(consultTransfer)。
func (a *App) onCTICallButton2() {
	target := strings.TrimSpace(a.dialSelect.Text)
	if target == "" {
		a.showInfo("提示", "请输入号码")
		return
	}
	var err error
	switch a.ctiDialMode {
	case "consult":
		err = a.runCTI(a.ctiCallBtn2, "consultThreeWay", map[string]any{"target": target})
	case "transfer":
		err = a.runCTI(a.ctiCallBtn2, "consultTransfer", map[string]any{"target": target})
	}
	if err == nil {
		a.core.AddRecentDial(target) // 同第一动作：记进拨号下拉的「最近呼叫」
	}
}

// finishCTIDial 发送完 CTI 拨号动作后：退出拨号复用模式、主窗口收起，
// 回到通话面板等 MSP_RESULT / MSP_EVENT 驱动状态。
func (a *App) finishCTIDial() {
	a.exitCTIDialMode()
	a.mainW.Hide()
	a.mainShown = false
}

// isCTIOwner 本机是否为该话务的 owner（会议/三方）。
func (a *App) isCTIOwner(owner string) bool {
	if owner == "" {
		return false
	}
	return owner == a.core.State().Register.Account
}

// callPanelHeightFor n 行面板的窗口高度（DIP）：3 行为基准，每多一行加一行高。
func callPanelHeightFor(n int) float32 {
	if n < 3 {
		n = 3
	}
	return float32(callPanelHeight) + float32(n-3)*float32(callPanelRowHeight+panelRowGap)
}

// setCallPanelHeight 把通话面板窗口高度调整为 n 行（双方通话为 3 行）。
func (a *App) setCallPanelHeight(n int) {
	if a.callW == nil {
		return
	}
	if a.callPanelRows == n {
		return // 行数没变：不重复 resize（避免每次重建面板都抖一下）
	}
	a.callPanelRows = n
	a.applyCallPanelHeight()
}

// applyCallPanelHeight 按 callPanelRows 立即设一次窗口高度（不做行数比较，幂等）。
//
// ⚠️ 前提：调用方必须**先把旧行从 callRows 里卸掉**（Objects = nil）。Fyne 的
// SetFixedSize(true) 会跑一次 fitContent，按当前内容的最小尺寸把窗口顶回去——
// 旧内容更高时（4 行内容最小高 170 > 3 行面板 144）目标高度根本设不进去，
// 窗口停在旧高度上（真机现象：4 行面板关闭后重新来电，窗口仍是 170 高）。
func (a *App) applyCallPanelHeight() {
	if a.callW == nil {
		return
	}
	n := a.callPanelRows
	if n < 3 {
		n = 3
	}
	a.callW.SetFixedSize(false)
	a.callW.Resize(fyne.NewSize(callPanelWidth, callPanelHeightFor(n)))
	a.callW.SetFixedSize(true)
	a.placeCallWindow()
}

// setCallPanelHeightDynamic 会议面板：按内容行数动态定高（成员滚动，高度封顶）。
func (a *App) setCallPanelHeightDynamic(n int) {
	const maxRows = 6
	if n > maxRows {
		n = maxRows
	}
	if n < 3 {
		n = 3
	}
	a.setCallPanelHeight(n)
}
