package wsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/updater"
)

// stubTel 测试电话桩：Dial 后立即模拟振铃→接通，Hangup 立即结束，
// 用于验证 WS 层方法分发与事件转发链路（真实信令流程由 internal/sip 集成测试覆盖）。
type stubTel struct {
	app *core.App

	mu   sync.Mutex // 记录发给电话层的 DTMF，供测试断言（handler 在自己的 goroutine 里跑）
	dtmf []string

	videoDisabled atomic.Bool // 「启用视频通话」总开关（默认 false = 启用，REQ-VIDEO-014）
}

func (s *stubTel) Register(ctx context.Context, acc config.Account) error { return nil }
func (s *stubTel) Unregister(ctx context.Context) error                   { return nil }
func (s *stubTel) Dial(ctx context.Context, target string) (string, error) {
	id := "stub-1"
	go func() {
		// 等待 App 完成通话登记（避免事件先于 calls 登记到达被丢弃）
		time.Sleep(20 * time.Millisecond)
		s.app.OnRinging(id, false)
		s.app.OnEstablished(id)
	}()
	return id, nil
}
func (s *stubTel) Answer(callID string) error { return nil }
func (s *stubTel) Hangup(callID string) error {
	s.app.OnTerminated(callID, "local_hangup")
	return nil
}
func (s *stubTel) Reject(callID string, code int) error        { return nil }
func (s *stubTel) Mute(callID string, muted bool) error        { return nil }
func (s *stubTel) MuteMic(callID string, muted bool) error     { return nil }
func (s *stubTel) MuteSpeaker(callID string, muted bool) error { return nil }
func (s *stubTel) SetMicGain(callID string, gain float32) error {
	return nil
}
func (s *stubTel) SetSpeakerVolume(callID string, vol float32) error { return nil }
func (s *stubTel) SendDTMF(callID, digit string) error {
	s.mu.Lock()
	s.dtmf = append(s.dtmf, digit)
	s.mu.Unlock()
	return nil
}

// dtmfDigits 已记录的 DTMF 序列（副本）。
func (s *stubTel) dtmfDigits() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dtmf...)
}
func (s *stubTel) SetAutoAnswer(enabled bool) error    { return nil }
func (s *stubTel) SetRPort(enabled bool) error         { return nil }
func (s *stubTel) SetMicrophone(deviceID string) error { return nil }
func (s *stubTel) SetSpeaker(deviceID string) error    { return nil }
func (s *stubTel) AudioDevices() ([]core.AudioDevice, []core.AudioDevice) {
	return nil, nil
}
func (s *stubTel) Close() error { return nil }

// ---- 视频（默认「有视频能力且总开关开启」：订阅视频的测试要用）----
// 关掉总开关（SetVideoCallEnabled(false)）后可验证 REQ-VIDEO-014 的失败路径。

func (s *stubTel) VideoSupported() bool   { return true }
func (s *stubTel) VideoCallEnabled() bool { return !s.videoDisabled.Load() }
func (s *stubTel) SetVideoCallEnabled(on bool) error {
	s.videoDisabled.Store(!on)
	return nil
}
func (s *stubTel) DialVideo(ctx context.Context, target string) (string, error) {
	return "", core.ErrVideoUnsupported
}
func (s *stubTel) AnswerVideo(callID string) error     { return core.ErrVideoUnsupported }
func (s *stubTel) IncomingHasVideo(callID string) bool { return false }
func (s *stubTel) VideoActive(callID string) bool      { return false }
func (s *stubTel) SetVideoEnabled(callID string, on bool) error {
	return core.ErrVideoUnsupported
}
func (s *stubTel) SetVideoPreview(callID string, on bool) error { return core.ErrVideoUnsupported }
func (s *stubTel) SetCamera(deviceID string) error              { return core.ErrVideoUnsupported }
func (s *stubTel) VideoDevices() []core.VideoDevice             { return nil }
func (s *stubTel) CameraSupported() bool                        { return false }

// newTestServer 构造测试用服务：stub 电话层 + 真实更新器 + httptest 服务。
// 更新器指向临时目录下的假 exe 路径（只做参数校验与状态机，不触发下载）。
func newTestServer(t *testing.T) (*httptest.Server, *core.App) {
	t.Helper()
	ts, app, _ := newTestServerAll(t)
	return ts, app
}

