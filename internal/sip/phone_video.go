package sip

import (
	"image"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/media"
	"mti-sip-phone/internal/video"
)

// 视频呼叫的 SIP 层集成（REQ-VIDEO-*）。
//
// 生命周期总览：
//
//	呼出   dial(video) → 建 VideoEngine → offer(含 m=video) → 2xx 时 ApplyAnswer+Start
//	呼入   handleIncoming → 建 VideoEngine + ApplyOffer → 接听时构造 answer → Start
//	挂断   endCallImmediate / cleanupCall 统一 closeVideo
//	保持   随音频一起停发（不发 re-INVITE 改视频段，避免与既有 re-INVITE 逻辑纠缠）
//
// 需求 7（本地回显开关）与需求 8（关闭视频）都是**纯本地动作**，不发 SIP 信令——
// 与麦克风/扬声器开关同语义，规避 re-INVITE 的 ACK/CSeq 竞态。

// SetVideoConfig 下发视频配置（启动时调用一次；摄像头选择变更时再调）。
func (p *Phone) SetVideoConfig(cfg config.VideoConfig) {
	p.videoMu.Lock()
	p.videoCfg = cfg
	p.videoMu.Unlock()
}

func (p *Phone) videoConfig() config.VideoConfig {
	p.videoMu.RLock()
	defer p.videoMu.RUnlock()
	return p.videoCfg
}

// cameraUsable 摄像头是否可用于发送本地视频（构建支持 + 未被禁用）。
func (p *Phone) cameraUsable() bool {
	// 三道门：本构建有视频能力、**本平台有摄像头后端**（Windows 386 没有，
	// 见 video.CameraSupported）、配置里没禁用摄像头。
	return p.VideoSupported() && video.CameraSupported() && p.videoConfig().CameraUsable()
}

// CameraSupported 本构建/平台是否有摄像头采集能力（供界面区分"没插摄像头"
// 与"这个版本压根不支持摄像头"）。
func (p *Phone) CameraSupported() bool { return video.CameraSupported() }

// VideoSupported 当前构建是否具备视频能力。
func (p *Phone) VideoSupported() bool { return video.Supported() }

// SetCamera 选择摄像头（REQ-VIDEO-002/005）。
// deviceID 为空 = 系统默认；config.CameraDisabled = 禁用。
func (p *Phone) SetCamera(deviceID string) error {
	p.videoMu.Lock()
	p.videoCfg.CameraID = deviceID
	p.videoMu.Unlock()
	logx.Logger().Info("摄像头已切换", "id", deviceID)
	return nil
}

// VideoDevices 枚举摄像头（可能耗时，调用方放后台 goroutine）。
func (p *Phone) VideoDevices() []core.VideoDevice {
	cams, err := video.ListCameras()
	if err != nil {
		logx.Logger().Warn("枚举摄像头失败", "err", err)
		return nil
	}
	out := make([]core.VideoDevice, 0, len(cams))
	for _, c := range cams {
		out = append(out, core.VideoDevice{ID: c.ID, Name: c.Name})
	}
	return out
}

// videoSinkFor 本通话的视频帧出口。帧**不走事件总线**（总线满即丢，
// 视频帧会把它打满），经 core.App 转发给界面层。
//
// ⚠️ 这里**不能**直接把 p.sink 断言成 core.VideoSink：p.sink 是 core.App
// （只实现 EventSink），实现 VideoSink 的是界面层。必须通过
// core.App.VideoFrameSink() 取界面注册的出口——否则断言恒失败、
// 出口恒为 nil，帧被静默丢弃（本地与远端画面全黑）。
func (p *Phone) videoSinkFor(callID string) media.FrameSink {
	prov, ok := p.sink.(interface{ VideoFrameSink() core.VideoSink })
	if !ok {
		return nil
	}
	sink := prov.VideoFrameSink()
	if sink == nil {
		return nil
	}
	return func(local bool, img *image.RGBA) {
		sink.OnVideoFrame(callID, local, img)
	}
}

// localWantedFunc 本地帧的额外消费者判定（网页订阅本地画面，REQ-VIDEO-009）。
//
// 界面回显关掉时，本地帧的 YUV→RGBA 转换本来会整个省掉；但网页若订阅了本地
// 画面，画面就不该跟着黑掉——把 core.App 的判定注入媒体层，由它逐帧决定。
// 取不到（p.sink 不是 core.App）时返回 nil，行为与从前一致。
func (p *Phone) localWantedFunc() func() bool {
	prov, ok := p.sink.(interface{ LocalVideoWanted() bool })
	if !ok {
		return nil
	}
	return prov.LocalVideoWanted
}

