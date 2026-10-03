package sip

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/pion/stun"

	"mti-sip-phone/internal/logx"
)

// stunTimeout 单次 STUN 探测的超时（含 DNS 解析）。
const stunTimeout = 3 * time.Second

// stunDiscoverCtx 通过 STUN 绑定请求探测本机公网地址（NAT 穿透，
// REQ-ACC-017；注册看护的初始探测与网络变化后重探均走这里，REQ-CALL-009）。
// 返回公网 IP（不含端口）；失败返回空字符串与错误。
//
// Dial 的 DNS 解析不受 SetDeadline 约束，网络异常时可阻塞数秒，
// 这里用 DialContext + 更早的截止时间兜住，取消/关闭时能及时返回。
func stunDiscoverCtx(ctx context.Context, serverAddr string) (string, error) {
	dialer := net.Dialer{Timeout: stunTimeout}
	conn, err := dialer.DialContext(ctx, "udp4", serverAddr)
	if err != nil {
		return "", fmt.Errorf("连接 STUN 服务器失败: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(stunTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	client, err := stun.NewClient(conn)
	if err != nil {
		return "", err
	}
	defer client.Close()

	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	var mapped stun.XORMappedAddress
	err = client.Do(msg, func(ev stun.Event) {
		if ev.Error != nil {
			err = ev.Error
			return
		}
		_ = mapped.GetFrom(ev.Message)
	})
	if err != nil {
		return "", fmt.Errorf("STUN 请求失败: %w", err)
	}
	if mapped.IP == nil {
		return "", fmt.Errorf("STUN 响应缺少映射地址")
	}
	logx.Logger().Info("STUN 探测成功", "publicIP", mapped.IP.String(), "server", serverAddr)
	return mapped.IP.String(), nil
}
