package media

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// TestG722CodecMetadata G.722 的元数据（REQ-MEDIA-011）：
// PT 固定 9、RTP 时钟 8000（**不是** 16 kHz 的 PCM 采样率）、PCM 帧长 320
// （20ms @16kHz）、时间戳步长 160（20ms @8kHz 时钟）——两个"率"不是一回事。
func TestG722CodecMetadata(t *testing.T) {
	c := codecByName(t, "G722")
	if c.PayloadType() != 9 {
		t.Fatalf("G.722 负载类型应固定为 9，实际 %d", c.PayloadType())
	}
	if c.ClockRate() != 8000 {
		t.Fatalf("G.722 的 RTP 时钟率应为 8000（RFC 3551），实际 %d", c.ClockRate())
	}
	if c.SampleRate() != 16000 {
		t.Fatalf("G.722 的 PCM 采样率应为 16000，实际 %d", c.SampleRate())
	}
	if c.FrameSize() != 320 {
		t.Fatalf("G.722 每帧应为 320 样本（20ms @16kHz），实际 %d", c.FrameSize())
	}
	if c.TimestampStep() != 160 {
		t.Fatalf("G.722 时间戳步长应为 160（20ms @8kHz），实际 %d", c.TimestampStep())
	}
	if c.Channels() != 1 {
		t.Fatalf("G.722 是单声道（rtpmap 省略 /1），实际 %d", c.Channels())
	}
	if c.RTPMap() != "G722/8000" {
		t.Fatalf("rtpmap 应为 G722/8000，实际 %s", c.RTPMap())
	}
}

// TestCodecRateInvariants 所有编解码器都满足两条不变量：
// FrameSize == SampleRate/50、TimestampStep == ClockRate/50（一帧恒为 20ms）。
// 它同时是"新增编解码器忘了声明采样率"的守门测试——Opus 若把 SampleRate
// 误接成 ClockRate(48000)，这里会直接失败。
func TestCodecRateInvariants(t *testing.T) {
	for _, c := range supportedCodecs {
		if got := c.SampleRate() / 50; c.FrameSize() != got {
			t.Errorf("%s：FrameSize=%d，期望 SampleRate/50=%d", c.Name(), c.FrameSize(), got)
		}
		if got := c.ClockRate() / 50; c.TimestampStep() != got {
			t.Errorf("%s：TimestampStep=%d，期望 ClockRate/50=%d", c.Name(), c.TimestampStep(), got)
		}
	}
}

// TestG722FrameSizeGuard 每帧必须恰好 320 样本，负载长度也要设上限。
func TestG722FrameSizeGuard(t *testing.T) {
	c := codecByName(t, "G722")
	for _, n := range []int{0, 319, 321, 640} {
		if _, err := c.Encode(frame(0, n)); err == nil {
			t.Fatalf("Encode(%d 样本) 应报错", n)
		}
	}
	enc, err := c.Encode(frame(1000, 320))
	if err != nil {
		t.Fatalf("Encode(320 样本) 应成功: %v", err)
	}
	if len(enc) != 160 {
		t.Fatalf("320 样本应编成 160 字节（64kbps），实际 %d", len(enc))
	}
	if _, err := c.Decode(nil); err == nil {
		t.Fatal("空负载应报错")
	}
	if _, err := c.Decode(make([]byte, g722MaxPayload+1)); err == nil {
		t.Fatal("超长负载应报错")
	}
	pcm, err := c.Decode(make([]byte, 160))
	if err != nil {
		t.Fatalf("Decode(160 字节) 应成功: %v", err)
	}
	if len(pcm) != 320 {
		t.Fatalf("160 字节应解出 320 样本，实际 %d", len(pcm))
	}
}

