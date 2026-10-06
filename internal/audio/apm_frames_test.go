package audio

import "testing"

// TestAPMFramerSplitsIntoFrames 分帧层（REQ-MEDIA-007）：任意帧长的采集/播放
// 样本要被切成定长 10ms 帧处理，处理结果按输入节奏回吐，且**两路始终同步**。
func TestAPMFramerSplitsIntoFrames(t *testing.T) {
	const frame = 80 // 8kHz 的 10ms
	f := newAPMFramer(frame, maxCallbackFrames)

	var frames [][2]int // 每次 process 收到的 (mic, ref) 首样本，用于核对切帧与同步
	process := func(micFrame, refFrame []int16) {
		if len(micFrame) != frame || len(refFrame) != frame {
			t.Fatalf("帧长不对：mic=%d ref=%d 期望 %d", len(micFrame), len(refFrame), frame)
		}
		frames = append(frames, [2]int{int(micFrame[0]), int(refFrame[0])})
		for i := range micFrame {
			micFrame[i] = -micFrame[i] // 让处理可见：取反
		}
	}

	// 回调帧长故意不整除：137 个样本（模拟 WASAPI 不规整周期）。
	// 注意输出比输入滞后不到一帧（设计如此），所以这里把两路都记下来，
	// 最后按固定延迟对齐核对。
	var inAll, outAll []int16
	mic := make([]int16, 137)
	ref := make([]int16, 137)
	for round := 0; round < 10; round++ {
		for i := range mic {
			mic[i] = int16(round*1000 + i)
			ref[i] = int16(round*1000 + i + 7) // 参考信号整体偏移，验证两路一一对应
		}
		inAll = append(inAll, mic...) // Filter 会就地覆盖，先存输入
		n := f.Filter(mic, ref, process)
		if n < 0 || n > len(mic) {
			t.Fatalf("返回样本数越界：%d", n)
		}
		outAll = append(outAll, mic[:n]...)
	}

	// 输出样本数略少于输入（<10ms 的尾巴还在内部）
	if len(outAll) > len(inAll) {
		t.Fatalf("回吐样本数 %d 超过输入 %d", len(outAll), len(inAll))
	}
	if len(inAll)-len(outAll) >= frame {
		t.Fatalf("遗留样本 %d 超过一帧（%d）", len(inAll)-len(outAll), frame)
	}

	// 找到固定延迟：outAll[k] 应等于 -inAll[k-delay]
	delay := -1
	for d := 0; d < frame && d < len(outAll); d++ {
		if outAll[0] == -inAll[d] {
			delay = d
			break
		}
	}
	if delay < 0 {
		t.Fatalf("输出与输入对不上：out[0]=%d 在输入前 %d 个样本里找不到", outAll[0], frame)
	}
	for k := range outAll {
		if outAll[k] != -inAll[k-delay] {
			t.Fatalf("第 %d 个输出样本（延迟 %d）不对：得到 %d 期望 %d", k, delay, outAll[k], -inAll[k-delay])
		}
	}

	// 每次处理的帧，mic 与 ref 的第一样本必须保持固定偏移 7（两路同步）
	for i, fr := range frames {
		if fr[1]-fr[0] != 7 {
			t.Fatalf("第 %d 帧两路不同步：mic=%d ref=%d", i, fr[0], fr[1])
		}
	}
}

// TestAPMFramerExactFrames 回调帧长正好是整数帧时应该逐帧直通、零额外延迟。
func TestAPMFramerExactFrames(t *testing.T) {
	const frame = 160 // 16kHz
	f := newAPMFramer(frame, maxCallbackFrames)
	count := 0
	process := func(micFrame, refFrame []int16) { count++ }

	mic := make([]int16, frame*2) // 一拍两帧（PeriodSize 20ms）
	ref := make([]int16, frame*2)
	n := f.Filter(mic, ref, process)
	if n != len(mic) {
		t.Fatalf("整数帧时应原样回吐：得到 %d 期望 %d", n, len(mic))
	}
	if count != 2 {
		t.Fatalf("两帧样本应处理 2 帧，实际 %d", count)
	}
}
