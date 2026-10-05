package media

// 媒体诊断探针：默认关闭，**零开销**；排查音频/媒体问题时用环境变量
//
//	SIP_PHONE_AUDIODUMP=1
//
// 打开（Windows 命令行：`set SIP_PHONE_AUDIODUMP=1 && dist\sip-phone-windows-amd64.exe`），
// 每通电话会额外产出：
//
//   - 日志里每秒一行「【诊断】媒体汇总」：收包数、写入音频样本数、补（PLC/静音）
//     样本数、播放回调次数、回调请求/实际读出的样本数、缓冲深度、累计截断；
//   - 日志目录下两份 WAV：`rx-<RTP端口>.wav`（送到播放缓冲的全部样本，含补的
//     静音，即"网络上收到的声音"）与 `play-<RTP端口>.wav`（音频设备实际拿到的
//     样本，即"用户听到的声音"）。两份按到达/读取顺序拼接，直接听或跑
//     `python tools/wavscan.py <wav>` 看时间轴与波形跳变。
//
// 判读要点（实战用过两次，都是靠它定位的）：
//   - 「写入」应≈采样率 样本/秒（8kHz 或 16kHz 实时）。远高于它 = 对端媒体成批涌入；
//   - 「回调请求」与「实际读出」不等 = 播放缓冲被读空（欠载），差值就是补静音的量；
//   - 「播放回调次数」应≈50/s。明显偏低 = 音频线程被卡（进程忙/被日志轮转等拖住）；
//   - 「深度」长期顶在上限、伴随「累计截断」增长 = 缓冲在丢音频（回铃音断续的
//     直接来源，配合 play WAV 能看到被切掉的音调）。
//
// 采集侧写盘一律在后台 goroutine（write 只投递，不阻塞音频回调），避免探针
// 自己干扰被观测的时序。

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mti-sip-phone/internal/logx"
)

const (
	probeWAVSeconds = 60 // 每份 WAV 最多 60 秒（换算成样本要乘当前采样率）
	probePeriod        = time.Second
	probeMaxSeconds    = 30 // 媒体开始后最多汇总 30 秒（前 30 秒是问题高发段）
)

type mediaProbe struct {
	port    int
	rx      *wavDump
	play    *wavDump
	mic     *wavDump // 送编码器的采集样本（含麦克风增益，REQ-MEDIA-006）
	startAt time.Time
	second  int
	rxAudio int64 // 窗口内：真实音频样本（帧解码结果）
	rxFill  int64 // 窗口内：丢包补的 PLC/静音样本
	rdCalls int64 // 窗口内：播放回调次数
	rdReq   int64 // 窗口内：回调请求的样本数
	rdGot   int64 // 窗口内：实际从缓冲取到的样本数
	rxPkts  int64 // 窗口内：收到的 RTP 包数
	// 采集侧电平（判断"麦克风增益到底有没有生效/是不是已经削波"）：
	// 窗口内的绝对峰值与均方根
	micSamples int64
	micPeak    int64
	micSumSq   int64
	// 累计量（通话结束时汇总一行）
	totRx, totFill, totRead, totReq int64

	eng  *Engine
	stop chan struct{}
	done chan struct{}
}

func newMediaProbe(port int) *mediaProbe {
	if os.Getenv("SIP_PHONE_AUDIODUMP") == "" {
		return nil
	}
	dir := logDir()
	cleanOldDumps(dir, port)
	p := &mediaProbe{port: port, stop: make(chan struct{}), done: make(chan struct{})}
	if w, err := newWavDump(filepath.Join(dir, fmt.Sprintf("rx-%d.wav", port))); err == nil {
		p.rx = w
	} else {
		logx.Logger().Warn("诊断 WAV（rx）创建失败", "err", err)
	}
	if w, err := newWavDump(filepath.Join(dir, fmt.Sprintf("play-%d.wav", port))); err == nil {
		p.play = w
	} else {
		logx.Logger().Warn("诊断 WAV（play）创建失败", "err", err)
	}
	if w, err := newWavDump(filepath.Join(dir, fmt.Sprintf("mic-%d.wav", port))); err == nil {
		p.mic = w
	} else {
		logx.Logger().Warn("诊断 WAV（mic）创建失败", "err", err)
	}
	logx.Logger().Info("【诊断】媒体探针已启用，WAV 输出到", "dir", dir, "rtpPort", port)
	return p
}

