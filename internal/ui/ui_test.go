package ui

import (
	"image"
	"math"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/core"
)

// TestLocalEchoControlsHiddenWithoutLocalVideo 本地不推视频（摄像头禁用）时，
// 回显区连同它的旋转/镜像按钮一起隐藏——它们作用的对象根本不存在。
func TestLocalEchoControlsHiddenWithoutLocalVideo(t *testing.T) {
	test.NewApp() // 构造按钮要光栅化 SVG 图标，需要 Fyne 应用/驱动
	a := &App{videoOn: true, callSending: false}
	a.videoSurface = newVideoSurface(nil)

	a.applyLocalVideoVisibility()
	for name, b := range map[string]*compactButton{
		"回显旋转": a.videoSurface.rotateLocalBtn,
		"回显镜像": a.videoSurface.mirrorLocalBtn,
		"回显开关": a.videoSurface.toggleBtn,
	} {
		if b.Visible() {
			t.Fatalf("无本地视频时不该显示%s按钮", name)
		}
	}

	// 本次通话推本地视频：三个按钮都出现
	a.callSending = true
	a.applyLocalVideoVisibility()
	for name, b := range map[string]*compactButton{
		"回显旋转": a.videoSurface.rotateLocalBtn,
		"回显镜像": a.videoSurface.mirrorLocalBtn,
		"回显开关": a.videoSurface.toggleBtn,
	} {
		if !b.Visible() {
			t.Fatalf("推本地视频时应显示%s按钮", name)
		}
	}
}

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

	// 对端画面到了：**通话建立后**照常显示
	a.remoteHasVideo = true
	a.callHasVideo = true
	a.uiState = uiInCall
	if !a.wantVideoWindow() {
		t.Fatal("收到对端画面（已接通）后应显示视频窗口")
	}
}

// TestWantVideoWindowRingPhase 振铃期的两套口径（用户要求）：
//   - 本端不推流（摄像头禁用/不可用）：振铃期不显示窗口，**接通后**才显示；
//   - 本端在推流：振铃期就显示窗口，但里面只有本地回显（对端画面等接通后，
//     由 renderVideoFrame 拦截），所以这里仍应返回 true。
func TestWantVideoWindowRingPhase(t *testing.T) {
	// 摄像头禁用 + 已经收到早期媒体画面：振铃期不显示
	a := &App{videoOn: true, activeCallID: "c1", uiState: uiRinging,
		callHasVideo: true, remoteHasVideo: true}
	if a.wantVideoWindow() {
		t.Fatal("摄像头禁用时振铃期不应显示视频窗口（早期媒体画面不展示）")
	}
	// 接通后显示
	a.uiState = uiInCall
	if !a.wantVideoWindow() {
		t.Fatal("接通后应显示视频窗口")
	}

	// 本端在推流 + 回显开着：振铃期即显示（里面只有本地回显）
	a = &App{videoOn: true, activeCallID: "c1", uiState: uiRinging,
		callSending: true, previewOn: true}
	if !a.wantVideoWindow() {
		t.Fatal("本端推流时振铃期应显示视频窗口（只放本地回显）")
	}
}

// TestWantVideoWindowWithWebSubscription 网页订阅视频期间不显示视频窗口
// （REQ-VIDEO-009：画面改在网页看），退订后立即恢复。
func TestWantVideoWindowWithWebSubscription(t *testing.T) {
	a := &App{videoOn: true, activeCallID: "c1", uiState: uiInCall, callHasVideo: true, remoteHasVideo: true}
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
	// 判定逻辑由 TestWantVideoWindowWithWebSubscription 覆盖）。
	// 订阅后是否隐藏窗口由 core 汇总成 HideWindow（REQ-VIDEO-009 修订：
	// 订阅方 keepWindow 时可保持窗口显示），界面只看这个字段。
	a := &App{}
	a.onEvent(core.Event{Name: "video.web", Data: core.WebVideoState{
		Active: true, Remote: true, Viewers: 1, HideWindow: true,
	}})
	if !a.webVideoOn {
		t.Fatal("收到 hideWindow=true 的 video.web 事件后应隐藏视频窗口")
	}
	// 订阅方要求保持窗口（keepWindow）：Active 仍为真，但不隐藏
	a.onEvent(core.Event{Name: "video.web", Data: core.WebVideoState{
		Active: true, Remote: true, Viewers: 1, HideWindow: false,
	}})
	if a.webVideoOn {
		t.Fatal("订阅方要求保持窗口时（hideWindow=false）不应隐藏视频窗口")
	}
	a.videoOn = false // 退订方向会走"恢复显示窗口"分支，测试里没有 Fyne 应用，先让它不成立
	a.onEvent(core.Event{Name: "video.web", Data: core.WebVideoState{}})
	if a.webVideoOn {
		t.Fatal("收到 hideWindow=false 后应退出隐藏状态")
	}
}

