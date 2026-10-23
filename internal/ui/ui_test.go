package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/config"
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
	a.dialSelect = newDialEntry(a)
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

// TestInstanceColumnShift 多实例（沙箱槽位）的窗口列偏移（REQ-NFR-007）：
// 主实例不偏移；槽位 N 整列左移 N × 列宽（列宽 = 视频窗口最大宽 + 边距，
// 取最大宽是因为面板/更新提示窗/视频窗口都要落在自己那一列里，互不重叠）。
func TestInstanceColumnShift(t *testing.T) {
	saved := instanceSlot
	defer func() { instanceSlot = saved }()

	instanceSlot = 0
	if got := instanceColumnShiftPx(1); got != 0 {
		t.Fatalf("主实例不应偏移，实际 %d", got)
	}
	SetInstanceSlot(2)
	if instanceSlot != 2 {
		t.Fatalf("SetInstanceSlot 未生效：%d", instanceSlot)
	}
	want := int32(2 * instanceColumnWidth) // scale = 1
	if got := instanceColumnShiftPx(1); got != want {
		t.Fatalf("槽位 2 的偏移应为 %d，实际 %d", want, got)
	}
	if got := instanceColumnShiftPx(1.5); got != int32(float32(want)*1.5) {
		t.Fatalf("缩放 1.5 时偏移应为 %d，实际 %d", int32(float32(want)*1.5), got)
	}
	SetInstanceSlot(-3) // 非法值忽略：仍是上一个有效槽位
	if instanceSlot != 2 {
		t.Fatalf("非法槽位不应改写当前值：%d", instanceSlot)
	}
}

// TestSRTPHintFitsWindow 媒体加密的说明文字必须塞得进固定 320 DIP 宽的主窗体
// （用户实测过：一行太长的中文说明会把窗体撑开）。中文没有空格，整行会被当成
// 一个"词"，光靠 TextWrapWord 折不开——所以**每一行都要短**，这里逐行卡住。
func TestSRTPHintFitsWindow(t *testing.T) {
	test.NewApp()
	// 主窗体固定 320 DIP 宽（SetFixedSize），说明的**每一行**都要自己塞得进去。
	// 长行虽然会被 Fyne 折行，但会折在"…对端要求则配合；建议 ="这种地方，读起来
	// 断断续续（用户实测反馈"说明行太长了"），所以这里按**不折行**量每行的自然宽度。
	const maxLineWidth = 280 // 320 DIP 减去内边距与滚动条，留点余量
	widthOf := func(s string) float32 {
		l := widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
		return l.MinSize().Width // 不设 Wrapping：量的是整行不折行时的自然宽度
	}
	for _, note := range []string{srtpHint, tlsNoteText} {
		for _, line := range strings.Split(note, "\n") {
			if w := widthOf(line); w > maxLineWidth {
				t.Fatalf("说明行过宽（%.0f > %d DIP），会被折到难读的位置：%q", w, maxLineWidth, line)
			}
		}
	}
	// 反例（证明这道卡量得出宽度、不是恒过的空断言）：早先把三档写在一行的句子，
	// 按同一口径必须被判超限。
	long := "支持 = 不主动声明，对端要求则配合；建议 = 主动建议但不强制；必要 = 对端不支持则拒绝通话。"
	if w := widthOf(long); w <= maxLineWidth {
		t.Fatalf("反例没被判超限（%.0f），说明本测试量不出宽度、形同虚设", w)
	}
}

// TestDeclaredIPForm 声明 IP 是可输入的下拉（同拨号框）：留空 = 自动，
// 选中候选 / 手填任意合法 IP 都算数，非法内容一律当"自动"（保存时会被拦下）。
func TestDeclaredIPForm(t *testing.T) {
	test.NewApp()
	a := &App{}
	a.accIPSelect = widget.NewSelectEntry([]string{"10.0.0.5", "192.168.3.15"})

	// 留空 = 自动
	a.accIPSelect.SetText("")
	if got := a.declaredIPValue(); got != "" {
		t.Fatalf("留空应取空（= 自动），实际 %q", got)
	}

	// 从下拉里选
	a.accIPSelect.SetText("10.0.0.5")
	if got := a.declaredIPValue(); got != "10.0.0.5" {
		t.Fatalf("应取选中的地址，实际 %q", got)
	}

	// 手填（不在候选里也要能填）
	a.accIPSelect.SetText(" 10.168.5.10 ")
	if got := a.declaredIPValue(); got != "10.168.5.10" {
		t.Fatalf("手填应被规范化，实际 %q", got)
	}

	// 非法输入：取值当自动（保存时用 TrimSpace(Text) != "" 来拦住）
	a.accIPSelect.SetText("example.com")
	if got := a.declaredIPValue(); got != "" {
		t.Fatalf("非法输入应取空，实际 %q", got)
	}
}

