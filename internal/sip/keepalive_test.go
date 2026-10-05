package sip

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

// teeListener 把 Accept 到的连接包一层：读到的明文复制一份给 ch（测试用）。
// 包在 tls.Listener 外面，所以复制到的是**解密后**的 SIP 字节流。
type teeListener struct {
	net.Listener
	ch chan []byte
}

func (l *teeListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &teeConn{Conn: c, ch: l.ch}, nil
}

type teeConn struct {
	net.Conn
	ch chan []byte
}

func (c *teeConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		cp := make([]byte, n)
		copy(cp, b[:n])
		select {
		case c.ch <- cp:
		default:
		}
	}
	return n, err
}

// TestConnKeepaliveSendsCRLF 连接型传输（TCP/TLS）的信令保活：在既有连接上发
// CRLF（RFC 5626 §3.5.1），刷新服务端的连接空闲计时——否则连接会在两次注册之间
// 被服务端关掉，它送 in-dialog 请求（对端 BYE/ACK）时只能重新拨号，而 NAT 后面
// 拨不进来（真机：`tcp_connect_blocking_timeout` + `Connection refused`）。
func TestConnKeepaliveSendsCRLF(t *testing.T) {
	// 真实 sipgo 服务端（正常应答 REGISTER，因此不会有重传干扰），
	// 但连接上读到的明文会被 tee 复制出来
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("ka-peer"),
		sipgo.WithUserAgenTLSConfig(&tls.Config{InsecureSkipVerify: true}))
	if err != nil {
		t.Fatalf("UA: %v", err)
	}
	srv, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("Server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(); _ = ua.Close() })
	srv.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Expires", "300"))
		_ = tx.Respond(res)
	})
	raw, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	tee := &teeListener{Listener: tls.NewListener(raw,
		&tls.Config{Certificates: []tls.Certificate{testTLSCert(t)}}), ch: make(chan []byte, 64)}
	go func() { _ = srv.ServeTLS(tee) }()

	host, portStr, _ := net.SplitHostPort(raw.Addr().String())
	port, _ := strconv.Atoi(portStr)
	sink := &testSink{}
	p := newTestPhone(t, sink)
	p.SetTLSDir(t.TempDir())
	acc := config.Account{Server: host, Port: port, Transport: "tls", Account: "1001", Password: "s", Expires: 300}
	if err := p.Register(context.Background(), acc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && sink.lastReg() != core.RegRegistered {
		time.Sleep(20 * time.Millisecond)
	}
	if sink.lastReg() != core.RegRegistered {
		t.Fatalf("注册未成功: %v", sink.regs)
	}

	if err := p.sendConnKeepalive(context.Background(), acc, "tls"); err != nil {
		t.Fatalf("发保活失败: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case b := <-tee.ch:
			if bytes.Contains(b, []byte("REGISTER")) {
				continue // 注册报文，继续等保活
			}
			if bytes.Contains(b, []byte(crlfKeepalive)) {
				return // ✓ 保活发出去了
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("对端没有收到 CRLF 保活")
}

// TestKeepaliveLoopSkipsUDP UDP 没有连接可保活：循环应当立即返回（不占 goroutine）。
func TestKeepaliveLoopSkipsUDP(t *testing.T) {
	p := newTestPhone(t, &testSink{})
	done := make(chan struct{})
	go func() {
		p.keepaliveLoop(context.Background(), config.Account{Transport: "udp", Server: "127.0.0.1", Port: 5060})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("UDP 下保活循环应立即返回")
	}
}
