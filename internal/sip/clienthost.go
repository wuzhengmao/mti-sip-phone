package sip

import (
	"reflect"
	"unsafe"

	"github.com/emiago/sipgo"

	"mti-sip-phone/internal/logx"
)

// setClientHost 动态修改 sipgo Client 的 sent-by 主机（Via 头里写的地址，
// REQ-ACC-023：配置的"声明 IP"要立刻作用到 Via 上，不必重启）。
//
// 为什么改这里而不是重建 Client：sipgo 的 `Client.host` 是私有字段、无公开
// setter，但它在**每次构建请求时**被 `clientRequestCreateVia` 读取
//（Host: c.host），因此反射定位 + unsafe 写入即可运行中切换——与
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
