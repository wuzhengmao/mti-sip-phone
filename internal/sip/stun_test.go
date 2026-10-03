package sip

// STUN 探测与"STUN 就绪后立即重新注册"的回归测试。
// 回归防护：此前 STUN 探测成功只把状态打回"注册中"，registerLoop
// 无重注册信号，界面停留在"注册中"直到下一次续注册。

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/stun"

	"mti-sip-phone/internal/core"
)

// startTestStunServer 启动最小 STUN 服务器：对 BindingRequest 回
// BindingSuccess + XOR-MAPPED-ADDRESS（源地址）。
func startTestStunServer(t *testing.T) string {
	t.Helper()
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("STUN 服务器监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, raddr, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			msg := &stun.Message{Raw: append([]byte{}, buf[:n]...)}
			if err := msg.Decode(); err != nil || msg.Type != stun.BindingRequest {
				continue
			}
			host, portStr, _ := net.SplitHostPort(raddr.String())
			port, _ := strconv.Atoi(portStr)
			res := stun.MustBuild(stun.NewTransactionIDSetter(msg.TransactionID), stun.BindingSuccess)
			_ = (stun.XORMappedAddress{IP: net.ParseIP(host), Port: port}).AddTo(res)
			_, _ = ln.WriteTo(res.Raw, raddr)
		}
	}()
	return ln.LocalAddr().String()
}

// TestStunDiscover STUN 探测：返回源地址映射。
func TestStunDiscover(t *testing.T) {
	addr := startTestStunServer(t)
	ip, err := stunDiscoverCtx(context.Background(), addr)
	if err != nil {
		t.Fatalf("STUN 探测失败: %v", err)
	}
	if ip != "127.0.0.1" {
		t.Fatalf("映射地址应为 127.0.0.1，实际 %q", ip)
	}
}

// startRegCounter 启动仅处理 REGISTER 的计数服务器（401 挑战 + 200）。
func startRegCounter(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("test-reg-counter"))
	if err != nil {
		t.Fatalf("创建计数服务器 UA 失败: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("创建计数服务器失败: %v", err)
	}
	var n atomic.Int32
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		if req.GetHeader("Authorization") == nil {
			res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
			res.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest realm="mti-test", nonce="abc123", algorithm=MD5`))
			_ = tx.Respond(res)
			return
		}
		n.Add(1)
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "3600"))
		_ = tx.Respond(res)
	})
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("计数服务器监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = ua.Close() })
	go func() { _ = srv.ServeUDP(ln) }()
	return ln.LocalAddr().String(), &n
}

// TestStunTriggersReRegister STUN 探测成功后必须立即重新注册：
// 第一次 REGISTER（内网 Contact）→ STUN 就绪 → 第二次 REGISTER
// （公网 Contact），且状态最终回到 registered（不能停留在注册中）。
func TestStunTriggersReRegister(t *testing.T) {
	stunAddr := startTestStunServer(t)
	regAddr, regCount := startRegCounter(t)

	sink := &testSink{}
	p := newTestPhone(t, sink)

	host, portStr, _ := net.SplitHostPort(regAddr)
	port, _ := strconv.Atoi(portStr)
	acc := testAccount(host, port)
	acc.STUNEnabled = true
	acc.STUNServer = stunAddr
	acc.Expires = 3600 // 续注册间隔足够长，第二次 REGISTER 必然由 STUN 触发

	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}

	// 等待 STUN 触发的第二次 REGISTER（200 认证通过计数 +1）
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if regCount.Load() >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if regCount.Load() < 2 {
		t.Fatalf("STUN 就绪后未触发重新注册，认证通过的 REGISTER 次数=%d", regCount.Load())
	}

	// 最终状态必须回到 registered（不能停留在注册中）
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sink.lastReg() == core.RegRegistered {
			// 检查最后一次注册状态是 registered 且在 STUN 重注册之后
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sink.lastReg() != core.RegRegistered {
		t.Fatalf("STUN 重注册后状态应为 registered，实际: %v", sink.regs)
	}
}
