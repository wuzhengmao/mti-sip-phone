package audio

// 音频预处理的分帧层（REQ-MEDIA-007）：WebRTC APM 只吃**定长 10ms 帧**，
// 而音频回调给的是任意帧长（WASAPI 共享模式由设备周期决定，可能是 480、
// 441、1024…），所以这里做累积/切帧。纯 Go、无 cgo，单独可测。

// apmFramer 把任意长度的采集/播放样本切成一帧一帧交给 process，
// 处理结果按输入节奏回吐（不足一帧的尾巴留在内部，下一拍再补）。
//
// 延迟：输出样本比输入滞后不到一帧（<10ms）——采样本就要等满一帧才能处理，
// 这点延迟对通话无影响，换来的是 APM 要求的定长帧。
type apmFramer struct {
	frame  int     // 一帧样本数（10ms）
	accMic []int16 // 采集累积缓冲
	accRef []int16 // 播放（参考）累积缓冲
	accN   int     // 已累积样本数（两路同步）
	out    []int16 // 已处理、待回吐的样本
	outN   int     // 待回吐样本数
}

// newAPMFramer frame 为一帧样本数（采样率/100），maxFrames 为单次回调可能给出的
// 最大样本数（缓冲按"余量 + 最大回调"预留，回调里不做任何分配）。
func newAPMFramer(frame, maxFrames int) *apmFramer {
	cap := maxFrames + frame
	return &apmFramer{
		frame:  frame,
		accMic: make([]int16, cap),
		accRef: make([]int16, cap),
		out:    make([]int16, cap),
	}
}

// Filter 把 mic/ref 累积并处理，**就地**把已处理的采集样本写回 mic，
// 返回本次可用的样本数（0..len(mic)）。
//
// 调用方必须按返回值取材：`engine.FeedMic(mic[:n])`。返回值小于 len(mic) 时
// 多出来的尾巴是尚未处理完的输入，不能直接送出去（那会把原始采集音也发一遍）。
func (f *apmFramer) Filter(mic, ref []int16, process func(micFrame, refFrame []int16)) int {
	n := len(mic)
	if len(ref) < n {
		n = len(ref)
	}
	// 缓冲用满（理论上不会：容量按最大回调 + 一帧预留）时截断，宁丢不错位
	if room := len(f.accMic) - f.accN; n > room {
		n = room
	}
	if n > 0 {
		copy(f.accMic[f.accN:], mic[:n])
		copy(f.accRef[f.accN:], ref[:n])
		f.accN += n
	}

	// 满一帧就处理一帧
	for f.accN >= f.frame {
		process(f.accMic[:f.frame], f.accRef[:f.frame])
		copy(f.out[f.outN:], f.accMic[:f.frame])
		f.outN += f.frame
		copy(f.accMic, f.accMic[f.frame:f.accN])
		copy(f.accRef, f.accRef[f.frame:f.accN])
		f.accN -= f.frame
	}

	// 回吐已处理样本（就地覆盖输入）
	m := f.outN
	if m > len(mic) {
		m = len(mic)
	}
	if m > 0 {
		copy(mic[:m], f.out[:m])
		copy(f.out, f.out[m:f.outN])
		f.outN -= m
	}
	return m
}
