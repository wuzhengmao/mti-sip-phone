package ui

import (
	"fmt"
	"image"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 视频窗口（REQ-VIDEO-006/007/008）。
//
// 渲染路径：媒体层解码出 *image.RGBA → core.VideoSink → 本文件 → 写入
// canvas.Image.Image 并 Refresh()。走 canvas.Image（GL 纹理上传）而不是
// canvas.Raster（逐像素生成函数），后者在大尺寸下每帧重算会明显掉帧。
//
// ⚠️ OnVideoFrame 在**媒体层 goroutine** 上调用，绝不能阻塞：这里只做
// 「存最新帧 + 合并刷新」，真正的渲染经 fyne.Do 排到主线程；同一路流
// 至多有一个刷新在途（CAS 合帧），避免把 Fyne 的事件队列打满。

// frameRef 一帧的引用（含来源通话 ID，用于丢弃过期通话的帧）。
type frameRef struct {
	callID string
	img    *image.RGBA
}

// previewScale 本地回显占视频窗口的比例（需求 7：1/3 宽高）。
const previewScale = 3

// previewLayout 本地回显叠加布局（需求 7）：**右上角**，2px 绿色描边，
// 开关按钮贴在回显区右上角并留 previewGap 的间隙。回显隐藏时按钮**仍留在
// 原位**——否则用户就再也打不开了。
//
// 回显区尺寸按**本地摄像头的宽高比**算（宽度取窗口 1/previewScale，高度按比例
// 推），而不是取窗口的 1/3 宽高——后者与摄像头比例无关，画面会被
// ImageFillContain 补出黑边（用户反馈）。
//
// objs[0]=画面 objs[1]=描边矩形 objs[2]=开关按钮（container.New 按序绘制，
// 序号小的在下层，所以描边能盖在画面边缘上、按钮又盖在最上层）。
type previewLayout struct{ s *videoSurface }

func (previewLayout) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(0, 0) }

// previewGap 回显区距窗口右上的留白。
const previewGap = 2

// previewBtnGap 回显开关按钮距窗口右上的留白（用户指定 4px）。
const previewBtnGap = 4

// 工具栏按钮摆放：
//
//	左上角：远端画面的 [旋转][镜像]
//	右上角：本地回显的 [旋转][镜像][回显开关]（开关在最右，位置与需求 7 一致）
//
// 按钮都在**窗口**边缘而不是回显区边缘——回显区只有窗口 1/3 宽，窗口缩小后
// 三个按钮放不下（需求 7 的开关本来也是贴窗口右上角）。
func (l previewLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	var rect fyne.Position
	var sz fyne.Size
	if l.s != nil && l.s.ringFull {
		// 振铃期（本端推流、尚未接通）：本地回显**铺满整个窗口**——这时没有
		// 对端画面可看，把本地画面放大给人看；描边是给右上角小窗用的，这里不要。
		rect = fyne.NewPos(0, 0)
		sz = size
		if len(objs) > 1 && objs[1] != nil {
			objs[1].Hide()
		}
	} else {
		w := size.Width / previewScale
		h := size.Height / previewScale
		if l.s != nil && l.s.localW > 0 && l.s.localH > 0 {
			h = w * float32(l.s.localH) / float32(l.s.localW)
		}
		rect = fyne.NewPos(size.Width-w-previewGap, previewGap)
		sz = fyne.NewSize(w, h)
	}
	for i := 0; i < 2 && i < len(objs); i++ {
		if objs[i] == nil || !objs[i].Visible() {
			continue
		}
		objs[i].Move(rect)
		objs[i].Resize(sz)
	}

	place := func(o fyne.CanvasObject, px, py float32) {
		if o == nil {
			return
		}
		bs := o.MinSize()
		o.Resize(bs)
		o.Move(fyne.NewPos(px, py))
	}
	btnGap := float32(4)
	step := float32(videoToggleButtonSize) + btnGap

	// 右上角：从右往左 = 回显开关、回显镜像、回显旋转
	if l.s != nil {
		bx := size.Width - float32(videoToggleButtonSize) - previewBtnGap
		place(l.s.toggleBtn, bx, previewBtnGap)
		place(l.s.mirrorLocalBtn, bx-step, previewBtnGap)
		place(l.s.rotateLocalBtn, bx-2*step, previewBtnGap)
		// 左上角：从左往右 = 远端旋转、远端镜像、窗口置顶、窗口锁
		place(l.s.rotateRemoteBtn, previewBtnGap, previewBtnGap)
		place(l.s.mirrorRemoteBtn, previewBtnGap+step, previewBtnGap)
		place(l.s.pinBtn, previewBtnGap+2*step, previewBtnGap)
		place(l.s.lockBtn, previewBtnGap+3*step, previewBtnGap)
	}
}

// previewBorderWidth 本地回显的绿色描边宽度（用户指定 2px）。
const previewBorderWidth = 2

// videoSurface 视频画面（远端）+ 本地回显叠加层 + 旋转/镜像工具栏 + 双击全屏。
type videoSurface struct {
	widget.BaseWidget
	remote    *canvas.Image
	local     *canvas.Image
	border    *canvas.Rectangle
	toggleBtn *compactButton
	// 旋转/镜像：远端画面与本地回显各一套（左上角是远端，右上角是回显）
	rotateRemoteBtn *compactButton
	mirrorRemoteBtn *compactButton
	rotateLocalBtn  *compactButton
	mirrorLocalBtn  *compactButton
	pinBtn          *compactButton
	lockBtn         *compactButton // 窗口锁（REQ-VIDEO-015）：默认锁上，不能移动/改大小
	onDoubleTap     func()

	// localW/localH 本地回显帧的尺寸（**已含旋转**），供 previewLayout 排回显区
	localW, localH int
	// ringFull 振铃期（本端推流、尚未接通）本地回显**铺满整个窗口**：
	// 那时没有对端画面可看，先把本地画面放大给人看；接通后回到右上角小窗。
	ringFull bool
}

