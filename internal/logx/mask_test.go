package logx

import (
	"strings"
	"testing"
)

// TestMaskSecretsCrypto SDES-SRTP 的媒体密钥（a=crypto 的 inline 密钥）绝不能
// 进日志文件（REQ-MEDIA-009）——信令记录会把整份 SDP 落盘。
// 套件名与 tag 保留（排查看得出用了什么算法），密钥本身必须脱敏。
func TestMaskSecretsCrypto(t *testing.T) {
	const secret = "PS1uQCVeeCFCanVmcjkpPywjNWhcYD0mXXtxaVBR"
	in := "m=audio 5004 RTP/AVP 8 101\r\n" +
		"a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:" + secret + "\r\n" +
		"a=crypto:2 AEAD_AES_256_GCM inline:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\r\n"
	out := MaskSecrets(in)
	if strings.Contains(out, secret) {
		t.Fatalf("媒体密钥未脱敏:\n%s", out)
	}
	if strings.Contains(out, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Fatalf("第二条 crypto 的密钥未脱敏:\n%s", out)
	}
	if !strings.Contains(out, "AES_CM_128_HMAC_SHA1_80") || !strings.Contains(out, "AEAD_AES_256_GCM") {
		t.Fatalf("套件名应保留（排查要用）:\n%s", out)
	}
	if n := strings.Count(out, "***脱敏***"); n != 2 {
		t.Fatalf("应有两处脱敏，实际 %d 处:\n%s", n, out)
	}
}

// TestMaskSecretsRegressions 既有脱敏规则不受影响（认证头 / password= / URI 密码）。
func TestMaskSecretsRegressions(t *testing.T) {
	auth := MaskSecrets("Authorization: Digest username=\"2999\", response=\"abcdef\"\r\n")
	if strings.Contains(auth, "abcdef") || !strings.Contains(auth, "***脱敏***") {
		t.Fatalf("认证头未脱敏:\n%s", auth)
	}
	pw := MaskSecrets("REGISTER sip:x SIP/2.0\r\nX-Param: password=topsecret\r\n")
	if strings.Contains(pw, "topsecret") {
		t.Fatalf("password= 未脱敏:\n%s", pw)
	}
	uri := MaskSecrets("Contact: <sip:alice:pw123@10.0.0.1:5060>\r\n")
	if strings.Contains(uri, "pw123") {
		t.Fatalf("URI 中的密码未脱敏:\n%s", uri)
	}
}
