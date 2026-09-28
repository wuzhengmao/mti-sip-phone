package sip

// 保持（hold）信令测试：本端保持发 re-INVITE（a=sendonly），
// 取消保持发 re-INVITE（a=sendrecv）；对端保持发 re-INVITE（a=recvonly）
// 时本端按镜像方向应答并停止发送媒体。

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/media"
)

// holdAnswerSDP 测试用 answer（方向 sendrecv）。
const holdAnswerSDP = "v=0\r\n" +
	"o=- 1 1 IN IP4 127.0.0.1\r\n" +
	"s=test\r\n" +
	"c=IN IP4 127.0.0.1\r\n" +
	"t=0 0\r\n" +
	"m=audio 4004 RTP/AVP 0 8\r\n" +
	"a=rtpmap:0 PCMU/8000\r\n" +
	"a=rtpmap:8 PCMA/8000\r\n" +
	"a=sendrecv\r\n"

// TestOutgoingHoldReInvite 呼出接通后保持：对端必须收到 a=sendonly 的
// re-INVITE；取消保持后收到 a=sendrecv 的 re-INVITE。
func TestOutgoingHoldReInvite(t *testing.T) {
	addr, peer := newPeerServer(t, 300*time.Millisecond)
	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	_ = p.Register(context.Background(), acc)

	callID, err := p.Dial(context.Background(), "1002")
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("通话未建立")
	}

	// 保持 → re-INVITE a=sendonly
	if err := p.Mute(callID, true); err != nil {
		t.Fatalf("Mute 失败: %v", err)
	}
	if !peer.waitInviteDir("sendonly", 5*time.Second) {
		t.Fatal("对端未收到 a=sendonly 的保持 re-INVITE")
	}

	// 取消保持 → re-INVITE a=sendrecv
	if err := p.Mute(callID, false); err != nil {
		t.Fatalf("取消 Mute 失败: %v", err)
	}
	if !peer.waitInviteDir("sendrecv", 5*time.Second) {
		t.Fatal("对端未收到 a=sendrecv 的恢复 re-INVITE")
	}

	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
}

// TestIncomingHoldReInvite 呼入接通后保持：对端（本端视角的远端）收到
// a=sendonly 的 re-INVITE。
func TestIncomingHoldReInvite(t *testing.T) {
	sink := &testSink{}
	p, addr := newTestPhoneListening(t, sink)
	uac := newTestUAC(t)
	uac.trackInvites() // 开启对端 re-INVITE 记录与 200 应答

	dlg, _ := uac.invite(t, addr, nil)
	if !sink.waitEvent("incoming:", 5*time.Second) {
		t.Fatal("本地未收到来电事件")
	}
	callID := sink.firstIncomingCallID()

	if err := p.Answer(callID); err != nil {
		t.Fatalf("Answer 失败: %v", err)
	}
	if !uac.waitAnswerOK(5 * time.Second) {
		t.Fatal("对端未收到 200")
	}
	if err := dlg.Ack(context.Background()); err != nil {
		t.Fatalf("对端 ACK 失败: %v", err)
	}
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("本地未进入通话状态")
	}

	// 保持 → 对端收到 re-INVITE a=sendonly
	if err := p.Mute(callID, true); err != nil {
		t.Fatalf("Mute 失败: %v", err)
	}
	if !uac.waitInviteDir("sendonly", 5*time.Second) {
		t.Fatal("对端未收到 a=sendonly 的保持 re-INVITE")
	}

	// 取消保持 → a=sendrecv
	if err := p.Mute(callID, false); err != nil {
		t.Fatalf("取消 Mute 失败: %v", err)
	}
	if !uac.waitInviteDir("sendrecv", 5*time.Second) {
		t.Fatal("对端未收到 a=sendrecv 的恢复 re-INVITE")
	}

	// ACK 的 From/CSeq 必须与 re-INVITE 一致（呼入场景回归防护）
	switch uac.waitAckState(6 * time.Second) {
	case 1:
		// OK
	case 2:
		t.Fatal("re-INVITE 的 ACK 头与请求不一致（对端将丢弃 ACK 并拒绝后续 re-INVITE）")
	case 3:
		t.Fatal("对端未收到 re-INVITE 的 ACK")
	default:
		t.Fatal("ACK 校验超时未出结果")
	}

	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
}

