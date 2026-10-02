// Package sip 基于 sipgo 实现 SIP 软电话：注册、呼出、呼入、接听、挂断。
// Phone 实现 core.Telephony 接口，对接 core.App 与 media 引擎。
package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"

	"mti-sip-phone/internal/audio"
	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/media"
)

// Phone SIP 软电话实现：持有 UA/Client/Server，管理注册与通话会话。
type Phone struct {
	sink  core.EventSink
	audio *audio.Manager

	ua     *sipgo.UserAgent
	client *sipgo.Client
	server *sipgo.Server
	dcc    *sipgo.DialogClientCache // 呼出 dialog
	dsc    *sipgo.DialogServerCache // 呼入 dialog

	mu        sync.Mutex
	acc       config.Account
	localPort int    // 本地 SIP 监听端口（Contact 用）
	publicIP  string // STUN 探测的公网 IP（NAT 穿透，Contact/SDP 用）

	regCancel context.CancelFunc

	calls map[string]*callSession

	autoAnswer atomic.Bool // 自动接听开关

	videoMu  sync.RWMutex
	videoCfg config.VideoConfig // 摄像头与视频参数（REQ-VIDEO-002/005）
}

// callSession 一次通话的运行时状态。
type callSession struct {
	id           string
	dir          string // incoming / outgoing
	remote       string
	remoteName   string
	engine       *media.Engine
	dialogC      *sipgo.DialogClientSession // 呼出
	dialogS      *sipgo.DialogServerSession // 呼入
	ctx          context.Context            // 会话上下文：挂断/清理时取消，用于竞态判断
	cancel       context.CancelFunc
	established  bool
	holdMu       sync.Mutex // 保持 re-INVITE 串行化（CSeq 必须严格递增）
	mediaStarted bool       // 媒体引擎已启动（183 早期媒体可能提前启动）

	// ---- 视频 ----
	video      *media.VideoEngine // nil = 本次通话没有视频
	offer      *media.Offer       // 呼入：对端 INVITE 的解析结果（接听时组合 answer 用）
	videoSend  bool               // 本端是否推送本地视频（视频接听/视频呼出且有摄像头）
	videoReady bool               // 视频引擎已 Start（防重复启动，对应 mediaStarted）
	videoMu    sync.Mutex         // 视频引擎创建/启动串行化
}

// UserAgentName 软电话标识（REQ-ACC-014）：格式 "mti-sip-phone v1.0.3"。
func UserAgentName() string {
	return "mti-sip-phone v" + core.Version
}

// NewPhone 创建软电话。sink 用于向核心层上报事件；audioMgr 为 nil 时
// 跳过音频设备对接（用于测试或无音频环境）。
//
// 注意：创建后必须调用 ListenAndServe 监听本地端口（呼入通道与注册
// 响应的接收端），否则只能呼出、无法收到任何呼入。
// 客户端出向 socket 由 sipgo 动态分配（避免 WithClientPort 导致
// 服务器按固定端口回包而无人监听的超时问题）。
func NewPhone(sink core.EventSink, audioMgr *audio.Manager, useRPort bool) (*Phone, error) {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent(UserAgentName()))
	if err != nil {
		return nil, fmt.Errorf("创建 UA 失败: %w", err)
	}
	// rport（REQ-ACC-016）：WithClientNAT 使 Via 携带 rport 参数并复用
	// 源端口（NAT 穿透），按配置开关（默认开启）。
	var opts []sipgo.ClientOption
	if useRPort {
		opts = append(opts, sipgo.WithClientNAT())
	}
	client, err := sipgo.NewClient(ua, opts...)
	if err != nil {
		return nil, fmt.Errorf("创建 SIP 客户端失败: %w", err)
	}
	server, err := sipgo.NewServer(ua)
	if err != nil {
		return nil, fmt.Errorf("创建 SIP 服务端失败: %w", err)
	}

	p := &Phone{
		sink:   sink,
		audio:  audioMgr,
		ua:     ua,
		client: client,
		server: server,
		calls:  make(map[string]*callSession),
	}
	p.setupHandlers()
	logx.Logger().Info("SIP 软电话已创建", "hostname", client.Hostname())
	return p, nil
}

// ListenAndServe 监听本地 SIP 端口（呼入通道）。addr 如 "0.0.0.0:5060"，
// network 为 udp / tcp。监听端口被记录用于 Contact 头构造。
// 应分别对 UDP 与 TCP 调用（各起一个 goroutine）。
func (p *Phone) ListenAndServe(ctx context.Context, network, addr string) error {
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		if port, err := strconv.Atoi(portStr); err == nil && p.localPort == 0 {
			p.localPort = port
			logx.Logger().Info("本地 SIP 监听端口", "network", network, "port", port)
		}
	}
	return p.server.ListenAndServe(ctx, network, addr)
}

// contactURI 构造 Contact 头地址：账号@本机IP:监听端口。
func (p *Phone) contactURI(user string) sip.Uri {
	u := sip.Uri{User: user, Host: p.localHost()}
	if p.localPort > 0 {
		u.Port = p.localPort
	}
	return u
}

// setupHandlers 注册呼入处理。
func (p *Phone) setupHandlers() {
	// 呼入 INVITE：必须在 handler 内同步处理整个来电生命周期——
	// sipgo 的服务端事务在 handler 返回后即被终止（若用 go 启动
	// goroutine，事务会立即 "transaction terminated"）。handler 本身
	// 运行在事务独立 goroutine 中，阻塞等待接听是安全的。
	p.server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// in-dialog re-INVITE（To 带 tag）与初始 INVITE 分流：
		// re-INVITE 用于通话中保持/恢复的媒体方向变更
		if req.To() != nil && req.To().Params != nil {
			if _, ok := req.To().Params.Get("tag"); ok {
				p.handleReInvite(req, tx)
				return
			}
		}
		p.handleIncoming(req, tx)
	})
	// 呼入 CANCEL（对端取消振铃中的呼入）
	p.server.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		go p.handleIncomingCancel(req, tx)
	})
	// 对端 BYE
	p.server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		go p.handleRemoteBye(req, tx)
	})
	// 对端 ACK：必须匹配 dialog 并通知（sipgo 的 RespondSDP 发送 200 后
	// 按 RFC 6026 阻塞等待 ACK；无此处理则 2xx 重传至超时且接听流程卡死）
	p.server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		if p.dsc == nil {
			logx.Logger().Warn("ACK 到达但 dialog 缓存未初始化")
			return
		}
		if err := p.dsc.ReadAck(req, tx); err != nil {
			logx.Logger().Warn("ACK 匹配失败", "err", err)
		}
	})
}

// ---------------------------------------------------------------------------
// core.Telephony 实现
// ---------------------------------------------------------------------------

