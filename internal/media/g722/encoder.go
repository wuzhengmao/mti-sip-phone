package g722

// prevSamplesBufSize 与参考实现一致：QMF 延迟线按 1024 样本的环形缓冲管理，
// 写满后只保留最后 22 个样本（QMF 需要 24 个，留下 22 是为了下次推入 2 个样本后
// 恰好凑齐 24）。
const prevSamplesBufSize = 1024

// Encoder G.722 编码器（模式 1：64 kbit/s，低带 6 位 + 高带 2 位）。
// 不是并发安全的，调用方负责串行化。
type Encoder struct {
	band [2]band
	prev [prevSamplesBufSize]int16 // QMF 延迟线
	pos  int
}

// NewEncoder 创建编码器（初始状态与标准规定一致：低带标度因子 8、高带 2，
// 延迟线预置 22 个零样本——这也是 G.722 编码器 22 个样本时延的来源）。
func NewEncoder() *Encoder {
	e := &Encoder{}
	e.Reset()
	return e
}

// Reset 复位到初始状态（换流/重新协商时调用，避免带上一次的状态）。
func (e *Encoder) Reset() {
	e.band[0].reset(8)
	e.band[1].reset(2)
	for i := range e.prev {
		e.prev[i] = 0
	}
	e.pos = 22
}

// Encode 把 16 kHz 单声道 PCM 编码成码流：len(pcm) 个样本 → len(pcm)/2 字节。
// out 至少要有 len(pcm)/2 字节；多余的样本（奇数个）被忽略。
func (e *Encoder) Encode(pcm []int16, out []byte) {
	n := 0
	for i := 0; i+1 < len(pcm) && n < len(out); i += 2 {
		xlow, xhigh := e.filter(pcm[i], pcm[i+1])

		// 高带 2 位、低带 6 位；顺序不能反：两个预测器都用**更新前**的状态编码
		ihigh := encodeHigh(&e.band[1], xhigh)
		ilow := encodeLow(&e.band[0], xlow)

		// 差分值按更新前的标度因子算，再更新预测器
		dhigh := int32(e.band[1].scaleFactor) * highInvQuant[ihigh] >> 10
		e.band[1].updateHigh(dhigh, ihigh)
		e.band[0].updateLow(ilow >> 2)

		out[n] = byte(ihigh<<6 | ilow)
		n++
	}
}

// filter 做 QMF 分解：推入 2 个 16 kHz 样本，得到低带/高带各一个样本。
// 与参考实现一致：低带 = (x0+x1)>>14，高带 = (x0-x1)>>14。
func (e *Encoder) filter(s0, s1 int16) (xlow, xhigh int32) {
	e.prev[e.pos] = s0
	e.pos++
	e.prev[e.pos] = s1
	e.pos++
	x0, x1 := applyQMF(e.prev[e.pos-24 : e.pos])
	if e.pos >= prevSamplesBufSize {
		copy(e.prev[:22], e.prev[e.pos-22:e.pos])
		e.pos = 22
	}
	return (x0 + x1) >> 14, (x0 - x1) >> 14
}

// encodeHigh 高带量化（2 位码字，标准 4.2 节）。
// 返回 0–3：符号位（bit1）+ 幅度是否小于判决电平（bit0）。
func encodeHigh(b *band, xhigh int32) int32 {
	diff := int32(clipInt16(xhigh - int32(b.sPredictor)))
	pred := 141 * int32(b.scaleFactor) >> 8
	// (diff ^ (diff>>31)) 即 |diff|（负数时为 ~diff = -diff-1）
	if (diff^(diff>>31)) < pred {
		if diff >= 0 {
			return 3
		}
		return 1
	}
	if diff >= 0 {
		return 2
	}
	return 0
}

// encodeLow 低带量化（6 位码字，标准 4.2 节）。
// 判决电平表 lowQuant 按 scaleFactor 缩放后逐级比较，得到的级数映射到 6 位码字。
func encodeLow(b *band, xlow int32) int32 {
	diff := int32(clipInt16(xlow - int32(b.sPredictor)))
	limit := diff ^ (diff >> 31) // |diff|
	i := int32(0)
	limit = (limit + 1) << 10
	if limit > lowQuant[8]*int32(b.scaleFactor) {
		i = 9
	}
	for i < 29 && limit > lowQuant[i]*int32(b.scaleFactor) {
		i++
	}
	if diff < 0 {
		if i < 2 {
			return 63 - i
		}
		return 33 - i
	}
	return 61 - i
}
