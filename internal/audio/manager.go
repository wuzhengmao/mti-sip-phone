// Package audio 管理音频设备：枚举、麦克风/扬声器选择、与媒体引擎对接。
//
// 后端实现按构建标签切换：
//   - 默认（无标签）：malgo（miniaudio，全双工 + 设备枚举，需 CGO）
//   - -tags noaudio：空后端（无音频设备，供纯 Go 构建与测试）
//
// 线程模型（重要，改动前先读）：
//   - 后端初始化在后台 goroutine 进行，NewManager 最多等待 initWait：
//     miniaudio 连接音频服务时是**同步且不可中断**的（环境变量
//     PULSE_SERVER 指向不可达地址时实测阻塞 15 秒），不能让启动路径与
//     界面线程被它拖住；
//   - 所有会碰后端的操作（开关音频流、播放提示音）都排入单一 worker 的
//     FIFO 队列后**立即返回**：顺序语义与同步调用一致（例如 StopRing 之后
//     PlayHangup 不会颠倒），同时 UI 线程、SIP 线程与信令流程都不会被
//     设备操作阻塞；
//   - **不要持锁调用后端**：设备操作可能阻塞数十秒，持锁会让 UI 线程的
//     enqueue / SetMic 反过来被卡住。锁只用于读写本结构的状态，调用后端
//     前先取快照并释放锁；
//   - 设备枚举 Devices 保持同步（枚举同样可能慢，例如 ALSA 逐个探测设备），
//     由调用方放到后台 goroutine 执行。
package audio

import (
	"sync"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/media"
)

const (
	// initWait 启动时最多等待后端初始化的时间。正常环境初始化在百毫秒内
	// 完成；超时说明音频服务不可达，此时先继续启动，就绪后经 OnReady 通知
	// 界面刷新设备列表。
	initWait = 2 * time.Second
	// closeWait 退出时最多等待命令队列排空的时间（设备操作可能长时间阻塞，
	// 超时则直接释放后端，不拖住退出）。
	closeWait = 2 * time.Second
	// cmdQueueLen 命令队列长度：一次通话只有几个音频操作，正常不会触及。
	cmdQueueLen = 32
)

// Manager 音频设备管理：全局单例，一次仅支持一路通话的音频流。
type Manager struct {
	mu            sync.Mutex
	micID         string     // 当前选择的麦克风 ID，空 = 系统默认
	speakerID     string     // 当前选择的扬声器 ID，空 = 系统默认
	ringSpeakerID string     // 振铃音专用扬声器 ID，空 = 系统默认
	preprocess    Preprocess // 音频预处理开关（REQ-MEDIA-007），随通话流生效
	engine        *media.Engine
	backend       Backend
	running       bool // 音频流是否已启动
	streamRate    int  // 已启动的音频流用的采样率（0 = 未启动）
	initDone      bool // 后端初始化是否已结束（成功或失败）
	closed        bool

	cmds       chan func()
	workerDone chan struct{}
	readyCh    chan struct{}
	onReady    []func() // 就绪回调（可注册多个：界面刷新设备列表 + 网页 audio.ready 事件）
	closeOnce  sync.Once
}

// NewManager 创建音频管理器：后端在后台初始化，最多等待 initWait。
// 后端不可用时返回的管理器依然可用——设备列表为空、音频操作被跳过
// （与"无音频设备"时的行为一致），就绪后经 OnReady 通知调用方。
func NewManager() *Manager {
	m := &Manager{
		cmds:       make(chan func(), cmdQueueLen),
		workerDone: make(chan struct{}),
		readyCh:    make(chan struct{}),
	}
	go m.runWorker()
	go m.initBackend()

	select {
	case <-m.readyCh:
	case <-time.After(initWait):
		logx.Logger().Warn("等待音频后端初始化超时，先继续启动",
			"wait", initWait,
			"hint", "音频服务不可达时优先检查 PULSE_SERVER / XDG_RUNTIME_DIR 等环境变量")
	}
	return m
}

