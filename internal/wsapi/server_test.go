package wsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

// stubTel 测试电话桩：Dial 后立即模拟振铃→接通，Hangup 立即结束，
// 用于验证 WS 层方法分发与事件转发链路（真实信令流程由 internal/sip 集成测试覆盖）。
type stubTel struct {
	app *core.App
}

func (s *stubTel) Register(ctx context.Context, acc config.Account) error { return nil }
func (s *stubTel) Unregister(ctx context.Context) error                   { return nil }
func (s *stubTel) Dial(ctx context.Context, target string) (string, error) {
	id := "stub-1"
	go func() {
		// 等待 App 完成通话登记（避免事件先于 calls 登记到达被丢弃）
		time.Sleep(20 * time.Millisecond)
		s.app.OnRinging(id)
		s.app.OnEstablished(id)
	}()
	return id, nil
}
func (s *stubTel) Answer(callID string) error { return nil }
func (s *stubTel) Hangup(callID string) error {
	s.app.OnTerminated(callID, "local_hangup")
	return nil
}
func (s *stubTel) Reject(callID string, code int) error  { return nil }
func (s *stubTel) Mute(callID string, muted bool) error  { return nil }
func (s *stubTel) SendDTMF(callID, digit string) error   { return nil }
func (s *stubTel) SetAutoAnswer(enabled bool) error      { return nil }
func (s *stubTel) SetRPort(enabled bool) error            { return nil }
func (s *stubTel) SetMicrophone(deviceID string) error   { return nil }
func (s *stubTel) SetSpeaker(deviceID string) error      { return nil }
func (s *stubTel) AudioDevices() ([]core.AudioDevice, []core.AudioDevice) {
	return nil, nil
}
func (s *stubTel) Close() error { return nil }

// newTestServer 构造测试用服务：stub 电话层 + httptest 服务。
func newTestServer(t *testing.T) (*httptest.Server, *core.App) {
	t.Helper()
	cfg := config.Default()

	bus := core.NewBus()
	app := core.New(cfg, nil, bus)
	tel := &stubTel{app: app}
	app.SetTel(tel)
	app.Start(context.Background())

	srv := New(app)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, app
}

// dialWS 建立测试 WS 连接（无 token 鉴权）。
func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(url, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("WS 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

// readMsg 读取一条消息并解析。
func readMsg(t *testing.T, conn *websocket.Conn) Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("读取消息失败: %v", err)
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("消息解析失败: %v", err)
	}
	return msg
}

// sendRequest 发送请求。
func sendRequest(t *testing.T, conn *websocket.Conn, msg Message) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
}

// 等待指定事件（跳过其他事件）。
func waitEvent(t *testing.T, conn *websocket.Conn, name string) Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := readMsg(t, conn)
		if msg.Type == "event" && msg.Event == name {
			return msg
		}
	}
	t.Fatalf("等待事件 %s 超时", name)
	return Message{}
}

// waitResponse 等待响应消息（跳过穿插的事件，如 call.dialing 等）。
func waitResponse(t *testing.T, conn *websocket.Conn) Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := readMsg(t, conn)
		if msg.Type == "response" {
			return msg
		}
	}
	t.Fatalf("等待响应超时")
	return Message{}
}

// waitResponseAndEvent 同时等待响应与指定事件（两者到达顺序不定）。
func waitResponseAndEvent(t *testing.T, conn *websocket.Conn, event string) (Message, Message) {
	t.Helper()
	var (
		resp, ev     Message
		gotResp, got bool
	)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := readMsg(t, conn)
		switch {
		case msg.Type == "response":
			resp = msg
			gotResp = true
		case msg.Type == "event" && msg.Event == event:
			ev = msg
			got = true
		}
		if gotResp && got {
			return resp, ev
		}
	}
	t.Fatalf("等待响应与事件 %s 超时（响应: %v 事件: %v）", event, resp, ev)
	return resp, ev
}

// TestWSConnect 无 token 鉴权：连接即可建立并收到状态快照事件。
func TestWSConnect(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dialWS(t, ts.URL)
	// 连接建立应立即收到状态快照事件
	msg := waitEvent(t, conn, "system.state")
	if msg.Data == nil {
		t.Fatal("system.state 事件缺少数据")
	}
}

