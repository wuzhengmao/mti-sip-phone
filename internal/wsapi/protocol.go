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
	// CodeVideoUnsupported 视频功能不可用（本构建没有视频 / 用户关闭了
	// 「启用视频通话」总开关，REQ-VIDEO-014）。
	CodeVideoUnsupported = "video_unsupported"
	// CodeCallLogUnavailable 通话记录不可用（数据库未接入/打开失败，REQ-WS-017）。
	CodeCallLogUnavailable = "calllog_unavailable"
	// CodeAudioUnavailable 音频设备能力不可用（本构建无音频后端，REQ-WS-016）。
	CodeAudioUnavailable = "audio_unavailable"
	// CodeConfigUnavailable 该配置项依赖的外部能力未接入（重启/自启/信令开关）。
	CodeConfigUnavailable = "config_unavailable"
	// CodeUpdateUnavailable 更新能力未接入（未配置更新器）。
	CodeUpdateUnavailable = "update_unavailable"
	// CodeCTIBusy 已有 CTI 指令在途（同一时间只允许一条，REQ-ACC-026）。
	CodeCTIBusy = "cti_busy"
	// CodeCTITimeout CTI 指令超时（约 2 秒内没收到 MSP_RESULT）。
	CodeCTITimeout = "cti_timeout"
	// CodeCTIFailed CTI 平台回了失败结果（MSP_RESULT success=false，
	// message 里带平台的错误信息与错误码）。
	CodeCTIFailed = "cti_failed"
	// CodeCTINotAgent 当前不在坐席模式（没有 displayName/agentMode 的 CTI 后端），
	// 话务类指令（咨询/转移/监听…）无从发起。
	CodeCTINotAgent = "cti_not_agent"
)

// 请求方法名。
const (
	MethodDial              = "call.dial"              // 拨号
	MethodAnswer            = "call.answer"            // 接听
	MethodHangup            = "call.hangup"            // 挂断
	MethodReject            = "call.reject"            // 拒接
	MethodMute              = "call.mute"              // 保持（静音）
	MethodMuteMic           = "call.muteMic"           // 麦克风开关（纯本地，REQ-UI-023）
	MethodMuteSpeaker       = "call.muteSpeaker"       // 扬声器开关（纯本地，REQ-UI-023）
	MethodSetMicGain        = "call.setMicGain"        // 麦克风增益百分比（0–200，0 = 禁用；REQ-MEDIA-006）
	MethodSetSpeakerVolume  = "call.setSpeakerVolume"  // 扬声器音量百分比（0–100，0 = 禁用；REQ-MEDIA-006）
	MethodSetVideo          = "call.setVideo"          // 视频总开关（纯本地，REQ-VIDEO-008）
	MethodDTMF              = "call.dtmf"              // 发送 DTMF（RFC 4733，REQ-CALL-006）
	MethodGetState          = "system.getState"        // 查询状态
	MethodSetAutoAnswer     = "system.setAutoAnswer"   // 自动接听开关（桌面按钮与网页共用一份）
	MethodRegister          = "system.register"        // 立即注册并恢复自动注册（REQ-ACC-024 / REQ-WS-021）
	MethodUnregister        = "system.unregister"      // 取消注册、停止注册周期并关闭自动注册
	MethodSetUpdateConfig   = "system.setUpdateConfig" // 自动更新配置（站点/应用 ID/周期/预览版，REQ-UPD-018）
	MethodNotifyUpdate      = "system.notifyUpdate"    // 前端强制推送新版本
	MethodVideoSubscribe    = "video.subscribe"        // 订阅视频画面（REQ-VIDEO-009）
	MethodVideoUnsubscribe  = "video.unsubscribe"      // 退订视频画面
	MethodVideoSetTransform = "video.setTransform"     // 画面旋转/水平镜像（REQ-VIDEO-012）
	MethodContactExport     = "contact.export"         // 导出通信录 CSV（REQ-WS-015）
	MethodContactImport     = "contact.import"         // 导入通信录 CSV（REQ-WS-015）

	// —— 网页控制台（REQ-WEB-001 起；仅托盘无 UI 版只有网页这一个界面）——
	MethodSetVideoPreview = "call.setVideoPreview" // 本地视频回显开关（纯本地）

	MethodCallLogQuery  = "calllog.query"  // 通话记录查询（REQ-WS-017）
	MethodCallLogDelete = "calllog.delete" // 删除若干条通话记录
	MethodCallLogClear  = "calllog.clear"  // 清空通话记录

	MethodContactList        = "contact.list"       // 列出全部联系人（REQ-WS-018）
	MethodContactSave        = "contact.save"       // 新增/修改联系人
	MethodContactDelete      = "contact.delete"     // 删除联系人
	MethodContactSetStarred  = "contact.setStarred" // 批量设置/取消常用
	MethodContactClear       = "contact.clear"      // 清空通信录
	MethodContactChangedPush = "contact.changed"    // 事件：通信录变更（所有写入路径）

	MethodGetConfig                 = "system.getConfig"                 // 配置快照（脱敏，REQ-WS-019）
	MethodSetAccount                = "system.setAccount"                // 修改 SIP 账户
	MethodAudioDevices              = "audio.devices"                    // 枚举音频设备
	MethodSetAudioConfig            = "system.setAudioConfig"            // 修改音频配置（设备 + 预处理）
	MethodVideoDevices              = "video.devices"                    // 枚举摄像头
	MethodSetVideoConfig            = "system.setVideoConfig"            // 修改视频配置
	MethodSetLogConfig              = "system.setLogConfig"              // 修改日志配置
	MethodSetWSPort                 = "system.setWSPort"                 // 修改本地 WS 端口（重启生效）
	MethodSetAutoStart              = "system.setAutoStart"              // 开机自启开关
	MethodSetUnregisterOnDisconnect = "system.setUnregisterOnDisconnect" // WS 断开自动取消注册（REQ-WS-020）
	MethodRestart                   = "system.restart"                   // 重启程序（网页确认后）
	MethodCheckUpdateNow            = "system.checkUpdateNow"            // 立即检查更新（REQ-UPD-020）
	MethodUpdateStart               = "update.start"                     // 开始下载更新
	MethodUpdateCancel              = "update.cancel"                    // 取消下载
	MethodUpdateApply               = "update.apply"                     // 安装并重启
	MethodUpdateStatus              = "update.status"                    // 更新状态快照
	MethodCTIAction                 = "cti.action"                       // CTI 反向控制指令（REQ-ACC-026）
	MethodCTIListAgents             = "cti.listAgents"                   // 在线座席列表（REQ-ACC-035）
)

