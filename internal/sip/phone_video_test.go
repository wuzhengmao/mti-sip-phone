package sip

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/media"
)

// TestVideoAnswerDirection RFC 3264 方向镜像规则（REQ-VIDEO-004/005）。
// 仅在「视频接听」时用得到：视频通话不接受时走的是拒绝段（见下一条测试）。
func TestVideoAnswerDirection(t *testing.T) {
	cases := []struct {
		offerDir string
		canSend  bool
		want     string
	}{
		{"sendrecv", true, "sendrecv"},
		{"sendrecv", false, "recvonly"}, // 视频接听但摄像头不可用：只收不发
		{"sendonly", true, "recvonly"},
		{"recvonly", true, "sendonly"},
		{"recvonly", false, "inactive"},
		{"inactive", true, "inactive"},
	}
	for _, c := range cases {
		if got := videoAnswerDirection(c.offerDir, c.canSend); got != c.want {
			t.Errorf("offer=%s canSend=%v：期望 %s，实际 %s", c.offerDir, c.canSend, c.want, got)
		}
	}
}

// TestAudioAnswerRejectsVideo 点普通「接听」= **不接受视频**：
// answer 里 m=video 端口置 0（RFC 3264 的拒绝写法），而不是 recvonly。
//
// 用户明确要求：视频来电点音频接听就表示不接受视频呼叫，返回的 SDP 不含 video
// 内容，视频窗口也不显示（Video.Negotiated 恒为 false）。
func TestAudioAnswerRejectsVideo(t *testing.T) {
	if !media.VideoSupported() {
		t.Skip("未启用视频的构建")
	}
	crlf := "\r\n"
	lines := []string{
		"v=0",
		"o=- 1 1 IN IP4 127.0.0.1",
		"s=-",
		"c=IN IP4 127.0.0.1",
		"t=0 0",
		"m=audio 50002 RTP/AVP 8",
		"a=rtpmap:8 PCMA/8000",
		"m=video 50000 RTP/AVP 96",
		"a=rtpmap:96 H264/90000",
		"a=fmtp:96 packetization-mode=1",
		"a=sendrecv",
	}
	body := []byte(strings.Join(lines, crlf) + crlf)

	offer, err := media.ParseSDP(body)
	if err != nil {
		t.Fatalf("解析 offer 失败: %v", err)
	}
	e, err := media.NewEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建音频引擎失败: %v", err)
	}
	defer e.Close()
	e.SetSDPIP("127.0.0.1")
	if _, err := e.SDPAnswer(body); err != nil {
		t.Fatalf("音频协商失败: %v", err)
	}

	// answerNoVideo 即普通接听；此时 cs.video 也已被回收（answerIncoming 里做的）
	cs := &callSession{id: "c1", engine: e, offer: offer}
	p := &Phone{}
	ans := p.buildAnswer(cs, answerNoVideo)

	if !strings.Contains(ans, "m=video 0 RTP/AVP 96") {
		t.Fatalf("音频接听应把 m=video 置 0 拒绝（不接受视频）：\n%s", ans)
	}
	if strings.Count(ans, "m=") != 2 {
		t.Fatalf("answer 的 m= 行数量应与 offer 一致（2 行）：\n%s", ans)
	}
	if strings.Index(ans, "m=audio") > strings.Index(ans, "m=video") {
		t.Fatalf("m= 行顺序应跟随 offer（audio 在前）：\n%s", ans)
	}
	if strings.Contains(ans, "a=rtpmap:96") {
		t.Fatalf("拒绝的视频段不应带 rtpmap：\n%s", ans)
	}
}