// initBackend 后台初始化后端（miniaudio），完成后通知 OnReady 回调。
func (m *Manager) initBackend() {
	start := time.Now()
	backend, err := newBackend()

	m.mu.Lock()
	if err == nil {
		m.backend = backend
	}
	m.initDone = true
	engine := m.engine // 初始化期间已挂上的引擎：就绪后补启动
	cbs := m.onReady
	m.mu.Unlock()

	if err != nil {
		logx.Logger().Warn("音频后端初始化失败，音频功能不可用",
			"err", err, "elapsed", time.Since(start))
	} else {
		logx.Logger().Info("音频后端已初始化（miniaudio）", "elapsed", time.Since(start))
	}
	close(m.readyCh)

	if err == nil && engine != nil {
		// 初始化期间接通的通话：补启动音频流（队列保证与后续操作顺序一致）
		m.enqueue(func() { m.startStreamFor(engine) })
	}
	for _, cb := range cbs {
		if cb != nil {
			cb()
		}
	}
}

// OnReady 注册后端初始化结束（成功或失败）后的回调，每个注册者各调一次。
// 回调在后台 goroutine 中执行：涉及界面的操作请自行用 fyne.Do 包裹。
// 若注册时初始化已结束，回调会被立即调用。
//
// 可以注册多个（历史上只保留最后一个，界面那时是唯一消费者）：
// 仅托盘无 UI 版没有界面，网页靠 main 注册的这条回调发 audio.ready 事件
// 刷新设备列表，两者不能互相顶掉。
func (m *Manager) OnReady(fn func()) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	if m.initDone {
		m.mu.Unlock()
		fn()
		return
	}
	m.onReady = append(m.onReady, fn)
	m.mu.Unlock()
}

// Ready 后端是否可用（初始化成功）。
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.backend != nil
}

// runWorker 串行执行音频命令：设备操作可能长时间阻塞，放在这里不影响任何
// 调用方，同时保证执行顺序与入队顺序一致。
func (m *Manager) runWorker() {
	defer close(m.workerDone)
	for cmd := range m.cmds {
		cmd()
	}
}

// enqueue 把音频操作排入队列（非阻塞，不等待执行）。队列已关闭（退出流程）
// 时丢弃；队列满时丢弃并告警——正常使用不会触及上限，丢弃比阻塞调用方安全。
func (m *Manager) enqueue(cmd func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	select {
	case m.cmds <- cmd:
	default:
		logx.Logger().Warn("音频命令队列已满，丢弃一次音频操作")
	}
}

// SetMic 选择麦克风。空 ID 表示系统默认设备。
// 切换在后台生效（设备重建可能耗时），失败经日志体现。
func (m *Manager) SetMic(deviceID string) {
	m.mu.Lock()
	m.micID = deviceID
	restart := m.running
	m.mu.Unlock()
	logx.Logger().Info("切换麦克风", "device", deviceID)
	if restart {
		m.enqueue(m.restartStream)
	}
}

// SetSpeaker 选择扬声器。空 ID 表示系统默认设备。
func (m *Manager) SetSpeaker(deviceID string) {
	m.mu.Lock()
	m.speakerID = deviceID
	restart := m.running
	m.mu.Unlock()
	logx.Logger().Info("切换扬声器", "device", deviceID)
	if restart {
		m.enqueue(m.restartStream)
	}
}

// SetRingSpeaker 设置振铃音专用扬声器（只影响后续提示音，无需重建音频流）。
func (m *Manager) SetRingSpeaker(deviceID string) {
	m.mu.Lock()
	m.ringSpeakerID = deviceID
	m.mu.Unlock()
	logx.Logger().Info("振铃设备已切换", "device", deviceID)
}

// SetPreprocess 设置音频预处理开关（REQ-MEDIA-007）。
// 音频流在每次通话开始时重建，所以改动**从下一通（或下一次关流重开）生效**；
// 已经建立的通话不受影响（与设备选择的语义一致）。
func (m *Manager) SetPreprocess(p Preprocess) {
	m.mu.Lock()
	m.preprocess = p
	m.mu.Unlock()
}

// Devices 返回麦克风与扬声器列表；后端未就绪时返回空列表。
// 枚举可能耗时（ALSA 逐个探测设备），调用方应在后台 goroutine 中调用。
func (m *Manager) Devices() ([]core.AudioDevice, []core.AudioDevice) {
	m.mu.Lock()
	backend := m.backend
	m.mu.Unlock()
	if backend == nil {
		return nil, nil
	}
	return backend.Devices()
}

