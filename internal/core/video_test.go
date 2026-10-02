package core

import (
	"image"
	"sync"
	"testing"

	"mti-sip-phone/internal/config"
)

// fakeVideoSink 记录收到的帧数。
type fakeVideoSink struct {
	mu     sync.Mutex
	frames int
	last   *image.RGBA
}

func (f *fakeVideoSink) OnVideoFrame(_ string, _ bool, img *image.RGBA) {
	f.mu.Lock()
	f.frames++
	f.last = img
	f.mu.Unlock()
}

func (f *fakeVideoSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frames
}

func testFrame() *image.RGBA { return image.NewRGBA(image.Rect(0, 0, 2, 2)) }

// TestVideoDemuxMulticast 视频帧出口多播：主槽（界面）与附加订阅者（网页中枢）
// 都应收到帧，移除后不再收到，且**互不覆盖**（早期单槽会被后者顶掉）。
func TestVideoDemuxMulticast(t *testing.T) {
	app := New(config.Default(), nil, NewBus())
	ui := &fakeVideoSink{}  // 主槽
	web := &fakeVideoSink{} // 附加订阅者
	app.SetVideoSink(ui)
	app.AddVideoSink(web)

	sink := app.VideoFrameSink()
	if sink == nil {
		t.Fatal("视频帧出口不应为 nil（无订阅者时静默丢帧即可）")
	}
	sink.OnVideoFrame("call-1", false, testFrame())
	if ui.count() != 1 || web.count() != 1 {
		t.Fatalf("多播失败：界面 %d 帧、网页 %d 帧，期望各 1 帧", ui.count(), web.count())
	}

	// 附加订阅者先注册、主槽后注册（真实组装顺序：wsapi.New 早于 ui.New）也不丢
	app2 := New(config.Default(), nil, NewBus())
	web2 := &fakeVideoSink{}
	ui2 := &fakeVideoSink{}
	app2.AddVideoSink(web2)
	app2.SetVideoSink(ui2)
	app2.VideoFrameSink().OnVideoFrame("call-1", true, testFrame())
	if ui2.count() != 1 || web2.count() != 1 {
		t.Fatalf("注册顺序影响多播：界面 %d 帧、网页 %d 帧，期望各 1 帧", ui2.count(), web2.count())
	}

	// 移除附加订阅者后只剩主槽
	app.RemoveVideoSink(web)
	sink.OnVideoFrame("call-1", false, testFrame())
	if ui.count() != 2 {
		t.Fatalf("主槽应收 2 帧，实际 %d 帧", ui.count())
	}
	if web.count() != 1 {
		t.Fatalf("移除后附加订阅者不应再收帧，实际 %d 帧", web.count())
	}
}

// TestWebVideoViewerState 网页订阅状态聚合：多连接取并集，变更时发布
// video.web 事件（界面据此隐藏/恢复视频窗口）。
func TestWebVideoViewerState(t *testing.T) {
	bus := NewBus()
	app := New(config.Default(), nil, bus)
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	if st := app.WebVideoState(); st.Active || st.Viewers != 0 {
		t.Fatalf("初始状态应为空，实际 %+v", st)
	}

	app.SetWebVideoViewer(1, false, true) // 只订阅远端
	st := <-sub
	if st.Name != "video.web" {
		t.Fatalf("期望 video.web 事件，实际 %s", st.Name)
	}
	got, ok := st.Data.(WebVideoState)
	if !ok || !got.Active || got.Local || !got.Remote || got.Viewers != 1 {
		t.Fatalf("事件负载不符：%+v", st.Data)
	}

	// 连接 2 只订阅本地：并集为 both
	app.SetWebVideoViewer(2, true, false)
	st = <-sub
	got = st.Data.(WebVideoState)
	if !got.Active || !got.Local || !got.Remote || got.Viewers != 2 {
		t.Fatalf("并集状态不符：%+v", got)
	}

	// 幂等：同一连接重复登记且结果不变时不重复发事件
	app.SetWebVideoViewer(2, true, false)
	select {
	case ev := <-sub:
		t.Fatalf("状态未变化却发了事件：%+v", ev)
	default:
	}

	// 连接 1 断开：还剩连接 2（本地）
	app.RemoveWebVideoViewer(1)
	got = (<-sub).Data.(WebVideoState)
	if !got.Active || !got.Local || got.Remote || got.Viewers != 1 {
		t.Fatalf("断开后的状态不符：%+v", got)
	}

	// 连接 2 断开：回到空（active=false → 界面恢复视频窗口）
	app.RemoveWebVideoViewer(2)
	got = (<-sub).Data.(WebVideoState)
	if got.Active || got.Viewers != 0 {
		t.Fatalf("全部断开后应回到空状态：%+v", got)
	}

	// 已移除的连接再移除：空操作，不发事件
	app.RemoveWebVideoViewer(2)
	select {
	case ev := <-sub:
		t.Fatalf("重复移除却发了事件：%+v", ev)
	default:
	}
}

