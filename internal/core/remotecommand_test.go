package core

import (
	"testing"
)

// newRingingApp 构造带一条振铃中呼入的 App（远端接听指令的目标）。
func newRingingApp(t *testing.T, hasVideo bool) (*App, *regTel) {
	t.Helper()
	app, tel := newRegTestApp(t)
	app.upsertCall(&Call{
		ID: "c1", Direction: "incoming", Remote: "2901",
		State: CallRinging, HasVideo: hasVideo,
	})
	return app, tel
}

// TestRemoteCommandDial 服务端拨号指令（MSP_DIAL，REQ-ACC-025）：
// withVideo=true 且视频功能可用（构建支持 + 配置总开关）→ 视频呼叫；
// 否则**静默降级**为纯音频呼叫。
func TestRemoteCommandDial(t *testing.T) {
	// 视频不可用 → 降级为纯音频
	app, tel := newRegTestApp(t)
	app.OnRemoteCommand(RemoteCommand{Kind: "dial", DestNo: "1001", WithVideo: true})
	if len(tel.dialed) != 1 || tel.dialed[0] != "1001" {
		t.Fatalf("视频不可用时应降级为纯音频拨号，实际 dialed=%v dialedVideo=%v", tel.dialed, tel.dialedVideo)
	}
	if len(tel.dialedVideo) != 0 {
		t.Fatalf("视频不可用时不应发起视频呼叫，实际 %v", tel.dialedVideo)
	}

	// 视频可用 → 视频呼叫
	app2, tel2 := newRegTestApp(t)
	tel2.videoSupported, tel2.videoEnabled = true, true
	app2.OnRemoteCommand(RemoteCommand{Kind: "dial", DestNo: "1002", WithVideo: true})
	if len(tel2.dialedVideo) != 1 || tel2.dialedVideo[0] != "1002" {
		t.Fatalf("视频可用时应发起视频呼叫，实际 dialed=%v dialedVideo=%v", tel2.dialed, tel2.dialedVideo)
	}

	// withVideo=false → 纯音频（与视频能力无关）
	app3, tel3 := newRegTestApp(t)
	tel3.videoSupported, tel3.videoEnabled = true, true
	app3.OnRemoteCommand(RemoteCommand{Kind: "dial", DestNo: "1003"})
	if len(tel3.dialed) != 1 || len(tel3.dialedVideo) != 0 {
		t.Fatalf("withVideo=false 应为纯音频拨号，实际 dialed=%v dialedVideo=%v", tel3.dialed, tel3.dialedVideo)
	}
}

// TestRemoteCommandAnswer 服务端接听指令（MSP_ANSWER）：withVideo=true 且
// **话机视频功能可用 + 本呼入含视频** → 视频接听；否则静默降级为普通接听
// （answer 里 m=video 0，不接受视频）。
func TestRemoteCommandAnswer(t *testing.T) {
	// 视频可用 + 呼入含视频 → 视频接听
	app, tel := newRingingApp(t, true)
	tel.videoSupported, tel.videoEnabled = true, true
	app.OnRemoteCommand(RemoteCommand{Kind: "answer", WithVideo: true})
	if len(tel.answeredVideo) != 1 || tel.answeredVideo[0] != "c1" {
		t.Fatalf("应视频接听，实际 answered=%v answeredVideo=%v", tel.answered, tel.answeredVideo)
	}

	// 视频可用但呼入不含视频 → 普通接听（降级）
	app2, tel2 := newRingingApp(t, false)
	tel2.videoSupported, tel2.videoEnabled = true, true
	app2.OnRemoteCommand(RemoteCommand{Kind: "answer", WithVideo: true})
	if len(tel2.answered) != 1 || len(tel2.answeredVideo) != 0 {
		t.Fatalf("呼入无视频时应降级为普通接听，实际 answered=%v answeredVideo=%v", tel2.answered, tel2.answeredVideo)
	}

	// 视频功能不可用 → 普通接听（降级）
	app3, tel3 := newRingingApp(t, true)
	app3.OnRemoteCommand(RemoteCommand{Kind: "answer", WithVideo: true})
	if len(tel3.answered) != 1 || len(tel3.answeredVideo) != 0 {
		t.Fatalf("视频不可用时应降级为普通接听，实际 answered=%v answeredVideo=%v", tel3.answered, tel3.answeredVideo)
	}
}

// TestRemoteCommandHangupAutoAnswer 挂断与自动接听指令（MSP_HANGUP /
// MSP_SET_AUTOANSWER）；没有可挂断的通话时只记日志、不报错。
func TestRemoteCommandHangupAutoAnswer(t *testing.T) {
	app, tel := newRegTestApp(t)
	app.upsertCall(&Call{ID: "c9", Direction: "outgoing", Remote: "1001", State: CallEstablished})
	app.OnRemoteCommand(RemoteCommand{Kind: "hangup"})
	if len(tel.hangups) != 1 || tel.hangups[0] != "c9" {
		t.Fatalf("挂断指令应挂断当前通话，实际 %v", tel.hangups)
	}

	// 没有通话时不应 panic、不应有动作
	app.OnRemoteCommand(RemoteCommand{Kind: "hangup"})

	on := true
	app.OnRemoteCommand(RemoteCommand{Kind: "autoanswer", AutoAnswer: &on})
	if tel.autoAnswer == nil || !*tel.autoAnswer {
		t.Fatalf("自动接听指令应打开开关，实际 %v", tel.autoAnswer)
	}
	// 缺少参数 → 忽略（不改变状态）
	app.OnRemoteCommand(RemoteCommand{Kind: "autoanswer"})
	if tel.autoAnswer == nil || !*tel.autoAnswer {
		t.Fatalf("缺参数不应改变自动接听状态，实际 %v", tel.autoAnswer)
	}
}

// TestRemoteCommandUnregister 服务端注销指令（MSP_UNREGISTER）：退出账号
// （禁用账户并立即注销）。
func TestRemoteCommandUnregister(t *testing.T) {
	app, tel := newRegTestApp(t)
	app.OnRemoteCommand(RemoteCommand{Kind: "unregister", Reason: "logout"})
	if app.Config().Account.Enabled {
		t.Fatal("注销指令后「启用账户」应为 false")
	}
	if _, unregs := tel.counts(); unregs != 1 {
		t.Fatalf("注销指令应注销一次，实际 %d 次", unregs)
	}
}
