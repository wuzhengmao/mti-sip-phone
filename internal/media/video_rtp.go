package media

import (
	"encoding/binary"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// 视频 RTP 打包/解包。
//
// H264/VP8/VP9 直接复用 pion/rtp/codecs 的实现；**H263-1998 没有现成实现**，
// 按 RFC 4629 §5.1 自写（16 位负载头 + 起始码剥离）。
//
// 与音频的关键差异：一帧视频会被拆成多个 RTP 包，**同一帧的所有包共用一个
// 时间戳，最后一包置 marker**；音频则是一包一帧、每包必发。

// videoMTU 单个 RTP 负载的上限（含编解码专属的负载头）。
// 取 1200 是留出 RTP/UDP/IP 头的余量，避免 IP 分片（分片对实时视频是灾难）。
const videoMTU = 1200

// videoPacketizer 一帧编码数据 → 一组 RTP 负载（不含 RTP 头）。
// 实现可以是**有状态**的（H264 的 STAP-A 会暂存 SPS/PPS），单 goroutine 使用。
type videoPacketizer interface {
	Payloads(frame []byte, mtu int) [][]byte
}

// videoDepacketizer 把 RTP 负载重组成访问单元（AU）。单 goroutine 使用。
type videoDepacketizer interface {
	// Push 送入一个 RTP 负载，返回本包的贡献数据与「访问单元是否在此结束」。
	// data 可能为空（H264 的 FU-A 中间分片只在末片产出完整 NAL）。
	Push(payload []byte, marker bool) (data []byte, complete bool)
	// Keyframe 判断刚结束的完整访问单元是否为关键帧（紧跟 complete=true 后调用）。
	Keyframe(au []byte) bool
	// Reset 清空内部重组状态（换流/重新开启视频时调用）。
	Reset()
}

// --- 打包 ---

// payloaderAdapter 把 pion 的 rtp.Payloader 适配成 videoPacketizer。
type payloaderAdapter struct{ p rtp.Payloader }

func (a payloaderAdapter) Payloads(frame []byte, mtu int) [][]byte {
	if len(frame) == 0 || mtu <= 0 {
		return nil
	}
	return a.p.Payload(uint16(mtu), frame)
}

// newVideoPacketizer 按协商到的编码名创建打包器。
func newVideoPacketizer(codecName string) videoPacketizer {
	switch codecName {
	case videoCodecH264:
		// DisableStapA=false：SPS/PPS 自动打成 STAP-A 随关键帧发送，
		// 不依赖 sprop-parameter-sets，收到 PLI 强制 IDR 时参数集随帧走。
		return payloaderAdapter{p: &codecs.H264Payloader{}}
	case videoCodecVP8:
		return payloaderAdapter{p: &codecs.VP8Payloader{EnablePictureID: true}}
	case videoCodecVP9:
		return payloaderAdapter{p: &codecs.VP9Payloader{}}
	case videoCodecH263:
		return h263Packetizer{}
	}
	return nil
}

// --- 解包 ---

// depacketizerAdapter 把 pion 的 rtp.Depacketizer 适配成 videoDepacketizer。
type depacketizerAdapter struct {
	d       rtp.Depacketizer
	keyFunc func(au []byte) bool
	reset   func()
}

func (a *depacketizerAdapter) Push(payload []byte, marker bool) ([]byte, bool) {
	data, err := a.d.Unmarshal(payload)
	if err != nil {
		// 单个坏包不应中断整条流：丢弃它，等待下一个完整访问单元。
		return nil, false
	}
	return data, marker
}

func (a *depacketizerAdapter) Keyframe(au []byte) bool {
	if a.keyFunc == nil {
		return false
	}
	return a.keyFunc(au)
}

func (a *depacketizerAdapter) Reset() {
	if a.reset != nil {
		a.reset()
	}
}

// vp9Packet 包一层以记录首个包的 P（帧间预测）标志：
// VP9 的关键帧判定依据在**首包的负载描述符**里，访问单元本身看不出来。
type vp9Packet struct {
	p      codecs.VP9Packet
	inAU   bool
	keyAU  bool
	broken bool
}

func (v *vp9Packet) Unmarshal(payload []byte) ([]byte, error) {
	if _, err := v.p.Unmarshal(payload); err != nil {
		v.broken = true
		return nil, err
	}
	if !v.inAU {
		v.inAU = true
		v.keyAU = !v.p.P
	}
	return v.p.Payload, nil
}

func (v *vp9Packet) keyOf(au []byte, marker bool) bool {
	if marker {
		v.inAU = false
	}
	return v.keyAU
}

// newVideoDepacketizer 按协商到的编码名创建解包器。
func newVideoDepacketizer(codecName string) videoDepacketizer {
	switch codecName {
	case videoCodecH264:
		p := &codecs.H264Packet{} // 有状态：内部 fuaBuffer 累积 FU-A 分片
		return &depacketizerAdapter{
			d:       p,
			keyFunc: h264IsKeyframe,
			reset:   func() { *p = codecs.H264Packet{} },
		}
	case videoCodecVP8:
		return &depacketizerAdapter{
			d:       &codecs.VP8Packet{},
			keyFunc: vp8IsKeyframe,
			// VP8Packet 无状态
		}
	case videoCodecVP9:
		v := &vp9Packet{}
		return &vp9DepacketizerAdapter{v: v}
	case videoCodecH263:
		return &h263Depacketizer{}
	}
	return nil
}

// vp9DepacketizerAdapter 处理 VP9 的「关键帧标志取自首包」。
type vp9DepacketizerAdapter struct{ v *vp9Packet }

func (a *vp9DepacketizerAdapter) Push(payload []byte, marker bool) ([]byte, bool) {
	data, err := a.v.Unmarshal(payload)
	if err != nil {
		return nil, false
	}
	if marker {
		a.v.inAU = false
	}
	return data, marker
}

func (a *vp9DepacketizerAdapter) Keyframe([]byte) bool { return a.v.keyAU }

func (a *vp9DepacketizerAdapter) Reset() { *a.v = vp9Packet{} }

// h264IsKeyframe 扫描 Annex-B 码流，出现 IDR（NAL 类型 5）即为关键帧。
// 比跟踪 FU 头更可靠——STAP-A 里的 SPS/PPS 不会被误判。
func h264IsKeyframe(au []byte) bool {
	for _, nalu := range splitAnnexB(au) {
		if len(nalu) > 0 && nalu[0]&0x1f == 5 {
			return true
		}
	}
	return false
}

// splitAnnexB 按起始码切分 Annex-B 码流（返回各 NALU，不含起始码）。
func splitAnnexB(b []byte) [][]byte {
	var out [][]byte
	start := -1
	i := 0
	for i+2 < len(b) {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				out = append(out, b[start:i])
			}
			i += 3
			start = i
			continue
		}
		i++
	}
	if start >= 0 && start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// vp8IsKeyframe VP8 帧数据首字节 bit0 为 0 表示关键帧（P 位取反）。
