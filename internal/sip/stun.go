package sip

import (
	"fmt"
	"net"
	"time"

	"github.com/pion/stun"

	"mti-sip-phone/internal/logx"
)

// stunDiscover 通过 STUN 绑定请求探测本机公网地址（NAT 穿透，REQ-ACC-017）。
// 返回公网 IP（不含端口）；失败返回空字符串与错误。
func stunDiscover(serverAddr string) (string, error) {
	conn, err := net.Dial("udp4", serverAddr)
	if err != nil {
		return "", fmt.Errorf("连接 STUN 服务器失败: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

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
