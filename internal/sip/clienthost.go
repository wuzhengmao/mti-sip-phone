package sip

import (
	"net"
	"reflect"
	"unsafe"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/logx"
)

// setClientHost 动态修改 sipgo Client 的 sent-by 主机（Via 头里写的地址，
// REQ-ACC-023：配置的"声明 IP"要立刻作用到 Via 上，不必重启）。
//
// 为什么改这里而不是重建 Client：sipgo 的 `Client.host` 是私有字段、无公开
// setter，但它在**每次构建请求时**被 `clientRequestCreateVia` 读取
// （Host: c.host），因此反射定位 + unsafe 写入即可运行中切换——与
// setClientRPort 同一手法、同一降级策略（版本不匹配时只告警，重启后仍生效）。
//
// ⚠️ 它只改**声明**：连接仍绑定本机真实网卡（transport 层只在 Via 未写主机时
// 才用连接地址回填，见 sipgo 的 overrideSentBy）。空串 = 交回 sipgo 自动填。
func setClientHost(client *sipgo.Client, host string) {
	if client == nil {
		return
	}
	v := reflect.ValueOf(client)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	f := v.FieldByName("host")
	if !f.IsValid() || f.Kind() != reflect.String || !f.CanAddr() {
		logx.Logger().Warn("sipgo 版本不匹配，Via 声明 IP 动态切换不可用（重启后生效）")
		return
	}
	old := *(*string)(unsafe.Pointer(f.UnsafeAddr()))
	if old == host {
		return
	}
	*(*string)(unsafe.Pointer(f.UnsafeAddr())) = host
	if host == "" {
		logx.Logger().Info("Via 声明地址已恢复自动（跟随本机出口地址）")
		return
	}
	logx.Logger().Info("Via 声明地址已切换", "host", host)
}

// setClientConnAddr 动态设置 sipgo Client 的出站**本地绑定地址**（`connAddr`，
// REQ-ACC-038 修订：UDP 下让出站请求复用本地 SIP 监听 socket，源端口 = 监听
// 端口，服务端 received/rport 重写后的 Contact 才指向监听端口）。
//
// host 为空/port 为 0 时清空（回退 sipgo 默认临时端口，TCP/TLS 用）。sipgo 把
// `connAddr` 在**每次构建请求时**写进 `req.Laddr`，传输层据此优先
// `GetConnection(laddr)` 复用监听 socket，故反射写入即可运行中切换——
// 与 setClientHost / setClientRPort 同一手法、同一降级策略（版本不匹配只告警）。
func setClientConnAddr(client *sipgo.Client, host string, port int) {
	if client == nil {
		return
	}
	v := reflect.ValueOf(client)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	f := v.FieldByName("connAddr")
	if !f.IsValid() || !f.CanAddr() {
		logx.Logger().Warn("sipgo 版本不匹配，出站复用监听 socket 不可用（回退临时端口）")
		return
	}
	addr := sip.Addr{Port: port, Hostname: host}
	if host != "" {
		addr.IP = net.ParseIP(host)
	}
	*(*sip.Addr)(unsafe.Pointer(f.UnsafeAddr())) = addr
	if host == "" {
		logx.Logger().Info("出站已回退临时端口（非 UDP 传输）")
		return
	}
	logx.Logger().Info("出站将复用本地 SIP 监听 socket", "host", host, "port", port)
}
