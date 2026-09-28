package sip

import (
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/logx"
)

// tracer 将 sipgo 传输层的 SIP 报文接入日志系统（REQ-LOG-002）。
// 通过 sip.SIPDebugTracer 注册，sipgo 底层每收发一条报文都会回调。
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
	logx.TraceSIP("收到", string(sipmsg))
}

func (t *tracer) SIPTraceWrite(transport, laddr, raddr string, sipmsg []byte) {
	logx.TraceSIP("发出", string(sipmsg))
}