func newVideoSurface(onDoubleTap func()) *videoSurface {
	s := &videoSurface{onDoubleTap: onDoubleTap}
	s.remote = canvas.NewImageFromImage(nil)
	s.remote.FillMode = canvas.ImageFillContain
	// 用平滑（双线性）而不是最近邻：视频窗口通常比源分辨率大，
	// ImageScaleFastest 的最近邻放大观感就是"糊/有块"，是"清晰度不够"的直接来源。
	// GL 路径下这只是个纹理过滤开关，开销可忽略。
	s.remote.ScaleMode = canvas.ImageScaleSmooth
	// 关键：把最小尺寸压到 1×1，否则 canvas.Image 会按图片尺寸撑大窗口，
	// 用户拖动改变窗口大小也会被"内容最小尺寸"顶回去（与通话面板同样的坑）。
	s.remote.SetMinSize(fyne.NewSize(1, 1))
	s.remote.Hide()

	s.local = canvas.NewImageFromImage(nil)
	s.local.FillMode = canvas.ImageFillContain
	s.local.ScaleMode = canvas.ImageScaleFastest
	s.local.SetMinSize(fyne.NewSize(1, 1))
	s.local.Hide()

	// 回显区描边：透明填充 + 绿色描边，画在画面之上只露边框
	s.border = canvas.NewRectangle(nil)
	s.border.FillColor = nil
	s.border.StrokeColor = colorPreviewBorder
	s.border.StrokeWidth = previewBorderWidth
	s.border.Hide()

	s.toggleBtn = newCompactButton(IconVideoOff, colorHoldBlue, videoToggleButtonSize, nil)
	// 旋转 / 水平镜像：远端画面在左上角，本地回显在右上角（见 previewLayout）
	s.rotateRemoteBtn = newCompactButton(IconRotate, colorTransformGray, videoToggleButtonSize, nil)
	s.mirrorRemoteBtn = newCompactButton(IconMirror, colorTransformGray, videoToggleButtonSize, nil)
	s.rotateLocalBtn = newCompactButton(IconRotate, colorTransformGray, videoToggleButtonSize, nil)
	s.mirrorLocalBtn = newCompactButton(IconMirror, colorTransformGray, videoToggleButtonSize, nil)
	// 窗口置顶开关（用户要求）：作用对象是窗口本身，放在左上角最右
	s.pinBtn = newCompactButton(IconPin, colorTransformGray, videoToggleButtonSize, nil)
	// 窗口锁（REQ-VIDEO-015）：默认锁上（不能移动、不能改大小），与置顶同在左上角
	s.lockBtn = newCompactButton(IconLockClosed, colorHoldBlue, videoToggleButtonSize, nil)
	s.ExtendBaseWidget(s)
	return s
}

// colorPreviewBorder 本地回显描边色（绿色，与接听/成功色一致以保证观感统一）。
var colorPreviewBorder = colorSuccessGreen

func (s *videoSurface) CreateRenderer() fyne.WidgetRenderer {
	overlay := container.New(previewLayout{s: s},
		s.local, s.border, s.toggleBtn,
		s.rotateRemoteBtn, s.mirrorRemoteBtn, s.rotateLocalBtn, s.mirrorLocalBtn, s.pinBtn, s.lockBtn)
	return widget.NewSimpleRenderer(container.NewStack(s.remote, overlay))
}

func (s *videoSurface) Tapped(*fyne.PointEvent) {}

// DoubleTapped 双击切换全屏（需求 6）。
func (s *videoSurface) DoubleTapped(*fyne.PointEvent) {
	if s.onDoubleTap != nil {
		s.onDoubleTap()
	}
}

// Cursor 视频画面区域用普通箭头——整块画面是个大控件，显示手型会误导
// 用户以为整块可点。只有本地回显开关按钮（compactButton）才用手型光标。
func (s *videoSurface) Cursor() desktop.Cursor { return desktop.DefaultCursor }

// videoToggleButtonSize 回显开关按钮尺寸（比面板按钮略小，不压画面）。
const videoToggleButtonSize = 28

// --- App 侧 ---