// notifyVideoState 上报视频状态（低频，走事件总线没问题）。
//
// 引擎已回收（对端没有视频 / answer 拒绝视频）时上报**全零状态**：视频呼叫
// 是"拨出即开摄像头+回显"的，对端没有视频时必须能让界面把视频窗口收回去，
// 否则会停在"发送中/回显中"的旧状态上、摄像头也白开。
func (p *Phone) notifyVideoState(cs *callSession) {
	if cs == nil {
		return
	}
	if cs.video == nil {
		p.sink.OnVideoState(cs.id, core.VideoState{})
		return
	}
	w, h, _ := cs.video.RemoteSize()
	st := core.VideoState{
		Negotiated: cs.video.Available(),
		Enabled:    cs.video.IsEnabled(),
		PreviewOn:  cs.video.IsPreviewOn(),
		Sending:    cs.video.IsSending(),
		Width:      w,
		Height:     h,
	}
	p.sink.OnVideoState(cs.id, st)
}

// IncomingHasVideo 该来电的 offer 是否含可用视频段。
func (p *Phone) IncomingHasVideo(callID string) bool {
	cs := p.getCall(callID)
	if cs == nil || cs.offer == nil {
		return false
	}
	return cs.offer.Has("video") && cs.video != nil
}

// VideoActive 该通话的视频是否已协商成功且处于开启状态。
func (p *Phone) VideoActive(callID string) bool {
	cs := p.getCall(callID)
	return cs != nil && cs.video != nil && cs.video.Available() && cs.video.IsEnabled()
}

// SetVideoEnabled 需求 8：关闭/恢复视频（远端不显示、本地不推送、视频窗口关闭）。
// 纯本地动作，不发 SIP 信令。
func (p *Phone) SetVideoEnabled(callID string, on bool) error {
	cs := p.getCall(callID)
	if cs == nil {
		return core.ErrCallNotFound
	}
	if cs.video == nil {
		return core.ErrVideoUnsupported
	}
	cs.video.SetEnabled(on)
	p.notifyVideoState(cs)
	return nil
}

// SetVideoPreview 需求 7：本地视频回显与推送开关。
func (p *Phone) SetVideoPreview(callID string, on bool) error {
	cs := p.getCall(callID)
	if cs == nil {
		return core.ErrCallNotFound
	}
	if cs.video == nil {
		return core.ErrVideoUnsupported
	}
	cs.video.SetVideoPreview(on)
	p.notifyVideoState(cs)
	return nil
}

// AnswerVideo 视频接听：打开摄像头，双向视频（REQ-VIDEO-004）。
func (p *Phone) AnswerVideo(callID string) error {
	return p.answerIncoming(callID, true)
}

// answerIncoming 接听呼入。withVideo=true 时打开摄像头推本地视频；
// false（普通接听）时视频段方向为 recvonly——**只接收对方的视频**（REQ-VIDEO-004）。
//
// 注意：sipgo 的 RespondSDP 发送 200 后按 RFC 6026 阻塞等待对端 ACK
// （最长 64*T1=32 秒），必须放在后台 goroutine 执行，否则阻塞 UI 线程
// 导致界面一直停留在振铃状态。
func (p *Phone) answerIncoming(callID string, withVideo bool) error {
	cs := p.getCall(callID)
	if cs == nil || cs.dialogS == nil {
		return core.ErrCallNotFound
	}
	go func() {
		answer := p.buildAnswer(cs, withVideo)
		if !withVideo {
			// 不接受视频：立刻回收视频引擎（answer 已置 0，不必也不能再收流），
			// 这样 UI 侧的 Video.Negotiated 恒为 false，不会弹视频窗口。
			p.closeVideo(cs)
		}
		if err := cs.dialogS.RespondSDP([]byte(answer)); err != nil {
			logx.Logger().Error("发送 200 OK 失败", "callId", callID, "err", err)
			p.cleanupCall(cs.id, "failed")
			return
		}
		// RespondSDP 阻塞等待 ACK 期间，通话可能已被对端 BYE / 本地挂断
		// 清理（cleanupCall 会取消 cs.ctx）。此时不能继续接通流程，
		// 否则界面会重新进入"通话中"且音频引擎空转（竞态 bug 修复）。
		if cs.ctx.Err() != nil {
			logx.Logger().Info("接听完成前通话已结束，跳过接通流程", "callId", callID)
			return
		}
		cs.established = true
		p.attachAudio(cs.engine)
		cs.engine.Start()
		p.startVideo(cs, withVideo)
		p.sink.OnEstablished(cs.id)
	}()
	logx.Logger().Info("已接听来电（后台发送 200 OK）", "callId", callID, "withVideo", withVideo)
	return nil
}

