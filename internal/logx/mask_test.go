package logx

import (
	"os"
	"path/filepath"
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

// TestTraceIgnoresLogLevel 信令记录**不受日志级别影响**（用户要求：勾选了就必定记录）。
// TraceSIP 走的是独立输出目标（traceOut，见 Init），不经过 slog 的等级过滤——
// 这条用例把该行为钉住：等级设成 error，信令仍要落盘。
func TestTraceIgnoresLogLevel(t *testing.T) {
	dir, err := os.MkdirTemp("", "logx-trace-level")
	if err != nil {
		t.Fatal(err)
	}
	if err := Init("error", dir, true, false); err != nil { // 等级 = error（最严）
		t.Fatal(err)
	}
	TraceSIP("发出", "INVITE sip:2901@example.com SIP/2.0\r\nVia: SIP/2.0/TLS 10.0.0.9\r\n")

	b, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "INVITE sip:2901@example.com") {
		t.Fatalf("等级=error 时信令没有落盘（信令不该被等级过滤）:\n%s", string(b))
	}
	// 先关句柄再删目录：Windows 上文件被自己占着时删不掉（与日志轮转同一个坑）。
	Close()
	_ = os.RemoveAll(dir)
}
