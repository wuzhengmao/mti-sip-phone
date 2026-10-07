package core

import (
	"fmt"
	"image"
	"sync"
)

// 视频帧出口与网页订阅状态（REQ-VIDEO-009）。

// videoDemux 稳定的视频帧多播出口。
//
// sip 层在通话开始时**取一次句柄并捕获进闭包**（见 internal/sip 的
// videoSinkFor → SetFrameSink），之后不再更换，所以对外返回的出口对象必须
// 恒定：界面层走主槽（SetVideoSink），网页视频中枢等附加订阅者走 AddVideoSink，
// 增删订阅者都不需要重新取出口。
//
// 历史坑：早期是单槽，第二方注册会把第一方顶掉——wsapi.New 早于 ui.New，
// 网页侧若占用主槽会被界面覆盖，画面两头都收不到。
//
// ⚠️ OnVideoFrame 在**媒体层 goroutine 上同步调用**，逐个回调所有订阅者，
// 任何一个实现都不得阻塞（见 VideoSink 接口注释），否则解码循环卡住、
// UDP 接收缓冲溢出，之后持续花屏。
type videoDemux struct {
	mu      sync.RWMutex
	primary VideoSink   // 主槽：界面层
	extra   []VideoSink // 附加订阅者：网页视频中枢等
}

// setPrimary 设置主槽（界面层注册）。
func (d *videoDemux) setPrimary(s VideoSink) {
	d.mu.Lock()
	d.primary = s
	d.mu.Unlock()
}

// hasPrimary 是否注册了主槽（桌面界面在不在）。
func (d *videoDemux) hasPrimary() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.primary != nil
}

// add 追加一个附加订阅者。
func (d *videoDemux) add(s VideoSink) {
	if s == nil {
		return
	}
	d.mu.Lock()
	d.extra = append(d.extra, s)
	d.mu.Unlock()
}

// remove 移除附加订阅者（不存在时为空操作）。
func (d *videoDemux) remove(s VideoSink) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, x := range d.extra {
		if x == s {
			d.extra = append(d.extra[:i], d.extra[i+1:]...)
			return
		}
	}
}

// OnVideoFrame 把帧分发给主槽与全部附加订阅者。
// 锁内只取快照，回调在锁外执行——订阅者可能要做编码等耗时动作，
// 不能占着锁调用，也不能影响后续订阅者的注册。
func (d *videoDemux) OnVideoFrame(callID string, local bool, img *image.RGBA) {
	d.mu.RLock()
	primary := d.primary
	var extra []VideoSink
	if len(d.extra) > 0 {
		extra = append(extra, d.extra...) // 复制，避免锁外读到被并发修改的切片
	}
	d.mu.RUnlock()

	if primary != nil {
		primary.OnVideoFrame(callID, local, img)
	}
	for _, s := range extra {
		s.OnVideoFrame(callID, local, img)
	}
}

// WebVideoState 网页端视频订阅状态（REQ-VIDEO-009）。
// 网页订阅了视频画面时，软电话默认**不再显示视频窗口**（画面在网页看）；
// 订阅方显式要求 keepWindow 时（REQ-VIDEO-009 修订）才保持窗口继续显示。
type WebVideoState struct {
	Active  bool `json:"active"`  // 有网页正在接收视频画面
	Local   bool `json:"local"`   // 有网页订阅本地画面
	Remote  bool `json:"remote"`  // 有网页订阅远端画面
	Viewers int  `json:"viewers"` // 当前订阅的连接数
	// HideWindow 是否要隐藏软电话的视频窗口：**任一**订阅者没有要求"保持窗口
	// 显示"（keepWindow=false，默认）就为真——只有所有订阅者都显式要求保持时才
	// 不隐藏。界面据此决定视频窗口的显隐。
	HideWindow bool `json:"hideWindow"`
}

// webViewer 单个连接（连接级，多连接取并集）的订阅意图。
type webViewer struct {
	local      bool
	remote     bool
	keepWindow bool // 订阅后是否仍保持软电话视频窗口显示（默认 false = 隐藏）
}

// SetWebVideoViewer 登记/更新一个网页连接的视频订阅（id 用连接标识，见 wsapi）。
// 状态变化时发布 video.web 事件（界面据此显隐视频窗口）。
func (a *App) SetWebVideoViewer(id uint64, local, remote, keepWindow bool) {
	a.mu.Lock()
	old := a.webVideoStateLocked()
	if a.webViewers == nil {
		a.webViewers = make(map[uint64]webViewer)
	}
	a.webViewers[id] = webViewer{local: local, remote: remote, keepWindow: keepWindow}
	st := a.webVideoStateLocked()
	a.mu.Unlock()
	if st != old {
		a.bus.Publish("video.web", st)
	}
}

// RemoveWebVideoViewer 注销一个网页连接的视频订阅（连接断开时调用）。
func (a *App) RemoveWebVideoViewer(id uint64) {
	a.mu.Lock()
	old := a.webVideoStateLocked()
	if _, ok := a.webViewers[id]; !ok {
		a.mu.Unlock()
		return
	}
	delete(a.webViewers, id)
	st := a.webVideoStateLocked()
	a.mu.Unlock()
	if st != old {
		a.bus.Publish("video.web", st)
	}
}

