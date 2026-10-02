package wsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 读超时：心跳周期内无消息则断开
const readTimeout = 60 * time.Second

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

	// 连接建立即推送当前状态快照（system.state 事件），客户端无需等待
	go func() {
		c.wmu.Lock()
		defer c.wmu.Unlock()
		msg := Message{Type: "event", Event: "system.state", Data: s.app.State()}
		_ = writeMsg(connCtx, conn, msg)
	}()

	// 心跳：定期 Ping 探测连接活性并保持链路畅通。
	// 读超时由客户端心跳消息（SDK 每 25 秒发空消息）重置，此处 Ping
	// 用于探测死连接（等 pong 失败即断开）——此前无任何心跳，空闲
	// 60 秒触发读超时断开后由 SDK 自动重连（用户反馈"过段时间断开
	// 又重连"——bug 修复）。
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
					logx.Logger().Debug("WS 心跳失败，断开连接", "err", err)
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
				logx.Logger().Debug("WS 连接断开", "err", err)
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
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || p.Target == "" {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 target"))
			break
		}
		var (
			callID string
			err    error
		)
		if p.Video {
			callID, err = s.app.DialVideo(ctx, p.Target)
		} else {
			callID, err = s.app.Dial(ctx, p.Target)
		}
		if err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, map[string]string{"callId": callID})

	case MethodAnswer, MethodHangup, MethodReject, MethodMute, MethodMuteMic, MethodMuteSpeaker, MethodSetVideo:
		var p struct {
			CallID  string `json:"callId"`
			Code    int    `json:"code"`
			Muted   bool   `json:"muted"`
			Video   bool   `json:"video"`   // 仅 call.answer：true = 视频接听（REQ-VIDEO-004）
			Enabled bool   `json:"enabled"` // 仅 call.setVideo：视频总开关（REQ-VIDEO-008）
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || p.CallID == "" {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 callId"))
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
		case MethodSetVideo:
			err = s.app.SetVideoEnabled(p.CallID, p.Enabled)
		}
		if err != nil {
			resp = errMessage(msg.ID, codeOf(err), err)
			break
		}
		resp = okMessage(msg.ID, map[string]any{"callId": p.CallID})

	case MethodVideoSubscribe:
		var p struct {
			Local     bool `json:"local"`  // 是否同时订阅本地画面（可选）
			Remote    bool `json:"remote"` // 保留字段：**恒为 true**（订阅必含远端画面）
			MaxWidth  int  `json:"maxWidth"`
			MaxHeight int  `json:"maxHeight"`
			FPS       int  `json:"fps"`
			Quality   int  `json:"quality"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误"))
			break
		}
		// 越界参数不报错，钳制到合法范围（返回生效值）
		eff := s.video.Subscribe(c, p.Local, videoParams{
			MaxW: p.MaxWidth, MaxH: p.MaxHeight, FPS: p.FPS, Quality: p.Quality,
		})
		resp = okMessage(msg.ID, map[string]any{
			"local": p.Local, "remote": true,
			"maxWidth": eff.MaxW, "maxHeight": eff.MaxH,
			"fps": eff.FPS, "quality": eff.Quality,
			// 当前通话快照：网页可立刻显示视频状态，无需等 call.video 事件
			"call": activeCall(s.app),
		})

	case MethodVideoUnsubscribe:
		s.video.Unsubscribe(c)
		resp = okMessage(msg.ID, map[string]any{"local": false, "remote": false})

	case MethodGetState:
		resp = okMessage(msg.ID, s.app.State())

	case MethodNotifyUpdate:
		var p struct {
			Version string `json:"version"`
			URL     string `json:"url"`
			SHA256  string `json:"sha256"`
			Notes   string `json:"notes"`
			OS      string `json:"os"`
			Arch    string `json:"arch"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			resp = errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误"))
			break
		}
		// 参数校验（必填、地址合法性、平台一致性、版本高低）在 updater 内完成
		err := s.app.NotifyUpdate(core.UpdateInfo{
			Version: p.Version, URL: p.URL, SHA256: p.SHA256,
			Notes: p.Notes, OS: p.OS, Arch: p.Arch,
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
		resp = errMessage(msg.ID, CodeUnknownMethod, fmt.Errorf("未知方法: %s", msg.Method))
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

// codeOf 将 Go error 映射为协议错误码。
func codeOf(err error) string {
	switch {
	case errors.Is(err, core.ErrTelephonyNotReady):
		return CodeNotReady
	case errors.Is(err, core.ErrCallNotFound):
		return CodeCallNotFound
	case errors.Is(err, core.ErrUpdateNotNewer):
		return CodeUpdateNotNewer
	case errors.Is(err, core.ErrUpdateInvalid):
		return CodeInvalidParams
	case errors.Is(err, core.ErrUpdateInProgress):
		return CodeInternal
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