// TestCallGripGeometry 通话面板顶部拖动条（REQ-UI-040）：细线要落在
// "距顶 4px、高 2px、占面板宽度一半"的位置上，可抓取区域比细线高一截
// （2px 太难点中），且不碰到第一行内容（内容从 y=14 才开始）。
func TestCallGripGeometry(t *testing.T) {
	test.NewApp()

	g := newCallGrip(nil, nil)
	m := g.MinSize()
	if m.Width != callPanelWidth/2 {
		t.Errorf("拖动条宽度应为面板的一半（%v），实际 %v", callPanelWidth/2, m.Width)
	}
	if m.Height != callGripHitH {
		t.Errorf("可抓取区域高应为 %v，实际 %v", callGripHitH, m.Height)
	}
	if callGripHitH > 14 {
		t.Errorf("可抓取区域（%v）不得伸进第一行内容（y=14 起）", callGripHitH)
	}

	// 细线在抓手区域内的位置与粗细
	line := canvas.NewRectangle(theme.PrimaryColor())
	callGripLineLayout{}.Layout([]fyne.CanvasObject{line}, m)
	if line.Position().Y != callGripLineTop || line.Size().Height != callGripLineHeight {
		t.Errorf("细线应距顶 %v 高 %v，实际 y=%v h=%v",
			callGripLineTop, callGripLineHeight, line.Position().Y, line.Size().Height)
	}
	if line.Size().Width != m.Width {
		t.Errorf("细线应占满抓手宽度 %v，实际 %v", m.Width, line.Size().Width)
	}

	// 定位层把它摆在顶部正中
	callGripLayer{}.Layout([]fyne.CanvasObject{g}, fyne.NewSize(callPanelWidth, callPanelHeight))
	wantX := (float32(callPanelWidth) - m.Width) / 2
	if g.Position().X != wantX || g.Position().Y != 0 {
		t.Errorf("拖动条应位于顶部居中 (%v,0)，实际 (%v,%v)", wantX, g.Position().X, g.Position().Y)
	}
}

// TestCallGripDragCallbacks 拖动条只负责"开始一次 + 结束一次"：
// Dragged 每帧都会被调（会话由 App 侧幂等守卫），DragEnd 收口。
func TestCallGripDragCallbacks(t *testing.T) {
	test.NewApp()
	starts, ends := 0, 0
	g := newCallGrip(func() { starts++ }, func() { ends++ })
	g.Dragged(nil)
	g.Dragged(nil)
	g.DragEnd()
	if starts != 2 || ends != 1 {
		t.Fatalf("回调次数不对：start=%d end=%d", starts, ends)
	}
}

// TestCallGripInterfaces 拖动条必须同时是 Draggable（能拖）与 Cursorable
// （悬停变"可移动"光标），且**不能**是 Tappable/Hoverable——否则会抢走
// 面板按钮的点击与名称备注的悬停。
func TestCallGripInterfaces(t *testing.T) {
	test.NewApp()
	g := newCallGrip(nil, nil)
	var obj fyne.CanvasObject = g
	if _, ok := obj.(fyne.Draggable); !ok {
		t.Error("拖动条必须实现 fyne.Draggable")
	}
	if _, ok := obj.(desktop.Cursorable); !ok {
		t.Error("拖动条必须实现 desktop.Cursorable（悬停提示可拖动）")
	}
	if _, ok := obj.(fyne.Tappable); ok {
		t.Error("拖动条不该实现 Tappable（会抢走面板按钮的点击）")
	}
	if _, ok := obj.(desktop.Hoverable); ok {
		t.Error("拖动条不该实现 Hoverable（会抢走名称备注的悬停）")
	}
	if c := g.Cursor(); c == desktop.DefaultCursor {
		t.Error("拖动条悬停时不该是默认箭头光标")
	}
}