// ensureVideoWindow 懒创建视频窗口。
func (a *App) ensureVideoWindow() {
	if a.videoW != nil {
		return
	}
	w := a.fyne.NewWindow(videoWindowTitle(""))
	w.SetIcon(IconVideoGreen) // 摄像头图标（不继承应用的红色话机图标）
	a.videoW = w
	a.videoSurface = newVideoSurface(a.toggleVideoFullscreen)
	a.videoSurface.toggleBtn.onTap = a.toggleLocalPreview
	a.videoSurface.rotateRemoteBtn.onTap = a.rotateVideoRemote
	a.videoSurface.mirrorRemoteBtn.onTap = a.mirrorVideoRemote
	a.videoSurface.rotateLocalBtn.onTap = a.rotateVideoLocal
	a.videoSurface.mirrorLocalBtn.onTap = a.mirrorVideoLocal
	a.videoSurface.pinBtn.onTap = a.toggleVideoTopmost
	a.videoSurface.lockBtn.onTap = a.toggleVideoLock
	a.applyVideoTransforms(a.core.VideoTransforms()) // 初始状态取自共享状态
	w.SetContent(a.videoSurface)
	w.SetPadded(false)
	// 默认尺寸同样按「优先原始尺寸、超屏则缩到 1/4」的规则算（首帧到达后
	// applyVideoSize 会按对端分辨率重算）。
	dw, dh := videoFitSize(videoDefaultW, videoDefaultH)
	w.Resize(fyne.NewSize(dw, dh))
	// 关闭窗口 = 需求 8 的「关闭视频」（纯本地，不发 SIP 信令）
	w.SetCloseIntercept(func() { a.setVideoEnabled(false) })
	// 刻意**不置顶**（用户要求）：视频窗口要能和别的窗口正常叠放，
	// 且它摆在通话面板上方不与之重叠，不需要靠置顶保证可见。
	//（置顶是用户手动开的开关，状态持久化；见 REQ-VIDEO-013 / REQ-VIDEO-015）

	// 置顶与锁的初始外观（两者都可能来自配置/默认锁上，按钮底色要跟上）
	a.applyVideoTopmost()
	a.applyVideoLock()
}

// videoDefaultW/H 视频窗口默认尺寸（拿到对端分辨率后按比例调整）。
const (
	videoDefaultW = 480
	videoDefaultH = 360
)

// wantVideoWindow 是否**有内容可显示**，据此决定视频窗口显隐。
//
// 规则（用户要求）：
//   - 本端在推本地视频且回显开着 → 显示——**视频呼叫一拨出就显示**（本地回显
//     立刻能看到，不等协商、不等对端画面）；对端没有视频时 sip 侧会关掉引擎
//     并上报零状态，窗口随之收起；
//   - 有远端画面 → 显示；
//   - 只有音频（普通接听、对端没视频）→ **不显示**，不留一个空窗口；
//   - 网页订阅了视频（REQ-VIDEO-009）→ 不显示，画面在网页看。
func (a *App) wantVideoWindow() bool {
	if a.webVideoOn {
		return false
	}
	if !a.videoOn || a.activeCallID == "" {
		return false
	}
	if a.callSending && a.previewOn {
		// 本端在推流且回显开着：拨出即显示（**振铃期只放本地回显**，
		// 对端画面等接通后，见 renderVideoFrame）
		return true
	}
	// 本端不发流（摄像头禁用/不可用）时没有本地回显可看：**接通后**才显示窗口
	//（用户要求：振铃期的早期媒体/回铃画面不进窗口）
	if a.uiState != uiInCall {
		return false
	}
	return a.callHasVideo && a.remoteHasVideo
}

// showVideoWindow 显示视频窗口（需求 6）。
func (a *App) showVideoWindow() {
	if !a.wantVideoWindow() {
		return
	}
	a.ensureVideoWindow()
	name := a.activeCallID
	if a.activeCall != nil {
		// 命中通信录时用联系人名称（REQ-UI-037；未命中仍是 SIP 显示名/号码）
		name, _, _ = a.callDisplay(a.activeCall)
	}
	a.videoW.SetTitle(videoWindowTitle(name))
	// **每次"打开"视频窗口都带到最前面**（用户要求）：只在隐藏→显示的那一次
	// 抢前台，之后同一通话里反复 sync 不会反复抢（避免一直打断用户操作）。
	wasShown := a.videoShown
	a.videoW.Show()
	a.videoShown = true
	if !wasShown {
		raiseVideoWindow(a.videoW, a.videoTopmost)
	}
	// 位置（REQ-VIDEO-015）：用户移动过 → 回到保存的位置；没移动过 →
	// 原有的"通话面板正上方、右边缘对齐"（只移动不改尺寸，首帧到达后
	// applyVideoSize 会带尺寸再摆一次）。
	a.placeVideoWindowAtSaved()
	// 用户此前开了置顶：窗口重新显示后再施加一次（Show 可能重置窗口管理器状态）
	if a.videoTopmost {
		setVideoWindowTopmost(a.videoW, true)
	}
	// 锁（REQ-VIDEO-015）：Show 之后句柄才存在，这里施加一次（默认锁上）；
	// 并启动位置守卫（Linux 靠轮询，Windows 靠窗口过程事件）
	setVideoWindowLock(a.videoW, a.videoLocked)
	startVideoWindowWatch(a.videoW)
}

// syncVideoWindow 按「当前有没有内容」决定窗口显隐（各处状态变化后统一调用）。
func (a *App) syncVideoWindow() {
	if a.wantVideoWindow() {
		a.showVideoWindow()
		return
	}
	// 本次通话明明有画面可显示，却因为是"有网页在看"而不显示——这是
	// REQ-VIDEO-009 的正常行为，但桌面上看不到窗口时最容易困惑，
	// 留一条日志点明原因（排查"视频窗口不出现"的第一线索）。
	if a.webVideoOn && a.videoOn && a.activeCallID != "" && (a.callSending || a.callHasVideo) {
		logx.Logger().Info("视频窗口不显示：有网页正在订阅画面（在网页上取消订阅或关掉该页面即可恢复）",
			"callSending", a.callSending, "callHasVideo", a.callHasVideo,
			"remoteHasVideo", a.remoteHasVideo)
	}
	a.hideVideoWindow()
}

