package sip

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/logx"
)

// TLS 传输（REQ-ACC-006）：注册、呼出、呼入的信令走 TLS(SIPS)。
//
// 证书口径（用户选定）：
//   - **服务器证书默认不校验**（配置项 Account.TLSVerify 打开才校验，用系统根证书）
//     ——内网/自建服务器多半自签，开箱即用优先；
//   - 本端 TLS 监听（服务端回连送呼入）用**自动生成的自签名证书**，持久化在实例
//     数据目录里，重启不变（服务端若要固定信任也有东西可加）；
//   - 不做客户端证书（mTLS）。
//
// 只加密信令：媒体仍是明文 RTP（SRTP 是另一个需求 REQ-MEDIA-009）。

const (
	tlsCertFile = "local-cert.pem"
	tlsKeyFile  = "local-key.pem"
	// 自签证书有效期（天）：默认不校验，过期与否不影响使用，取长一点免打扰
	tlsCertValidDays = 3650
)

// tlsVerifyFlag 客户端"是否校验服务器证书"的开关（原子读，可运行中改变）。
//
// 为什么不用 tls.Config.InsecureSkipVerify 直接表达：UA 只在启动时创建一次，
// sipgo 会把传给它的 *tls.Config **指针**捕获进拨号闭包（sip.TransportTLS.init），
// 之后没法换成新配置；而运行中直接改 tls.Config 的字段又与并发握手构成数据竞争。
// 所以固定 InsecureSkipVerify=true（只关掉 Go 自己的校验），真正的校验放进
// VerifyConnection 回调，按这个标记逐次决定——设置页改完立即生效，不必重建 UA。
type tlsVerifyFlag struct{ on atomic.Bool }

// tlsInUse 判断账户是否走 TLS 传输。
func tlsInUse(acc config.Account) bool {
	return config.NormalizeTransport(acc.Transport) == config.TransportTLS
}

// clientTLSConfig 构造出向 TLS 配置，传给 sipgo.WithUserAgenTLSConfig（UA 创建时）。
func clientTLSConfig(flag *tlsVerifyFlag) *tls.Config {
	return &tls.Config{
		// 真正的校验在 VerifyConnection 里（见 tlsVerifyFlag 注释）
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if !flag.on.Load() {
				return nil
			}
			return verifyPeerCert(cs)
		},
	}
}

// verifyPeerCert 按系统根证书校验对端证书链与主机名。
// cs.ServerName 是 sipgo 拨号时按目标主机写入的 SNI/ServerName（见
// sip.TransportTLS.init：config.ServerName 为空时按目标 hostname 填）。
func verifyPeerCert(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("对端未提供证书")
	}
	opts := x509.VerifyOptions{
		DNSName:       cs.ServerName,
		Intermediates: x509.NewCertPool(),
	}
	for _, c := range cs.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	if _, err := cs.PeerCertificates[0].Verify(opts); err != nil {
		return fmt.Errorf("服务器证书校验失败: %w", err)
	}
	return nil
}

// SetTLSDir 设置自签证书目录（main 在启动时调用；缺省用配置目录）。
func (p *Phone) SetTLSDir(dir string) {
	p.tlsMu.Lock()
	p.tlsDir = dir
	p.tlsMu.Unlock()
}

// tlsDirPath 返回证书目录（未显式设置时用配置目录下的 tls/）。
func (p *Phone) tlsDirPath() string {
	p.tlsMu.Lock()
	dir := p.tlsDir
	p.tlsMu.Unlock()
	if dir != "" {
		return dir
	}
	if base, err := config.ConfigDir(); err == nil {
		return filepath.Join(base, "tls")
	}
	return "tls"
}

