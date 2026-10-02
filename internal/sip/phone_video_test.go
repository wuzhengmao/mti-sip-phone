package sip

import (
	"strings"
	"testing"

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

	// withVideo=false 即普通接听；此时 cs.video 也已被回收（answerIncoming 里做的）
	cs := &callSession{id: "c1", engine: e, offer: offer}
	p := &Phone{}
	ans := p.buildAnswer(cs, false)

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