// hideVideoWindow 隐藏视频窗口（挂断、需求 8 关闭视频、无内容可显示时调用）。
func (a *App) hideVideoWindow() {
	if a.videoW == nil {
		return
	}
	if a.videoFullscreen {
		a.setVideoFullscreen(false)
	}
	stopVideoWindowWatch(a.videoW) // 位置守卫只在显示期间跑（REQ-VIDEO-015）
	a.videoW.Hide()
	a.videoShown = false
	a.videoSurface.remote.Hide()
	a.videoSurface.local.Hide()
	a.videoSurface.border.Hide()
	// 画面一并清空：视频关闭/挂断后若窗口再次显示，不该先闪出上一帧的定格画面
	a.videoSurface.remote.Image = nil
	a.videoSurface.local.Image = nil
	a.videoSurface.remote.Refresh()
	a.videoSurface.local.Refresh()
	a.videoSurface.border.Refresh()
	a.videoSized = false // 下一通电话重新按对端分辨率定尺寸
}

// setVideoEnabled 需求 8：关闭/恢复视频。
func (a *App) setVideoEnabled(on bool) {
	if a.activeCallID == "" {
		return
	}
	if err := a.core.SetVideoEnabled(a.activeCallID, on); err != nil {
		logx.Logger().Warn("切换视频开关失败", "err", err)
		return
	}
	a.videoOn = on
	a.applyVideoButton()
	a.applyLocalVideoVisibility()
	a.syncVideoWindow()
}

// toggleLocalPreview 需求 7：本地回显开关。
//
// **只关本地显示，不影响推送到远端**（用户明确要求）：摄像头继续采集编码发送，
// 对端始终能看到本端画面。
func (a *App) toggleLocalPreview() {
	if a.activeCallID == "" {
		return
	}
	on := !a.previewOn
	if err := a.core.SetVideoPreview(a.activeCallID, on); err != nil {
		logx.Logger().Warn("切换本地回显失败", "err", err)
		return
	}
	a.previewOn = on
	a.applyLocalVideoVisibility()
	a.syncVideoWindow()
}

// applyLocalVideoVisibility 回显层（含绿色描边）与开关按钮的可见性。
//
// 规则（需求 7，用户明确）：
//   - 可见性由**意图**决定，不等首帧：本端要推流且回显开着就把回显区显示出来，
//     摄像头打不开时该区域是黑的（用户要求"打开失败也要显示回显区"）；
//   - 关掉回显后隐藏回显区，但**按钮保留**——否则无法再次打开；
//   - 本次通话根本不推本地视频（普通接听 / 摄像头禁用）时按钮也不显示，
//     免得出现一个点了没反应的按钮。
func (a *App) applyLocalVideoVisibility() {
	if a.videoSurface == nil {
		return
	}
	showPreview := a.videoOn && a.callSending && a.previewOn
	if showPreview {
		a.videoSurface.local.Show()
		a.videoSurface.border.Show()
	} else {
		a.videoSurface.local.Hide()
		a.videoSurface.border.Hide()
	}
	if a.videoOn && a.callSending {
		a.videoSurface.toggleBtn.Show()
	} else {
		a.videoSurface.toggleBtn.Hide()
	}
	// 本地回显整个不存在时（摄像头禁用 / 本次通话不推本地视频），回显区的
	// 旋转、镜像按钮也一并隐藏——它们作用的对象根本不存在（用户要求）。
	for _, b := range []*compactButton{a.videoSurface.rotateLocalBtn, a.videoSurface.mirrorLocalBtn} {
		if a.videoOn && a.callSending {
			b.Show()
		} else {
			b.Hide()
		}
		b.Refresh()
	}
	a.videoSurface.toggleBtn.SetIcon(iconForLocalVideo(a.previewOn))
	// 振铃期（本端推流、尚未接通）：本地回显铺满窗口；接通后回到右上角小窗
	//（用户要求）。状态变化都要重排一次，Layout 才会按新口径摆放。
	ringFull := a.uiState != uiInCall && a.videoOn && a.callSending && a.previewOn
	if a.videoSurface.ringFull != ringFull {
		a.videoSurface.ringFull = ringFull
		a.videoSurface.Refresh()
	}
	a.videoSurface.local.Refresh()
	a.videoSurface.border.Refresh()
	a.videoSurface.toggleBtn.Refresh()
}

func iconForLocalVideo(on bool) fyne.Resource {
	if on {
		return IconVideoOn
	}
	return IconVideoOff
}

// toggleVideoFullscreen 双击切换全屏（需求 6）。
func (a *App) toggleVideoFullscreen() {
	a.setVideoFullscreen(!a.videoFullscreen)
}

func (a *App) setVideoFullscreen(on bool) {
	if a.videoW == nil {
		return
	}
	// 全屏由 GLFW 直接 SetWindowPos 完成（移动+改尺寸）：锁定期间要让"锁定钳制"
	// 放行，否则会被当成用户移动而被挡回去（REQ-VIDEO-015）
	suspendVideoWindowLockClamp()
	a.videoW.SetFullScreen(on)
	resumeVideoWindowLockClamp()
	a.videoFullscreen = on
	logx.Logger().Info("视频窗口全屏切换", "fullscreen", on)
}

// videoMaxW/H 视频窗口客户区的最大尺寸（DIP）：**固定 640**，不再按屏幕大小算
// （用户要求：小屏上窗口被压得太小，大屏上又会开得过大）。
const (
	videoMaxW = 640
	videoMaxH = 640
)

// 窗口客户区的可用性下限（DIP）：只有拿不到有效分辨率时的兜底尺寸
// （正常路径下窗口恒被放到 640 那一侧，见 videoFitSize）。
const (
	videoMinW = 160
	videoMinH = 120
)