// TestG722RoundTrip 编码→解码往返：16 kHz 正弦编解码后能量相当（G.722 有损
// 且有 22 样本时延，不逐样本比对）。
func TestG722RoundTrip(t *testing.T) {
	c := codecByName(t, "G722")
	const frames = 20
	var src, out []int16
	for f := 0; f < frames; f++ {
		pcm := make([]int16, 320)
		for i := range pcm {
			// 440Hz 正弦 @16kHz，取有符号三角波的整数近似
			n := f*320 + i
			ph := (n * 440 * 2 / 16) % 3200 // 一个周期 3200 个"半单位"
			v := 8000
			if ph >= 1600 {
				v = 8000 - (ph-1600)*10
			} else {
				v = -8000 + ph*10
			}
			pcm[i] = int16(v)
		}
		enc, err := c.Encode(pcm)
		if err != nil {
			t.Fatalf("第 %d 帧编码失败: %v", f, err)
		}
		dec, err := c.Decode(enc)
		if err != nil {
			t.Fatalf("第 %d 帧解码失败: %v", f, err)
		}
		if f == 0 {
			continue // 第一帧还在填充预测器/时延线，跳过
		}
		src = append(src, pcm...)
		out = append(out, dec...)
	}
	if len(src) == 0 {
		t.Fatal("没有可比对的样本")
	}
	ratio := rms(out) / rms(src)
	if ratio < 0.3 || ratio > 1.5 {
		t.Fatalf("往返能量比异常：%.3f", ratio)
	}
}

// TestG722Reset 复位后仍能正常编解码，且输出与"全新实例"一致
//（编解码器实例是包级共享的，复位必须彻底）。
func TestG722Reset(t *testing.T) {
	c := codecByName(t, "G722")
	load := make([]int16, 320)
	for i := range load {
		load[i] = int16((i * 97) % 20000)
	}
	// 编解码器实例是包级共享的（别的测试也会用它），先复位再取基准
	resetCodec(c)
	clean, err := c.Encode(load) // 干净状态下的首帧
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	// 弄脏状态（多编几帧），复位后同一输入必须得到与干净状态一致的结果
	for i := 0; i < 3; i++ {
		if _, err := c.Encode(load); err != nil {
			t.Fatalf("编码失败: %v", err)
		}
	}
	resetCodec(c)
	again, err := c.Encode(load)
	if err != nil {
		t.Fatalf("复位后编码失败: %v", err)
	}
	if string(clean) != string(again) {
		t.Fatal("复位后同一输入的编码结果应与干净状态一致（状态没清干净）")
	}
}

