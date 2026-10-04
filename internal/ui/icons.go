package ui

import (
	"fmt"
	"strings"

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

// rotateIconSVG 画面旋转图标（Material Design rotate_right path）。
// 视频窗口工具栏用：远端画面与本地回显各一个。
func rotateIconSVG(color string) string {
	path := `M15.55 5.55L11 1v3.07C7.06 4.56 4 7.92 4 12s3.05 7.44 7 7.93v-2.02c-2.84-.48-5-2.94-5-5.91s2.16-5.43 5-5.91V10l4.55-4.45zM19.93 11a7.9 7.9 0 0 0-1.62-3.89l-1.42 1.42c.54.75.88 1.6 1.02 2.47h2.02zM13 17.91v2.02c1.39-.22 2.68-.75 3.79-1.5l-1.44-1.44c-.71.51-1.51.84-2.35.92zm3.77-2.88l1.42 1.42c.75-1.11 1.28-2.4 1.5-3.79h-2.02c-.13.87-.47 1.72-1.02 2.47z`
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
	IconVideoCall = fyne.NewStaticResource("video-call.svg", []byte(videoIconSVG("#1a1a1a", false)))
	// IconVideoGreen 视频窗口图标（绿色摄像头；不继承应用的红色话机图标）。
	IconVideoGreen = fyne.NewStaticResource("video-green.svg", []byte(videoIconSVG("#43a047", false)))
	// IconVideoAnswer 来电「视频接听」按钮（绿底白图标）。
	IconVideoAnswer = fyne.NewStaticResource("video-answer.svg", []byte(videoIconSVG("#ffffff", false)))

	// IconRotate 画面旋转（视频窗口工具栏：远端画面 / 本地回显各一个按钮）。
	IconRotate = fyne.NewStaticResource("rotate.svg", []byte(rotateIconSVG("#ffffff")))
	// IconMirror 水平镜像（视频窗口工具栏）。
	IconMirror = fyne.NewStaticResource("mirror.svg", []byte(mirrorIconSVG("#ffffff")))
	// IconPin 视频窗口置顶开关（工具栏）。
	IconPin = fyne.NewStaticResource("pin.svg", []byte(pinIconSVG("#ffffff")))

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

	// ---- 通话记录行方向图标（REQ-UI-031）----
	// 两个图元分开出图、由 ui_history.go 叠放合成（不能用 SVG 的 <g transform>）：
	// 话机为主体，箭头贴右上角（↗ 呼出 / ↙ 呼入）。颜色表示结果：
	// 绿=已接通、红=未接通。
	// IconVideoMark 视频通话标记（第一行号码右边的小摄像头）：这里给的填充色
	// 会被 theme.NewColoredResource 换成主题前景色，与行内文字同色。
	IconVideoMark = fyne.NewStaticResource("video-mark.svg", []byte(videoIconSVG("#1a1a1a", false)))
	// IconStar 常用联系人标记（金色五角星）：通信录行、通话记录行与通话面板
	// 上跟在名称后面（REQ-UI-032/037）。
	IconStar = fyne.NewStaticResource("star.svg", []byte(starIconSVG("#fbc02d")))
	// IconContacts 通信录入口按钮（顶栏，颜色同其它主题图标）。
	IconContacts = fyne.NewStaticResource("contacts.svg", []byte(contactsIconSVG("#b3b3b3")))

	// IconArrowOutGreen/Red 呼出箭头（↗）；IconArrowInGreen/Red 呼入箭头（↙）。
	IconArrowOutGreen = fyne.NewStaticResource("arrow-out-green.svg", []byte(arrowIconSVG("#43a047", false)))
	IconArrowOutRed   = fyne.NewStaticResource("arrow-out-red.svg", []byte(arrowIconSVG("#e53935", false)))
	IconArrowInGreen  = fyne.NewStaticResource("arrow-in-green.svg", []byte(arrowIconSVG("#43a047", true)))
	IconArrowInRed    = fyne.NewStaticResource("arrow-in-red.svg", []byte(arrowIconSVG("#e53935", true)))
)

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

// contactsIconSVG 通信录图标（按用户给的参考图：一本通讯录 + 右侧索引格 + 中间听筒）。
//
// **线稿风格要靠"细长矩形拼描边"**：Fyne 的 ThemedResource 只把 **fill** 换成
// 主题色（internal/svg 的 replaceFillColor 只处理填充，不认 stroke），用
// stroke 画的线不会跟着主题变色。听筒是 Material phone 路径按 0.5 缩放、平移到
// 封面中央的（坐标由脚本换算，别手改）。
// contactsHandsetPath 听筒路径：Material phone 按 0.5 缩放 + 平移到封面中央
// （坐标由脚本换算，手改容易错位）。
const contactsHandsetPath = "M 7.46 11.395 A 7.525 7.525 0 0 0 10.755 14.69 L 11.855 13.59 A 0.5 0.5 0 0 1 12.365 13.47 C 12.925 13.655 13.53 13.755 14.15 13.755 A 0.5 0.5 0 0 1 14.65 14.255 L 14.65 16 A 0.5 0.5 0 0 1 14.15 16.5 A 8.5 8.5 0 0 1 5.65 8 A 0.5 0.5 0 0 1 6.15 7.5 L 7.9 7.5 A 0.5 0.5 0 0 1 8.4 8 C 8.4 8.62 8.5 9.225 8.685 9.785 A 0.5 0.5 0 0 1 8.565 10.295 L 7.46 11.395 Z"

func contactsIconSVG(color string) string {
	f := func(x, y, w, h, r float32) string {
		return fmt.Sprintf(`<rect x="%g" y="%g" width="%g" height="%g" rx="%g" fill="%s"/>`,
			x, y, w, h, r, color)
	}
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">` + "\n")
	// 封面：左侧书脊 + 上/下/右三条边框
	b.WriteString(f(2.2, 2.5, 2.4, 19, 1.2) + "\n")
	b.WriteString(f(2.2, 2.5, 15.4, 2.2, 1.1) + "\n")
	b.WriteString(f(2.2, 19.3, 15.4, 2.2, 1.1) + "\n")
	b.WriteString(f(15.4, 2.5, 2.2, 19, 1.1) + "\n")
	// 右侧 4 个索引格
	for _, y := range []float32{5.2, 9.4, 13.6, 17.8} {
		b.WriteString(f(18, y, 3.8, 2.2, 0.7) + "\n")
	}
	// 中间听筒
	b.WriteString(`<path fill="` + color + `" d="` + contactsHandsetPath + `"/>` + "\n")
	b.WriteString(`</svg>`)
	return b.String()
}

// arrowIconSVG 通话记录行的方向箭头（REQ-UI-031）：↗ 呼出 / ↙ 呼入。
//
// 形状参考常见的"去电/来电"图标：**粗箭头**（三角箭头 + 粗杆），
// 不是 Material 的细线箭头。杆与三角头在斜边上正好接合（杆的右上角落在
// 三角的斜边上），合成一个整体。
//
// 注意：**不要把基图形与箭头用 SVG 的 <g transform> 拼在一个文件里**——
// Fyne 的 SVG 渲染器不认这个变换，结果是整张图空白（实测）。两个图元在
// ui_history.go 里用两个 canvas.Image 叠放合成。
func arrowIconSVG(colour string, incoming bool) string {
	path := `M22 2 L12 2 L22 12 Z M19 9 L9 19 L6 16 L16 6 Z` // ↗ 呼出（三角头 10×10）
	if incoming {
		path = `M2 22 L12 22 L2 12 Z M5 15 L15 5 L18 8 L8 18 Z` // ↙ 呼入（180° 旋转）
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24">
<path fill="` + colour + `" d="` + path + `"/>
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
