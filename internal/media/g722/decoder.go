package g722

// Decoder G.722 解码器（模式 1：64 kbit/s，低带 6 位 + 高带 2 位）。
// 不是并发安全的，调用方负责串行化。
type Decoder struct {
	band [2]band
	prev [prevSamplesBufSize]int16
	pos  int
}

// NewDecoder 创建解码器（初始状态与标准/参考实现一致：低带标度因子 8、
// 高带 2，延迟线预置 22 个零样本）。
func NewDecoder() *Decoder {
	d := &Decoder{}
	d.Reset()
	return d
}

// Reset 复位到初始状态。
func (d *Decoder) Reset() {
	d.band[0].reset(8)
	d.band[1].reset(2)
	for i := range d.prev {
		d.prev[i] = 0
	}
	d.pos = 22
}

// Decode 把码流解码成 16 kHz 单声道 PCM：len(data) 字节 → 2×len(data) 个样本。
// out 至少要有 2×len(data) 个样本位。
func (d *Decoder) Decode(data []byte, out []int16) {
	for j, cw := range data {
		ihigh := int32(cw >> 6)  // 高 2 位给高带
		ilow := int32(cw & 0x3f) // 低 6 位给低带
		b0, b1 := &d.band[0], &d.band[1]

		// 低带重建（14 位限幅）后再更新预测器
		rlow := clipIntp2((int32(b0.scaleFactor)*lowInvQuant6[ilow]>>10)+int32(b0.sPredictor), 14)
		b0.updateLow(ilow >> 2)

		// 高带重建
		dhigh := int32(b1.scaleFactor) * highInvQuant[ihigh] >> 10
		rhigh := clipIntp2(dhigh+int32(b1.sPredictor), 14)
		b1.updateHigh(dhigh, ihigh)

		// 两个子带样本合成回去：延迟线推入 (rlow+rhigh) 与 (rlow-rhigh)
		d.prev[d.pos] = int16(rlow + rhigh)
		d.pos++
		d.prev[d.pos] = int16(rlow - rhigh)
		d.pos++
		x0, x1 := applyQMF(d.prev[d.pos-24 : d.pos])
		out[2*j] = clipInt16(x0 >> 11)
		out[2*j+1] = clipInt16(x1 >> 11)

		if d.pos >= prevSamplesBufSize {
			copy(d.prev[:22], d.prev[d.pos-22:d.pos])
			d.pos = 22
		}
	}
}