// newTestServerAll 同 newTestServer，另返回电话桩供断言（如 DTMF 收到什么）。
func newTestServerAll(t *testing.T) (*httptest.Server, *core.App, *stubTel) {
	t.Helper()
	cfg := config.Default()

	bus := core.NewBus()
	app := core.New(cfg, nil, bus)
	tel := &stubTel{app: app}
	app.SetTel(tel)
	app.SetUpdater(updater.New(updater.Options{
		Bus:     bus,
		ExePath: filepath.Join(t.TempDir(), "sip-phone-test.exe"),
	}))
	app.Start(context.Background())

	srv := New(app)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, app, tel
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
	// 版本与平台/架构随状态上报（前端据此查询对应产物的更新）
	if got.Version != core.Version {
		t.Errorf("版本号 = %q，期望 %q", got.Version, core.Version)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Errorf("平台/架构 = %s/%s，期望 %s/%s", got.OS, got.Arch, runtime.GOOS, runtime.GOARCH)
	}
}

// TestNotifyUpdate 前端告知新版本：成功、版本不更高、参数非法三种路径。
func TestNotifyUpdate(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	// 1) 更高版本：受理并回 available，同时推送 system.update 事件
	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "system.notifyUpdate",
		Params: json.RawMessage(`{"version":"9.9.9","url":"http://dl.example.com/a.exe"}`)})
	resp, ev := waitResponseAndEvent(t, conn, "system.update")
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("notifyUpdate 应成功，收到: %+v", resp)
	}
	data, _ := json.Marshal(resp.Data)
	var got map[string]string
	_ = json.Unmarshal(data, &got)
	if got["phase"] != core.UpdateAvailable || got["currentVersion"] != core.Version {
		t.Errorf("响应数据异常: %+v", got)
	}
	evData, _ := json.Marshal(ev.Data)
	var st core.UpdateStatus
	_ = json.Unmarshal(evData, &st)
	if st.Phase != core.UpdateAvailable || st.Host != "dl.example.com" {
		t.Errorf("system.update 事件异常: %+v", st)
	}

	// 2) 版本不高于当前：update_not_newer
	sendRequest(t, conn, Message{ID: json.RawMessage("2"), Method: "system.notifyUpdate",
		Params: json.RawMessage(`{"version":"` + core.Version + `","url":"http://dl.example.com/a.exe"}`)})
	resp = waitResponse(t, conn)
	if resp.OK == nil || *resp.OK || resp.Error == nil || resp.Error.Code != CodeUpdateNotNewer {
		t.Fatalf("期望 update_not_newer，收到: %+v", resp)
	}

	// 3) 参数非法：invalid_params（缺地址 / 非 http(s)）
	for i, params := range []string{
		`{"version":"9.9.9"}`,
		`{"version":"9.9.9","url":"ftp://dl.example.com/a.exe"}`,
	} {
		sendRequest(t, conn, Message{ID: json.RawMessage("3"), Method: "system.notifyUpdate",
			Params: json.RawMessage(params)})
		resp = waitResponse(t, conn)
		if resp.OK == nil || *resp.OK || resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Fatalf("用例 %d：期望 invalid_params，收到: %+v", i, resp)
		}
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

// TestCallFailedCarriesReason call.failed / call.terminated 事件必须带 reason
// （回归：publishCall 的附加字段要**键值成对**，早先只传了一个值，
// 事件里因此一直没有 reason，网页显示的失败原因恒为空）。
func TestCallFailedCarriesReason(t *testing.T) {
	ts, app := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	app.OnIncoming("c1", "1002", "1002", false)
	ev := waitEvent(t, conn, "call.incoming")
	if d, _ := ev.Data.(map[string]any); d["callId"] != "c1" {
		t.Fatalf("来电事件异常: %+v", d)
	}

	app.OnFailed("c1", "486 Busy Here")
	ev = waitEvent(t, conn, "call.failed")
	d, _ := ev.Data.(map[string]any)
	if d["reason"] != "486 Busy Here" {
		t.Fatalf("call.failed 应带 reason，实际: %+v", d)
	}

	app.OnIncoming("c2", "1003", "1003", false)
	waitEvent(t, conn, "call.incoming")
	app.OnTerminated("c2", "remote_hangup")
	ev = waitEvent(t, conn, "call.terminated")
	d, _ = ev.Data.(map[string]any)
	if d["reason"] != "remote_hangup" {
		t.Fatalf("call.terminated 应带 reason，实际: %+v", d)
	}
}