// TestPendingVideoStateBeforeCallRegistered 视频状态早于通话登记到达时不能丢。
//
// 视频呼叫在 sip 侧是"拨出即开摄像头/回显"的：首个 Sending=true 状态会在
// core 登记通话之前上报（tel.DialVideo 返回后调用方才 upsertCall）。
// 早期实现直接丢弃，界面因此一直不显示视频窗口。
func TestPendingVideoStateBeforeCallRegistered(t *testing.T) {
	app := New(config.Default(), nil, NewBus())

	// 状态先到，通话还没登记
	app.OnVideoState("c1", VideoState{Enabled: true, PreviewOn: true, Sending: true})
	if c := app.getCall("c1"); c != nil {
		t.Fatalf("此时不该有通话记录: %+v", c)
	}

	// 登记时并入
	app.upsertCall(&Call{ID: "c1", Direction: "outgoing", State: CallRinging, HasVideo: true})
	c := app.getCall("c1")
	if !c.Video.Sending || !c.Video.Enabled {
		t.Fatalf("早到的视频状态应并入通话: %+v", c.Video)
	}

	// 之后的状态变化照常发布事件
	sub := app.Bus().Subscribe()
	defer app.Bus().Unsubscribe(sub)
	app.OnVideoState("c1", VideoState{Negotiated: true, Enabled: true, Sending: true})
	ev := <-sub
	if ev.Name != "call.video" {
		t.Fatalf("期望 call.video 事件，实际 %s", ev.Name)
	}
	if c := app.getCall("c1"); !c.Video.Negotiated || !c.HasVideo {
		t.Fatalf("协商成功后状态不符: %+v", c)
	}
}

// TestVideoTransformSharedState 旋转/镜像的共享状态（REQ-VIDEO-012）：
// 两条通道各一套、旋转 +90° 循环、变更广播 video.transform、快照进 State()。
func TestVideoTransformSharedState(t *testing.T) {
	bus := NewBus()
	app := New(config.Default(), nil, bus)
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	if st := app.VideoTransforms(); st.Remote.Rotation != 0 || st.Local.Mirror {
		t.Fatalf("初始状态应为零值: %+v", st)
	}

	// 远端转 4 次回到 0，且每次变化都广播
	for i := 1; i <= 4; i++ {
		st, err := app.RotateVideo(VideoChannelRemote)
		if err != nil {
			t.Fatalf("RotateVideo: %v", err)
		}
		want := (i * 90) % 360
		if st.Remote.Rotation != want {
			t.Fatalf("第 %d 次旋转后角度 = %d，期望 %d", i, st.Remote.Rotation, want)
		}
		if st.Local.Rotation != 0 {
			t.Fatalf("旋转远端不该动到本地: %+v", st.Local)
		}
		ev := <-sub
		if ev.Name != "video.transform" {
			t.Fatalf("期望 video.transform 事件，实际 %s", ev.Name)
		}
		if got := ev.Data.(VideoTransforms); got.Remote.Rotation != want {
			t.Fatalf("事件负载角度 = %d，期望 %d", got.Remote.Rotation, want)
		}
	}

	// 远端镜像 + 本地旋转（互不影响）
	if _, err := app.SetVideoMirror(VideoChannelRemote, true); err != nil {
		t.Fatalf("SetVideoMirror: %v", err)
	}
	<-sub
	st, err := app.RotateVideo(VideoChannelLocal)
	if err != nil {
		t.Fatalf("RotateVideo(local): %v", err)
	}
	if !st.Remote.Mirror || st.Local.Rotation != 90 {
		t.Fatalf("两条通道应各管各的: %+v", st)
	}
	<-sub

	// 状态快照要出现在 State() 里（网页重连时据此对齐）
	if got := app.State().Video; got != st {
		t.Fatalf("State().Video = %+v，期望 %+v", got, st)
	}

	// 幂等：设成同样的值不发事件
	if _, err := app.SetVideoMirror(VideoChannelRemote, true); err != nil {
		t.Fatalf("SetVideoMirror: %v", err)
	}
	select {
	case ev := <-sub:
		t.Fatalf("状态未变化却发了事件：%+v", ev)
	default:
	}

	// 未知通道报错
	if _, err := app.RotateVideo("bogus"); err == nil {
		t.Fatal("未知通道应报错")
	}
}

// TestLocalVideoWanted 界面之外的本地帧消费者判定（REQ-VIDEO-009）：
// 只有订阅了本地画面的网页才算，且关闭界面回显不影响它。
func TestLocalVideoWanted(t *testing.T) {
	app := New(config.Default(), nil, NewBus())
	if app.LocalVideoWanted() {
		t.Fatal("无订阅者时不应要本地帧")
	}

	app.SetWebVideoViewer(1, false, true) // 只看远端
	if app.LocalVideoWanted() {
		t.Fatal("只订阅远端时不应要本地帧")
	}

	app.SetWebVideoViewer(2, true, true) // 其中一个连接订阅了本地
	if !app.LocalVideoWanted() {
		t.Fatal("有连接订阅本地画面时应要本地帧")
	}

	app.RemoveWebVideoViewer(2)
	if app.LocalVideoWanted() {
		t.Fatal("本地订阅者断开后不应再要本地帧")
	}
}