func TestGetState(t *testing.T) {
	ts, app := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "system.getState", Params: json.RawMessage("{}")})
	msg := waitResponse(t, conn)
	if msg.OK == nil || !*msg.OK {
		t.Fatalf("getState 应成功，收到: %+v", msg)
	}

	// 验证返回的状态与 App 一致
	state := app.State()
	data, _ := json.Marshal(msg.Data)
	var got core.State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("返回数据格式错误: %v", err)
	}
	if got.Register.State != state.Register.State {
		t.Fatalf("状态不一致: got=%+v want=%+v", got, state)
	}
}

// TestDialFlow 演示模式完整流程：拨号 → 振铃 → 接通 → 挂断。
func TestDialFlow(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	// 拨号：响应与 call.dialing 事件到达顺序不定，同时等待
	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "call.dial",
		Params: json.RawMessage(`{"target":"1001"}`)})
	msg, _ := waitResponseAndEvent(t, conn, "call.dialing")
	if msg.OK == nil || !*msg.OK {
		t.Fatalf("拨号应成功，收到: %+v", msg)
	}
	callID := msg.Data.(map[string]any)["callId"].(string)

	// 振铃事件
	waitEvent(t, conn, "call.ringing")
	// 接通事件
	waitEvent(t, conn, "call.established")

	// 挂断：响应与 call.terminated 事件到达顺序不定，同时等待
	sendRequest(t, conn, Message{ID: json.RawMessage("2"), Method: "call.hangup",
		Params: json.RawMessage(`{"callId":"` + callID + `"}`)})
	msg, _ = waitResponseAndEvent(t, conn, "call.terminated")
	if msg.OK == nil || !*msg.OK {
		t.Fatalf("挂断应成功，收到: %+v", msg)
	}
}


// TestDialRejectedWhileActive 单通话限制：已有通话时第二次拨号被拒绝。
func TestDialRejectedWhileActive(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	// 第一通：拨号并等待接通
	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "call.dial",
		Params: json.RawMessage(`{"target":"1001"}`)})
	msg, _ := waitResponseAndEvent(t, conn, "call.dialing")
	if msg.OK == nil || !*msg.OK {
		t.Fatalf("第一次拨号应成功，收到: %+v", msg)
	}
	waitEvent(t, conn, "call.established")

	// 第二通：应被拒绝（已有通话进行中）
	sendRequest(t, conn, Message{ID: json.RawMessage("2"), Method: "call.dial",
		Params: json.RawMessage(`{"target":"1002"}`)})
	msg = waitResponse(t, conn)
	if msg.Error == nil {
		t.Fatalf("已有通话时第二次拨号应失败，收到: %+v", msg)
	}
}

// TestUnknownMethod 未知方法应返回错误码。
func TestUnknownMethod(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	sendRequest(t, conn, Message{ID: json.RawMessage("9"), Method: "foo.bar", Params: json.RawMessage("{}")})
	msg := waitResponse(t, conn)
	if msg.OK == nil || *msg.OK {
		t.Fatalf("未知方法应失败，收到: %+v", msg)
	}
	if msg.Error == nil || msg.Error.Code != CodeUnknownMethod {
		t.Fatalf("错误码应为 %s，收到: %+v", CodeUnknownMethod, msg.Error)
	}
}

// TestHangupUnknownCall 挂断不存在的通话应返回 call_not_found。
func TestHangupUnknownCall(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "call.hangup",
		Params: json.RawMessage(`{"callId":"nonexistent"}`)})
	msg := waitResponse(t, conn)
	if msg.Error == nil || msg.Error.Code != CodeCallNotFound {
		t.Fatalf("错误码应为 %s，收到: %+v", CodeCallNotFound, msg.Error)
	}
}

// TestIndexPage 测试页面正常返回且不含 token 占位符。
func TestIndexPage(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("请求测试页面失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码应为 200，收到 %d", resp.StatusCode)
	}
	if strings.Contains(string(body), "__SDK_TOKEN__") {
		t.Fatal("测试页面残留 token 占位符")
	}
}