func vp8IsKeyframe(au []byte) bool {
	return len(au) > 0 && au[0]&0x01 == 0
}

// --- H263-1998（RFC 4629）---

// h263Packetizer RFC 4629 打包：16 位负载头 + 定长分片。
//
// 负载头字段（RFC 4629 §5.1）：
//
//	| RR(5) | P(1) | V(1) | PLEN(6) | PEBIT(3) |
//
// 帧首包置 P=1 并**剥掉帧数据开头的两个 0x00**（H.263 的 PSC/GOB/Slice 起始码
// 都是 00 00 8x，接收端见到 P=1 会把这两个零补回来）。V/PLEN/PEBIT 本实现不使用。
type h263Packetizer struct{}

func (h263Packetizer) Payloads(frame []byte, mtu int) [][]byte {
	if len(frame) == 0 || mtu <= h263PayloadHeaderLen {
		return nil
	}
	data := frame
	withPSC := false
	if len(data) >= 2 && data[0] == 0 && data[1] == 0 {
		data = data[2:]
		withPSC = true
	}
	chunk := mtu - h263PayloadHeaderLen
	var out [][]byte
	first := true
	for len(data) > 0 {
		n := len(data)
		if n > chunk {
			n = chunk
		}
		p := make([]byte, h263PayloadHeaderLen+n)
		if first && withPSC {
			binary.BigEndian.PutUint16(p, h263FlagP)
		}
		copy(p[h263PayloadHeaderLen:], data[:n])
		out = append(out, p)
		data = data[n:]
		first = false
	}
	return out
}

const h263PayloadHeaderLen = 2

const (
	h263FlagP    = 0x0400 // P：起始码存在（接收端补两个 0x00）
	h263FlagV    = 0x0200 // V：VRC 字段存在
	h263PLENMask = 0x01f8 // PLEN：额外图像头长度（字节）
)

// h263Depacketizer RFC 4629 解包。H.263 无长期参考帧，
// 任何以 P=1 起始的完整帧都可独立解码，故 Keyframe 恒为真。
type h263Depacketizer struct{}

func (d *h263Depacketizer) Push(payload []byte, marker bool) ([]byte, bool) {
	if len(payload) < h263PayloadHeaderLen {
		return nil, false
	}
	hdr := binary.BigEndian.Uint16(payload)
	data := payload[h263PayloadHeaderLen:]
	if hdr&h263FlagV != 0 { // VRC 字段（8 位），跳过
		if len(data) < 1 {
			return nil, false
		}
		data = data[1:]
	}
	if plen := int((hdr & h263PLENMask) >> 3); plen > 0 {
		if len(data) < plen {
			return nil, false
		}
		data = data[plen:]
	}
	if hdr&h263FlagP != 0 {
		out := make([]byte, 0, 2+len(data))
		out = append(out, 0, 0)
		out = append(out, data...)
		return out, marker
	}
	return data, marker
}

func (d *h263Depacketizer) Keyframe([]byte) bool { return true }

func (d *h263Depacketizer) Reset() {}