// videoFitSize 把「视频原始宽高」换算成窗口客户区尺寸（用户指定规则）：
// **在固定上限 640×640 内等比放到最大**——比 640 小的流也放大到窗口
// （用户要求：对端 192×128 的小流不该只开个小窗，要看就看清一点）；
// 长边通常恒为 640，只有宽高比极端时另一边先顶到 640（如 900×100 → 640×71）。
//
// 历史：早期是"优先 1:1、超 640 才缩"，小流就开一个很小的窗口。
func videoFitSize(fw, fh int) (float32, float32) {
	if fw <= 0 || fh <= 0 {
		return videoMinW, videoMinH
	}
	w, h := float32(fw), float32(fh)
	k := float32(videoMaxW) / w
	if kh := float32(videoMaxH) / h; kh < k {
		k = kh
	}
	return w * k, h * k
}

// applyVideoSize 按对端视频分辨率初始化窗口尺寸（需求 6）。
// applyVideoSize 按对端画面尺寸调整窗口大小（**分辨率变化时**才重设——
// 首帧、以及通话中对端换分辨率/换流都会走到这里；每帧都调会反复定位窗口）。
//
// ⚠️ 调用点必须只在"尺寸变了"时做重活（Resize + placeVideoWindow 内部要
// EnumWindows + SetWindowPos），所以这里比对上次的尺寸后早退。
func (a *App) applyVideoSize(fw, fh int) {
	if a.videoW == nil || fw <= 0 || fh <= 0 {
		return
	}
	if a.videoSized && fw == a.videoSizedW && fh == a.videoSizedH {
		return
	}
	w, h := videoFitSize(fw, fh)
	// 尺寸交给 Fyne（GLFW 设的是客户区，宽高比才与对端画面一致，不会出黑边）；
	// 位置由 placeVideoWindow 负责，内部带延迟校正以跟上异步生效的尺寸。
	a.videoW.Resize(fyne.NewSize(w, h))
	a.placeVideoWindowIfNeeded()
	a.videoSized = true
	a.videoSizedW, a.videoSizedH = fw, fh
	logx.Logger().Info("视频窗口尺寸已按原视频设置",
		"remote", fw, "x", fh, "window", int(w), "x", int(h),
		"max", videoMaxW, "x", videoMaxH)
}

// ---------------------------------------------------------------------------
// 旋转 / 水平镜像（显示用，不影响推送给对端与网页的原帧）
// ---------------------------------------------------------------------------

// transformFrame 按旋转/镜像生成显示用的帧；**不修改入参**——同一帧还会被
// 网页订阅的出口拿去（那边有自己的旋转设置），必须保持原样。
// 旋转 90°/270° 时宽高互换（"旋转后宽高也随之变化"）；无变换时原样返回。
func transformFrame(img *image.RGBA, rot int, mirror bool) *image.RGBA {
	rot = ((rot % 360) + 360) % 360
	if img == nil || (rot == 0 && !mirror) {
		return img
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	dw, dh := w, h
	if rot == 90 || rot == 270 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := 0; x < w; x++ {
			sx := x
			if mirror {
				sx = w - 1 - x
			}
			var dx, dy int
			switch rot {
			case 90:
				dx, dy = h-1-y, x
			case 180:
				dx, dy = w-1-x, h-1-y
			case 270:
				dx, dy = y, w-1-x
			default:
				dx, dy = x, y
			}
			sp := row[sx*4 : sx*4+4]
			dp := dst.Pix[dy*dst.Stride+dx*4:]
			dp[0], dp[1], dp[2], dp[3] = sp[0], sp[1], sp[2], sp[3]
		}
	}
	return dst
}

// 旋转/镜像的**权威状态在 core**（桌面与网页共用一份，见 core.VideoTransforms）：
// 点按钮只调 core，界面自己的原子量由 video.transform 事件回流更新。
// 这样网页那边改了，桌面按钮跟着变；断线重连、订阅/退订也不会丢。

func (a *App) rotateVideoRemote() { a.videoTransformCall(a.core.RotateVideo(core.VideoChannelRemote)) }
func (a *App) mirrorVideoRemote() {
	a.videoTransformCall(a.core.SetVideoMirror(core.VideoChannelRemote, !a.remoteMirror.Load()))
}
func (a *App) rotateVideoLocal() { a.videoTransformCall(a.core.RotateVideo(core.VideoChannelLocal)) }
func (a *App) mirrorVideoLocal() {
	a.videoTransformCall(a.core.SetVideoMirror(core.VideoChannelLocal, !a.localMirror.Load()))
}

// videoTransformCall 统一处理 core 的返回（失败只记日志：界面状态由事件回流）。
func (a *App) videoTransformCall(_ core.VideoTransforms, err error) {
	if err != nil {
		logx.Logger().Warn("切换画面旋转/镜像失败", "err", err)
	}
}