// TestG722Negotiation 协商：对端只提供 G.722（带/不带 rtpmap）都能协商到它，
// 且 answer 用对端 PT、rtpmap 为 G722/8000；对端同时提供 G.711 时**选 G.711**
//（G.722 排最后，是用户明确要求的优先级）。
func TestG722Negotiation(t *testing.T) {
	body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 5000 RTP/AVP 9\r\na=rtpmap:9 G722/8000\r\na=sendrecv\r\n"
	e := newTestEngine(t)
	answer, err := e.SDPAnswer([]byte(body))
	if err != nil {
		t.Fatalf("协商失败: %v", err)
	}
	if e.Codec().Name() != "G722" {
		t.Fatalf("应协商到 G722，实际 %s", e.Codec().Name())
	}
	if e.SampleRate() != 16000 {
		t.Fatalf("协商到 G.722 后管线采样率应为 16000，实际 %d", e.SampleRate())
	}
	if !strings.Contains(answer, "a=rtpmap:9 G722/8000") {
		t.Fatalf("answer 应宣告 G722/8000：\n%s", answer)
	}

	// 没有 rtpmap 时走静态号回退（PT 9 ≤ 95）
	bodyNoRtpmap := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 5000 RTP/AVP 9\r\na=sendrecv\r\n"
	e2 := newTestEngine(t)
	if _, err := e2.SDPAnswer([]byte(bodyNoRtpmap)); err != nil {
		t.Fatalf("协商失败: %v", err)
	}
	if e2.Codec().Name() != "G722" {
		t.Fatalf("无 rtpmap 的 PT 9 应按静态号回退到 G722，实际 %s", e2.Codec().Name())
	}

	// 对端同时给 PCMA 与 G.722：选 PCMA（优先级更高），管线回到 8 kHz
	bodyBoth := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 5000 RTP/AVP 9 8\r\na=rtpmap:9 G722/8000\r\na=rtpmap:8 PCMA/8000\r\na=sendrecv\r\n"
	e3 := newTestEngine(t)
	if _, err := e3.SDPAnswer([]byte(bodyBoth)); err != nil {
		t.Fatalf("协商失败: %v", err)
	}
	if e3.Codec().Name() != "PCMA" {
		t.Fatalf("对端同时提供 PCMA 与 G.722 时应选 PCMA，实际 %s", e3.Codec().Name())
	}
	if e3.SampleRate() != 8000 {
		t.Fatalf("PCMA 通话的采样率应为 8000，实际 %d", e3.SampleRate())
	}

	// 对端同时给 G.722 与 Opus（都是动态/非 G.711 场景）：选 G.722
	//（宽频优先，排在 Opus 之前）；没有 Opus 的构建跳过这段
	if OpusSupported() {
		bodyWide := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
			"m=audio 5000 RTP/AVP 111 9\r\na=rtpmap:111 opus/48000/2\r\na=rtpmap:9 G722/8000\r\na=sendrecv\r\n"
		e4 := newTestEngine(t)
		if _, err := e4.SDPAnswer([]byte(bodyWide)); err != nil {
			t.Fatalf("协商失败: %v", err)
		}
		if e4.Codec().Name() != "G722" {
			t.Fatalf("对端同时提供 Opus 与 G.722 时应选 G.722，实际 %s", e4.Codec().Name())
		}
		if e4.SampleRate() != 16000 {
			t.Fatalf("G.722 通话的采样率应为 16000，实际 %d", e4.SampleRate())
		}
	}
}

// TestG722SampleRateSwitch 采样率切换（183 → 200 换编码这类场景）：
// 回调被通知、播放缓冲按新速率换算且被清空、采集侧残留半帧被丢弃。
func TestG722SampleRateSwitch(t *testing.T) {
	e := newTestEngine(t)
	if e.SampleRate() != 8000 {
		t.Fatalf("初始应为 8000，实际 %d", e.SampleRate())
	}
	var rates []int
	e.SetSampleRateListener(func(rate int) { rates = append(rates, rate) })

	// 先按 8 kHz 灌入一段播放缓冲与半帧采集残留
	e.playMu.Lock()
	e.ring.Write(frame(1000, 1600))
	e.playMu.Unlock()
	e.micMu.Lock()
	e.micBuf = append(e.micBuf, frame(1, 80)...)
	e.micMu.Unlock()

	body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 5000 RTP/AVP 9\r\na=rtpmap:9 G722/8000\r\na=sendrecv\r\n"
	if _, err := e.SDPAnswer([]byte(body)); err != nil {
		t.Fatalf("协商失败: %v", err)
	}
	if len(rates) != 1 || rates[0] != 16000 {
		t.Fatalf("采样率回调应收到一次 16000，实际 %v", rates)
	}
	if e.SampleRate() != 16000 || e.ring.Rate() != 16000 {
		t.Fatalf("引擎与播放缓冲都应切到 16000：engine=%d ring=%d", e.SampleRate(), e.ring.Rate())
	}
	if d := e.ring.Depth(); d != 0 {
		t.Fatalf("切换速率应清空播放缓冲（旧速率样本），实际深度 %d", d)
	}
	e.micMu.Lock()
	n := len(e.micBuf)
	e.micMu.Unlock()
	if n != 0 {
		t.Fatalf("切换速率应丢掉采集侧半帧，实际残留 %d 样本", n)
	}
	// 16 kHz 下的深度阈值应是 8 kHz 的两倍（时间语义不变）
	if got := e.ring.targetDepth(); got != 3200 {
		t.Fatalf("16kHz 下目标深度应为 3200，实际 %d", got)
	}
	if got := e.ring.maxDepth(); got != 8000 {
		t.Fatalf("16kHz 下硬上限应为 8000，实际 %d", got)
	}

	// 再协商回 G.711：回到 8 kHz
	body711 := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 5000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\na=sendrecv\r\n"
	if _, err := e.SDPAnswer([]byte(body711)); err != nil {
		t.Fatalf("协商失败: %v", err)
	}
	if len(rates) != 2 || rates[1] != 8000 {
		t.Fatalf("第二次回调应为 8000，实际 %v", rates)
	}
}

