package media

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/pion/sdp/v3"
)

// SDP 构造与解析（支持多 m= 行：音频 + 视频）。
//
// 原实现把 m=audio 硬编码在 sdp() 里、协商时只取 MediaDescriptions[0]，
// 视频加入后必须能按媒体类型定位，否则对端只改视频方向时会被误判成音频
// （保持 re-INVITE 的方向镜像因此出错），纯视频 offer 也会被当成音频解析。

// ErrMediaNotFound 指定的媒体类型不在 SDP 中。
var ErrMediaNotFound = errors.New("SDP 中缺少该媒体类型")

// ErrMediaRejected 该媒体的 m= 端口为 0（对端拒绝）。
var ErrMediaRejected = errors.New("对端拒绝了该媒体")

// sdpCodec 一条编解码器（对应 a=rtpmap，可带 a=fmtp）。
type sdpCodec struct {
	PT        uint8
	Encoding  string // rtpmap 编码名，如 "PCMA" / "opus" / "H264" / "H263-1998"
	ClockRate int
	Channels  int    // >1 时输出 "/<Channels>"（Opus 固定 2；G.711 与视频不写）
	FMTP      string // 空则不输出 a=fmtp
}

// sdpSection 一个媒体段。
type sdpSection struct {
	Kind     string // "audio" / "video"
	Port     int
	RTCPPort int // 0 = 不输出 a=rtcp
	Codecs   []sdpCodec
	Dir      string // 空则不输出方向属性
	// Raw 非空时原样输出该文本段（用于 m=video 0 拒绝段：只输出 m= 行，
	// 不带 rtpmap/rtcp/方向属性——RFC 3264 规定被拒媒体的属性无意义）。
	Raw string
}

// buildSDP 组装完整 SDP 文本：会话头 + 各媒体段。
//
// 输出顺序与原 sdp() 逐字节一致（见 TestSDPOfferGolden）：
//   m= 行 → 全部 a=rtpmap → 全部 a=fmtp → a=rtcp → a=<方向>
func buildSDP(ssrc uint32, ip string, sections []sdpSection) string {
	var b strings.Builder
	fmt.Fprintf(&b, "v=0\r\no=- %d 1 IN IP4 %s\r\ns=mti-sip-phone\r\nc=IN IP4 %s\r\nt=0 0\r\n",
		ssrc, ip, ip)
	for _, s := range sections {
		if s.Raw != "" {
			b.WriteString(s.Raw)
			continue
		}
		fmt.Fprintf(&b, "m=%s %d RTP/AVP", s.Kind, s.Port)
		for _, c := range s.Codecs {
			fmt.Fprintf(&b, " %d", c.PT)
		}
		b.WriteString("\r\n")
		for _, c := range s.Codecs {
			fmt.Fprintf(&b, "a=rtpmap:%d %s/%d", c.PT, c.Encoding, c.ClockRate)
			if c.Channels > 1 {
				// RFC 7587 要求 Opus 恒写 "/2"（线上常量，与实际单声道无关）
				fmt.Fprintf(&b, "/%d", c.Channels)
			}
			b.WriteString("\r\n")
		}
		for _, c := range s.Codecs {
			if c.FMTP != "" {
				fmt.Fprintf(&b, "a=fmtp:%d %s\r\n", c.PT, c.FMTP)
			}
		}
		if s.RTCPPort != 0 {
			fmt.Fprintf(&b, "a=rtcp:%d\r\n", s.RTCPPort)
		}
		if s.Dir != "" {
			fmt.Fprintf(&b, "a=%s\r\n", s.Dir)
		}
	}
	return b.String()
}

// --- 解析 ---

// MediaInfo 一个媒体段的解析结果。
type MediaInfo struct {
	Kind     string
	Port     int
	Addr     *net.UDPAddr // 远端 RTP 地址（端口为 0 时无意义，看 Rejected）
	RTCPPort int          // 对端声明的 RTCP 端口（0 = 未声明，惯例 RTP+1）
	Dir      string       // sendrecv / sendonly / recvonly / inactive
	Rejected bool         // m= 端口为 0
	PTs      []uint8      // 按 offer 中出现的顺序
	Rtpmap   map[uint8]string
	Fmtp     map[uint8]string
}

