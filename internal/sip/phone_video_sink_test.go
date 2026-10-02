package sip

import (
	"image"
	"sync"
	"testing"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

// fakeVideoSink 记录是否收到过帧（模拟界面层的 core.VideoSink 实现）。
type fakeVideoSink struct {
	mu   sync.Mutex
	n    int
	last string
}

func (f *fakeVideoSink) OnVideoFrame(callID string, local bool, img *image.RGBA) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.last = callID
}

func (f *fakeVideoSink) got() (int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n, f.last
}

// TestVideoSinkChain 视频帧出口必须经 core.App.VideoFrameSink() 取到界面注册的实现。
//
// 回归测试：此前 sip.Phone 直接把 p.sink 断言成 core.VideoSink，而 p.sink 是
// core.App（只实现 EventSink），真正实现 VideoSink 的是界面层——断言恒失败、
// 出口恒为 nil、所有帧被静默丢弃。表现是「本地回显和远端画面全黑」，
// 且编译运行都不报错，极易误判成编解码问题。
func TestVideoSinkChain(t *testing.T) {
	bus := core.NewBus()
	app := core.New(config.Default(), nil, bus)
	sink := &fakeVideoSink{}
	app.SetVideoSink(sink)

	// 不起 UA，只验证取帧出口这一段链路
	p := &Phone{sink: app}
	fs := p.videoSinkFor("call-1")
	if fs == nil {
		t.Fatal("videoSinkFor 不能返回 nil（返回 nil 等于画面全黑）")
	}

	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	fs(false, img)
	fs(true, img)
	n, last := sink.got()
	if n != 2 {
		t.Fatalf("界面层应收到 2 帧，实际 %d", n)
	}
	if last != "call-1" {
		t.Fatalf("callId 应透传为 call-1，实际 %q", last)
	}
}

// TestVideoSinkChainWithoutUI 界面未注册时返回 nil（不 panic），
// 由 startVideo 打 Warn 日志——这是「无界面模式」下的正常情况。
func TestVideoSinkChainWithoutUI(t *testing.T) {
	bus := core.NewBus()
	app := core.New(config.Default(), nil, bus)
	p := &Phone{sink: app}
	if fs := p.videoSinkFor("call-1"); fs != nil {
		t.Fatal("未注册出口时应返回 nil")
	}
}
