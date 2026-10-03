//go:build audio && cgo

package media

/*
#cgo LDFLAGS: -lopus
#cgo linux LDFLAGS: -lm

#include <opus/opus.h>

// opus_encoder_ctl / opus_decoder_ctl 是可变参数函数，且参数宏（OPUS_SET_BITRATE 等）
// 展开成多个实参，cgo 调不了——统一用静态包装函数固定下来。
static int sp_opus_set_bitrate(OpusEncoder *e, int bps) {
	return opus_encoder_ctl(e, OPUS_SET_BITRATE(bps));
}
static int sp_opus_reset_encoder(OpusEncoder *e) {
	return opus_encoder_ctl(e, OPUS_RESET_STATE);
}
static int sp_opus_reset_decoder(OpusDecoder *d) {
	return opus_decoder_ctl(d, OPUS_RESET_STATE);
}

// OPUS_APPLICATION_VOIP 是枚举常量，cgo 侧类型是 C 的枚举而非 int，
// 直接作为实参可能对不上——在 C 里取用最稳。
static OpusEncoder *sp_opus_encoder_new(int fs, int channels, int *err) {
	return opus_encoder_create(fs, channels, OPUS_APPLICATION_VOIP, err);
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

// Opus 编解码器（libopus，cgo 静态链接）。
//
// **采样率**：本端内部 PCM 管线恒为 8kHz（音频设备 backend_malgo 固定 8000、
// 播放缓冲按 8000 换算），因此编码器/解码器都以 Fs=8000 创建：libopus 内部
// 按 48kHz 处理后降到 8kHz 输出（窄带），本端不需要任何重采样；对端发来的
// 宽带/全带包同样能正常解码（libopus 自行降采样）。
//
// **RTP 侧时钟率恒为 48000**（RFC 7587 的线上常量），所以 20ms 帧的时间戳
// 步长是 960，与 PCM 帧长 160 不是一回事——见 Codec.TimestampStep。
// rtpmap 按 RFC 7587 固定写 `opus/48000/2`（声道数 2 是线上常量，与实际单声道无关）。
//
// **负载类型固定 96**（本端 offer 用）；作为 answerer 仍复用对端 offer 里的号
// （Engine.activePT），否则动态 PT 会协商串味。
//
// **有状态**：编解码器实例在包级共享（supportedCodecs 是原型），C 状态靠
// Reset() 在通话开始/换流/编解码切换时复位；本应用限单通话，这样是安全的。
// 若将来支持多路通话，必须改成每个 Engine 一份实例。
const (
	opusPayloadType   = 96
	opusSampleRate    = 8000
	opusFrameSamples  = opusSampleRate / 50 // 20ms 帧的 PCM 样本数（160）
	opusTimestampStep = 48000 / 50          // 20ms 帧的 RTP 时间戳增量（960）
	opusChannels      = 2                   // RFC 7587：rtpmap 恒为 /2
	// opusMaxFrameSamples 解码端能接受的最大帧长：Opus 单包最长 120ms。
	opusMaxFrameSamples = opusSampleRate * 120 / 1000 // 960
	// opusMaxPacket 单包上限（20ms 窄带实际只有几十字节，取 MTU 安全值）。
	opusMaxPacket = 1275
	// opusBitrate 窄带语音码率（G.711 是 64kbps）。
	opusBitrate = 16000
)

// optionalCodecs 由构建标签提供的可选编解码器（按优先级排在 G.711 之后）。
var optionalCodecs = newOptionalCodecs()

// opusInitErr 本构建 Opus 实例创建失败的原因（包初始化时日志系统尚未就绪，
// 由 NewEngine 在运行时记入日志）。
var opusInitErr error

type opusCodec struct {
	// 编码器会被采集回调（FeedMic）与 SIP 线程（punchHole/保活）并发调用，
	// 解码器由接收循环独占（含 PLC）——各一把锁。
	encMu sync.Mutex
	decMu sync.Mutex
	enc   *C.OpusEncoder
	dec   *C.OpusDecoder
}

func newOptionalCodecs() []Codec {
	c, err := newOpusCodec()
	if err != nil {
		opusInitErr = err
		return nil
	}
	return []Codec{c}
}

func newOpusCodec() (*opusCodec, error) {
	var errno C.int
	enc := C.sp_opus_encoder_new(C.int(opusSampleRate), C.int(1), &errno)
	if errno != C.OPUS_OK || enc == nil {
		return nil, fmt.Errorf("opus_encoder_create 失败: %d", int(errno))
	}
	if rc := C.sp_opus_set_bitrate(enc, C.int(opusBitrate)); rc != C.OPUS_OK {
		C.opus_encoder_destroy(enc)
		return nil, fmt.Errorf("设置 Opus 码率失败: %d", int(rc))
	}
	dec := C.opus_decoder_create(C.opus_int32(opusSampleRate), C.int(1), &errno)
	if errno != C.OPUS_OK || dec == nil {
		C.opus_encoder_destroy(enc)
		return nil, fmt.Errorf("opus_decoder_create 失败: %d", int(errno))
	}
	return &opusCodec{enc: enc, dec: dec}, nil
}

func (c *opusCodec) Name() string       { return "opus" }
func (c *opusCodec) PayloadType() uint8 { return opusPayloadType }
func (c *opusCodec) ClockRate() int     { return 48000 }
func (c *opusCodec) FrameSize() int     { return opusFrameSamples }
func (c *opusCodec) TimestampStep() int { return opusTimestampStep }
func (c *opusCodec) Channels() int      { return opusChannels }
func (c *opusCodec) RTPMap() string     { return "opus/48000/2" }

// Encode 编码一帧 20ms 的 8kHz 单声道 PCM。
func (c *opusCodec) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != opusFrameSamples {
		return nil, fmt.Errorf("Opus 每帧需要 %d 个样本，实际 %d", opusFrameSamples, len(pcm))
	}
	buf := make([]byte, opusMaxPacket)
	c.encMu.Lock()
	n := C.opus_encode(c.enc,
		(*C.opus_int16)(unsafe.Pointer(&pcm[0])), C.int(opusFrameSamples),
		(*C.uchar)(unsafe.Pointer(&buf[0])), C.opus_int32(len(buf)))
	c.encMu.Unlock()
	if n < 0 {
		return nil, fmt.Errorf("opus_encode 失败: %d", int(n))
	}
	return buf[:n], nil
}

// Decode 解码一个 RTP 负载为 8kHz PCM（帧长可为 2.5–120ms，由包内信令决定）。
func (c *opusCodec) Decode(data []byte) ([]int16, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("空的 Opus 负载")
	}
	out := make([]int16, opusMaxFrameSamples)
	c.decMu.Lock()
	n := C.opus_decode(c.dec,
		(*C.uchar)(unsafe.Pointer(&data[0])), C.opus_int32(len(data)),
		(*C.opus_int16)(unsafe.Pointer(&out[0])), C.int(opusMaxFrameSamples), 0)
	c.decMu.Unlock()
	if n < 0 {
		return nil, fmt.Errorf("opus_decode 失败: %d", int(n))
	}
	return out[:n], nil
}

// DecodePLC 丢包隐藏：让 libopus 按上一包的状态外推 samples 个样本
//（比补静音自然得多）。样本数必须是合法的 Opus 帧长。
func (c *opusCodec) DecodePLC(samples int) []int16 {
	if samples <= 0 || samples > opusMaxFrameSamples {
		return nil
	}
	out := make([]int16, samples)
	c.decMu.Lock()
	n := C.opus_decode(c.dec, nil, 0,
		(*C.opus_int16)(unsafe.Pointer(&out[0])), C.int(samples), 0)
	c.decMu.Unlock()
	if n <= 0 {
		return nil
	}
	return out[:n]
}

// Reset 复位编解码器内部状态（新通话 / 媒体流切换 / 编解码切换时）。
func (c *opusCodec) Reset() {
	c.encMu.Lock()
	C.sp_opus_reset_encoder(c.enc)
	c.encMu.Unlock()
	c.decMu.Lock()
	C.sp_opus_reset_decoder(c.dec)
	c.decMu.Unlock()
}
