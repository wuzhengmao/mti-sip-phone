package core

import (
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
// 任一网页连接订阅了视频画面，软电话就**不再显示视频窗口**（画面在网页看）。
type WebVideoState struct {
	Active  bool `json:"active"`  // 有网页正在接收视频画面
	Local   bool `json:"local"`   // 有网页订阅本地画面
	Remote  bool `json:"remote"`  // 有网页订阅远端画面
	Viewers int  `json:"viewers"` // 当前订阅的连接数
}

// webViewer 单个连接（连接级，多连接取并集）的订阅意图。
type webViewer struct {
	local  bool
	remote bool
}

// SetWebVideoViewer 登记/更新一个网页连接的视频订阅（id 用连接标识，见 wsapi）。
// 状态变化时发布 video.web 事件（界面据此显隐视频窗口）。
func (a *App) SetWebVideoViewer(id uint64, local, remote bool) {
	a.mu.Lock()
	old := a.webVideoStateLocked()
	if a.webViewers == nil {
		a.webViewers = make(map[uint64]webViewer)
	}
	a.webViewers[id] = webViewer{local: local, remote: remote}
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

// WebVideoState 当前网页订阅状态快照。
func (a *App) WebVideoState() WebVideoState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.webVideoStateLocked()
}

// webVideoStateLocked 汇总所有连接的订阅（调用方需持有 a.mu）。
func (a *App) webVideoStateLocked() WebVideoState {
	st := WebVideoState{Viewers: len(a.webViewers)}
	for _, v := range a.webViewers {
		st.Local = st.Local || v.local
		st.Remote = st.Remote || v.remote
	}
	st.Active = st.Local || st.Remote
	return st
}
