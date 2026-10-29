package wsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 读超时：心跳周期内无消息则断开
const readTimeout = 180 * time.Second

// wsConn 单个 WS 连接的状态：连接句柄 + 写串行化 + 视频订阅标识。
// 文本消息与视频二进制帧共用同一把写锁（coder/websocket 的并发写）。
type wsConn struct {
	id   uint64          // 订阅标识（与 core.App 的网页订阅 id 一致）
	conn *websocket.Conn // 连接句柄
	ctx  context.Context // 连接上下文（帧写入的超时基准）
	wmu  sync.Mutex      // 并发写串行化
}

// serveConn 处理单个 WS 连接：事件转发 + 请求分发。
func (s *Server) serveConn(ctx context.Context, conn *websocket.Conn) {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	c := &wsConn{id: s.video.nextID.Add(1), conn: conn, ctx: connCtx}
	// 连接断开：注销视频订阅（并通知核心层恢复视频窗口显示）
	defer s.video.Unsubscribe(c)
	// 「WS 断开自动取消注册」（REQ-WS-020）：登记/注销连接计数。
	// 必须在读循环之前登记：全部连接断开后按策略自动注销 SIP 注册。
	s.unreg.connOpened()
	defer s.unreg.connClosed()

	// 事件转发：订阅总线，事件 → 客户端
	sub := s.app.Bus().Subscribe()
	defer s.app.Bus().Unsubscribe(sub)
	go func() {
		for ev := range sub {
			if !s.forwardEvent(connCtx, c, ev) {
				cancel() // 发送失败：连接已断开，结束整个连接
				return
			}
		}
	}()

	// 连接建立即推送当前状态快照（system.state 事件），客户端无需等待。
	// 顺序（用户要求）：system.state（坐席只剩 agentMode）→ cti.agent（坐席状态）
	// → call.panel（通话面板快照）。
	go func() {
		c.wmu.Lock()
		defer c.wmu.Unlock()
		_ = writeMsg(connCtx, conn, Message{Type: "event", Event: "system.state", Data: s.app.State()})
		_ = writeMsg(connCtx, conn, Message{Type: "event", Event: "cti.agent", Data: s.app.AgentState()})
		// 通话面板快照（REQ-WS-023）：让刚连上的页面立刻有面板可渲染，
		// 不用等下一次话务事件。
		_ = writeMsg(connCtx, conn, Message{Type: "event", Event: "call.panel", Data: s.app.PanelState()})
	}()

	// 心跳：定期 Ping 探测连接活性并保持链路畅通。
	// 读超时由客户端心跳消息（SDK 每 25 秒发空消息）重置，此处 Ping
	// 用于探测死连接（等 pong 失败即断开）——此前无任何心跳，空闲
	// 60 秒触发读超时断开后由 SDK 自动重连（用户反馈"过段时间断开
	// 又重连"——bug 修复）。读超时后来抬到 180s：Chrome 后台标签页会
	// 把 SDK 的心跳定时器节流到每分钟一次，60s 会被误判断开；死连接
	// 仍由这里的 Ping/pong 兜底（浏览器后台也会在网络层回 pong）。
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(connCtx, 10*time.Second)
				if err := conn.Ping(pingCtx); err != nil {
					pingCancel()
					logx.Logger().Info("WS 心跳失败，断开连接", "err", err)
					cancel()
					return
				}
				pingCancel()
			}
		}
	}()

	// 读循环：解析请求并分发
	for {
		readCtx, readCancel := context.WithTimeout(connCtx, readTimeout)
		typ, data, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			cancel()
			// 心跳超时或正常关闭，不记错误日志
			if !errors.Is(err, context.Canceled) {
				logx.Logger().Info("WS 连接断开", "err", err)
			}
			return
		}
		if typ != websocket.MessageText {
			continue
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			s.reply(c, errMessage(nil, CodeInvalidParams, fmt.Errorf("消息不是合法 JSON")))
			continue
		}
		if msg.Method == "" {
			// 客户端发来非请求消息（如心跳），忽略
			continue
		}
		go s.dispatch(connCtx, c, msg)
	}
}

