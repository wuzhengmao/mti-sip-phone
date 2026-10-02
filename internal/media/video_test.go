package media

import (
	"strings"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// TestLocalFramesWanted 本地帧投递判定（需求 7 修订，REQ-VIDEO-009）：
// 回显开着要投；回显关着但网页订阅了本地画面**也要投**；禁用视频则都不投。
func TestLocalFramesWanted(t *testing.T) {
	v := &VideoEngine{}
	if v.localFramesWanted() {
		t.Fatal("视频未开启时不应投递本地帧")
	}

	v.enabled.Store(true)
	if v.localFramesWanted() {
		t.Fatal("回显关闭且无其他消费者时不应投递本地帧")
	}

	// 网页订阅了本地画面：回显关着也要投
	v.SetLocalWantedFunc(func() bool { return true })
	if !v.localFramesWanted() {
		t.Fatal("网页订阅本地画面时应投递本地帧（与回显开关无关）")
	}

	// 回显开着：投
	v.SetLocalWantedFunc(func() bool { return false })
	v.previewOn.Store(true)
	if !v.localFramesWanted() {
		t.Fatal("回显开启时应投递本地帧")
	}

	// 禁用视频：两边都不投（用户要求）
	v.enabled.Store(false)
	if v.localFramesWanted() {
		t.Fatal("禁用视频后不应投递本地帧")
	}
}

// --- SDP：音频行为回归 + 视频扩展 ---

// TestSDPOfferAudioGolden 纯音频 offer 的逐字节回归。
// 需求 1：音频编解码优先级必须是 PCMA(8) 在 PCMU(0) 之前。
func TestSDPOfferAudioGolden(t *testing.T) {
	e := newTestEngine(t)
	e.SetSDPIP("10.0.0.5")
	e.ssrc = 42
	e.localPort = 40000

	got := e.SDPOffer()
	want := "v=0\r\n" +
		"o=- 42 1 IN IP4 10.0.0.5\r\n" +
		"s=mti-sip-phone\r\n" +
		"c=IN IP4 10.0.0.5\r\n" +
		"t=0 0\r\n" +
		"m=audio 40000 RTP/AVP 8 0 101\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=rtpmap:101 telephone-event/8000\r\n" +
		"a=fmtp:101 0-16\r\n" +
		"a=rtcp:40001\r\n" +
		"a=sendrecv\r\n"
	if got != want {
		t.Fatalf("offer 与既有格式不一致：\n得到:\n%s\n期望:\n%s", got, want)
	}
}

// TestParseMediaByKind 解析按媒体类型定位，而不是永远取第一段。
func TestParseMediaByKind(t *testing.T) {
	// 对端把 video 排在 audio 前面（部分实现如此）
	body := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 192.168.1.9\r\n" +
		"s=-\r\n" +
		"c=IN IP4 192.168.1.9\r\n" +
		"t=0 0\r\n" +
		"m=video 50000 RTP/AVP 97\r\n" +
		"a=rtpmap:97 H264/90000\r\n" +
		"a=recvonly\r\n" +
		"m=audio 50002 RTP/AVP 8 0\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=sendrecv\r\n")

	vinfo, err := ParseMedia(body, "video")
	if err != nil {
		t.Fatalf("解析视频段失败: %v", err)
	}
	if vinfo.Port != 50000 || vinfo.Dir != "recvonly" {
		t.Fatalf("视频段解析错误: port=%d dir=%s", vinfo.Port, vinfo.Dir)
	}
	if !vinfo.EncodingMatches(97, "H264") {
		t.Fatalf("视频 rtpmap 匹配失败: %v", vinfo.Rtpmap)
	}

	ainfo, err := ParseMedia(body, "audio")
	if err != nil {
		t.Fatalf("解析音频段失败: %v", err)
	}
	if ainfo.Port != 50002 {
		t.Fatalf("音频端口应为 50002，实际 %d（说明取错了媒体段）", ainfo.Port)
	}
	if ainfo.Dir != "sendrecv" {
		t.Fatalf("音频方向应为 sendrecv，实际 %s", ainfo.Dir)
	}

	// 方向按类型取：视频段是 recvonly，不能被当成音频方向
	if got := SDPDirectionFor(body, "audio"); got != "sendrecv" {
		t.Fatalf("音频方向误判为 %s", got)
	}
	if got := SDPDirectionFor(body, "video"); got != "recvonly" {
		t.Fatalf("视频方向误判为 %s", got)
	}
}