// Register 发起注册（阻塞直到第一次注册结果确定，后续续注册在后台进行）。
func (p *Phone) Register(ctx context.Context, acc config.Account) error {
	p.mu.Lock()
	p.acc = acc
	p.mu.Unlock()

	if p.regCancel != nil {
		p.regCancel()
	}
	regCtx, cancel := context.WithCancel(ctx)
	p.regCancel = cancel

	// STUN 公网地址探测（启用时，REQ-ACC-017）：后台执行，
	// 探测成功后通过 stunCh 通知注册循环立即重新注册，
	// 让 Contact 携带公网地址（NAT 穿透）。
	stunCh := make(chan struct{}, 1)
	go func() {
		if !acc.STUNEnabled || acc.STUNServer == "" {
			return
		}
		ip, err := stunDiscover(acc.STUNServer)
		if err != nil {
			logx.Logger().Warn("STUN 探测失败", "err", err)
			return
		}
		p.mu.Lock()
		p.publicIP = ip
		p.mu.Unlock()
		// 通知注册循环重注册（sendRegister 内部会发布"注册中"状态）
		select {
		case stunCh <- struct{}{}:
		default:
		}
	}()

	go p.registerLoop(regCtx, acc, stunCh)
	return nil
}

// Unregister 注销。
func (p *Phone) Unregister(ctx context.Context) error {
	if p.regCancel != nil {
		p.regCancel()
		p.regCancel = nil
	}
	// 发送 expires=0 的 REGISTER 通知服务端
	if _, err := p.sendRegister(ctx, p.acc, 0); err != nil {
		logx.Logger().Debug("注销 REGISTER 发送失败", "err", err)
	}
	p.sink.OnRegisterState(core.RegUnregistered, "")
	return nil
}

// Dial 呼出（纯音频）。
func (p *Phone) Dial(ctx context.Context, target string) (string, error) {
	return p.dial(ctx, target, false)
}

// DialVideo 发起视频呼叫：offer 同时含 m=audio 与 m=video。
// 摄像头被禁用时视频段方向为 recvonly（只收不发，REQ-VIDEO-005）。
func (p *Phone) DialVideo(ctx context.Context, target string) (string, error) {
	return p.dial(ctx, target, true)
}

func (p *Phone) dial(ctx context.Context, target string, withVideo bool) (string, error) {
	p.mu.Lock()
	acc := p.acc
	p.mu.Unlock()
	if acc.Server == "" || acc.Account == "" {
		return "", fmt.Errorf("未配置 SIP 账户")
	}

	// 目标处理：号码 → sip:号码@服务器；sip: URI 原样使用
	recipient, err := parseTarget(target, acc)
	if err != nil {
		return "", err
	}

	// 创建媒体引擎与 SDP offer
	engine, err := media.NewEngine(p.localIP())
	if err != nil {
		return "", err
	}
	// SDP 宣告地址使用对端可达地址（NAT：注册回显地址 / STUN 公网地址）
	engine.SetSDPIP(p.localHost())

	callID := uuid.NewString()
	callCtx, cancel := context.WithCancel(ctx)
	cs := &callSession{
		id:     callID,
		dir:    "outgoing",
		remote: recipient.User,
		engine: engine,
		ctx:    callCtx,
		cancel: cancel,
	}

	// 视频：仅当本构建有视频能力且本次是视频呼叫时创建视频引擎
	if withVideo {
		if !p.VideoSupported() {
			cancel()
			engine.Close()
			return "", core.ErrVideoUnsupported
		}
		v, err := media.NewVideoEngine(p.localIP())
		if err != nil {
			cancel()
			engine.Close()
			return "", err
		}
		v.SetSDPIP(p.localHost())
		v.SetFrameSink(p.videoSinkFor(callID))
		v.SetSendEnabled(p.cameraUsable())
		cs.video = v
		cs.videoSend = p.cameraUsable()
	}

	p.mu.Lock()
	p.calls[callID] = cs
	p.mu.Unlock()

	offer := media.SDPOfferFor(engine, cs.video, "sendrecv", p.videoOfferDirection(cs.video))
	dlg, err := p.dccInvite(recipient, offer, acc)
	if err != nil {
		cancel()
		p.mu.Lock()
		delete(p.calls, callID)
		p.mu.Unlock()
		if cs.video != nil {
			cs.video.Close()
		}
		engine.Close()
		return "", err
	}
	cs.dialogC = dlg

	// 视频呼叫：**一拨出就打开摄像头与本地回显**（用户要求），不等对端 answer。
	// 对端 answer 里没有视频时，applyVideoAnswer 会关掉引擎与摄像头，
	// 并上报全零视频状态让界面收掉视频窗口。
	if cs.video != nil {
		p.startVideo(cs, cs.videoSend)
	}

	go p.outgoingFlow(callCtx, cs, acc)
	return callID, nil
}

// videoOfferDirection 本端 offer 里视频段的方向：
// 能推本地视频 → sendrecv；只收不发 → recvonly（摄像头禁用/不可用）。
func (p *Phone) videoOfferDirection(v *media.VideoEngine) string {
	if v == nil {
		return ""
	}
	if p.cameraUsable() {
		return "sendrecv"
	}
	return "recvonly"
}

// Answer 接听呼入（普通接听）。
// 需求 4：只点普通接听时**不打开摄像头**，只接收对方的视频（视频段方向 recvonly）。
//
// 实现见 phone_video.go 的 answerIncoming（后台发送 200 OK 的原因见那里）。
func (p *Phone) Answer(callID string) error {
	return p.answerIncoming(callID, false)
}

// attachAudio 接通后把媒体引擎对接音频设备（audio 为 nil 时跳过）。
func (p *Phone) attachAudio(engine *media.Engine) {
	if p.audio != nil {
		p.audio.AttachEngine(engine)
	}
}

// Hangup 挂断：本地状态与界面立即结束（不等待对端 200/ACK），
// 信令（BYE / 603 / CANCEL）在后台 goroutine 发送。
func (p *Phone) Hangup(callID string) error {
	cs := p.getCall(callID)
	if cs == nil {
		return core.ErrCallNotFound
	}
	// 信令后台发送（BYE/603 的响应等待不能阻塞调用方 UI 线程）。
	// 信令分支按 SIP dialog 实际状态判断，不用 cs.established——
	// 200 OK 发出后到本地标志置位之间存在竞态窗口（如呼入接听中、
	// 呼出 2xx 已收未 ACK），期间挂断若误判为"振铃中"会漏发 BYE
	// 或误发 603，导致对端不挂断（bug 修复）。
	go func() {
		if cs.dialogS != nil && cs.dialogS.LoadState() < sip.DialogStateEstablished {
			// 呼入振铃中挂断 = 拒接：回 603 通知对端。
			// 603 必须先于上下文取消发出——handleIncoming 一旦退出，
			// sipgo 对尚无最终响应的 INVITE 事务 TerminateGracefully
			// 会立即 Terminate，此后 603 必然发送失败（bug 修复）。
			if err := cs.dialogS.Respond(603, "Decline", nil); err != nil {
				logx.Logger().Debug("发送拒接响应失败", "err", err)
			}
			logx.Logger().Info("振铃中拒接来电（603）", "callId", callID)
			if cs.cancel != nil {
				cs.cancel() // 603 已发出，再结束 handleIncoming 等待
			}
			_ = cs.dialogS.Close()
			return
		}
		if cs.cancel != nil {
			// 呼出振铃中：sipgo 的 WaitAnswer 收到取消后自动发 CANCEL；
			// 其余场景取消会话上下文用于竞态判断。
			cs.cancel()
		}
		switch {
		case cs.dialogC != nil:
			// 呼出：已接通发 BYE；振铃中的取消由 outgoingFlow 统一处理
			//（WaitAnswer 收到 ctx 取消自动发 CANCEL；2xx 后补 ACK+BYE）
			if cs.established {
				if err := cs.dialogC.Bye(context.Background()); err != nil {
					logx.Logger().Debug("发送 BYE 失败", "err", err)
				}
				_ = cs.dialogC.Close()
			}
		case cs.dialogS != nil:
			// 200 OK 已发出（含接听流程进行中）：发送 BYE 结束通话
			if err := cs.dialogS.Bye(context.Background()); err != nil {
				logx.Logger().Debug("发送 BYE 失败", "err", err)
			}
			_ = cs.dialogS.Close()
		}
	}()
	// 本地状态与界面立即清理：不取消上下文、不关闭 dialog——
	// 取消/关闭的时机由上面的信令 goroutine 掌控（避免 603 竞态）。
	p.endCallImmediate(callID, "local_hangup")
	return nil
}

