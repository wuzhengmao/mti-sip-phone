package ui

import (
	"image"
	"math"
	"testing"

	"mti-sip-phone/internal/core"
)

// TestIsBusyReason 遇忙音判定：486/600 才算忙（sip 层给的 reason 形如
// "486 Busy Here"），其余失败走挂断音。
func TestIsBusyReason(t *testing.T) {
	busy := []string{"486 Busy Here", "600 Busy Everywhere", "486"}
	notBusy := []string{"", "404 Not Found", "603 Decline", "488 Not Acceptable Here", "未知原因"}
	for _, r := range busy {
		if !isBusyReason(r) {
			t.Errorf("%q 应判为忙", r)
		}
	}
	for _, r := range notBusy {
		if isBusyReason(r) {
			t.Errorf("%q 不该判为忙", r)
		}
	}
}

// TestTransformFrame 旋转/镜像变换（显示用）：宽高互换、像素映射正确、
// **不改原帧**（同一块数据还要给网页订阅的出口用）。
func TestTransformFrame(t *testing.T) {
	// 2×3 的图，用像素值编码坐标便于断言
	src := image.NewRGBA(image.Rect(0, 0, 2, 3))
	set := func(x, y int, v byte) {
		i := y*src.Stride + x*4
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = v, 0, 0, 0xff
	}
	//  1 2
	//  3 4
	//  5 6
	set(0, 0, 1)
	set(1, 0, 2)
	set(0, 1, 3)
	set(1, 1, 4)
	set(0, 2, 5)
	set(1, 2, 6)
	at := func(img *image.RGBA, x, y int) byte { return img.Pix[y*img.Stride+x*4] }

	// 无变换：原样返回（不复制）
	if got := transformFrame(src, 0, false); got != src {
		t.Fatal("无变换时应原样返回")
	}

	// 顺时针 90°：2×3 → 3×2，src(x,y) → dst(h-1-y, x)：顶行变成右列
	r90 := transformFrame(src, 90, false)
	if b := r90.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("旋转 90° 后应为 3×2，实际 %d×%d", b.Dx(), b.Dy())
	}
	if at(r90, 2, 0) != 1 || at(r90, 1, 0) != 3 || at(r90, 0, 0) != 5 {
		t.Fatalf("旋转 90° 映射不符：%d %d %d，期望 1 3 5", at(r90, 2, 0), at(r90, 1, 0), at(r90, 0, 0))
	}

	// 180°：尺寸不变，四角对调
	r180 := transformFrame(src, 180, false)
	if at(r180, 1, 2) != 1 || at(r180, 0, 0) != 6 {
		t.Fatalf("旋转 180° 映射不符：%d %d，期望 1 6", at(r180, 1, 2), at(r180, 0, 0))
	}

	// 水平镜像：src(x,y) → dst(w-1-x, y)
	m := transformFrame(src, 0, true)
	if at(m, 1, 0) != 1 || at(m, 0, 0) != 2 {
		t.Fatalf("镜像映射不符：%d %d，期望 1 2", at(m, 1, 0), at(m, 0, 0))
	}

	// 镜像 + 旋转：先镜像再旋转
	mr := transformFrame(src, 90, true)
	if at(mr, 2, 0) != 2 { // src'(0,0)=src(1,0)=2
		t.Fatalf("镜像+旋转映射不符：%d，期望 2", at(mr, 2, 0))
	}

	// 原帧一个像素都不能被改（网页订阅的出口共用这块数据）
	if at(src, 0, 0) != 1 || at(src, 1, 2) != 6 {
		t.Fatal("原帧被就地修改了")
	}
}

// TestWantVideoWindowWhileSendingLocal 视频呼叫「拨出即显示」：
// 本端在推本地视频且回显开着时，不等视频协商、不等对端画面就显示窗口
// （用户要求）；本端不推流且对端没画面时不显示（纯音频通话不留空窗口）。
func TestWantVideoWindowWhileSendingLocal(t *testing.T) {
	a := &App{videoOn: true, activeCallID: "c1", callSending: true, previewOn: true}
	if !a.wantVideoWindow() {
		t.Fatal("视频呼叫拨出后（本端推流+回显开）应显示视频窗口")
	}

	// 对端没有视频：sip 侧关闭引擎后 callSending=false → 窗口收起
	a.callSending = false
	if a.wantVideoWindow() {
		t.Fatal("本端不推流且无对端画面时不应显示视频窗口")
	}

	// 回显关掉：本地画面不显示，窗口也不该为它留着
	a.callSending = true
	a.previewOn = false
	if a.wantVideoWindow() {
		t.Fatal("回显关闭且无对端画面时不应显示视频窗口")
	}

	// 对端画面到了：照常显示
	a.remoteHasVideo = true
	a.callHasVideo = true
	if !a.wantVideoWindow() {
		t.Fatal("收到对端画面后应显示视频窗口")
	}
}