// AttachEngine 通话建立时对接媒体引擎：后台启动全双工音频流。
// 不阻塞调用方（接听/呼叫流程先走信令，音频设备在队列里打开）。
func (m *Manager) AttachEngine(engine *media.Engine) {
	m.mu.Lock()
	m.engine = engine
	m.mu.Unlock()
	if engine != nil {
		// 收到对端的 DTMF：对端发的是事件包不是音频，本机要自己发声
		//（真机也是这么做的），否则用户什么都听不到（REQ-CALL-006）。
		// 扬声器静音时不出声（那是"停止本地播放"的语义）。
		engine.SetDTMFListener(func(digit string) {
			if !engine.PlayMuted() {
				m.PlayDTMF(digit)
			}
		})
		// 协商换到不同采样率的编解码器（G.711 ⇄ G.722）：设备已按旧速率开流时
		// 关流重开。回调在协商线程上跑，只做"排队"（不阻塞信令）。
		engine.SetSampleRateListener(func(int) {
			m.enqueue(func() { m.restartStreamIfRateChanged(engine) })
		})
	}
	m.enqueue(func() { m.startStreamFor(engine) })
}

// DetachEngine 通话结束时停止音频流。不阻塞调用方（挂断流程先走信令与
// 界面收尾，设备在队列里关闭）。
func (m *Manager) DetachEngine() {
	m.mu.Lock()
	m.engine = nil
	m.mu.Unlock()
	m.enqueue(m.stopStream)
}

// PlayRing 播放振铃音（循环），通话事件驱动；使用振铃专用扬声器（若已配置）。
func (m *Manager) PlayRing() {
	m.enqueue(func() { m.playTone(ringtone, true, true) }) // 来电振铃：振铃设备
}

// PlayHangup 播放挂断提示音（一次）。
func (m *Manager) PlayHangup() {
	m.enqueue(func() { m.playTone(hangup, false, false) }) // 挂断提示音：扬声器（用户要求）
}

// PlayRingback 播放**本地回铃音**（循环）：呼出时没有早期媒体（对端没下发
// 回铃音）才播；对端一旦接通或回早期媒体，UI 会调 StopRing 停掉。
func (m *Manager) PlayRingback() {
	m.enqueue(func() { m.playTone(ringback, true, false) }) // 本地回铃音：扬声器（用户要求）
}

// PlayBusy 播放遇忙音（一次）：对端回 486 Busy Here / 600 Busy Everywhere 时播。
func (m *Manager) PlayBusy() {
	m.enqueue(func() { m.playTone(busy, false, false) }) // 遇忙音：扬声器（同回铃音）
}

// PlayDTMF 播放按键回显音（REQ-UI-029）：拨号盘按钮 / 主窗体敲数字时本机响一声。
//
// 走**独立的提示音播放设备**（同回铃音/遇忙音，目标 = 当前选择的扬声器），
// 不复用通话的媒体流，所以通话中按键也能听到回显；连续按键会打断上一个音
// （后端按"同一设备复用 + 空闲自动关闭"处理，见 backend_malgo.go 的 PlayTone）。
func (m *Manager) PlayDTMF(digit string) {
	tone := dtmfTones[digit]
	if tone == nil {
		return
	}
	m.enqueue(func() {
		m.mu.Lock()
		backend := m.backend
		device := m.speakerID
		m.mu.Unlock()
		if backend == nil {
			return
		}
		if device == "" {
			device = "系统默认"
		}
		// 按键音不循环、不写 Info 日志（一次拨号会有十几次按键，避免刷日志）
		if err := backend.PlayTone(tone, false, device); err != nil {
			logx.Logger().Warn("播放按键音失败", "err", err)
		}
	})
}

// StopRing 停止提示音（振铃结束/通话建立时）。
func (m *Manager) StopRing() {
	m.enqueue(func() {
		m.mu.Lock()
		backend := m.backend
		m.mu.Unlock()
		if backend == nil {
			return
		}
		backend.StopTone()
	})
}

