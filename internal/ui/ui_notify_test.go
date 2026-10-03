package ui

import (
	"strings"
	"testing"
	"time"

	"mti-sip-phone/internal/core"
)

// TestMissedNotificationText 未接来电通知文案（REQ-UI-010）：
// 标题「未接来电」，正文「主叫 - 振铃 N 秒」。
func TestMissedNotificationText(t *testing.T) {
	const title = "未接来电"
	cases := []struct {
		name    string
		call    *core.Call
		ringFor time.Duration
		body    string
	}{
		{"名字+号码+时长", &core.Call{Remote: "1002", RemoteName: "张三"}, 3 * time.Second, "张三（1002） - 振铃 3 秒"},
		{"只有号码", &core.Call{Remote: "1002"}, 4 * time.Second, "1002 - 振铃 4 秒"},
		{"只有名字", &core.Call{RemoteName: "张三"}, 500 * time.Millisecond, "张三 - 振铃 1 秒"}, // 不足 1 秒按 1 秒
		{"名字与号码相同", &core.Call{Remote: "1002", RemoteName: "1002"}, time.Minute + 20*time.Second, "1002 - 振铃 1 分 20 秒"},
		{"无主叫信息", &core.Call{}, 2 * time.Second, "未知号码 - 振铃 2 秒"},
		{"没有振铃时长", &core.Call{Remote: "1002"}, 0, "1002"},
		{"空通话", nil, time.Second, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTitle, gotBody := missedNotificationText(tc.call, tc.ringFor)
			if gotTitle != title {
				t.Fatalf("标题应为 %q，实际 %q", title, gotTitle)
			}
			if gotBody != tc.body {
				t.Fatalf("正文应为 %q，实际 %q", tc.body, gotBody)
			}
		})
	}
}

// TestMissedNotificationTextSanitized 来自 SIP 报文的文本要清理：
// 控制字符（换行等）不能被带进通知，超长按 rune 截断。
func TestMissedNotificationTextSanitized(t *testing.T) {
	c := &core.Call{Remote: "1002\r\nX", RemoteName: "张\t三\x07"}
	_, body := missedNotificationText(c, 3*time.Second)
	if strings.ContainsAny(body, "\r\n\x07") {
		t.Fatalf("控制字符未清理: %q", body)
	}
	if body != "张 三（1002X） - 振铃 3 秒" {
		t.Fatalf("清理结果不符: %q", body)
	}

	long := &core.Call{Remote: strings.Repeat("9", 200)}
	_, body = missedNotificationText(long, time.Second)
	if n := len([]rune(body)); n != notifyBodyMax {
		t.Fatalf("超长正文应截断到 %d 个字符，实际 %d", notifyBodyMax, n)
	}
}

// TestFormatRingDuration 振铃时长文案。
func TestFormatRingDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{200 * time.Millisecond, "1 秒"},
		{3500 * time.Millisecond, "3 秒"},
		{59 * time.Second, "59 秒"},
		{time.Minute, "1 分 0 秒"},
		{2*time.Minute + 5*time.Second, "2 分 5 秒"},
	}
	for _, tc := range cases {
		if got := formatRingDuration(tc.d); got != tc.want {
			t.Fatalf("%v 应格式化为 %q，实际 %q", tc.d, tc.want, got)
		}
	}
}

// TestSanitizeNotifyText 清理函数的边界。
func TestSanitizeNotifyText(t *testing.T) {
	if got := sanitizeNotifyText("  张三  ", 0); got != "张三" {
		t.Fatalf("首尾空白应去掉，实际 %q", got)
	}
	if got := sanitizeNotifyText("中\r\n文", 0); got != "中文" {
		t.Fatalf("换行应去掉，实际 %q", got)
	}
	if got := sanitizeNotifyText("abc", 2); got != "ab" {
		t.Fatalf("应按 rune 截断，实际 %q", got)
	}
	if got := sanitizeNotifyText("中文测试", 3); got != "中文测" {
		t.Fatalf("按 rune 而非字节截断，实际 %q", got)
	}
}