// setRate 采样率变化（协商到 16 kHz 编解码器）时同步三份 WAV 的速率。
func (p *mediaProbe) setRate(rate int) {
	if p == nil {
		return
	}
	p.rx.setRate(rate)
	p.play.setRate(rate)
	p.mic.setRate(rate)
}

// cleanOldDumps 清掉上一次诊断留下的 WAV（端口每通电话都不同，不清会越攒越多；
// 诊断文件只保留最近一次通话的，够用了）。
func cleanOldDumps(dir string, keepPort int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		// 只动探针产物：rx-/play-/mic-<端口>.wav
		isDump := strings.HasPrefix(name, "rx-") || strings.HasPrefix(name, "play-") ||
			strings.HasPrefix(name, "mic-")
		if !isDump || !strings.HasSuffix(name, ".wav") {
			continue
		}
		if name == fmt.Sprintf("rx-%d.wav", keepPort) || name == fmt.Sprintf("play-%d.wav", keepPort) ||
			name == fmt.Sprintf("mic-%d.wav", keepPort) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// logDir 与 logx 相同的日志目录（诊断文件与日志放一起）。
func logDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return "."
	}
	return filepath.Join(d, "mti-sip-phone", "logs")
}

// start 启动每秒汇总。
func (p *mediaProbe) start(e *Engine) {
	if p == nil {
		return
	}
	p.eng = e
	p.startAt = time.Now()
	go p.loop()
}

func (p *mediaProbe) loop() {
	defer close(p.done)
	t := time.NewTicker(probePeriod)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.report()
			p.second++
			if p.second >= probeMaxSeconds {
				return
			}
		}
	}
}

func (p *mediaProbe) report() {
	el := time.Since(p.startAt)
	rxAudio := atomic.SwapInt64(&p.rxAudio, 0)
	rxFill := atomic.SwapInt64(&p.rxFill, 0)
	rdCalls := atomic.SwapInt64(&p.rdCalls, 0)
	rdReq := atomic.SwapInt64(&p.rdReq, 0)
	rdGot := atomic.SwapInt64(&p.rdGot, 0)
	rxPkts := atomic.SwapInt64(&p.rxPkts, 0)
	micN := atomic.SwapInt64(&p.micSamples, 0)
	micPeak := atomic.SwapInt64(&p.micPeak, 0)
	micSumSq := atomic.SwapInt64(&p.micSumSq, 0)

	depth, trim := 0, 0
	if p.eng != nil {
		p.eng.playMu.Lock()
		depth, trim = p.eng.ring.Depth(), p.eng.ring.Trimmed()
		p.eng.playMu.Unlock()
	}
	// 采集侧电平：峰值贴着 32767 说明已经削波（此时再增益也只会更糊，不会更响）；
	// 峰值很低（<1000）说明话筒输入本身太小，增益才有效果。
	micRMS := int64(0)
	if micN > 0 {
		micRMS = int64(math.Sqrt(float64(micSumSq) / float64(micN)))
	}
	gainPct := 0
	if p.eng != nil {
		gainPct = int(p.eng.MicGain()*100 + 0.5)
	}
	logx.Logger().Info("【诊断】媒体汇总",
		"启动后ms", el.Milliseconds(),
		"收包", rxPkts,
		"写入音频样本/s", rxAudio,
		"补静音样本/s", rxFill,
		"播放回调次数/s", rdCalls,
		"回调请求样本/s", rdReq,
		"实际读出样本/s", rdGot,
		"深度", depth,
		"累计截断", trim,
		"采集样本/s", micN,
		"采集峰值", micPeak,
		"采集RMS", micRMS,
		"麦克风增益%", gainPct)
}

