package audio

import (
	"math"
	"testing"
)

// goertzel 用 Goertzel 算法估算某频率分量的幅度（DTMF 检测的经典做法）。
func goertzel(pcm []int16, rate int, freq float64) float64 {
	n := float64(len(pcm))
	coeff := 2 * math.Cos(2*math.Pi*freq/float64(rate))
	var s1, s2 float64
	for _, s := range pcm {
		s0 := float64(s) + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	return math.Sqrt(s1*s1+s2*s2-coeff*s1*s2) / n * 2
}

// TestDTMFTonesAreStandardDTMF 按键音必须是标准 DTMF：每个按键音里两个标称
// 频率的分量要远强于其它频率（否则线路/对端的 DTMF 检测会失败）。
func TestDTMFTonesAreStandardDTMF(t *testing.T) {
	if len(dtmfTones) != len(dtmfFreqs) {
		t.Fatalf("按键音数量 %d，频率表 %d", len(dtmfTones), len(dtmfFreqs))
	}
	wantSamples := dtmfSampleRate * dtmfDurationMS / 1000
	for key, f := range dtmfFreqs {
		tone := dtmfTones[key]
		if tone == nil {
			t.Fatalf("%q 没有生成按键音", key)
		}
		if tone.sampleRate != dtmfSampleRate || tone.channels != 1 {
			t.Fatalf("%q 参数不对：%dHz %d 声道", key, tone.sampleRate, tone.channels)
		}
		if len(tone.pcm) != wantSamples {
			t.Fatalf("%q 时长不对：%d 个样本，期望 %d", key, len(tone.pcm), wantSamples)
		}
		lo := goertzel(tone.pcm, tone.sampleRate, f[0])
		hi := goertzel(tone.pcm, tone.sampleRate, f[1])
		other := goertzel(tone.pcm, tone.sampleRate, 1000) // 对照：非标称频率
		if lo < other*4 || hi < other*4 {
			t.Errorf("%q（%.0f/%.0fHz）分量不足：低频 %.0f、高频 %.0f、对照 1000Hz %.0f",
				key, f[0], f[1], lo, hi, other)
		}
		// 首尾淡入淡出（防爆音）：第一个样本为 0
		if tone.pcm[0] != 0 {
			t.Errorf("%q 起始样本 %d，期望淡入到 0", key, tone.pcm[0])
		}
		// 不削顶：两个正弦叠加的峰值不超过设计幅度
		var peak int16
		for _, s := range tone.pcm {
			if s > peak {
				peak = s
			}
		}
		// 设计峰值（两个正弦叠加）+ 5% 余量：math.Round 让它是运行期值而不是常量
		limit := int16(math.Round(32767 * 2 * dtmfAmplitude * 1.05))
		if peak > limit {
			t.Errorf("%q 峰值 %d 超过设计上限 %d", key, peak, limit)
		}
		if peak == 0 {
			t.Errorf("%q 是静音", key)
		}
	}
}
