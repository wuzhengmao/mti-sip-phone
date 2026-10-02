// Package video 提供视频编解码与摄像头采集。
//
// 编解码后端是 FFmpeg 静态库（cgo），只有当构建时带上 `video` 标签、
// 且 third_party/video/<target> 里的静态库存在时才编译进去；否则整个包
// 退化为空实现（Supported() == false），上层据此隐藏视频入口、SDP 不宣告
// m=video，音频功能完全不受影响。
//
// 分工：
//   - 解码（H264/H263/VP8/VP9）与 swscale 色彩转换 → FFmpeg
//   - 编码 H264 → OpenH264；编码 VP8/VP9 → libvpx；编码 H263 → FFmpeg
//
// 线程模型：Encoder/Decoder 均**不是并发安全**的，同一实例只能被一个
// goroutine 使用（与 FFmpeg/OpenH264/libvpx 的上下文约束一致）。
package video

import (
	"errors"
	"image"
)

// ErrUnsupported 当前构建/平台不支持视频。
var ErrUnsupported = errors.New("当前构建未启用视频支持（需要 -tags video 与 FFmpeg 静态库，见 scripts/build-video-libs.sh）")

// CodecName 支持的编码名（与 SDP rtpmap 名一致）。
const (
	CodecH264  = "H264"
	CodecH263  = "H263-1998"
	CodecVP8   = "VP8"
	CodecVP9   = "VP9"
)

// YUV420P 一帧 I420 平面数据（编码器输入）。
// 平面数据由 Go 侧持有，跨 cgo 边界一律拷贝，规避 C 侧缓冲生命周期问题。
type YUV420P struct {
	W, H                      int
	Y, U, V                   []byte
	StrideY, StrideU, StrideV int
}

// NewYUV420P 分配一帧 I420（紧凑排列，stride == 宽）。
func NewYUV420P(w, h int) *YUV420P {
	cw, ch := (w+1)/2, (h+1)/2
	return &YUV420P{
		W: w, H: h,
		Y:                     make([]byte, w*h),
		U:                     make([]byte, cw*ch),
		V:                     make([]byte, cw*ch),
		StrideY:               w,
		StrideU:               cw,
		StrideV:               cw,
	}
}

// Frame 一帧 RGBA 图像（内存布局与 image.RGBA 一致，可零拷贝包装）。
type Frame struct {
	W, H   int
	Stride int
	Pix    []byte // RGBA，A 恒为 0xFF
}

// RGBA 零拷贝包装为 image.RGBA（共享 Pix，调用方不得改动）。
func (f *Frame) RGBA() *image.RGBA {
	return &image.RGBA{Pix: f.Pix, Stride: f.Stride, Rect: image.Rect(0, 0, f.W, f.H)}
}

// Encoder 视频编码器（单 goroutine 使用）。
type Encoder interface {
	// Encode 编码一帧。forceKey 要求本帧为关键帧（收到 PLI/FIR 时用）。
	// 返回的 data 为可直接交给 RTP 打包器的码流：
	// H264 为 Annex-B，VP8/VP9 为裸帧，H263-1998 为 H.263 码流（含起始码）。
	Encode(f *YUV420P, forceKey bool) (data []byte, keyframe bool, err error)
	// SetBitrate 动态调整码率（bps）。不支持时静默忽略。
	SetBitrate(bps int) error
	Close()
}

// Decoder 视频解码器（单 goroutine 使用）。
type Decoder interface {
	// Decode 解码一个完整访问单元。无输出帧（如参数集尚未齐备）返回 (nil, nil)。
	Decode(au []byte) (*Frame, error)
	// Reset 清空内部状态（换流/重新开启视频时调用）。
	Reset()
	Close()
}

// Supported 当前构建是否具备视频能力。
func Supported() bool { return supported }

// EncoderAvailable 指定编码能否用于**发送**（编不出来只能收）。
func EncoderAvailable(name string) bool { return encoderAvailable(name) }

// NewEncoder 创建编码器（单 goroutine 使用）。
func NewEncoder(name string, w, h, fps, bitrateBps int) (Encoder, error) {
	return newEncoder(name, w, h, fps, bitrateBps)
}

// NewDecoder 创建解码器（单 goroutine 使用）。
func NewDecoder(name string) (Decoder, error) { return newDecoder(name) }

// ScaleYUV 缩放一帧 I420 到目标尺寸（sws_scale）。
func ScaleYUV(src *YUV420P, w, h int) (*YUV420P, error) { return scaleYUV(src, w, h) }

// ToRGBA 把 I420 转成 RGBA（sws_scale），供 Fyne 显示。
func ToRGBA(src *YUV420P) (*Frame, error) { return toRGBA(src) }