// Reject 拒接呼入，code 为 SIP 状态码。
func (p *Phone) Reject(callID string, code int) error {
	cs := p.getCall(callID)
	if cs == nil || cs.dialogS == nil {
		return core.ErrCallNotFound
	}
	// 拒接响应必须先于上下文取消发出（同 Hangup 的 603 竞态修复：
	// handleIncoming 退出后 sipgo 会 TerminateGracefully 终止事务，
	// 之后再 Respond 必然失败）
	go func() {
		if cs.dialogS.LoadState() >= sip.DialogStateEstablished {
			// 200 OK 已发出（接听进行中）：拒接已无意义，改发 BYE 挂断
			if err := cs.dialogS.Bye(context.Background()); err != nil {
				logx.Logger().Debug("发送 BYE 失败", "err", err)
			}
		} else if err := cs.dialogS.Respond(code, statusText(code), nil); err != nil {
			logx.Logger().Debug("发送拒接响应失败", "err", err)
		}
		if cs.cancel != nil {
			cs.cancel()
		}
		_ = cs.dialogS.Close()
	}()
	p.endCallImmediate(callID, "rejected")
	return nil
}

// Mute 保持切换：本地立即停止/恢复发送音频，并按 SIP 规范发送
// re-INVITE 通知对端——保持时媒体方向 a=sendonly，取消保持恢复
// a=sendrecv（用户要求；信令在后台 goroutine 发送）。
func (p *Phone) Mute(callID string, muted bool) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	if !cs.established {
		return fmt.Errorf("通话未接通，无法保持")
	}
	cs.engine.SetMuted(muted)
	// 保持期间同时停发本地视频（远端画面保留）。
	// **不重协商视频 m= 行**：视频段方向保持原样，避免与既有 re-INVITE 的
	// ACK/CSeq 逻辑纠缠（保持本身只改音频方向）。
	if cs.video != nil {
		cs.video.SetRemoteHold(muted)
	}
	go p.sendHoldReInvite(cs, muted)
	return nil
}

// MuteMic 麦克风静音切换：纯本地停止/恢复发送（不发 SIP 信令，
// 区别于 Mute 的保持 re-INVITE——对端听到静音但通话保持正常状态）。
func (p *Phone) MuteMic(callID string, muted bool) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	cs.engine.SetMicMuted(muted)
	return nil
}

// MuteSpeaker 扬声器静音切换：纯本地停止/恢复播放（继续接收 RTP）。
func (p *Phone) MuteSpeaker(callID string, muted bool) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	cs.engine.SetPlayMuted(muted)
	return nil
}

// sendHoldReInvite 发送保持/取消保持的 re-INVITE：
// INVITE（新 SDP offer，方向 sendonly/sendrecv）→ 2xx → 应用 answer → ACK。
func (p *Phone) sendHoldReInvite(cs *callSession, muted bool) {
	cs.holdMu.Lock()
	defer cs.holdMu.Unlock()
	// 期间通话可能已结束
	if p.getCall(cs.id) == nil {
		return
	}

	dir := "sendrecv"
	if muted {
		dir = "sendonly"
	}
	// 视频段方向跟随音频：保持时也置 sendonly，恢复时按本端能否推送决定。
	videoDir := ""
	if cs.video != nil {
		if muted || !cs.videoSend || !p.cameraUsable() {
			videoDir = "sendonly"
		} else {
			videoDir = "sendrecv"
		}
	}
	offer := media.SDPOfferFor(cs.engine, cs.video, dir, videoDir)

	// 远程目标：dialog 的远端地址（呼出取 2xx 的 Contact，呼入取 INVITE 的 Contact）
	var (
		target sip.Uri
		tx     sip.ClientTransaction
		req    *sip.Request
		err    error
	)
	switch {
	case cs.dialogC != nil:
		if cs.dialogC.InviteResponse != nil && cs.dialogC.InviteResponse.Contact() != nil {
			target = cs.dialogC.InviteResponse.Contact().Address
		} else if cs.dialogC.InviteResponse != nil {
			// 兜底：200 未带 Contact（非规范对端）时退回原 R-URI
			target = cs.dialogC.InviteRequest.Recipient
		} else {
			logx.Logger().Warn("保持 re-INVITE 失败：缺少远端地址", "callId", cs.id)
			return
		}
		req = buildReInvite(target, offer)
		tx, err = cs.dialogC.TransactionRequest(context.Background(), req)
	case cs.dialogS != nil:
		if cs.dialogS.InviteRequest.Contact() == nil {
			logx.Logger().Warn("保持 re-INVITE 失败：缺少远端地址", "callId", cs.id)
			return
		}
		target = cs.dialogS.InviteRequest.Contact().Address
		req = buildReInvite(target, offer)
		tx, err = cs.dialogS.TransactionRequest(context.Background(), req)
	default:
		return
	}
	if err != nil {
		logx.Logger().Warn("保持 re-INVITE 失败", "callId", cs.id, "err", err)
		return
	}
	// 事务延迟 5 秒收尾：期间对端重传的 200 会触发 OnRetransmission
	// 补发 ACK（RFC 3261 13.2.2.4），过早 Terminate 会让重传 200 成为
	// Unhandled，对端 dialog 卡在等待 ACK 而拒绝后续 re-INVITE
	go func() {
		time.Sleep(5 * time.Second)
		tx.Terminate()
	}()

	// 等待最终响应（跳过 1xx）
	var res *sip.Response
	deadline := time.After(30 * time.Second)
waitLoop:
	for {
		select {
		case r, ok := <-tx.Responses():
			if !ok {
				logx.Logger().Warn("保持 re-INVITE 事务结束", "callId", cs.id)
				return
			}
			if r.IsProvisional() {
				continue
			}
			res = r
			break waitLoop
		case <-tx.Done():
			logx.Logger().Warn("保持 re-INVITE 事务异常结束", "callId", cs.id, "err", tx.Err())
			return
		case <-deadline:
			logx.Logger().Warn("保持 re-INVITE 响应超时", "callId", cs.id)
			return
		}
	}
	if !res.IsSuccess() {
		logx.Logger().Warn("保持 re-INVITE 被拒绝", "callId", cs.id, "code", res.StatusCode)
		return
	}
	if body := res.Body(); len(body) > 0 {
		if err := cs.engine.SDPApplyAnswer(body); err != nil {
			logx.Logger().Debug("re-INVITE answer 解析失败", "callId", cs.id, "err", err)
		}
	}
	// 发送 ACK。req 已由 buildReq 补齐 dialog 头（From/To/Call-ID/CSeq
	// 均为 re-INVITE 自身的值），ACK 必须复用这些头——此前按初始
	// INVITE 的头构造，呼入场景 From 变成了对端，对端 dialog 校验
	// 失败丢弃 ACK，导致取消保持的 re-INVITE 被 491 拒绝（bug 修复）。
	sendReInviteAck(cs, req, res)
	// 对端重传 200 时补发 ACK（RFC 3261 13.2.2.4），避免对端 dialog
	// 卡在等待 ACK 状态而拒绝后续 re-INVITE
	tx.OnRetransmission(func(r *sip.Response) {
		if r.IsSuccess() {
			sendReInviteAck(cs, req, r)
		}
	})
	logx.Logger().Info("保持 re-INVITE 完成", "callId", cs.id, "direction", dir)
}