// TestAnswerVideoModes 两条接听路径的视频策略：
//
//	answerNoVideo  点普通「接听」→ 拒绝视频（m=video 0，见上一条测试）
//	answerSendRecv 「视频接听」/ 自动接听 → 摄像头可用时 sendrecv，
//	               被禁用/不可用时退化成 recvonly（只收不发）
//
// 回归：自动接听曾传 withVideo=false（= 拒绝视频），注释却写着 recvonly
// ——用户实测「视频呼叫自动接听只接通了音频」。
func TestAnswerVideoModes(t *testing.T) {
	if !media.VideoSupported() {
		t.Skip("未启用视频的构建")
	}
	crlf := "\r\n"
	lines := []string{
		"v=0",
		"o=- 1 1 IN IP4 127.0.0.1",
		"s=-",
		"c=IN IP4 127.0.0.1",
		"t=0 0",
		"m=audio 50002 RTP/AVP 8",
		"a=rtpmap:8 PCMA/8000",
		"m=video 50000 RTP/AVP 96",
		"a=rtpmap:96 H264/90000",
		"a=fmtp:96 packetization-mode=1",
		"a=sendrecv",
	}
	body := []byte(strings.Join(lines, crlf) + crlf)

	offer, err := media.ParseSDP(body)
	if err != nil {
		t.Fatalf("解析 offer 失败: %v", err)
	}
	e, err := media.NewEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建音频引擎失败: %v", err)
	}
	defer e.Close()
	e.SetSDPIP("127.0.0.1")
	if _, err := e.SDPAnswer(body); err != nil {
		t.Fatalf("音频协商失败: %v", err)
	}

	v, err := media.NewVideoEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建视频引擎失败: %v", err)
	}
	defer v.Close()
	vinfo, err := offer.Media("video")
	if err != nil {
		t.Fatalf("取视频段失败: %v", err)
	}
	if !v.ApplyOffer(vinfo) {
		t.Fatal("视频协商应成功")
	}

	cs := &callSession{id: "c1", engine: e, offer: offer, video: v}
	// 只看视频段的方向（音频段恒为 sendrecv，混在一起断言会误判）
	videoSec := func(s string) string {
		if i := strings.Index(s, "m=video"); i >= 0 {
			return s[i:]
		}
		return ""
	}

	// 摄像头被禁用：即使走「视频接听」也退化成 recvonly（REQ-VIDEO-005）
	pDisabled := &Phone{}
	pDisabled.SetVideoConfig(config.VideoConfig{CameraID: config.CameraDisabled})
	got := videoSec(pDisabled.buildAnswer(cs, answerSendRecv))
	if got == "" || strings.HasPrefix(got, "m=video 0 ") || !strings.Contains(got, "a=recvonly") {
		t.Fatalf("摄像头禁用时应只收不发：\n%s", got)
	}

	// 视频接听/自动接听 + 摄像头可用：双向
	p := &Phone{}
	got = videoSec(p.buildAnswer(cs, answerSendRecv))
	if !strings.Contains(got, "a=sendrecv") {
		t.Fatalf("视频接听应为双向（sendrecv）：\n%s", got)
	}
	// 视频段必须复用对端 PT 号、带上视频端口
	if !strings.Contains(got, "m=video "+strconv.Itoa(v.LocalPort())+" RTP/AVP 96") {
		t.Fatalf("视频段应复用对端 PT 96：\n%s", got)
	}
}

// TestVideoCallSwitchOff 「启用视频通话」总开关关闭（REQ-VIDEO-014）：
//   - 视频呼叫直接失败（ErrVideoDisabled，不建媒体引擎、不发 INVITE）；
//   - 来电 offer 里的视频段被当纯音频处理（handleIncoming 不解析视频段，
//     cs.offer 保持 nil），answer 只回 m=audio、完全不含 video 段。
func TestVideoCallSwitchOff(t *testing.T) {
	if !media.VideoSupported() {
		t.Skip("未启用视频的构建")
	}
	p := &Phone{}
	p.acc = config.Account{Server: "127.0.0.1:15060", Account: "2999"}
	p.SetVideoCallEnabled(false)
	if p.VideoCallEnabled() || p.videoUsable() {
		t.Fatal("总开关关闭后 VideoCallEnabled / videoUsable 应为 false")
	}
	if _, err := p.DialVideo(context.Background(), "1002"); !errors.Is(err, core.ErrVideoDisabled) {
		t.Fatalf("总开关关闭后 DialVideo 应返回 ErrVideoDisabled，实际: %v", err)
	}

	// 视频来电的处理结果：offer 里带 m=video，但视频不可用时我们"不认识"它
	body := []byte(strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 127.0.0.1",
		"s=-",
		"c=IN IP4 127.0.0.1",
		"t=0 0",
		"m=audio 50002 RTP/AVP 8",
		"a=rtpmap:8 PCMA/8000",
		"m=video 50000 RTP/AVP 96",
		"a=rtpmap:96 H264/90000",
		"a=fmtp:96 packetization-mode=1",
		"a=sendrecv",
	}, "\r\n") + "\r\n")

	e, err := media.NewEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建音频引擎失败: %v", err)
	}
	defer e.Close()
	e.SetSDPIP("127.0.0.1")
	if _, err := e.SDPAnswer(body); err != nil {
		t.Fatalf("音频协商失败: %v", err)
	}
	// cs.offer 为 nil = handleIncoming 跳过了视频段解析（开关关闭时的实际状态）
	answer := p.buildAnswer(&callSession{id: "c1", engine: e}, answerNoVideo)
	if strings.Contains(answer, "m=video") {
		t.Fatalf("总开关关闭后 answer 不应含任何视频段：\n%s", answer)
	}
	if !strings.Contains(answer, "m=audio") {
		t.Fatalf("answer 应保留音频段：\n%s", answer)
	}

	// 重新打开：视频呼叫不再被开关拦下（本测试不真发 INVITE，只看到达
	// "创建视频引擎"这一步就够——引擎创建失败会返回别的错误）
	if err := p.SetVideoCallEnabled(true); err != nil {
		t.Fatalf("打开总开关失败: %v", err)
	}
	if !p.videoUsable() {
		t.Fatal("重新打开后 videoUsable 应为 true")
	}
}
