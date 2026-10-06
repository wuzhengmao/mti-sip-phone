//go:build apm && cgo

package audio

import (
	"math"
	"math/rand"
	"testing"
)

// TestAPMSuppressesEcho 音频预处理真的在消回声（REQ-MEDIA-007）：
// 构造一个"扬声器放什么、麦克风就收到它的延迟衰减副本（外加本端噪声）"的
// 假回声路径，跑一段后看残余回声比输入小多少（ERLE）。AEC 需要时间收敛，
// 所以只衡量后半段的抑制量。
//
// 这个测试同时验证三件事：C++ 桥接能跑、10ms 分帧正确、参考信号接对了
//（参考就是"播放中的样本"，接错方向/Tee 错信号时抑制量会接近 0）。
func TestAPMSuppressesEcho(t *testing.T) {
	const rate = 8000
	const frame = rate / 100 // 10ms
	const total = rate * 4   // 4 秒

	pre := newPreprocessor(rate, Preprocess{AEC: true})
	if pre == nil {
		t.Skip("本构建没有音频预处理（需要 -tags apm 与 APM 静态库）")
	}
	defer pre.Close()
	if _, ok := pre.(*apmProcessor); !ok {
		t.Fatalf("期望拿到 APM 实现，实际 %T", pre)
	}

	rng := rand.New(rand.NewSource(1))
	// 参考信号（远端说话）：白噪声经一阶低通，接近语音的频谱重心
	ref := make([]int16, total)
	lowpass := 0.0
	for i := range ref {
		lowpass = 0.7*lowpass + 0.3*(rng.Float64()*2-1)
		ref[i] = int16(lowpass * 12000)
	}
	// 麦克风信号 = 延迟 32ms、衰减 0.5 的回声 + 本端噪声
	const delay = 32 * (rate / 1000)
	micClean := make([]int16, total)
	for i := range micClean {
		var echo float64
		if i >= delay {
			echo = 0.5 * float64(ref[i-delay])
		}
		noise := (rng.Float64()*2 - 1) * 200
		micClean[i] = int16(clampF(echo+noise, -32768, 32767))
	}

	out := make([]int16, total)
	var outN int
	mic := make([]int16, frame)
	refBuf := make([]int16, frame)
	for off := 0; off+frame <= total; off += frame {
		copy(mic, micClean[off:off+frame])
		copy(refBuf, ref[off:off+frame])
		n := pre.Process(mic, refBuf)
		// 输出比输入滞后不到一帧：这里按 FIFO 收集，最后从尾部对齐比较
		for i := 0; i < n && outN < total; i++ {
			out[outN] = mic[i]
			outN++
		}
	}

	// 收敛后（后 1 秒）+ 延迟补偿：比较残余回声与原始回声的能量
	const skip = 3 * rate
	delayComp := total - outN // 输出整体滞后这么多
	var inE, outE float64
	var cnt int
	for i := skip; i+delayComp < total; i++ {
		inE += math.Pow(float64(micClean[i]), 2)
		outE += math.Pow(float64(out[i+delayComp]), 2)
		cnt++
	}
	if cnt == 0 {
		t.Fatal("样本数不足，测试构造有误")
	}
	inRMS := math.Sqrt(inE / float64(cnt))
	outRMS := math.Sqrt(outE / float64(cnt))
	erle := 20 * math.Log10(inRMS/math.Max(outRMS, 1))
	t.Logf("输入 RMS=%.0f 输出 RMS=%.0f 回声抑制≈%.1f dB", inRMS, outRMS, erle)
	if erle < 6 {
		t.Fatalf("回声抑制只有 %.1f dB，AEC 没起作用（期望 ≥6 dB）", erle)
	}
}

// TestAPMAllOffReturnsNil 三项全关时不建实例（不白花 CPU）。
func TestAPMAllOffReturnsNil(t *testing.T) {
	if p := newPreprocessor(8000, Preprocess{}); p != nil {
		p.Close()
		t.Fatal("全关时不该创建预处理器")
	}
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