// buildReInvite 构造 in-dialog re-INVITE 请求（dialog 头由 sipgo 的
// buildReq 在 TransactionRequest 时补齐并递增 CSeq）。
func buildReInvite(target sip.Uri, offer string) *sip.Request {
	req := sip.NewRequest(sip.INVITE, target)
	req.SetBody([]byte(offer))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	return req
}

// sendReInviteAck 对 re-INVITE 的 2xx 发送 ACK（RFC 3261：in-dialog
// INVITE 的 2xx 必须 ACK；sipgo 的 Ack() 仅适用于初始 INVITE）。
// req 为 re-INVITE 请求（TransactionRequest 已补齐 dialog 头），
// ACK 的 From/Call-ID/CSeq 必须与其一致，To 取 2xx 的 To。
func sendReInviteAck(cs *callSession, req *sip.Request, res *sip.Response) {
	var (
		target sip.Uri
		sender func(*sip.Request) error
	)
	switch {
	case cs.dialogC != nil:
		target = cs.dialogC.InviteResponse.Contact().Address
		sender = cs.dialogC.WriteRequest
	case cs.dialogS != nil:
		target = cs.dialogS.InviteRequest.Contact().Address
		sender = cs.dialogS.WriteRequest
	default:
		return
	}
	ack := sip.NewRequest(sip.ACK, target)
	if f := req.From(); f != nil {
		ack.AppendHeader(sip.HeaderClone(f))
	}
	if t := res.To(); t != nil {
		ack.AppendHeader(sip.HeaderClone(t))
	}
	if c := req.CallID(); c != nil {
		ack.AppendHeader(sip.HeaderClone(c))
	}
	if c := req.CSeq(); c != nil {
		cs := sip.HeaderClone(c)
		cs.(*sip.CSeqHeader).MethodName = sip.ACK
		ack.AppendHeader(cs)
	}
	if err := sender(ack); err != nil {
		logx.Logger().Debug("re-INVITE ACK 发送失败", "callId", cs.id, "err", err)
	}
}

// SendDTMF 通话中发送 DTMF 音（RFC 2833）。
func (p *Phone) SendDTMF(callID, digit string) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	cs.engine.SendDTMF(digit)
	return nil
}

// SetAutoAnswer 开关自动接听：开启后来电自动接听（REQ-CALL 新增）。
func (p *Phone) SetAutoAnswer(enabled bool) error {
	p.autoAnswer.Store(enabled)
	logx.Logger().Info("自动接听设置", "enabled", enabled)
	return nil
}

// SetRPort 动态切换 rport（REQ-ACC-016，运行中立即生效，无需重启）。
func (p *Phone) SetRPort(enabled bool) error {
	setClientRPort(p.client, enabled)
	return nil
}

// SetMicrophone 选择麦克风。
// 切换在音频后台队列中生效（设备重建可能耗时，不能阻塞调用方），
// 失败经日志体现。
func (p *Phone) SetMicrophone(deviceID string) error {
	if p.audio == nil {
		return fmt.Errorf("音频管理器不可用")
	}
	p.audio.SetMic(deviceID)
	return nil
}

// SetSpeaker 选择扬声器。
func (p *Phone) SetSpeaker(deviceID string) error {
	if p.audio == nil {
		return fmt.Errorf("音频管理器不可用")
	}
	p.audio.SetSpeaker(deviceID)
	return nil
}

// AudioDevices 返回音频设备列表。
func (p *Phone) AudioDevices() ([]core.AudioDevice, []core.AudioDevice) {
	if p.audio == nil {
		return nil, nil
	}
	return p.audio.Devices()
}

// Close 释放全部资源。
func (p *Phone) Close() error {
	if p.regCancel != nil {
		p.regCancel()
	}
	p.mu.Lock()
	calls := make([]*callSession, 0, len(p.calls))
	for _, cs := range p.calls {
		calls = append(calls, cs)
	}
	p.calls = make(map[string]*callSession)
	p.mu.Unlock()
	for _, cs := range calls {
		p.closeVideo(cs) // 视频引擎也必须回收（停采集、关端口）
		cs.engine.Close()
		if cs.cancel != nil {
			cs.cancel()
		}
	}
	_ = p.client.Close()
	_ = p.ua.Close()
	return nil
}

// ---------------------------------------------------------------------------
// 注册流程
// ---------------------------------------------------------------------------

