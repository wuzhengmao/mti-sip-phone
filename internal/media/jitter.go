package media

// playRing 播放环形缓冲：样本粒度（int16），供音频回调按任意帧长读取。
// 容量按秒数换算（8kHz 采样率），写满时丢弃最旧数据，防止播放延迟累积。
//
// 取代早期按包重排的 jitterBuffer：后者在缓冲空时无限循环最后一帧
//（回铃音停顿期产生持续嗡鸣噪音），且消费粒度固定 20ms、无法适配
// 音频回调的实际帧长（WASAPI 共享模式下回调帧数不保证等于请求值）。
type playRing struct {
	buf  []int16
	r    int // 读位置
	w    int // 写位置
	size int // 当前样本数
}

func newPlayRing(seconds int) *playRing {
	return &playRing{buf: make([]int16, seconds*8000)}
}

// Write 追加样本；缓冲满时丢弃最旧数据。
func (r *playRing) Write(samples []int16) {
	if len(samples) == 0 {
		return
	}
	// 单次写入超过容量：只保留末尾
	if len(samples) >= len(r.buf) {
		samples = samples[len(samples)-len(r.buf):]
	}
	// 空间不足：丢弃最旧数据
	if over := len(samples) - (len(r.buf) - r.size); over > 0 {
		r.r = (r.r + over) % len(r.buf)
		r.size -= over
	}
	first := len(r.buf) - r.w
	if first > len(samples) {
		first = len(samples)
	}
	copy(r.buf[r.w:], samples[:first])
	copy(r.buf, samples[first:])
	r.w = (r.w + len(samples)) % len(r.buf)
	r.size += len(samples)
}

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
