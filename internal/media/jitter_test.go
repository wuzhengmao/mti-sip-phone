package media

import "testing"

// TestPlayRingBasic 环形缓冲基本读写：目标深度内写多少读多少，跨环形边界连续。
func TestPlayRingBasic(t *testing.T) {
	r := newPlayRing(1)
	samples := make([]int16, playTargetDepth)
	for i := range samples {
		samples[i] = int16(i)
	}
	r.Write(samples)

	buf := make([]int16, playTargetDepth)
	if n := r.Read(buf); n != playTargetDepth {
		t.Fatalf("应读到 %d 样本，实际 %d", playTargetDepth, n)
	}
	for i := range buf {
		if buf[i] != int16(i) {
			t.Fatalf("样本 %d 应为 %d，实际 %d", i, i, buf[i])
		}
	}
}

// TestPlayRingDepthBounded 缓冲深度必须封顶——这是"声音延迟几秒"的根治点。
//
// 读侧（音频回调）恒为实时速率，写侧会被丢包补静音、对端突发一次性垫高；
// 早先容量 8 秒，垫高后再也回不去，表现为"通话越久延迟越大"。
// 现在超过 playMaxDepth 就丢最旧数据回到 playTargetDepth。
func TestPlayRingDepthBounded(t *testing.T) {
	r := newPlayRing(8) // 容量仍是 8 秒
	// 一次性灌入 2 秒（对端突发/大量丢包补静音的极端情形）
	burst := make([]int16, 16000)
	for i := range burst {
		burst[i] = 7
	}
	if dropped := r.Write(burst); dropped == 0 {
		t.Fatal("超深写入应被截断，否则延迟会一直累积")
	}
	if d := r.Depth(); d != playTargetDepth {
		t.Fatalf("截断后深度应为 %d（%dms），实际 %d", playTargetDepth, playTargetDepth*1000/8000, d)
	}
	if r.Trimmed() == 0 {
		t.Fatal("应记录累计丢弃量（诊断用）")
	}

	// 稳态：逐帧写入不超过上限时不应被截断
	r.Reset()
	frame := make([]int16, 160)
	for i := 0; i < 20; i++ { // 20 帧 = 400ms < 500ms 上限
		if dropped := r.Write(frame); dropped != 0 {
			t.Fatalf("第 %d 帧不应被截断（深度仍在安全范围内）", i)
		}
	}
}

// TestPlayRingOverflow 缓冲超过上限后写新数据：最旧数据被丢弃，读到的总是最新。
func TestPlayRingOverflow(t *testing.T) {
	r := newPlayRing(1)
	first := make([]int16, playMaxDepth)
	for i := range first {
		first[i] = 1
	}
	r.Write(first) // 恰好到硬上限，尚不触发截断
	tail := []int16{2, 3, 4, 5}
	if dropped := r.Write(tail); dropped == 0 {
		t.Fatal("超过硬上限应触发截断，否则延迟会一直累积")
	}
	if d := r.Depth(); d != playTargetDepth {
		t.Fatalf("截断后应为目标深度 %d，实际 %d", playTargetDepth, d)
	}

	buf := make([]int16, playTargetDepth)
	if n := r.Read(buf); n != playTargetDepth {
		t.Fatalf("应读到 %d 样本，实际 %d", playTargetDepth, n)
	}
	if buf[0] != 1 {
		t.Fatalf("截断保留的应是最新的旧数据，实际首样本 %d", buf[0])
	}
	for i, v := range buf[playTargetDepth-len(tail):] {
		if v != tail[i] {
			t.Fatalf("尾部新数据 %d 应为 %d，实际 %d", i, tail[i], v)
		}
	}
}

// TestPlayRingReadLessThanAvailable 分次读取：数据不丢不重。
func TestPlayRingReadLessThanAvailable(t *testing.T) {
	r := newPlayRing(1)
	samples := make([]int16, 1000) // 1000 < playTargetDepth，不会被截断
	for i := range samples {
		samples[i] = int16(i)
	}
	r.Write(samples)

	buf := make([]int16, 300)
	total := 0
	for total < 1000 {
		n := r.Read(buf)
		if n == 0 {
			t.Fatal("不应提前读空")
		}
		for i, v := range buf[:n] {
			if v != int16(total+i) {
				t.Fatalf("样本 %d 应为 %d，实际 %d", total+i, total+i, v)
			}
		}
		total += n
	}
	if n := r.Read(buf); n != 0 {
		t.Fatalf("读空后应返回 0，实际 %d", n)
	}
}

// TestPlayRingReset Reset 后缓冲清空。
func TestPlayRingReset(t *testing.T) {
	r := newPlayRing(1)
	r.Write(make([]int16, 160))
	r.Reset()
	buf := make([]int16, 160)
	if n := r.Read(buf); n != 0 {
		t.Fatalf("Reset 后应读空，实际 %d", n)
	}
}

// TestPlayRingTrimQuiet 超深截断要丢"最安静"的一段：回铃音场景（静音 + 音调）
// 丢静音、音调一个样本都不能少——硬切会把音调截断，听感就是"断续"。
func TestPlayRingTrimQuiet(t *testing.T) {
	r := newPlayRing(8)
	silence := make([]int16, 3000) // 375ms 静音
	tone := make([]int16, 1500)    // 187ms 音调（幅值 8000，便于辨认）
	for i := range tone {
		tone[i] = 8000
	}
	r.Write(silence)
	r.Write(tone) // 深度 4500 > 上限 4000 → 截断到 1600（丢掉 2900，静音够容纳）

	if d := r.Depth(); d != playTargetDepth {
		t.Fatalf("截断后深度应为 %d，实际 %d", playTargetDepth, d)
	}
	// 音调必须完整保留（1500 样本）
	out := make([]int16, 8192)
	n := r.Read(out)
	toneCount := 0
	for _, v := range out[:n] {
		if v == 8000 {
			toneCount++
		}
	}
	if toneCount != len(tone) {
		t.Fatalf("音调应完整保留 %d 个样本，实际 %d（被硬切在音调里了）", len(tone), toneCount)
	}
}

// TestPlayRingTrimContinuous 全是有声内容（找不到安静窗口）时退回最旧端，
// 深度仍被压回目标值且不 panic。
func TestPlayRingTrimContinuous(t *testing.T) {
	r := newPlayRing(8)
	loud := make([]int16, 8000)
	for i := range loud {
		loud[i] = 9000
	}
	r.Write(loud)
	if d := r.Depth(); d != playTargetDepth {
		t.Fatalf("连续有声内容也应压回目标深度，实际 %d", d)
	}
}