// TestRemoteHoldReInvite 对端发 a=recvonly 的 re-INVITE（对端保持）：
// 本地必须回 200，answer 方向为镜像 a=sendonly，且本地媒体发送被停止。
func TestRemoteHoldReInvite(t *testing.T) {
	sink := &testSink{}
	p, addr := newTestPhoneListening(t, sink)
	uac := newTestUAC(t)

	dlg, _ := uac.invite(t, addr, nil)
	if !sink.waitEvent("incoming:", 5*time.Second) {
		t.Fatal("本地未收到来电事件")
	}
	callID := sink.firstIncomingCallID()

	if err := p.Answer(callID); err != nil {
		t.Fatalf("Answer 失败: %v", err)
	}
	if !uac.waitAnswerOK(5 * time.Second) {
		t.Fatal("对端未收到 200")
	}
	if err := dlg.Ack(context.Background()); err != nil {
		t.Fatalf("对端 ACK 失败: %v", err)
	}
	if !sink.waitEvent("established:"+callID, 5*time.Second) {
		t.Fatal("本地未进入通话状态")
	}

	// 对端发保持 re-INVITE（a=recvonly）
	req := sip.NewRequest(sip.INVITE, dlg.InviteResponse.Contact().Address)
	req.SetBody([]byte(recvOnlyOffer()))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res, err := dlg.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("对端 re-INVITE 失败: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("本地对 re-INVITE 应答状态应为 200，实际 %d", res.StatusCode)
	}
	// 本地 answer 方向应为镜像 a=sendonly
	if got := media.SDPDirection(res.Body()); got != "sendonly" {
		t.Fatalf("本地 answer 方向应为 sendonly，实际 %q", got)
	}

	p.mu.Lock()
	cs := p.calls[callID]
	p.mu.Unlock()
	if cs == nil || cs.engine == nil {
		t.Fatal("通话会话不存在")
	}

	// 对端保持（recvonly，对端只想收）→ 本端照常发送媒体
	before := txPacketsOf(cs)
	cs.engine.FeedMic(make([]int16, cs.engine.Codec().FrameSize()))
	after := txPacketsOf(cs)
	if after != before+1 {
		t.Fatal("对端保持（recvonly）后本端应继续发送 RTP 包")
	}

	// 对端只发不收（sendonly）→ 本端停止发送（holdTx 生效）
	req2 := sip.NewRequest(sip.INVITE, dlg.InviteResponse.Contact().Address)
	req2.SetBody([]byte(sendOnlyOffer()))
	req2.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res2, err := dlg.Do(context.Background(), req2)
	if err != nil {
		t.Fatalf("对端 sendonly re-INVITE 失败: %v", err)
	}
	if res2.StatusCode != 200 {
		t.Fatalf("sendonly re-INVITE 应答状态应为 200，实际 %d", res2.StatusCode)
	}
	if got := media.SDPDirection(res2.Body()); got != "recvonly" {
		t.Fatalf("对端 sendonly 时本地 answer 方向应为 recvonly，实际 %q", got)
	}
	before = txPacketsOf(cs)
	cs.engine.FeedMic(make([]int16, cs.engine.Codec().FrameSize()))
	after = txPacketsOf(cs)
	if after != before {
		t.Fatal("对端 sendonly 后本端仍发送了 RTP 包（应停止发送）")
	}

	// 对端恢复（a=sendrecv）→ 本端恢复发送
	req3 := sip.NewRequest(sip.INVITE, dlg.InviteResponse.Contact().Address)
	req3.SetBody([]byte(sendRecvOffer()))
	req3.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res3, err := dlg.Do(context.Background(), req3)
	if err != nil {
		t.Fatalf("对端恢复 re-INVITE 失败: %v", err)
	}
	if res3.StatusCode != 200 {
		t.Fatalf("恢复 re-INVITE 应答状态应为 200，实际 %d", res3.StatusCode)
	}
	if got := media.SDPDirection(res3.Body()); got != "sendrecv" {
		t.Fatalf("恢复后本地 answer 方向应为 sendrecv，实际 %q", got)
	}
	before = txPacketsOf(cs)
	cs.engine.FeedMic(make([]int16, cs.engine.Codec().FrameSize()))
	after = txPacketsOf(cs)
	if after != before+1 {
		t.Fatal("对端恢复后本端应恢复发送 RTP 包")
	}

	if err := p.Hangup(callID); err != nil {
		t.Fatalf("Hangup 失败: %v", err)
	}
}

// recvOnlyOffer 对端保持时的 re-INVITE offer（a=recvonly）。
func recvOnlyOffer() string {
	return "v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=test\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"t=0 0\r\n" +
		"m=audio 4006 RTP/AVP 0 8\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"a=recvonly\r\n"
}

// sendOnlyOffer 对端只发不收时的 re-INVITE offer（a=sendonly）。
func sendOnlyOffer() string {
	return "v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=test\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"t=0 0\r\n" +
		"m=audio 4006 RTP/AVP 0 8\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"a=sendonly\r\n"
}

// sendRecvOffer 对端恢复时的 re-INVITE offer（a=sendrecv）。
func sendRecvOffer() string {
	return "v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=test\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"t=0 0\r\n" +
		"m=audio 4006 RTP/AVP 0 8\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=rtpmap:8 PCMA/8000\r\n" +
		"a=sendrecv\r\n"
}

// txPacketsOf 读取引擎发送包统计（txPackets 无独立 getter，用 Stats）。
func txPacketsOf(cs *callSession) uint64 {
	_, _, tx := cs.engine.Stats()
	return tx
}
