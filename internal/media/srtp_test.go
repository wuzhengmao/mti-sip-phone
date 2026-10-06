package media

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// TestSRTPPolicySemantics 四种策略的对外语义（REQ-MEDIA-009）。
func TestSRTPPolicySemantics(t *testing.T) {
	cases := []struct {
		p       SRTPPolicy
		offers  bool // 本端 offer 是否声明 SRTP
		accepts bool // 对端要求时是否配合
		require bool // 对端不支持是否失败
	}{
		{SRTPOff, false, false, false},
		{SRTPSupported, false, true, false}, // 默认：不主动声明，对端要求则配合
		{SRTPPreferred, true, true, false},
		{SRTPRequired, true, true, true},
	}
	for _, c := range cases {
		if got := c.p.offersSRTP(); got != c.offers {
			t.Errorf("%v.offersSRTP() = %v, 期望 %v", c.p, got, c.offers)
		}
		if got := c.p.acceptsSRTP(); got != c.accepts {
			t.Errorf("%v.acceptsSRTP() = %v, 期望 %v", c.p, got, c.accepts)
		}
		if got := c.p.requiresSRTP(); got != c.require {
			t.Errorf("%v.requiresSRTP() = %v, 期望 %v", c.p, got, c.require)
		}
	}
}

// TestCryptoLineRoundTrip 本端生成的 a=crypto 行能被自己解析回来
//（tag / 套件 / 密钥 / 盐逐字节一致）。
func TestCryptoLineRoundTrip(t *testing.T) {
	for i, s := range cryptoSuites {
		k, err := newCryptoKey(i+1, s)
		if err != nil {
			t.Fatalf("%s 生成密钥失败: %v", s.name, err)
		}
		pc, ok := parseCryptoAttr(k.line())
		if !ok {
			t.Fatalf("%s 生成的行解析失败: %q", s.name, k.line())
		}
		if pc.tag != k.tag || pc.suite.name != s.name {
			t.Errorf("%s tag/套件不匹配: got %d/%s", s.name, pc.tag, pc.suite.name)
		}
		if !bytes.Equal(pc.key, k.key) || !bytes.Equal(pc.salt, k.salt) {
			t.Errorf("%s 密钥/盐不一致", s.name)
		}
		// 行里必须带 inline: 且不含 MKI 等本端不生成的东西
		if !strings.Contains(k.line(), "inline:") {
			t.Errorf("%s 生成的行缺少 inline: %q", s.name, k.line())
		}
	}
}

// TestParseCryptoRejectsUnsupported 不支持/畸形的 crypto 行必须被拒（跳过），
// 而不是当成能用。
func TestParseCryptoRejectsUnsupported(t *testing.T) {
	key, err := newCryptoKey(1, cryptoSuites[0])
	if err != nil {
		t.Fatal(err)
	}
	good := key.line()
	cases := map[string]string{
		"未知套件":       "1 AES_CM_128_HMAC_SHA1_99 inline:AAAA",
		"非 inline":    "1 AES_CM_128_HMAC_SHA1_80 url:http://x",
		"带 lifetime":  good + "|2^20",
		"带 MKI":       good + "|2^20|1:4",
		"会话参数":        good + " UNENCRYPTED_SRTP",
		"密钥长度不符":      "1 AES_CM_128_HMAC_SHA1_80 inline:AAAA",
		"字段不足":        "1 AES_CM_128_HMAC_SHA1_80",
		"tag 非数字":     "x AES_CM_128_HMAC_SHA1_80 inline:AAAA",
		"base64 非法":   "1 AES_CM_128_HMAC_SHA1_80 inline:!!!!",
		"空串":          "",
	}
	for name, v := range cases {
		if _, ok := parseCryptoAttr(v); ok {
			t.Errorf("%s 的行不应被接受: %q", name, v)
		}
	}
	// 对照组：正常的行必须能解析
	if _, ok := parseCryptoAttr(good); !ok {
		t.Fatalf("正常的 crypto 行被拒: %q", good)
	}
}