// registerLoop 注册生命周期：注册 → 失败退避重试 → 成功后续注册。
// stunCh：STUN 公网地址探测就绪信号——收到后立即重新注册，
// 让 Contact 携带公网地址（此前仅把状态打回"注册中"却没有触发
// 重注册，界面会一直停留在"注册中"直到下次续注册——bug 修复）。
func (p *Phone) registerLoop(ctx context.Context, acc config.Account, stunCh <-chan struct{}) {
	backoff := time.Second
	expires := acc.Expires // 注册到期时间（REQ-ACC-018），服务端返回值优先

	for {
		if ctx.Err() != nil {
			return
		}
		gotExpires, err := p.doRegister(ctx, acc)
		if err != nil {
			p.sink.OnRegisterState(core.RegFailed, err.Error())
			logx.Logger().Warn("注册失败，将重试", "err", err, "backoff", backoff.String())
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		expires = gotExpires
		p.sink.OnRegisterState(core.RegRegistered, "")
		// 日志携带本次注册的 Contact 地址，便于确认 STUN 公网地址是否
		// 已生效（Contact host 为公网 IP 即 STUN 生效）
		contact := p.contactURI(acc.Account)
		logx.Logger().Info("注册成功", "expires", expires, "contact", contact.Addr())

		// 续注册：到期前 30% 时重新注册（按 RFC 3261 建议）；
		// STUN 就绪时立即重新注册
		renew := time.Duration(float64(expires)*0.7) * time.Second
		select {
		case <-time.After(renew):
			// 继续循环重新注册
		case <-stunCh:
			logx.Logger().Info("STUN 公网地址就绪，立即重新注册")
		case <-ctx.Done():
			return
		}
	}
}

// doRegister 发送一次 REGISTER（含 Digest 认证），成功返回 expires。
func (p *Phone) doRegister(ctx context.Context, acc config.Account) (int, error) {
	return p.sendRegister(ctx, acc, acc.Expires)
}

// sendRegister 发送 REGISTER，expires=0 表示注销。成功返回服务端 expires。
func (p *Phone) sendRegister(ctx context.Context, acc config.Account, expires int) (int, error) {
	p.sink.OnRegisterState(core.RegRegistering, "")

	// 请求行携带用户名（REQ-ACC-012）：OpenSIPS 等服务器的 registrar 从
	// Request-URI 提取 location 表的 username，缺省会导致 username 为空。
	recipient := sip.Uri{User: acc.Account, Host: acc.Server, Port: acc.Port}
	req := sip.NewRequest(sip.REGISTER, recipient)
	req.SetTransport(strings.ToUpper(acc.Transport))

	authUser := acc.AuthUser
	if authUser == "" {
		authUser = acc.Account
	}
	// From / To（必须是独立的两个头对象，复用同一指针会导致 From 重复输出）
	from := &sip.FromHeader{
		DisplayName: acc.DisplayName,
		Address:     sip.Uri{User: acc.Account, Host: acc.Server},
	}
	to := &sip.ToHeader{
		DisplayName: acc.DisplayName,
		Address:     sip.Uri{User: acc.Account, Host: acc.Server},
	}
	req.AppendHeader(from)
	req.AppendHeader(to)
	// Contact：<sip:账号@本机IP:监听端口>;expires=N
	contact := &sip.ContactHeader{
		Address: p.contactURI(acc.Account),
		Params:  sip.NewParams(),
	}
	contact.Params.Add("expires", strconv.Itoa(expires))
	req.AppendHeader(contact)
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))
	req.AppendHeader(sip.NewHeader("Allow", "INVITE, ACK, CANCEL, BYE, OPTIONS, UPDATE, NOTIFY, INFO, REFER"))
	req.AppendHeader(sip.NewHeader("User-Agent", UserAgentName()))

	logx.TraceSIP("发出", req.String())
	// 自定义 build：sipgo 默认的 ClientRequestRegisterBuild 按 RFC 3261 清空
	// R-URI 的用户部分，但 OpenSIPS 的 registrar 从 R-URI 提取 location 表的
	// username（REQ-ACC-012），这里在默认构建之后恢复用户名。
	buildReq := func(c *sipgo.Client, r *sip.Request) error {
		if err := sipgo.ClientRequestRegisterBuild(c, r); err != nil {
			return err
		}
		r.Recipient.User = acc.Account
		// 出站代理（REQ-ACC-019）：R-URI 保持目标，网络层发往代理
		if acc.Proxy != "" {
			r.SetDestination(acc.Proxy)
		}
		return nil
	}
	tx, err := p.client.TransactionRequest(ctx, req, buildReq)
	if err != nil {
		return 0, fmt.Errorf("发送 REGISTER 失败: %w", err)
	}
	defer tx.Terminate()

	// 处理响应（最多 2 次认证重试）
	auth := sipgo.DigestAuth{Username: authUser, Password: acc.Password}
	for attempt := 0; attempt < 3; attempt++ {
		res, err := p.readResponse(ctx, tx, "REGISTER")
		if err != nil {
			return 0, err
		}
		logx.TraceSIP("收到", res.String())
		switch res.StatusCode {
		case 200:
			// 解析服务端 expires
			exp := expires
			if eh := res.GetHeader("Expires"); eh != nil {
				if v, err := strconv.Atoi(eh.Value()); err == nil && v > 0 {
					exp = v
				}
			}
			if ch := res.Contact(); ch != nil && ch.Params != nil {
				if v, ok := ch.Params.Get("expires"); ok {
					if vi, err := strconv.Atoi(v); err == nil && vi > 0 {
						exp = vi
					}
				}
			}
			return exp, nil
		case 401, 407:
			if attempt >= 2 {
				return 0, fmt.Errorf("认证失败：连续 3 次未通过")
			}
			res2, err := p.client.DoDigestAuth(ctx, req, res, auth)
			if err != nil {
				return 0, fmt.Errorf("认证请求失败: %w", err)
			}
			logx.TraceSIP("收到", res2.String())
			if res2.StatusCode == 200 {
				return expires, nil
			}
			return 0, fmt.Errorf("注册被拒绝: %d %s", res2.StatusCode, res2.Reason)
		default:
			return 0, fmt.Errorf("注册被拒绝: %d %s", res.StatusCode, res.Reason)
		}
	}
	return 0, fmt.Errorf("注册未完成")
}

// readResponse 读取事务的下一个最终响应（跳过 1xx）。
// 同时监听 ctx 与事务结束，避免退出注销时等待满 30 秒。
func (p *Phone) readResponse(ctx context.Context, tx sip.ClientTransaction, label string) (*sip.Response, error) {
	for {
		select {
		case res, ok := <-tx.Responses():
			if !ok {
				return nil, fmt.Errorf("%s 事务已结束", label)
			}
			if res.IsProvisional() {
				continue
			}
			return res, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tx.Done():
			return nil, fmt.Errorf("%s 事务异常结束: %v", label, tx.Err())
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("%s 响应超时", label)
		}
	}
}

// ---------------------------------------------------------------------------
// 呼出流程
// ---------------------------------------------------------------------------

// dccInvite 创建 dialog client cache（按账户传输方式）并发起 INVITE。
func (p *Phone) dccInvite(recipient sip.Uri, offer string, acc config.Account) (*sipgo.DialogClientSession, error) {
	if p.dcc == nil {
		contact := &sip.ContactHeader{Address: p.contactURI(acc.Account)}
		p.dcc = sipgo.NewDialogClientCache(p.client, *contact)
	}
	if strings.EqualFold(acc.Transport, "tcp") {
		recipient.UriParams = sip.NewParams()
		recipient.UriParams.Add("transport", "tcp")
	}
	// From / To：sipgo dialog 层不自动生成，必须显式添加（缺失会导致
	// OpenSIPS 报 "bad from header"）。From 按 RFC 3261 携带 tag。
	from := &sip.FromHeader{
		DisplayName: acc.DisplayName,
		Address:     sip.Uri{User: acc.Account, Host: acc.Server},
		Params:      sip.NewParams(),
	}
	from.Params.Add("tag", sip.GenerateTagN(8))
	to := &sip.ToHeader{
		Address: sip.Uri{User: recipient.User, Host: recipient.Host, Port: recipient.Port},
	}
	headers := []sip.Header{
		sip.NewHeader("Content-Type", "application/sdp"),
		sip.NewHeader("User-Agent", UserAgentName()),
		from, to,
	}
	// 出站代理（REQ-ACC-019）：首条 Route 指向代理（loose routing）
	if acc.Proxy != "" {
		headers = append(headers, sip.NewHeader("Route", "<sip:"+acc.Proxy+";lr>"))
	}
	dlg, err := p.dcc.Invite(context.Background(), recipient, []byte(offer), headers...)
	if err != nil {
		return nil, fmt.Errorf("发送 INVITE 失败: %w", err)
	}
	logx.TraceSIP("发出", dlg.InviteRequest.String())
	return dlg, nil
}

