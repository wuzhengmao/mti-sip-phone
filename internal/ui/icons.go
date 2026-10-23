package ui

import (
	"strings"

	"fyne.io/fyne/v2"

	"mti-sip-phone/internal/appicon"
)

// 托盘图标：话机形状，未注册为红色、注册成功后为绿色（REQ-UI-011）。
// SVG 源在 internal/appicon（仅托盘无 UI 版要以同一份图标自己光栅化，
// 那条路不链接 fyne），这里包成 Fyne 静态资源交给 driver 渲染。

// micIconSVG 麦克风图标（Material Design mic / mic-off path），
// 通话面板麦克风开关按钮用（REQ-UI-023）。
func micIconSVG(color string, off bool) string {
	path := `M12 14a3 3 0 0 0 3-3V5a3 3 0 0 0-6 0v6a3 3 0 0 0 3 3zm5-3a5 5 0 0 1-10 0H5a7 7 0 0 0 6 6.92V21h2v-3.08A7 7 0 0 0 19 11h-2z`
	if off {
		path = `M19 11h-1.7c0 .74-.16 1.43-.43 2.05l1.23 1.23c.56-.98.9-2.09.9-3.28zm-4.02.17c0-.06.02-.11.02-.17V5a3 3 0 0 0-5.54-1.65L11 4.86V11c0 .06-.01.11-.02.17l3.99 4zM4.27 3L3 4.27l6.01 6.01V11c0 1.66 1.33 3 2.99 3 .22 0 .44-.03.65-.08l1.66 1.66c-.71.33-1.5.52-2.31.52-2.76 0-5.3-2.1-5.3-5.1H5c0 3.41 2.72 6.23 6 6.72V21h2v-3.28c.91-.13 1.77-.45 2.54-.9L19.73 21 21 19.73 4.27 3z`
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + path + `"/>
</svg>`
}

// speakerIconSVG 扬声器图标（Material Design volume_up / volume_off path），
// 通话面板扬声器开关按钮用（REQ-UI-023）。
func speakerIconSVG(color string, off bool) string {
	path := `M3 9v6h4l5 5V4L7 9H3zm13.5 3A4.5 4.5 0 0 0 14 7.97v8.05A4.5 4.5 0 0 0 16.5 12zM14 3.23v2.06a7 7 0 0 1 0 13.42v2.06a9 9 0 0 0 0-17.54z`
	if off {
		path = `M16.5 12A4.5 4.5 0 0 0 14 7.97v2.21l2.45 2.45c.03-.2.05-.41.05-.63zm2.5 0c0 .94-.2 1.82-.54 2.64l1.51 1.51A8.8 8.8 0 0 0 21 12c0-4.28-2.99-7.86-7-8.77v2.06c2.89.86 5 3.54 5 6.71zM4.27 3L3 4.27 7.73 9H3v6h4l5 5v-6.73l4.25 4.25c-.67.52-1.42.93-2.25 1.18v2.06a8.99 8.99 0 0 0 3.69-1.81L19.73 21 21 19.73l-9-9L4.27 3zM12 4L9.91 6.09 12 8.18V4z`
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + path + `"/>
</svg>`
}

// videoIconSVG 摄像头图标（Material Design videocam / videocam_off path），
// 视频呼叫按钮与通话面板视频开关用（REQ-VIDEO-003/008）。
func videoIconSVG(color string, off bool) string {
	path := `M17 10.5V7c0-.55-.45-1-1-1H4c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h12c.55 0 1-.45 1-1v-3.5l4 4v-11l-4 4z`
	if off {
		path = `M21 6.5l-4 4V7c0-.55-.45-1-1-1H9.82L21 17.18V6.5zM3.27 2L2 3.27 4.73 6H4c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h12c.21 0 .39-.08.54-.18L19.73 21 21 19.73 3.27 2z`
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + path + `"/>
</svg>`
}

// rotateIconSVG 画面旋转图标（Material Design rotate_right path）。
// 视频窗口工具栏用：远端画面与本地回显各一个。
func rotateIconSVG(color string) string {
	path := `M15.55 5.55L11 1v3.07C7.06 4.56 4 7.92 4 12s3.05 7.44 7 7.93v-2.02c-2.84-.48-5-2.94-5-5.91s2.16-5.43 5-5.91V10l4.55-4.45zM19.93 11a7.9 7.9 0 0 0-1.62-3.89l-1.42 1.42c.54.75.88 1.6 1.02 2.47h2.02zM13 17.91v2.02c1.39-.22 2.68-.75 3.79-1.5l-1.44-1.44c-.71.51-1.51.84-2.35.92zm3.77-2.88l1.42 1.42c.75-1.11 1.28-2.4 1.5-3.79h-2.02c-.13.87-.47 1.72-1.02 2.47z`
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + path + `"/>
</svg>`
}

// lockIconSVG 窗口锁定图标（Material Design lock path）：通话面板与视频窗口
// 左上角的小锁用（REQ-UI-040 / REQ-VIDEO-015）。闭锁 = 锁定中。
func lockIconSVG(color string) string {
	path := `M18 8h-1V6c0-2.76-2.24-5-5-5S7 3.24 7 6v2H6c-1.1 0-2 .9-2 2v10c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V10c0-1.1-.9-2-2-2zm-6 9c-1.1 0-2-.9-2-2s.9-2 2-2 2 .9 2 2-.9 2-2 2zm3.1-9H8.9V6c0-1.71 1.39-3.1 3.1-3.1 1.71 0 3.1 1.39 3.1 3.1v2z`
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + path + `"/>
</svg>`
}

// unlockIconSVG 窗口解锁图标（Material Design lock_open path）：开锁 = 可拖动。
func unlockIconSVG(color string) string {
	path := `M12 17c1.1 0 2-.9 2-2s-.9-2-2-2-2 .9-2 2 .9 2 2 2zm6-9h-1V6c0-2.76-2.24-5-5-5S7 3.24 7 6h1.9c0-1.71 1.39-3.1 3.1-3.1 1.71 0 3.1 1.39 3.1 3.1v2H6c-1.1 0-2 .9-2 2v10c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V10c0-1.1-.9-2-2-2zm0 12H6V10h12v10z`
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + path + `"/>
</svg>`
}

// pinIconSVG 置顶图标（Material Design push_pin path）：视频窗口工具栏用。
func pinIconSVG(color string) string {
	path := `M16 9V4h1c.55 0 1-.45 1-1s-.45-1-1-1H7c-.55 0-1 .45-1 1s.45 1 1 1h1v5c0 1.66-1.34 3-3 3v2h5.97v7l1 1 1-1v-7H19v-2c-1.66 0-3-1.34-3-3z`
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + path + `"/>
</svg>`
}

// mirrorIconSVG 水平镜像图标（Material Design flip path：中线左侧实体、
// 右侧虚框）。
func mirrorIconSVG(color string) string {
	path := `M15 21h2v-2h-2v2zm4-12h2V7h-2v2zM3 5v14c0 1.1.9 2 2 2h4v-2H5V5h4V3H5c-1.1 0-2 .9-2 2zm16-2v2h2c0-1.1-.9-2-2-2zm-8 20h2V1h-2v22zm8-6h2v-2h-2v2zM15 5h2V3h-2v2zm4 8h2v-2h-2v2zm0 8c1.1 0 2-.9 2-2h-2v2z`
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + path + `"/>
</svg>`
}

var (
	// IconVideoOn 摄像头开：白色前景（配蓝色底）。
	IconVideoOn = fyne.NewStaticResource("video-on.svg", []byte(videoIconSVG("#ffffff", false)))
	// IconVideoOff 摄像头关：红色斜线（浅灰底上清晰可见）。
	IconVideoOff = fyne.NewStaticResource("video-off.svg", []byte(videoIconSVG("#e53935", true)))
	// IconVideoCall 主窗体视频呼叫按钮（绿底黑图标，与呼叫按钮同色系）。
	// IconVideoCallWhite 白色视频呼叫图标：主窗体视频呼叫按钮（蓝底白图标）。
	IconVideoCallWhite = newIconPNG("video-out")
	IconVideoCall      = fyne.NewStaticResource("video-call.svg", []byte(videoIconSVG("#1a1a1a", false)))
	// IconVideoGreen 视频窗口图标（绿色摄像头；不继承应用的红色话机图标）。
	IconVideoGreen = fyne.NewStaticResource("video-green.svg", []byte(videoIconSVG("#43a047", false)))
	// IconVideoAnswer 来电「视频接听」按钮（绿底白图标）。
	IconVideoAnswer = newIconPNG("video-in")

	// IconRotate 画面旋转（视频窗口工具栏：远端画面 / 本地回显各一个按钮）。
	IconRotate = fyne.NewStaticResource("rotate.svg", []byte(rotateIconSVG("#ffffff")))
	// IconMirror 水平镜像（视频窗口工具栏）。
	IconMirror = fyne.NewStaticResource("mirror.svg", []byte(mirrorIconSVG("#ffffff")))
	// IconPin 视频窗口置顶开关（工具栏）。
	IconPin = fyne.NewStaticResource("pin.svg", []byte(pinIconSVG("#ffffff")))
	// IconLockClosed / IconLockOpen 视频窗口的锁定开关（工具栏，REQ-VIDEO-015）：
	// 白图标，蓝底（锁定中）与灰底（已解锁）上都清晰。
	IconLockClosed = fyne.NewStaticResource("lock.svg", []byte(lockIconSVG("#ffffff")))
	IconLockOpen   = fyne.NewStaticResource("unlock.svg", []byte(unlockIconSVG("#ffffff")))

	// IconPhoneRed 未注册 / 注册中托盘图标（红色话机，PNG：iconfont 原始渲染）。
	IconPhoneRed = fyne.NewStaticResource("phone-red.png", appicon.PhoneRedPNG())
	// IconPhoneGreen 已注册且坐席空闲（绿色话机，PNG：iconfont 原始渲染）。
	IconPhoneGreen = fyne.NewStaticResource("phone-green.png", appicon.PhoneGreenPNG())
	// IconPhoneYellow 已注册且坐席置忙（黄色话机，由绿图按 alpha 现着色）。
	IconPhoneYellow = fyne.NewStaticResource("phone-yellow.png", appicon.PhoneYellowPNG())
	// IconPhoneBlack 黑色话机（接听按钮图标，前景色黑色）。
	IconPhoneBlack = fyne.NewStaticResource("phone-black.svg", []byte(appicon.PhoneSVG("#1a1a1a")))
	// IconPhoneWhite 振铃「接听」按钮（电话呼入，白色）。
	IconPhoneWhite = newIconPNG("phone-in")
	// IconPhoneCallWhite 主窗体「呼叫」按钮、通信录/通话记录行的呼叫按钮（电话呼出，白色）。
	IconPhoneCallWhite = newIconPNG("phone-out")
	// IconMicOn 麦克风开：白色前景（配蓝色底 HighImportance）。
	IconMicOn = fyne.NewStaticResource("mic-on.svg", []byte(micIconSVG("#ffffff", false)))
	// IconMicOff 麦克风禁用：**同一字形 + 斜杠**（位置与开状态完全一致，
	// 颜色由无底色按钮按强调色（红）着色）。
	IconMicOff = withSlash(IconMicOn)
	// IconSpeakerOn 扬声器开：白色前景（配蓝色底 HighImportance）。
	IconSpeakerOn = fyne.NewStaticResource("speaker-on.svg", []byte(speakerIconSVG("#ffffff", false)))
	// IconSpeakerOff 扬声器禁用：同一字形 + 斜杠。
	IconSpeakerOff = withSlash(IconSpeakerOn)
	// IconPlayWhite 白色播放三角（呼叫按钮：绿底白图标）。
	IconPlayWhite = fyne.NewStaticResource("play-white.svg", []byte(mediaIconSVG("#ffffff", "play")))
	// IconPlayBlack 黑色播放三角（主窗体呼叫按钮：与按钮文字颜色一致，REQ-UI-024 修订）。
	IconPlayBlack = fyne.NewStaticResource("play-black.svg", []byte(mediaIconSVG("#1a1a1a", "play")))
	// IconStopWhite 白色停止方块（挂断按钮：红底白图标）。
	IconStopWhite = fyne.NewStaticResource("stop-white.svg", []byte(mediaIconSVG("#ffffff", "stop")))
	// IconPauseWhite 白色暂停（保持按钮：黄底白图标）。
	IconPauseWhite = fyne.NewStaticResource("pause-white.svg", []byte(mediaIconSVG("#ffffff", "pause")))
	// IconReplayWhite 白色恢复箭头（取消保持按钮）。
	IconReplayWhite = fyne.NewStaticResource("replay-white.svg", []byte(mediaIconSVG("#ffffff", "replay")))

	// ---- 通话记录行方向图标（REQ-UI-031）----
	// 两个图元分开出图、由 ui_history.go 叠放合成（不能用 SVG 的 <g transform>）：
	// 话机为主体，箭头贴右上角（↗ 呼出 / ↙ 呼入）。颜色表示结果：
	// 绿=已接通、红=未接通。
	// IconStar 常用联系人标记（金色五角星）：通信录行、通话记录行与通话面板
	// 上跟在名称后面（REQ-UI-032/037）。
	IconStar = fyne.NewStaticResource("star.svg", []byte(starIconSVG("#fbc02d")))
	// IconContacts 通信录入口按钮；IconHistory 通话记录入口按钮。
	// 加留白让图形与 Fyne 内置的设置齿轮视觉大小一致（PNG 图形铺满画布显大）。
	IconContacts = newIconPNGPadded("contacts", 0.8)
	IconHistory  = newIconPNGPadded("history", 0.8)

	// IconCallIn/Out、IconVideoIn/Out 通话记录方向图标（白色 PNG）：呼入/呼出 ×
	// 语音/视频，方向由图形本身表达。已接通 = 白，未接通 = 红（Red 后缀版本）。
	IconCallIn      = newIconPNG("phone-in")
	IconCallOut     = newIconPNG("phone-out")
	IconVideoIn     = newIconPNG("video-in")
	IconVideoOut    = newIconPNG("video-out")
	IconCallInRed   = newIconPNGTinted("phone-in", colorDangerRed)
	IconCallOutRed  = newIconPNGTinted("phone-out", colorDangerRed)
	IconVideoInRed  = newIconPNGTinted("video-in", colorDangerRed)
	IconVideoOutRed = newIconPNGTinted("video-out", colorDangerRed)

	// IconHangup 强制挂断 / 移除成员：**圆圈里一个减号**（程序生成的白色图标，
	// 无底色按钮按强调色着色）。原挂断话机图标不再使用（用户要求换图形）。
	IconHangup = newCircleMinusIcon()

	// ---- CTI 话务按钮图标（REQ-ACC-026，iconfont 导出的白色 PNG）----
	IconCTIConsult         = newIconPNG("consult")
	IconCTIConsultTransfer = newIconPNG("consult-transfer")
	// 三方图标原图有效区域偏小（宽扁），裁剪后按画布铺满，视觉大小向其它图标看齐
	IconCTIThreeWay        = newIconPNGFit("threeway", 0.02)
	IconCTITransfer        = newIconPNG("transfer")
	IconCTIConference      = newIconPNG("conference")
	IconCTIVideoConference = newIconPNG("conference-video")
	IconCTIMonitor         = newIconPNG("monitor")
	// IconCTITakeover 代接（icons/代接.png，用户提供）：在线座席行上代接振铃中的坐席。
	IconCTITakeover  = newIconPNG("takeover")
	IconCTIWhisper   = newIconPNG("whisper")
	IconCTIIntercept = newIconPNG("intercept")
	IconCTIIntervene = newIconPNG("intervene")
	IconCTIAddMember = newIconPNG("add-member")
	// IconHost 会议 owner 标记（蓝色主持人图标）。
	IconHost = newIconPNG("host")
	// IconCTIExitConference 退出会议（也用作「结束咨询」——两者都是"离开当前话务"）。
	IconCTIExitConference = newIconPNG("exit-conference")
	// IconCTIEndConference 结束会议（owner）。
	IconCTIEndConference = newIconPNG("end-conference")

	// 会控开关（白图标，可配任意底色）：静音 / 禁听 / 禁视频。
	// 未生效时用**不带斜杠**的普通图标（灰），生效时换成带斜杠的版本（红）。
	IconCTIMic        = fyne.NewStaticResource("cti-mic.svg", []byte(micIconSVG("#ffffff", false)))
	IconCTISpeaker    = fyne.NewStaticResource("cti-speaker.svg", []byte(speakerIconSVG("#ffffff", false)))
	IconCTIVideo      = fyne.NewStaticResource("cti-video.svg", []byte(videoIconSVG("#ffffff", false)))
	IconCTIMicOff     = fyne.NewStaticResource("cti-mic-off.svg", []byte(micIconSVG("#ffffff", true)))
	IconCTISpeakerOff = fyne.NewStaticResource("cti-speaker-off.svg", []byte(speakerIconSVG("#ffffff", true)))
	IconCTIVideoOff   = fyne.NewStaticResource("cti-video-off.svg", []byte(videoIconSVG("#ffffff", true)))

	// IconAgentSeat 坐席模式按钮（icons/座席.png，用户提供）：白图，按状态
	// 染成绿色（空闲）或黄色（置忙）；非坐席模式（agentMode=none）时隐藏。
	IconAgentSeat = newIconPNG("agent-seat")
	// IconAgentRoster 在线座席入口（icons/座位表.png，用户提供）：坐席模式下
	// 显示在坐席状态按钮右侧，点击打开在线座席页。
	IconAgentRoster = newIconPNG("agent-roster")
	// IconArrowRight 右箭头（监听面板表示"主叫 → 被叫"，用矢量图标不用字符）。
	IconArrowRight = fyne.NewStaticResource("arrow-right.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="#ffffff" d="M4 11v2h12l-5.5 5.5 1.42 1.42L19.84 12l-7.92-7.92L10.5 5.5 16 11H4z"/>
</svg>`))
	// IconAutoAnswerOn 自动接听按钮（icons/自动接听开启.png，用户提供）：
	// 顶栏图形按钮，开启时染绿、关闭时置灰，悬停给文字提示。
	IconAutoAnswerOn = newIconPNG("auto-answer")
)

// withSlash 在白色图标上叠一条"禁用斜杠"：取同一个字形（位置不跳），把斜杠
// path 追加进 SVG——我们的图标都是简单的 `<svg><path fill="#…" d="…"/></svg>`，
// 染色时 fill 会被一起替换。
func withSlash(res fyne.Resource) fyne.Resource {
	if res == nil {
		return res
	}
	content := string(res.Content())
	i := strings.LastIndex(content, "</svg>")
	if i < 0 {
		return res
	}
	const slash = `<path fill="#ffffff" d="M4.2 1.8 L6 0 L22.2 16.2 L20.4 18 Z"/>`
	out := content[:i] + slash + content[i:]
	return fyne.NewStaticResource("slash-"+res.Name(), []byte(out))
}

// starIconSVG 常用联系人标记（REQ-UI-032）：实心五角星。
//
// **不要用文字 "★"**：Fyne 的主题图标集里没有星形，默认字体的回退链在
// 精简的 Linux 环境下不保证有 U+2605（会渲染成方框）；自绘 SVG 单 <path>
// 最稳（注意不要用 <g transform>，Fyne 的 SVG 渲染器不认）。
func starIconSVG(color string) string {
	path := `M12 17.27L18.18 21l-1.64-7.03L22 9.24l-7.19-.61L12 2 9.19 8.63 2 9.24l5.46 4.73L5.82 21z`
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + path + `"/>
</svg>`
}

// mediaIconSVG 播放控制图标（Material Design play/stop/pause/replay path）。
func mediaIconSVG(color, kind string) string {
	paths := map[string]string{
		"play":   `M8 5v14l11-7z`,
		"stop":   `M6 6h12v12H6z`,
		"pause":  `M6 19h4V5H6v14zm8-14v14h4V5h-4z`,
		"replay": `M12 5V1L7 6l5 5V7c3.31 0 6 2.69 6 6s-2.69 6-6 6-6-2.69-6-6H4c0 4.42 3.58 8 8 8s8-3.58 8-8-3.58-8-8-8z`,
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="` + paths[kind] + `"/>
</svg>`
}