// applyVideoTransforms 把共享状态落到界面：原子量（供媒体 goroutine 逐帧读）、
// 按钮外观；远端旋转后宽高互换，还要按最新一帧重算窗口尺寸。
func (a *App) applyVideoTransforms(st core.VideoTransforms) {
	prev := core.VideoTransforms{
		Remote: core.VideoTransform{Rotation: int(a.remoteRot.Load()), Mirror: a.remoteMirror.Load()},
		Local:  core.VideoTransform{Rotation: int(a.localRot.Load()), Mirror: a.localMirror.Load()},
	}
	prevRemoteRot := a.remoteRot.Load()
	a.remoteRot.Store(int32(st.Remote.Rotation))
	a.remoteMirror.Store(st.Remote.Mirror)
	a.localRot.Store(int32(st.Local.Rotation))
	a.localMirror.Store(st.Local.Mirror)
	if st != prev {
		logx.Logger().Info("画面旋转/镜像已更新",
			"远端", fmt.Sprintf("旋转%d° 镜像%v", st.Remote.Rotation, st.Remote.Mirror),
			"本地", fmt.Sprintf("旋转%d° 镜像%v", st.Local.Rotation, st.Local.Mirror))
	}
	a.applyVideoTransformButtons()
	// 本地回显的旋转/镜像持久化（REQ-VIDEO-015）：桌面按钮与网页
	// video.setTransform 都汇流到这里，任一侧改动都会落盘；**远端不落盘**
	//（用户明确要求不对称）。值没变就不写盘。
	if a.cfg != nil {
		if a.cfg.UI.LocalPreviewRotation != st.Local.Rotation || a.cfg.UI.LocalPreviewMirror != st.Local.Mirror {
			a.cfg.UI.LocalPreviewRotation = st.Local.Rotation
			a.cfg.UI.LocalPreviewMirror = st.Local.Mirror
			a.persistConfig()
			logx.Logger().Info("本地回显旋转/镜像已保存",
				"rotation", st.Local.Rotation, "mirror", st.Local.Mirror)
		}
	}
	if int32(st.Remote.Rotation) != prevRemoteRot {
		a.resizeVideoWindowForRotation(int(prevRemoteRot), st.Remote.Rotation)
	}
	if a.videoSurface != nil {
		a.videoSurface.Refresh() // 回显区宽高比可能变了，重排一次
	}
}

// resizeVideoWindowForRotation 旋转按钮按下后**立刻**调整窗口形状（用户要求：
// 点按钮就要看到窗口跟着转，这样对端画面来了才装在正确的框里）。两种情况：
//
//   - 有对端画面（记过原始宽高）：按「原视频尺寸 + 当前旋转」算，与首帧到达时
//     同一套规则（优先原尺寸、上限 640）；
//   - 还没收到对端画面：把**当前窗口尺寸**按旋转的宽高奇偶变化翻转，先给出反馈
//     （对端首帧到达时 applyVideoSize 会再按真实分辨率定一次）。
func (a *App) resizeVideoWindowForRotation(oldRot, newRot int) {
	if a.videoW == nil {
		return
	}
	var w, h float32
	if sw, sh := int(a.remoteSrcW.Load()), int(a.remoteSrcH.Load()); sw > 0 && sh > 0 {
		if newRot%180 != 0 {
			w, h = videoFitSize(sh, sw) // 旋转 90/270：宽高互换
		} else {
			w, h = videoFitSize(sw, sh)
		}
	} else {
		size := a.videoW.Canvas().Size()
		w, h = size.Width, size.Height
		if (newRot/90)%2 != (oldRot/90)%2 {
			w, h = h, w
		}
	}
	if w <= 0 || h <= 0 {
		return
	}
	logx.Logger().Info("旋转后调整视频窗口形状", "rot", newRot, "window", int(w), "x", int(h))
	a.videoW.Resize(fyne.NewSize(w, h))
	a.placeVideoWindowIfNeeded()
}

// toggleVideoTopmost 视频窗口置顶开关（用户要求）：点一下置顶，再点取消。
//
// 注意视频窗口**默认不置顶**（REQ-VIDEO-006 用户明确要求），这里是用户手动
// 打开的例外；状态持久化到配置（REQ-VIDEO-015），窗口重新显示时会再应用一次。
func (a *App) toggleVideoTopmost() {
	a.videoTopmost = !a.videoTopmost
	a.applyVideoTopmost()
	if a.cfg.UI.VideoTopmost != a.videoTopmost {
		a.cfg.UI.VideoTopmost = a.videoTopmost
		a.persistConfig()
	}
	logx.Logger().Info("视频窗口置顶切换", "topmost", a.videoTopmost)
}

// applyVideoTopmost 施加置顶状态并刷新按钮外观（生效 = 蓝底）。
func (a *App) applyVideoTopmost() {
	if a.videoW != nil {
		setVideoWindowTopmost(a.videoW, a.videoTopmost)
	}
	btn := (*compactButton)(nil)
	if a.videoSurface != nil {
		btn = a.videoSurface.pinBtn
	}
	if btn != nil {
		if a.videoTopmost {
			btn.SetBg(colorHoldBlue)
		} else {
			btn.SetBg(colorTransformGray)
		}
		btn.Refresh()
	}
}

// ---- 视频窗口的锁与位置记忆（REQ-VIDEO-015）----

// toggleVideoLock 视频窗口锁开关：锁定时窗口既不能移动也不能改大小
// （默认锁上；会话内记忆，不持久化）。
func (a *App) toggleVideoLock() {
	a.videoLocked = !a.videoLocked
	a.applyVideoLock()
	logx.Logger().Info("视频窗口锁定切换", "locked", a.videoLocked)
}

// applyVideoLock 施加锁状态并刷新锁按钮外观（锁定 = 蓝底闭锁，解锁 = 灰底开锁）。
func (a *App) applyVideoLock() {
	if a.videoW != nil {
		setVideoWindowLock(a.videoW, a.videoLocked)
	}
	btn := (*compactButton)(nil)
	if a.videoSurface != nil {
		btn = a.videoSurface.lockBtn
	}
	if btn != nil {
		if a.videoLocked {
			btn.SetIcon(IconLockClosed)
			btn.SetBg(colorHoldBlue)
		} else {
			btn.SetIcon(IconLockOpen)
			btn.SetBg(colorTransformGray)
		}
		btn.Refresh()
	}
}

