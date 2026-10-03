//go:build !noaudio

package audio

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/gen2brain/malgo"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/media"
)

// toneIdleClose 提示音播完后，多久没有新音就关掉提示音设备。
// 不立刻关是为了让连续按键（DTMF 回显）复用同一个设备——开一次设备要几十毫秒，
// 拨号时每次按键都重开会有明显滞后感；也不能一直开着，否则静音流长期占着扬声器。
const toneIdleClose = 1500 * time.Millisecond

// malgoBackend 基于 miniaudio（malgo）的音频后端：
// 全双工采集/播放、设备枚举、设备选择、提示音播放。需要 CGO。
type malgoBackend struct {
	ctx      *malgo.AllocatedContext
	device   *malgo.Device
	toneDev  *malgo.Device // 提示音播放设备
	toneMu   sync.Mutex
	tonePCM  []int16
	toneCh   int
	tonePos  int
	toneLoop bool
	// 设备复用与空闲关闭用（都只在 toneMu 下读写）：
	toneRate  int         // 当前提示音设备的采样率（0 = 无设备）
	toneDevID string      // 当前提示音设备的设备 ID（管理器传的原始字符串）
	toneGen   uint64      // 代次：每次换音 +1，用于作废过期的空闲关闭定时器
	toneIdle  *time.Timer // 空闲关闭定时器
}

func newBackend() (Backend, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("初始化音频上下文失败: %w", err)
	}
	logx.Logger().Info("音频后端已初始化（miniaudio）")
	return &malgoBackend{ctx: ctx}, nil
}

// Devices 枚举麦克风与扬声器。
func (b *malgoBackend) Devices() ([]core.AudioDevice, []core.AudioDevice) {
	captures, err := b.ctx.Devices(malgo.Capture)
	if err != nil {
		logx.Logger().Debug("枚举麦克风失败", "err", err)
	}
	playbacks, err := b.ctx.Devices(malgo.Playback)
	if err != nil {
		logx.Logger().Debug("枚举扬声器失败", "err", err)
	}

	// 系统默认设备放在首位（ID 为空）
	mics := []core.AudioDevice{{ID: "", Name: "系统默认麦克风"}}
	for _, d := range captures {
		mics = append(mics, core.AudioDevice{ID: hex.EncodeToString(d.ID[:]), Name: d.Name()})
	}
	speakers := []core.AudioDevice{{ID: "", Name: "系统默认扬声器"}}
	for _, d := range playbacks {
		speakers = append(speakers, core.AudioDevice{ID: hex.EncodeToString(d.ID[:]), Name: d.Name()})
	}
	return mics, speakers
}