// forwardEvent 将总线事件封装为 WS 事件消息发送，返回是否成功。
func (s *Server) forwardEvent(ctx context.Context, c *wsConn, ev core.Event) bool {
	msg := Message{Type: "event", Event: ev.Name, Data: ev.Data}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := writeMsg(ctx, c.conn, msg); err != nil {
		logx.Logger().Debug("事件发送失败", "event", ev.Name, "err", err)
		return false
	}
	return true
}

// dispatch 按方法名分发请求。
func (s *Server) dispatch(ctx context.Context, c *wsConn, msg Message) {
	conn := c.conn
	var resp Message
	switch msg.Method {
	case MethodDial:
		var p struct {
			Target string `json:"target"`
			Video  bool   `json:"video"` // 可选：true 发起视频呼叫（REQ-VIDEO-001）
			// Name 可选的**对端显示名**（如在线座席列表里的 displayName）：
			// 呼叫期间优先于通信录名称显示，为空时行为不变。
			Name string `json:"name"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || p.Target == "" {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 target"))
			break
		}
		var (
			callID string
			err    error
		)
		name := strings.TrimSpace(p.Name)
		switch {
		case p.Video && name != "":
			callID, err = s.app.DialVideoNamed(ctx, p.Target, name)
		case p.Video:
			callID, err = s.app.DialVideo(ctx, p.Target)
		case name != "":
			callID, err = s.app.DialNamed(ctx, p.Target, name)
		default:
			callID, err = s.app.Dial(ctx, p.Target)
		}
		if err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, map[string]string{"callId": callID})

	case MethodAnswer, MethodHangup, MethodReject, MethodMute, MethodMuteMic, MethodMuteSpeaker,
		MethodSetMicGain, MethodSetSpeakerVolume, MethodSetVideo, MethodSetVideoPreview, MethodDTMF:
		var p struct {
			CallID  string `json:"callId"`
			Code    int    `json:"code"`
			Muted   bool   `json:"muted"`
			Video   bool   `json:"video"`   // 仅 call.answer：true = 视频接听（REQ-VIDEO-004）
			Enabled bool   `json:"enabled"` // 仅 call.setVideo：视频总开关（REQ-VIDEO-008）
			Digit   string `json:"digit"`   // 仅 call.dtmf：一个 DTMF 字符（0-9 * # A-D）
			// 音量（REQ-MEDIA-006）：**百分比整数**，0 = 禁用（同开关的「关」）
			Gain          *int `json:"gain"`          // 仅 call.setMicGain：0–200
			SpeakerVolume *int `json:"speakerVolume"` // 仅 call.setSpeakerVolume：0–100
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || p.CallID == "" {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 callId"))
			break
		}
		if msg.Method == MethodDTMF && len(p.Digit) != 1 {
			resp = errMessage(msg.ID, CodeInvalidParams,
				fmt.Errorf("digit 必须是单个字符（0-9 * # A-D）"))
			break
		}
		if msg.Method == MethodSetMicGain && (p.Gain == nil || *p.Gain < 0 || *p.Gain > 200) {
			resp = errMessage(msg.ID, CodeInvalidParams,
				fmt.Errorf("gain 必须是 0–200 的百分比整数"))
			break
		}
		if msg.Method == MethodSetSpeakerVolume && (p.SpeakerVolume == nil || *p.SpeakerVolume < 0 || *p.SpeakerVolume > 100) {
			resp = errMessage(msg.ID, CodeInvalidParams,
				fmt.Errorf("speakerVolume 必须是 0–100 的百分比整数"))
			break
		}
		var err error
		switch msg.Method {
		case MethodAnswer:
			if p.Video {
				err = s.app.AnswerVideo(p.CallID)
			} else {
				err = s.app.Answer(p.CallID)
			}
		case MethodHangup:
			err = s.app.Hangup(p.CallID)
		case MethodReject:
			err = s.app.Reject(p.CallID, p.Code)
		case MethodMute:
			err = s.app.Mute(p.CallID, p.Muted)
		case MethodMuteMic:
			err = s.app.MuteMic(p.CallID, p.Muted)
		case MethodMuteSpeaker:
			err = s.app.MuteSpeaker(p.CallID, p.Muted)
		case MethodSetMicGain:
			err = s.app.SetMicGain(p.CallID, float32(*p.Gain)/100)
		case MethodSetSpeakerVolume:
			err = s.app.SetSpeakerVolume(p.CallID, float32(*p.SpeakerVolume)/100)
		case MethodSetVideo:
			err = s.app.SetVideoEnabled(p.CallID, p.Enabled)
		case MethodSetVideoPreview:
			// 本地回显开关（纯本地，REQ-VIDEO-009）：无 UI 版没有视频窗口，
			// 这里只是与桌面口径保持一致（网页订阅本地画面走 video.subscribe）
			err = s.app.SetVideoPreview(p.CallID, p.Enabled)
		case MethodDTMF:
			err = s.app.SendDTMF(p.CallID, p.Digit)
		}
		if err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, map[string]any{"callId": p.CallID})

	case MethodVideoSubscribe:
		// 视频功能不可用（本构建没有视频 / 用户关闭了「启用视频通话」总开关，
		// REQ-VIDEO-014）：订阅直接失败，不建立订阅关系。
		if !s.app.VideoUsable() {
			resp = errMessage(msg.ID, CodeVideoUnsupported, videoUnavailableErr(s.app))
			break
		}
		// 按通道给参数：{"remote":{...}} 必订（缺省用默认参数），
		// {"local":{...}} 可选——不传 local 就是不订阅本地回显。
		// 两条通道可以各要各的分辨率/帧率/质量（本地回显通常小一号）。
		var p struct {
			Remote *videoParamReq `json:"remote"`
			Local  *videoParamReq `json:"local"`
			// KeepWindow 订阅后是否仍保持软电话的视频窗口显示（REQ-VIDEO-009 修订）：
			// 缺省 false = 隐藏窗口（原有行为，画面在网页看）
			KeepWindow bool `json:"keepWindow"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误"))
			break
		}
		var localP, remoteP videoParams
		if p.Local != nil {
			localP = p.Local.params()
		}
		if p.Remote != nil {
			remoteP = p.Remote.params()
		}
		// 越界参数不报错，钳制到合法范围（返回生效值）
		effLocal, effRemote := s.video.Subscribe(c, p.Local != nil, localP, remoteP, p.KeepWindow)
		data := map[string]any{
			"remote": effRemote,
			// 订阅后是否保持软电话视频窗口显示（回显生效值，便于页面自检）
			"keepWindow": p.KeepWindow,
			// 当前通话快照：网页可立刻显示视频状态，无需等 call.video 事件
			"call": activeCall(s.app),
			// 当前旋转/镜像：订阅时以软电话为准（桌面那边可能已经转过）
			"transforms": s.app.VideoTransforms(),
		}
		if p.Local != nil {
			data["local"] = effLocal
		}
		resp = okMessage(msg.ID, data)

	case MethodVideoUnsubscribe:
		s.video.Unsubscribe(c)
		resp = okMessage(msg.ID, map[string]any{"local": false, "remote": false})

	case MethodVideoSetTransform:
		// 旋转/镜像存在**软电话侧**（桌面视频窗口与网页共用一份，断线/退订都不丢）：
		// {"channel":"remote","rotate":true} | {"rotation":90} | {"mirror":true}
		var p struct {
			Channel  string `json:"channel"`
			Rotate   bool   `json:"rotate"`
			Rotation *int   `json:"rotation"`
			Mirror   *bool  `json:"mirror"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误"))
			break
		}
		var (
			st  core.VideoTransforms
			err error
		)
		switch {
		case p.Rotate:
			st, err = s.app.RotateVideo(p.Channel)
		case p.Rotation != nil:
			st, err = s.app.SetVideoRotation(p.Channel, *p.Rotation)
		case p.Mirror != nil:
			st, err = s.app.SetVideoMirror(p.Channel, *p.Mirror)
		default:
			err = fmt.Errorf("需要 rotate / rotation / mirror 之一")
		}
		if err != nil {
			resp = errMessage(msg.ID, CodeInvalidParams, err)
			break
		}
		resp = okMessage(msg.ID, st)

	case MethodContactExport:
		// 导出通信录 CSV（REQ-WS-015）：网页端一键下载，不做增删改查
		csv, err := s.app.ExportContactsCSV()
		if err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, map[string]any{
			"csv":   csv,
			"count": s.app.ContactCount(),
		})

	case MethodContactImport:
		// 导入通信录 CSV（REQ-WS-015）：按号码去重，已存在的只覆盖号码/名称/备注，
		// **不覆盖常用标志**（与桌面端导入同一份实现，见 core.ImportContactsCSV）
		var p struct {
			CSV string `json:"csv"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || p.CSV == "" {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 csv"))
			break
		}
		res, err := s.app.ImportContactsCSV(p.CSV)
		if err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, map[string]any{
			"added":   res.Added,
			"updated": res.Updated,
			"skipped": res.Skipped,
			"total":   s.app.ContactCount(),
		})

	case MethodGetState:
		resp = okMessage(msg.ID, s.app.State())

	case MethodGetCallPanel:
		resp = okMessage(msg.ID, s.app.PanelState())

	case MethodGetAgentState:
		resp = okMessage(msg.ID, s.app.AgentState())

	case MethodSetAutoAnswer:
		// 自动接听开关（桌面按钮与网页开关共用一份状态，变更后广播
		// system.autoAnswer，两边同步显示）
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误"))
			break
		}
		if err := s.app.SetAutoAnswer(p.Enabled); err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, map[string]any{"enabled": p.Enabled})

	case MethodRegister, MethodUnregister:
		// 登录 / 退出账号（REQ-ACC-024 / REQ-WS-021）：与托盘菜单一致——
		// **共用「启用账户」这一个配置项**并落盘：
		//   register   → 启用账户 → 立即发起注册；
		//   unregister → 禁用账户 → 发 expires=0、停注册周期。
		// 注册/注销在后台进行，结果经 register.state 事件回显（这里只回
		// 已接受的配置状态，不等待网络结果）。
		on := msg.Method == MethodRegister
		if err := s.app.SetRegistered(on); err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, map[string]any{
			"enabled": on,
			"state":   string(s.app.State().Register.State),
		})

	case MethodSetUpdateConfig:
		// 自动更新配置（REQ-UPD-018）：网页可写入站点/应用 ID/周期/预览版，
		// 软电话据此自己定时检查（配了站点才查）。字段**都是可选的**，
		// 只改传了的（用指针区分"没传"与"传了零值"），写入后落盘并广播
		// system.updateConfig，桌面配置页与其它网页同步。
		var p struct {
			URL             *string `json:"url"`
			AppID           *string `json:"appId"`
			IntervalMinutes *int    `json:"intervalMinutes"`
			Prerelease      *bool   `json:"prerelease"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误"))
			break
		}
		cfg := s.app.UpdateConfig()
		if p.URL != nil {
			cfg.URL = *p.URL
		}
		if p.AppID != nil {
			cfg.AppID = *p.AppID
		}
		if p.IntervalMinutes != nil {
			cfg.IntervalMins = *p.IntervalMinutes
		}
		if p.Prerelease != nil {
			cfg.Prerelease = *p.Prerelease
		}
		if err := s.app.SetUpdateConfig(cfg); err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, s.app.UpdateConfig())

	case MethodNotifyUpdate:
		var p struct {
			Version string `json:"version"`
			URL     string `json:"url"`
			SHA256  string `json:"sha256"`
			Notes   string `json:"notes"`
			OS      string `json:"os"`
			Arch    string `json:"arch"`
			Force   bool   `json:"force"` // 强制推送：跳过版本高低检查
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误"))
			break
		}
		// 参数校验（必填、地址合法性、平台一致性、版本高低）在 updater 内完成
		err := s.app.NotifyUpdate(core.UpdateInfo{
			Version: p.Version, URL: p.URL, SHA256: p.SHA256,
			Notes: p.Notes, OS: p.OS, Arch: p.Arch, Force: p.Force,
		})
		if err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		st := s.app.UpdateStatus()
		resp = okMessage(msg.ID, map[string]string{
			"phase":          st.Phase,
			"version":        st.Info.Version,
			"currentVersion": core.Version,
		})

	default:
		// 分组的子分发器（各文件一组方法，主 switch 不再无限膨胀）：
		// 依次询问，谁认领谁返回响应。
		if r, ok := s.dispatchCTI(ctx, msg); ok {
			resp = r
			break
		}
		if r, ok := s.dispatchCallLog(msg); ok {
			resp = r
			break
		}
		if r, ok := s.dispatchContacts(msg); ok {
			resp = r
			break
		}
		if r, ok := s.dispatchConfig(c, msg); ok {
			resp = r
			break
		}
		if r, ok := s.dispatchUpdate(ctx, msg); ok {
			resp = r
			break
		}
		resp = errMessage(msg.ID, CodeUnknownMethod, fmt.Errorf("未知方法: %s", msg.Method))
	}

	// 异步回包的方法（设备枚举等）返回零值 Message：响应已经由它们自己发出
	if resp.Type == "" {
		return
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := writeMsg(ctx, conn, resp); err != nil {
		logx.Logger().Debug("响应发送失败", "method", msg.Method, "err", err)
	}
}

// reply 发送响应（解析失败等场景，无需关联具体连接上下文）。
func (s *Server) reply(c *wsConn, msg Message) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = writeMsg(writeCtx, c.conn, msg)
}

// videoUnavailableErr 视频不可用时给网页的错误：区分"本构建没有视频"
// 与"用户在配置里关掉了视频通话"（REQ-VIDEO-014），网页提示文案不同。
func videoUnavailableErr(app *core.App) error {
	if !app.VideoSupported() {
		return core.ErrVideoUnsupported
	}
	return core.ErrVideoDisabled
}

// codeOf 将 Go error 映射为协议错误码。
func codeOf(err error) string {
	switch {
	case errors.Is(err, core.ErrTelephonyNotReady):
		return CodeNotReady
	case errors.Is(err, core.ErrCallNotFound):
		return CodeCallNotFound
	case errors.Is(err, core.ErrVideoUnsupported), errors.Is(err, core.ErrVideoDisabled):
		return CodeVideoUnsupported
	case errors.Is(err, core.ErrUpdateNotNewer):
		return CodeUpdateNotNewer
	case errors.Is(err, core.ErrUpdateInvalid):
		return CodeInvalidParams
	case errors.Is(err, core.ErrContactsUnavailable):
		return CodeContactsFailed
	case errors.Is(err, core.ErrContactNumberExists), errors.Is(err, core.ErrContactNumberEmpty):
		return CodeInvalidParams
	case errors.Is(err, core.ErrUpdateInProgress):
		return CodeInternal
	case errors.Is(err, core.ErrCallLogUnavailable):
		return CodeCallLogUnavailable
	case errors.Is(err, core.ErrAudioUnavailable):
		return CodeAudioUnavailable
	case errors.Is(err, core.ErrConfigUnavailable):
		return CodeConfigUnavailable
	case errors.Is(err, core.ErrConfigInvalid):
		return CodeInvalidParams
	case errors.Is(err, core.ErrCTIBusy):
		return CodeCTIBusy
	case errors.Is(err, errCTITimeout):
		return CodeCTITimeout
	default:
		return CodeInternal
	}
}

// writeMsg 序列化并写入消息。
func writeMsg(ctx context.Context, conn *websocket.Conn, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}
