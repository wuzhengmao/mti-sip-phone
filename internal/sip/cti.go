package sip

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 反向 CTI 通道（REQ-ACC-026，docs/cti-sip-phone-protocol.md 第二部分）：
// 话机 → CTI 的控制指令与心跳**都走 out-of-dialog SIP NOTIFY**（早期版本的
// in-dialog INFO 已废弃）：
//
//	Event: MSP_ACTION    —— 咨询/转移/三方/监听/会议等控制指令（§8/§9）
//	Event: MSP_HEARTBEAT —— 坐席模式心跳（§10）
//
// 指令结果经**正向** NOTIFY 的 `MSP_RESULT` 回执（按 seq 对齐），2 秒内没收到
// 即视为失败；同一时间只允许一条 MSP_ACTION 在途。心跳没有回执。

const (
	// ctiResultTimeout 等待 MSP_RESULT 的超时时间（协议默认 2s）。
	ctiResultTimeout = 2 * time.Second
	// ctiNotifyTimeout NOTIFY 事务的超时：只等 SIP 层的最终响应（信令送达），
	// 业务结果走 MSP_RESULT，所以短超时足够。
	ctiNotifyTimeout = 5 * time.Second
)

// SendCTICommand 发送一条 CTI 控制指令（Event: MSP_ACTION），返回指令序号
// seq（**字符串**，协议 1.0.18 修订；结果经 OnCTIResult 异步回传）。同一时间
// 只允许一条在途，否则返回 core.ErrCTIBusy。**不要求正在通话**：monitor /
// takeover 等动作在通话外发起。
func (p *Phone) SendCTICommand(action string, params map[string]any) (string, error) {
	p.ctiMu.Lock()
	if p.ctiInFlight {
		p.ctiMu.Unlock()
		return "", core.ErrCTIBusy
	}
	p.ctiInFlight = true
	p.ctiNextSeq++
	seq := strconv.Itoa(p.ctiNextSeq)
	p.ctiInFlightSeq = seq
	p.ctiMu.Unlock()

	// body：seq / extension（本机坐席号）/ action + 动作参数
	body := map[string]any{"seq": seq, "extension": p.ctiExtension(), "action": action}
	for k, v := range params {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		p.deliverCTIResult(core.CTIResult{Seq: seq, Code: "ENCODE", Message: err.Error(), Action: action})
		return seq, nil
	}

	if err := p.sendCTINotify("MSP_ACTION", payload); err != nil {
		logx.Logger().Warn("CTI 指令发送失败", "action", action, "seq", seq, "err", err)
		p.deliverCTIResult(core.CTIResult{Seq: seq, Code: "SEND", Message: err.Error(), Action: action})
		return seq, nil
	}

	// 超时判定：2s 内没收到 MSP_RESULT 即视为失败
	go func() {
		time.Sleep(ctiResultTimeout)
		p.deliverCTIResult(core.CTIResult{Seq: seq, Code: "TIMEOUT", Message: "等待 CTI 结果超时", Action: action})
	}()

	return seq, nil
}

// SendHeartbeat 发送一次 MSP_HEARTBEAT（协议 §10）：上报坐席模式（字符串
// none/normal/dial_out/monitor）、置忙状态、话后整理态（isWork）、离席（isLeave）、
// 显示名与话务状态，expires 告诉 CTI「这么久没收到新心跳就当作不可用」。
// 心跳没有业务回执；**SIP 响应不是 200 时返回 core.ErrCTINotifyUnsupported**
// （服务端不支持 NOTIFY，核心层据此停止心跳并退出坐席模式）。
func (p *Phone) SendHeartbeat(agentMode string, isBusy bool, displayName, status string, isWork, leave bool, expires time.Duration) error {
	p.ctiMu.Lock()
	p.ctiHeartbeatSeq++
	seq := strconv.Itoa(p.ctiHeartbeatSeq)
	p.ctiMu.Unlock()

	if agentMode == "" {
		agentMode = core.AgentModeNone
	}
	body := map[string]any{
		"seq":       seq,
		"extension": p.ctiExtension(),
		"expires":   int(expires.Seconds()),
		"agentMode": agentMode,
		"isBusy":    isBusy,
		"isWork":    isWork,
		"isLeave":   leave,
	}
	if displayName != "" {
		body["displayName"] = displayName
	}
	if status != "" {
		body["status"] = status
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	respStatus, err := p.sendCTINotifyWait("MSP_HEARTBEAT", payload)
	if err != nil {
		return err
	}
	if respStatus != 200 {
		logx.Logger().Warn("MSP_HEARTBEAT 的 SIP 响应不是 200，服务端不支持 NOTIFY",
			"status", respStatus)
		return core.ErrCTINotifyUnsupported
	}
	return nil
}

// deliverCTIResult 投递一条指令结果。超时与 MSP_RESULT 共用一个出口，
// **幂等**：只有第一个到达的（seq 与在途 seq 匹配）生效，之后清空在途标志。
func (p *Phone) deliverCTIResult(res core.CTIResult) {
	p.ctiMu.Lock()
	if !p.ctiInFlight || p.ctiInFlightSeq != res.Seq {
		p.ctiMu.Unlock()
		return
	}
	p.ctiInFlight = false
	p.ctiInFlightSeq = ""
	p.ctiMu.Unlock()
	go p.sink.OnCTIResult(res)
}

// handleCTIResult MSP_RESULT NOTIFY 的入口（body 已解析）。
func (p *Phone) handleCTIResult(seq string, success bool, code, message, action string, data any) {
	p.deliverCTIResult(core.CTIResult{
		Seq: seq, Success: success, Code: code, Message: message,
		Action: action, Data: data,
	})
}

// ctiExtension 当前注册账号（报文的 extension 字段，坐席号）。
func (p *Phone) ctiExtension() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acc.Account
}