// placeVideoWindowIfNeeded 尺寸变化后的摆放（首帧定尺寸、旋转换形状）：
//   - 用户没移动过 → 走原有的"通话面板正上方、右边缘对齐"（尺寸变了要重新对齐）；
//   - 移动过 → **什么都不做**：窗口尺寸变化时左上角本来就不动，
//     用户摆好的位置不该被拉回面板上方（REQ-VIDEO-015）。
func (a *App) placeVideoWindowIfNeeded() {
	if a.videoW == nil || a.cfg.UI.VideoWindow.Moved {
		return
	}
	placeVideoWindow(a.videoW)
}

// placeVideoWindowAtSaved 窗口显示时的摆放：有保存位置且仍可用 → 用它；
// 否则回退默认摆放（保存值越界，例如拔掉了副屏——配置值保留，屏幕回来还能用）。
func (a *App) placeVideoWindowAtSaved() {
	if a.videoW == nil {
		return
	}
	u := a.cfg.UI.VideoWindow
	if !u.Moved {
		a.placeVideoWindowIfNeeded()
		return
	}
	scale := a.videoW.Canvas().Scale()
	sz := a.videoW.Canvas().Size()
	w, h := int(sz.Width*scale), int(sz.Height*scale)
	if sx, sy, sw, sh := screenBounds(); windowPosUsable(u.X, u.Y, w, h, sx, sy, sw, sh) {
		moveVideoWindowToSaved(a.videoW, u.X, u.Y)
		return
	}
	logx.Logger().Info("视频窗口保存的位置已不在屏幕上，回退默认摆放", "x", u.X, "y", u.Y)
	placeVideoWindow(a.videoW)
}

// onVideoWindowUserMoved 用户把视频窗口挪走了（Windows：窗口过程的
// WM_EXITSIZEMOVE；Linux：守卫轮询到的稳定位置变化）→ 记住并持久化。
// **程序化摆放绝不走这里**，否则"没移动过就跟随通话面板"的默认摆放会失效。
func (a *App) onVideoWindowUserMoved(x, y int) {
	if p := a.cfg.UI.VideoWindow; p.Moved && p.X == x && p.Y == y {
		return
	}
	a.cfg.UI.VideoWindow = config.WindowPlacement{Moved: true, X: x, Y: y}
	a.persistConfig()
	logx.Logger().Info("视频窗口位置已保存", "x", x, "y", y)
}

// applyVideoTransformButtons 旋转/镜像按钮的外观：生效中蓝底，否则灰底。
func (a *App) applyVideoTransformButtons() {
	s := a.videoSurface
	if s == nil {
		return
	}
	set := func(b *compactButton, on bool) {
		if b == nil {
			return
		}
		if on {
			b.SetBg(colorHoldBlue) // 生效中：蓝底
		} else {
			b.SetBg(colorTransformGray) // 未生效：深灰底（压在画面上，浅灰太跳）
		}
		b.Refresh()
	}
	set(s.rotateRemoteBtn, a.remoteRot.Load() != 0)
	set(s.mirrorRemoteBtn, a.remoteMirror.Load())
	set(s.rotateLocalBtn, a.localRot.Load() != 0)
	set(s.mirrorLocalBtn, a.localMirror.Load())
}

// --- core.VideoSink 实现 ---

// OnVideoFrame 视频帧回调（core.VideoSink）。
// 合并刷新：媒体层每帧都调它，但主线程刷新最多一个在途，中间帧直接被
// 覆盖（视频允许丢帧，绝不反过来拖住解码循环）。
// **在媒体层 goroutine 上同步调用**，只做存帧与排队，绝不阻塞。
func (a *App) OnVideoFrame(callID string, local bool, img *image.RGBA) {
	if img == nil {
		return
	}
	// 显示用的旋转/镜像（在媒体 goroutine 上做，不占主线程；
	// 不修改原帧——网页订阅的出口拿的是同一块数据，它有自己的一套设置）
	if local {
		img = transformFrame(img, int(a.localRot.Load()), a.localMirror.Load())
	} else {
		// 记下**原始**宽高：旋转按钮要按"原视频尺寸 + 当前旋转"算窗口形状，
		// 而存下来的帧已经是旋转过的（用它算会得到旋转前的形状）
		a.remoteSrcW.Store(int32(img.Rect.Dx()))
		a.remoteSrcH.Store(int32(img.Rect.Dy()))
		img = transformFrame(img, int(a.remoteRot.Load()), a.remoteMirror.Load())
	}
	ref := &frameRef{callID: callID, img: img}
	var pending *atomic.Bool
	var slot *atomic.Pointer[frameRef]
	if local {
		slot, pending = &a.localFrame, &a.localPending
	} else {
		slot, pending = &a.remoteFrame, &a.remotePending
	}
	slot.Store(ref)
	if !pending.CompareAndSwap(false, true) {
		return // 已有刷新在途，本帧覆盖即可
	}
	fyne.Do(func() {
		pending.Store(false)
		a.renderVideoFrame(local)
	})
}