// Start 启动全双工流：20ms 帧（8kHz × 160 样本），采集回调驱动编码发送，
// 播放侧从媒体引擎抖动缓冲取数据。
func (b *malgoBackend) Start(micID, speakerID string, engine *media.Engine) error {
	if b.device != nil {
		b.Stop()
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Duplex)
	deviceConfig.SampleRate = 8000
	deviceConfig.PeriodSizeInMilliseconds = 20
	deviceConfig.Periods = 3
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = 1
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Playback.Channels = 1

	if micID != "" {
		if id, err := parseDeviceID(micID); err == nil {
			deviceConfig.Capture.DeviceID = id.Pointer()
		} else {
			logx.Logger().Warn("麦克风设备 ID 无效，使用默认设备", "id", micID, "err", err)
		}
	}
	if speakerID != "" {
		if id, err := parseDeviceID(speakerID); err == nil {
			deviceConfig.Playback.DeviceID = id.Pointer()
		} else {
			logx.Logger().Warn("扬声器设备 ID 无效，使用默认设备", "id", speakerID, "err", err)
		}
	}

	// 回调帧数不保证等于 20ms（WASAPI 共享模式下由设备周期决定）：
	// 媒体引擎的 FeedMic/ReadPlayback 均接受任意帧长，内部按编解码器
	// 帧长（20ms=160 样本）累积/取用，避免帧长不匹配导致的数据错乱。
	const maxCallbackFrames = 4096
	playBuf := make([]int16, maxCallbackFrames)
	micBuf := make([]int16, maxCallbackFrames)

	onFrames := func(output, input []byte, frameCount uint32) {
		if frameCount > maxCallbackFrames {
			frameCount = maxCallbackFrames
		}
		n := int(frameCount) * 2 // int16 → 2 字节
		// 播放：从媒体引擎按回调帧长取样本
		engine.ReadPlayback(playBuf[:frameCount])
		if len(output) >= n {
			for i, s := range playBuf[:frameCount] {
				binary.LittleEndian.PutUint16(output[i*2:], uint16(s))
			}
		}
		// 采集：输入 PCM → 媒体引擎（内部累积成 20ms 帧发送）
		if len(input) >= n {
			for i := range micBuf[:frameCount] {
				micBuf[i] = int16(binary.LittleEndian.Uint16(input[i*2:]))
			}
			engine.FeedMic(micBuf[:frameCount])
		}
	}

	callbacks := malgo.DeviceCallbacks{Data: onFrames}
	device, err := malgo.InitDevice(b.ctx.Context, deviceConfig, callbacks)
	if err != nil {
		return fmt.Errorf("初始化音频设备失败: %w", err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		return fmt.Errorf("启动音频设备失败: %w", err)
	}
	b.device = device
	return nil
}

func (b *malgoBackend) Stop() {
	if b.device == nil {
		return
	}
	b.device.Uninit()
	b.device = nil
}

// PlayTone 播放提示音（独立 playback 设备；loop=true 循环）。
// deviceID 指定播放设备（hex 编码），空 = 系统默认。
//
// 线程模型（改动前必读）：toneMu 只保护提示音状态，**不得跨越设备调用持有**
// ——miniaudio 的 ma_device_start/ma_device_uninit 会等待音频回调线程，而回调
// 要拿 toneMu，持锁调用必然死锁（实测：树莓派 PulseAudio 下 ma_device_start
// 永不返回，音频 worker 卡死，此后所有音频操作排队不执行，通话彻底无声）。
func (b *malgoBackend) PlayTone(tone *wavPCM, loop bool, deviceID string) error {
	if tone == nil || len(tone.pcm) == 0 {
		b.StopTone()
		return nil
	}
	channels := tone.channels
	if channels < 1 {
		channels = 1
	}

	// 设备复用快路径：同一设备、同采样率、同声道数时只换缓冲区，
	// 不再反复 Stop/Init（连续按键音就靠它保持跟手）。
	b.toneMu.Lock()
	if b.toneDev != nil && b.toneRate == tone.sampleRate &&
		b.toneCh == channels && b.toneDevID == deviceID {
		b.toneGen++
		if b.toneIdle != nil {
			b.toneIdle.Stop()
			b.toneIdle = nil
		}
		b.tonePCM, b.tonePos, b.toneLoop = tone.pcm, 0, loop
		b.toneMu.Unlock()
		return nil
	}
	b.toneMu.Unlock()

	b.StopTone()

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.SampleRate = uint32(tone.sampleRate)
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Playback.Channels = uint32(channels)
	if deviceID != "" {
		if id, err := parseDeviceID(deviceID); err == nil {
			deviceConfig.Playback.DeviceID = id.Pointer()
		}
	}

	// 状态写入持锁但极短（回调可能马上就跑起来读这些字段）
	b.toneMu.Lock()
	b.tonePCM = tone.pcm
	b.toneCh = channels
	b.tonePos = 0
	b.toneLoop = loop
	b.toneMu.Unlock()

	onFrames := func(output, input []byte, frameCount uint32) {
		b.toneMu.Lock()
		defer b.toneMu.Unlock()
		if len(b.tonePCM) == 0 {
			for i := range output {
				output[i] = 0
			}
			return
		}
		for i := 0; i < int(frameCount); i++ {
			for c := 0; c < b.toneCh; c++ {
				idx := i*2*b.toneCh + c*2
				if idx+1 < len(output) {
					var v int16
					if b.tonePos < len(b.tonePCM) {
						v = b.tonePCM[b.tonePos]
					}
					binary.LittleEndian.PutUint16(output[idx:], uint16(v))
				}
			}
			b.tonePos++
			if b.tonePos >= len(b.tonePCM) {
				if b.toneLoop {
					b.tonePos = 0 // 循环
				} else {
					b.tonePCM = nil // 播放完毕
					b.scheduleToneIdleCloseLocked()
					break
				}
			}
		}
	}

	callbacks := malgo.DeviceCallbacks{Data: onFrames}
	// 不持锁调用后端：Start 会等待音频回调线程，而回调要拿 toneMu
	device, err := malgo.InitDevice(b.ctx.Context, deviceConfig, callbacks)
	if err != nil {
		return fmt.Errorf("初始化提示音设备失败: %w", err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		return fmt.Errorf("启动提示音设备失败: %w", err)
	}
	b.toneMu.Lock()
	b.toneDev = device
	b.toneRate = tone.sampleRate
	b.toneCh = channels
	b.toneDevID = deviceID
	b.toneGen++
	b.toneMu.Unlock()
	return nil
}

// scheduleToneIdleCloseLocked 安排"空闲后关闭提示音设备"（须持 toneMu）。
// 用代次判断期间有没有新音进来：有就作废这次关闭（新音会自己重启计时）。
func (b *malgoBackend) scheduleToneIdleCloseLocked() {
	gen := b.toneGen
	if b.toneIdle != nil {
		b.toneIdle.Stop()
	}
	b.toneIdle = time.AfterFunc(toneIdleClose, func() { b.closeToneIfIdle(gen) })
}

// closeToneIfIdle 提示音播完且一段时间没有新音 → 关闭设备。
func (b *malgoBackend) closeToneIfIdle(gen uint64) {
	b.toneMu.Lock()
	b.toneIdle = nil
	if b.toneGen != gen || b.tonePCM != nil || b.toneDev == nil {
		b.toneMu.Unlock()
		return
	}
	device := b.toneDev
	b.toneDev = nil
	b.toneRate, b.toneCh, b.toneDevID = 0, 0, ""
	b.toneMu.Unlock()

	// 不持锁调用后端：Uninit 等待回调线程退出，而回调要拿 toneMu。
	device.Uninit()
}

// StopTone 停止提示音播放。
func (b *malgoBackend) StopTone() {
	b.toneMu.Lock()
	device := b.toneDev
	b.toneDev = nil
	b.tonePCM = nil
	b.toneRate, b.toneCh, b.toneDevID = 0, 0, ""
	b.toneGen++ // 作废在途的空闲关闭定时器
	if b.toneIdle != nil {
		b.toneIdle.Stop()
		b.toneIdle = nil
	}
	b.toneMu.Unlock()

	// 不持锁调用后端：Uninit 等待回调线程退出，而回调要拿 toneMu，
	// 持锁调用会死锁（同 PlayTone 的线程模型说明）。
	if device != nil {
		device.Uninit()
	}
}

func (b *malgoBackend) Close() {
	b.StopTone()
	b.Stop()
	if b.ctx != nil {
		_ = b.ctx.Uninit()
		b.ctx.Free()
	}
}

// parseDeviceID 将 hex 编码的设备 ID 转回 malgo 字节数组格式。
func parseDeviceID(s string) (*malgo.DeviceID, error) {
	var id malgo.DeviceID
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(raw) != len(id) {
		return nil, fmt.Errorf("设备 ID 长度非法: %d", len(raw))
	}
	copy(id[:], raw)
	return &id, nil
}
