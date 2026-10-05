package audio

import (
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/media"
)

// engineSampleRate 音频设备要用的采样率：跟随引擎当前协商的编解码器
//（G.711 / Opus 8 kHz，G.722 16 kHz，见 media.Codec.SampleRate）。
// 只接受已知的两档，异常值回落到 8 kHz——宁可回到历史行为，也不要把设备
// 开到一个谁也没验证过的速率上。
func engineSampleRate(engine *media.Engine) int {
	if engine == nil {
		return 8000
	}
	switch rate := engine.SampleRate(); rate {
	case 8000, 16000:
		return rate
	default:
		return 8000
	}
}

// Backend 音频后端接口：设备枚举与全双工流管理。
type Backend interface {
	// Devices 枚举麦克风（capture）与扬声器（playback）设备。
	Devices() (mics []core.AudioDevice, speakers []core.AudioDevice)
	// Start 启动全双工音频流：
	//   - 采集：以 20ms 帧周期调用 engine.FeedMic
	//   - 播放：以 20ms 帧周期调用 engine.ReadPlayback 获取数据
	// micID / speakerID 为空表示使用系统默认设备；采样率取自 engine.SampleRate()。
	Start(micID, speakerID string, engine *media.Engine) error
	// Stop 停止音频流。
	Stop()
	// PlayTone 播放提示音：loop=true 循环播放（如振铃），否则播放一次。
	// deviceID 指定播放设备（空 = 系统默认），振铃音可选用独立扬声器。
	PlayTone(tone *wavPCM, loop bool, deviceID string) error
	// StopTone 停止提示音播放。
	StopTone()
	// Close 释放后端资源。
	Close()
}