// writeRx 记录并落盘真实音频样本（帧解码结果）。
func (p *mediaProbe) writeRx(s []int16) {
	if p == nil {
		return
	}
	atomic.AddInt64(&p.rxAudio, int64(len(s)))
	atomic.AddInt64(&p.totRx, int64(len(s)))
	if p.rx != nil {
		p.rx.write(s)
	}
}

func (p *mediaProbe) writeFill(n int) {
	if p == nil {
		return
	}
	atomic.AddInt64(&p.rxFill, int64(n))
	atomic.AddInt64(&p.totFill, int64(n))
	if p.rx != nil {
		p.rx.write(make([]int16, n))
	}
}

func (p *mediaProbe) addRead(req, got int) {
	if p == nil {
		return
	}
	atomic.AddInt64(&p.rdCalls, 1)
	atomic.AddInt64(&p.rdReq, int64(req))
	atomic.AddInt64(&p.rdGot, int64(got))
	atomic.AddInt64(&p.totRead, int64(got))
	atomic.AddInt64(&p.totReq, int64(req))
}

func (p *mediaProbe) writePlay(s []int16) {
	if p == nil || p.play == nil {
		return
	}
	p.play.write(s)
}

// writeMic 记录并落盘**送编码器的采集样本**（已含麦克风增益，REQ-MEDIA-006），
// 同时统计电平：和 rx/play 两份 WAV 一起就能判断"是采集端太小、还是增益没生效、
// 还是已经削波"。
func (p *mediaProbe) writeMic(s []int16) {
	if p == nil || len(s) == 0 {
		return
	}
	// 本次调用的局部统计（音频回调线程）；跨线程汇总走原子量——与 rx/play 的
	// 计数同一套写法，别改成裸字段读写。
	var peak, sumSq int64
	for _, v := range s {
		a := int64(v)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
		sumSq += a * a
	}
	for {
		old := atomic.LoadInt64(&p.micPeak)
		if peak <= old || atomic.CompareAndSwapInt64(&p.micPeak, old, peak) {
			break
		}
	}
	atomic.AddInt64(&p.micSumSq, sumSq)
	atomic.AddInt64(&p.micSamples, int64(len(s)))
	if p.mic != nil {
		p.mic.write(s)
	}
}

func (p *mediaProbe) addPkt() {
	if p == nil {
		return
	}
	atomic.AddInt64(&p.rxPkts, 1)
}

func (p *mediaProbe) close() {
	if p == nil {
		return
	}
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	select {
	case <-p.done:
	case <-time.After(200 * time.Millisecond):
	}
	if p.rx != nil {
		p.rx.close()
	}
	if p.play != nil {
		p.play.close()
	}
	trim := 0
	if p.eng != nil {
		p.eng.playMu.Lock()
		trim = p.eng.ring.Trimmed()
		p.eng.playMu.Unlock()
	}
	logx.Logger().Info("【诊断】本次通话媒体合计",
		"rtpPort", p.port,
		"音频样本", atomic.LoadInt64(&p.totRx),
		"补静音样本", atomic.LoadInt64(&p.totFill),
		"回调请求样本", atomic.LoadInt64(&p.totReq),
		"实际读出样本", atomic.LoadInt64(&p.totRead),
		"累计截断样本", trim)
}

// ---------------------------------------------------------------------------
// WAV 落盘（16bit 单声道；采样率随编解码器，8kHz 或 16kHz）
// ---------------------------------------------------------------------------

type wavDump struct {
	f    *os.File
	path string
	ch   chan []int16
	done chan struct{}
	// mu/closed 保护 ch 的关闭：音频回调（write）与通话结束（close）是并发
	// 路径，向已关闭的通道发送会 panic——投递与关闭必须互斥。
	mu     sync.Mutex
	closed bool
	// bytes 由写盘 goroutine 更新、write 侧读取（都用原子量）
	bytes atomic.Int64
	buf   []byte
	// rate 本次通话的 PCM 采样率（Hz）：头部先在创建时按默认值写好，
	// 采样率变了/收尾时再回填（诊断 WAV 的时长与播放速度都靠它）。
	rate atomic.Int32
}

