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
	buf      []int16
	r        int // 读位置
	w        int // 写位置
	size     int // 当前样本数
	trim     int // 累计因超深而丢弃的样本数（诊断用）
	cutRatio float64
	scratch  []int16
}

func newPlayRing(seconds int) *playRing {
	return &playRing{buf: make([]int16, seconds*8000)}
}

// Write 追加样本；超过硬上限时按"最安静窗口"丢弃数据回到目标深度。
// 返回本次丢弃的样本数。
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
		dropped += r.trimQuiet(r.size - playTargetDepth)
	}
	r.trim += dropped
	return dropped
}

// trimQuiet 去掉 out 个样本把深度压回目标值：**优先在缓冲区最旧一段里最安静
// 的窗口丢**（回铃音、语音都有停顿），而不是机械地从最旧端硬切。
//
// 为什么：硬切会把音调从中间截断，听感就是"断续"——实测每通呼出的早期媒体
// 建立约 2 秒时都会超深一次（对端回铃音刚开始/设备启动期的垫高），被硬切的
// 恰好是第一声回铃。挑安静窗口丢，回铃音与语音的停顿足够容纳这点长度。
// 找不到安静位置时退回最旧端，并在接缝处做 2ms 交叉淡化避免爆音。
func (r *playRing) trimQuiet(out int) int {
	if out <= 0 || out >= r.size {
		return 0
	}
	// 只在最旧的一段里找（丢得越早，延迟回收越直接）；跨度 = 需要丢的量 + 300ms 余量
	const (
		fadeStep  = 80 // 候选窗口步长：10ms（8kHz）
		fadeSlack = 2400
		fadeLen   = 16 // 接缝交叉淡化 2ms
	)
	if cap(r.scratch) < r.size {
		r.scratch = make([]int16, r.size)
	}
	tmp := r.scratch[:r.size]
	first := len(r.buf) - r.r
	if first > r.size {
		first = r.size
	}
	copy(tmp[:first], r.buf[r.r:r.r+first])
	copy(tmp[first:], r.buf[:r.size-first])

	span := out + fadeSlack
	if span > len(tmp)-out {
		span = len(tmp) - out
	}

	// 找窗口内绝对值和最小的位置（近似能量，够用且便宜）
	best, bestEnergy := 0, int64(1)<<62
	var total, candidates int64
	for off := 0; off+out <= span; off += fadeStep {
		var e int64
		for _, v := range tmp[off : off+out] {
			if v < 0 {
				e -= int64(v)
			} else {
				e += int64(v)
			}
		}
		total += e
		candidates++
		if e < bestEnergy {
			best, bestEnergy = off, e
		}
	}
	// 切点能量 / 平均能量：接近 0 = 切在停顿里（听不出来）；接近 1 = 全是连续
	// 有声内容（只能硬切，靠接缝淡化兜）。日志用它判断这次截断是否安全。
	r.cutRatio = 1
	if candidates > 0 {
		if avg := total / candidates; avg > 0 {
			r.cutRatio = float64(bestEnergy) / float64(avg)
		}
	}

	// 删掉 [best, best+out)（copy 对重叠区间是 memmove 语义）
	copy(tmp[best:], tmp[best+out:])
	tmp = tmp[:len(tmp)-out]

	// 接缝交叉淡化：把断点前的 fadeLen 个样本与断点后的 blend 起来
	if best >= fadeLen && len(tmp) >= best+fadeLen {
		pre := make([]int16, fadeLen)
		copy(pre, tmp[best-fadeLen:best])
		for i := 0; i < fadeLen; i++ {
			a := int32(i+1) * 32768 / int32(fadeLen+1)
			tmp[best-fadeLen+i] = int16((int32(pre[i])*(32768-a) + int32(tmp[best+i])*a) >> 15)
		}
	}

	// 写回（重置为从 0 开始的连续段）
	copy(r.buf, tmp)
	r.r, r.w, r.size = 0, len(tmp), len(tmp)
	return out
}

// Depth 当前缓冲深度（样本数）。
func (r *playRing) Depth() int { return r.size }

// Trimmed 累计丢弃的样本数（诊断用）。
func (r *playRing) Trimmed() int { return r.trim }

// CutRatio 最近一次超深截断的"切点能量 / 平均能量"（诊断用，见 trimQuiet）。
func (r *playRing) CutRatio() float64 { return r.cutRatio }

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
