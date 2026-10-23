package sip

import (
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/core"
)

// TestParseMSPEventName MSP_EVENT_ 事件名解析：去前缀、转小写、忽略 Event 参数；
// 非 MSP_EVENT_* 返回空。
func TestParseMSPEventName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"MSP_EVENT_CONSULT_STARTED", "consult_started"},
		{"msp_event_three_way_started", "three_way_started"},
		{"MSP_EVENT_CONFERENCE_MEMBERS_CHANGED;id=1", "conference_members_changed"},
		{"MSP_DIAL", ""},
		{"DIALOG", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := parseMSPEventName(tc.in); got != tc.want {
			t.Fatalf("parseMSPEventName(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestSendCTICommandWithoutCall 新协议下反向指令走 out-of-dialog NOTIFY，
// **不要求正在通话**（monitor / takeover 等动作本来就在通话外发起）。
func TestSendCTICommandWithoutCall(t *testing.T) {
	sink := &testSink{}
	p := newTestPhone(t, sink)
	seq, err := p.SendCTICommand("monitor", map[string]any{"target": "2002"})
	if err != nil {
		t.Fatalf("未通话时也应能发送指令（out-of-dialog NOTIFY）：%v", err)
	}
	if seq == "" {
		t.Fatal("应返回指令序号（字符串）")
	}
	// 测试机没有配置账户：发送阶段失败会立即投递 SEND 结果并清掉在途标志
	if p.ctiInFlight {
		t.Fatal("发送失败后不应还留着在途标志")
	}
}

// TestDeliverCTIResultIdempotent INFO 结果投递幂等：只有第一个 seq 匹配的投递
// 生效，后续（超时与回执竞态）不重复回调。
func TestDeliverCTIResultIdempotent(t *testing.T) {
	sink := &testSink{}
	p := newTestPhone(t, sink)

	p.ctiMu.Lock()
	p.ctiInFlight = true
	p.ctiInFlightSeq = "7"
	p.ctiMu.Unlock()

	// 正确 seq 的结果
	p.handleCTIResult("7", true, "", "", "", nil)
	// 竞态：超时/错误 seq 不应再生效
	p.handleCTIResult("7", false, "TIMEOUT", "超时", "", nil)
	p.handleCTIResult("8", false, "TIMEOUT", "超时", "", nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		n := len(sink.ctiResults)
		sink.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.ctiResults) != 1 {
		t.Fatalf("应恰好回调一次结果，实际 %d 次：%+v", len(sink.ctiResults), sink.ctiResults)
	}
	if !sink.ctiResults[0].Success || sink.ctiResults[0].Seq != "7" {
		t.Fatalf("结果应为 seq=7 成功，实际 %+v", sink.ctiResults[0])
	}
	// 投递后应清空在途标志
	p.ctiMu.Lock()
	inFlight := p.ctiInFlight
	p.ctiMu.Unlock()
	if inFlight {
		t.Fatal("结果投递后 ctiInFlight 应为 false")
	}
}

// startNotifyResponder 起一个只回固定状态码的 UDP SIP 服务器（收 NOTIFY）。
func startNotifyResponder(t *testing.T, status int) (string, int) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-notify-responder"))
	if err != nil {
		t.Fatalf("创建 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	srv.OnNotify(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, status, "Test", nil))
	})
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = ua.Close() })
	go func() { _ = srv.ServeUDP(ln) }()
	host, portStr, _ := net.SplitHostPort(ln.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)
	return host, port
}

// TestSendHeartbeatRespectsSIPStatus 协议 1.0.18：MSP_HEARTBEAT 的 SIP 响应
// 不是 200 时返回 ErrCTINotifyUnsupported（核心层据此停心跳、退出坐席模式）；
// 200 则正常返回。
func TestSendHeartbeatRespectsSIPStatus(t *testing.T) {
	sink := &testSink{}

	host, port := startNotifyResponder(t, 200)
	p := newTestPhone(t, sink)
	p.mu.Lock()
	p.acc = testAccount(host, port)
	p.mu.Unlock()
	if err := p.SendHeartbeat("normal", false, "", "free", 50*time.Second); err != nil {
		t.Fatalf("200 响应时心跳应成功，实际 %v", err)
	}

	host2, port2 := startNotifyResponder(t, 503)
	p2 := newTestPhone(t, sink)
	p2.mu.Lock()
	p2.acc = testAccount(host2, port2)
	p2.mu.Unlock()
	if err := p2.SendHeartbeat("normal", false, "", "free", 50*time.Second); !errors.Is(err, core.ErrCTINotifyUnsupported) {
		t.Fatalf("503 响应应返回 ErrCTINotifyUnsupported，实际 %v", err)
	}
}

// TestFlexStringCode MSP_RESULT 的 code 可能是**数字**（CTI 实际下发 770007），
// 协议里写的是字符串——两种都要能解析，否则整条回执被丢弃、界面只能等超时。
// seq 在协议 1.0.18 改成字符串，同样用 flexString 兼容老服务端的数字。
func TestFlexStringCode(t *testing.T) {
	type result struct {
		Seq     flexString `json:"seq"`
		Success bool       `json:"success"`
		Code    flexString `json:"code"`
		Message string     `json:"message"`
	}
	var b result
	body := `{"seq":"2","action":"consult","success":false,"code":770007,"message":"坐席不在通话中：2901"}`
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatalf("数字 code 不应导致解析失败: %v", err)
	}
	if string(b.Code) != "770007" || b.Message != "坐席不在通话中：2901" || b.Success || b.Seq != "2" {
		t.Fatalf("字符串 seq + 数字 code 解析结果不符: %+v", b)
	}
	b = result{}
	if err := json.Unmarshal([]byte(`{"seq":3,"success":false,"code":"AGENT_NOT_IN_CALL"}`), &b); err != nil {
		t.Fatalf("字符串 code 解析失败: %v", err)
	}
	if string(b.Code) != "AGENT_NOT_IN_CALL" || b.Seq != "3" {
		t.Fatalf("字符串 code 应原样保留、数字 seq 应兼容，实际 %+v", b)
	}
}
