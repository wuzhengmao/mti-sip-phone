//go:build audio && cgo

package media

import (
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
)

// 运行：go test -tags audio ./internal/media/
// 需要先跑 scripts/build-audio-libs.sh 生成 third_party/audio/<目标>/lib/libopus.a，
// 并把 include/lib 经 CGO_CFLAGS/CGO_LDFLAGS 传给 go（build.sh / build.bat 会自动做）。

// opusSine 生成 8kHz、freq Hz、幅度 amp 的正弦波。
func opusSine(n int, freq, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/8000))
	}
	return out
}

// TestOpusCodecMetadata 元数据：PT 固定 96、RTP 时钟 48000、PCM 帧长 160、
// 时间戳步长 960（时钟率与 PCM 帧长不是一回事）、rtpmap 固定 opus/48000/2。
func TestOpusCodecMetadata(t *testing.T) {
	if !OpusSupported() {
		t.Fatal("带 opus 标签的构建必须支持 Opus")
	}
	c := codecByName(t, "opus")
	if c == nil {
		t.Fatal("supportedCodecs 里找不到 opus")
	}
	if c.PayloadType() != 96 {
		t.Fatalf("Opus 负载类型应固定为 96，实际 %d", c.PayloadType())
	}
	if c.ClockRate() != 48000 {
		t.Fatalf("Opus RTP 时钟率应为 48000，实际 %d", c.ClockRate())
	}
	if c.FrameSize() != 160 {
		t.Fatalf("Opus PCM 帧长应为 160（20ms @ 8kHz 管线），实际 %d", c.FrameSize())
	}
	if c.TimestampStep() != 960 {
		t.Fatalf("Opus 时间戳步长应为 960（20ms @ 48kHz），实际 %d", c.TimestampStep())
	}
	if c.Channels() != 2 {
		t.Fatalf("Opus rtpmap 声道数应为 2（RFC 7587），实际 %d", c.Channels())
	}
	if c.RTPMap() != "opus/48000/2" {
		t.Fatalf("rtpmap 应为 opus/48000/2，实际 %s", c.RTPMap())
	}
}

// TestOpusRoundTrip 编码→解码往返：8kHz 单声道正弦波编解码后能量相当
//（不逐样本比对：Opus 有算法时延与有损压缩，逐样本比对必然失败）。
func TestOpusRoundTrip(t *testing.T) {
	c := codecByName(t, "opus")
	const frames = 10
	var in, out []int16
	for i := 0; i < frames; i++ {
		pcm := opusSine(160, 440, 8000)
		payload, err := c.Encode(pcm)
		if err != nil {
			t.Fatalf("第 %d 帧编码失败: %v", i, err)
		}
		if len(payload) == 0 || len(payload) > 1275 {
			t.Fatalf("第 %d 帧负载长度异常: %d", i, len(payload))
		}
		dec, err := c.Decode(payload)
		if err != nil {
			t.Fatalf("第 %d 帧解码失败: %v", i, err)
		}
		if len(dec) != 160 {
			t.Fatalf("第 %d 帧解码样本数应为 160，实际 %d", i, len(dec))
		}
		in = append(in, pcm...)
		out = append(out, dec...)
	}
	if r := rms(out); r < 500 {
		t.Fatalf("解码输出能量过低（rms=%.0f），编码/解码链路可能没工作", r)
	}
	ratio := rms(out) / rms(in)
	if ratio < 0.3 || ratio > 1.5 {
		t.Fatalf("解码输出能量与输入相差过大：输入 rms=%.0f 输出 rms=%.0f（比值 %.2f）",
			rms(in), rms(out), ratio)
	}
}

// TestOpusFrameSizeGuard 每帧必须是 20ms（160 样本），否则报错而不是越界读。
func TestOpusFrameSizeGuard(t *testing.T) {
	c := codecByName(t, "opus")
	if _, err := c.Encode(make([]int16, 100)); err == nil {
		t.Fatal("非 160 样本的帧应报错")
	}
	if _, err := c.Decode(nil); err == nil {
		t.Fatal("空负载应报错")
	}
}

// TestOpusPLC 丢包隐藏：解过一包之后能外推同样长度的替代样本。
func TestOpusPLC(t *testing.T) {
	c := codecByName(t, "opus")
	plc, ok := c.(plcCodec)
	if !ok {
		t.Fatal("Opus 应实现丢包隐藏接口")
	}
	if _, err := c.Decode(mustEncode(t, c, opusSine(160, 440, 8000))); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	got := plc.DecodePLC(160)
	if len(got) != 160 {
		t.Fatalf("PLC 应输出 160 个样本，实际 %d", len(got))
	}
	// 非法长度必须安全返回 nil（不能越界）
	if got := plc.DecodePLC(0); got != nil {
		t.Fatal("PLC 样本数为 0 时应返回 nil")
	}
	if got := plc.DecodePLC(99999); got != nil {
		t.Fatal("PLC 样本数超上限时应返回 nil")
	}
}