// renderVideoFrame 在主线程把最新帧写入画布（合并刷新的落地处）。
func (a *App) renderVideoFrame(local bool) {
	// 视频窗口是懒创建的，而 wantVideoWindow 又要求"先有内容才显示"——
	// 若这里直接因 surface 为空返回，就会形成死锁：帧被丢 → remoteHasVideo
	// 永远为 false → 窗口永远不建（摄像头禁用、callSending=false 时必然踩中，
	// 表现为"远端的画面能收到却没有视频窗口"）。所以首帧到达时就地补建窗口，
	// 但**仍然不 Show**——显示与否交给 syncVideoWindow 判断。
	if a.videoSurface == nil || a.videoW == nil {
		a.ensureVideoWindow()
		if a.videoSurface == nil || a.videoW == nil {
			return
		}
	}
	var ref *frameRef
	if local {
		ref = a.localFrame.Load()
	} else {
		ref = a.remoteFrame.Load()
	}
	if ref == nil || ref.callID != a.activeCallID {
		return // 过期通话的帧，丢弃
	}
	if a.webVideoOn {
		// 网页正在看画面：软电话不再显示视频窗口，帧**也不落画布**。
		// 这里必须短路——renderVideoFrame 里还有直接 Show() 的分支，
		// 只靠 wantVideoWindow 挡不住（窗口已存在时会被逐帧显示回来）。
		return
	}

	if local {
		if !a.localVideoEver {
			a.localVideoEver = true
			logx.Logger().Info("本地视频首帧已到达界面", "callId", callIDOf(ref),
				"size", imgSize(ref))
		}
		// 回显关着时**不能 Show**——否则每来一帧都会把用户刚关掉的回显又显示回来
		if a.videoOn && a.callSending && a.previewOn {
			// 本地帧尺寸变化时让整个叠加层重排一次（回显区按摄像头宽高比算，
			// 只 Refresh 画面本身不会触发父容器重新布局）
			w, h := ref.img.Rect.Dx(), ref.img.Rect.Dy()
			if w != a.videoSurface.localW || h != a.videoSurface.localH {
				a.videoSurface.localW, a.videoSurface.localH = w, h
				defer a.videoSurface.Refresh()
			}
			// 振铃期窗口里只有本地回显（ringFull）：窗口尺寸按**本地画面**适配
			// ——否则窗口会停在默认尺寸或上一通电话留下的尺寸（表现为"振铃期
			// 窗口跟本地画面不匹配"，接通后被对端画面纠正）。旋转本地画面时
			// 帧尺寸随宽高互换变化，这里同样会重设窗口形状。
			if a.videoSurface.ringFull {
				a.applyVideoSize(w, h)
			}
			a.videoSurface.local.Image = ref.img
			a.videoSurface.local.Show()
			a.videoSurface.border.Show()
			a.videoSurface.local.Refresh()
			a.videoSurface.border.Refresh()
		}
		return
	}

	if !a.videoOn {
		return
	}
	// 对端画面**接通后才放**（用户要求）：振铃期窗口里只有本地回显，
	// 早期媒体/回铃画面不显示——帧照常存下（remoteHasVideo 标记照常置位），
	// 接通后由首帧或 call.established 的同步把它显示出来。
	if a.uiState != uiInCall {
		a.videoSurface.remote.Hide()
		return
	}
	a.videoSurface.remote.Image = ref.img
	a.videoSurface.remote.Show()
	a.videoSurface.remote.Refresh()

	// 尺寸适配：分辨率**变化**时重设窗口（首帧、对端换分辨率/换流；不变则直接早退）
	a.applyVideoSize(ref.img.Rect.Dx(), ref.img.Rect.Dy())

	if !a.remoteHasVideo {
		a.remoteHasVideo = true
		logx.Logger().Info("远端视频首帧已到达界面", "callId", callIDOf(ref),
			"size", imgSize(ref))
		// 首次收到对端画面：定好窗口大小后显示窗口。
		//
		// ⚠️ 这几个调用只能放在"首次"分支里：showVideoWindow 会 SetTitle +
		// Show + moveVideoWindowAboveCallPanel（内部 EnumWindows 找窗口 + SetWindowPos），
		// 每帧来一遍会直接把 UI 拖垮。状态变化时另有 call.video / 开关回调触发。
		a.applyLocalVideoVisibility()
		a.syncVideoWindow()
	}
}

// callIDOf / imgSize 诊断日志用的小工具。
func callIDOf(f *frameRef) string {
	if f == nil {
		return ""
	}
	return f.callID
}

func imgSize(f *frameRef) string {
	if f == nil || f.img == nil {
		return ""
	}
	return fmt.Sprintf("%dx%d", f.img.Rect.Dx(), f.img.Rect.Dy())
}

// resetVideoUI 通话状态重置时清掉视频相关的界面状态。
func (a *App) resetVideoUI() {
	a.videoOn = false
	a.previewOn = true
	a.callHasVideo = false
	a.callSending = false
	a.remoteHasVideo = false
	a.localVideoEver = false
	a.videoSized = false
	a.videoFullscreen = false
	a.remoteFrame.Store(nil)
	a.localFrame.Store(nil)
	a.hideVideoWindow()
	a.applyVideoButton()
}

// applyVideoButton 刷新通话面板「关闭视频」按钮的图标与底色（需求 8）。
func (a *App) applyVideoButton() {
	if a.videoBtn == nil {
		return
	}
	if a.videoOn {
		a.videoBtn.SetIcon(IconVideoOn)
		a.videoBtn.SetBg(colorHoldBlue)
	} else {
		a.videoBtn.SetIcon(IconVideoOff)
		a.videoBtn.SetBg(colorSwitchGray)
	}
	a.videoBtn.Refresh()
}