// outgoingFlow 呼出事务流程：振铃 →（认证重试）→ 接通 / 失败 / 取消。
func (p *Phone) outgoingFlow(ctx context.Context, cs *callSession, acc config.Account) {
	defer func() {
		if !cs.established {
			p.cleanupCall(cs.id, "failed")
		}
	}()

	authUser := acc.AuthUser
	if authUser == "" {
		authUser = acc.Account
	}
	err := cs.dialogC.WaitAnswer(ctx, sipgo.AnswerOptions{
		Username: authUser,
		Password: acc.Password,
		OnResponse: func(res *sip.Response) error {
			// 180 / 183 振铃
			if res.StatusCode == 180 || res.StatusCode == 183 {
				logx.TraceSIP("收到", res.String())
				// 183 携带 SDP 即早期媒体（服务器下发的回铃音）：
				// 解析对端地址、启动媒体接收与播放（本端保持静音，
				// 早期媒体阶段不发送本端音频）
				earlyMedia := false
				if res.StatusCode == 183 && len(res.Body()) > 0 && !cs.mediaStarted {
					if err := cs.engine.SDPApplyAnswer(res.Body()); err != nil {
						logx.Logger().Debug("早期媒体 SDP 解析失败", "callId", cs.id, "err", err)
					} else {
						cs.mediaStarted = true
						earlyMedia = true
						cs.engine.SetMuted(true)     // 早期媒体阶段静音本端（不发送真实麦克风数据）
						cs.engine.SetKeepalive(true) // 持续发送静音保活 NAT 映射
						p.attachAudio(cs.engine)
						cs.engine.Start()
						logx.Logger().Info("早期媒体已建立（回铃音）", "callId", cs.id)
					}
				}
				// earlyMedia 一并上报：界面据此决定要不要放本地回铃音
				p.sink.OnRinging(cs.id, earlyMedia)
			}
			return nil
		},
	})

	if err != nil {
		if errors.Is(err, context.Canceled) {
			// 本地取消（挂断）。CANCEL 与对端 200 存在竞态（RFC 3261 §9.1）：
			// 若 CANCEL 期间对端已回 2xx，需补 ACK + BYE 结束会话，
			// 否则对端会一直重传 200 并保持通话（bug 修复）。
			if res := cs.dialogC.InviteResponse; res != nil && res.IsSuccess() {
				logx.Logger().Info("取消后收到对端 2xx，补发 ACK + BYE", "callId", cs.id)
				_ = cs.dialogC.Ack(context.Background())
				if err := cs.dialogC.Bye(context.Background()); err != nil {
					logx.Logger().Debug("发送 BYE 失败", "err", err)
				}
				_ = cs.dialogC.Close()
			}
			return
		}
		// 非 2xx 最终响应：忙/拒绝/无应答
		reason := err.Error()
		if res := cs.dialogC.InviteResponse; res != nil {
			logx.TraceSIP("收到", res.String())
			reason = fmt.Sprintf("%d %s", res.StatusCode, res.Reason)
		}
		p.sink.OnFailed(cs.id, reason)
		return
	}

	// 2xx：解析 SDP answer，建立媒体
	if res := cs.dialogC.InviteResponse; res != nil {
		logx.TraceSIP("收到", res.String())
		if body := res.Body(); len(body) > 0 {
			if err := cs.engine.SDPApplyAnswer(body); err != nil {
				p.sink.OnFailed(cs.id, fmt.Sprintf("SDP 协商失败: %v", err))
				_ = cs.dialogC.Bye(context.Background())
				return
			}
			// 视频：对端可能只接受音频（answer 里 m=video 端口为 0）
			p.applyVideoAnswer(cs, body)
		} else if cs.video != nil {
			// 对端 2xx 没带 SDP（异常对端）：视频无从协商，按"没有视频"处理，
			// 关掉摄像头并让界面收掉视频窗口（视频呼叫拨出即开摄像头，
			// 不收回来就会一直空转）。
			logx.Logger().Info("对端 2xx 未带 SDP，视频按未协商处理", "callId", cs.id)
			p.closeVideo(cs)
			p.notifyVideoState(cs)
		}
	}

	// 本地可能在 2xx 到达后、接通完成前挂断（ctx 已取消）：此时
	// WaitAnswer 已返回不会再发 CANCEL，必须补 ACK + BYE 通知对端，
	// 否则对端一直保持通话（竞态 bug 修复）。
	if ctx.Err() != nil {
		logx.Logger().Info("2xx 后检测到本地挂断，补发 ACK + BYE", "callId", cs.id)
		_ = cs.dialogC.Ack(context.Background())
		if err := cs.dialogC.Bye(context.Background()); err != nil {
			logx.Logger().Debug("发送 BYE 失败", "err", err)
		}
		_ = cs.dialogC.Close()
		return
	}

	// 发送 ACK
	if err := cs.dialogC.Ack(context.Background()); err != nil {
		logx.Logger().Debug("发送 ACK 失败", "err", err)
	}
	cs.established = true
	// 早期媒体（183）阶段引擎可能已启动接收：复用并恢复正常发送
	if !cs.mediaStarted {
		p.attachAudio(cs.engine)
		cs.engine.Start()
		cs.mediaStarted = true
	}
	cs.engine.SetKeepalive(false) // 关闭保活，恢复真实音频发送
	cs.engine.SetMuted(false)
	p.startVideo(cs, cs.videoSend)
	p.sink.OnEstablished(cs.id)
}

// applyVideoAnswer 处理对端 2xx 里的视频段：协商成功则保留引擎（启动在
// 接通流程里做），被拒绝（m=video 端口为 0 或无该段）则关闭视频引擎，
// 本次通话退化为纯音频。
func (p *Phone) applyVideoAnswer(cs *callSession, body []byte) {
	if cs.video == nil {
		return
	}
	offer, err := media.ParseSDP(body)
	if err != nil {
		logx.Logger().Debug("视频 answer 解析失败，按纯音频处理", "err", err)
		p.closeVideo(cs)
		return
	}
	info, err := offer.Media("video")
	if err != nil || info.Rejected {
		logx.Logger().Info("对端未接受视频，本次通话为纯音频（关闭摄像头与视频窗口）", "callId", cs.id)
		p.closeVideo(cs)
		p.notifyVideoState(cs)
		return
	}
	if !cs.video.ApplyAnswer(info) {
		logx.Logger().Warn("视频协商失败，本次通话为纯音频（关闭摄像头与视频窗口）", "callId", cs.id)
		p.closeVideo(cs)
		p.notifyVideoState(cs)
	}
}

// ---------------------------------------------------------------------------
// 呼入流程
// ---------------------------------------------------------------------------