// TestOpusResetReset 复位后仍能正常编解码（复位用于新通话/换流）。
func TestOpusReset(t *testing.T) {
	c := codecByName(t, "opus")
	payload := mustEncode(t, c, opusSine(160, 440, 8000))
	if _, err := c.Decode(payload); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	resetCodec(c)
	if _, err := c.Decode(payload); err != nil {
		t.Fatalf("复位后解码失败: %v", err)
	}
}

func mustEncode(t *testing.T, c Codec, pcm []int16) []byte {
	t.Helper()
	b, err := c.Encode(pcm)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	return b
}

// --- SDP 协商 ---

// opusOnlyOffer 对端只支持 Opus（动态 PT 111）。
const opusOnlyOffer = "v=0\r\n" +
	"o=- 1 1 IN IP4 192.168.1.9\r\n" +
	"s=-\r\n" +
	"c=IN IP4 192.168.1.9\r\n" +
	"t=0 0\r\n" +
	"m=audio 50000 RTP/AVP 111\r\n" +
	"a=rtpmap:111 opus/48000/2\r\n" +
	"a=sendrecv\r\n"

// TestOpusNegotiateAsAnswerer 对端只给 Opus 时协商到 Opus，
// 且 answer 必须**复用对端的 PT 号**（RFC 3264）并写 opus/48000/2。
func TestOpusNegotiateAsAnswerer(t *testing.T) {
	e := newTestEngine(t)
	answer, err := e.SDPAnswer([]byte(opusOnlyOffer))
	if err != nil {
		t.Fatalf("协商失败: %v", err)
	}
	if e.Codec().Name() != "opus" {
		t.Fatalf("应协商到 opus，实际 %s", e.Codec().Name())
	}
	if e.PayloadType() != 111 {
		t.Fatalf("线上应使用对端 PT 111，实际 %d", e.PayloadType())
	}
	if !strings.Contains(answer, "m=audio "+strconv.Itoa(e.LocalPort())+" RTP/AVP 111") {
		t.Fatalf("answer 的 m= 行应复用对端 PT 111：\n%s", answer)
	}
	if !strings.Contains(answer, "a=rtpmap:111 opus/48000/2") {
		t.Fatalf("answer 应写 opus/48000/2（不是本端的 96）：\n%s", answer)
	}
	if strings.Contains(answer, "PCMA") || strings.Contains(answer, "PCMU") {
		t.Fatalf("answer 只应列协商到的那一个编码：\n%s", answer)
	}
}

// TestOpusLowerPriorityThanG711 双方都支持时仍优先 PCMA（Opus 是最后手段）。
func TestOpusLowerPriorityThanG711(t *testing.T) {
	offer := "v=0\r\n" +
		"o=- 1 1 IN IP4 192.168.1.9\r\n" +
		"s=-\r\n" +
		"c=IN IP4 192.168.1.9\r\n" +
		"t=0 0\r\n" +
		"m=audio 50000 RTP/AVP 111 8 0\r\n" +
		"a=rtpmap:111 opus/48000/2\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=sendrecv\r\n"
	e := newTestEngine(t)
	if _, err := e.SDPAnswer([]byte(offer)); err != nil {
		t.Fatalf("协商失败: %v", err)
	}
	if e.Codec().Name() != "PCMA" {
		t.Fatalf("PCMA 可用时不应选 Opus，实际 %s", e.Codec().Name())
	}
}

// TestOpusAnswerToOurOffer 本端 offer（含 Opus）被对端选中时：
// answer 解析按本端号 96，时间戳步长按 48kHz 时钟走 960。
func TestOpusAnswerToOurOffer(t *testing.T) {
	e := newTestEngine(t)
	answer := "v=0\r\n" +
		"o=- 1 1 IN IP4 192.168.1.9\r\n" +
		"s=-\r\n" +
		"c=IN IP4 192.168.1.9\r\n" +
		"t=0 0\r\n" +
		"m=audio 50000 RTP/AVP 96\r\n" +
		"a=rtpmap:96 opus/48000/2\r\n" +
		"a=sendrecv\r\n"
	if err := e.SDPApplyAnswer([]byte(answer)); err != nil {
		t.Fatalf("应用 answer 失败: %v", err)
	}
	if e.Codec().Name() != "opus" || e.PayloadType() != 96 {
		t.Fatalf("应协商到 opus/96，实际 %s/%d", e.Codec().Name(), e.PayloadType())
	}
	// 发送一帧：时间戳必须按 48000 时钟前进 960（不是 PCM 帧长 160）
	e.setRemote(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 50000}, 0)
	before := e.rtpTimestamp()
	e.FeedMic(make([]int16, 160))
	if got := e.rtpTimestamp() - before; got != 960 {
		t.Fatalf("Opus 时间戳步长应为 960，实际 %d", got)
	}
}