// Close 释放后端资源：停止音频流并关闭后端。
// 最多等待命令队列排空 closeWait——设备操作可能长时间阻塞，超时则直接
// 释放（退出流程不能被音频拖住）。
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		close(m.cmds)
		m.mu.Unlock()

		select {
		case <-m.workerDone:
		case <-time.After(closeWait):
			logx.Logger().Warn("等待音频命令队列排空超时，直接释放后端", "wait", closeWait)
		}

		m.stopStream()

		m.mu.Lock()
		backend := m.backend
		m.backend = nil
		m.mu.Unlock()
		if backend != nil {
			backend.Close()
		}
	})
}

// startStreamFor 队列命令：为指定引擎启动音频流（引擎已被后续通话取代时跳过）。
func (m *Manager) startStreamFor(engine *media.Engine) {
	m.mu.Lock()
	if m.running || m.engine != engine || m.backend == nil || engine == nil {
		m.mu.Unlock()
		return
	}
	backend, mic, speaker := m.backend, m.micID, m.speakerID
	rate := engineSampleRate(engine)
	m.running = true // 先置位，避免与 restart/stop 重复启动
	m.mu.Unlock()

	// 不持锁调用后端：设备打开可能阻塞数十秒
	if err := backend.Start(mic, speaker, engine, m.preprocess); err != nil {
		m.mu.Lock()
		m.running = false
		m.streamRate = 0
		m.mu.Unlock()
		logx.Logger().Error("启动音频流失败", "err", err, "采样率", rate)
		return
	}
	m.mu.Lock()
	m.streamRate = rate
	m.mu.Unlock()
	logx.Logger().Info("音频流已启动", "mic", mic, "speaker", speaker, "采样率", rate)
}

// stopStream 队列命令：停止音频流。
func (m *Manager) stopStream() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	backend := m.backend
	m.running = false
	m.streamRate = 0
	m.mu.Unlock()

	if backend == nil {
		return
	}
	backend.Stop() // 不持锁：设备关闭同样可能阻塞
	logx.Logger().Debug("音频流已停止")
}

// restartStreamIfRateChanged 队列命令：引擎的采样率与当前音频流不一致时
// 关流重开（协商换到不同速率的编解码器，REQ-MEDIA-011）。
//
// 只在**真的不一致**时才重开：保持/取消保持这类 re-INVITE 也会触发采样率
// 回调，但速率没变，不能白重开一次设备（会有可听的断点）。速率在队列里
// 连变两次时也会自动合并（执行时比较的是"当前值"）。
func (m *Manager) restartStreamIfRateChanged(engine *media.Engine) {
	m.mu.Lock()
	ok := m.engine == engine && m.running && m.backend != nil
	cur := m.streamRate
	m.mu.Unlock()
	if !ok || engineSampleRate(engine) == cur {
		return
	}
	m.restartStream()
}

// restartStream 队列命令：设备切换时重建音频流。
func (m *Manager) restartStream() {
	m.mu.Lock()
	engine := m.engine
	running := m.running
	m.mu.Unlock()
	if !running || engine == nil {
		return
	}
	m.stopStream()
	m.startStreamFor(engine)
}

// playTone 队列命令：播放提示音。
//
// useRingDevice=true 走**振铃专用扬声器**——**只有来电振铃走它**（便于单独接
// 一台铃音箱）；false 走**当前选择的扬声器**（挂断提示音、本地回铃音、遇忙音
// ——用户要求这几类都走扬声器，不走振铃设备）。
// 设备 ID 为空都表示系统默认设备。
func (m *Manager) playTone(tone *wavPCM, loop bool, useRingDevice bool) {
	m.mu.Lock()
	backend := m.backend
	device := m.speakerID
	if useRingDevice {
		device = m.ringSpeakerID
	}
	m.mu.Unlock()
	if backend == nil {
		return
	}
	if device == "" {
		device = "系统默认"
	}
	logx.Logger().Info("播放提示音", "loop", loop, "目标设备", device, "振铃设备", useRingDevice)
	if err := backend.PlayTone(tone, loop, device); err != nil {
		logx.Logger().Warn("播放提示音失败", "err", err)
	}
}
