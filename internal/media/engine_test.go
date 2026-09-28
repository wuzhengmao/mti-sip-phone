package media

import (
	"net"
	"testing"
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