// TestMuteMicSpeakerFlow 麦克风/扬声器开关（REQ-UI-023）：方法生效、
// 状态记入通话快照，并广播 call.audio 事件（桌面面板与网页据此同步按钮外观）。
func TestMuteMicSpeakerFlow(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "call.dial",
		Params: json.RawMessage(`{"target":"1001"}`)})
	msg, _ := waitResponseAndEvent(t, conn, "call.dialing")
	callID := msg.Data.(map[string]any)["callId"].(string)
	waitEvent(t, conn, "call.established")

	sendRequest(t, conn, Message{ID: json.RawMessage("2"), Method: MethodMuteMic,
		Params: json.RawMessage(`{"callId":"` + callID + `","muted":true}`)})
	resp, ev := waitResponseAndEvent(t, conn, EvCallAudio)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("麦克风静音应成功，收到: %+v", resp)
	}
	if call := ev.Data.(map[string]any)["call"].(map[string]any); call["micMuted"] != true {
		t.Fatalf("call.audio 事件里 micMuted 应为 true: %+v", call)
	}

	sendRequest(t, conn, Message{ID: json.RawMessage("3"), Method: MethodMuteSpeaker,
		Params: json.RawMessage(`{"callId":"` + callID + `","muted":true}`)})
	resp, ev = waitResponseAndEvent(t, conn, EvCallAudio)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("扬声器静音应成功，收到: %+v", resp)
	}
	call := ev.Data.(map[string]any)["call"].(map[string]any)
	if call["speakerMuted"] != true || call["micMuted"] != true {
		t.Fatalf("call.audio 事件状态不符: %+v", call)
	}
}

// TestDTMFFlow 网页经 call.dtmf 让软电话发 DTMF（REQ-WS-014）：digit 透传到电话层，
// 参数缺失或 digit 不是单字符报 invalid_params。
func TestDTMFFlow(t *testing.T) {
	ts, _, tel := newTestServerAll(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "call.dial",
		Params: json.RawMessage(`{"target":"1001"}`)})
	msg, _ := waitResponseAndEvent(t, conn, "call.dialing")
	callID := msg.Data.(map[string]any)["callId"].(string)
	waitEvent(t, conn, "call.established")

	for i, digit := range []string{"5", "#", "A"} {
		sendRequest(t, conn, Message{ID: json.RawMessage(strconv.Itoa(i + 2)), Method: MethodDTMF,
			Params: json.RawMessage(`{"callId":"` + callID + `","digit":"` + digit + `"}`)})
		resp := waitResponse(t, conn)
		if resp.OK == nil || !*resp.OK {
			t.Fatalf("发送 DTMF %q 应成功，收到: %+v（错误: %+v）", digit, resp, resp.Error)
		}
	}
	if got := tel.dtmfDigits(); len(got) != 3 || got[0] != "5" || got[1] != "#" || got[2] != "A" {
		t.Fatalf("电话层收到的 DTMF 序列为 %v，期望 [5 # A]", got)
	}

	sendRequest(t, conn, Message{ID: json.RawMessage("9"), Method: MethodDTMF,
		Params: json.RawMessage(`{"callId":"` + callID + `","digit":"55"}`)})
	resp := waitResponse(t, conn)
	if resp.OK == nil || *resp.OK {
		t.Fatalf("多位 digit 应报错，收到: %+v", resp)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("错误码应为 invalid_params，收到: %+v", resp.Error)
	}
}

