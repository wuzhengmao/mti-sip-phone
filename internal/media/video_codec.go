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
//
// PT 号是**本端 offer 的固定号**（用户指定）：H264=99 / H263-1998=98 /
// VP8=100 / VP9=101——号段与优先级顺序无关，m= 行仍按上面的优先级列出
//（`m=video … RTP/AVP 99 100 101 98`）。音频 Opus 用 96，两者在不同 m= 行，互不冲突。
// 作为 answerer 时**不**使用这些号，仍按 RFC 3264 复用对端 offer 里的号
//（见 answerSection 的注释：按本端号回会让协商串味）。
var supportedVideoCodecs = []VideoCodec{
	{
		Name: videoCodecH264, PT: 99, ClockRate: 90000,
		// profile-level-id=42e01f（Constrained Baseline L3.1）、packetization-mode=1（FU-A 分片）
		FMTP: "profile-level-id=42e01f;packetization-mode=1;level-asymmetry-allowed=1",
	},
	{Name: videoCodecVP8, PT: 100, ClockRate: 90000},
	{Name: videoCodecVP9, PT: 101, ClockRate: 90000},
	{Name: videoCodecH263, PT: 98, ClockRate: 90000, FMTP: "CIF=1;QCIF=1"},
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
// 返回本地编码描述、**对端 offer 里的 PT 号**，以及 answer 应使用的 fmtp。
//
// H264 的 packetization-mode=0（单 NAL 模式）**不再跳过**：该参数说的是对端
// 自己怎么发（对端只发单 NAL），并不限制本端接收——本端解包器对单 NAL /
// STAP-A / FU-A 一视同仁。而拒掉整条 m=video 的代价极大：对端也就不发视频了，
// 两边全黑。实测（FreeSWITCH 呼入视频电话，offer 是
// `a=fmtp:102 profile-level-id=42000a; packetization-mode=0`）200 OK 回
// `m=video 0`，接听后没有任何画面。此前对「未声明 packetization-mode」
//（RFC 6184 里同样等价于模式 0，且更常见）就是宽容处理的，现在两者一致。
//
// 发送侧仍按 pion 的 H264 打包器走（NAL 塞得进 MTU 就单包，大帧 FU-A 分片）：
// 真按模式 0 只发单包等于发不出画面；而绝大多数接收端（含 FreeSWITCH——它的
// 视频是 passthrough、根本不解析 H264 fmtp）对 FU-A 都能解。
func negotiateVideo(info *MediaInfo) (VideoCodec, uint8, string, bool) {
	for _, c := range supportedVideoCodecs {
		if !video.EncoderAvailable(c.Name) && !videoCodecDecodable(c.Name) {
			continue
		}
		for _, pt := range info.PTs {
			if !info.EncodingMatches(pt, c.Name) {
				continue
			}
			return c, pt, answerVideoFMTP(c, info, pt), true
		}
	}
	return VideoCodec{}, 0, "", false
}

// answerVideoFMTP answer 里视频段的 fmtp：本端模板（描述本端发送什么），
// 但 **packetization-mode 换成对端 offer 声明过的值**——answer 只能选 offer
// 里出现过的模式，回本端的 1 会被严格的对端判为协商失败（488）。
// 对端未声明该参数时保持模板原样（此时按 RFC 6184 缺省即模式 0，但实践中
// 省略它的对端都支持 FU-A，回 1 反而更贴合实际行为）。
func answerVideoFMTP(c VideoCodec, info *MediaInfo, pt uint8) string {
	mode, ok := info.FmtpParam(pt, "packetization-mode")
	if !ok || !strings.Contains(c.FMTP, "packetization-mode=") {
		return c.FMTP
	}
	parts := strings.Split(c.FMTP, ";")
	for i, p := range parts {
		if k, _, found := strings.Cut(p, "="); found && strings.EqualFold(strings.TrimSpace(k), "packetization-mode") {
			parts[i] = "packetization-mode=" + strings.TrimSpace(mode)
		}
	}
	return strings.Join(parts, ";")
}

// VideoCodecSupported 指定编码在当前构建里可用（UI 判断视频入口是否显示）。
func VideoCodecSupported(name string) bool {
	_, ok := videoCodecByName(name)
	return ok && (video.EncoderAvailable(name) || videoCodecDecodable(name))
}

// VideoSupported 当前构建是否具备视频能力。
func VideoSupported() bool { return video.Supported() }
