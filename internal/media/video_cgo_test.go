//go:build video && cgo

package media

import (
	"image"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"

	"mti-sip-phone/internal/video"
)

// TestVideoCodecRTPRoundTrip 真实码流的端到端往返：
// 编码 → RTP 打包 → 解包重组 → 解码。
//
// 与 video_test.go 里用合成数据的那组测试互补：合成数据能覆盖分片/重组
// 逻辑，但 VP9 的打包器会用 VP9 帧头解析器判断是否关键帧（决定负载描述符
// 长度），只有真实码流才解析得过——所以这一组必须带 -tags video 跑。
func TestVideoCodecRTPRoundTrip(t *testing.T) {
	if !video.Supported() {
		t.Skip("未启用视频的构建")
	}
	for _, name := range []string{
		video.CodecH264, video.CodecVP8, video.CodecVP9, video.CodecH263,
	} {
		t.Run(name, func(t *testing.T) {
			enc, err := video.NewEncoder(name, 176, 144, 25, 300000)
			if err != nil {
				t.Fatalf("创建编码器失败: %v", err)
			}
			defer enc.Close()

			// 造一帧有明显内容的图像（纯色图对全零码流不友好）
			src := video.NewYUV420P(176, 144)
			for y := 0; y < src.H; y++ {
				for x := 0; x < src.W; x++ {
					src.Y[y*src.StrideY+x] = byte((x + y) % 256)
				}
			}
			for i := range src.U {
				src.U[i] = byte(i % 200)
				src.V[i] = byte((i * 3) % 200)
			}

			data, key, err := enc.Encode(src, true)
			if err != nil || len(data) == 0 {
				t.Fatalf("编码失败: err=%v len=%d", err, len(data))
			}
			if !key {
				t.Log("编码器未把首帧标为关键帧（部分编码器首帧即为 IDR，此处不强制）")
			}

			pack := newVideoPacketizer(name)
			depack := newVideoDepacketizer(name)
			if pack == nil || depack == nil {
				t.Fatalf("%s 缺少打包/解包实现", name)
			}
			payloads := pack.Payloads(data, videoMTU)
			if len(payloads) == 0 {
				t.Fatalf("%s 打包结果为空", name)
			}

			var au []byte
			var ended bool
			var gotKey bool
			for i, p := range payloads {
				if len(p) > videoMTU {
					t.Fatalf("%s 第 %d 个负载超出 MTU：%d", name, i, len(p))
				}
				d, complete := depack.Push(p, i == len(payloads)-1)
				au = append(au, d...)
				if complete {
					ended = true
					gotKey = depack.Keyframe(au)
				}
			}
			if !ended || len(au) == 0 {
				t.Fatalf("%s 解包未产出完整访问单元（ended=%v len=%d）", name, ended, len(au))
			}
			if !gotKey {
				t.Errorf("%s 关键帧判定失败——引擎在等关键帧时会一直丢帧、画面黑屏", name)
			}

			dec, err := video.NewDecoder(name)
			if err != nil {
				t.Fatalf("创建解码器失败: %v", err)
			}
			defer dec.Close()
			img, err := dec.Decode(au)
			if err != nil {
				t.Fatalf("解码失败: %v", err)
			}
			if img == nil {
				t.Fatal("解码无输出帧")
			}
			if img.W != 176 || img.H != 144 {
				t.Fatalf("解码尺寸错误：%dx%d", img.W, img.H)
			}
			if len(img.Pix) != img.Stride*img.H {
				t.Fatalf("像素缓冲长度不符：%d != %d", len(img.Pix), img.Stride*img.H)
			}
		})
	}
}