// TestWantVideoWindowWithWebSubscription 网页订阅视频期间不显示视频窗口
// （REQ-VIDEO-009：画面改在网页看），退订后立即恢复。
func TestWantVideoWindowWithWebSubscription(t *testing.T) {
	a := &App{videoOn: true, activeCallID: "c1", callHasVideo: true, remoteHasVideo: true}
	if !a.wantVideoWindow() {
		t.Fatal("有远端画面时应显示视频窗口")
	}
	a.webVideoOn = true
	if a.wantVideoWindow() {
		t.Fatal("网页订阅期间不应显示视频窗口")
	}
	a.webVideoOn = false
	if !a.wantVideoWindow() {
		t.Fatal("网页退订后应恢复显示视频窗口")
	}
}

// TestOnVideoWebEvent 网页订阅事件（video.web）驱动窗口显隐。
//
// 回归测试：总线是**进程内**的，事件负载是 core.WebVideoState 结构体、
// 不经 JSON；界面若照 WS 客户端的习惯断言成 map[string]any，断言恒失败，
// 表现为"网页订阅了但桌面视频窗口不关"（编译运行都不报错）。
func TestOnVideoWebEvent(t *testing.T) {
	// 只验证「事件 → webVideoOn 状态翻转」（真实的窗口显隐需要 Fyne 运行时，
	// 判定逻辑由 TestWantVideoWindowWithWebSubscription 覆盖）
	a := &App{}
	a.onEvent(core.Event{Name: "video.web", Data: core.WebVideoState{Active: true, Remote: true, Viewers: 1}})
	if !a.webVideoOn {
		t.Fatal("收到 active=true 的 video.web 事件后应进入网页订阅状态")
	}
	a.videoOn = false // 退订方向会走"恢复显示窗口"分支，测试里没有 Fyne 应用，先让它不成立
	a.onEvent(core.Event{Name: "video.web", Data: core.WebVideoState{}})
	if a.webVideoOn {
		t.Fatal("收到 active=false 后应退出网页订阅状态")
	}
}

// TestVideoFitSize 视频窗口尺寸规则（用户指定）：
//  1. 优先按原视频宽高显示；
//  2. 宽或高超过屏幕 1/4 时等比缩小，缩小后宽高都在 1/4 内。
//
// 用 1920×1080 屏幕 → 上限 480×270 做基准。
func TestVideoFitSize(t *testing.T) {
	const maxW, maxH float32 = 480, 270
	cases := []struct {
		name   string
		fw, fh int
		w, h   float32
	}{
		{"原样（宽高都未超上限）", 320, 240, 320, 240},
		{"恰好在边界上", 480, 270, 480, 270},
		{"高度超上限，按高收缩", 352, 288, 330, 270},
		{"宽度超上限，按宽收缩", 640, 240, 480, 180},
		{"宽高都超上限，取更小比例", 1920, 1080, 480, 270},
		{"16:9 源", 1280, 720, 480, 270},
		{"帧过小，等比放大到下限", 96, 72, 160, 120},
		{"极端宽高比：下限与上限撞车时上限优先", 900, 100, 480, 53.333332},
	}
	for _, c := range cases {
		w, h := videoFitSize(c.fw, c.fh, maxW, maxH)
		if math.Abs(float64(w-c.w)) > 0.01 || math.Abs(float64(h-c.h)) > 0.01 {
			t.Errorf("%s：videoFitSize(%d,%d) = %.2f×%.2f，期望 %.2f×%.2f",
				c.name, c.fw, c.fh, w, h, c.w, c.h)
		}
		// 规则 2 必须恒成立
		if w > maxW+0.01 || h > maxH+0.01 {
			t.Errorf("%s：结果 %.2f×%.2f 超出上限 %.0f×%.0f", c.name, w, h, maxW, maxH)
		}
		// 等比（不改宽高比）
		if math.Abs(float64(w/h)-float64(c.fw)/float64(c.fh)) > 0.001 {
			t.Errorf("%s：宽高比 %.4f 与原视频 %.4f 不一致", c.name, w/h, float64(c.fw)/float64(c.fh))
		}
	}
}

// TestTrayRegisteredState 托盘图标颜色状态机：续注册（registering）
// 期间保持原色——已注册保持绿色、失败/未注册为红色；只有注册失败
// 才由绿转红（用户要求：定时续注册不应让图标闪红）。
func TestTrayRegisteredState(t *testing.T) {
	a := &App{}
	steps := []struct {
		state string
		want  bool // trayRegistered（true=托盘显示绿色）
	}{
		{"unregistered", false},
		{"registering", false}, // 首次注册中：还没成功过，保持红色
		{"registered", true},
		{"registering", true}, // 续注册中：保持绿色
		{"registered", true},
		{"registering", true},
		{"failed", false}, // 续注册失败：转红
		{"registering", false},
		{"registered", true},
	}
	for _, s := range steps {
		a.setRegState(s.state)
		if a.trayRegistered != s.want {
			t.Errorf("状态 %q 后 trayRegistered = %v，期望 %v", s.state, a.trayRegistered, s.want)
		}
		if a.regState != s.state {
			t.Errorf("状态 %q 未记录，regState = %q", s.state, a.regState)
		}
	}
}