// handleIncoming 处理呼入 INVITE。
// 单通话限制：呼叫/振铃/通话中时新来电直接回 486（USER_BUSY）。
func (p *Phone) handleIncoming(req *sip.Request, tx sip.ServerTransaction) {
	logx.TraceSIP("收到", req.String())
	p.mu.Lock()
	busy := len(p.calls) > 0
	acc := p.acc
	p.mu.Unlock()
	if busy {
		logx.Logger().Info("已有通话进行中，拒绝来电（486 User Busy）", "callId", req.CallID().Value())
		res := sip.NewResponseFromRequest(req, 486, "Busy Here", nil)
		_ = tx.Respond(res)
		return
	}

	// 解析主叫信息
	from := req.From()
	remote := from.Address.User
	remoteName := from.DisplayName

	callID := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	cs := &callSession{
		id:         callID,
		dir:        "incoming",
		remote:     remote,
		remoteName: remoteName,
		ctx:        ctx,
		cancel:     cancel,
	}
	p.mu.Lock()
	p.calls[callID] = cs
	p.mu.Unlock()

	// 用 dialog server 处理 INVITE
	contact := &sip.ContactHeader{Address: p.contactURI(acc.Account)}
	if p.dsc == nil {
		p.dsc = sipgo.NewDialogServerCache(p.client, *contact)
	}
	dlg, err := p.dsc.ReadInvite(req, tx)
	if err != nil {
		logx.Logger().Error("处理呼入 INVITE 失败", "err", err)
		p.cleanupCall(callID, "failed")
		return
	}
	cs.dialogS = dlg

	// 解析 offer 中的远端 RTP 信息，准备媒体引擎（SDPAnswer 在接听时确定）
	engine, err := media.NewEngine(p.localIP())
	if err != nil {
		logx.Logger().Error("创建媒体引擎失败", "err", err)
		_ = dlg.Respond(500, statusText(500), nil)
		p.cleanupCall(callID, "failed")
		return
	}
	// SDP 宣告地址使用对端可达地址（NAT：注册回显地址 / STUN 公网地址）
	engine.SetSDPIP(p.localHost())
	cs.engine = engine
	if body := req.Body(); len(body) > 0 {
		if _, err := engine.SDPAnswer(body); err != nil {
			logx.Logger().Debug("呼入 SDP 解析失败", "err", err)
		}
	}

	// 视频（REQ-VIDEO-004）：解析对端 offer 的视频段并准备好视频引擎，
	// 但**先不启动**——接听时才根据用户点的按钮决定视频段方向。
	hasVideo := false
	if body := req.Body(); len(body) > 0 && p.VideoSupported() {
		if offer, err := media.ParseSDP(body); err == nil {
			cs.offer = offer
			if info, mErr := offer.Media("video"); mErr == nil && !info.Rejected {
				if v, vErr := media.NewVideoEngine(p.localIP()); vErr == nil {
					v.SetSDPIP(p.localHost())
					if v.ApplyOffer(info) {
						cs.video = v
						hasVideo = true
					} else {
						v.Close()
					}
				} else {
					logx.Logger().Warn("创建视频引擎失败，按纯音频接听", "err", vErr)
				}
			}
		} else {
			logx.Logger().Debug("呼入 SDP 二次解析失败", "err", err)
		}
	}

	// 上报来电事件（核心层会弹窗/推送 WS 事件）
	p.sink.OnIncoming(callID, remote, remoteName, hasVideo)

	// 自动接听决策：
	//   1) INVITE 携带 Call-Info: <...>;answer-after=x（REQ-CALL-015）：
	//      x=0 立即接听，x>0 延迟 x 秒接听（期间先回 180）；
	//   2) 本地自动接听开关开启：立即接听；
	//   3) 否则回 180 等待用户操作。
	answerAfter := parseAnswerAfter(req)
	if answerAfter > 0 {
		// 延迟自动接听：先振铃，x 秒后自动接听
		p.respondRinging(dlg)
		logx.Logger().Info("延迟自动接听", "callId", callID, "delay", answerAfter)
		go func() {
			select {
			case <-time.After(time.Duration(answerAfter) * time.Second):
				logx.Logger().Info("延迟时间到，执行自动接听", "callId", callID)
				if !cs.established {
					// 自动接听按隐私优先：不打开摄像头（视频方向 recvonly）
					_ = p.answerIncoming(cs.id, false)
				}
			case <-ctx.Done():
			}
		}()
	} else if answerAfter == 0 || p.autoAnswer.Load() {
		// 立即自动接听
		logx.Logger().Info("自动接听来电", "callId", callID, "answerAfter", answerAfter)
		// 自动接听按隐私优先：不打开摄像头（视频方向 recvonly）
		_ = p.answerIncoming(cs.id, false)
	} else {
		p.respondRinging(dlg)
	}

	// 等待：接听（Answer 中 RespondSDP）/ 拒接（Reject）/
	// 对端取消（dialog 状态结束，sipgo 事务层处理 CANCEL 后通知）。
	// 注意：以 dialog 实际状态判断，不能用 cs.established——RespondSDP
	// 内部先 setState(Established) 触发 StateRead 通知，而 cs.established
	// 在返回后才置位，误判会把正常接听当成对端取消清理掉（bug 修复）。
	select {
	case <-ctx.Done():
	case <-dlg.StateRead():
		// 振铃阶段 dialog 终止（对端 CANCEL / 事务超时）时状态为
		// DialogStateEnded(3)。注意不能用 state < Established 判断——
		// 枚举中 Ended=3 大于 Established=1，该判断对 Ended 恒为 false，
		// 导致 CANCEL 后通话从未被清理（bug 修复）。
		if state := dlg.LoadState(); state == sip.DialogStateEnded {
			logx.Logger().Info("对端取消呼叫（CANCEL）", "callId", callID, "state", state)
			p.cleanupCall(callID, "remote_cancel")
		}
	}
}

// respondRinging 回 180 振铃。
func (p *Phone) respondRinging(dlg *sipgo.DialogServerSession) {
	if err := dlg.Respond(sip.StatusRinging, "Ringing", nil); err != nil {
		logx.Logger().Debug("发送 180 失败", "err", err)
	}
}

// parseAnswerAfter 解析 INVITE 的 Call-Info 头中的 answer-after 参数。
// 返回 -1 表示未指定。
// 头格式：Call-Info: <sip:xxx>;answer-after=0
func parseAnswerAfter(req *sip.Request) int {
	for _, h := range req.GetHeaders("Call-Info") {
		v := h.Value()
		// 在 <...> 之后的参数中查找 answer-after=N
		if idx := strings.Index(v, "answer-after="); idx >= 0 {
			rest := v[idx+len("answer-after="):]
			num := ""
			for _, c := range rest {
				if c >= '0' && c <= '9' {
					num += string(c)
				} else {
					break
				}
			}
			if num != "" {
				if n, err := strconv.Atoi(num); err == nil && n >= 0 {
					return n
				}
			}
		}
	}
	return -1
}

// handleReInvite 处理通话中的 in-dialog re-INVITE（对端保持/恢复）：
// 解析对端 offer 的媒体方向（对端保持时为 a=recvonly），按镜像方向
// 回 200 + SDP answer；对端只发不收（sendonly）或完全停发（inactive）
// 时本端停止发送媒体。
func (p *Phone) handleReInvite(req *sip.Request, tx sip.ServerTransaction) {
	logx.TraceSIP("收到", req.String())
	cs := p.matchByCallID(req.CallID().Value())
	if cs == nil || cs.engine == nil || !cs.established {
		logx.Logger().Warn("re-INVITE 无匹配通话", "callId", req.CallID().Value())
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
		return
	}

	// 方向必须**按媒体类型分别取**：对端可能只改视频段的方向，
	// 用「第一段」的旧写法会把视频方向误当成音频方向（多 m= 行引入的坑）。
	remoteDir := media.SDPDirectionFor(req.Body(), "audio")
	// 视频段方向单独镜像；对端 re-INVITE 里 m=video 端口为 0 表示拒收视频。
	videoDir := ""
	if cs.video != nil {
		if vinfo, vErr := media.ParseMedia(req.Body(), "video"); vErr == nil && !vinfo.Rejected {
			cs.video.SetRemoteHold(vinfo.Dir == "sendonly" || vinfo.Dir == "inactive")
			if vinfo.Dir != "inactive" {
				videoDir = media.MirrorDirection(vinfo.Dir)
			}
		}
	}
	answer, err := cs.engine.SDPAnswerWithDirection(req.Body(), media.MirrorDirection(remoteDir))
	if err != nil {
		logx.Logger().Warn("re-INVITE SDP 协商失败", "callId", cs.id, "err", err)
		_ = tx.Respond(sip.NewResponseFromRequest(req, 500, "Server Internal Error", nil))
		return
	}
	// 含视频时按 offer 的媒体顺序重组 answer
	if videoDir != "" && cs.video != nil {
		if offer, oErr := media.ParseSDP(req.Body()); oErr == nil {
			answer = media.SDPAnswerFor(cs.engine, cs.video, offer, cs.engine.PayloadType(), videoDir)
		}
	}

	// 回 200 在后台执行：2xx 的重传与 ACK 等待不能阻塞事务回调
	go func() {
		res := sip.NewSDPResponseFromRequest(req, []byte(answer))
		if err := tx.Respond(res); err != nil {
			logx.Logger().Debug("re-INVITE 200 发送失败", "callId", cs.id, "err", err)
			return
		}
		// 吸收 ACK（避免事务层 ACK missed 告警）
		select {
		case <-tx.Acks():
		case <-time.After(10 * time.Second):
		}
	}()

	// 对端只发不收（sendonly）或完全停发（inactive）→ 本端停止发送；
	// 对端保持（recvonly，只想收）→ 本端照常发送
	cs.engine.SetRemoteHold(remoteDir == "sendonly" || remoteDir == "inactive")
	logx.Logger().Info("对端 re-INVITE 已处理", "callId", cs.id, "remoteDir", remoteDir)
}