// TestVideoScaleAndConvert swscale 缩放与 YUV→RGBA 转换。
func TestVideoScaleAndConvert(t *testing.T) {
	if !video.Supported() {
		t.Skip("未启用视频的构建")
	}
	src := video.NewYUV420P(320, 240)
	for i := range src.Y {
		src.Y[i] = byte(i % 256)
	}
	dst, err := video.ScaleYUV(src, 176, 144)
	if err != nil {
		t.Fatalf("缩放失败: %v", err)
	}
	if dst.W != 176 || dst.H != 144 || len(dst.Y) != 176*144 {
		t.Fatalf("缩放结果尺寸错误：%dx%d len=%d", dst.W, dst.H, len(dst.Y))
	}
	img, err := video.ToRGBA(dst)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if img.Stride != 176*4 || len(img.Pix) != 176*4*144 {
		t.Fatalf("RGBA 缓冲尺寸错误：stride=%d len=%d", img.Stride, len(img.Pix))
	}
}

// TestVideoAnswerUsesPeerPT answer 的视频段必须复用**对端 offer 的 PT 号**。
//
// 回归测试：此前 answer 拿对端的 PT（如 100）去查本端编码表（96–99），
// 查不到就返回 nil、上层输出 m=video 0——对端于是认为我们拒绝了视频，
// 表现为「两边都看不到画面」。这条路径只有带 -tags video 才走得到
//（未启用视频的构建根本不宣告 m=video），所以放在本文件。
func TestVideoAnswerUsesPeerPT(t *testing.T) {
	if !video.Supported() {
		t.Skip("未启用视频的构建")
	}
	e := newTestEngine(t)
	e.SetSDPIP("10.0.0.5")
	e.ssrc = 11
	e.localPort = 42000

	// 对端用非本端的动态 PT 号（100=H264、101=VP8）
	body := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 192.168.1.9\r\n" +
		"s=-\r\n" +
		"c=IN IP4 192.168.1.9\r\n" +
		"t=0 0\r\n" +
		"m=audio 50002 RTP/AVP 8\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"m=video 50000 RTP/AVP 100 101\r\n" +
		"a=rtpmap:100 H264/90000\r\n" +
		"a=fmtp:100 profile-level-id=42e01f;packetization-mode=1\r\n" +
		"a=rtpmap:101 VP8/90000\r\n" +
		"a=sendrecv\r\n")
	offer, err := ParseSDP(body)
	if err != nil {
		t.Fatalf("解析 offer 失败: %v", err)
	}
	vinfo, err := offer.Media("video")
	if err != nil {
		t.Fatalf("取视频段失败: %v", err)
	}

	v, err := NewVideoEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建视频引擎失败: %v", err)
	}
	defer v.Close()
	if !v.ApplyOffer(vinfo) {
		t.Fatal("视频协商应成功")
	}

	ans := SDPAnswerFor(e, v, offer, 8, "sendrecv")
	if !strings.Contains(ans, "m=video "+strconv.Itoa(v.LocalPort())+" RTP/AVP 100") {
		t.Fatalf("answer 的视频段应复用例对端 PT 100：\n%s", ans)
	}
	if !strings.Contains(ans, "a=rtpmap:100 H264/90000") {
		t.Fatalf("answer 缺少 H264 的 rtpmap：\n%s", ans)
	}
	if strings.Contains(ans, "m=video 0 ") {
		t.Fatalf("answer 不应把视频置 0（等于拒绝视频）：\n%s", ans)
	}
}