// sendCTINotify 发送一条 out-of-dialog NOTIFY（话机 → CTI），**不等响应**：
// 指令的业务结果走 MSP_RESULT（协议 §11），SIP 层只要报文发出去了就够。
func (p *Phone) sendCTINotify(event string, payload []byte) error {
	tx, cancel, err := p.startCTINotify(event, payload)
	if err != nil {
		return err
	}
	cancel()
	tx.Terminate()
	return nil
}

// sendCTINotifyWait 发送 NOTIFY 并**等待最终 SIP 响应**，返回状态码。
// 心跳用：非 200 表示服务端不支持 NOTIFY（协议 1.0.18，核心层据此停心跳、
// 退出坐席模式）。
func (p *Phone) sendCTINotifyWait(event string, payload []byte) (int, error) {
	tx, cancel, err := p.startCTINotify(event, payload)
	if err != nil {
		return 0, err
	}
	defer cancel()
	defer tx.Terminate()
	timer := time.NewTimer(ctiNotifyTimeout)
	defer timer.Stop()
	for {
		select {
		case res := <-tx.Responses():
			if res == nil {
				return 0, errors.New("NOTIFY 未收到 SIP 响应")
			}
			if res.StatusCode >= 200 {
				return res.StatusCode, nil
			}
			// 1xx（100 Trying 等）：继续等最终响应
		case <-timer.C:
			return 0, errors.New("等待 NOTIFY 响应超时")
		}
	}
}

// startCTINotify 组装并发出 NOTIFY，返回事务（调用方负责 Terminate 与 cancel）。
//
// 目的地与 REGISTER 一致：报文发往 Server:Port，配了出站代理走代理；配了域名
// 时域名只用于 URI（报文仍发服务器）。From/To 都是本机 AOR（与协议示例一致），
// From 必须带 tag。
func (p *Phone) startCTINotify(event string, payload []byte) (sip.ClientTransaction, context.CancelFunc, error) {
	// 各服务器对等、无主从（REQ-ACC-038 修订）：out-of-dialog NOTIFY（MSP_HEARTBEAT /
	// MSP_ACTION）轮询选择一个**已注册**服务器发送，让两个域都能收到心跳/指令。
	// 没有任何服务器注册成功时直接失败（不能向未注册的服务器出站）。
	acc, ok := p.roundRobinRegistrar()
	if !ok {
		return nil, func() {}, errors.New("尚未注册到任何 SIP 服务器")
	}

	rhost := uriHostOf(acc)
	recipient := sip.Uri{User: acc.Account, Host: rhost}
	if acc.Domain == "" {
		recipient.Port = acc.Port
	}
	if tlsInUse(acc) {
		recipient.Scheme = "sips"
	}
	req := sip.NewRequest(sip.NOTIFY, recipient)
	// sipgo 不会从 URI 的 sips: 推断传输层，必须显式指定（UDP/TCP/TLS）
	req.SetTransport(strings.ToUpper(config.NormalizeTransport(acc.Transport)))

	from := &sip.FromHeader{
		DisplayName: acc.DisplayName,
		Address:     sip.Uri{User: acc.Account, Host: rhost},
	}
	from.Params = sip.NewParams()
	from.Params.Add("tag", sip.GenerateTagN(16))
	to := &sip.ToHeader{
		DisplayName: acc.DisplayName,
		Address:     sip.Uri{User: acc.Account, Host: rhost},
	}
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(sip.NewHeader("Event", event))
	// RFC 3265：NOTIFY 必须带 Subscription-State（无订阅，terminated）
	req.AppendHeader(sip.NewHeader("Subscription-State", "terminated;reason=noresource"))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/json"))
	req.AppendHeader(sip.NewHeader("User-Agent", UserAgentName()))
	req.SetBody(payload)

	buildReq := func(c *sipgo.Client, r *sip.Request) error {
		if err := sipgo.ClientRequestBuild(c, r); err != nil {
			return err
		}
		// 与 REGISTER 同口径：域名只改 URI，报文仍发到 Server:Port
		if acc.Proxy != "" {
			r.SetDestination(acc.Proxy)
		} else if acc.Domain != "" {
			r.SetDestination(serverDest(acc))
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctiNotifyTimeout)
	tx, err := p.client.TransactionRequest(ctx, req, buildReq)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	return tx, cancel, nil
}

// parseMSPEventName 把 Event 头值解析成 MSP_EVENT 事件名（去前缀、转小写）。
// 返回空串表示不是 MSP_EVENT_* 事件。
func parseMSPEventName(ev string) string {
	u := strings.ToUpper(strings.TrimSpace(strings.SplitN(ev, ";", 2)[0]))
	if !strings.HasPrefix(u, "MSP_EVENT_") {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(u, "MSP_EVENT_"))
}

// flexString 兼容字符串与数字的 JSON 字段：协议里 MSP_RESULT 的 code 写作
// 字符串（`"code":"AGENT_NOT_IN_CALL"`），但 CTI 实际下发的是数字
// （如 `770007`）——按 string 解析会让整个 body 解不出来
// （json: cannot unmarshal number into Go struct field ... of type string），
// 回执被静默丢弃、界面一直等到超时。两种都收。
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexString(n.String())
		return nil
	}
	*f = "" // null / 其它类型：按空处理，别让整条回执被拒
	return nil
}
