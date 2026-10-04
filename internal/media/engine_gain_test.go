package media

import (
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// 音量/增益（REQ-MEDIA-006）的媒体层测试。

// TestApplyGain 缩放、削波与单位增益直通。
func TestApplyGain(t *testing.T) {
	buf := []int16{1000, -1000, 0, 30000, -30000}
	applyGain(buf, 1) // 单位增益：原样
	want := []int16{1000, -1000, 0, 30000, -30000}
	for i := range want {
		if buf[i] != want[i] {
			t.Fatalf("增益 1 不应改动样本，[%d] = %d", i, buf[i])
		}
	}

	applyGain(buf, 0.5)
	want = []int16{500, -500, 0, 15000, -15000}
	for i := range want {
		if buf[i] != want[i] {
			t.Fatalf("增益 0.5 后 [%d] 应为 %d，实际 %d", i, want[i], buf[i])
		}
	}

	// 200% 放大必须削波（int16 回绕会把响音变成爆音）
	applyGain(buf, 4)
	want = []int16{2000, -2000, 0, 32767, -32768}
	for i := range want {
		if buf[i] != want[i] {
			t.Fatalf("削波后 [%d] 应为 %d，实际 %d", i, want[i], buf[i])
		}
	}
}

// TestGainVolumeDefaults 引擎零值必须是 100% 单位增益（内部存的是
// 「百分比 − 100」的偏移量，不是 float 位模式），且入参越界会被夹取。
func TestGainVolumeDefaults(t *testing.T) {
	var zero Engine // 未初始化的引擎：零值必须等于单位增益
	if zero.MicGain() != 1 || zero.SpeakerVolume() != 1 {
		t.Fatalf("零值引擎应为单位增益，实际 gain=%v vol=%v", zero.MicGain(), zero.SpeakerVolume())
	}

	e := newTestEngine(t)
	e.SetMicGain(0.8)
	if e.MicGain() != 0.8 {
		t.Fatalf("麦克风增益应为 0.8，实际 %v", e.MicGain())
	}
	e.SetMicGain(5) // 上限 200%
	if e.MicGain() != 2 {
		t.Fatalf("麦克风增益应夹取到 2，实际 %v", e.MicGain())
	}
	e.SetMicGain(-1)
	if e.MicGain() != 0 {
		t.Fatalf("麦克风增益应夹取到 0，实际 %v", e.MicGain())
	}
	e.SetSpeakerVolume(0.35)
	if e.SpeakerVolume() != 0.35 {
		t.Fatalf("扬声器音量应为 0.35，实际 %v", e.SpeakerVolume())
	}
	e.SetSpeakerVolume(3) // 扬声器上限 100%
	if e.SpeakerVolume() != 1 {
		t.Fatalf("扬声器音量应夹取到 1，实际 %v", e.SpeakerVolume())
	}
}

// TestReadPlaybackVolume 扬声器音量作用在取出的样本上。
func TestReadPlaybackVolume(t *testing.T) {
	e := newTestEngine(t)
	e.playPush(100, 10, frame(1000, 160))

	e.SetSpeakerVolume(0.5)
	buf := make([]int16, 160)
	e.ReadPlayback(buf)
	for i, v := range buf {
		if v != 500 {
			t.Fatalf("音量 50%% 时样本应为 500，[%d] = %d", i, v)
		}
	}

	// 静音优先于音量：输出全零（音量不改变静音语义）
	e.playPush(100, 11, frame(1000, 160))
	e.SetPlayMuted(true)
	e.ReadPlayback(buf)
	for i, v := range buf {
		if v != 0 {
			t.Fatalf("静音时应全零，[%d] = %d", i, v)
		}
	}
}

// TestMicGainReachesWire **端到端**验证麦克风增益：在真实 UDP socket 上收下
// 引擎发出的 RTP 包，把 G.711 载荷解回 PCM 比对幅度——证明增益确实作用在
// "发到对端的声音"上（不只是内部缓冲）。
func TestMicGainReachesWire(t *testing.T) {
	e := newTestEngine(t)

	// 本机再开一个 UDP 口当对端
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer pc.Close()
	e.setRemote(pc.LocalAddr().(*net.UDPAddr), 0)

	decodePacket := func() []int16 {
		t.Helper()
		_ = pc.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 2048)
		n, _, err := pc.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("未收到 RTP 包: %v", err)
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			t.Fatalf("RTP 解析失败: %v", err)
		}
		codec := codecByPT(pkt.PayloadType)
		pcm, err := codec.Decode(pkt.Payload)
		if err != nil {
			t.Fatalf("解码失败: %v", err)
		}
		return pcm
	}

	// 单位增益：发一帧 1000（PCMA 有量化误差，容差放宽）
	e.FeedMic(frame(1000, 160))
	base := decodePacket()
	if len(base) == 0 {
		t.Fatal("解码结果为空")
	}
	if d := int(base[0]) - 1000; d > 80 || d < -80 {
		t.Fatalf("单位增益下解码样本应接近 1000，实际 %d", base[0])
	}

	// 200% 增益：同一输入应解出约两倍的幅度
	e.SetMicGain(2)
	e.FeedMic(frame(1000, 160))
	loud := decodePacket()
	if d := int(loud[0]) - 2000; d > 200 || d < -200 {
		t.Fatalf("增益 200%% 下解码样本应接近 2000（说明增益没进 RTP），实际 %d", loud[0])
	}
	if loud[0] <= base[0] {
		t.Fatalf("增益 200%% 应比单位增益响，实际 %d <= %d", loud[0], base[0])
	}
}

// TestFeedMicGain 采集增益作用在送编码器的样本上，且不修改调用方缓冲。
// 用**不足一帧**的输入观察累积缓冲（凑满 160 就编码发走了，无需真收包）。
func TestFeedMicGain(t *testing.T) {
	e := newTestEngine(t)
	e.setRemote(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4000}, 0)

	e.SetMicGain(2)
	in := frame(1000, 100)
	e.FeedMic(in)
	if len(e.micBuf) != 100 {
		t.Fatalf("不足一帧应留在累积缓冲，实际 %d", len(e.micBuf))
	}
	if e.micBuf[0] != 2000 {
		t.Fatalf("增益 200%% 后应为 2000，实际 %d", e.micBuf[0])
	}
	// 调用方缓冲不能被就地改写（音频回调的缓冲还要用于其它用途）
	if in[0] != 1000 {
		t.Fatalf("不应修改调用方缓冲，实际 %d", in[0])
	}

	// 再次不足一帧的输入 + 削波
	e.FeedMic(frame(30000, 50))
	if len(e.micBuf) != 150 {
		t.Fatalf("累积应为 150 样本，实际 %d", len(e.micBuf))
	}
	if e.micBuf[100] != 32767 {
		t.Fatalf("放大后应削波到 32767，实际 %d", e.micBuf[100])
	}

	// 补足一帧后发出，累积缓冲清空
	e.FeedMic(frame(0, 10))
	_, _, tx := e.Stats()
	if tx != 1 {
		t.Fatalf("凑满一帧应发出 1 个包，实际 %d", tx)
	}
	if len(e.micBuf) != 0 {
		t.Fatalf("发帧后累积缓冲应清空，实际 %d", len(e.micBuf))
	}
}