// TestVideoAnswerH264SingleNALMode 对端 offer 只给 packetization-mode=0 时，
// answer 必须照收视频，并回**对端声明的模式**（answer 只能是 offer 的子集）。
//
// 回归：实测 FreeSWITCH 呼入视频电话的 offer 就是这个形态
//（`a=fmtp:102 profile-level-id=42000a; packetization-mode=0`，PT 102），
// 早期协商直接跳过 H264 → 200 OK 回 `m=video 0` → 接听后两边都没画面。
func TestVideoAnswerH264SingleNALMode(t *testing.T) {
	if !video.Supported() {
		t.Skip("未启用视频的构建")
	}
	e := newTestEngine(t)
	e.SetSDPIP("10.0.0.5")
	e.ssrc = 12
	e.localPort = 43000

	body := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 10.168.4.118\r\n" +
		"s=-\r\n" +
		"c=IN IP4 10.168.4.118\r\n" +
		"t=0 0\r\n" +
		"m=audio 22150 RTP/AVP 8\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"m=video 19120 RTP/AVP 102\r\n" +
		"a=rtpmap:102 H264/90000\r\n" +
		"a=fmtp:102 profile-level-id=42000a; packetization-mode=0\r\n" +
		"a=rtcp-fb:102 nack pli\r\n" +
		"a=sendrecv\r\n")
	offer, err := ParseSDP(body)
	if err != nil {
		t.Fatalf("解析 offer 失败: %v", err)
	}
	vinfo, err := offer.Media("video")
	if err != nil {
		t.Fatalf("取视频段失败: %v", err)
	}

	v, err := NewVideoEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建视频引擎失败: %v", err)
	}
	defer v.Close()
	if !v.ApplyOffer(vinfo) {
		t.Fatal("packetization-mode=0 时视频协商仍应成功")
	}

	ans := SDPAnswerFor(e, v, offer, 8, "sendrecv")
	if strings.Contains(ans, "m=video 0 ") {
		t.Fatalf("answer 不应把视频置 0（等于拒绝视频）：\n%s", ans)
	}
	if !strings.Contains(ans, "m=video "+strconv.Itoa(v.LocalPort())+" RTP/AVP 102") {
		t.Fatalf("answer 的视频段应复用对端 PT 102：\n%s", ans)
	}
	if !strings.Contains(ans, "a=fmtp:102 profile-level-id=42e01f;packetization-mode=0;level-asymmetry-allowed=1") {
		t.Fatalf("answer 的 fmtp 应保留本端 profile、回对端的 packetization-mode=0：\n%s", ans)
	}
}

// TestEncoderRejectsSmallerFrame 编码器必须拒绝比自身要求更小的输入帧。
//
// 回归测试：摄像头驱动可能不按请求分辨率出图（Windows DirectShow 挑最接近的
// 一档），若编码器按 640×480 建、帧按 352×288 喂，OpenH264/libvpx 会按
// 640×480 去读一块更小的缓冲 —— **越界读，进程直接崩溃**。
// 这是「视频接听后有时崩溃」的根因，C 侧已加硬校验，这里锁住该行为。
func TestEncoderRejectsSmallerFrame(t *testing.T) {
	if !video.Supported() {
		t.Skip("未启用视频的构建")
	}
	enc, err := video.NewEncoder(video.CodecH264, 640, 480, 25, 500000)
	if err != nil {
		t.Fatalf("创建编码器失败: %v", err)
	}
	defer enc.Close()

	small := video.NewYUV420P(176, 144)
	if _, _, err := enc.Encode(small, false); err == nil {
		t.Fatal("编码器应拒绝比自身要求更小的帧，否则会越界读导致崩溃")
	}

	// 同尺寸应当正常编出码流
	ok := video.NewYUV420P(640, 480)
	data, _, err := enc.Encode(ok, true)
	if err != nil {
		t.Fatalf("同尺寸编码失败: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("同尺寸编码结果为空")
	}
}

// TestVideoEngineReceivePath RTP → 重组 → 关键帧门控 → 解码 → RGBA → 出口。
//
// 绕过网络直接喂 RTP 包给引擎，把接收侧除 UDP 之外的每一段都走一遍。
// 之前「画面全黑」有两类根因（出口为 nil、answer 用错 PT），这条测试
// 锁住前者在媒体侧的半边：只要协商成功且收到可解的关键帧，出口就必须有帧。
func TestVideoEngineReceivePath(t *testing.T) {
	if !video.Supported() {
		t.Skip("未启用视频的构建")
	}
	// 1) 用真实编码器造一段 H264 码流（合成数据解不出来）
	enc, err := video.NewEncoder(video.CodecH264, 176, 144, 25, 300000)
	if err != nil {
		t.Fatalf("创建编码器失败: %v", err)
	}
	src := video.NewYUV420P(176, 144)
	for i := range src.Y {
		src.Y[i] = byte(i % 256)
	}
	data, key, err := enc.Encode(src, true)
	enc.Close()
	if err != nil || len(data) == 0 {
		t.Fatalf("编码失败: err=%v len=%d", err, len(data))
	}
	if !key {
		t.Fatal("首帧应为关键帧")
	}

	// 2) 建引擎、协商（对端用 PT 96 提供 H264）
	v, err := NewVideoEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建视频引擎失败: %v", err)
	}
	defer v.Close()

	offer := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"t=0 0\r\n" +
		"m=video 50000 RTP/AVP 96\r\n" +
		"a=rtpmap:96 H264/90000\r\n" +
		"a=fmtp:96 profile-level-id=42e01f;packetization-mode=1\r\n" +
		"a=sendrecv\r\n")
	info, err := ParseMedia(offer, "video")
	if err != nil {
		t.Fatalf("解析 offer 失败: %v", err)
	}
	if !v.ApplyOffer(info) {
		t.Fatal("视频协商应成功")
	}

	got := make(chan *image.RGBA, 8)
	v.SetFrameSink(func(local bool, img *image.RGBA) {
		if local || img == nil {
			return // 本用例只关心远端
		}
		select {
		case got <- img:
		default:
		}
	})
	v.Start()

	// 3) 打包成 RTP 并直接投喂（包序号连续、最后一包置 marker）
	pay := newVideoPacketizer(video.CodecH264)
	payloads := pay.Payloads(data, videoMTU)
	if len(payloads) == 0 {
		t.Fatal("打包结果为空")
	}
	pt := v.payloadType()
	ssrc := uint32(0x1234)
	for i, p := range payloads {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         i == len(payloads)-1,
				PayloadType:    pt,
				SequenceNumber: uint16(i),
				Timestamp:      9000,
				SSRC:           ssrc,
			},
			Payload: p,
		}
		v.handleRTP(pkt)
	}

	// 4) 解码是异步的，等一会儿
	select {
	case img := <-got:
		if img.Rect.Dx() != 176 || img.Rect.Dy() != 144 {
			t.Fatalf("解码尺寸错误：%v", img.Rect)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("接收链路未能产出画面帧（出口未收到远端帧）")
	}
}