func newWavDump(path string) (*wavDump, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 0) // 结束时回填
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)     // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1)     // 单声道
	binary.LittleEndian.PutUint32(hdr[24:], 8000)  // 采样率
	binary.LittleEndian.PutUint32(hdr[28:], 16000) // 字节率
	binary.LittleEndian.PutUint16(hdr[32:], 2)     // 块对齐
	binary.LittleEndian.PutUint16(hdr[34:], 16)    // 位深
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], 0) // 结束时回填
	if _, err := f.Write(hdr); err != nil {
		f.Close()
		return nil, err
	}
	w := &wavDump{f: f, path: path, ch: make(chan []int16, 512), done: make(chan struct{})}
	w.rate.Store(defaultSampleRate)
	go w.loop()
	return w, nil
}

// loop 写盘 goroutine：磁盘 I/O 一律在这里做。
func (w *wavDump) loop() {
	defer close(w.done)
	for s := range w.ch {
		w.writeFile(s)
	}
}

// write 投递样本（非阻塞）：**不在调用方线程做磁盘 I/O**——音频回调里写盘会
// 把回调本身拖住，诊断工具就改变了被诊断对象的行为（回调被 I/O 阻塞 →
// WASAPI 丢周期 → 消费样本数变少，正是要排查的现象）。
func (w *wavDump) write(s []int16) {
	if w == nil || len(s) == 0 {
		return
	}
	if w.bytes.Load() >= w.maxBytes() {
		return
	}
	cp := make([]int16, len(s))
	copy(cp, s)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	select {
	case w.ch <- cp:
	default: // 写盘跟不上：丢这一帧，绝不阻塞采集/播放路径
	}
}

func (w *wavDump) writeFile(s []int16) {
	need := len(s) * 2
	if w.bytes.Load()+int64(need) > w.maxBytes() {
		return
	}
	if cap(w.buf) < need {
		w.buf = make([]byte, need)
	}
	b := w.buf[:need]
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	n, err := w.f.Write(b)
	if err != nil {
		return
	}
	w.bytes.Add(int64(n))
}

func (w *wavDump) close() {
	if w == nil || w.f == nil {
		return
	}
	w.mu.Lock()
	if w.closed { // 幂等：重复 close 不能重复关通道
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.ch)
	w.mu.Unlock()
	<-w.done // 等写盘 goroutine 收尾，再回填头
	n := w.bytes.Load()
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(36+n))
	_, _ = w.f.WriteAt(b[:], 4) // RIFF 大小
	binary.LittleEndian.PutUint32(b[:], uint32(n))
	_, _ = w.f.WriteAt(b[:], 40) // data 大小
	// 采样率回填（创建时按默认 8kHz 写的头，实际速率可能不同）
	binary.LittleEndian.PutUint32(b[:], uint32(w.Rate()))
	_, _ = w.f.WriteAt(b[:], 24) // 采样率
	binary.LittleEndian.PutUint32(b[:], uint32(w.Rate()*2))
	_, _ = w.f.WriteAt(b[:], 28) // 字节率
	_ = w.f.Close()
	logx.Logger().Info("【诊断】WAV 已写出", "file", w.path, "采样率", w.Rate(), "秒", n/(int64(w.Rate())*2))
}

// Rate 当前记录的采样率（Hz）。
func (w *wavDump) Rate() int { return int(w.rate.Load()) }

// setRate 更新采样率（协商到 G.722 等 16 kHz 编解码器时）。头部在 close 时
// 按最终速率回填；同一份 WAV 里跨采样率时以最终值为准（诊断尽力而为）。
func (w *wavDump) setRate(rate int) {
	if w == nil || rate <= 0 {
		return
	}
	w.rate.Store(int32(rate))
}

// maxBytes 本份 WAV 的字节上限（probeWAVSeconds 秒 × 采样率 × 2 字节）。
func (w *wavDump) maxBytes() int64 {
	return int64(w.Rate()) * probeWAVSeconds * 2
}