// TestSetAutoAnswerFlow 网页设置自动接听开关：响应回生效值、广播
// system.autoAnswer，且状态查询里能查到当前值（桌面按钮与网页共用一份）。
func TestSetAutoAnswerFlow(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: MethodSetAutoAnswer,
		Params: json.RawMessage(`{"enabled":true}`)})
	resp, ev := waitResponseAndEvent(t, conn, EvAutoAnswer)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("开启自动接听应成功，收到: %+v", resp)
	}
	if d, _ := resp.Data.(map[string]any); d["enabled"] != true {
		t.Fatalf("响应应回生效值 enabled=true: %+v", resp.Data)
	}
	if d, _ := ev.Data.(map[string]any); d["enabled"] != true {
		t.Fatalf("system.autoAnswer 事件负载应为 enabled=true: %+v", ev.Data)
	}

	// 状态快照里也要带上当前值（网页刷新/重连后据此显示勾选状态）
	sendRequest(t, conn, Message{ID: json.RawMessage("2"), Method: MethodGetState,
		Params: json.RawMessage(`{}`)})
	st := waitResponse(t, conn)
	if d, _ := st.Data.(map[string]any); d["autoAnswer"] != true {
		t.Fatalf("getState 应包含 autoAnswer=true: %+v", st.Data)
	}

	// 关掉：事件与响应都要反映
	sendRequest(t, conn, Message{ID: json.RawMessage("3"), Method: MethodSetAutoAnswer,
		Params: json.RawMessage(`{"enabled":false}`)})
	resp, ev = waitResponseAndEvent(t, conn, EvAutoAnswer)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("关闭自动接听应成功，收到: %+v", resp)
	}
	if d, _ := ev.Data.(map[string]any); d["enabled"] != false {
		t.Fatalf("system.autoAnswer 事件负载应为 enabled=false: %+v", ev.Data)
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

// TestIndexPage 网页控制台正常返回且不含 token 占位符。
func TestIndexPage(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("请求控制台页面失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码应为 200，收到 %d", resp.StatusCode)
	}
	if strings.Contains(string(body), "__SDK_TOKEN__") {
		t.Fatal("页面残留 token 占位符")
	}
	// 控制台外壳的四个视图与常驻通话条（REQ-WEB-001/002）
	for _, id := range []string{`id="view-call"`, `id="view-history"`, `id="view-contacts"`,
		`id="view-settings"`, `id="callBar"`} {
		if !strings.Contains(string(body), id) {
			t.Fatalf("控制台页面缺少 %s", id)
		}
	}
}

// TestWebRoutes 网页资源的白名单路由（REQ-WEB-003）：页面别名、静态资源与 404。
func TestWebRoutes(t *testing.T) {
	ts, _ := newTestServer(t)
	cases := []struct {
		path string
		code int
		ct   string
	}{
		{"/", 200, "text/html"},
		{"/index.html", 200, "text/html"},
		{"/history", 200, "text/html"},   // hash 路由别名
		{"/test.html", 200, "text/html"}, // 旧测试页的兼容入口
		{"/css/app.css", 200, "text/css"},
		{"/js/app.js", 200, "application/javascript"},
		{"/js/views/settings.js", 200, "application/javascript"},
		{"/js/views/diag.js", 200, "application/javascript"},
		{"/sdk/sip-phone-sdk.js", 200, "application/javascript"},
		{"/nope", 404, ""},
		// 目录穿越：net/http 的 mux 先做 path.Clean，归一化后落到白名单外 → 404
		//（`/js/../sdk/...` 会被归一化成 `/sdk/...`，那是个合法路径，不算穿越）
		{"/js/../../etc/passwd", 404, ""},
	}
	for _, c := range cases {
		resp, err := http.Get(ts.URL + c.path)
		if err != nil {
			t.Fatalf("请求 %s 失败: %v", c.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.code {
			t.Fatalf("%s 状态码应为 %d，收到 %d", c.path, c.code, resp.StatusCode)
		}
		// 内嵌资源必须带 no-cache：升级软电话后浏览器拿旧页面是最容易踩的坑
		if c.code == 200 && c.path != "/nope" {
			if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
				t.Fatalf("%s 的 Cache-Control 应为 no-cache，实际 %q", c.path, cc)
			}
		}
		if c.ct != "" {
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, c.ct) {
				t.Fatalf("%s 的 Content-Type 应为 %s*，实际 %q", c.path, c.ct, ct)
			}
		}
	}
}
