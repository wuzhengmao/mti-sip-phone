package audio

import "mti-sip-phone/internal/core"

// Control 把 audio.Manager 适配成 core.AudioControl（网页配置页用，REQ-WS-016）。
//
// 为什么要有这一层：core 不能 import internal/audio（audio 已经 import 了
// core——Devices() 直接返回 core.AudioDevice），所以 core 侧只定义窄接口，
// 由本包的适配器实现、main 注入（app.SetAudioControl(audio.NewControl(mgr))）。
type Control struct {
	m *Manager
}

// NewControl 包装一个音频管理器。
func NewControl(m *Manager) *Control { return &Control{m: m} }

// Ready 后端是否可用。
func (c *Control) Ready() bool { return c != nil && c.m != nil && c.m.Ready() }

// Devices 枚举麦克风与扬声器。
// ⚠️ 可能阻塞（音频服务不可达时 miniaudio 会长时间等待），调用方放后台 goroutine。
func (c *Control) Devices() (mics, speakers []core.AudioDevice) {
	if c == nil || c.m == nil {
		return nil, nil
	}
	return c.m.Devices()
}

// SetMic 选择麦克风（空 = 系统默认）。
func (c *Control) SetMic(deviceID string) {
	if c != nil && c.m != nil {
		c.m.SetMic(deviceID)
	}
}

// SetSpeaker 选择扬声器（空 = 系统默认）。
func (c *Control) SetSpeaker(deviceID string) {
	if c != nil && c.m != nil {
		c.m.SetSpeaker(deviceID)
	}
}

// SetRingSpeaker 选择振铃专用扬声器（空 = 系统默认）。
func (c *Control) SetRingSpeaker(deviceID string) {
	if c != nil && c.m != nil {
		c.m.SetRingSpeaker(deviceID)
	}
}

// SetPreprocess 设置音频预处理（从下一通电话生效）。
func (c *Control) SetPreprocess(p core.AudioPreprocess) {
	if c == nil || c.m == nil {
		return
	}
	c.m.SetPreprocess(Preprocess{AEC: p.AEC, NS: p.NS, AGC: p.AGC})
}

// PreprocessSupported 本构建是否带音频预处理（缺静态库时降级为无）。
func (c *Control) PreprocessSupported() bool { return PreprocessSupported() }