// TestMoveCursorImage 自定义"可移动"光标：热点在正中，图形有黑有白
// （白描边保证深浅背景下都看得清）。
func TestMoveCursorImage(t *testing.T) {
	img, hx, hy := moveCursor{}.Image()
	if img == nil {
		t.Fatal("光标位图不能为空")
	}
	if hx != moveCursorHot || hy != moveCursorHot {
		t.Errorf("热点应在正中 (%d,%d)，实际 (%d,%d)", moveCursorHot, moveCursorHot, hx, hy)
	}
	b := img.Bounds()
	if b.Dx() != moveCursorSize || b.Dy() != moveCursorSize {
		t.Errorf("光标位图应为 %d×%d，实际 %d×%d", moveCursorSize, moveCursorSize, b.Dx(), b.Dy())
	}
	var black, white int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, a := img.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			if r == 0 && g == 0 && bb == 0 {
				black++
			} else {
				white++
			}
		}
	}
	if black == 0 || white == 0 {
		t.Fatalf("光标位图应含黑色主体与白色描边，实际 black=%d white=%d", black, white)
	}
}

// TestApplyVideoTransformsPersistsLocalOnly 本地回显的旋转/镜像落盘、
// 远端的不落盘（REQ-VIDEO-015，用户明确要求不对称）。
func TestApplyVideoTransformsPersistsLocalOnly(t *testing.T) {
	test.NewApp()
	a := &App{cfg: config.Default()}

	a.applyVideoTransforms(core.VideoTransforms{
		Remote: core.VideoTransform{Rotation: 90, Mirror: true},
		Local:  core.VideoTransform{Rotation: 180, Mirror: true},
	})
	if a.cfg.UI.LocalPreviewRotation != 180 || !a.cfg.UI.LocalPreviewMirror {
		t.Fatalf("本地回显的旋转/镜像应写入配置，实际 rot=%d mirror=%v",
			a.cfg.UI.LocalPreviewRotation, a.cfg.UI.LocalPreviewMirror)
	}

	// 只改远端：配置里的本地值不该跟着变（远端不持久化）
	a.applyVideoTransforms(core.VideoTransforms{
		Remote: core.VideoTransform{Rotation: 270, Mirror: false},
		Local:  core.VideoTransform{Rotation: 180, Mirror: true},
	})
	if a.cfg.UI.LocalPreviewRotation != 180 || !a.cfg.UI.LocalPreviewMirror {
		t.Fatalf("远端变换不该影响配置里的本地值，实际 rot=%d mirror=%v",
			a.cfg.UI.LocalPreviewRotation, a.cfg.UI.LocalPreviewMirror)
	}
	if a.remoteRot.Load() != 270 {
		t.Fatalf("远端旋转原子量未更新：%d", a.remoteRot.Load())
	}
}

// TestTooltipWidgetInterfaces 提示控件的接口契约：通信录/通话记录的名称备注
// 靠 hoverNote 的悬停（大段删代码时容易把方法一起删掉，编译照样过、功能静默失效）。
func TestTooltipWidgetInterfaces(t *testing.T) {
	test.NewApp()
	tip := newTooltipLayer()

	var note any = newHoverNote("张三", 12, "备注", tip)
	if _, ok := note.(desktop.Hoverable); !ok {
		t.Error("hoverNote 必须实现 desktop.Hoverable（联系人备注靠悬停显示）")
	}
}

// TestStatusRowAgentIcon 顶栏状态行加坐席图标后：
//  1. 先用较窄宽度布局（模拟图标未显示时的旧尺寸）再把「已注册」截断；
//  2. 换到足够宽度重新布局，文字必须**恢复**（不能被永久截成「...」）；
//  3. 图标排在文字右边的正确位置。
func TestStatusRowAgentIcon(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	tip := newTooltipLayer()
	label := newPlainText("已注册", 14, theme.ForegroundColor(), true)
	icon := newHoverIcon(IconAgentSeat, 16, tip)
	icon.SetNote("张三")
	objs := []fyne.CanvasObject{newStatusDot("registered"), label, icon}

	// 窄宽度（比 MinSize 少 1px）→ 文字被截断
	m := statusRowLayout{}.MinSize(objs)
	statusRowLayout{}.Layout(objs, fyne.NewSize(m.Width-1, 20))
	if label.text.Text == "已注册" {
		t.Fatal("窄宽度下本应截断，测试前提不成立")
	}
	// 足够宽度（= MinSize，即布局实际会分到的宽度）→ 文字恢复
	statusRowLayout{}.Layout(objs, fyne.NewSize(m.Width, 20))
	if label.text.Text != "已注册" {
		t.Fatalf("宽度恢复后文字应恢复为「已注册」，实际 %q", label.text.Text)
	}
	if m.Width < 2+16+2+fyne.MeasureText("已注册", 14, fyne.TextStyle{Bold: true}).Width+3+16 {
		t.Fatalf("MinSize 应包含文字原始宽度与图标宽度，实际 %v", m.Width)
	}
	// 命中区比图标本体大一圈（左移了 hoverIconHitPad），视觉图标仍在文字右侧
	if icon.Position().X+hoverIconHitPad <= label.Position().X+label.Size().Width-1 {
		t.Fatalf("图标应在文字右侧：label x=%.1f w=%.1f icon x=%.1f",
			label.Position().X, label.Size().Width, icon.Position().X)
	}
}