// buildAnswer 构造 200 OK 的 SDP。
//
// withVideo=false（点普通「接听」）= **本次通话不接受视频**：answer 里把
// m=video 的端口置 0（RFC 3264 的拒绝写法）。这与"只收不发"的 recvonly 不同
// ——对端不会再发视频流，本端自然也不建视频窗口（用户明确要求）。
// 对端 offer 没有 m=video 时走原来的纯音频路径（输出逐字节不变）。
func (p *Phone) buildAnswer(cs *callSession, withVideo bool) string {
	if cs.offer == nil {
		return cs.engine.SDPNegotiatedAnswer()
	}
	info, err := cs.offer.Media("video")
	if err != nil || info.Rejected {
		return cs.engine.SDPNegotiatedAnswer()
	}
	if !withVideo {
		// v 传 nil → SDPAnswerFor 输出 m=video 0 拒绝段，
		// 且仍保持与 offer 一致的 m= 行顺序与数量。
		return media.SDPAnswerFor(cs.engine, nil, cs.offer, cs.engine.PayloadType(), "")
	}
	dir := videoAnswerDirection(info.Dir, p.cameraUsable())
	return media.SDPAnswerFor(cs.engine, cs.video, cs.offer, cs.engine.PayloadType(), dir)
}

// videoAnswerDirection RFC 3264 方向镜像：
//
//	offer sendrecv + 本端能发 → sendrecv；不能发 → recvonly
//	offer sendonly             → recvonly（对端只发不收）
//	offer recvonly + 能发      → sendonly；不能发 → inactive
//	offer inactive             → inactive
func videoAnswerDirection(offerDir string, canSend bool) string {
	switch offerDir {
	case "sendonly":
		return "recvonly"
	case "recvonly":
		if canSend {
			return "sendonly"
		}
		return "inactive"
	case "inactive":
		return "inactive"
	default: // sendrecv
		if canSend {
			return "sendrecv"
		}
		return "recvonly"
	}
}

// startVideo 接通后启动视频收发（幂等）。
// sendLocal 为 true 且摄像头可用时才推送本地视频。
func (p *Phone) startVideo(cs *callSession, sendLocal bool) {
	cs.videoMu.Lock()
	if cs.video == nil || cs.videoReady {
		cs.videoMu.Unlock()
		return
	}
	cs.videoReady = true
	v := cs.video
	cs.videoMu.Unlock()

	// 顺序很重要：先给采集参数（否则监管协程可能用空 ID 抢开默认摄像头），
	// 再放开发送开关，最后 Start。
	send := sendLocal && p.cameraUsable()
	sink := p.videoSinkFor(cs.id)
	if sink == nil {
		// 出口为空 = 画面一定不显示。出口经 core.App.VideoFrameSink() 多播，
		// **恒不为 nil**（无订阅者时静默丢帧），所以这里只可能是 p.sink 不是
		// core.App（组装错误）。这条日志是排查"画面一片黑"的第一线索。
		logx.Logger().Warn("取不到视频帧出口，画面不会显示（p.sink 不是 core.App）", "callId", cs.id)
	}
	v.SetFrameSink(sink)
	v.SetLocalWantedFunc(p.localWantedFunc())
	if send {
		vc := p.videoConfig()
		v.StartCamera(vc.CameraID, vc.CameraName, vc.Width, vc.Height, vc.FPS, vc.BitrateKbps*1000)
	}
	v.SetSendEnabled(send)
	v.SetEnabled(true)
	v.SetStateCallback(func() { p.notifyVideoState(cs) })
	v.Start()
	logx.Logger().Info("视频引擎已启动",
		"callId", cs.id, "sendLocal", send, "codec", codecName(v))
	p.notifyVideoState(cs)
}

// codecName 已协商的视频编码名（日志用）。
func codecName(v *media.VideoEngine) string {
	if c, ok := v.NegotiatedCodec(); ok {
		return c.Name
	}
	return "无"
}

// closeVideo 关闭本通话的视频引擎（幂等；挂断/清理路径统一走这里）。
func (p *Phone) closeVideo(cs *callSession) {
	if cs == nil {
		return
	}
	cs.videoMu.Lock()
	v := cs.video
	cs.video = nil
	cs.videoReady = false
	cs.videoMu.Unlock()
	if v != nil {
		v.Close()
	}
}
