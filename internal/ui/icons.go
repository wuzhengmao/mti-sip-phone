package ui

import (
	"fyne.io/fyne/v2"
)

// 托盘图标：话机形状（Material Design phone 图标 path），
// 未注册为红色，注册成功后为绿色（REQ-UI-011）。
// SVG 静态资源由 Fyne driver 渲染为托盘位图。

func phoneIconSVG(color string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + color + `" d="M6.62 10.79a15.05 15.05 0 0 0 6.59 6.59l2.2-2.2a1 1 0 0 1 1.02-.24c1.12.37 2.33.57 3.57.57a1 1 0 0 1 1 1V20a1 1 0 0 1-1 1A17 17 0 0 1 3 4a1 1 0 0 1 1-1h3.5a1 1 0 0 1 1 1c0 1.24.2 2.45.57 3.57a1 1 0 0 1-.24 1.02l-2.21 2.2z"/>
</svg>`
}

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

var (
	// IconVideoOn 摄像头开：白色前景（配蓝色底）。
	IconVideoOn = fyne.NewStaticResource("video-on.svg", []byte(videoIconSVG("#ffffff", false)))
	// IconVideoOff 摄像头关：红色斜线（浅灰底上清晰可见）。
	IconVideoOff = fyne.NewStaticResource("video-off.svg", []byte(videoIconSVG("#e53935", true)))
	// IconVideoCall 主窗体视频呼叫按钮（绿底黑图标，与呼叫按钮同色系）。
	IconVideoCall = fyne.NewStaticResource("video-call.svg", []byte(videoIconSVG("#1a1a1a", false)))
	// IconVideoGreen 视频窗口图标（绿色摄像头；不继承应用的红色话机图标）。
	IconVideoGreen = fyne.NewStaticResource("video-green.svg", []byte(videoIconSVG("#43a047", false)))
	// IconVideoAnswer 来电「视频接听」按钮（绿底白图标）。
	IconVideoAnswer = fyne.NewStaticResource("video-answer.svg", []byte(videoIconSVG("#ffffff", false)))

	// IconPhoneRed 未注册状态托盘图标（红色话机）。
	IconPhoneRed = fyne.NewStaticResource("phone-red.svg", []byte(phoneIconSVG("#e53935")))
	// IconPhoneGreen 已注册状态托盘图标（绿色话机）。
	IconPhoneGreen = fyne.NewStaticResource("phone-green.svg", []byte(phoneIconSVG("#43a047")))
	// IconPhoneBlack 黑色话机（接听按钮图标，前景色黑色）。
	IconPhoneBlack = fyne.NewStaticResource("phone-black.svg", []byte(phoneIconSVG("#1a1a1a")))
	// IconPhoneWhite 白色话机（接听按钮：绿底白图标，REQ-UI-023 修订）。
	IconPhoneWhite = fyne.NewStaticResource("phone-white.svg", []byte(phoneIconSVG("#ffffff")))
	// IconMicOn 麦克风开：白色前景（配蓝色底 HighImportance）。
	IconMicOn = fyne.NewStaticResource("mic-on.svg", []byte(micIconSVG("#ffffff", false)))
	// IconMicOff 麦克风关：红色斜线（浅灰底上清晰可见）。
	IconMicOff = fyne.NewStaticResource("mic-off.svg", []byte(micIconSVG("#e53935", true)))
	// IconSpeakerOn 扬声器开：白色前景（配蓝色底 HighImportance）。
	IconSpeakerOn = fyne.NewStaticResource("speaker-on.svg", []byte(speakerIconSVG("#ffffff", false)))
	// IconSpeakerOff 扬声器关：红色（浅灰底上清晰可见）。
	IconSpeakerOff = fyne.NewStaticResource("speaker-off.svg", []byte(speakerIconSVG("#e53935", true)))
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
)

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
