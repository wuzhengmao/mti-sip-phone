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
	CodeUnknownMethod  = "unknown_method"       // 未知方法
	CodeInvalidParams  = "invalid_params"       // 参数缺失或非法
	CodeNotReady       = "not_ready"            // 电话能力尚未接入
	CodeCallNotFound   = "call_not_found"       // 通话不存在
	CodeInternal       = "internal"             // 内部错误
	CodeUpdateNotNewer = "update_not_newer"     // 告知的版本不高于当前版本
	CodeContactsFailed = "contacts_unavailable" // 通信录不可用（数据库未接入/打开失败）
)

// 请求方法名。
const (
	MethodDial              = "call.dial"            // 拨号
	MethodAnswer            = "call.answer"          // 接听
	MethodHangup            = "call.hangup"          // 挂断
	MethodReject            = "call.reject"          // 拒接
	MethodMute              = "call.mute"            // 保持（静音）
	MethodMuteMic           = "call.muteMic"         // 麦克风开关（纯本地，REQ-UI-023）
	MethodMuteSpeaker       = "call.muteSpeaker"     // 扬声器开关（纯本地，REQ-UI-023）
	MethodSetMicGain       = "call.setMicGain"       // 麦克风增益百分比（0–200，0 = 禁用；REQ-MEDIA-006）
	MethodSetSpeakerVolume = "call.setSpeakerVolume" // 扬声器音量百分比（0–100，0 = 禁用；REQ-MEDIA-006）
	MethodSetVideo          = "call.setVideo"        // 视频总开关（纯本地，REQ-VIDEO-008）
	MethodDTMF              = "call.dtmf"            // 发送 DTMF（RFC 4733，REQ-CALL-006）
	MethodGetState          = "system.getState"          // 查询状态
	MethodSetAutoAnswer     = "system.setAutoAnswer"     // 自动接听开关（桌面按钮与网页共用一份）
	MethodSetUpdateConfig   = "system.setUpdateConfig"   // 自动更新配置（站点/应用 ID/周期/预览版，REQ-UPD-018）
	MethodNotifyUpdate      = "system.notifyUpdate"      // 前端强制推送新版本
	MethodVideoSubscribe    = "video.subscribe"      // 订阅视频画面（REQ-VIDEO-009）
	MethodVideoUnsubscribe  = "video.unsubscribe"    // 退订视频画面
	MethodVideoSetTransform = "video.setTransform"   // 画面旋转/水平镜像（REQ-VIDEO-012）
	MethodContactExport     = "contact.export"       // 导出通信录 CSV（REQ-WS-015）
	MethodContactImport     = "contact.import"       // 导入通信录 CSV（REQ-WS-015）
)

// 事件名。
const (
	EvSystemReady     = "system.ready"      // 服务就绪（全局总线事件）
	EvSystemState     = "system.state"      // 状态快照（连接建立时推送给该连接）
	EvCallIncoming    = "call.incoming"     // 来电（振铃）
	EvCallRinging     = "call.ringing"      // 呼出振铃
	EvCallEstablished = "call.established"  // 通话建立
	EvCallTerminated  = "call.terminated"   // 通话结束
	EvCallFailed      = "call.failed"       // 呼叫失败
	EvCallMuted       = "call.muted"        // 保持（静音）状态变化
	EvCallAudio       = "call.audio"        // 麦克风/扬声器开关变化（纯本地，REQ-UI-023）
	EvCallDialing     = "call.dialing"      // 呼出已发起（本地与 WS 拨号共用）
	EvCallVideo       = "call.video"        // 视频状态变化（协商/开关/对端分辨率，REQ-VIDEO-001）
	EvVideoWeb        = "video.web"         // 网页视频订阅状态变化（订阅后软电话隐藏视频窗口）
	EvVideoTransform  = "video.transform"   // 画面旋转/镜像变化（桌面与网页共用一份状态）
	EvRegisterState   = "register.state"    // 注册状态变化
	EvSystemUpdate    = "system.update"       // 自动更新状态变化（phase/进度/失败原因）
	EvAutoAnswer      = "system.autoAnswer"   // 自动接听开关变化（桌面与网页共用一份状态）
	EvUpdateConfig    = "system.updateConfig" // 自动更新配置变化（REQ-UPD-018，桌面与网页共用一份）
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