// ParseMedia 从 SDP 中解析指定媒体类型（"audio" / "video"）。
func ParseMedia(body []byte, kind string) (*MediaInfo, error) {
	sd := &sdp.SessionDescription{}
	if err := sd.Unmarshal(body); err != nil {
		return nil, fmt.Errorf("SDP 解析失败: %w", err)
	}
	return mediaInfoOf(sd, kind)
}

// Offer 已解析的对端 SDP（offer 或 answer），保留媒体段原始顺序。
// answer 必须严格按 offer 的 m= 行顺序与数量作答（RFC 3264），故顺序要留着。
type Offer struct {
	sd    *sdp.SessionDescription
	order []string
}

// ParseSDP 解析 SDP 文本。
func ParseSDP(body []byte) (*Offer, error) {
	sd := &sdp.SessionDescription{}
	if err := sd.Unmarshal(body); err != nil {
		return nil, fmt.Errorf("SDP 解析失败: %w", err)
	}
	o := &Offer{sd: sd}
	for _, md := range sd.MediaDescriptions {
		o.order = append(o.order, strings.ToLower(md.MediaName.Media))
	}
	return o, nil
}

// Order 媒体段顺序（小写，如 ["audio","video"]）。
func (o *Offer) Order() []string { return o.order }

// Media 取指定媒体段的解析结果。
func (o *Offer) Media(kind string) (*MediaInfo, error) { return mediaInfoOf(o.sd, kind) }

// Has 是否包含指定媒体段，且该媒体未被拒绝（m= 端口非 0）。
func (o *Offer) Has(kind string) bool {
	info, err := o.Media(kind)
	return err == nil && !info.Rejected
}

// HasSection 是否包含指定媒体段（含被拒绝的，即 m= 端口为 0）。
func (o *Offer) HasSection(kind string) bool {
	_, err := o.Media(kind)
	return err == nil
}

// mediaInfoOf 从已解析的 SDP 中取指定媒体段。
// 连接地址规则与原实现一致：m= 行的 c= 优先，其次会话级 c=。
func mediaInfoOf(sd *sdp.SessionDescription, kind string) (*MediaInfo, error) {
	var md *sdp.MediaDescription
	for _, m := range sd.MediaDescriptions {
		if strings.EqualFold(m.MediaName.Media, kind) {
			md = m
			break
		}
	}
	if md == nil {
		return nil, fmt.Errorf("%w: %s", ErrMediaNotFound, kind)
	}

	info := &MediaInfo{
		Kind:     kind,
		Port:     md.MediaName.Port.Value,
		Dir:      directionOf(md),
		Rtpmap:   make(map[uint8]string),
		Fmtp:     make(map[uint8]string),
		Rejected: md.MediaName.Port.Value == 0,
	}

	// 连接地址
	ip := sd.ConnectionInformation
	if md.ConnectionInformation != nil {
		ip = md.ConnectionInformation
	}
	if ip == nil || ip.Address == nil {
		if !info.Rejected {
			return nil, fmt.Errorf("SDP 缺少 %s 的连接信息", kind)
		}
	} else {
		addr := &net.UDPAddr{IP: net.ParseIP(ip.Address.Address), Port: info.Port}
		if addr.IP == nil {
			return nil, fmt.Errorf("SDP 连接地址非法: %s", ip.Address.Address)
		}
		info.Addr = addr
	}

	for _, attr := range md.Attributes {
		switch attr.Key {
		case "rtcp":
			if v, err := strconv.Atoi(attr.Value); err == nil {
				info.RTCPPort = v
			}
		case "rtpmap":
			// "97 VP8/90000" / "101 telephone-event/8000"
			fields := strings.Fields(attr.Value)
			if len(fields) < 2 {
				continue
			}
			pt, err := strconv.ParseUint(fields[0], 10, 8)
			if err != nil {
				continue
			}
			info.Rtpmap[uint8(pt)] = fields[1] // "VP8/90000"
		case "fmtp":
			// "96 profile-level-id=42e01f;packetization-mode=1"
			sp := strings.IndexByte(attr.Value, ' ')
			if sp <= 0 {
				continue
			}
			pt, err := strconv.ParseUint(attr.Value[:sp], 10, 8)
			if err != nil {
				continue
			}
			info.Fmtp[uint8(pt)] = strings.TrimSpace(attr.Value[sp+1:])
		}
	}

	for _, s := range md.MediaName.Formats {
		if v, err := strconv.ParseUint(s, 10, 8); err == nil {
			info.PTs = append(info.PTs, uint8(v))
		}
	}
	return info, nil
}