// TestVideoFitSize 视频窗口尺寸规则（用户指定）：**在固定上限 640×640 内等比
// 放到最大**——小于 640 的流也放大到窗口（小流不再开小窗），超过的等比缩小
// （不按屏幕大小算）。
func TestVideoFitSize(t *testing.T) {
	cases := []struct {
		name   string
		fw, fh int
		w, h   float32
	}{
		{"常见 640×480：正好顶满宽", 640, 480, 640, 480},
		{"恰好在边界上", 640, 640, 640, 640},
		{"小于上限：放大到宽 640", 320, 240, 640, 480},
		{"对端 192×128 小流也放大", 192, 128, 640, 426.66666},
		{"很小的小流同样放大", 96, 72, 640, 480},
		{"对端 720×480：按宽缩到 640", 720, 480, 640, 426.66666},
		{"高度超上限，按高收缩", 320, 720, 284.44443, 640},
		{"宽高都超，取更小比例", 1280, 720, 640, 360},
		{"1920×1080 同样缩到 640 宽", 1920, 1080, 640, 360},
		{"竖屏 480×720：按高收缩", 480, 720, 426.66666, 640},
		{"极端宽高比：另一边先顶到 640", 900, 100, 640, 71.111114},
		{"拿不到分辨率：兜底下限", 0, 0, videoMinW, videoMinH},
	}
	for _, c := range cases {
		w, h := videoFitSize(c.fw, c.fh)
		if math.Abs(float64(w-c.w)) > 0.01 || math.Abs(float64(h-c.h)) > 0.01 {
			t.Errorf("%s：videoFitSize(%d,%d) = %.2f×%.2f，期望 %.2f×%.2f",
				c.name, c.fw, c.fh, w, h, c.w, c.h)
		}
		if w > videoMaxW+0.01 || h > videoMaxH+0.01 {
			t.Errorf("%s：结果 %.2f×%.2f 超出固定上限 %d×%d", c.name, w, h, videoMaxW, videoMaxH)
		}
		if c.fw <= 0 || c.fh <= 0 {
			continue
		}
		if math.Abs(float64(w/h)-float64(c.fw)/float64(c.fh)) > 0.001 {
			t.Errorf("%s：宽高比 %.4f 与原视频 %.4f 不一致", c.name, w/h, float64(c.fw)/float64(c.fh))
		}
		// 顶到上限：宽或高至少有一个等于 640（放大/缩小都一样）
		if math.Abs(float64(w)-videoMaxW) > 0.01 && math.Abs(float64(h)-videoMaxH) > 0.01 {
			t.Errorf("%s：结果 %.2f×%.2f 没有顶到上限 %d×%d（该放大的没放大）",
				c.name, w, h, videoMaxW, videoMaxH)
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

// TestDialEntryTypingIsSilent 在号码输入框里输入数字**不回显按键音、也不发
// DTMF**（用户明确要求）：按键音只属于"点号码盘"与"未聚焦时在主窗体敲数字"。
// 这条测试锁住这个约定，防止有人再往输入框上挂按键音钩子。
func TestDialEntryTypingIsSilent(t *testing.T) {
	test.NewApp()
	a := &App{}
	a.dialSelect = widget.NewSelectEntry(nil)
	tones := 0
	a.onKeyTone = func(string) { tones++ }

	a.dialSelect.SetText("1") // 输入框里敲（含程序化设置）
	a.dialSelect.SetText("10086")
	a.dialSelect.SetText("")
	if tones != 0 {
		t.Fatalf("输入框文本变化触发了 %d 次按键音，期望 0 次", tones)
	}

	// 号码盘按键 / 未聚焦时敲数字：响
	a.onKeypadDigit("5")
	a.onKeypadDigit("#")
	if tones != 2 {
		t.Fatalf("号码盘按键后按键音次数为 %d，期望 2", tones)
	}
}

// TestIsDialChar 只有 0-9 / * / # 算拨号键（REQ-UI-024/026）。
func TestIsDialChar(t *testing.T) {
	for _, c := range []string{"0", "5", "9", "*", "#"} {
		if !isDialChar(c) {
			t.Errorf("%q 应算拨号键", c)
		}
	}
	for _, c := range []string{"", "a", "+", "12", "A"} {
		if isDialChar(c) {
			t.Errorf("%q 不该算拨号键", c)
		}
	}
	if !isDialDigit(fyne.KeyName("7")) || isDialDigit(fyne.KeyName("Backspace")) {
		t.Error("isDialDigit 对按键名的判断不对")
	}
}

// TestShouldShowMainAfterCall 通话结束时的主窗体显示规则（REQ-UI-030，用户定的）：
// 通话前打开的 → 一律显示（通话中开关过也显示）；通话前没开的 → 看通话中的最后状态。
func TestShouldShowMainAfterCall(t *testing.T) {
	cases := []struct {
		wasVisible, stillShown, want bool
	}{
		{wasVisible: true, stillShown: true, want: true},    // 前开、没关
		{wasVisible: true, stillShown: false, want: true},   // 前开、通话中自己关了 → 仍显示
		{wasVisible: false, stillShown: true, want: true},   // 前没开、通话中打开没关 → 保持
		{wasVisible: false, stillShown: false, want: false}, // 前没开、关了（或没开过）
	}
	for _, c := range cases {
		if got := shouldShowMainAfterCall(c.wasVisible, c.stillShown); got != c.want {
			t.Errorf("shouldShowMainAfterCall(%v, %v) = %v，期望 %v",
				c.wasVisible, c.stillShown, got, c.want)
		}
	}
}