// handleIncomingCancel 对端取消振铃中的呼入。
func (p *Phone) handleIncomingCancel(req *sip.Request, tx sip.ServerTransaction) {
	logx.TraceSIP("收到", req.String())
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	_ = tx.Respond(res)
	// 找到对应通话并终止（按 Call-ID 匹配）
	target := p.matchByCallID(req.CallID().Value())
	if target != nil && !target.established {
		p.cleanupCall(target.id, "remote_cancel")
	}
}

// handleRemoteBye 对端挂断。
func (p *Phone) handleRemoteBye(req *sip.Request, tx sip.ServerTransaction) {
	logx.TraceSIP("收到", req.String())
	if tx != nil {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		_ = tx.Respond(res)
	}

	target := p.matchByCallID(req.CallID().Value())
	if target != nil {
		p.cleanupCall(target.id, "remote_hangup")
	}
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

// getCall 取通话会话。
func (p *Phone) getCall(callID string) *callSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[callID]
}

// matchByCallID 按 SIP Call-ID 匹配会话：呼入用 dialogS、呼出用 dialogC
// （此前仅匹配 dialogS，呼出通话的对端 BYE 被忽略——bug 修复）。
func (p *Phone) matchByCallID(callID string) *callSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, cs := range p.calls {
		var csCallID string
		if cs.dialogS != nil && cs.dialogS.InviteRequest != nil {
			csCallID = cs.dialogS.InviteRequest.CallID().Value()
		} else if cs.dialogC != nil && cs.dialogC.InviteRequest != nil {
			csCallID = cs.dialogC.InviteRequest.CallID().Value()
		}
		if csCallID != "" && csCallID == callID {
			return cs
		}
	}
	return nil
}

// endCallImmediate 立即结束通话的本地状态：从会话表移除、释放媒体、
// 上报核心层（界面即时恢复）。不取消会话上下文、不关闭 dialog——
// 这两个动作涉及信令时序（如 603 必须先于上下文取消发出），由
// 调用方（cleanupCall / Hangup 的信令 goroutine）负责。
func (p *Phone) endCallImmediate(callID, reason string) *callSession {
	p.mu.Lock()
	cs := p.calls[callID]
	delete(p.calls, callID)
	p.mu.Unlock()
	if cs == nil {
		return nil
	}
	if p.audio != nil {
		p.audio.DetachEngine()
	}
	// 视频引擎先关（停采集/停解码），再关音频端口。闭环幂等，
	// 与 cleanupCall / Phone.Close 并发调用无副作用。
	if v := cs.video; v != nil {
		vrx, vtx, vlost := v.Stats()
		logx.Logger().Info("视频媒体统计", "callId", cs.id, "rx", vrx, "lost", vlost, "tx", vtx)
	}
	p.closeVideo(cs)
	if cs.engine != nil {
		// 收包统计（Info 级）：回铃音等早期媒体排查用——rx=0 说明
		// 对端 RTP 未到达（网络/地址问题），rx>0 说明到达但播放链路问题
		rx, lost, tx := cs.engine.Stats()
		logx.Logger().Info("通话媒体统计", "callId", cs.id, "rx", rx, "lost", lost, "tx", tx)
		cs.engine.Close()
	}
	p.sink.OnTerminated(cs.id, reason)
	return cs
}

// cleanupCall 结束通话：endCallImmediate + 取消会话上下文 + 关闭 dialog。
func (p *Phone) cleanupCall(callID, reason string) {
	cs := p.endCallImmediate(callID, reason)
	if cs == nil {
		return
	}
	if cs.cancel != nil {
		cs.cancel()
	}
	// 关闭 dialog 做缓存清理。Close 不发送任何信令（BYE/CANCEL 由
	// 信令路径负责）、幂等且线程安全（sync.Map 删除），与后台信令
	// goroutine 并发调用无副作用——此前远程挂断场景 dialog 泄漏在
	// cache 中（bug 修复）。
	if cs.dialogS != nil {
		_ = cs.dialogS.Close()
	}
	if cs.dialogC != nil {
		_ = cs.dialogC.Close()
	}
}

// localIP 本机 IP（SDP 用）：取默认路由接口地址。
func (p *Phone) localIP() string {
	conn, err := net.Dial("udp", p.localAddr())
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// localAddr 账户服务器地址（用于确定出口 IP）。
func (p *Phone) localAddr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return net.JoinHostPort(p.acc.Server, strconv.Itoa(p.acc.Port))
}

// localHost 本机 Contact/SDP 主机：STUN 公网地址优先（启用时），
// 其次本机出口 IP，最后主机名。
func (p *Phone) localHost() string {
	p.mu.Lock()
	pub := p.publicIP
	p.mu.Unlock()
	if pub != "" {
		return pub
	}
	if ip := p.localIP(); ip != "" {
		return ip
	}
	return p.client.Hostname()
}

// parseTarget 解析呼叫目标：纯号码补全为 sip:号码@服务器。
func parseTarget(target string, acc config.Account) (sip.Uri, error) {
	if strings.HasPrefix(strings.ToLower(target), "sip:") || strings.HasPrefix(strings.ToLower(target), "sips:") {
		u := sip.Uri{}
		if err := sip.ParseUri(target, &u); err != nil {
			return u, fmt.Errorf("目标 URI 非法: %w", err)
		}
		if u.Host == "" {
			u.Host = acc.Server
		}
		return u, nil
	}
	// 纯号码
	if strings.ContainsAny(target, " \t\r\n") {
		return sip.Uri{}, fmt.Errorf("号码包含非法字符")
	}
	return sip.Uri{User: target, Host: acc.Server, Port: acc.Port}, nil
}

// statusText 常用 SIP 状态码的简短原因短语。
func statusText(code int) string {
	switch code {
	case 180:
		return "Ringing"
	case 200:
		return "OK"
	case 486:
		return "Busy Here"
	case 500:
		return "Server Internal Error"
	case 603:
		return "Decline"
	default:
		return "OK"
	}
}

// 确保实现 core.Telephony 接口。
var _ core.Telephony = (*Phone)(nil)
