package g722

// 单个子带（低带 / 高带）的自适应量化器与预测器状态。
//
// 字段的整数宽度**刻意与参考实现一致**（int16 的地方就是 int16）：参考实现里
// 若干赋值会写进 16 位变量，超范围时按二进制补码截断——要让我们的输出与参考
// 实现逐位一致，就得保留同样的截断语义，不能统统换成 int32。
type band struct {
	sPredictor      int16    // 预测器输出（重建信号的预测值）
	sZero           int32    // 零极点预测器的输出
	partReconstMem  [2]int8  // 前两次"部分重建信号"的符号位（0/1）
	prevQtzdReconst int16    // 上一次量化重建信号（内部值，用 lowInvQuant4 标度）
	poleMem         [2]int16 // 二阶极点段的系数缓冲
	diffMem         [6]int32 // 量化差分信号记忆（7 阶零点段）
	zeroMem         [6]int16 // 7 阶零点段的系数缓冲
	logFactor       int16    // 延迟的对数标度因子
	scaleFactor     int16    // 延迟的量化器标度因子
}

// signLookup 与参考实现一致：下标 0 → -1，1 → +1。
var signLookup = [2]int32{-1, 1}

// reset 恢复标准规定的初始状态（低带 8、高带 2，其余为 0）。
// 两个子带初值不同，所以由调用方指定。
func (b *band) reset(scaleFactor int16) {
	*b = band{scaleFactor: scaleFactor}
}

// updateLow 低带的自适应预测器 + 量化器标度因子更新（标准 4.2 节）。
// ilow4 是**低带码字的高 4 位**（6 位码字 >> 2：预测器更新与标度因子表都按 4 位索引）。
func (b *band) updateLow(ilow4 int32) {
	b.adaptivePrediction(int32(b.scaleFactor) * lowInvQuant4[ilow4] >> 10)
	// 量化器标度因子自适应：指数衰减 + 本次码字对应的对数步长
	b.logFactor = int16(clipInt32((int32(b.logFactor)*127>>7)+lowLogFactorStep[ilow4], 0, 18432))
	b.scaleFactor = linearScaleFactor(int32(b.logFactor) - (8 << 11))
}

// updateHigh 高带的自适应预测器 + 量化器标度因子更新（标准 4.2 节）。
// dhigh 是本次码字对应的差分值（由调用方按**更新前**的 scaleFactor 算好）。
func (b *band) updateHigh(dhigh, ihigh int32) {
	b.adaptivePrediction(dhigh)
	b.logFactor = int16(clipInt32((int32(b.logFactor)*127>>7)+highLogFactorStep[ihigh&1], 0, 22528))
	b.scaleFactor = linearScaleFactor(int32(b.logFactor) - (10 << 11))
}

// adaptivePrediction 自适应预测器（标准 4.2 节的 pole/zero 两段）：
// 极点段（二阶，poleMem）与零点段（七阶记忆，diffMem/zeroMem）都在这里更新。
func (b *band) adaptivePrediction(curDiff int32) {
	// 部分重建信号的符号位（0/1）
	curPartReconst := int32(0)
	if b.sZero+curDiff < 0 {
		curPartReconst = 1
	}

	// 与上两次符号比较，得到极点段两个系数的符号
	sg0 := signLookup[boolToInt(curPartReconst != int32(b.partReconstMem[0]))]
	sg1 := signLookup[boolToInt(curPartReconst == int32(b.partReconstMem[1]))]
	b.partReconstMem[1] = b.partReconstMem[0]
	b.partReconstMem[0] = int8(curPartReconst)

	// 二阶极点段（第二个系数先更新：它的旧值参与第一个系数的限幅）
	b.poleMem[1] = int16(clipInt32(
		(sg0*clipInt32(int32(b.poleMem[0]), -8191, 8191)>>5)+(sg1*128)+(int32(b.poleMem[1])*127>>7),
		-12288, 12288))
	limit := 15360 - int32(b.poleMem[1])
	b.poleMem[0] = int16(clipInt32(-192*sg0+(int32(b.poleMem[0])*255>>8), -limit, limit))

	// 七阶零点段
	b.updateZero(curDiff)

	// 预测器输出 = 零点段输出 + 极点段（用上两次的量化重建信号）
	curQtzdReconst := clipInt16((int32(b.sPredictor) + curDiff) * 2)
	b.sPredictor = clipInt16(int32(b.sZero) +
		(int32(b.poleMem[0])*int32(curQtzdReconst) >> 15) +
		(int32(b.poleMem[1])*int32(b.prevQtzdReconst) >> 15))
	b.prevQtzdReconst = curQtzdReconst
}

// updateZero 七阶零点段（标准 4.2 节的 d(k) 记忆更新）。
// 数组是从高阶往低阶**依次移位**更新的，顺序不能改：每一步写入 diffMem[k] 的是
// 上一步读到的值。
func (b *band) updateZero(curDiff int32) {
	d := int32(0)
	if curDiff != 0 {
		d = 1
	}
	sZero := int32(0)
	for k := 5; k >= 0; k-- {
		var x int32
		if k == 0 {
			x = curDiff * 2
		} else {
			x = b.diffMem[k-1]
		}
		sign := int32(128)
		if (b.diffMem[k] ^ curDiff) < 0 {
			sign = -128
		}
		tmp := x
		b.zeroMem[k] = int16((int32(b.zeroMem[k])*255)>>8 + d*sign)
		b.diffMem[k] = tmp
		sZero += (tmp * int32(b.zeroMem[k])) >> 15
	}
	b.sZero = sZero
}

// linearScaleFactor 对数标度因子 → 线性标度因子（标准 4.2 节的 2^ 反变换）。
// logFactor 可为负（低带减 8<<11、高带减 10<<11 之后），移位方向随符号变。
func linearScaleFactor(logFactor int32) int16 {
	wd1 := invLog2Table[(logFactor>>6)&31]
	shift := logFactor >> 11
	if shift < 0 {
		return int16(wd1 >> -shift)
	}
	return int16(wd1 << shift)
}

// boolToInt true → 1，false → 0（用表下标模拟参考实现的三目表达式）。
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// clipInt32 把 v 限制到 [lo, hi]。
func clipInt32(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clipInt16 限制到 int16 范围（参考实现的 av_clip_int16）。
func clipInt16(v int32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// clipIntp2 限制到 [-2^bits, 2^bits-1]（参考实现的 av_clip_intp2 语义：
// 它的参数 p 是**指数**，不是位数——写成位数会少一半范围，解码输出会有 ±1
// 的偏差，golden 向量能当场抓出来）。
func clipIntp2(v int32, bits uint) int32 {
	lo := -int32(1) << bits
	hi := int32(1)<<bits - 1
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// applyQMF 对最近 24 个样本做正交镜像滤波（标准 4.2 节的 QMF 银行）。
// p 长度必须为 24、p[0] 最旧、p[23] 最新；返回 (xout[0], xout[1])。
// 调用方按用途决定移位（编码器 >>14，解码器 >>11）——与参考实现一致。
func applyQMF(p []int16) (int32, int32) {
	var x0, x1 int32
	for i := 0; i < 12; i++ {
		x1 += int32(p[2*i]) * qmfEven[i]
		x0 += int32(p[2*i+1]) * qmfOdd[i]
	}
	return x0, x1
}
