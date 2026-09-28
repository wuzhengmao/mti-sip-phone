package sip

import (
	"reflect"
	"unsafe"

	"github.com/emiago/sipgo"

	"mti-sip-phone/internal/logx"
)

// setClientRPort 动态修改 sipgo Client 的 rport 开关（REQ-ACC-016：
// 无需重启即生效）。
//
// sipgo 的 Client.rport 为私有字段且无公开 setter，但该字段在每次
// 请求构建（Via 生成）时被读取，因此通过反射定位并按 unsafe 写入
// 即可实现运行中切换。sipgo 升级后若字段名变化，反射定位失败时
// 安全降级（仅记录告警，配置重启后仍会生效）。
func setClientRPort(client *sipgo.Client, enabled bool) {
	v := reflect.ValueOf(client)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	f := v.FieldByName("rport")
	if !f.IsValid() || f.Kind() != reflect.Bool || !f.CanAddr() {
		logx.Logger().Warn("sipgo 版本不匹配，rport 动态切换不可用（重启后生效）")
		return
	}
	*(*bool)(unsafe.Pointer(f.UnsafeAddr())) = enabled
	logx.Logger().Info("rport 已动态切换", "enabled", enabled)
}
