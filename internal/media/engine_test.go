package media

import (
	"net"
	"testing"

	"github.com/pion/rtp"
)

// newTestEngine 创建测试用引擎（不启动收发循环）。
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建引擎失败: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

// frame 构造含 n 个相同值的测试帧。
func frame(v int16, n int) []int16 {
	f := make([]int16, n)
	for i := range f {
		f[i] = v
	}
	return f
}

// TestPlayPushSequential 连续序号包按序进入播放缓冲。
func TestPlayPushSequential(t *testing.T) {
	e := newTestEngine(t)
	e.playPush(100, 10, frame(1, 160))
	e.playPush(100, 11, frame(2, 160))
	e.playPush(100, 12, frame(3, 160))

	buf := make([]int16, 480)
	e.ReadPlayback(buf)
	for i, v := range buf {
		want := int16(i/160 + 1)
		if v != want {
			t.Fatalf("样本 %d 应为 %d，实际 %d", i, want, v)
		}
	}
}

// TestPlayPushGapFillsSilence 丢包时填充静音（而非循环上一帧——噪音回归）。
func TestPlayPushGapFillsSilence(t *testing.T) {
	e := newTestEngine(t)
	e.playPush(100, 10, frame(1000, 160))
	// 丢包：seq 11 缺失，直接来 12
	e.playPush(100, 12, frame(2000, 160))

	buf := make([]int16, 480)
	e.ReadPlayback(buf)
	if buf[0] != 1000 || buf[159] != 1000 {
		t.Fatal("第一帧应正常播放")
	}
	for i, v := range buf[160:320] {
		if v != 0 {
			t.Fatalf("丢包位置应补静音，样本 %d 实际 %d", i, v)
		}
	}
	if buf[320] != 2000 {
		t.Fatal("第二帧应正常播放")
	}
}

// TestPlayPushSSRCSwitchResets 媒体流 SSRC 切换（183 早期媒体 → 200 新媒体流）：
// 播放缓冲必须重置，旧序列不得阻塞新流（"接通后无声"回归）。
func TestPlayPushSSRCSwitchResets(t *testing.T) {
	e := newTestEngine(t)
	e.playPush(100, 10, frame(1, 160))
	e.playPush(100, 11, frame(2, 160))
	// SSRC 切换且序号重新编号
	e.playPush(200, 5000, frame(3, 160))
	e.playPush(200, 5001, frame(4, 160))

	buf := make([]int16, 320)
	e.ReadPlayback(buf)
	for i, v := range buf {
		want := int16(3)
		if i >= 160 {
			want = 4
		}
		if v != want {
			t.Fatalf("切换后应直接播放新流，样本 %d 应为 %d，实际 %d", i, want, v)
		}
	}
}

// TestPlayPushSeqJumpResets 序号大跳变（> 100 包 = 2 秒）视为新流重置。
func TestPlayPushSeqJumpResets(t *testing.T) {
	e := newTestEngine(t)
	e.playPush(100, 10, frame(1, 160))
	e.playPush(100, 200, frame(2, 160))

	buf := make([]int16, 160)
	e.ReadPlayback(buf)
	if buf[0] != 2 {
		t.Fatalf("大跳变后应直接播放新包，实际 %d", buf[0])
	}
}

// TestPlayPushOutOfOrderDropped 乱序（过期）包丢弃。
func TestPlayPushOutOfOrderDropped(t *testing.T) {
	e := newTestEngine(t)
	e.playPush(100, 10, frame(1, 160))
	e.playPush(100, 11, frame(2, 160))
	e.playPush(100, 10, frame(9, 160)) // 重复/过期：丢弃

	buf := make([]int16, 320)
	e.ReadPlayback(buf)
	if buf[0] != 1 || buf[160] != 2 {
		t.Fatalf("过期包不应影响播放序列: %d %d", buf[0], buf[160])
	}
}

// TestFeedMicAccumulates 采集任意帧长输入：内部按 20ms 帧累积发送。
func TestFeedMicAccumulates(t *testing.T) {
	e := newTestEngine(t)
	// 无对端地址时不发送
	e.FeedMic(make([]int16, 160))
	if _, _, tx := e.Stats(); tx != 0 {
		t.Fatalf("未设置对端地址不应发送，tx=%d", tx)
	}
	e.setRemote(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4000}, 0)

	// 三次小帧（100 样本）应凑成一帧半：1 帧发出 + 60 样本留存
	e.FeedMic(make([]int16, 100))
	e.FeedMic(make([]int16, 100))
	e.FeedMic(make([]int16, 100))
	_, _, tx := e.Stats()
	if tx != 1 {
		t.Fatalf("300 样本应发 1 帧，实际 %d", tx)
	}
	// 补足剩余 60 样本后再发一帧
	e.FeedMic(make([]int16, 60))
	_, _, tx = e.Stats()
	if tx != 2 {
		t.Fatalf("补足后应共发 2 帧，实际 %d", tx)
	}
}