// ensureTLSListener 启动本端 TLS 监听（幂等）：transport=tls 时由 Register 调用，
// 保证"设置页改成 TLS 后立刻能收到 TLS 呼入"；不用 TLS 的用户不会生成证书、
// 不会多占端口（与 UDP/TCP 监听在启动时无条件打开的做法不同——证书要生成，代价更高）。
//
// 监听端口在 40000~50000 里现找一个 TCP 空闲端口（REQ-ACC-013 修订的口径），
// 与 UDP/TCP 监听端口分开：TLS 也跑在 TCP 上，同一个端口不能既明文又加密。
func (p *Phone) ensureTLSListener() {
	// 占位防并发重复启动：tlsMu 不跨 p.mu 持有（两把锁不嵌套，避免与
	// contactFor 的加锁顺序纠缠）
	p.tlsMu.Lock()
	if p.tlsStarted {
		p.tlsMu.Unlock()
		return
	}
	p.tlsStarted = true
	dir := p.tlsDir
	p.tlsMu.Unlock()

	if dir == "" {
		if base, err := config.ConfigDir(); err == nil {
			dir = filepath.Join(base, "tls")
		} else {
			dir = "tls"
		}
	}
	conf, err := serverTLSConfig(dir)
	if err != nil {
		// 生成不了证书只影响"接收 TLS 呼入"，出向注册/呼叫照常
		logx.Logger().Warn("TLS 监听未启动：自签证书不可用", "dir", dir, "err", err)
		p.tlsMu.Lock()
		p.tlsStarted = false
		p.tlsMu.Unlock()
		return
	}
	// 探测即绑定（BindFreeTCPPort），消除"探测→释放→再绑定"的 TOCTOU 窗口
	//（多实例几乎同时启动时可能撞同端口）；随后包一层 TLS 交给 ServeTLS。
	ln, port, err := BindFreeTCPPort()
	if err != nil {
		logx.Logger().Warn("TLS 监听未启动：无空闲端口", "err", err)
		p.tlsMu.Lock()
		p.tlsStarted = false
		p.tlsMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.tlsMu.Lock()
	p.tlsCancel = cancel
	p.tlsMu.Unlock()
	p.mu.Lock()
	p.localTLSPort = port
	p.mu.Unlock()
	logx.Logger().Info("TLS 信令监听已启动", "port", port, "cert", filepath.Join(dir, tlsCertFile))
	tlsLn := tls.NewListener(ln, conf)
	go func() {
		stop := context.AfterFunc(ctx, func() { _ = tlsLn.Close() })
		defer stop()
		if err := p.server.ServeTLS(tlsLn); err != nil && ctx.Err() == nil {
			logx.Logger().Error("TLS SIP 监听失败", "port", port, "err", err)
		}
	}()
}

// closeTLSListener 停止 TLS 监听（Close 时调用）。
func (p *Phone) closeTLSListener() {
	p.tlsMu.Lock()
	cancel := p.tlsCancel
	p.tlsCancel = nil
	p.tlsStarted = false
	p.tlsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// serverTLSConfig 本端 TLS 监听配置：自签证书（不存在就生成后落盘）。
func serverTLSConfig(dir string) (*tls.Config, error) {
	cert, err := loadOrCreateCert(dir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// loadOrCreateCert 读取 <dir>/local-{cert,key}.pem；缺失或已损坏就重新生成。
func loadOrCreateCert(dir string) (tls.Certificate, error) {
	certPath := filepath.Join(dir, tlsCertFile)
	keyPath := filepath.Join(dir, tlsKeyFile)
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, nil
	}
	certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("创建 TLS 证书目录失败: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, fmt.Errorf("写证书失败: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("写私钥失败: %w", err)
	}
	logx.Logger().Info("已生成 TLS 自签证书", "cert", certPath)
	return tls.X509KeyPair(certPEM, keyPEM)
}

// generateSelfSigned 生成 RSA 2048 自签证书：SAN 覆盖本机各网卡 IP、hostname 与
// localhost——服务端按 IP 回连时，若它开启了对端校验，证书里得有这个 IP 才有东西可对
//（默认不校验时这些只是让证书看起来正常）。RSA 而非 ECDSA：兼容性优先。
func generateSelfSigned() (certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("生成密钥失败: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("生成序列号失败: %w", err)
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "mti-sip-phone"
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   hostname,
			Organization: []string{"mti-sip-phone"},
		},
		NotBefore:             time.Now().Add(-time.Hour), // 容忍对端时钟略慢
		NotAfter:              time.Now().AddDate(0, 0, tlsCertValidDays),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", hostname},
		IPAddresses:           localCertIPs(),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("生成证书失败: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

// localCertIPs 本机网卡地址（去重，含回环），用作自签证书的 IP SAN。
func localCertIPs() []net.IP {
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	seen := map[string]bool{"127.0.0.1": true, "::1": true}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		s := ipNet.IP.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		ips = append(ips, ipNet.IP)
	}
	return ips
}
