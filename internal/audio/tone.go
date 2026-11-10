package audio

import (
	"embed"
	"encoding/binary"
	"fmt"
	"math"
)

//go:embed assets/*
var toneAssets embed.FS

// wavPCM 内嵌铃声（PCM 16bit），随二进制分发。
type wavPCM struct {
	sampleRate int
	channels   int
	pcm        []int16
}

var (
	ringtone = mustLoadWAV("assets/ringtone.wav")
	hangup   = mustLoadWAV("assets/hangup.wav")
	// ringback 本地回铃音：呼出时**没有早期媒体**才播（有早期媒体就听对端的）。
	ringback = mustLoadWAV("assets/ringback.wav")
	// busy 遇忙音：对端回 486/600 时播。
	busy = mustLoadWAV("assets/busy.wav")
)

// ---- DTMF 按键回显音（拨号盘按钮 / 键盘数字按键时本机播放）----

// dtmfFreqs ITU-T 双音多频频率表（低频, 高频），单位 Hz。
// A-D 是规范里的第四列，拨号盘上没有，这里一并生成备用。
var dtmfFreqs = map[string][2]float64{
	"1": {697, 1209}, "2": {697, 1336}, "3": {697, 1477},
	"4": {770, 1209}, "5": {770, 1336}, "6": {770, 1477},
	"7": {852, 1209}, "8": {852, 1336}, "9": {852, 1477},
	"*": {941, 1209}, "0": {941, 1336}, "#": {941, 1477},
	"A": {697, 1633}, "B": {770, 1633}, "C": {852, 1633}, "D": {941, 1633},
}

const (
	dtmfSampleRate = 8000 // 与通话音频管线一致（8kHz 单声道）
	dtmfDurationMS = 120  // 单个按键音时长：够听清又不拖泥带水
	dtmfFadeMS     = 5    // 首尾淡入淡出，避免爆音
	// 两个正弦相加后峰值最多 2×，留足余量（0.22×2 ≈ 0.44 满幅）
	dtmfAmplitude = 0.22
)

// dtmfTones 按键回显音：按频率表**合成**短音（而不是内嵌 WAV——16 个音共约 30KB
// PCM，合成逻辑更直观，加 A-D 或改时长也不用重新做资源）。
var dtmfTones = buildDTMFTones()

// buildDTMFTones 逐个按键合成 16bit PCM 短音。
func buildDTMFTones() map[string]*wavPCM {
	n := dtmfSampleRate * dtmfDurationMS / 1000
	fade := dtmfSampleRate * dtmfFadeMS / 1000
	out := make(map[string]*wavPCM, len(dtmfFreqs))
	for key, f := range dtmfFreqs {
		pcm := make([]int16, n)
		for i := 0; i < n; i++ {
			tt := float64(i) / float64(dtmfSampleRate)
			v := math.Sin(2*math.Pi*f[0]*tt) + math.Sin(2*math.Pi*f[1]*tt)
			gain := 1.0
			switch {
			case fade > 0 && i < fade:
				gain = float64(i) / float64(fade)
			case fade > 0 && i >= n-fade:
				gain = float64(n-i-1) / float64(fade)
			}
			pcm[i] = int16(v * dtmfAmplitude * gain * math.MaxInt16)
		}
		out[key] = &wavPCM{sampleRate: dtmfSampleRate, channels: 1, pcm: pcm}
	}
	return out
}

// ---- 自动接听接通提示音（"嘀"）----

const (
	beepSampleRate = 8000 // 与 DTMF / 通话管线一致
	beepFrequency  = 1000 // 单频正弦，清晰的一声"嘀"
	beepDurationMS = 200  // 够听清、不拖泥带水
	beepFadeMS     = 5    // 首尾淡入淡出，避免爆音
	// 单正弦峰值就是 1×，0.5 满幅足够响亮且不刺耳
	beepAmplitude = 0.5
)

// beep 自动接听接通提示音：与 DTMF 同理**合成**短音，不额外加 WAV 资源。
var beep = buildBeep()

// buildBeep 合成 16bit PCM 单频短音。
func buildBeep() *wavPCM {
	n := beepSampleRate * beepDurationMS / 1000
	fade := beepSampleRate * beepFadeMS / 1000
	pcm := make([]int16, n)
	for i := 0; i < n; i++ {
		tt := float64(i) / float64(beepSampleRate)
		v := math.Sin(2 * math.Pi * beepFrequency * tt)
		gain := 1.0
		switch {
		case fade > 0 && i < fade:
			gain = float64(i) / float64(fade)
		case fade > 0 && i >= n-fade:
			gain = float64(n-i-1) / float64(fade)
		}
		pcm[i] = int16(v * beepAmplitude * gain * math.MaxInt16)
	}
	return &wavPCM{sampleRate: beepSampleRate, channels: 1, pcm: pcm}
}

// mustLoadWAV 加载内嵌 WAV（仅支持 PCM 16bit），失败直接 panic
// （资源损坏属于构建问题，运行期不恢复）。
func mustLoadWAV(name string) *wavPCM {
	data, err := toneAssets.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("铃声资源缺失: %s", name))
	}
	sr, ch, pcm, err := decodeWAV(data)
	if err != nil {
		panic(fmt.Sprintf("铃声解析失败 %s: %v", name, err))
	}
	return &wavPCM{sampleRate: sr, channels: ch, pcm: pcm}
}

// decodeWAV 解析 RIFF/WAVE PCM 16bit 文件，返回采样率、声道数与样本。
func decodeWAV(data []byte) (int, int, []int16, error) {
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, 0, nil, fmt.Errorf("不是合法的 WAV 文件")
	}
	var sampleRate, channels, bitsPerSample int
	var pcm []int16
	offset := 12
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		body := data[offset+8:]
		if len(body) < size {
			break
		}
		switch chunkID {
		case "fmt ":
			channels = int(binary.LittleEndian.Uint16(body[0:2]))
			sampleRate = int(binary.LittleEndian.Uint32(body[4:8]))
			bitsPerSample = int(binary.LittleEndian.Uint16(body[14:16]))
		case "data":
			if bitsPerSample != 16 {
				return 0, 0, nil, fmt.Errorf("不支持的位深: %d", bitsPerSample)
			}
			n := size / 2
			pcm = make([]int16, n)
			for i := 0; i < n; i++ {
				pcm[i] = int16(binary.LittleEndian.Uint16(body[i*2:]))
			}
		}
		offset += 8 + size + (size & 1) // 块按 2 字节对齐
	}
	if pcm == nil {
		return 0, 0, nil, fmt.Errorf("WAV 缺少 data 块")
	}
	return sampleRate, channels, pcm, nil
}
