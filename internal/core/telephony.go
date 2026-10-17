package core

import (
	"context"
	"image"

	"mti-sip-phone/internal/config"
)

// Telephony 电话能力抽象接口：注册、呼叫控制与音频设备管理。
// 由 SIP + 媒体层实现（internal/sip，后续接入）；演示模式由 FakeTelephony 实现。
type Telephony interface {
	// Register 使用给定账户发起注册（阻塞直到注册请求发出，结果经 EventSink 回调）。
	Register(ctx context.Context, acc config.Account) error
	// Unregister 注销。
	Unregister(ctx context.Context) error
	// Dial 呼出目标号码或 SIP URI，返回本次呼叫 ID。
	Dial(ctx context.Context, target string) (string, error)
	// Answer 接听指定呼叫。
	Answer(callID string) error
	// Hangup 挂断指定呼叫。
	Hangup(callID string) error
	// Reject 拒接指定呼叫，code 为 SIP 状态码（486 忙 / 603 拒绝）。
	Reject(callID string, code int) error
	// Mute 设置通话静音：muted=true 停止发送本地音频，保持接收。
	Mute(callID string, muted bool) error
	// MuteMic 麦克风静音：muted=true 停止发送本地音频（纯本地，不发 SIP 信令）。
	MuteMic(callID string, muted bool) error
	// MuteSpeaker 扬声器静音：muted=true 停止本地播放（继续接收）。
	MuteSpeaker(callID string, muted bool) error
	// SetMicGain 麦克风增益（REQ-MEDIA-006）：1 = 原始电平，0–2（界面显示 0–200%）。
	// 纯本地，不发 SIP 信令。
	SetMicGain(callID string, gain float32) error
	// SetSpeakerVolume 扬声器音量（REQ-MEDIA-006）：1 = 原始电平，0–1
	//（界面显示 0–100%）。纯本地，不发 SIP 信令。
	SetSpeakerVolume(callID string, vol float32) error
	// SendDTMF 在通话中发送一个 DTMF 音（RFC 2833）。
	SendDTMF(callID, digit string) error
	// SetAutoAnswer 开关自动接听：开启后来电自动接听。
	SetAutoAnswer(enabled bool) error
	// SetRPort 动态切换 rport（运行中立即生效）。
	SetRPort(enabled bool) error
	// SetMicrophone 选择麦克风输入设备。
	SetMicrophone(deviceID string) error
	// SetSpeaker 选择扬声器输出设备。
	SetSpeaker(deviceID string) error
	// AudioDevices 返回麦克风与扬声器设备列表。
	AudioDevices() (mics []AudioDevice, speakers []AudioDevice)
	// Close 释放资源。
	Close() error

	// ---- 视频（REQ-VIDEO-*）----
	// 未启用视频的构建里这些方法仍然存在，但会返回 ErrVideoUnsupported。

	// VideoSupported 本构建/平台**是否具备**视频能力（编译期决定，不含用户的
	// 「启用视频通话」开关）。判断"视频功能能不能用"请用 VideoUsable。
	VideoSupported() bool
	// VideoCallEnabled 用户是否启用了视频通话（配置总开关，默认启用，REQ-VIDEO-014）。
	VideoCallEnabled() bool
	// SetVideoCallEnabled 开/关视频通话总开关（立即生效，持久化由调用方做）。
	SetVideoCallEnabled(on bool) error
	// DialVideo 发起视频呼叫（offer 同时含 m=audio 与 m=video）。
	// 视频不可用（本构建不支持 / 总开关关闭）时返回 ErrVideoUnsupported / ErrVideoDisabled。
	DialVideo(ctx context.Context, target string) (string, error)
	// AnswerVideo 视频接听（打开摄像头，双向视频）。
	AnswerVideo(callID string) error
	// IncomingHasVideo 该来电的 offer 是否含可用视频段（决定是否显示「视频接听」）。
	IncomingHasVideo(callID string) bool
	// VideoActive 该通话的视频是否已协商成功且处于开启状态。
	VideoActive(callID string) bool
	// SetVideoEnabled 需求 8：关闭/恢复视频（不显示远端、不推本地、关闭视频窗口）。
	// 纯本地动作，不发 SIP 信令。
	SetVideoEnabled(callID string, on bool) error
	// SetVideoPreview 需求 7：本地视频回显与推送开关。
	SetVideoPreview(callID string, on bool) error
	// SetCamera 选择摄像头（空 = 系统默认，config.CameraDisabled = 禁用）。
	SetCamera(deviceID string) error
	// VideoDevices 枚举摄像头（可能耗时，调用方放后台 goroutine）。
	VideoDevices() []VideoDevice
	// CameraSupported 本构建/平台是否有摄像头采集（darwin 这类没有采集后端的
	// 目标：编解码可用但不采集，界面据此把摄像头选择置灰、把视频呼叫降级为只收不发）。
	CameraSupported() bool
}

