package media

import (
	"fmt"
	"sync"

	"mti-sip-phone/internal/media/g722"
)

// G.722 编解码器（REQ-MEDIA-011）。
//
// 与 G.711 / Opus 的区别都在两个"率"上（见 Codec 接口的注释）：
//   - RTP 时钟率 = **8000**（RFC 3551 规定，虽然音频是 16 kHz 宽频）；
//   - PCM 管线采样率 = **16000**：协商到这个编解码器的通话，音频设备与
//     媒体引擎整条管线切到 16 kHz（真宽频）。
//
// 因此 FrameSize（320 = 20ms@16k）≠ TimestampStep（160 = 20ms@8k 时钟）——
// 与 Opus 是同一类"两个率不是一回事"的情况，但方向相反。
//
// 纯 Go 实现（`internal/media/g722`），不依赖 CGO 与第三方库，任何构建都带。
// 编码器/解码器实例是**包级共享的原型**（supportedCodecs 只建一次），所以
// 必须实现 resetter：通话开始、协商切换、换流时引擎会调 Reset()。

type g722Codec struct {
	// 编码器会被采集回调（FeedMic）与 SIP 线程（punchHole/保活静音）并发调用；
	// 解码器由接收循环独占——各一把锁，与 Opus 同构。
	encMu sync.Mutex
	decMu sync.Mutex
	enc   *g722.Encoder
	dec   *g722.Decoder
}

// NewG722Codec 创建 G.722 编解码器。
func NewG722Codec() Codec {
	return &g722Codec{enc: g722.NewEncoder(), dec: g722.NewDecoder()}
}

const (
	// g722PayloadType RFC 3551 给 G.722 的静态负载类型号。
	g722PayloadType = 9
	// g722ClockRate G.722 的 RTP 时钟率：**8000**（RFC 3551 明确规定，
	// 与它的 16 kHz 采样率无关，别写成 16000）。
	g722ClockRate = 8000
	// g722SampleRate PCM 管线采样率：16 kHz 宽频。
	g722SampleRate = 16000
	// g722FrameSamples 每帧 PCM 样本数（20ms @16kHz）。
	g722FrameSamples = g722.FrameSamples
	// g722TimestampStep 每帧的 RTP 时间戳增量（20ms @8kHz 时钟）。
	g722TimestampStep = 160
	// g722MaxPayload 一包允许的最大字节数（20ms 的 4 倍，防异常负载）。
	g722MaxPayload = g722.FrameBytes * 4
)

func (c *g722Codec) Name() string       { return "G722" }
func (c *g722Codec) PayloadType() uint8 { return g722PayloadType }
func (c *g722Codec) ClockRate() int     { return g722ClockRate }
func (c *g722Codec) SampleRate() int    { return g722SampleRate }
func (c *g722Codec) FrameSize() int     { return g722FrameSamples }
func (c *g722Codec) TimestampStep() int { return g722TimestampStep }
func (c *g722Codec) Channels() int      { return 1 }
func (c *g722Codec) RTPMap() string     { return "G722/8000" }

// Encode 编码一帧 20ms 的 16 kHz 单声道 PCM（320 样本 → 160 字节）。
func (c *g722Codec) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != g722FrameSamples {
		return nil, fmt.Errorf("G.722 每帧需要 %d 个样本，实际 %d", g722FrameSamples, len(pcm))
	}
	out := make([]byte, g722.FrameBytes)
	c.encMu.Lock()
	c.enc.Encode(pcm, out)
	c.encMu.Unlock()
	return out, nil
}

// Decode 解码一包码流（每字节 2 个样本：160 字节 → 320 样本）。
func (c *g722Codec) Decode(data []byte) ([]int16, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("G.722 负载为空")
	}
	if len(data) > g722MaxPayload {
		return nil, fmt.Errorf("G.722 负载过长（%d 字节）", len(data))
	}
	out := make([]int16, len(data)*2)
	c.decMu.Lock()
	c.dec.Decode(data, out)
	c.decMu.Unlock()
	return out, nil
}

// Reset 复位编解码器内部状态（resetter 接口，见 Codec 注释）。
// 不实现它会把上一通电话的预测器/QMF 状态带进新通话（起始杂音）。
func (c *g722Codec) Reset() {
	c.encMu.Lock()
	c.enc.Reset()
	c.encMu.Unlock()
	c.decMu.Lock()
	c.dec.Reset()
	c.decMu.Unlock()
}
