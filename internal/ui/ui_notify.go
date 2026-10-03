package ui

import (
	"fmt"
	"strings"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 未接来电桌面通知（REQ-UI-010）的文案与派发。是否未接由核心判定
// （core.missedOnEnd，事件里的 missed 字段），这里只管展示；
// 平台实现见 notify_windows.go（WinRT toast）与 notify_other.go（Fyne/D-Bus）。

// notifyBodyMax 通知正文的长度上限（按 rune）：系统横幅对过长的正文
// 会截断得很难看，这里先按合理长度截。
const notifyBodyMax = 64

// missedNotificationText 未接来电通知文案（REQ-UI-010）：
//   - 标题：「未接来电」；
//   - 正文：「主叫 - 振铃 N 秒」，ringFor 为零（界面没记到振铃起点）时省略时长；
//     主叫为「显示名（号码）」，两者只有其一（或相同）时就用其一。
//
// 通知**第一行**（应用名）由系统显示，不走这里：Windows 取 AUMID 注册表的
// DisplayName（固定「mti-sip-phone 软电话」，不随账号变化）。
func missedNotificationText(c *core.Call, ringFor time.Duration) (title, body string) {
	title = "未接来电"
	if c == nil {
		return title, ""
	}
	// 主叫：有显示名且与号码不同时用「显示名（号码）」，号码始终可见
	who := c.Remote
	if c.RemoteName != "" && c.RemoteName != c.Remote {
		if who != "" {
			who = c.RemoteName + "（" + c.Remote + "）"
		} else {
			who = c.RemoteName
		}
	}
	if who == "" {
		who = "未知号码"
	}
	body = who
	if ringFor > 0 {
		body += " - 振铃 " + formatDurationCN(ringFor)
	}
	return title, sanitizeNotifyText(body, notifyBodyMax)
}

// formatDurationCN 时长中文文案（未接通知的振铃时长与通话记录共用）：
// 不足 1 秒按 1 秒（响过/通过就有时长，避免出现「0 秒」），不足 1 分钟用秒，
// 不足 1 小时用「N 分 N 秒」，再长用「N 小时 N 分」（不然会念成「65 分 3 秒」）。
func formatDurationCN(d time.Duration) string {
	sec := int(d.Seconds())
	if sec < 1 {
		sec = 1
	}
	switch {
	case sec < 60:
		return fmt.Sprintf("%d 秒", sec)
	case sec < 3600:
		return fmt.Sprintf("%d 分 %d 秒", sec/60, sec%60)
	default:
		return fmt.Sprintf("%d 小时 %d 分", sec/3600, (sec%3600)/60)
	}
}

// sanitizeNotifyText 清理来自 SIP 报文的文本：去掉控制字符（换行会破坏
// 通知排版）、按 rune 截断。
//
// 注意：这只是排版与纵深防御——真正的命令注入防护在 notify_windows.go
// （脚本是编译期常量、数据走环境变量，不参与命令解析）。
func sanitizeNotifyText(s string, max int) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
	if max > 0 {
		if r := []rune(cleaned); len(r) > max {
			cleaned = string(r[:max])
		}
	}
	return strings.TrimSpace(cleaned)
}

// notifyMissedCall 发送未接来电桌面通知。
//
// 在 Fyne 主线程调用（文案要读界面状态），平台发送在内部转后台 goroutine：
// Windows 要拉起 PowerShell 进程、Linux 是同步 D-Bus 调用，都不能占主线程。
func (a *App) notifyMissedCall(c *core.Call, ringFor time.Duration) {
	title, body := missedNotificationText(c, ringFor)
	if body == "" {
		return
	}
	logx.Logger().Info("未接来电，发送桌面通知", "title", title, "body", body)
	go notifyPlatform(title, body)
}