// TestPreviewToggleDoesNotStopSending 关本地回显**不得**影响推送到远端。
//
// 用户明确要求："关闭回显只是关闭本地的回显，并不影响到本地视频推送到远端"。
// 因此回显开关（previewOn）只门控"是否把本地帧投递给界面"，与采集/编码/发送
// （sendEnabled + camConfigured）完全解耦。
func TestPreviewToggleDoesNotStopSending(t *testing.T) {
	if !video.Supported() {
		t.Skip("未启用视频的构建")
	}
	v, err := NewVideoEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建视频引擎失败: %v", err)
	}
	defer v.Close()

	offer := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"t=0 0\r\n" +
		"m=video 50000 RTP/AVP 96\r\n" +
		"a=rtpmap:96 H264/90000\r\n" +
		"a=fmtp:96 packetization-mode=1\r\n" +
		"a=sendrecv\r\n")
	info, err := ParseMedia(offer, "video")
	if err != nil {
		t.Fatalf("解析 offer 失败: %v", err)
	}
	if !v.ApplyOffer(info) {
		t.Fatal("视频协商应成功")
	}
	v.SetSendEnabled(true)
	// 直接置位采集参数标记：本用例只验证开关语义，不去真的打开摄像头
	//（测试机可能没有摄像头，也不该被测试占用）
	v.camMu.Lock()
	v.camConfigured = true
	v.camMu.Unlock()

	if !v.IsSending() {
		t.Fatal("SetSendEnabled(true) 后 IsSending 应为真")
	}

	v.SetVideoPreview(false)
	if v.IsPreviewOn() {
		t.Fatal("回显开关应已关闭")
	}
	if !v.IsSending() {
		t.Fatal("关本地回显不得停止推送本地视频（回显与推送必须解耦）")
	}

	v.SetVideoPreview(true)
	if !v.IsPreviewOn() || !v.IsSending() {
		t.Fatal("重新打开回显后状态应恢复")
	}
}
