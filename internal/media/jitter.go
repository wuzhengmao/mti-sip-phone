package media

// playTargetDepth 播放缓冲的目标深度（样本数，8kHz 下 1600 = 200ms）。
// 足够吸收 LAN 抖动，又不会让人耳察觉到延迟。
const playTargetDepth = 1600

// playMaxDepth 播放缓冲的硬上限（样本数，8000 下 4000 = 500ms）。
// 超过就丢最旧数据回到目标深度。
//
// 为什么必须封顶：读侧（音频回调）**恒为实时速率**，而写侧会被两类事件
// 一次性垫高——丢包时按 gap 补静音、对端/网关的突发投递。垫高之后深度
// 只会保持不变、**永远回不去**（读多少写多少），于是"通话越久延迟越大"。
// 早先容量给到 8 秒，实际等于允许累积 8 秒延迟。宁可偶发一次轻微跳音。
const playMaxDepth = 4000

// playRing 播放环形缓冲：样本粒度（int16），供音频回调按任意帧长读取。
// 容量按秒数换算（8kHz 采样率），超过硬上限时丢弃最旧数据。
//
// 取代早期按包重排的 jitterBuffer：后者在缓冲空时无限循环最后一帧
//（回铃音停顿期产生持续嗡鸣噪音），且消费粒度固定 20ms、无法适配
// 音频回调的实际帧长（WASAPI 共享模式下回调帧数不保证等于请求值）。
type playRing struct {
	buf  []int16
	r    int // 读位置
	w    int // 写位置
	size int // 当前样本数
	trim int // 累计因超深而丢弃的样本数（诊断用）
}

func newPlayRing(seconds int) *playRing {
	return &playRing{buf: make([]int16, seconds*8000)}
}

// Write 追加样本；超过硬上限时丢弃最旧数据回到目标深度。返回本次丢弃的样本数。
func (r *playRing) Write(samples []int16) int {
	if len(samples) == 0 {
		return 0
	}
	// 单次写入超过容量：只保留末尾
	if len(samples) >= len(r.buf) {
		samples = samples[len(samples)-len(r.buf):]
	}
	dropped := 0
	// 空间不足：丢弃最旧数据
	if over := len(samples) - (len(r.buf) - r.size); over > 0 {
		r.r = (r.r + over) % len(r.buf)
		r.size -= over
		dropped += over
	}
	first := len(r.buf) - r.w
	if first > len(samples) {
		first = len(samples)
	}
	copy(r.buf[r.w:], samples[:first])
	copy(r.buf, samples[first:])
	r.w = (r.w + len(samples)) % len(r.buf)
	r.size += len(samples)

	// 深度封顶：见 playMaxDepth 注释
	if r.size > playMaxDepth {
		over := r.size - playTargetDepth
		r.r = (r.r + over) % len(r.buf)
		r.size -= over
		dropped += over
	}
	r.trim += dropped
	return dropped
}

// Depth 当前缓冲深度（样本数）。
func (r *playRing) Depth() int { return r.size }

// Trimmed 累计丢弃的样本数（诊断用）。
func (r *playRing) Trimmed() int { return r.trim }

// Read 读取至多 len(samples) 个样本，返回实际数量（不足时由调用方补静音）。
func (r *playRing) Read(samples []int16) int {
	n := len(samples)
	if n > r.size {
		n = r.size
	}
	first := len(r.buf) - r.r
	if first > n {
		first = n
	}
	copy(samples[:first], r.buf[r.r:])
	copy(samples[first:n], r.buf[:n-first])
	r.r = (r.r + n) % len(r.buf)
	r.size -= n
	return n
}

// Reset 清空缓冲（媒体流切换时使用）。
func (r *playRing) Reset() {
	r.r, r.w, r.size = 0, 0, 0
}