// TestHoverIconShowsNote 坐席图标悬停：停住后提示图层显示 displayName。
func TestHoverIconShowsNote(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	tip := newTooltipLayer()
	icon := newHoverIcon(IconAgentSeat, 16, tip)
	icon.SetNote("张三")
	if m := icon.MinSize(); m.Width < 16 || m.Height < 16 {
		t.Fatalf("图标最小尺寸异常：%v", m)
	}
	icon.MouseIn(&desktop.MouseEvent{PointEvent: fyne.PointEvent{AbsolutePosition: fyne.NewPos(10, 10)}})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if tip.shown {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("悬停后应显示提示")
}

// TestTooltipLayerRenders 提示图层真的画出来了：在 Stack 顶层显示提示后，
// 画布对应位置的像素不应与背景相同（防止"逻辑上 show 了、实际没绘制"）。
func TestTooltipLayerRenders(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	bg := canvas.NewRectangle(color.NRGBA{R: 0, G: 0, B: 0, A: 255})
	tip := newTooltipLayer()
	w := test.NewWindow(container.NewStack(bg, tip))
	defer w.Close()
	w.Resize(fyne.NewSize(200, 120))

	tip.show("座席已登录 - 张三", fyne.NewPos(10, 10))
	img := w.Canvas().Capture()
	// 提示框画在 absPos + (12,18) 处，取框内一点
	px := color.NRGBAModel.Convert(img.At(40, 45)).(color.NRGBA)
	if px.R > 20 || px.G > 20 || px.B > 20 {
		return // 非黑 = 画出了东西
	}
	t.Fatalf("提示图层未绘制：像素 (40,45) = %+v", px)
}

// TestHoverIconShowsNoteOnMove 图标"出现时鼠标已经在上面"的场景（Fyne 只发
// MouseMoved、不发 MouseIn）：MouseMoved 也必须能拉起提示（REQ-ACC-028，
// 用户实测"图标出来了但移上去不弹提示，重新注册一次又好了"）。
func TestHoverIconShowsNoteOnMove(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	tip := newTooltipLayer()
	icon := newHoverIcon(IconAgentSeat, 16, tip)
	icon.SetNote("座席已登录 - 张三")
	icon.MouseMoved(&desktop.MouseEvent{PointEvent: fyne.PointEvent{AbsolutePosition: fyne.NewPos(10, 10)}})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if tip.shown {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("MouseMoved 后也应显示提示")
}

// TestTooltipLayerShowsOnFirstShow 第一次显示就必须自己把底色/文字摆好：
// 底色在显示前是 0 尺寸，若只依赖"下一次布局"，提示会看不见——直到别的事件
// 触发一次整帧重绘才突然出现（真机现象"一会有一会没"）。
func TestTooltipLayerShowsOnFirstShow(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	tip := newTooltipLayer()
	w := test.NewWindow(container.NewStack(tip))
	defer w.Close()
	w.Resize(fyne.NewSize(200, 120))

	tip.show("座席已登录 - 张三", fyne.NewPos(10, 10))
	if !tip.box.Visible() {
		t.Fatal("显示后提示框必须可见")
	}
	if tip.bg.Size().Width <= 0 || tip.bg.Size().Height <= 0 {
		t.Fatalf("首次显示时底色尺寸不应为 0：%v", tip.bg.Size())
	}
	if tip.bg.Position().X < 10 || tip.bg.Position().Y < 10 {
		t.Fatalf("底色应摆在指针右下方，实际 %v", tip.bg.Position())
	}

	tip.hide()
	if tip.box.Visible() || tip.shown {
		t.Fatal("hide 后提示框必须隐藏")
	}
}

// TestCTISpinFrames 「指令在途」的旋转圆圈：8 帧都要能解码、且不是空图。
func TestCTISpinFrames(t *testing.T) {
	if len(ctiSpinFrames) != 8 {
		t.Fatalf("旋转图标应有 8 帧，实际 %d", len(ctiSpinFrames))
	}
	for i, f := range ctiSpinFrames {
		img, err := png.Decode(bytes.NewReader(f.Content()))
		if err != nil {
			t.Fatalf("第 %d 帧不是可解码的 PNG：%v", i, err)
		}
		if img.Bounds().Dx() == 0 || img.Bounds().Dy() == 0 {
			t.Fatalf("第 %d 帧是空图", i)
		}
	}
}

// TestCTIIconsRender 话务按钮图标必须可用：PNG 图标（iconfont 原始渲染）要能
// 解码出非空位图；SVG 图标（通用 X）要含 <path d="...">。兜底"图标空白/写错"。
func TestCTIIconsRender(t *testing.T) {
	for name, res := range map[string]fyne.Resource{
		"咨询":   IconCTIConsult,
		"咨询转接": IconCTIConsultTransfer,
		"三方":   IconCTIThreeWay,
		"转移":   IconCTITransfer,
		"会议":   IconCTIConference,
		"视频会议": IconCTIVideoConference,
		"监听":   IconCTIMonitor,
		"耳语":   IconCTIWhisper,
		"强拦":   IconCTIIntercept,
		"强插":   IconCTIIntervene,
		"添加成员": IconCTIAddMember,
		"强制挂断": IconHangup,
		"主持人":  IconHost,
	} {
		img, err := png.Decode(bytes.NewReader(res.Content()))
		if err != nil {
			t.Fatalf("%s 图标不是可解码的 PNG：%v", name, err)
		}
		if img.Bounds().Dx() == 0 || img.Bounds().Dy() == 0 {
			t.Fatalf("%s 图标是空图", name)
		}
	}
}

// TestCompactButtonSetBgSurvivesEnable 改过底色后禁用再启用，颜色必须保持：
// Enable() 按 baseBg 还原，SetBg 不同步 baseBg 的话会回到构造时的旧色
// （踩过：主窗体呼叫按钮设成蓝底后又被 applyMainCallMode 的 Enable 变回绿色）。
func TestCompactButtonSetBgSurvivesEnable(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	b := newCompactButton(IconPlayWhite, colorSuccessGreen, 36, nil)
	b.SetBg(colorHoldBlue)
	b.Disable()
	b.Enable()
	if got := b.bg.FillColor; got != colorHoldBlue {
		t.Fatalf("启用后底色应保持蓝色，实际 %v", got)
	}
}

// TestCallNameBoxMinSizeUsesFullText 通话面板按 MinSize 给对端号码分配宽度：
// MinSize 若取"被截断后"的宽度，号码会永远显示成「…」——用户实测：拨号时
// 前面的呼叫号码变成了 …
func TestCallNameBoxMinSizeUsesFullText(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	b := newCallNameBox(newTooltipLayer())
	b.Set("1381003", false, "")
	full := b.MinSize().Width
	if full < 40 {
		t.Fatalf("MinSize 应按完整号码算宽度，实际 %.1f", full)
	}
	b.Resize(fyne.NewSize(8, 20)) // 极窄：文字被截成「…」
	if got := b.MinSize().Width; got != full {
		t.Fatalf("文字被截断后 MinSize 不应缩小（否则永远恢复不了）：%.1f → %.1f", full, got)
	}
}

// TestCompactButtonShowsTip 通话面板的紧凑按钮（只有图标）悬停后要弹出文字说明
// （用户要求：面板上每个按钮移动上去都给提示）。没挂提示的按钮不能弹。
func TestCompactButtonShowsTip(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	tip := newTooltipLayer()

	b := newCompactButton(IconPlayWhite, colorSuccessGreen, 36, nil)
	if _, ok := any(b).(desktop.Hoverable); !ok {
		t.Fatal("compactButton 必须实现 desktop.Hoverable（面板按钮的悬停提示靠它）")
	}
	b.setTip(tip, "挂断")
	b.MouseIn(&desktop.MouseEvent{PointEvent: fyne.PointEvent{AbsolutePosition: fyne.NewPos(10, 10)}})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if tip.shown {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !tip.shown {
		t.Fatal("悬停后应显示按钮提示")
	}

	// 移开：提示立即收起
	b.MouseOut()
	if tip.shown {
		t.Fatal("移开后提示应收起")
	}

	// 没有提示文案的按钮（主窗体上的按钮）不弹
	noTip := newCompactButton(IconPlayWhite, colorSuccessGreen, 36, nil)
	noTip.MouseIn(&desktop.MouseEvent{PointEvent: fyne.PointEvent{AbsolutePosition: fyne.NewPos(10, 10)}})
	time.Sleep(tooltipDelay + 100*time.Millisecond)
	if tip.shown {
		t.Fatal("未设置提示的按钮不该弹提示")
	}
}

// fakeContacts 内存通信录（peerText 的优先级测试用）。
type fakeContacts struct{ list []core.Contact }

func (f *fakeContacts) List() ([]core.Contact, error)          { return f.list, nil }
func (f *fakeContacts) Save(string, core.Contact) error        { return nil }
func (f *fakeContacts) SetStarred([]string, bool) (int, error) { return 0, nil }
func (f *fakeContacts) Delete([]string) (int, error)           { return 0, nil }
func (f *fakeContacts) Clear() (int, error)                    { return 0, nil }
func (f *fakeContacts) Merge([]core.Contact) (int, int, error) { return 0, 0, nil }
func (f *fakeContacts) Close() error                           { return nil }

// TestCallDisplayPrefersPeerName INVITE 的 Call-Info display-name（core.Call.PeerName，
// REQ-ACC-029）**优先于通信录名称**；没有才回退通信录、再回退 SIP 显示名。
func TestCallDisplayPrefersPeerName(t *testing.T) {
	test.NewApp()
	cfg := config.Default()
	c := core.New(cfg, nil, core.NewBus())
	c.SetContacts(&fakeContacts{list: []core.Contact{{Number: "2002", Name: "通信录名字"}}})
	a := &App{core: c, cfg: cfg}

	call := &core.Call{Remote: "2002", RemoteName: "From名字", PeerName: "Call-Info名字"}
	if got, _, _ := a.callDisplay(call); got != "2002 Call-Info名字" {
		t.Fatalf("Call-Info 名称应优先于通信录，实际 %q", got)
	}
	if got := callRemoteName(call); got != "Call-Info名字" {
		t.Fatalf("callRemoteName 也应优先 Call-Info 名称，实际 %q", got)
	}
	call.PeerName = ""
	if got, _, _ := a.callDisplay(call); got != "2002 通信录名字" {
		t.Fatalf("无 Call-Info 名称时应回退通信录，实际 %q", got)
	}
}

// TestPeerTextPrefersCTIName 话务面板里号码的展示：**协议给的名称优先**
// （MSP_EVENT_* 的 xxxName），没有才回退通信录的名称，再没有就只显示号码。
func TestPeerTextPrefersCTIName(t *testing.T) {
	test.NewApp()
	cfg := config.Default()
	c := core.New(cfg, nil, core.NewBus())
	c.SetContacts(&fakeContacts{list: []core.Contact{{Number: "2002", Name: "通信录名字"}}})
	a := &App{core: c, cfg: cfg}

	if got := a.peerText("2002", "李四"); got != "2002 李四" {
		t.Fatalf("应优先用协议给的名称，实际 %q", got)
	}
	if got := a.peerText("2002", ""); got != "2002 通信录名字" {
		t.Fatalf("协议没给名称时应回退通信录，实际 %q", got)
	}
	if got := a.peerText("9999", ""); got != "9999" {
		t.Fatalf("都没有时应只显示号码，实际 %q", got)
	}
}

// TestCompactButtonIconReraster 换图标后**位图必须真的重画**。
//
// Fyne 的 canvas.Image 在只换 Resource 时 Refresh 不一定重新栅格化 SVG——
// 实测：视频图标从蓝换成红、Resource 已经变了，画布上却仍是蓝色；转圈动画
// 每帧换资源也一样"转了但画面不变"。修复是给图标做一次整数尺寸的
// Resize 抖动（见 rerasterIcon），这条测试钉住它。
func TestCompactButtonIconReraster(t *testing.T) {
	test.NewApp()
	btn := newPanelButton(IconVideoOn, colorHoldBlue, callPanelButtonSize, nil)
	w := test.NewWindow(btn)
	defer w.Close()
	w.Resize(fyne.NewSize(80, 80))
	if btn.icon.Image == nil {
		t.Fatal("图标未渲染，无法比较位图")
	}
	center := func() string {
		b := btn.icon.Image.Bounds()
		r, g, bl, a := btn.icon.Image.At(b.Dx()/2, b.Dy()/2).RGBA()
		return fmt.Sprintf("%d,%d,%d,%d", r>>8, g>>8, bl>>8, a>>8)
	}
	on := center()
	btn.SetIcon(IconVideoOff)
	btn.SetAccent(colorDangerRed)
	if off := center(); off == on {
		t.Fatalf("换图标后位图没有重画（仍为 %s）", on)
	}
}

// TestConsultEndNotice 咨询在拨号/振铃阶段结束（CONSULT_FINISHED）时的提示：
// 拨号中 → 「咨询失败」，振铃中 → 「咨询未接通」，已接通或非咨询不提示。
func TestConsultEndNotice(t *testing.T) {
	consult := func(phase string) *core.Call {
		return &core.Call{CTI: core.CTIState{TalkState: core.TalkConsult, ConsultPhase: phase}}
	}
	twoParty := &core.Call{CTI: core.CTIState{TalkState: core.TalkTwoParty}}
	cases := []struct {
		name       string
		prev, next *core.Call
		want       string
	}{
		{"拨号中结束", consult("dialing"), twoParty, "咨询失败"},
		{"振铃中结束", consult("ringing"), twoParty, "咨询未接通"},
		{"接通后结束", consult("talking"), twoParty, ""},
		{"非咨询状态", twoParty, twoParty, ""},
		{"没有上一个状态", nil, twoParty, ""},
	}
	for _, c := range cases {
		if got := consultEndNotice(c.prev, c.next); got != c.want {
			t.Errorf("%s：提示应为 %q，实际 %q", c.name, c.want, got)
		}
	}
}

// TestCTIDialAction 拨号复用模式只对自己的动作收尾（收起主窗体）：复用期间
// 在主窗体上点坐席状态按钮（setBusy/setIdle）等，回执不能把窗口关掉。
func TestCTIDialAction(t *testing.T) {
	cases := []struct {
		mode, action string
		want         bool
	}{
		{"consult", "consult", true},
		{"consult", "consultThreeWay", true},
		{"consult", "setBusy", false},
		{"consult", "listAgents", false},
		{"transfer", "transfer", true},
		{"transfer", "consultTransfer", true},
		{"transfer", "consult", false},
		{"conference", "addConferenceMembers", true},
		{"conference", "setIdle", false},
		{"", "consult", false},
	}
	for _, c := range cases {
		if got := ctiDialAction(c.mode, c.action); got != c.want {
			t.Errorf("ctiDialAction(%q, %q) = %v，期望 %v", c.mode, c.action, got, c.want)
		}
	}
}

// TestMonitorPeerLayoutKeepsNumbersFull 监听面板第二行：空间不够时**号码保持
// 完整**，只截断名称；名称截断后文本带省略号（悬停显示全文由 hoverClipNote 负责）。
func TestMonitorPeerLayoutKeepsNumbersFull(t *testing.T) {
	test.NewApp()
	leftNo := newThemedText("1381003", 14, false)
	leftName := newHoverClipNote("张", 14, nil)
	leftName.Hide()
	arrow := canvas.NewRectangle(color.Transparent)
	arrow.SetMinSize(fyne.NewSize(14, 14))
	rightNo := newThemedText("2999", 14, false)
	rightName := newHoverClipNote("吴正茂（市局）", 14, nil)

	row := container.New(monitorPeerLayout{gap: 4},
		leftNo, leftName, arrow, rightNo, rightName)
	row.Resize(fyne.NewSize(170, 36))

	if got, want := leftNo.Size().Width, leftNo.fullWidth(); got != want {
		t.Fatalf("左号码应完整显示（宽度 %v），实际 %v", want, got)
	}
	if got, want := rightNo.Size().Width, rightNo.fullWidth(); got != want {
		t.Fatalf("右号码应完整显示（宽度 %v），实际 %v", want, got)
	}
	if rightName.text.Size().Width >= rightName.fullWidth() {
		t.Fatalf("空间不足时名称应被截断（宽度 %v，完整 %v）",
			rightName.text.Size().Width, rightName.fullWidth())
	}
	if !strings.HasSuffix(rightName.text.text.Text, "…") {
		t.Fatalf("截断后的名称应以省略号结尾，实际 %q", rightName.text.text.Text)
	}
}

// TestConferenceLeadLayoutAlignsNumbers 会议成员行左侧：[首列][号码]。首列里
// 放移除按钮、主持人标记或空位（三者同列），号码的起始位置与首列内容无关——
// 保证各行号码左边缘对齐、且移除按钮与主持人图标在同一列。
func TestConferenceLeadLayoutAlignsNumbers(t *testing.T) {
	test.NewApp()
	l := conferenceLeadLayout{slot: callPanelButtonSize, gap: 4}
	textX := func(slotW, slotH float32) float32 {
		slot := canvas.NewRectangle(color.Transparent)
		slot.SetMinSize(fyne.NewSize(slotW, slotH))
		text := newThemedText("2002 张三", 12, false)
		c := container.New(l, slot, text)
		l.Layout(c.Objects, fyne.NewSize(260, 36))
		return text.Position().X
	}
	// 主持人标记（14px，居中对齐在首列里）与移除按钮（36px）下的号码位置一致
	hostX, removeX := textX(14, 14), textX(callPanelButtonSize, callPanelButtonSize)
	if hostX != removeX {
		t.Fatalf("首列内容不同时号码应对齐：主持人行 x=%v，移除按钮行 x=%v", hostX, removeX)
	}
	if want := float32(callPanelButtonSize + 4); hostX != want {
		t.Fatalf("号码应在首列之后（x=%v），实际 x=%v", want, hostX)
	}
}

// TestHoverIconTap 顶栏的图形按钮（坐席/自动接听）用的是可点击的 hoverIcon：
// 设置了 onTap 才能点，禁用后点击被拦下（applyMainCallMode 在通话中会禁用）。
func TestHoverIconTap(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	h := newHoverIcon(IconAgentSeat, 16, newTooltipLayer())
	if _, ok := interface{}(h).(fyne.Tappable); !ok {
		t.Fatal("hoverIcon 应实现 fyne.Tappable（顶栏图形按钮可点击）")
	}
	n := 0
	h.SetOnTap(func() { n++ })
	h.Tapped(&fyne.PointEvent{})
	if n != 1 {
		t.Fatalf("点击应触发回调一次，实际 %d", n)
	}
	h.Disable()
	h.Tapped(&fyne.PointEvent{})
	if n != 1 {
		t.Fatalf("禁用后点击不应触发回调，实际 %d", n)
	}
	h.Enable()
	h.Tapped(&fyne.PointEvent{})
	if n != 2 {
		t.Fatalf("重新启用后点击应恢复，实际 %d", n)
	}
}

// TestTrayIconBusy 图标颜色状态机（REQ-UI-011 修订）：未注册/注册中红、
// 已注册且坐席置忙黄、已注册且空闲绿。
func TestTrayIconBusy(t *testing.T) {
	a := &App{}
	if got := a.trayIcon(); got != IconPhoneRed {
		t.Fatalf("未注册应为红色，实际 %v", got.Name())
	}
	a.setRegState("registering")
	if got := a.trayIcon(); got != IconPhoneRed {
		t.Fatalf("首次注册中应为红色，实际 %v", got.Name())
	}
	a.setRegState("registered")
	if got := a.trayIcon(); got != IconPhoneGreen {
		t.Fatalf("已注册且空闲应为绿色，实际 %v", got.Name())
	}
	a.agentBusy = true
	if got := a.trayIcon(); got != IconPhoneYellow {
		t.Fatalf("已注册且置忙应为黄色，实际 %v", got.Name())
	}
	a.setRegState("failed")
	if got := a.trayIcon(); got != IconPhoneRed {
		t.Fatalf("注册失败应为红色（置忙也不改），实际 %v", got.Name())
	}
}

// TestStatusRowTwoIcons 顶栏状态行可以带两个图标（坐席 + 班长监听）：
// 隐藏的不占位，显示时按顺序排在文字后面。
func TestStatusRowTwoIcons(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	tip := newTooltipLayer()
	label := newPlainText("已注册", 14, theme.ForegroundColor(), true)
	agent := newHoverIcon(IconAgentSeat, 16, tip)
	monitor := newHoverIcon(IconCTIMonitor, 16, tip)
	objs := []fyne.CanvasObject{newStatusDot("registered"), label, agent, monitor}

	monitor.Hide()
	m1 := statusRowLayout{}.MinSize(objs)
	statusRowLayout{}.Layout(objs, fyne.NewSize(m1.Width, 20))
	agentRight := agent.Position().X + agent.Size().Width

	monitor.Show()
	m2 := statusRowLayout{}.MinSize(objs)
	if m2.Width != m1.Width+3+monitor.MinSize().Width {
		t.Fatalf("监听图标显示后 MinSize 应多出 3+图标宽：%v → %v", m1.Width, m2.Width)
	}
	statusRowLayout{}.Layout(objs, fyne.NewSize(m2.Width, 20))
	if monitor.Position().X+hoverIconHitPad < agentRight {
		t.Fatalf("监听图标应排在坐席图标右侧：坐席右缘 %.1f，监听 x=%.1f",
			agentRight, monitor.Position().X)
	}
}