// directionOf 读取媒体段的方向属性（无则按 RFC 3264 默认双向）。
func directionOf(md *sdp.MediaDescription) string {
	for _, attr := range md.Attributes {
		switch attr.Key {
		case "sendonly", "sendrecv", "recvonly", "inactive":
			return attr.Key
		}
	}
	return "sendrecv"
}

// EncodingOf 取该负载类型对应的 rtpmap 编码名（大写，去掉时钟率）。
// 例：map[97]="VP8/90000" → "VP8"；"H263-1998/90000" → "H263-1998"。
func (m *MediaInfo) EncodingOf(pt uint8) string {
	rm := m.Rtpmap[pt]
	if rm == "" {
		return ""
	}
	if i := strings.IndexByte(rm, '/'); i >= 0 {
		rm = rm[:i]
	}
	return strings.ToUpper(rm)
}

// EncodingMatches 该负载类型的 rtpmap 名是否等于 name（不区分大小写）。
func (m *MediaInfo) EncodingMatches(pt uint8, name string) bool {
	return strings.EqualFold(m.EncodingOf(pt), name)
}

// FmtpParam 取该负载类型 a=fmtp 中指定键的值。
// 第二个返回值区分「未声明」与「声明为空」——H264 的 packetization-mode
// 缺省为 0，但「未声明」在实践中的含义与「显式声明 0」不同，必须能区分。
func (m *MediaInfo) FmtpParam(pt uint8, key string) (string, bool) {
	lowerKey := strings.ToLower(key) + "="
	for _, kv := range strings.Split(m.Fmtp[pt], ";") {
		kv = strings.TrimSpace(kv)
		if len(kv) >= len(lowerKey) && strings.EqualFold(kv[:len(lowerKey)], lowerKey) {
			return kv[len(lowerKey):], true
		}
	}
	return "", false
}

// PacketizationMode 取 H264 fmtp 中的 packetization-mode（缺省按 RFC 6184 为 0）。
func (m *MediaInfo) PacketizationMode(pt uint8) int {
	if v, ok := m.FmtpParam(pt, "packetization-mode"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return 0
}

// SDPDirection 提取音频段的方向属性（保持既有调用点语义不变）。
func SDPDirection(sdpText []byte) string {
	return SDPDirectionFor(sdpText, "audio")
}

// SDPDirectionFor 提取指定媒体段的方向属性。
// 无方向属性、解析失败或该媒体不存在时返回 sendrecv（与旧行为一致）。
func SDPDirectionFor(sdpText []byte, kind string) string {
	info, err := ParseMedia(sdpText, kind)
	if err != nil {
		return "sendrecv"
	}
	return info.Dir
}

// MirrorDirection 镜像媒体方向（对端 offer 的方向 → 本端 answer 的方向）：
// 对端只发不收（sendonly）→ 本端只收不发（recvonly），依此类推。
func MirrorDirection(dir string) string {
	switch dir {
	case "sendonly":
		return "recvonly"
	case "recvonly":
		return "sendonly"
	case "inactive":
		return "inactive"
	default:
		return "sendrecv"
	}
}