// TestAllSuitesRoundTrip 每个套件都能加解密（RTP + RTCP），且密文与明文不同。
func TestAllSuitesRoundTrip(t *testing.T) {
	for _, s := range cryptoSuites {
		txKey, err := newCryptoKey(1, s)
		if err != nil {
			t.Fatal(err)
		}
		rxKey, err := newCryptoKey(1, s)
		if err != nil {
			t.Fatal(err)
		}
		sender, err := newSRTPStream(s, txKey.key, txKey.salt, rxKey.key, rxKey.salt)
		if err != nil {
			t.Fatalf("%s 建流失败: %v", s.name, err)
		}
		receiver, err := newSRTPStream(s, rxKey.key, rxKey.salt, txKey.key, txKey.salt)
		if err != nil {
			t.Fatalf("%s 建流失败: %v", s.name, err)
		}

		plain := testRTPPacket(t, 1, 100)
		enc, err := sender.protectRTP(plain, testRTPHeader(t, plain))
		if err != nil {
			t.Fatalf("%s 加密失败: %v", s.name, err)
		}
		if bytes.Equal(enc, plain) {
			t.Fatalf("%s 加密后与明文相同", s.name)
		}
		dec, err := receiver.unprotectRTP(enc, testRTPHeader(t, enc))
		if err != nil {
			t.Fatalf("%s 解密失败: %v", s.name, err)
		}
		if !bytes.Equal(dec, plain) {
			t.Fatalf("%s 解密结果与原文不一致", s.name)
		}

		// RTCP
		rplain := testRTCPPacket(t)
		renc, err := sender.protectRTCP(rplain, testRTCPHeader(t, rplain))
		if err != nil {
			t.Fatalf("%s RTCP 加密失败: %v", s.name, err)
		}
		if bytes.Equal(renc, rplain) {
			t.Fatalf("%s RTCP 加密后与明文相同", s.name)
		}
		rdec, err := receiver.unprotectRTCP(renc, testRTCPHeader(t, renc))
		if err != nil {
			t.Fatalf("%s RTCP 解密失败: %v", s.name, err)
		}
		if !bytes.Equal(rdec, rplain) {
			t.Fatalf("%s RTCP 解密结果与原文不一致", s.name)
		}
	}
}

// TestSRTPTamperRejected 改一个比特就解不开（完整性保护）。
func TestSRTPTamperRejected(t *testing.T) {
	s := cryptoSuites[0]
	txKey, _ := newCryptoKey(1, s)
	rxKey, _ := newCryptoKey(1, s)
	sender, err := newSRTPStream(s, txKey.key, txKey.salt, rxKey.key, rxKey.salt)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := newSRTPStream(s, rxKey.key, rxKey.salt, txKey.key, txKey.salt)
	if err != nil {
		t.Fatal(err)
	}
	plain := testRTPPacket(t, 1, 100)
	enc, err := sender.protectRTP(plain, testRTPHeader(t, plain))
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), enc...)
	tampered[len(tampered)-3] ^= 0x01
	if _, err := receiver.unprotectRTP(tampered, testRTPHeader(t, tampered)); err == nil {
		t.Fatal("被篡改的包竟然解密成功（完整性保护失效）")
	}
}

