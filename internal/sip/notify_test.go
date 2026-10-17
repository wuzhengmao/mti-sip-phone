package sip

import (
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

// newNotifyPhone 构造只用于 NOTIFY 解析的 Phone（账户服务器设为 127.0.0.1，
// 用于来源校验；不起注册循环）。
func newNotifyPhone(t *testing.T) (*Phone, *testSink) {
	t.Helper()
	sink := &testSink{}
	p := newTestPhone(t, sink)
	p.mu.Lock()
	p.acc = config.Account{Server: "127.0.0.1", Account: "2999"}
	p.mu.Unlock()
	return p, sink
}

// newControlNotify 构造一条来源为 127.0.0.1 的控制 NOTIFY。
func newControlNotify(event, src, body string) *sip.Request {
	req := sip.NewRequest(sip.NOTIFY, sip.Uri{User: "2999", Host: "127.0.0.1", Port: 5060})
	req.SetSource(src)
	if event != "" {
		req.AppendHeader(sip.NewHeader("Event", event))
	}
	if body != "" {
		req.AppendHeader(sip.NewHeader("Content-Type", "application/json"))
		req.SetBody([]byte(body))
	}
	return req
}

// TestControlNotifyProtocol 私有控制协议（docs/cti-sip-phone-protocol.md，
// REQ-ACC-025）的 5 个事件：Event 匹配、来源为注册服务器时经 sink 上报
// 对应指令；Event 带参数也能识别。
func TestControlNotifyProtocol(t *testing.T) {
	cases := []struct {
		name  string
		event string
		body  string
		pred  func(core.RemoteCommand) bool
	}{
		{"MSP_DIAL", "MSP_DIAL", `{"destNo":"91381005","withVideo":false}`,
			func(c core.RemoteCommand) bool { return c.Kind == "dial" && c.DestNo == "91381005" && !c.WithVideo }},
		{"MSP_DIAL 视频", "MSP_DIAL", `{"destNo":"123","withVideo":true}`,
			func(c core.RemoteCommand) bool { return c.Kind == "dial" && c.DestNo == "123" && c.WithVideo }},
		{"MSP_ANSWER", "MSP_ANSWER", `{"withVideo":false}`,
			func(c core.RemoteCommand) bool { return c.Kind == "answer" && !c.WithVideo }},
		{"MSP_ANSWER 视频", "MSP_ANSWER", `{"withVideo":true}`,
			func(c core.RemoteCommand) bool { return c.Kind == "answer" && c.WithVideo }},
		{"MSP_HANGUP（无 body）", "MSP_HANGUP", "",
			func(c core.RemoteCommand) bool { return c.Kind == "hangup" }},
		{"MSP_SET_AUTOANSWER", "MSP_SET_AUTOANSWER", `{"autoAnswer":true}`,
			func(c core.RemoteCommand) bool { return c.Kind == "autoanswer" && c.AutoAnswer != nil && *c.AutoAnswer }},
		{"MSP_UNREGISTER", "MSP_UNREGISTER", `{"reason":"logout"}`,
			func(c core.RemoteCommand) bool { return c.Kind == "unregister" && c.Reason == "logout" }},
		{"Event 带参数", "MSP_HANGUP;id=42", "",
			func(c core.RemoteCommand) bool { return c.Kind == "hangup" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, sink := newNotifyPhone(t)
			p.handleControlNotify(newControlNotify(tc.event, "127.0.0.1:5060", tc.body))
			if !sink.waitRemoteCmd(2*time.Second, tc.pred) {
				t.Fatalf("%s 未上报预期指令", tc.event)
			}
		})
	}
}

// TestControlNotifyIgnored 未知事件、非注册服务器来源、非法 JSON 一律忽略。
func TestControlNotifyIgnored(t *testing.T) {
	p, sink := newNotifyPhone(t)

	// 未知事件（200 由 handler 回，这里只验证不产生指令）
	p.handleControlNotify(newControlNotify("DIALOG", "127.0.0.1:5060", `{"destNo":"1"}`))
	// 非注销/控制事件
	p.handleControlNotify(newControlNotify("mti-sip-unregister", "127.0.0.1:5060", `{"reason":"x"}`))
	// 来源不是注册服务器
	p.handleControlNotify(newControlNotify("MSP_HANGUP", "10.9.9.9:5060", ""))
	// MSP_DIAL 正文非法
	p.handleControlNotify(newControlNotify("MSP_DIAL", "127.0.0.1:5060", "not-json"))
	// MSP_SET_AUTOANSWER 缺参数
	p.handleControlNotify(newControlNotify("MSP_SET_AUTOANSWER", "127.0.0.1:5060", `{}`))
	time.Sleep(150 * time.Millisecond) // 给异步回调一点时间（不该出现）
	if len(sink.remoteCmds) != 0 {
		t.Fatalf("不该上报任何控制指令，实际: %+v", sink.remoteCmds)
	}
}
