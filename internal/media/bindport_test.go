package media

import (
	"net"
	"testing"
)

// TestWildcardPortFree 通配端口空闲判定：
// 被通配 socket 占用的端口必须判为"不空闲"（这正是撞号防护的依据——
// 媒体不能选"已被信令出站 socket 以 0.0.0.0 占用"的端口）。
func TestWildcardPortFree(t *testing.T) {
	// 占一个通配端口
	held, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("绑定通配端口失败: %v", err)
	}
	defer held.Close()
	port := held.LocalAddr().(*net.UDPAddr).Port

	if wildcardPortFree(port) {
		t.Fatalf("被通配占用的端口 %d 应判为不空闲", port)
	}

	// 释放后应判为空闲
	_ = held.Close()
	if !wildcardPortFree(port) {
		t.Fatalf("释放后的端口 %d 应判为空闲", port)
	}
}

// TestBindRTPRTCPInvariant bindRTPRTCP 的基本不变量与撞号防护的返回形态：
// RTP 为偶数、RTCP = RTP+1，且**返回的端口对在通配地址上均为空闲**
// （防护保证"选中时通配未被别的 socket 占用"——否则对端回给出站信令
// socket 的响应会被媒体 socket 吸走，见 bindRTPRTCP 的说明）。
func TestBindRTPRTCPInvariant(t *testing.T) {
	for i := 0; i < 5; i++ {
		c1, c2, err := bindRTPRTCP("127.0.0.1")
		if err != nil {
			t.Fatalf("bindRTPRTCP 失败: %v", err)
		}
		rtpPort := c1.LocalAddr().(*net.UDPAddr).Port
		rtcpPort := c2.LocalAddr().(*net.UDPAddr).Port
		_ = c1.Close()
		_ = c2.Close()

		if rtpPort%2 != 0 {
			t.Fatalf("RTP 端口应为偶数，实际 %d", rtpPort)
		}
		if rtcpPort != rtpPort+1 {
			t.Fatalf("RTCP 端口应为 RTP+1，实际 %d/%d", rtpPort, rtcpPort)
		}
		// 关闭后两端口在通配下都应可用（选端口时也经过同一校验）
		if !wildcardPortFree(rtpPort) || !wildcardPortFree(rtcpPort) {
			t.Fatalf("返回的端口对（%d/%d）在通配地址上不空闲", rtpPort, rtcpPort)
		}
	}
}
