package audio

import (
	"embed"
	"encoding/binary"
	"fmt"
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