// TestNegotiateVideoPriority 视频协商按本地优先级、按 rtpmap 名匹配。
func TestNegotiateVideoPriority(t *testing.T) {
	if !VideoSupported() {
		t.Skip("未启用视频的构建（需要 -tags video）")
	}
	body := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 192.168.1.9\r\n" +
		"s=-\r\n" +
		"c=IN IP4 192.168.1.9\r\n" +
		"t=0 0\r\n" +
		"m=video 50000 RTP/AVP 100 101\r\n" +
		"a=rtpmap:101 VP8/90000\r\n" +
		"a=rtpmap:100 H264/90000\r\n" +
		"a=fmtp:100 profile-level-id=42e01f;packetization-mode=1\r\n" +
		"a=sendrecv\r\n")
	info, err := ParseMedia(body, "video")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	rc, pt, _, ok := negotiateVideo(info)
	if !ok {
		t.Fatal("应当协商成功")
	}
	if rc.Name != videoCodecH264 {
		t.Fatalf("应按优先级选 H264，实际 %s", rc.Name)
	}
	// answer 必须复用**对端**的 PT 号（100），不是本端的 96
	if pt != 100 {
		t.Fatalf("应复用对端 PT 100，实际 %d", pt)
	}
}

// TestNegotiateVideoH264Mode0 H264 显式声明 packetization-mode=0 也要接受，
// 且 answer 的 fmtp 回**对端声明的模式**（answer 只能是 offer 的子集）。
//
// 回归：早期这里直接跳过 H264，实测 FreeSWITCH 呼入视频电话（offer 就是
// `a=fmtp:102 profile-level-id=42000a; packetization-mode=0`）因此在 200 OK
// 里回 `m=video 0`，两边都看不到画面。该参数说的是对端自己怎么发，不影响
// 本端接收。
func TestNegotiateVideoH264Mode0(t *testing.T) {
	if !VideoSupported() {
		t.Skip("未启用视频的构建（需要 -tags video）")
	}
	body := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 192.168.1.9\r\n" +
		"s=-\r\n" +
		"c=IN IP4 192.168.1.9\r\n" +
		"t=0 0\r\n" +
		"m=video 50000 RTP/AVP 100 101\r\n" +
		"a=rtpmap:100 H264/90000\r\n" +
		"a=fmtp:100 profile-level-id=42000a; packetization-mode=0\r\n" +
		"a=rtpmap:101 VP8/90000\r\n" +
		"a=sendrecv\r\n")
	info, err := ParseMedia(body, "video")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	rc, pt, fmtp, ok := negotiateVideo(info)
	if !ok {
		t.Fatal("packetization-mode=0 不应导致协商失败")
	}
	if rc.Name != videoCodecH264 || pt != 100 {
		t.Fatalf("应选 H264/100，实际 %s/%d", rc.Name, pt)
	}
	if !strings.Contains(fmtp, "packetization-mode=0") || strings.Contains(fmtp, "packetization-mode=1") {
		t.Fatalf("answer 的 fmtp 应回对端声明的 packetization-mode=0：%q", fmtp)
	}
	if !strings.Contains(fmtp, "profile-level-id=42e01f") {
		t.Fatalf("answer 的 fmtp 应保留本端 profile-level-id：%q", fmtp)
	}

	// 对端没声明 packetization-mode 时保持本端模板（行为与从前一致）
	noMode := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 192.168.1.9\r\n" +
		"s=-\r\n" +
		"c=IN IP4 192.168.1.9\r\n" +
		"t=0 0\r\n" +
		"m=video 50000 RTP/AVP 100\r\n" +
		"a=rtpmap:100 H264/90000\r\n" +
		"a=fmtp:100 profile-level-id=42e01f\r\n" +
		"a=sendrecv\r\n")
	info2, _ := ParseMedia(noMode, "video")
	c := supportedVideoCodecs[0]
	if got := answerVideoFMTP(c, info2, 100); got != c.FMTP {
		t.Fatalf("对端未声明时应保持本端 fmtp，实际 %q", got)
	}
}