// TestG722Timestamp G.722 的时间戳步长恒为 160（8 kHz 时钟），
// 与 PCM 帧长 320 无关。
func TestG722Timestamp(t *testing.T) {
	c := codecByName(t, "G722")
	enc, err := c.Encode(frame(3000, 320))
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	_ = enc
	if c.TimestampStep() != 160 {
		t.Fatalf("时间戳步长应为 160，实际 %d", c.TimestampStep())
	}
	if c.FrameSize() == c.TimestampStep() {
		t.Fatal("G.722 的 PCM 帧长与时间戳步长必须不同（320 vs 160）")
	}
}

// TestG722RTPRoundTrip G.722 走**真实 RTP 收发路径**（不是绕过 socket 直接调
// playPush）：320 样本 → 一包 PT 9 / 160 字节 / 时间戳 +160；把该包送回去，
// 播放侧能取回 320 个样本。这是"帧长、时钟、负载三者对齐"的端到端证据。
func TestG722RTPRoundTrip(t *testing.T) {
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("建对端 socket 失败: %v", err)
	}
	defer peer.Close()
	peerPort := peer.LocalAddr().(*net.UDPAddr).Port

	e := newTestEngine(t)
	offer := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio " + strconv.Itoa(peerPort) + " RTP/AVP 9\r\na=rtpmap:9 G722/8000\r\na=sendrecv\r\n"
	if _, err := e.SDPAnswer([]byte(offer)); err != nil {
		t.Fatalf("协商失败: %v", err)
	}
	e.Start()
	defer e.Close()

	// ---- 发送方向：一帧 320 样本 = 一包 ----
	e.FeedMic(frame(1200, 320))
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := peer.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("没收到 RTP 包: %v", err)
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(buf[:n]); err != nil {
		t.Fatalf("解析 RTP 失败: %v", err)
	}
	if pkt.PayloadType != 9 {
		t.Fatalf("负载类型应为 9，实际 %d", pkt.PayloadType)
	}
	if len(pkt.Payload) != 160 {
		t.Fatalf("20ms 的 G.722 负载应为 160 字节，实际 %d", len(pkt.Payload))
	}
	firstTS := pkt.Timestamp

	// 再一帧：时间戳应 +160（8 kHz 时钟 × 20ms）
	e.FeedMic(frame(1200, 320))
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = peer.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("没收到第二个包: %v", err)
	}
	if err := pkt.Unmarshal(buf[:n]); err != nil {
		t.Fatalf("解析 RTP 失败: %v", err)
	}
	if got := pkt.Timestamp - firstTS; got != 160 {
		t.Fatalf("时间戳步长应为 160，实际 %d", got)
	}

	// ---- 接收方向：把包送回引擎，播放侧应取回 320 个样本 ----
	if _, err := e.conn.WriteToUDP(buf[:n], &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: e.localPort}); err != nil {
		t.Fatalf("发送 RTP 失败: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	out := make([]int16, 320)
	for time.Now().Before(deadline) {
		if e.ReadPlayback(out) == 320 {
			return // 成功：解码并播放了整帧
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("播放侧没有取到 320 个样本（接收链路没走通）")
}
