//go:build windows

package ui

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"

	"mti-sip-phone/internal/logx"
)

// notifyAppID 通知用的 AppUserModelID，必须与 ui.New 里
// app.NewWithID("com.mti.sip-phone") 一致（Fyne 的应用 ID 也是它）。
const notifyAppID = "com.mti.sip-phone"

// toastScript 是**编译期常量**脚本：标题/正文/AppID 全部从环境变量取，
// 命令行与脚本里不含任何来自 SIP 报文的字符串。主叫可以把 From 显示名
// 设成任意内容，把它拼进脚本就是一条命令注入通道——Fyne 自带的 Windows
// 通知实现正是这么做的（Go 的 %q 转义出的 \" 在 PowerShell 里不是转义符），
// 所以不能用它。文本节点用 CreateTextNode 生成，XML 转义交给 DOM。
//
// 脚本内只用单引号（不出现双引号），避免 CreateProcess 命令行转义的坑；
// 模板用 ToastText02（标题 + 正文，不带图）：本仓库图标是 SVG，Windows
// toast 的图片只认 PNG/JPG，带图反而会让整条通知失效。
const toastScript = `
$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$template = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$xml = [xml]$template.GetXml()
$xml.GetElementsByTagName('text')[0].AppendChild($xml.CreateTextNode($env:MTI_NOTIFY_TITLE)) > $null
$xml.GetElementsByTagName('text')[1].AppendChild($xml.CreateTextNode($env:MTI_NOTIFY_BODY)) > $null
$doc = New-Object Windows.Data.Xml.Dom.XmlDocument
$doc.LoadXml($xml.OuterXml)
$toast = [Windows.UI.Notifications.ToastNotification]::new($doc)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($env:MTI_NOTIFY_APPID).Show($toast)
`

const (
	notifyEnvTitle = "MTI_NOTIFY_TITLE"
	notifyEnvBody  = "MTI_NOTIFY_BODY"
	notifyEnvAppID = "MTI_NOTIFY_APPID"

	notifyTimeout = 15 * time.Second
)

var notifyRegOnce sync.Once

// ensureNotifyRegistered 注册通知用的 AUMID（首次发通知前，幂等）。
//
// 未打包的便携 exe（本程序没有「开始菜单快捷方式」）若不注册 AUMID，
// CreateToastNotifier 会**静默失败**：不弹、也不报错。写 HKCU 免管理员，
// 键值含义见
// https://learn.microsoft.com/windows/apps/develop/notifications/app-notifications/send-local-toast-other-apps
//
// DisplayName 是通知抬头那行显示的应用名，**固定值**（不随本机号码变化：
// 注册表里的应用标识应当稳定，账号只出现在通知标题里）。
func ensureNotifyRegistered() {
	notifyRegOnce.Do(func() {
		k, _, err := registry.CreateKey(registry.CURRENT_USER,
			`Software\Classes\AppUserModelId\`+notifyAppID, registry.SET_VALUE)
		if err != nil {
			logx.Logger().Warn("注册通知应用 ID 失败，桌面通知可能不显示", "err", err)
			return
		}
		defer k.Close()
		_ = k.SetStringValue("DisplayName", "mti-sip-phone 软电话")
		_ = k.SetDWordValue("ShowInSettings", 1)
		_ = k.SetStringValue("IconBackgroundColor", "#2E7D32")
		// 不写 IconUri：必须是真 PNG/JPG 的绝对路径（仓库图标是 SVG）；
		// 不写 CustomActivator：本通知不支持点击激活，写了反而要求注册 COM 类工厂。
	})
}

// notifyPlatform 发送 Windows 通知：PowerShell + WinRT toast。
// 失败只记日志（带 stderr，便于排查"静默不显示"）。
func notifyPlatform(title, body string) {
	ensureNotifyRegistered()

	ps, err := exec.LookPath("powershell")
	if err != nil {
		logx.Logger().Warn("未找到 PowerShell，无法发送桌面通知", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command", toastScript)
	cmd.Env = append(os.Environ(),
		notifyEnvTitle+"="+title,
		notifyEnvBody+"="+body,
		notifyEnvAppID+"="+notifyAppID,
	)
	// GUI 子系统构建下也不该闪控制台窗口
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		logx.Logger().Warn("发送桌面通知失败", "err", err, "out", strings.TrimSpace(string(out)))
		return
	}
	logx.Logger().Debug("桌面通知已发送", "title", title)
}