// TestAnswerFollowsOfferOrder answer 的 m= 行顺序必须与 offer 一致（RFC 3264）。
func TestAnswerFollowsOfferOrder(t *testing.T) {
	e := newTestEngine(t)
	e.SetSDPIP("10.0.0.5")
	e.ssrc = 7
	e.localPort = 41000

	body := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 192.168.1.9\r\n" +
		"s=-\r\n" +
		"c=IN IP4 192.168.1.9\r\n" +
		"t=0 0\r\n" +
		"m=video 50000 RTP/AVP 97\r\n" +
		"a=rtpmap:97 VP8/90000\r\n" +
		"m=audio 50002 RTP/AVP 8\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n")
	offer, err := ParseSDP(body)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 不接受视频 → 必须回 m=video 0 拒绝段，且顺序与 offer 一致
	ans := SDPAnswerFor(e, nil, offer, 8, "")
	vi := strings.Index(ans, "m=video 0 RTP/AVP 97")
	ai := strings.Index(ans, "m=audio")
	if vi < 0 {
		t.Fatalf("缺少 m=video 拒绝段：\n%s", ans)
	}
	if ai < 0 || vi > ai {
		t.Fatalf("m= 行顺序未跟随 offer（video 应在 audio 之前）：\n%s", ans)
	}
}

// --- RTP 打包/解包 ---

// TestVideoRTPRoundTrip 四种编码的「打包 → 解包」往返。
func TestVideoRTPRoundTrip(t *testing.T) {
	// 造一个足够大的帧促使分片（> MTU）
	makeFrame := func(name string) []byte {
		switch name {
		case videoCodecH264:
			// Annex-B：SPS + PPS + IDR，每段都超过 MTU
			big := make([]byte, 3000)
			for i := range big {
				big[i] = 0x42
			}
			au := []byte{0, 0, 0, 1, 0x67, 0x42, 0x00, 0x1f}    // SPS
			au = append(au, 0, 0, 0, 1, 0x68, 0xce, 0x3c, 0x80) // PPS
			au = append(au, 0, 0, 0, 1, 0x65)                   // IDR 头
			return append(au, big...)
		case videoCodecVP8:
			f := make([]byte, 2000)
			f[0] = 0x10 // 关键帧标志位（bit0=0）
			return f
		case videoCodecVP9:
			f := make([]byte, 2000)
			f[0] = 0x82
			return f
		default: // H263-1998：起始码 00 00 8x + 数据
			f := []byte{0x00, 0x00, 0x80}
			big := make([]byte, 2000)
			for i := range big {
				big[i] = 0x55
			}
			return append(f, big...)
		}
	}

	// VP9 不在合成数据上测：pion 的 VP9Payloader 会用 VP9 帧头解析器
	// 判断是否关键帧（决定负载描述符长度），合成数据解析不过会直接返回 0 包。
	// 真实 VP9 码流的往返在 video_cgo_test.go（-tags video）里覆盖。
	for _, name := range []string{videoCodecH264, videoCodecVP8, videoCodecH263} {
		t.Run(name, func(t *testing.T) {
			pack := newVideoPacketizer(name)
			depack := newVideoDepacketizer(name)
			if pack == nil || depack == nil {
				t.Fatalf("%s 缺少打包/解包实现", name)
			}
			src := makeFrame(name)
			payloads := pack.Payloads(src, videoMTU)
			if len(payloads) < 2 {
				t.Fatalf("%s 应产生多个 RTP 包，实际 %d", name, len(payloads))
			}
			for i, p := range payloads {
				if len(p) > videoMTU {
					t.Fatalf("%s 第 %d 包超出 MTU：%d", name, i, len(p))
				}
			}

			var got []byte
			var key bool
			var ended bool
			for i, p := range payloads {
				data, complete := depack.Push(p, i == len(payloads)-1)
				got = append(got, data...)
				if complete {
					ended = true
					// 与引擎一致：关键帧在**完整访问单元**上判定
					key = depack.Keyframe(got)
					if i != len(payloads)-1 {
						t.Fatalf("%s 提前判定访问单元结束", name)
					}
				}
			}
			if !ended {
				t.Fatalf("%s 未判定访问单元结束", name)
			}
			if len(got) == 0 {
				t.Fatalf("%s 解包为空", name)
			}
			if name == videoCodecH263 {
				// H263 的 P=1 会把被剥掉的两个 0x00 补回来
				if len(got) < 3 || got[0] != 0 || got[1] != 0 || got[2] != 0x80 {
					t.Fatalf("H263 起始码未还原：% x", got[:min(4, len(got))])
				}
			}
			if !key {
				t.Fatalf("%s 应判定为关键帧", name)
			}
		})
	}
}

