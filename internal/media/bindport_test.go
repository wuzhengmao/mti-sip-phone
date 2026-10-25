package media

import (
	"net"
	"testing"
)

// TestBindRTPRTCPInvariant 基本不变量 + **绑定形态断言**：
//   - RTP 为偶数、RTCP = RTP+1（RFC 3550），两端口关闭后归还（通配可再绑）；
//   - **返回的端口对必须已被"通配地址"绑定占用**——对它们再做通配绑定必须
//     失败。这是撞号防护的结构性依据：媒体与 sipgo 信令出站都是通配绑定，
//     同端口无法共存，内核在分配层就排除了"媒体吸走信令响应"的撞号
//     （见 bindRTPRTCP 头注释）。**若实现回退成"具体 IP 绑定"，通配重绑会
//     成功，本测试立刻失败**——这是它的回归强度所在。
func TestBindRTPRTCPInvariant(t *testing.T) {
	for i := 0; i < 5; i++ {
		c1, c2, err := bindRTPRTCP()
		if err != nil {
			t.Fatalf("bindRTPRTCP 失败: %v", err)
		}
		rtpPort := c1.LocalAddr().(*net.UDPAddr).Port
		rtcpPort := c2.LocalAddr().(*net.UDPAddr).Port

		if rtpPort%2 != 0 {
			t.Fatalf("RTP 端口应为偶数，实际 %d", rtpPort)
		}
		if rtcpPort != rtpPort+1 {
			t.Fatalf("RTCP 端口应为 RTP+1，实际 %d/%d", rtpPort, rtcpPort)
		}

		// 占用形态：仍被通配占着（重绑必须失败）
		for _, p := range []int{rtpPort, rtcpPort} {
			probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: p})
			if err == nil {
				_ = probe.Close()
				t.Fatalf("端口 %d 可被再次通配绑定——媒体不是通配绑定，撞号防护失效", p)
			}
		}

		_ = c1.Close()
		_ = c2.Close()

		// 归还后可再绑（说明上面失败的原因是占用而非其他）
		for _, p := range []int{rtpPort, rtcpPort} {
			probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: p})
			if err != nil {
				t.Fatalf("关闭后端口 %d 应可重新绑定: %v", p, err)
			}
			_ = probe.Close()
		}
	}
}
