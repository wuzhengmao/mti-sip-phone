//go:build noaudio

package audio

import (
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/media"
)

// nullBackend 空音频后端：无真实设备，Start 后以定时器驱动空音频流，
// 供纯 Go 构建（-tags noaudio）与无音频环境下的联调测试。
type nullBackend struct {
	stopCh chan struct{}
}

func newBackend() (Backend, error) {
	logx.Logger().Warn("音频后端为空实现（-tags noaudio）：无真实音频设备")
	return &nullBackend{stopCh: make(chan struct{})}, nil
}

func (b *nullBackend) Devices() ([]core.AudioDevice, []core.AudioDevice) {
	return []core.AudioDevice{{ID: "", Name: "默认（无音频后端）"}},
		[]core.AudioDevice{{ID: "", Name: "默认（无音频后端）"}}
}

func (b *nullBackend) Start(micID, speakerID string, engine *media.Engine) error {
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		// 帧长按**当前编解码器**取（20ms：G.711/Opus 160、G.722 320）；每轮重取
		// 一次，协商中途换到不同采样率的编解码器时也不会错位。两个缓冲分开：
		// 播放缓冲读出来的数据不能再当采集数据喂回去。
		var silence, buf []int16
		for {
			select {
			case <-b.stopCh:
				return
			case <-ticker.C:
				if n := engine.Codec().FrameSize(); len(buf) != n {
					silence = make([]int16, n) // 全零 = 静音
					buf = make([]int16, n)
				}
				engine.FeedMic(silence)
				engine.ReadPlayback(buf)
			}
		}
	}()
	return nil
}

func (b *nullBackend) Stop() {
	select {
	case <-b.stopCh:
	default:
		close(b.stopCh)
	}
	b.stopCh = make(chan struct{})
}

// PlayTone 空后端不播放提示音。
func (b *nullBackend) PlayTone(tone *wavPCM, loop bool, deviceID string) error { return nil }

// StopTone 空后端无操作。
func (b *nullBackend) StopTone() {}

func (b *nullBackend) Close() { b.Stop() }