// TestH263PayloadHeader RFC 4629 负载头字段位置。
func TestH263PayloadHeader(t *testing.T) {
	frame := []byte{0x00, 0x00, 0x80, 0x11, 0x22, 0x33}
	payloads := h263Packetizer{}.Payloads(frame, videoMTU)
	if len(payloads) != 1 {
		t.Fatalf("小帧应只有 1 个包，实际 %d", len(payloads))
	}
	hdr := uint16(payloads[0][0])<<8 | uint16(payloads[0][1])
	if hdr&h263FlagP == 0 {
		t.Fatalf("帧首包必须置 P 位，头=%#04x", hdr)
	}
	if hdr&h263FlagV != 0 {
		t.Fatalf("未使用 VRC，V 位应为 0，头=%#04x", hdr)
	}
	// 帧首包应剥掉两个 0x00
	if payloads[0][2] != 0x80 {
		t.Fatalf("起始码前两个 0x00 应被剥离，实际首字节 %#02x", payloads[0][2])
	}

	// 非帧首包的 P 位必须为 0
	long := make([]byte, videoMTU*2)
	copy(long, frame)
	ps := h263Packetizer{}.Payloads(long, videoMTU)
	if len(ps) < 2 {
		t.Fatalf("长帧应分片，实际 %d 包", len(ps))
	}
	hdr2 := uint16(ps[1][0])<<8 | uint16(ps[1][1])
	if hdr2&h263FlagP != 0 {
		t.Fatalf("后续包 P 位应为 0，头=%#04x", hdr2)
	}
}

// TestVideoDepacketizerGapRecovery 丢包后丢弃当前访问单元，但**不冻结**——
// 后续完整帧仍应产出数据（视频靠解码器做错误隐藏，冻结到下一个 IDR 更糟）。
func TestVideoDepacketizerGapRecovery(t *testing.T) {
	depack := newVideoDepacketizer(videoCodecVP8)
	pack := newVideoPacketizer(videoCodecVP8)

	f := make([]byte, 3000)
	f[0] = 0x10
	payloads := pack.Payloads(f, videoMTU)
	if len(payloads) < 3 {
		t.Skip("分片数不足，跳过")
	}
	// 送第 1 包后跳过第 2 包，直接送第 3 包（模拟丢包）。
	// 引擎在检测到序号跳变时会重置解包器——这里显式模拟该动作。
	depack.Push(payloads[0], false)
	depack.Reset()
	data, complete := depack.Push(payloads[2], true)
	if len(data) == 0 {
		t.Fatal("丢包重置后，带 marker 的包仍应产出数据")
	}
	if !complete {
		t.Fatal("带 marker 的包应判定为访问单元结束")
	}
}

// TestSplitAnnexB Annex-B 切分（关键帧判定的基础）。
func TestSplitAnnexB(t *testing.T) {
	au := []byte{0, 0, 0, 1, 0x67, 0xAA, 0, 0, 1, 0x65, 0xBB, 0xCC}
	nalus := splitAnnexB(au)
	if len(nalus) != 2 {
		t.Fatalf("应切出 2 个 NALU，实际 %d", len(nalus))
	}
	if !h264IsKeyframe(au) {
		t.Fatal("含 IDR(0x65) 应判定为关键帧")
	}
	if h264IsKeyframe([]byte{0, 0, 1, 0x67, 0xAA}) {
		t.Fatal("只有 SPS 不应判定为关键帧")
	}
}

// TestVideoPacketizerInterfaces 打包器/解包器实现的是 pion 的接口。
func TestVideoPacketizerInterfaces(t *testing.T) {
	var _ rtp.Payloader = &codecs.H264Payloader{}
	var _ rtp.Depacketizer = &codecs.H264Packet{}
	if p := newVideoPacketizer(videoCodecH264); p == nil {
		t.Fatal("H264 打包器缺失")
	}
}