// AudioDevice 音频设备。
type AudioDevice struct {
	ID   string `json:"id"`   // 设备唯一标识（供 SetMicrophone/SetSpeaker 使用）
	Name string `json:"name"` // 展示名称
}

// VideoDevice 摄像头设备。
// 注意 ID 在 Windows 侧每次启动都可能变化，展示名称才是稳定的持久化标识。
type VideoDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// VideoState 一路通话的视频状态快照（低频，随事件推送）。
type VideoState struct {
	Negotiated bool `json:"negotiated"` // 视频已协商成功
	Enabled    bool `json:"enabled"`    // 需求 8 的总开关
	PreviewOn  bool `json:"previewOn"`  // 需求 7 的本地回显开关
	Sending    bool `json:"sending"`    // 正在推送本地视频
	Width      int  `json:"width"`      // 对端视频分辨率（未收到帧时为 0）
	Height     int  `json:"height"`
}

// VideoSink 视频帧出口，由界面层实现并注册。
//
// **不走事件总线**：总线是容量 64 的 channel、满了直接丢事件，
// 一帧 640×480 RGBA 就有 1.2MB，走总线会瞬间打满并把控制事件挤掉。
//
// ⚠️ 回调**在媒体层 goroutine 上同步调用**，实现必须立即返回：
// 禁止 fyne.DoAndWait 之类的阻塞调用，否则解码循环会卡住、UDP 接收缓冲
// 溢出，之后持续花屏（用 fyne.Do 入队即可）。
type VideoSink interface {
	OnVideoFrame(callID string, local bool, img *image.RGBA)
}

// RemoteCommand 服务端 NOTIFY 控制指令（REQ-ACC-025，协议见
// docs/cti-sip-phone-protocol.md）。Kind 是事件名去掉 MSP_ 前缀的小写形式：
//
//	"dial"       MSP_DIAL         DestNo / WithVideo
//	"answer"     MSP_ANSWER       WithVideo
//	"hangup"     MSP_HANGUP       （无参数）
//	"autoanswer" MSP_SET_AUTOANSWER AutoAnswer
//	"unregister" MSP_UNREGISTER   Reason（Agent 为兼容可选的附加字段）
type RemoteCommand struct {
	Kind       string
	DestNo     string // dial：被叫号码
	WithVideo  bool   // dial / answer：是否要视频（不可用时静默降级为纯音频）
	AutoAnswer *bool  // autoanswer：nil = 未指定（忽略）
	Reason     string // unregister：注销原因（记日志）
	Agent      string // unregister：可选的坐席号（协议未定义，兼容记录）
}

// EventSink 电话层向核心层上报事件的回调接口，由 App 实现。
type EventSink interface {
	// OnIncoming 呼入（收到 INVITE），进入振铃状态。
	// hasVideo 表示对端 offer 里含可用的视频段（决定是否显示「视频接听」按钮）。
	OnIncoming(callID, remote, remoteName string, hasVideo bool)
	// OnRinging 呼出已振铃（收到 180/183）。
	// earlyMedia 表示本次振铃**带着早期媒体**（183 + SDP，回铃音由对端下发）——
	// 界面据此决定要不要放本地回铃音（有早期媒体就不放，免得两种铃音重叠加）。
	OnRinging(callID string, earlyMedia bool)
	// OnEstablished 通话建立。
	OnEstablished(callID string)
	// OnTerminated 通话结束，reason 如 remote_hangup / local_hangup / rejected。
	OnTerminated(callID, reason string)
	// OnFailed 呼叫失败，reason 如 busy / no_answer / not_found。
	OnFailed(callID, reason string)
	// OnRegisterState 注册状态变化。
	OnRegisterState(state RegisterState, reason string)
	// OnRemoteCommand 服务端通过 NOTIFY 下发控制指令（REQ-ACC-025，协议见
	// docs/cti-sip-phone-protocol.md）：拨号/接听/挂断/设置自动接听/注销。
	// 由 sip 层的 NOTIFY handler 在独立 goroutine 里调用（不阻塞信令事务）。
	OnRemoteCommand(cmd RemoteCommand)
	// OnVideoState 视频状态变化（协商成功、开关切换、拿到对端分辨率）。
	// 低频事件，走事件总线没问题；**视频帧不走这里**（见 VideoSink）。
	OnVideoState(callID string, st VideoState)
}
