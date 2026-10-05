// Package media 实现通话媒体引擎：RTP 收发、G.711 编解码、抖动缓冲。
//
// 编解码器设计为可插拔接口（架构约束），后续版本可增加 G.722 / Opus 等。
package media

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/zaf/g711"
)

// Codec 编解码器接口：音频帧（PCM 16bit）与 RTP 负载之间的转换。
type Codec interface {
	// Name 编解码器名，如 PCMU / PCMA / opus
	Name() string
	// PayloadType RTP 负载类型号（PCMU=0，PCMA=8，opus=96）
	PayloadType() uint8
	// ClockRate RTP 时钟率（G.711 = 8000，Opus = 48000，G.722 = 8000）
	ClockRate() int
	// SampleRate 本编解码器要求的 **PCM 管线采样率**（Hz）。**与 ClockRate 不是
	// 一回事**：G.722 的 RTP 时钟是 8000 而 PCM 是 16000；Opus 的 RTP 时钟是
	// 48000 而本端 PCM 是 8000（它以 Fs=8000 创建、窄带工作）。
	//
	// 媒体引擎、音频设备、播放缓冲与诊断 WAV 都按**当前协商编解码器的**
	// SampleRate 工作：G.711 / Opus 通话恒为 8 kHz（与历史行为一字不变），
	// 协商到 G.722 的通话整条管线切到 16 kHz（REQ-MEDIA-011）。
	SampleRate() int
	// FrameSize 每帧 PCM 样本数 = SampleRate()/50（20ms 帧）：
	// G.711/Opus 160（8kHz）、G.722 320（16kHz）
	FrameSize() int
	// TimestampStep 每帧的 RTP 时间戳增量（时钟率单位；20ms 帧 = ClockRate/50）。
	// **不等于 FrameSize**：Opus 的 PCM 帧长是 160（8kHz 管线）而时间戳步长
	// 是 960（48000 时钟）——FrameSize 曾兼任二者，静态 PT 的 G.711 下两者
	// 恰好相等才没暴露。
	TimestampStep() int
	// Channels SDP a=rtpmap 宣告的声道数：1 时省略（G.711 惯例），
	// Opus 固定为 2（RFC 7587 的线上常量，与实际单声道无关）。
	Channels() int
	// Encode PCM → RTP 负载
	Encode(pcm []int16) ([]byte, error)
	// Decode RTP 负载 → PCM
	Decode(data []byte) ([]int16, error)
	// RTPMap SDP a=rtpmap 属性值，如 "PCMU/8000"
	//（本端不用它拼 SDP——audioSection 自己拼 ClockRate/Channels）
	RTPMap() string
}

// NewG711Codec 创建 G.711 编解码器：muLaw 为 true 时 PCMU（μ律），否则 PCMA（A 律）。
func NewG711Codec(muLaw bool) Codec {
	if muLaw {
		return &g711Codec{name: "PCMU", pt: 0, muLaw: true}
	}
	return &g711Codec{name: "PCMA", pt: 8, muLaw: false}
}

type g711Codec struct {
	name  string
	pt    uint8
	muLaw bool
}

func (c *g711Codec) Name() string       { return c.name }
func (c *g711Codec) PayloadType() uint8 { return c.pt }
func (c *g711Codec) ClockRate() int     { return 8000 }

// SampleRate G.711 是 8 kHz 窄带：RTP 时钟与 PCM 采样率相同。
func (c *g711Codec) SampleRate() int { return 8000 }
func (c *g711Codec) FrameSize() int  { return 160 } // 20ms @ 8kHz
// TimestampStep G.711 的 PCM 帧长与 RTP 时间戳步长同为 160（20ms @ 8kHz）。
func (c *g711Codec) TimestampStep() int { return 160 }

// Channels G.711 惯例不在 rtpmap 里写声道数（返回 1 即省略）。
func (c *g711Codec) Channels() int  { return 1 }
func (c *g711Codec) RTPMap() string { return fmt.Sprintf("%s/8000", c.name) }

// Encode 将 PCM 样本编码为 G.711 字节。
func (c *g711Codec) Encode(pcm []int16) ([]byte, error) {
	b := pcmToBytes(pcm)
	if c.muLaw {
		return g711.EncodeUlaw(b), nil
	}
	return g711.EncodeAlaw(b), nil
}

// Decode 将 G.711 字节解码为 PCM 样本。
func (c *g711Codec) Decode(data []byte) ([]int16, error) {
	var b []byte
	if c.muLaw {
		b = g711.DecodeUlaw(data)
	} else {
		b = g711.DecodeAlaw(data)
	}
	return bytesToPCM(b), nil
}

// pcmToBytes int16 切片转小端字节（zaf/g711 接口要求）。
func pcmToBytes(pcm []int16) []byte {
	b := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(s))
	}
	return b
}

// bytesToPCM 小端字节转 int16 切片。
func bytesToPCM(b []byte) []int16 {
	n := len(b) / 2
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return pcm
}

// ClampInt16 限制样本值到 int16 范围（音频叠加后使用）。
func ClampInt16(v int32) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}

// Silence 生成 n 个静音样本。
func Silence(n int) []int16 {
	return make([]int16, n)
}