// TestDTMFDigitMapping RFC 4733 事件码 ↔ 拨号字符（0-9=0-9，10=*，11=#，12-15=A-D）。
func TestDTMFDigitMapping(t *testing.T) {
	want := map[byte]string{0: "0", 5: "5", 9: "9", 10: "*", 11: "#", 12: "A", 15: "D"}
	for code, digit := range want {
		got, ok := dtmfDigit(code)
		if !ok || got != digit {
			t.Errorf("事件码 %d → %q（ok=%v），期望 %q", code, got, ok, digit)
		}
	}
	if _, ok := dtmfDigit(16); ok {
		t.Error("事件码 16 不该映射出字符")
	}
}

// TestHandleDTMFEvent 收到对端 telephone-event：解出数字并回调一次；
// 同一个事件的冗余重传（事件码 + 时间戳相同）只通知一次。
func TestHandleDTMFEvent(t *testing.T) {
	e := newTestEngine(t)
	var got []string
	e.SetDTMFListener(func(digit string) { got = append(got, digit) })
	e.dtmfPT.Store(101)

	// 事件码 5（'5'）的三次冗余重传：只通知一次
	for i := 0; i < 3; i++ {
		e.handleDTMFEvent(rtp.Packet{Header: rtp.Header{PayloadType: 101, Timestamp: 1000}, Payload: []byte{5, 0x0A}})
	}
	// 同一个数字再按一次：时间戳不同 → 是**新事件**，再通知一次
	e.handleDTMFEvent(rtp.Packet{Header: rtp.Header{PayloadType: 101, Timestamp: 2000}, Payload: []byte{5, 0x0A}})
	// '*'（事件码 10）
	e.handleDTMFEvent(rtp.Packet{Header: rtp.Header{PayloadType: 101, Timestamp: 3000}, Payload: []byte{10, 0x0A}})
	// 空负载 / 未知事件码：忽略
	e.handleDTMFEvent(rtp.Packet{Header: rtp.Header{PayloadType: 101, Timestamp: 4000}})
	e.handleDTMFEvent(rtp.Packet{Header: rtp.Header{PayloadType: 101, Timestamp: 5000}, Payload: []byte{99}})

	if len(got) != 3 || got[0] != "5" || got[1] != "5" || got[2] != "*" {
		t.Fatalf("回调序列为 %v，期望 [5 5 *]", got)
	}
}

// TestPeerDTMFPT 从对端 SDP 的音频段取 telephone-event 的负载类型号。
func TestPeerDTMFPT(t *testing.T) {
	sdp := "v=0\r\nm=audio 4000 RTP/AVP 8 101\r\n" +
		"a=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\n" +
		"m=video 4002 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n"
	if got := peerDTMFPT([]byte(sdp)); got != 101 {
		t.Errorf("telephone-event PT = %d，期望 101", got)
	}
	// 换个号也对
	sdp2 := "v=0\r\nm=audio 4000 RTP/AVP 0 97\r\na=rtpmap:97 telephone-event/8000\r\n"
	if got := peerDTMFPT([]byte(sdp2)); got != 97 {
		t.Errorf("telephone-event PT = %d，期望 97", got)
	}
	// 没声明 → 0
	if got := peerDTMFPT([]byte("v=0\r\nm=audio 4000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n")); got != 0 {
		t.Errorf("未声明 telephone-event 时 PT = %d，期望 0", got)
	}
}

// TestBindRTPRTCPEvenPort RFC 3550 约定：RTP 用偶数值端口、RTCP = RTP + 1
// （服务端/网关按此配对，奇数 RTP 会被判为不规范）。
func TestBindRTPRTCPEvenPort(t *testing.T) {
	for i := 0; i < 10; i++ {
		c1, c2, err := bindRTPRTCP("127.0.0.1")
		if err != nil {
			t.Fatalf("绑定 RTP/RTCP 端口对失败: %v", err)
		}
		p1 := c1.LocalAddr().(*net.UDPAddr).Port
		p2 := c2.LocalAddr().(*net.UDPAddr).Port
		_ = c1.Close()
		_ = c2.Close()
		if p1%2 != 0 {
			t.Fatalf("RTP 端口必须为偶数，实际 %d", p1)
		}
		if p2 != p1+1 {
			t.Fatalf("RTCP 端口应为 RTP+1，实际 %d（RTP %d）", p2, p1)
		}
	}
}
