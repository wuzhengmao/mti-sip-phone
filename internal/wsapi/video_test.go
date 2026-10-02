package wsapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/jpeg"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// readBinary 读取一条二进制帧（跳过穿插的文本事件）。
func readBinary(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		typ, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("读取二进制帧失败: %v", err)
		}
		if typ == websocket.MessageBinary {
			return data
		}
	}
	t.Fatalf("等待二进制帧超时")
	return nil
}

// subscribeVideo 发送订阅请求并校验响应与 video.web 事件。
func subscribeVideo(t *testing.T, conn *websocket.Conn, params string) (Message, Message) {
	t.Helper()
	sendRequest(t, conn, Message{
		ID:     json.RawMessage("1"),
		Method: MethodVideoSubscribe,
		Params: json.RawMessage(params),
	})
	resp, ev := waitResponseAndEvent(t, conn, EvVideoWeb)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("订阅应成功，实际响应: %+v", resp)
	}
	return resp, ev
}

// channelParams 取响应里某条通道的生效参数（不存在时返回 nil）。
func channelParams(t *testing.T, resp Message, channel string) map[string]any {
	t.Helper()
	data, _ := resp.Data.(map[string]any)
	p, _ := data[channel].(map[string]any)
	return p
}

// TestVideoSubscribeFrames 订阅 → 帧以二进制推送（通道/序号/JPEG、按参数缩放）
// → 退订后不再推帧。
func TestVideoSubscribeFrames(t *testing.T) {
	ts, app := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	// 订阅必含远端：即使请求里不写 remote，远端照样用默认参数订阅
	resp, ev := subscribeVideo(t, conn, `{"remote":{"maxWidth":100,"maxHeight":100,"fps":30,"quality":80}}`)
	// 生效参数回显
	if p := channelParams(t, resp, "remote"); p == nil ||
		p["maxWidth"] != float64(100) || p["maxHeight"] != float64(100) ||
		p["fps"] != float64(30) || p["quality"] != float64(80) {
		t.Fatalf("订阅响应参数不符: %+v", resp.Data)
	}
	if p := channelParams(t, resp, "local"); p != nil {
		t.Fatalf("未传 local 时不该订阅本地画面: %+v", p)
	}
	// video.web 事件：有网页在看画面（界面据此隐藏视频窗口）
	webData, _ := ev.Data.(map[string]any)
	if webData["active"] != true || webData["remote"] != true || webData["viewers"] != float64(1) {
		t.Fatalf("video.web 事件负载不符: %+v", webData)
	}

	// 投一帧 200×100 的远端画面：应被缩放（保持宽高比）到 100×50 再编码
	app.VideoFrameSink().OnVideoFrame("call-1", false, image.NewRGBA(image.Rect(0, 0, 200, 100)))
	frame := readBinary(t, conn)
	if frame[0] != frameChannelRemote {
		t.Fatalf("通道字节 = %d，期望远端 %d", frame[0], frameChannelRemote)
	}
	if seq := binary.BigEndian.Uint32(frame[1:5]); seq != 1 {
		t.Fatalf("序号 = %d，期望 1", seq)
	}
	if frame[5] != 0xFF || frame[6] != 0xD8 || frame[7] != 0xFF {
		t.Fatalf("不是 JPEG 数据（头 % x）", frame[5:8])
	}
	img, err := jpeg.Decode(bytes.NewReader(frame[5:]))
	if err != nil {
		t.Fatalf("JPEG 解码失败: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 100 || b.Dy() != 50 {
		t.Fatalf("缩放后尺寸 = %d×%d，期望 100×50", b.Dx(), b.Dy())
	}

	// 退订：video.web(active=false) + 之后不再有帧
	sendRequest(t, conn, Message{ID: json.RawMessage("2"), Method: MethodVideoUnsubscribe, Params: json.RawMessage(`{}`)})
	_, ev2 := waitResponseAndEvent(t, conn, EvVideoWeb)
	if d, _ := ev2.Data.(map[string]any); d["active"] != false || d["viewers"] != float64(0) {
		t.Fatalf("退订后 video.web 负载不符: %+v", d)
	}
	app.VideoFrameSink().OnVideoFrame("call-1", false, image.NewRGBA(image.Rect(0, 0, 64, 48)))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if typ, data, err := conn.Read(ctx); err == nil {
		t.Fatalf("退订后仍收到消息（type=%v, %d 字节）", typ, len(data))
	}
}

// TestVideoSubscribeClampAndLocal 参数钳制、本地通道、两通道分开设置、空参数。
func TestVideoSubscribeClampAndLocal(t *testing.T) {
	ts, app := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	// 越界参数：maxWidth 10 → 64，maxHeight 99999 → 1920，fps 999 → 30，quality 1 → 30
	resp, _ := subscribeVideo(t, conn,
		`{"remote":{"maxWidth":10,"maxHeight":99999,"fps":999,"quality":1}}`)
	if p := channelParams(t, resp, "remote"); p == nil ||
		p["maxWidth"] != float64(64) || p["maxHeight"] != float64(1920) ||
		p["fps"] != float64(30) || p["quality"] != float64(30) {
		t.Fatalf("参数钳制不符: %+v", resp.Data)
	}

	// 两通道各带各的参数（本地回显小一号）
	resp, _ = subscribeVideo(t, conn,
		`{"remote":{"maxWidth":640,"maxHeight":480,"fps":30},"local":{"maxWidth":160,"maxHeight":120,"fps":10}}`)
	remote := channelParams(t, resp, "remote")
	local := channelParams(t, resp, "local")
	if remote["maxWidth"] != float64(640) || remote["fps"] != float64(30) {
		t.Fatalf("远端参数不符: %+v", remote)
	}
	if local == nil || local["maxWidth"] != float64(160) || local["maxHeight"] != float64(120) ||
		local["fps"] != float64(10) {
		t.Fatalf("本地参数不符: %+v", local)
	}

	// 本地通道的帧：通道字节应为 2，且按本地参数（160×120）缩放
	app.VideoFrameSink().OnVideoFrame("call-1", true, image.NewRGBA(image.Rect(0, 0, 640, 480)))
	frame := readBinary(t, conn)
	if frame[0] != frameChannelLocal {
		t.Fatalf("通道字节 = %d，期望本地 %d", frame[0], frameChannelLocal)
	}
	img, err := jpeg.Decode(bytes.NewReader(frame[5:]))
	if err != nil {
		t.Fatalf("JPEG 解码失败: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 160 || b.Dy() != 120 {
		t.Fatalf("本地画面应按本地参数缩放到 160×120，实际 %d×%d", b.Dx(), b.Dy())
	}

	// 空参数：等价于「只看远端」，用默认参数
	sendRequest(t, conn, Message{
		ID:     json.RawMessage("3"),
		Method: MethodVideoSubscribe,
		Params: json.RawMessage(`{}`),
	})
	resp = waitResponse(t, conn)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("空参数订阅应成功（默认只看远端），实际: %+v", resp)
	}
	if p := channelParams(t, resp, "remote"); p == nil || p["maxWidth"] != float64(640) {
		t.Fatalf("空参数应用默认值: %+v", resp.Data)
	}
	if p := channelParams(t, resp, "local"); p != nil {
		t.Fatalf("空参数不该订阅本地画面: %+v", p)
	}
}

// TestVideoWebEventOnDisconnect 连接断开时自动退订（软电话恢复视频窗口）。
func TestVideoWebEventOnDisconnect(t *testing.T) {
	ts, app := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	subscribeVideo(t, conn, `{"remote":{}}`)
	// 另开一条连接观察广播事件
	observer := dialWS(t, ts.URL)
	waitEvent(t, observer, "system.state")

	_ = conn.Close(websocket.StatusNormalClosure, "")
	ev := waitEvent(t, observer, EvVideoWeb)
	if d, _ := ev.Data.(map[string]any); d["active"] != false || d["viewers"] != float64(0) {
		t.Fatalf("断开后应广播 active=false，实际: %+v", d)
	}
	if st := app.WebVideoState(); st.Active {
		t.Fatalf("断开后订阅状态应清空，实际 %+v", st)
	}
}
