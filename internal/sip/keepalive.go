package sip

import (
	"context"
	"fmt"
	"time"

	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/logx"
)

// 连接型传输（TCP/TLS）的信令保活：RFC 5626 §3.5.1 的 CRLF keepalive。
//
// **为什么必须有**：服务端对 TCP/TLS 连接有**空闲超时**（OpenSIPS 的
// `tcp_connection_lifetime`，默认两分钟级），而我们的续注册周期是
// `0.7×expires`（默认 210 秒）——连接会在两次注册之间被服务端关掉。之后服务端
// 要把 in-dialog 请求（对端的 BYE / ACK / re-INVITE）送回来时，连接表里已经没有
// 这条连接，只能**按 Contact 地址重新拨号**；而 NAT 后面的地址外面拨不进来
// （真机日志：
//
//	ERROR:core:tcp_connect_blocking_timeout: ... [server=10.168.5.10:59436] (111) Connection refused
//	ERROR:proto_tls:proto_tls_send: connect failed
//	ERROR:tm:msg_send: send() to 10.168.5.10:59436 for proto tls/3 failed
//
// ），于是表现为"**接通后过一段时间，对端挂断本端不知道**"（刚接通时连接还热着，
// 一切正常——这也是这类问题最容易误判的地方）。
//
// 按时发 CRLF 是标准做法：服务端按 RFC 5626 会回一个 CRLF/"pong"（非 SIP 载荷，
// 传输层读钩子直接忽略），而**连接的空闲计时被这段流量刷新**，于是连接一直在，
// 服务端的 in-dialog 请求就能顺着它送回来。
const (
	connKeepaliveInterval = 30 * time.Second
	crlfKeepalive         = "\r\n\r\n"
)

// keepaliveLoop 周期性在注册用的那条连接上发 CRLF keepalive（仅 TCP/TLS）。
// 随注册上下文启停：重新注册会取消旧上下文、起新的循环。
func (p *Phone) keepaliveLoop(ctx context.Context, acc config.Account) {
	tr := config.NormalizeTransport(acc.Transport)
	if tr == config.TransportUDP {
		return // UDP 没有连接可保活（NAT 映射由注册与媒体/optchk 心跳维持）
	}
	tk := time.NewTicker(connKeepaliveInterval)
	defer tk.Stop()
	// 第一次成功发一条 info（现场验证"保活确实在跑"），之后按 debug 记
	logged := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			if err := p.sendConnKeepalive(ctx, acc, tr); err == nil && !logged {
				logged = true
				logx.Logger().Info("信令连接保活已启动（每 30 秒一个 CRLF，防止服务端空闲超时断连）",
					"transport", tr, "server", acc.Server)
			}
		}
	}
}

// sendConnKeepalive 在既有连接上写一个 CRLF；池里没有连接就跳过
// （下一次注册会建连，不需要在这里补）。
func (p *Phone) sendConnKeepalive(ctx context.Context, acc config.Account, tr string) error {
	addr, ok := serverConnKey(ctx, acc)
	if !ok {
		return fmt.Errorf("解析服务器地址失败")
	}
	conn, err := p.client.TransportLayer().GetConnection(tr, addr)
	if err != nil || conn == nil {
		return fmt.Errorf("连接池中没有到 %s 的连接", addr)
	}
	// sipgo 的 Connection 接口只有 WriteMsg（没有裸写字节），而 CRLF 不是 SIP
	// 报文；这里断言到具体类型写原始字节——TCP 与 TLS 的连接都是 *sip.TCPConnection
	//（TransportTLS 内嵌 TransportTCP，建连走同一套 newConnection）。
	c, ok := conn.(*sip.TCPConnection)
	if !ok {
		return fmt.Errorf("连接类型不支持裸写（%T）", conn)
	}
	if _, err := c.Write([]byte(crlfKeepalive)); err != nil {
		logx.Logger().Debug("信令保活失败（连接可能已被服务端关闭，下次注册会重连）",
			"remote", addr, "err", err)
		return err
	}
	logx.Logger().Debug("已发送信令保活（CRLF）", "transport", tr, "remote", addr)
	return nil
}