// LocalVideoWanted 是否有网页订阅了本地画面——界面回显之外的本地帧消费者。
//
// 媒体层逐帧调用它决定要不要做 YUV→RGBA 转换（见 media.VideoEngine.
// SetLocalWantedFunc）：**关闭界面回显不该影响网页订阅**，只要还有网页在看
// 本地画面就得继续投递（REQ-VIDEO-009）。
func (a *App) LocalVideoWanted() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, v := range a.webViewers {
		if v.local {
			return true
		}
	}
	return false
}

// HasDesktopVideoSink 是否注册了桌面视频帧出口（界面层）。
//
// 只有完整版（Fyne 界面）会注册主槽；仅托盘无 UI 版与 headless 都没有窗口，
// 本地画面的唯一消费者是网页订阅。电话层据此决定媒体引擎的**回显默认值**：
// 没有桌面出口时回显默认关——否则采集循环会每帧白做一次 YUV→RGBA 分配
// （640×480×25fps ≈ 30MB/s），而转换结果只会被静默丢弃。
// 网页订阅本地画面不受影响（走 LocalVideoWanted）。
func (a *App) HasDesktopVideoSink() bool { return a.video.hasPrimary() }

// ---------------------------------------------------------------------------
// 显示用的旋转 / 水平镜像（REQ-VIDEO-012）
// ---------------------------------------------------------------------------

// VideoTransform 一条通道的旋转/水平镜像。
type VideoTransform struct {
	Rotation int  `json:"rotation"` // 0/90/180/270（顺时针，90/270 宽高互换）
	Mirror   bool `json:"mirror"`   // 水平镜像
}

// VideoTransforms 远端画面与本地回显各自的变换状态。
//
// **存在软电话侧、两端共用一份**（用户要求）：桌面视频窗口的按钮与网页的按钮
// 改的是同一份状态，互相同步；订阅/退订、网页断线重连都不丢。
type VideoTransforms struct {
	Remote VideoTransform `json:"remote"` // 远端画面
	Local  VideoTransform `json:"local"`  // 本地回显
}

// VideoChannelRemote / VideoChannelLocal video.setTransform 的通道名。
const (
	VideoChannelRemote = "remote"
	VideoChannelLocal  = "local"
)

// VideoTransforms 当前变换状态快照。
func (a *App) VideoTransforms() VideoTransforms {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.videoTx
}

// RotateVideo 指定通道顺时针 +90°（90/270 时宽高互换），返回新状态。
func (a *App) RotateVideo(channel string) (VideoTransforms, error) {
	return a.setVideoTransform(channel, func(t *VideoTransform) {
		t.Rotation = (t.Rotation + 90) % 360
	})
}

// SetVideoRotation 直接设定旋转角度（取模到 0/90/180/270 的整圈值）。
func (a *App) SetVideoRotation(channel string, deg int) (VideoTransforms, error) {
	deg = ((deg % 360) + 360) % 360
	deg = (deg / 90) * 90
	return a.setVideoTransform(channel, func(t *VideoTransform) { t.Rotation = deg })
}

// SetVideoMirror 指定通道的水平镜像开关，返回新状态。
func (a *App) SetVideoMirror(channel string, on bool) (VideoTransforms, error) {
	return a.setVideoTransform(channel, func(t *VideoTransform) { t.Mirror = on })
}

// setVideoTransform 改一条通道的变换并发 video.transform 事件（变化才发）。
func (a *App) setVideoTransform(channel string, fn func(*VideoTransform)) (VideoTransforms, error) {
	a.mu.Lock()
	var t *VideoTransform
	switch channel {
	case VideoChannelRemote, "":
		t = &a.videoTx.Remote
	case VideoChannelLocal:
		t = &a.videoTx.Local
	default:
		a.mu.Unlock()
		return VideoTransforms{}, fmt.Errorf("未知通道: %s（应为 remote/local）", channel)
	}
	old := *t
	fn(t)
	st := a.videoTx
	a.mu.Unlock()
	if *t != old {
		a.bus.Publish("video.transform", st)
	}
	return st, nil
}

// WebVideoState 当前网页订阅状态快照。
func (a *App) WebVideoState() WebVideoState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.webVideoStateLocked()
}

// webVideoStateLocked 汇总所有连接的订阅（调用方需持有 a.mu）。
//
// HideWindow：任一订阅者没要求保持窗口 → 隐藏（默认行为）；只有**所有**订阅者
// 都显式 keepWindow 时才不隐藏——多页面同时订阅时，只要有一个页面在看画面，
// 窗口就该让位（避免"双向都在显示"的糊涂状态）。
func (a *App) webVideoStateLocked() WebVideoState {
	st := WebVideoState{Viewers: len(a.webViewers)}
	hiding := false
	for _, v := range a.webViewers {
		st.Local = st.Local || v.local
		st.Remote = st.Remote || v.remote
		if !v.keepWindow {
			hiding = true
		}
	}
	st.Active = st.Local || st.Remote
	st.HideWindow = st.Active && hiding
	return st
}