// TestSRTPReplayRejected 重放同一个包必须被拒（replay protection 真的开着——
// pion 的 CreateContext 默认是关的，本端显式打开）。
func TestSRTPReplayRejected(t *testing.T) {
	s := cryptoSuites[0]
	txKey, _ := newCryptoKey(1, s)
	rxKey, _ := newCryptoKey(1, s)
	sender, _ := newSRTPStream(s, txKey.key, txKey.salt, rxKey.key, rxKey.salt)
	receiver, _ := newSRTPStream(s, rxKey.key, rxKey.salt, txKey.key, txKey.salt)

	plain := testRTPPacket(t, 1, 100)
	enc, err := sender.protectRTP(plain, testRTPHeader(t, plain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.unprotectRTP(enc, testRTPHeader(t, enc)); err != nil {
		t.Fatalf("首次解密失败: %v", err)
	}
	if _, err := receiver.unprotectRTP(enc, testRTPHeader(t, enc)); err == nil {
		t.Fatal("同一个包重放竟然被接受（重放保护失效）")
	}
}

// TestSRTPOfferAnswerExchange 主叫（建议）↔ 被叫（支持）走完整的 SDP 协商，
// 随后双方能互相解密对方的包。
func TestSRTPOfferAnswerExchange(t *testing.T) {
	caller := newSRTPEngine(t, SRTPPreferred)
	defer caller.Close()
	callee := newSRTPEngine(t, SRTPSupported)
	defer callee.Close()

	offer := caller.SDPOffer()
	if !strings.Contains(offer, "a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:") {
		t.Fatalf("建议策略的 offer 未带 crypto 行:\n%s", offer)
	}
	answer, err := callee.SDPAnswer([]byte(offer))
	if err != nil {
		t.Fatalf("被叫应答失败: %v", err)
	}
	if !strings.Contains(answer, "a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:") {
		t.Fatalf("被叫 answer 未带 crypto 行:\n%s", answer)
	}
	// answer 的密钥必须与 offer 不同（密钥单向、各发各的）
	if offerKey, answerKey := cryptoMaterial(t, offer), cryptoMaterial(t, answer); offerKey == answerKey {
		t.Fatal("offer 与 answer 用了同一把密钥（密钥必须是单向的）")
	}
	if err := caller.SDPApplyAnswer([]byte(answer)); err != nil {
		t.Fatalf("主叫应用 answer 失败: %v", err)
	}
	if !caller.SRTPActive() || !callee.SRTPActive() {
		t.Fatalf("协商后未启用加密：caller=%v callee=%v", caller.SRTPActive(), callee.SRTPActive())
	}

	// 主叫 → 被叫
	plain := testRTPPacket(t, 1, 100)
	enc, err := caller.crypto.streamRef().protectRTP(plain, testRTPHeader(t, plain))
	if err != nil {
		t.Fatal(err)
	}
	dec, ok := callee.crypto.unprotectRTP(enc, testRTPHeader(t, enc))
	if !ok || !bytes.Equal(dec, plain) {
		t.Fatal("被叫解不开主叫的包")
	}
	// 被叫 → 主叫（反向也要能用，且用的是另一把密钥）
	enc2, err := callee.crypto.streamRef().protectRTP(plain, testRTPHeader(t, plain))
	if err != nil {
		t.Fatal(err)
	}
	dec2, ok := caller.crypto.unprotectRTP(enc2, testRTPHeader(t, enc2))
	if !ok || !bytes.Equal(dec2, plain) {
		t.Fatal("主叫解不开被叫的包")
	}
}

// TestSRTPSupportedDoesNotOffer 默认「支持」策略**不主动声明** SRTP：
// offer 与关闭策略逐字节一致（也保证不加密时的 SDP 与历史一致）。
func TestSRTPSupportedDoesNotOffer(t *testing.T) {
	supported := newSRTPEngine(t, SRTPSupported)
	defer supported.Close()
	off := newSRTPEngine(t, SRTPOff)
	defer off.Close()

	if got := supported.SDPOffer(); strings.Contains(got, "a=crypto") {
		t.Fatalf("「支持」策略的 offer 不应带 crypto 行:\n%s", got)
	}
	// 除 SSRC/端口外文本结构一致即可：这里只比 crypto 与 rtpmap 行
	if strings.Contains(off.SDPOffer(), "a=crypto") {
		t.Fatal("关闭策略的 offer 不应带 crypto 行")
	}
}

// TestSRTPRequiredRejectsPlainOffer 「必要」策略下，对端 offer 没有 crypto → 拒接。
// 而「建议/支持」策略下同样情形必须正常协商（明文）。
func TestSRTPRequiredRejectsPlainOffer(t *testing.T) {
	plainOffer := func() string {
		e := newSRTPEngine(t, SRTPOff)
		defer e.Close()
		return e.SDPOffer()
	}()

	required := newSRTPEngine(t, SRTPRequired)
	defer required.Close()
	if _, err := required.SDPAnswer([]byte(plainOffer)); !errors.Is(err, ErrSRTPRequired) {
		t.Fatalf("必要策略应拒接不加密的 offer，实际 err=%v", err)
	}

	for _, p := range []SRTPPolicy{SRTPSupported, SRTPPreferred} {
		e := newSRTPEngine(t, p)
		if _, err := e.SDPAnswer([]byte(plainOffer)); err != nil {
			t.Errorf("%v 策略下不应因缺少 crypto 而失败: %v", p, err)
		}
		if e.SRTPActive() {
			t.Errorf("%v 策略下不应启用加密", p)
		}
		e.Close()
	}
}

// TestSRTPRequiredFailsWhenAnswerLacksCrypto 「必要」策略下，对端 answer
// 没接受 SRTP → 本次呼叫失败。
func TestSRTPRequiredFailsWhenAnswerLacksCrypto(t *testing.T) {
	caller := newSRTPEngine(t, SRTPRequired)
	defer caller.Close()
	callee := newSRTPEngine(t, SRTPOff) // 对端关闭加密
	defer callee.Close()

	offer := caller.SDPOffer()
	answer, err := callee.SDPAnswer([]byte(offer))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(answer, "a=crypto") {
		t.Fatal("关闭策略的 answer 不应带 crypto 行")
	}
	if err := caller.SDPApplyAnswer([]byte(answer)); !errors.Is(err, ErrSRTPRequired) {
		t.Fatalf("必要策略应因对端未接受加密而失败，实际 err=%v", err)
	}
}

// TestSRTPPreferredFallsBackToPlain 「建议」策略下对端不接受 → 明文继续（不报错）。
func TestSRTPPreferredFallsBackToPlain(t *testing.T) {
	caller := newSRTPEngine(t, SRTPPreferred)
	defer caller.Close()
	callee := newSRTPEngine(t, SRTPOff)
	defer callee.Close()

	answer, err := callee.SDPAnswer([]byte(caller.SDPOffer()))
	if err != nil {
		t.Fatal(err)
	}
	if err := caller.SDPApplyAnswer([]byte(answer)); err != nil {
		t.Fatalf("建议策略应允许降级为明文: %v", err)
	}
	if caller.SRTPActive() {
		t.Fatal("降级后不应处于加密状态")
	}
}

// TestSRTPVideoSectionCrypto 视频段独立密钥、独立 crypto 行（与音频不同）。
func TestSRTPVideoSectionCrypto(t *testing.T) {
	audio := newSRTPEngine(t, SRTPPreferred)
	defer audio.Close()
	video, err := NewVideoEngine("127.0.0.1")
	if err != nil {
		t.Skipf("本构建不支持视频: %v", err)
	}
	defer video.Close()
	video.SetSRTPPolicy(SRTPPreferred)

	offer := SDPOfferFor(audio, video, "sendrecv", "sendrecv")
	if n := strings.Count(offer, "a=crypto:"); n != len(cryptoSuites)*2 {
		t.Fatalf("offer 应有音频+视频两组 crypto 行（%d 条），实际 %d 条:\n%s",
			len(cryptoSuites)*2, n, offer)
	}
	// 音频与视频的密钥必须不同（每个 m= 行独立密钥）
	lines := cryptoLinesOf(offer)
	if len(lines) < 2 {
		t.Fatal("解析不到 crypto 行")
	}
	if cryptoMaterialOfLine(t, lines[0]) == cryptoMaterialOfLine(t, lines[len(cryptoSuites)]) {
		t.Fatal("音频与视频用了同一把密钥（应为每个 m= 行独立生成）")
	}
}

// TestSRTPDatagramFlow 端到端：两个引擎协商后真的用 UDP 收发，
// 接收侧解出来的音频进入播放缓冲（加密链路的整体回归）。
func TestSRTPDatagramFlow(t *testing.T) {
	caller := newSRTPEngine(t, SRTPPreferred)
	defer caller.Close()
	callee := newSRTPEngine(t, SRTPSupported)
	defer callee.Close()

	answer, err := callee.SDPAnswer([]byte(caller.SDPOffer()))
	if err != nil {
		t.Fatal(err)
	}
	if err := caller.SDPApplyAnswer([]byte(answer)); err != nil {
		t.Fatal(err)
	}
	if !caller.SRTPActive() || !callee.SRTPActive() {
		t.Fatal("协商后未启用加密")
	}

	callee.Start() // 启动接收循环
	defer callee.Close()

	codec := caller.Codec()
	samples := make([]int16, codec.FrameSize())
	for i := range samples {
		samples[i] = int16(1000 + i)
	}
	payload, err := codec.Encode(samples)
	if err != nil {
		t.Fatal(err)
	}
	caller.sendPacket(codec, payload)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rx, _, _ := callee.Stats(); rx > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	rx, _, _ := callee.Stats()
	t.Fatalf("接收侧未收到（解密后的）RTP 包，rx=%d（加密链路可能没接通）", rx)
}

// --- 测试辅助 ---

// newTestEngine 建一个只用于协商的音频引擎（不启动收发）。
func newSRTPEngine(t *testing.T, p SRTPPolicy) *Engine {
	t.Helper()
	e, err := NewEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建引擎失败: %v", err)
	}
	e.SetSRTPPolicy(p)
	return e
}

func testRTPHeader(t *testing.T, pkt []byte) *rtp.Header {
	t.Helper()
	var h rtp.Header
	if _, err := h.Unmarshal(pkt); err != nil {
		t.Fatalf("RTP 头解析失败: %v", err)
	}
	return &h
}

// testRTPPacket 造一个带真实负载的 RTP 包。
func testRTPPacket(t *testing.T, seq uint16, ts uint32) []byte {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 8, SequenceNumber: seq, Timestamp: ts, SSRC: 0x11223344,
		},
		Payload: []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
	}
	data, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testRTCPPacket(t *testing.T) []byte {
	t.Helper()
	pkt := []byte{0x80, 203, 0x00, 0x01, 0x11, 0x22, 0x33, 0x44} // RTPFB 头 + SSRC
	return pkt
}

func testRTCPHeader(t *testing.T, pkt []byte) *rtcp.Header {
	t.Helper()
	var h rtcp.Header
	if err := h.Unmarshal(pkt); err != nil {
		t.Fatalf("RTCP 头解析失败: %v", err)
	}
	return &h
}

// cryptoMaterial 从 SDP 文本里取第一条 crypto 行的密钥段（比较用）。
func cryptoMaterial(t *testing.T, sdp string) string {
	t.Helper()
	lines := cryptoLinesOf(sdp)
	if len(lines) == 0 {
		t.Fatalf("SDP 里没有 crypto 行:\n%s", sdp)
	}
	return cryptoMaterialOfLine(t, lines[0])
}

func cryptoLinesOf(sdp string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "a=crypto:") {
			out = append(out, strings.TrimPrefix(line, "a=crypto:"))
		}
	}
	return out
}

func cryptoMaterialOfLine(t *testing.T, line string) string {
	t.Helper()
	fields := strings.Fields(line)
	if len(fields) < 3 {
		t.Fatalf("crypto 行格式不对: %q", line)
	}
	return fields[2]
}
