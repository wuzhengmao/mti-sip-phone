package media

import (
	"strings"

	"mti-sip-phone/internal/video"
)

// 视频编解码器表与协商。
//
// 与音频的关键差异：视频**全部使用动态负载类型**（96–127），
// 因此协商必须按 a=rtpmap 编码名匹配，且 answer 必须复用对端 offer 里的
// PT 号（RFC 3264）——按本地号回会导致协商串味。

// 支持的视频编码名（与 SDP rtpmap 名一致）。
const (
	videoCodecH264 = "H264"
	videoCodecH263 = "H263-1998"
	videoCodecVP8  = "VP8"
	videoCodecVP9  = "VP9"
)

// VideoCodec 一个视频编解码器的 SDP 描述。
type VideoCodec struct {
	Name      string
	PT        uint8 // 本端 offer 使用的动态负载类型号
	ClockRate int
	FMTP      string // a=fmtp 值（可为空）
}

// supportedVideoCodecs 按优先级排列：H264 > VP8 > VP9 > H263-1998。
// H264 互通性最好、VP8/VP9 次之，H263-1998 最老。调整优先级只改这里。
var supportedVideoCodecs = []VideoCodec{
	{
		Name: videoCodecH264, PT: 96, ClockRate: 90000,
		// profile-level-id=42e01f（Constrained Baseline L3.1）、packetization-mode=1（FU-A 分片）
		FMTP: "profile-level-id=42e01f;packetization-mode=1;level-asymmetry-allowed=1",
	},
	{Name: videoCodecVP8, PT: 97, ClockRate: 90000},
	{Name: videoCodecVP9, PT: 98, ClockRate: 90000},
	{Name: videoCodecH263, PT: 99, ClockRate: 90000, FMTP: "CIF=1;QCIF=1"},
}

// videoCodecByName 按编码名取描述。
func videoCodecByName(name string) (VideoCodec, bool) {
	for _, c := range supportedVideoCodecs {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return VideoCodec{}, false
}

// videoCodecByPT 按负载类型号取描述（本端号，answer 场景不适用）。
func videoCodecByPT(pt uint8) (VideoCodec, bool) {
	for _, c := range supportedVideoCodecs {
		if c.PT == pt {
			return c, true
		}
	}
	return VideoCodec{}, false
}

// usableVideoCodecs 本端当前可用于 SDP 宣告的编码（编译进来的那些）。
// 未启用视频的构建返回空，上层据此不宣告 m=video。
func usableVideoCodecs() []VideoCodec {
	if !video.Supported() {
		return nil
	}
	out := make([]VideoCodec, 0, len(supportedVideoCodecs))
	for _, c := range supportedVideoCodecs {
		if video.EncoderAvailable(c.Name) || videoCodecDecodable(c.Name) {
			out = append(out, c)
		}
	}
	return out
}

// videoCodecDecodable 本端能否解码该编码（解码走 FFmpeg 原生解码器，
// 与编码器可用性无关；未启用视频时恒为 false）。
func videoCodecDecodable(name string) bool {
	if !video.Supported() {
		return false
	}
	// 四种编码的 FFmpeg 原生解码器都在同一份静态库里，能编或能解即视为可用；
	// 单独探测解码器没有额外信息量，这里以 Supported() 为准。
	switch name {
	case videoCodecH264, videoCodecH263, videoCodecVP8, videoCodecVP9:
		return true
	}
	return false
}

// negotiateVideo 在对端的视频媒体段中按本地优先级挑选编码。
// 返回本地编码描述与**对端 offer 里的 PT 号**。
//
// H264 特例：对端显式声明 packetization-mode=0 时跳过——我们发不出
// 「单 NAL 必须小于 MTU」的约束，大帧只能靠 FU-A 分片。
// 未声明该属性时按宽容处理（多数实现省略但支持 FU-A）。
func negotiateVideo(info *MediaInfo) (VideoCodec, uint8, bool) {
	for _, c := range supportedVideoCodecs {
		if !video.EncoderAvailable(c.Name) && !videoCodecDecodable(c.Name) {
			continue
		}
		for _, pt := range info.PTs {
			if !info.EncodingMatches(pt, c.Name) {
				continue
			}
			if c.Name == videoCodecH264 {
				if mode, ok := info.FmtpParam(pt, "packetization-mode"); ok && strings.TrimSpace(mode) == "0" {
					continue
				}
			}
			return c, pt, true
		}
	}
	return VideoCodec{}, 0, false
}

// VideoCodecSupported 指定编码在当前构建里可用（UI 判断视频入口是否显示）。
func VideoCodecSupported(name string) bool {
	_, ok := videoCodecByName(name)
	return ok && (video.EncoderAvailable(name) || videoCodecDecodable(name))
}

// VideoSupported 当前构建是否具备视频能力。
func VideoSupported() bool { return video.Supported() }
