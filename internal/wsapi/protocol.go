// Package wsapi 实现本地 WebSocket 控制接口：网页通过本机 WS 连接软电话，
// 控制拨号/接听/挂断，并接收振铃、挂断等事件。
//
// 协议详见 docs/ws-protocol.md。
package wsapi

import "encoding/json"

// Message WS 消息统一结构。
//   - 请求：{ "id": 1, "method": "call.dial", "params": {...} }
//   - 响应：{ "id": 1, "type": "response", "ok": true, "data": {...} }
//   - 事件：{ "type": "event", "event": "call.incoming", "data": {...} }
type Message struct {
	ID     json.RawMessage `json:"id,omitempty"`     // 请求-响应关联 ID（透传）
	Type   string          `json:"type,omitempty"`   // "response" / "event"
	Method string          `json:"method,omitempty"` // 请求方法名
	Params json.RawMessage `json:"params,omitempty"` // 请求参数
	OK     *bool           `json:"ok,omitempty"`     // 响应是否成功
	Data   any             `json:"data,omitempty"`   // 响应数据 / 事件负载
	Error  *Error          `json:"error,omitempty"`  // 失败信息
	Event  string          `json:"event,omitempty"`  // 事件名
}

// Error 错误信息。
type Error struct {
	Code    string `json:"code"`    // 机器可读错误码
	Message string `json:"message"` // 人类可读描述
}

// 错误码定义。
const (
	CodeUnknownMethod = "unknown_method" // 未知方法
	CodeInvalidParams = "invalid_params" // 参数缺失或非法
	CodeNotReady      = "not_ready"      // 电话能力尚未接入
	CodeCallNotFound  = "call_not_found" // 通话不存在
	CodeInternal      = "internal"       // 内部错误
)

// 请求方法名。
const (
	MethodDial      = "call.dial"    // 拨号
	MethodAnswer    = "call.answer"  // 接听
	MethodHangup    = "call.hangup"  // 挂断
	MethodReject    = "call.reject"  // 拒接
	MethodMute      = "call.mute"    // 静音
	MethodGetState = "system.getState" // 查询状态
)

// 事件名。
const (
	EvSystemReady     = "system.ready"     // 服务就绪（全局总线事件）
	EvSystemState     = "system.state"     // 状态快照（连接建立时推送给该连接）
	EvCallIncoming    = "call.incoming"    // 来电（振铃）
	EvCallRinging     = "call.ringing"     // 呼出振铃
	EvCallEstablished = "call.established" // 通话建立
	EvCallTerminated  = "call.terminated"  // 通话结束
	EvCallFailed      = "call.failed"      // 呼叫失败
	EvCallMuted       = "call.muted"       // 静音状态变化
	EvRegisterState   = "register.state"   // 注册状态变化
)

// newError 构造错误信息。
func newError(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// errMessage 将 Go error 映射为响应 Message。
func errMessage(id json.RawMessage, code string, err error) Message {
	msg := "未知错误"
	if err != nil {
		msg = err.Error()
	}
	ok := false
	return Message{ID: id, Type: "response", OK: &ok, Error: newError(code, msg)}
}

// okMessage 构造成功响应。
func okMessage(id json.RawMessage, data any) Message {
	ok := true
	return Message{ID: id, Type: "response", OK: &ok, Data: data}
}