// 事件名。
const (
	EvSystemReady     = "system.ready"        // 服务就绪（全局总线事件）
	EvSystemState     = "system.state"        // 状态快照（连接建立时推送给该连接）
	EvCallIncoming    = "call.incoming"       // 来电（振铃）
	EvCallRinging     = "call.ringing"        // 呼出振铃
	EvCallEstablished = "call.established"    // 通话建立
	EvCallTerminated  = "call.terminated"     // 通话结束
	EvCallFailed      = "call.failed"         // 呼叫失败
	EvCallMuted       = "call.muted"          // 保持（静音）状态变化
	EvCallAudio       = "call.audio"          // 麦克风/扬声器开关变化（纯本地，REQ-UI-023）
	EvCallDialing     = "call.dialing"        // 呼出已发起（本地与 WS 拨号共用）
	EvCallVideo       = "call.video"          // 视频状态变化（协商/开关/对端分辨率，REQ-VIDEO-001）
	EvVideoWeb        = "video.web"           // 网页视频订阅状态变化（订阅后软电话隐藏视频窗口）
	EvVideoTransform  = "video.transform"     // 画面旋转/镜像变化（桌面与网页共用一份状态）
	EvRegisterState   = "register.state"      // 注册状态变化
	EvSystemUpdate    = "system.update"       // 自动更新状态变化（phase/进度/失败原因）
	EvAutoAnswer      = "system.autoAnswer"   // 自动接听开关变化（桌面与网页共用一份状态）
	EvUpdateConfig    = "system.updateConfig" // 自动更新配置变化（REQ-UPD-018，桌面与网页共用一份）
	// EvContactChanged 通信录变更（新增/修改/删除/清空/导入，REQ-WS-018）：
	// 所有写入路径（桌面界面、网页、CSV/JSON 导入）都会广播，多标签与桌面列表据此刷新。
	EvContactChanged = "contact.changed"
	// EvCallLogChanged 通话记录被删除/清空（REQ-WS-017）：新增记录由
	// call.terminated / call.failed 驱动刷新，这里只覆盖删除类变更。
	EvCallLogChanged = "calllog.changed"
	// EvSystemConfig 配置被改写（REQ-WS-019）：section 指明哪一段
	//（account/audio/video/log/ws/autoStart），values 是生效后的值。
	EvSystemConfig = "system.config"
	// EvAudioReady 音频后端就绪（REQ-WS-016）：后台初始化完成，设备列表可枚举。
	EvAudioReady = "audio.ready"
	// EvCTIAgent 坐席模式/置忙状态变化（MSP_EVENT_AGENT_ONLINE/OFFLINE，REQ-ACC-028）：
	// {online, displayName, agentMode, isBusy}。
	EvCTIAgent = "cti.agent"
	// EvCTIState 通话的话务状态变化（咨询/三方/监听/会议，REQ-ACC-026）：
	// {call}，call.cti 里是完整的 CTIState。
	EvCTIState = "cti.state"
	// EvCTIResult CTI 指令结果（MSP_RESULT）：{seq, success, code, message, action, data?}。
	// cti.action / cti.listAgents 的调用方会同时收到这条事件（同步回包之外）。
	EvCTIResult = "cti.result"
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
