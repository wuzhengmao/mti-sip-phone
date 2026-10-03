package sip

import (
	"bytes"

	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/logx"
)

// tracer 将 sipgo 传输层的 SIP 报文接入日志系统（REQ-LOG-002）。
// 通过 sip.SIPDebugTracer 注册，sipgo 底层每收发一条报文都会回调。
// **信令日志的唯一出口**：业务代码里不要再散点调用 logx.TraceSIP，
// 否则同一条报文会记两遍（历史缺陷）。
type tracer struct{}

// SetTraceEnabled 开关 SIP 信令跟踪（DEBUG 级日志，敏感信息自动脱敏）。
// 可运行中动态切换；disabled 时传 nil 给 SIPDebugTracer 即不记录。
func SetTraceEnabled(enabled bool) {
	sip.SIPDebug = enabled
	if enabled {
		sip.SIPDebugTracer(&tracer{})
		logx.Logger().Info("SIP 信令跟踪已开启（DEBUG 级，敏感信息脱敏）")
	} else {
		sip.SIPDebugTracer(nil)
		logx.Logger().Debug("SIP 信令跟踪已关闭")
	}
}

func (t *tracer) SIPTraceRead(transport, laddr, raddr string, sipmsg []byte) {
	if !isSIPPayload(sipmsg) {
		return
	}
	logx.TraceSIP("收到", string(sipmsg))
}

func (t *tracer) SIPTraceWrite(transport, laddr, raddr string, sipmsg []byte) {
	if !isSIPPayload(sipmsg) {
		return
	}
	logx.TraceSIP("发出", string(sipmsg))
}

// isSIPPayload 粗判是否为 SIP 报文：传输层还会收到非 SIP 载荷，比如实测
// 服务端/NAT 每约 10 秒发一条四个 0 字节的 NAT 保活包，记进信令日志只是噪音。
func isSIPPayload(b []byte) bool {
	if bytes.HasPrefix(b, []byte("SIP/2.0")) {
		return true // 响应
	}
	i := bytes.IndexByte(b, ' ')
	if i <= 0 || i > 16 {
		return false
	}
	switch string(b[:i]) {
	case "INVITE", "ACK", "BYE", "CANCEL", "REGISTER", "OPTIONS", "INFO", "UPDATE",
		"PRACK", "SUBSCRIBE", "NOTIFY", "REFER", "MESSAGE", "PUBLISH", "SERVICE":
		return true
	}
	return false
}
