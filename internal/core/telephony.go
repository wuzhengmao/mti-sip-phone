package core

import (
	"context"

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
}

// AudioDevice 音频设备。
type AudioDevice struct {
	ID   string `json:"id"`   // 设备唯一标识（供 SetMicrophone/SetSpeaker 使用）
	Name string `json:"name"` // 展示名称
}

// EventSink 电话层向核心层上报事件的回调接口，由 App 实现。
type EventSink interface {
	// OnIncoming 呼入（收到 INVITE），进入振铃状态。
	OnIncoming(callID, remote, remoteName string)
	// OnRinging 呼出已振铃（收到 180/183）。
	OnRinging(callID string)
	// OnEstablished 通话建立。
	OnEstablished(callID string)
	// OnTerminated 通话结束，reason 如 remote_hangup / local_hangup / rejected。
	OnTerminated(callID, reason string)
	// OnFailed 呼叫失败，reason 如 busy / no_answer / not_found。
	OnFailed(callID, reason string)
	// OnRegisterState 注册状态变化。
	OnRegisterState(state RegisterState, reason string)
}
