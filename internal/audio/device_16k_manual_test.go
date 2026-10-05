package audio

import (
	"os"
	"testing"
	"time"

	"mti-sip-phone/internal/media"
)

// TestManualDeviceAt16k 手动验证用（默认跳过）：用真实音频后端按 16 kHz 开流，
// 确认设备/后端/提示音链路在 16 kHz 下能正常工作（REQ-MEDIA-011）。
//
// 运行（需要 CGO 与真实音频设备）：SIP_PHONE_MANUAL_16K=1 //   go test -run TestManualDeviceAt16k -v ./internal/audio/
// 观察日志里的「音频流已启动 … 采样率 16000」与后端打印的内部采样率。
func TestManualDeviceAt16k(t *testing.T) {
	if os.Getenv("SIP_PHONE_MANUAL_16K") == "" {
		t.Skip("手动测试：设 SIP_PHONE_MANUAL_16K=1 运行")
	}
	e, err := media.NewEngine("127.0.0.1")
	if err != nil {
		t.Fatalf("创建引擎失败: %v", err)
	}
	defer e.Close()
	// 用一份只提供 G.722 的对端 offer 让引擎协商到 16 kHz
	offer := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 5000 RTP/AVP 9\r\na=rtpmap:9 G722/8000\r\na=sendrecv\r\n"
	if _, err := e.SDPAnswer([]byte(offer)); err != nil {
		t.Fatalf("协商失败: %v", err)
	}
	if e.SampleRate() != 16000 {
		t.Fatalf("采样率应为 16000，实际 %d", e.SampleRate())
	}

	m := NewManager()
	defer m.Close()
	m.AttachEngine(e)
	time.Sleep(3 * time.Second) // 等设备打开（日志里看结果）
	m.DetachEngine()
	time.Sleep(500 * time.Millisecond)
	t.Log("已按 16 kHz 打开音频流，见日志「音频流已启动 … 采样率 16000」")
}
