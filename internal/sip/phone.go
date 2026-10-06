// Package sip 基于 sipgo 实现 SIP 软电话：注册、呼出、呼入、接听、挂断。
// Phone 实现 core.Telephony 接口，对接 core.App 与 media 引擎。
package sip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"

	"mti-sip-phone/internal/audio"
	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/media"
)

// Phone SIP 软电话实现：持有 UA/Client/Server，管理注册与通话会话。
type Phone struct {
	sink  core.EventSink
	audio *audio.Manager

	ua     *sipgo.UserAgent
	client *sipgo.Client
	server *sipgo.Server

	// dialog 缓存（p.mu 保护）：sipgo 要求每种传输一个实例，传输方式或本机
	// 地址变化时重建（见 ensureDialogCaches），dialogKey 是当前的 Contact 键。
	dcc       *sipgo.DialogClientCache // 呼出 dialog
	dsc       *sipgo.DialogServerCache // 呼入 dialog
	dialogKey string

	mu           sync.Mutex
	acc          config.Account
	localPort    int    // 本地 SIP 监听端口（Contact 用）
	localTLSPort int    // 本端 TLS 监听端口（transport=tls 时 Contact 用；0 = 未启动）
	publicIP     string // STUN 探测的公网 IP（NAT 穿透，Contact/SDP 用）

	// TLS（REQ-ACC-006）：客户端校验开关（Register 时按配置更新，出向握手实时读）
	// 与"本端 TLS 监听"的一次性启动状态。tlsMu 只护监听状态，不跨 p.mu 持有。
	tlsVerify  tlsVerifyFlag
	tlsMu      sync.Mutex
	tlsDir     string // 自签证书目录（main 启动时设置）
	tlsStarted bool
	tlsCancel  context.CancelFunc

	regCancel context.CancelFunc

	// 服务端心跳看护（REQ-ACC-020/021）：从**传输层**收到的 OPTIONS 学习服务
	// 端的探测间隔，超过 2.5 个周期没收到就立即重新注册（服务端在前几次探测
	// 无响应后往往已把本机摘出 location，等 0.7×expires 的续注册太久）。
	// 计数必须在传输层做：服务端的探测复用同一个 Via branch，sipgo 的事务层
	// 会把 Timer J（32 秒）内的相同 branch 当重传、不再回调 handler。
	hbMu       sync.Mutex
	hbLast     time.Time     // 上一条 OPTIONS 的墙钟时刻（零值 = 尚未观测到/已复位）
	hbCount    int           // 学习期内已观测到的间隔样本数（= 心跳条数 - 1）
	hbSum      time.Duration // 样本间隔总和（求平均用）
	hbMinGap   time.Duration // 见过的最小间隔（判定"探测中断后重启"的基准）
	hbInterval time.Duration // 平均间隔（0 = 样本不足 hbMinHeartbeats，尚未武装）
	regPeerIP  string        // 最近一次 REGISTER 成功响应来自的 IP（只统计它发来的探测）

	// 注册看护参数（REQ-CALL-009，测试注入用）：零值取默认值；
	// 必须在 Register 之前设置，之后由看护 goroutine 只读。
	watchTick  time.Duration                                            // 巡检周期
	watchGap   time.Duration                                            // 墙钟跳变阈值（休眠唤醒判定）
	watchNow   func() time.Time                                         // 取当前时刻（墙钟比较用）
	watchProbe func(ctx context.Context, target string) (string, error) // 出向源地址探测

	calls map[string]*callSession

	autoAnswer atomic.Bool // 自动接听开关

	videoMu  sync.RWMutex
	videoCfg config.VideoConfig // 摄像头与视频参数（REQ-VIDEO-002/005）
}

// callSession 一次通话的运行时状态。
type callSession struct {
	id           string
	dir          string // incoming / outgoing
	remote       string
	remoteName   string
	engine       *media.Engine
	dialogC      *sipgo.DialogClientSession // 呼出
	dialogS      *sipgo.DialogServerSession // 呼入
	ctx          context.Context            // 会话上下文：挂断/清理时取消，用于竞态判断
	cancel       context.CancelFunc
	established  bool
	holdMu       sync.Mutex // 保持 re-INVITE 串行化（CSeq 必须严格递增）
	mediaStarted bool       // 媒体引擎已启动（183 早期媒体可能提前启动）

	// ---- 视频 ----
	video      *media.VideoEngine // nil = 本次通话没有视频
	offer      *media.Offer       // 呼入：对端 INVITE 的解析结果（接听时组合 answer 用）
	videoSend  bool               // 本端是否推送本地视频（视频接听/视频呼出且有摄像头）
	videoReady bool               // 视频引擎已 Start（防重复启动，对应 mediaStarted）
	videoMu    sync.Mutex         // 视频引擎创建/启动串行化
}

// UserAgentName 软电话标识（REQ-ACC-014）：格式 "mti-sip-phone v1.0.8"。
func UserAgentName() string {
	return "mti-sip-phone v" + core.Version
}

const (
	// hbMinHeartbeats 心跳间隔学习所需的最少心跳条数（REQ-ACC-021）：至少观测到
	// 这么多条才取平均武装判定——样本太少不足以代表服务端的真实周期。
	hbMinHeartbeats = 5
	// hbDuplicateGap 小于此间隔的心跳视为重传/重复探测（实测服务端偶尔在
	// 0.2~0.3 秒内重发一条），不参与学习、但更新"最近心跳时刻"。
	hbDuplicateGap = time.Second
	// hbOverdueFactor 超时判定的周期倍数：超过「2.5 个心跳周期」没收到探测即
	// 认为服务端已经不再探测（多半已把本机摘掉），立即重新注册。
	hbOverdueFactor = 2.5
)

// userAgentFor 注册报文使用的 User-Agent：启用服务端心跳检测时在标准标识后
// 追加 optchk 标记（REQ-ACC-020），服务端据此定期向本机发 OPTIONS 探测。
// OPTIONS 不需要专门应答——sipgo 默认回 405，实测服务端接受（回 200 反而
// 在 NAT/rport 场景下对不上而被判失败）。
func userAgentFor(acc config.Account) string {
	ua := UserAgentName()
	// optchk 只在 **UDP** 传输时加（用户要求）：服务端的心跳探测（OPTIONS）
	// 在连接型传输（TCP/TLS）上另有保活/连接状态可用，脚本里的心跳判定
	// 也未必覆盖它们，标记只会让服务端多做无用探测。
	if acc.ChkOpt && config.NormalizeTransport(acc.Transport) == config.TransportUDP {
		ua += " optchk"
	}
	return ua
}

// nowClock 取当前时刻：看护相关逻辑共用（测试可注入 watchNow）。
func (p *Phone) nowClock() time.Time {
	if p.watchNow != nil {
		return p.watchNow()
	}
	return time.Now()
}

// noteHeartbeat 记录一条服务端 OPTIONS 探测，并从相邻间隔学习探测周期
// （REQ-ACC-021）：累计 hbMinHeartbeats 条心跳（即 4 个间隔样本）后取**平均**
// 作为间隔；样本不足不武装（免得拿不准的间隔误判）。时钟回拨不学习；间隔突增
//（> 3× 见过的最小间隔）视为探测中断后重启，样本清零重新学习；亚秒级重发
// 视为重复探测，只更新时刻不记样本。
func (p *Phone) noteHeartbeat() {
	now := p.nowClock()
	p.hbMu.Lock()
	defer p.hbMu.Unlock()
	if p.hbLast.IsZero() {
		p.hbLast = now
		return
	}
	gap := time.Duration(now.UnixNano() - p.hbLast.UnixNano())
	switch {
	case gap <= 0:
		return // 时钟回拨：不学习（也不更新时刻）
	case gap < hbDuplicateGap:
		p.hbLast = now // 重传/重复探测：只更新"最近见到心跳"的时刻
		return
	}
	p.hbLast = now
	if p.hbMinGap > 0 && gap > 3*p.hbMinGap {
		// 探测中断后重启：重新学习（本条心跳作为新的第一条）
		p.hbCount, p.hbSum, p.hbMinGap, p.hbInterval = 0, 0, 0, 0
		return
	}
	p.hbCount++
	p.hbSum += gap
	if p.hbMinGap == 0 || gap < p.hbMinGap {
		p.hbMinGap = gap
	}
	if p.hbCount >= hbMinHeartbeats-1 {
		interval := p.hbSum / time.Duration(p.hbCount)
		switch {
		case p.hbInterval == 0:
			logx.Logger().Info("已学习服务端心跳间隔，开始漏检判定",
				"interval", interval.Round(time.Second).String(), "samples", p.hbCount)
		case interval > p.hbInterval+time.Second || interval < p.hbInterval-time.Second:
			logx.Logger().Info("服务端心跳间隔变化",
				"old", p.hbInterval.Round(time.Second).String(),
				"new", interval.Round(time.Second).String(), "samples", p.hbCount)
		}
		p.hbInterval = interval
	}
}

// heartbeatOverdue 是否已超过 hbOverdueFactor 个心跳周期没收到探测
// （REQ-ACC-021）：只在「已观测到心跳且间隔已知」时成立，成立后**复位**
//（等下一次心跳重新武装），避免服务端彻底不再探测时反复触发重新注册。
// 服务器不发 OPTIONS（不支持 optchk）时从未观测到心跳，这里恒为 false。
func (p *Phone) heartbeatOverdue(now time.Time) bool {
	p.hbMu.Lock()
	defer p.hbMu.Unlock()
	if p.hbLast.IsZero() || p.hbInterval <= 0 {
		return false
	}
	limit := time.Duration(float64(p.hbInterval) * hbOverdueFactor)
	if time.Duration(now.UnixNano()-p.hbLast.UnixNano()) <= limit {
		return false
	}
	p.hbLast = time.Time{}
	return true
}

// heartbeatInterval 已学习到的心跳间隔（0 = 未知；仅用于日志）。
func (p *Phone) heartbeatInterval() time.Duration {
	p.hbMu.Lock()
	defer p.hbMu.Unlock()
	return p.hbInterval
}

// resetHeartbeat 复位心跳看护状态：每次 Register（新账户/重新注册）都重新
// 观测、重新学习间隔，避免拿旧的（可能属于另一台服务器的）间隔误判。
func (p *Phone) resetHeartbeat() {
	p.hbMu.Lock()
	defer p.hbMu.Unlock()
	p.hbLast = time.Time{}
	p.hbCount, p.hbSum, p.hbMinGap, p.hbInterval = 0, 0, 0, 0
}

// noteRegisterPeer 记录注册服务器的地址（REGISTER 成功响应的来源，形如
// "10.168.4.40:15060"），只留 IP——心跳只统计它发来的探测。
func (p *Phone) noteRegisterPeer(src string) {
	if src == "" {
		return
	}
	host := src
	if h, _, err := net.SplitHostPort(src); err == nil {
		host = h
	}
	p.mu.Lock()
	p.regPeerIP = strings.Trim(host, "[]")
	p.mu.Unlock()
}

// uriHostOf SIP URI 里用的域名（REQ-ACC-022）：配了域名就用域名，
// 否则用 SIP 服务器地址（历史行为）。**网络目的地不受它影响**——始终是
// Server:Port（域名只让 URI 长得对，见 sendRegister/dccInvite 里的
// SetDestination 与 Route）。
func uriHostOf(acc config.Account) string {
	if d := config.NormalizeDomain(acc.Domain); d != "" {
		return d
	}
	return acc.Server
}

// serverDest 注册/呼叫的实际目的地 "host:port"（域名与服务器地址不同时，
// 报文仍要发到服务器）。
func serverDest(acc config.Account) string {
	return net.JoinHostPort(acc.Server, strconv.Itoa(acc.Port))
}

// hostIsServer host（不含端口）是否就是注册的 SIP 服务器：先比"最近一次
// REGISTER 成功响应来自的 IP"（最可靠，配置写域名也成立），再比配置里的
// server / proxy。SIP 里的域与地址都可能不带端口，所以只比 host。
func (p *Phone) hostIsServer(host string) bool {
	if host == "" {
		return false
	}
	host = strings.Trim(host, "[]")
	p.mu.Lock()
	peer, acc := p.regPeerIP, p.acc
	p.mu.Unlock()
	if peer != "" && strings.EqualFold(host, peer) {
		return true
	}
	for _, s := range []string{acc.Server, acc.Proxy} {
		if s == "" {
			continue
		}
		if h, _, err := net.SplitHostPort(s); err == nil {
			s = h
		}
		if strings.EqualFold(host, strings.Trim(s, "[]")) {
			return true
		}
	}
	return false
}

// readFilter 传输层入口钩子：统计注册服务器发来的 OPTIONS 探测（REQ-ACC-021）。
// **只统计、不改报文、不吞请求**——OPTIONS 由 sipgo 默认回 405（实测服务端接受）。
//
// 为什么必须在传输层数、而不是在 OPTIONS 的 handler 里：服务端的探测复用
// 同一个 Via branch（实测 OpenSIPS），sipgo 的服务端事务会把 Timer J（32 秒）
// 内的相同 branch 请求当**重传**、不再回调 handler——按 handler 计数会把
// 10 秒的真实心跳看成 ~40 秒，学出来的间隔与漏检判定全部失真。
func (p *Phone) readFilter(props sip.TransportReadProps, data []byte) ([]byte, error) {
	// 服务端心跳只对 **UDP** 生效（REQ-ACC-020 修订，用户要求）：连接型传输
	// （TCP/TLS）注册报文不带 optchk、服务端不按心跳探测，所以计数、间隔学习与
	// 超时重注册一并停掉；此前学到的状态由 Register 里的 resetHeartbeat 清掉。
	if bytes.HasPrefix(data, []byte("OPTIONS ")) && p.heartbeatWanted() && p.hostIsServer(hostOfAddr(props.RemoteAddr)) {
		p.noteHeartbeat()
	}
	return data, nil
}

// heartbeatWanted 当前账户是否需要服务端心跳检测：**仅 UDP 传输**且开关打开。
func (p *Phone) heartbeatWanted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acc.ChkOpt && config.NormalizeTransport(p.acc.Transport) == config.TransportUDP
}

// hostOfAddr 取 net.Addr 的 host 部分（不含端口）。
func hostOfAddr(a net.Addr) string {
	s := addrString(a)
	if s == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

// addrString net.Addr → "host:port"（nil 安全）。
func addrString(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}

// NewPhone 创建软电话。sink 用于向核心层上报事件；audioMgr 为 nil 时
// 跳过音频设备对接（用于测试或无音频环境）。
//
// 注意：创建后必须调用 ListenAndServe 监听本地端口（呼入通道与注册
// 响应的接收端），否则只能呼出、无法收到任何呼入。
// 客户端出向 socket 由 sipgo 动态分配（避免 WithClientPort 导致
// 服务器按固定端口回包而无人监听的超时问题）。
func NewPhone(sink core.EventSink, audioMgr *audio.Manager, useRPort bool) (*Phone, error) {
	// Phone 先建（UA 的传输层读钩子要挂在 p 上，见 readFilter/REQ-ACC-021）
	p := &Phone{sink: sink, audio: audioMgr, calls: make(map[string]*callSession)}
	ua, err := sipgo.NewUA(
		sipgo.WithUserAgent(UserAgentName()),
		sipgo.WithUserAgentTransportLayerOptions(sip.WithTransportLayerReadFilter(p.readFilter)),
		// TLS（REQ-ACC-006）：配置在 UA 创建时注入（sipgo 把指针捕获进拨号
		// 闭包，之后换不了），校验开关按原子标记实时读，见 tlsVerifyFlag
		sipgo.WithUserAgenTLSConfig(clientTLSConfig(&p.tlsVerify)),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 UA 失败: %w", err)
	}
	// rport（REQ-ACC-016）：WithClientNAT 使 Via 携带 rport 参数并复用
	// 源端口（NAT 穿透），按配置开关（默认开启）。
	var opts []sipgo.ClientOption
	if useRPort {
		opts = append(opts, sipgo.WithClientNAT())
	}
	client, err := sipgo.NewClient(ua, opts...)
	if err != nil {
		return nil, fmt.Errorf("创建 SIP 客户端失败: %w", err)
	}
	server, err := sipgo.NewServer(ua)
	if err != nil {
		return nil, fmt.Errorf("创建 SIP 服务端失败: %w", err)
	}

	p.ua = ua
	p.client = client
	p.server = server
	p.setupHandlers()
	logx.Logger().Info("SIP 软电话已创建", "hostname", client.Hostname())
	return p, nil
}

// ListenAndServe 监听本地 SIP 端口（呼入通道）。addr 如 "0.0.0.0:5060"，
// network 为 udp / tcp。监听端口被记录用于 Contact 头构造。
// 应分别对 UDP 与 TCP 调用（各起一个 goroutine）。
func (p *Phone) ListenAndServe(ctx context.Context, network, addr string) error {
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		if port, err := strconv.Atoi(portStr); err == nil {
			p.mu.Lock()
			if p.localPort == 0 {
				p.localPort = port
				logx.Logger().Info("本地 SIP 监听端口", "network", network, "port", port)
			}
			p.mu.Unlock()
		}
	}
	return p.server.ListenAndServe(ctx, network, addr)
}

// contactFor 构造当前账户传输方式对应的 Contact 地址：
//
//	udp / tcp → <sip:账号@本机IP:UDP/TCP监听端口>
//	tls       → <sips:账号@本机IP:TLS监听端口;transport=tls>
//
// 走 TLS 时必须是 sips: 且带 transport=tls 参数（用 sip: 会让对端按明文回连）。
func (p *Phone) contactFor(acc config.Account) sip.Uri {
	host := p.localHost() // 自己加锁（内部有 UDP 探测），不能与下面的取锁合并
	p.mu.Lock()
	port, tlsPort := p.localPort, p.localTLSPort
	p.mu.Unlock()

	u := sip.Uri{User: acc.Account, Host: host}
	if tlsInUse(acc) {
		u.Scheme = "sips"
		if tlsPort > 0 {
			u.Port = tlsPort
		}
		u.UriParams = sip.NewParams()
		u.UriParams.Add("transport", config.TransportTLS)
		return u
	}
	if port > 0 {
		u.Port = port
	}
	// 连接型传输（TCP）：Contact 要显式写 `transport=tcp`——对端/服务端按 Contact
	// 的 URI 决定用哪种传输回来，而 `sip:` URI 不带 transport 参数时**默认是 UDP**
	//（NAT/TLS 那一串问题里踩过的同一个坑）。UDP 不加（缺省即 UDP）。
	if config.NormalizeTransport(acc.Transport) == config.TransportTCP {
		u.UriParams = sip.NewParams()
		u.UriParams.Add("transport", config.TransportTCP)
	}
	return u
}

// setupHandlers 注册呼入处理。
func (p *Phone) setupHandlers() {
	// 呼入 INVITE：必须在 handler 内同步处理整个来电生命周期——
	// sipgo 的服务端事务在 handler 返回后即被终止（若用 go 启动
	// goroutine，事务会立即 "transaction terminated"）。handler 本身
	// 运行在事务独立 goroutine 中，阻塞等待接听是安全的。
	p.server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// in-dialog re-INVITE（To 带 tag）与初始 INVITE 分流：
		// re-INVITE 用于通话中保持/恢复的媒体方向变更
		if req.To() != nil && req.To().Params != nil {
			if _, ok := req.To().Params.Get("tag"); ok {
				p.handleReInvite(req, tx)
				return
			}
		}
		p.handleIncoming(req, tx)
	})
	// 呼入 CANCEL（对端取消振铃中的呼入）
	p.server.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		go p.handleIncomingCancel(req, tx)
	})
	// 对端 BYE
	p.server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		go p.handleRemoteBye(req, tx)
	})
	// 对端 ACK：必须匹配 dialog 并通知（sipgo 的 RespondSDP 发送 200 后
	// 按 RFC 6026 阻塞等待 ACK；无此处理则 2xx 重传至超时且接听流程卡死）
	p.server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		dsc := p.dialogServer()
		if dsc == nil {
			logx.Logger().Warn("ACK 到达但 dialog 缓存未初始化")
			return
		}
		if err := dsc.ReadAck(req, tx); err != nil {
			logx.Logger().Warn("ACK 匹配失败", "err", err)
		}
	})
}

// ---------------------------------------------------------------------------
// core.Telephony 实现
// ---------------------------------------------------------------------------

// Register 发起注册（阻塞直到第一次注册结果确定，后续续注册在后台进行）。
func (p *Phone) Register(ctx context.Context, acc config.Account) error {
	p.mu.Lock()
	p.acc = acc
	// STUN 未启用时清空旧公网地址：否则用户关掉 STUN 重新注册后，
	// Contact 仍会带上一次探测到的公网地址（localHost 无条件优先 publicIP）。
	if !acc.STUNEnabled || acc.STUNServer == "" {
		p.publicIP = ""
	}
	p.mu.Unlock()

	// 声明 IP（REQ-ACC-023）：保存账户即生效——Via 的 sent-by 在这里改写，
	// Contact/SDP 走 localHost()（读 p.acc）自动跟上。
	p.applyDeclaredIP(acc)

	// TLS（REQ-ACC-006）：校验开关即时生效（出向握手每次按它判定）；
	// transport=tls 时确保本端 TLS 监听已启动（首次注册时才生成证书/占端口，
	// 不用 TLS 的用户零开销），运行中从 udp/tcp 改成 tls 也走这里。
	p.tlsVerify.on.Store(acc.TLSVerify)
	if tlsInUse(acc) {
		p.ensureTLSListener()
	}

	if p.regCancel != nil {
		p.regCancel()
	}
	p.resetHeartbeat() // 重新等第一条服务端心跳（REQ-ACC-021）
	regCtx, cancel := context.WithCancel(ctx)
	p.regCancel = cancel

	// 立即重新注册信号（REQ-CALL-009）：注册看护（系统休眠唤醒/网络变化，
	// 见 netwatch.go）与初始 STUN 探测都经这里通知注册循环。
	// 约定：buffer=1、非阻塞投递、任何一方都不 close——已有待处理信号时
	// 合并为一次，看护永不阻塞。
	kickCh := make(chan string, 1)

	go p.watchRegistration(regCtx, acc, kickCh)
	go p.registerLoop(regCtx, acc, kickCh)
	// 连接型传输（TCP/TLS）的信令保活：防止服务端的连接空闲超时把连接关掉，
	// 之后它送 in-dialog 请求（对端 BYE/ACK）就只能重新拨号、而 NAT 后面拨不进来
	//（真机：tcp_connect_blocking_timeout + Connection refused）。
	go p.keepaliveLoop(regCtx, acc)
	return nil
}

// Unregister 注销。
func (p *Phone) Unregister(ctx context.Context) error {
	if p.regCancel != nil {
		p.regCancel()
		p.regCancel = nil
	}
	// 发送 expires=0 的 REGISTER 通知服务端
	if _, err := p.sendRegister(ctx, p.acc, 0); err != nil {
		logx.Logger().Debug("注销 REGISTER 发送失败", "err", err)
	}
	p.sink.OnRegisterState(core.RegUnregistered, "")
	return nil
}

// srtpPolicyOf 账户配置 → 媒体加密策略（REQ-MEDIA-009）。
// 空值/非法值按默认「支持」处理（旧配置没有该字段）。
func srtpPolicyOf(acc config.Account) media.SRTPPolicy {
	switch config.NormalizeSRTP(acc.SRTP) {
	case config.SRTPOff:
		return media.SRTPOff
	case config.SRTPPreferred:
		return media.SRTPPreferred
	case config.SRTPRequired:
		return media.SRTPRequired
	default:
		return media.SRTPSupported
	}
}

// Dial 呼出（纯音频）。
func (p *Phone) Dial(ctx context.Context, target string) (string, error) {
	return p.dial(ctx, target, false)
}

// DialVideo 发起视频呼叫：offer 同时含 m=audio 与 m=video。
// 摄像头被禁用时视频段方向为 recvonly（只收不发，REQ-VIDEO-005）。
func (p *Phone) DialVideo(ctx context.Context, target string) (string, error) {
	return p.dial(ctx, target, true)
}

func (p *Phone) dial(ctx context.Context, target string, withVideo bool) (string, error) {
	p.mu.Lock()
	acc := p.acc
	p.mu.Unlock()
	if acc.Server == "" || acc.Account == "" {
		return "", fmt.Errorf("未配置 SIP 账户")
	}

	// 视频呼叫但视频功能不可用（本构建没有视频 / 总开关关闭，REQ-VIDEO-014）：
	// 直接失败——不建媒体引擎、不发 INVITE。
	if withVideo && !p.videoUsable() {
		return "", p.videoUnavailableErr()
	}

	// 目标处理：号码 → sip:号码@服务器；sip: URI 原样使用
	recipient, err := parseTarget(target, acc)
	if err != nil {
		return "", err
	}

	// dialog 缓存必须在本通登记进 p.calls **之前**按当前传输方式准备好：
	// `ensureDialogCaches` 有"有通话进行中就不换缓存"的保护，放到登记之后
	// 会把本通自己当成"进行中的通话"，改了传输方式却继续用旧 Contact 发 INVITE。
	p.ensureDialogCaches(acc)

	// 创建媒体引擎与 SDP offer
	engine, err := media.NewEngine(p.localIP())
	if err != nil {
		return "", err
	}
	// 媒体加密策略（REQ-MEDIA-009）必须在**生成 offer 之前**设置：
	// 「建议/必要」会在 offer 里带上 a=crypto 行
	engine.SetSRTPPolicy(srtpPolicyOf(acc))
	// SDP 宣告地址使用对端可达地址（NAT：注册回显地址 / STUN 公网地址）
	engine.SetSDPIP(p.localHost())

	callID := uuid.NewString()
	callCtx, cancel := context.WithCancel(ctx)
	cs := &callSession{
		id:     callID,
		dir:    "outgoing",
		remote: recipient.User,
		engine: engine,
		ctx:    callCtx,
		cancel: cancel,
	}

	// 视频：仅当视频功能可用（构建支持 + 总开关启用，前面已校验）且本次是
	// 视频呼叫时创建视频引擎——cs.video 为 nil 时 offer 里没有 m=video。
	if withVideo {
		v, err := media.NewVideoEngine(p.localIP())
		if err != nil {
			cancel()
			engine.Close()
			return "", err
		}
		v.SetSDPIP(p.localHost())
		v.SetFrameSink(p.videoSinkFor(callID))
		v.SetSendEnabled(p.cameraUsable())
		v.SetSRTPPolicy(srtpPolicyOf(acc)) // 视频是独立 m= 行，独立密钥
		cs.video = v
		cs.videoSend = p.cameraUsable()
	}

	p.mu.Lock()
	p.calls[callID] = cs
	p.mu.Unlock()

	offer := media.SDPOfferFor(engine, cs.video, "sendrecv", p.videoOfferDirection(cs.video))
	dlg, err := p.dccInvite(recipient, offer, acc)
	if err != nil {
		cancel()
		p.mu.Lock()
		delete(p.calls, callID)
		p.mu.Unlock()
		if cs.video != nil {
			cs.video.Close()
		}
		engine.Close()
		return "", err
	}
	cs.dialogC = dlg

	// 视频呼叫：**一拨出就打开摄像头与本地回显**（用户要求），不等对端 answer。
	// 对端 answer 里没有视频时，applyVideoAnswer 会关掉引擎与摄像头，
	// 并上报全零视频状态让界面收掉视频窗口。
	if cs.video != nil {
		p.startVideo(cs, cs.videoSend)
	}

	go p.outgoingFlow(callCtx, cs, acc)
	return callID, nil
}

// videoOfferDirection 本端 offer 里视频段的方向：
// 能推本地视频 → sendrecv；只收不发 → recvonly（摄像头禁用/不可用）。
func (p *Phone) videoOfferDirection(v *media.VideoEngine) string {
	if v == nil {
		return ""
	}
	if p.cameraUsable() {
		return "sendrecv"
	}
	return "recvonly"
}

// Answer 接听呼入（普通接听）。
// 需求 4（用户明确要求）：点普通「接听」= **不接受视频**，answer 里 m=video
// 端口置 0；要接通视频请用 AnswerVideo。
//
// 实现见 phone_video.go 的 answerIncoming（后台发送 200 OK 的原因见那里）。
func (p *Phone) Answer(callID string) error {
	return p.answerIncoming(callID, answerNoVideo)
}

// attachAudio 接通后把媒体引擎对接音频设备（audio 为 nil 时跳过）。
func (p *Phone) attachAudio(engine *media.Engine) {
	if p.audio != nil {
		p.audio.AttachEngine(engine)
	}
}

// Hangup 挂断：本地状态与界面立即结束（不等待对端 200/ACK），
// 信令（BYE / 603 / CANCEL）在后台 goroutine 发送。
func (p *Phone) Hangup(callID string) error {
	cs := p.getCall(callID)
	if cs == nil {
		return core.ErrCallNotFound
	}
	// 信令后台发送（BYE/603 的响应等待不能阻塞调用方 UI 线程）。
	// 信令分支按 SIP dialog 实际状态判断，不用 cs.established——
	// 200 OK 发出后到本地标志置位之间存在竞态窗口（如呼入接听中、
	// 呼出 2xx 已收未 ACK），期间挂断若误判为"振铃中"会漏发 BYE
	// 或误发 603，导致对端不挂断（bug 修复）。
	go func() {
		if cs.dialogS != nil && cs.dialogS.LoadState() < sip.DialogStateEstablished {
			// 呼入振铃中挂断 = 拒接：回 603 通知对端。
			// 603 必须先于上下文取消发出——handleIncoming 一旦退出，
			// sipgo 对尚无最终响应的 INVITE 事务 TerminateGracefully
			// 会立即 Terminate，此后 603 必然发送失败（bug 修复）。
			if err := cs.dialogS.Respond(603, "Decline", nil); err != nil {
				logx.Logger().Debug("发送拒接响应失败", "err", err)
			}
			logx.Logger().Info("振铃中拒接来电（603）", "callId", callID)
			if cs.cancel != nil {
				cs.cancel() // 603 已发出，再结束 handleIncoming 等待
			}
			_ = cs.dialogS.Close()
			return
		}
		if cs.cancel != nil {
			// 呼出振铃中：sipgo 的 WaitAnswer 收到取消后自动发 CANCEL；
			// 其余场景取消会话上下文用于竞态判断。
			//
			// CANCEL 必须按 **INVITE 的路径**发（RFC 3261 §9.1：目的地址、Call-ID、
			// CSeq、Via branch 都与 INVITE 一致），不能带对话的路由集。但 sipgo 构造
			// CANCEL 时走的是 dialog 的 buildReq，会把**响应里的 Record-Route** 当
			// Route 塞进去，于是 CANCEL 被发到 Record-Route 指向的代理地址——真机上
			// 那条腿连不上，CANCEL 静默丢失（对端一直振铃）。这里在取消前把**临时
			// 响应**上的 Record-Route 摘掉，让 CANCEL 回到 INVITE 的原始目的地；
			// 最终响应（2xx）的 Record-Route 不能动——它要留给补发的 ACK/BYE。
			if cs.dialogC != nil {
				if res := cs.dialogC.InviteResponse; res != nil && res.StatusCode < 200 {
					stripRecordRoutes(res)
				}
			}
			cs.cancel()
		}
		switch {
		case cs.dialogC != nil:
			// 呼出：已接通发 BYE；振铃中的取消由 outgoingFlow 统一处理
			//（WaitAnswer 收到 ctx 取消自动发 CANCEL；2xx 后补 ACK+BYE）
			if cs.established {
				if err := cs.dialogC.Bye(context.Background()); err != nil {
					logx.Logger().Warn("发送 BYE 失败", "err", err)
				}
				_ = cs.dialogC.Close()
			}
		case cs.dialogS != nil:
			// 200 OK 已发出（含接听流程进行中）：发送 BYE 结束通话
			if err := cs.dialogS.Bye(context.Background()); err != nil {
				logx.Logger().Warn("发送 BYE 失败", "err", err)
			}
			_ = cs.dialogS.Close()
		}
	}()
	// 本地状态与界面立即清理：不取消上下文、不关闭 dialog——
	// 取消/关闭的时机由上面的信令 goroutine 掌控（避免 603 竞态）。
	p.endCallImmediate(callID, "local_hangup")
	return nil
}

// Reject 拒接呼入，code 为 SIP 状态码。
func (p *Phone) Reject(callID string, code int) error {
	cs := p.getCall(callID)
	if cs == nil || cs.dialogS == nil {
		return core.ErrCallNotFound
	}
	// 拒接响应必须先于上下文取消发出（同 Hangup 的 603 竞态修复：
	// handleIncoming 退出后 sipgo 会 TerminateGracefully 终止事务，
	// 之后再 Respond 必然失败）
	go func() {
		if cs.dialogS.LoadState() >= sip.DialogStateEstablished {
			// 200 OK 已发出（接听进行中）：拒接已无意义，改发 BYE 挂断
			if err := cs.dialogS.Bye(context.Background()); err != nil {
				logx.Logger().Warn("发送 BYE 失败", "err", err)
			}
		} else if err := cs.dialogS.Respond(code, statusText(code), nil); err != nil {
			logx.Logger().Debug("发送拒接响应失败", "err", err)
		}
		if cs.cancel != nil {
			cs.cancel()
		}
		_ = cs.dialogS.Close()
	}()
	p.endCallImmediate(callID, "rejected")
	return nil
}

// Mute 保持切换：本地立即停止/恢复发送音频，并按 SIP 规范发送
// re-INVITE 通知对端——保持时媒体方向 a=sendonly，取消保持恢复
// a=sendrecv（用户要求；信令在后台 goroutine 发送）。
func (p *Phone) Mute(callID string, muted bool) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	if !cs.established {
		return fmt.Errorf("通话未接通，无法保持")
	}
	cs.engine.SetMuted(muted)
	// 保持期间同时停发本地视频（远端画面保留）。
	// **不重协商视频 m= 行**：视频段方向保持原样，避免与既有 re-INVITE 的
	// ACK/CSeq 逻辑纠缠（保持本身只改音频方向）。
	if cs.video != nil {
		cs.video.SetRemoteHold(muted)
	}
	go p.sendHoldReInvite(cs, muted)
	return nil
}

// MuteMic 麦克风静音切换：纯本地停止/恢复发送（不发 SIP 信令，
// 区别于 Mute 的保持 re-INVITE——对端听到静音但通话保持正常状态）。
func (p *Phone) MuteMic(callID string, muted bool) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	cs.engine.SetMicMuted(muted)
	return nil
}

// MuteSpeaker 扬声器静音切换：纯本地停止/恢复播放（继续接收 RTP）。
func (p *Phone) MuteSpeaker(callID string, muted bool) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	cs.engine.SetPlayMuted(muted)
	return nil
}

// SetMicGain 麦克风增益（REQ-MEDIA-006）：纯本地，不发 SIP 信令。
func (p *Phone) SetMicGain(callID string, gain float32) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	cs.engine.SetMicGain(gain)
	return nil
}

// SetSpeakerVolume 扬声器音量（REQ-MEDIA-006）：纯本地，不发 SIP 信令。
func (p *Phone) SetSpeakerVolume(callID string, vol float32) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	cs.engine.SetSpeakerVolume(vol)
	return nil
}

// sendHoldReInvite 发送保持/取消保持的 re-INVITE：
// INVITE（新 SDP offer，方向 sendonly/sendrecv）→ 2xx → 应用 answer → ACK。
func (p *Phone) sendHoldReInvite(cs *callSession, muted bool) {
	cs.holdMu.Lock()
	defer cs.holdMu.Unlock()
	// 期间通话可能已结束
	if p.getCall(cs.id) == nil {
		return
	}

	dir := "sendrecv"
	if muted {
		dir = "sendonly"
	}
	// 视频段方向跟随音频：保持时也置 sendonly，恢复时按本端能否推送决定。
	videoDir := ""
	if cs.video != nil {
		if muted || !cs.videoSend || !p.cameraUsable() {
			videoDir = "sendonly"
		} else {
			videoDir = "sendrecv"
		}
	}
	offer := media.SDPOfferFor(cs.engine, cs.video, dir, videoDir)

	// 远程目标：dialog 的远端地址（呼出取 2xx 的 Contact，呼入取 INVITE 的 Contact）
	var (
		target sip.Uri
		tx     sip.ClientTransaction
		req    *sip.Request
		err    error
	)
	switch {
	case cs.dialogC != nil:
		if cs.dialogC.InviteResponse != nil && cs.dialogC.InviteResponse.Contact() != nil {
			target = cs.dialogC.InviteResponse.Contact().Address
		} else if cs.dialogC.InviteResponse != nil {
			// 兜底：200 未带 Contact（非规范对端）时退回原 R-URI
			target = cs.dialogC.InviteRequest.Recipient
		} else {
			logx.Logger().Warn("保持 re-INVITE 失败：缺少远端地址", "callId", cs.id)
			return
		}
		req = buildReInvite(target, offer)
		tx, err = cs.dialogC.TransactionRequest(context.Background(), req)
	case cs.dialogS != nil:
		if cs.dialogS.InviteRequest.Contact() == nil {
			logx.Logger().Warn("保持 re-INVITE 失败：缺少远端地址", "callId", cs.id)
			return
		}
		target = cs.dialogS.InviteRequest.Contact().Address
		req = buildReInvite(target, offer)
		tx, err = cs.dialogS.TransactionRequest(context.Background(), req)
	default:
		return
	}
	if err != nil {
		logx.Logger().Warn("保持 re-INVITE 失败", "callId", cs.id, "err", err)
		return
	}
	// 事务延迟 5 秒收尾：期间对端重传的 200 会触发 OnRetransmission
	// 补发 ACK（RFC 3261 13.2.2.4），过早 Terminate 会让重传 200 成为
	// Unhandled，对端 dialog 卡在等待 ACK 而拒绝后续 re-INVITE
	go func() {
		time.Sleep(5 * time.Second)
		tx.Terminate()
	}()

	// 等待最终响应（跳过 1xx）
	var res *sip.Response
	deadline := time.After(30 * time.Second)
waitLoop:
	for {
		select {
		case r, ok := <-tx.Responses():
			if !ok {
				logx.Logger().Warn("保持 re-INVITE 事务结束", "callId", cs.id)
				return
			}
			if r.IsProvisional() {
				continue
			}
			res = r
			break waitLoop
		case <-tx.Done():
			logx.Logger().Warn("保持 re-INVITE 事务异常结束", "callId", cs.id, "err", tx.Err())
			return
		case <-deadline:
			logx.Logger().Warn("保持 re-INVITE 响应超时", "callId", cs.id)
			return
		}
	}
	if !res.IsSuccess() {
		logx.Logger().Warn("保持 re-INVITE 被拒绝", "callId", cs.id, "code", res.StatusCode)
		return
	}
	if body := res.Body(); len(body) > 0 {
		if err := cs.engine.SDPApplyAnswer(body); err != nil {
			logx.Logger().Debug("re-INVITE answer 解析失败", "callId", cs.id, "err", err)
		}
	}
	// 发送 ACK。req 已由 buildReq 补齐 dialog 头（From/To/Call-ID/CSeq
	// 均为 re-INVITE 自身的值），ACK 必须复用这些头——此前按初始
	// INVITE 的头构造，呼入场景 From 变成了对端，对端 dialog 校验
	// 失败丢弃 ACK，导致取消保持的 re-INVITE 被 491 拒绝（bug 修复）。
	sendReInviteAck(cs, req, res)
	// 对端重传 200 时补发 ACK（RFC 3261 13.2.2.4），避免对端 dialog
	// 卡在等待 ACK 状态而拒绝后续 re-INVITE
	tx.OnRetransmission(func(r *sip.Response) {
		if r.IsSuccess() {
			sendReInviteAck(cs, req, r)
		}
	})
	logx.Logger().Info("保持 re-INVITE 完成", "callId", cs.id, "direction", dir)
}

// buildReInvite 构造 in-dialog re-INVITE 请求（dialog 头由 sipgo 的
// buildReq 在 TransactionRequest 时补齐并递增 CSeq）。
func buildReInvite(target sip.Uri, offer string) *sip.Request {
	req := sip.NewRequest(sip.INVITE, target)
	req.SetBody([]byte(offer))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	return req
}

// sendReInviteAck 对 re-INVITE 的 2xx 发送 ACK（RFC 3261：in-dialog
// INVITE 的 2xx 必须 ACK；sipgo 的 Ack() 仅适用于初始 INVITE）。
// req 为 re-INVITE 请求（TransactionRequest 已补齐 dialog 头），
// ACK 的 From/Call-ID/CSeq 必须与其一致，To 取 2xx 的 To。
func sendReInviteAck(cs *callSession, req *sip.Request, res *sip.Response) {
	var (
		target sip.Uri
		sender func(*sip.Request) error
	)
	switch {
	case cs.dialogC != nil:
		target = cs.dialogC.InviteResponse.Contact().Address
		sender = cs.dialogC.WriteRequest
	case cs.dialogS != nil:
		target = cs.dialogS.InviteRequest.Contact().Address
		sender = cs.dialogS.WriteRequest
	default:
		return
	}
	ack := sip.NewRequest(sip.ACK, target)
	if f := req.From(); f != nil {
		ack.AppendHeader(sip.HeaderClone(f))
	}
	if t := res.To(); t != nil {
		ack.AppendHeader(sip.HeaderClone(t))
	}
	if c := req.CallID(); c != nil {
		ack.AppendHeader(sip.HeaderClone(c))
	}
	if c := req.CSeq(); c != nil {
		cs := sip.HeaderClone(c)
		cs.(*sip.CSeqHeader).MethodName = sip.ACK
		ack.AppendHeader(cs)
	}
	if err := sender(ack); err != nil {
		logx.Logger().Debug("re-INVITE ACK 发送失败", "callId", cs.id, "err", err)
	}
}

// SendDTMF 通话中发送 DTMF 音（RFC 2833）。
func (p *Phone) SendDTMF(callID, digit string) error {
	cs := p.getCall(callID)
	if cs == nil || cs.engine == nil {
		return core.ErrCallNotFound
	}
	cs.engine.SendDTMF(digit)
	return nil
}

// SetAutoAnswer 开关自动接听：开启后来电自动接听（REQ-CALL 新增）。
func (p *Phone) SetAutoAnswer(enabled bool) error {
	p.autoAnswer.Store(enabled)
	logx.Logger().Info("自动接听设置", "enabled", enabled)
	return nil
}

// SetRPort 动态切换 rport（REQ-ACC-016，运行中立即生效，无需重启）。
func (p *Phone) SetRPort(enabled bool) error {
	setClientRPort(p.client, enabled)
	return nil
}

// SetMicrophone 选择麦克风。
// 切换在音频后台队列中生效（设备重建可能耗时，不能阻塞调用方），
// 失败经日志体现。
func (p *Phone) SetMicrophone(deviceID string) error {
	if p.audio == nil {
		return fmt.Errorf("音频管理器不可用")
	}
	p.audio.SetMic(deviceID)
	return nil
}

// SetSpeaker 选择扬声器。
func (p *Phone) SetSpeaker(deviceID string) error {
	if p.audio == nil {
		return fmt.Errorf("音频管理器不可用")
	}
	p.audio.SetSpeaker(deviceID)
	return nil
}

// AudioDevices 返回音频设备列表。
func (p *Phone) AudioDevices() ([]core.AudioDevice, []core.AudioDevice) {
	if p.audio == nil {
		return nil, nil
	}
	return p.audio.Devices()
}

// Close 释放全部资源。
func (p *Phone) Close() error {
	if p.regCancel != nil {
		p.regCancel()
	}
	p.mu.Lock()
	calls := make([]*callSession, 0, len(p.calls))
	for _, cs := range p.calls {
		calls = append(calls, cs)
	}
	p.calls = make(map[string]*callSession)
	p.mu.Unlock()
	for _, cs := range calls {
		p.closeVideo(cs) // 视频引擎也必须回收（停采集、关端口）
		cs.engine.Close()
		if cs.cancel != nil {
			cs.cancel()
		}
	}
	p.closeTLSListener() // TLS 监听协程随进程收尾（REQ-ACC-006）
	_ = p.client.Close()
	_ = p.ua.Close()
	return nil
}

// ---------------------------------------------------------------------------
// 注册流程
// ---------------------------------------------------------------------------

// registerLoop 注册生命周期：注册 → 失败退避重试 → 成功后续注册。
// kickCh：立即重新注册信号（REQ-CALL-009）——注册看护检测到系统休眠
// 唤醒、网络变化/恢复时投递，STUN 公网地址就绪也走这条路径（此前仅把
// 状态打回"注册中"却没有触发重注册，界面会一直停留在"注册中"直到
// 下次续注册——bug 修复）。收到信号立即重试/重注册，**不重置退避**：
// 连续失败时退避照常翻倍，防止网络抖动击穿退避。
func (p *Phone) registerLoop(ctx context.Context, acc config.Account, kickCh <-chan string) {
	backoff := time.Second
	expires := acc.Expires // 注册到期时间（REQ-ACC-018），服务端返回值优先

	for {
		if ctx.Err() != nil {
			return
		}
		gotExpires, err := p.doRegister(ctx, acc)
		if err != nil {
			p.sink.OnRegisterState(core.RegFailed, err.Error())
			logx.Logger().Warn("注册失败，将重试", "err", err, "backoff", backoff.String())
			select {
			case <-time.After(backoff):
			case reason := <-kickCh:
				// 网络恢复/休眠唤醒等：跳过退避立即重试
				logx.Logger().Info("收到重新注册信号，跳过退避立即重试", "reason", reason)
			case <-ctx.Done():
				return
			}
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		expires = gotExpires
		p.sink.OnRegisterState(core.RegRegistered, "")
		// 日志携带本次注册的 Contact 地址，便于确认 STUN 公网地址是否
		// 已生效（Contact host 为公网 IP 即 STUN 生效）
		contact := p.contactFor(acc)
		logx.Logger().Info("注册成功", "expires", expires, "contact", contact.String())

		// 续注册：到期前 30% 时重新注册（按 RFC 3261 建议）；
		// 收到看护信号（休眠唤醒/网络变化/STUN 就绪）时立即重新注册
		renew := time.Duration(float64(expires)*0.7) * time.Second
		select {
		case <-time.After(renew):
			// 继续循环重新注册
		case reason := <-kickCh:
			logx.Logger().Info("收到重新注册信号，立即重新注册", "reason", reason)
		case <-ctx.Done():
			return
		}
	}
}

// doRegister 发送一次 REGISTER（含 Digest 认证），成功返回 expires。
func (p *Phone) doRegister(ctx context.Context, acc config.Account) (int, error) {
	return p.sendRegister(ctx, acc, acc.Expires)
}

// sendRegister 发送 REGISTER，expires=0 表示注销。成功返回服务端 expires。
func (p *Phone) sendRegister(ctx context.Context, acc config.Account, expires int) (int, error) {
	p.sink.OnRegisterState(core.RegRegistering, "")

	// 域名（REQ-ACC-022）：配了域名就用它做 URI 里的域（AOR 归属），
	// 端口只在用服务器地址时才写（域名不带端口，报文照样发到 Server:Port）。
	rhost := uriHostOf(acc)
	// 请求行携带用户名（REQ-ACC-012）：OpenSIPS 等服务器的 registrar 从
	// Request-URI 提取 location 表的 username，缺省会导致 username 为空。
	recipient := sip.Uri{User: acc.Account, Host: rhost}
	if acc.Domain == "" {
		recipient.Port = acc.Port
	}
	// TLS：R-URI 声明 sips（RFC 3261 §10.2，registrar 域用 sips: 表示只接受 TLS）
	if tlsInUse(acc) {
		recipient.Scheme = "sips"
	}
	req := sip.NewRequest(sip.REGISTER, recipient)
	// sipgo 不会从 URI 的 sips: 推断传输层，必须显式指定（UDP/TCP/TLS）
	req.SetTransport(strings.ToUpper(config.NormalizeTransport(acc.Transport)))

	authUser := acc.AuthUser
	if authUser == "" {
		authUser = acc.Account
	}
	// From / To（必须是独立的两个头对象，复用同一指针会导致 From 重复输出）
	from := &sip.FromHeader{
		DisplayName: acc.DisplayName,
		Address:     sip.Uri{User: acc.Account, Host: rhost},
	}
	to := &sip.ToHeader{
		DisplayName: acc.DisplayName,
		Address:     sip.Uri{User: acc.Account, Host: rhost},
	}
	req.AppendHeader(from)
	req.AppendHeader(to)
	// Contact：<sip:账号@本机IP:监听端口>;expires=N
	//（transport=tls 时是 <sips:账号@本机IP:TLS端口;transport=tls>，见 contactFor）
	contact := &sip.ContactHeader{
		Address: p.contactFor(acc),
		Params:  sip.NewParams(),
	}
	contact.Params.Add("expires", strconv.Itoa(expires))
	req.AppendHeader(contact)
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))
	req.AppendHeader(sip.NewHeader("Allow", "INVITE, ACK, CANCEL, BYE, OPTIONS, UPDATE, NOTIFY, INFO, REFER"))
	req.AppendHeader(sip.NewHeader("User-Agent", userAgentFor(acc)))

	// 自定义 build：sipgo 默认的 ClientRequestRegisterBuild 按 RFC 3261 清空
	// R-URI 的用户部分，但 OpenSIPS 的 registrar 从 R-URI 提取 location 表的
	// username（REQ-ACC-012），这里在默认构建之后恢复用户名。
	buildReq := func(c *sipgo.Client, r *sip.Request) error {
		if err := sipgo.ClientRequestRegisterBuild(c, r); err != nil {
			return err
		}
		r.Recipient.User = acc.Account
		// 出站代理（REQ-ACC-019）：R-URI 保持目标，网络层发往代理
		if acc.Proxy != "" {
			r.SetDestination(acc.Proxy)
		} else if acc.Domain != "" {
			// 配了域名：R-URI 是 sip:账号@域名，报文仍发到 Server:Port
			//（域名多半只能在服务器内部解析，本机不一定能解析它）
			r.SetDestination(serverDest(acc))
		}
		return nil
	}
	tx, err := p.client.TransactionRequest(ctx, req, buildReq)
	if err != nil {
		return 0, fmt.Errorf("发送 REGISTER 失败: %w", err)
	}
	defer tx.Terminate()

	// 处理响应（最多 2 次认证重试）
	auth := sipgo.DigestAuth{Username: authUser, Password: acc.Password}
	for attempt := 0; attempt < 3; attempt++ {
		res, err := p.readResponse(ctx, tx, "REGISTER")
		if err != nil {
			return 0, err
		}
		switch res.StatusCode {
		case 200:
			p.noteRegisterPeer(res.Source()) // 记注册服务器地址（心跳只统计它发来的探测）
			// 解析服务端 expires
			exp := expires
			if eh := res.GetHeader("Expires"); eh != nil {
				if v, err := strconv.Atoi(eh.Value()); err == nil && v > 0 {
					exp = v
				}
			}
			if ch := res.Contact(); ch != nil && ch.Params != nil {
				if v, ok := ch.Params.Get("expires"); ok {
					if vi, err := strconv.Atoi(v); err == nil && vi > 0 {
						exp = vi
					}
				}
			}
			return exp, nil
		case 401, 407:
			if attempt >= 2 {
				return 0, fmt.Errorf("认证失败：连续 3 次未通过")
			}
			res2, err := p.client.DoDigestAuth(ctx, req, res, auth)
			if err != nil {
				return 0, fmt.Errorf("认证请求失败: %w", err)
			}
			if res2.StatusCode == 200 {
				p.noteRegisterPeer(res2.Source())
				return expires, nil
			}
			return 0, fmt.Errorf("注册被拒绝: %d %s", res2.StatusCode, res2.Reason)
		default:
			return 0, fmt.Errorf("注册被拒绝: %d %s", res.StatusCode, res.Reason)
		}
	}
	return 0, fmt.Errorf("注册未完成")
}

// readResponse 读取事务的下一个最终响应（跳过 1xx）。
// 同时监听 ctx 与事务结束，避免退出注销时等待满 30 秒。
func (p *Phone) readResponse(ctx context.Context, tx sip.ClientTransaction, label string) (*sip.Response, error) {
	for {
		select {
		case res, ok := <-tx.Responses():
			if !ok {
				return nil, fmt.Errorf("%s 事务已结束", label)
			}
			if res.IsProvisional() {
				continue
			}
			return res, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tx.Done():
			return nil, fmt.Errorf("%s 事务异常结束: %v", label, tx.Err())
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("%s 响应超时", label)
		}
	}
}

// ---------------------------------------------------------------------------
// 呼出流程
// ---------------------------------------------------------------------------

// ensureDialogCaches 按账户当前的传输方式准备 dialog 缓存（dcc/dsc）。
//
// sipgo 要求"每种传输一个实例"（缓存里存着 Contact 头与进行中的 dialog）：
// 传输方式或本机地址变了就得换一套，否则新呼叫会带着旧传输的 Contact。
//
// **有通话进行中时不换**：缓存里放着那通电话的 dialog，换掉会让 in-dialog
// 请求（ACK / BYE 匹配）找不到会话；等这一通结束后的下一次注册/呼叫再换。
func (p *Phone) ensureDialogCaches(acc config.Account) {
	contact := &sip.ContactHeader{Address: p.contactFor(acc)}
	key := contact.Address.String()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dialogKey == key && p.dcc != nil && p.dsc != nil {
		return
	}
	if p.dialogKey != "" && len(p.calls) > 0 {
		// 通话进行中：沿用旧缓存（调用方都在本通登记进 p.calls 之前调用，
		// 所以这里的"有通话"指另一通）
		return
	}
	p.dcc = sipgo.NewDialogClientCache(p.client, *contact)
	p.dsc = sipgo.NewDialogServerCache(p.client, *contact)
	p.dialogKey = key
}

// dialogClient / dialogServer 取当前 dialog 缓存（可能为 nil，调用方判空）。
func (p *Phone) dialogClient() *sipgo.DialogClientCache {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dcc
}

func (p *Phone) dialogServer() *sipgo.DialogServerCache {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dsc
}

// dccInvite 创建 dialog client cache（按账户传输方式）并发起 INVITE。
func (p *Phone) dccInvite(recipient sip.Uri, offer string, acc config.Account) (*sipgo.DialogClientSession, error) {
	p.ensureDialogCaches(acc)
	// 出向请求走哪个传输由 R-URI 的 transport 参数决定（sipgo 的 dialog 层
	// 会把它写成 req.SetTransport，见 sipgo/dialog_ua.go）——tcp / tls 都必须显式写，
	// 否则会退回默认的 UDP。TLS 还要把 URI 换成 sips:。
	tr := config.NormalizeTransport(acc.Transport)
	if tr == config.TransportTCP || tr == config.TransportTLS {
		recipient.UriParams = sip.NewParams()
		recipient.UriParams.Add("transport", tr)
	}
	if tr == config.TransportTLS && !strings.EqualFold(recipient.Scheme, "sips") {
		recipient.Scheme = "sips"
	}
	// From / To：sipgo dialog 层不自动生成，必须显式添加（缺失会导致
	// OpenSIPS 报 "bad from header"）。From 按 RFC 3261 携带 tag。
	from := &sip.FromHeader{
		DisplayName: acc.DisplayName,
		Address:     sip.Uri{User: acc.Account, Host: uriHostOf(acc)},
		Params:      sip.NewParams(),
	}
	from.Params.Add("tag", sip.GenerateTagN(8))
	to := &sip.ToHeader{
		Address: sip.Uri{User: recipient.User, Host: recipient.Host, Port: recipient.Port},
	}
	headers := []sip.Header{
		sip.NewHeader("Content-Type", "application/sdp"),
		sip.NewHeader("User-Agent", UserAgentName()),
		from, to,
	}
	// 出站代理（REQ-ACC-019）：首条 Route 指向代理（loose routing）；
	// TLS 下代理地址也用 sips:（RFC 3261 §8.1.2：Route 与 R-URI 的保密级别一致）。
	// 域名（REQ-ACC-022）同一个手法：R-URI/To 写域名，但报文要发到 Server:Port
	// （域名多半只在服务器内部解析，本机不一定解析得了）。代理配置优先。
	routeTo := ""
	if acc.Proxy != "" {
		routeTo = acc.Proxy
	} else if acc.Domain != "" {
		routeTo = serverDest(acc)
	}
	if routeTo != "" {
		scheme := "sip"
		if tr == config.TransportTLS {
			scheme = "sips"
		}
		headers = append(headers, sip.NewHeader("Route", "<"+scheme+":"+routeTo+";lr>"))
	}
	dcc := p.dialogClient()
	if dcc == nil {
		return nil, fmt.Errorf("dialog 缓存未初始化")
	}
	dlg, err := dcc.Invite(context.Background(), recipient, []byte(offer), headers...)
	if err != nil {
		return nil, fmt.Errorf("发送 INVITE 失败: %w", err)
	}
	return dlg, nil
}

// outgoingFlow 呼出事务流程：振铃 →（认证重试）→ 接通 / 失败 / 取消。
func (p *Phone) outgoingFlow(ctx context.Context, cs *callSession, acc config.Account) {
	defer func() {
		if !cs.established {
			p.cleanupCall(cs.id, "failed")
		}
	}()

	authUser := acc.AuthUser
	if authUser == "" {
		authUser = acc.Account
	}
	err := cs.dialogC.WaitAnswer(ctx, sipgo.AnswerOptions{
		Username: authUser,
		Password: acc.Password,
		OnResponse: func(res *sip.Response) error {
			// 180 / 183 振铃
			if res.StatusCode == 180 || res.StatusCode == 183 {
				// 183 携带 SDP 即早期媒体（服务器下发的回铃音）：
				// 解析对端地址、启动媒体接收与播放（本端保持静音，
				// 早期媒体阶段不发送本端音频）
				earlyMedia := false
				if res.StatusCode == 183 && len(res.Body()) > 0 && !cs.mediaStarted {
					if err := cs.engine.SDPApplyAnswer(res.Body()); err != nil {
						logx.Logger().Debug("早期媒体 SDP 解析失败", "callId", cs.id, "err", err)
					} else {
						cs.mediaStarted = true
						earlyMedia = true
						cs.engine.SetMuted(true)     // 早期媒体阶段静音本端（不发送真实麦克风数据）
						cs.engine.SetKeepalive(true) // 持续发送静音保活 NAT 映射
						p.attachAudio(cs.engine)
						cs.engine.Start()
						// 视频：183 里带 m=video 就**提前协商并启动**——对端（实测
						// MicroSIP/FreeSWITCH 一侧）从 183 起就在发，而且本端越早在
						// 这个流上发东西，对端越早把发送目标锁到我们的真实地址：
						// 实测"对端画面在接通后 8 秒才来"→ 早期媒体阶段就开始发后
						// 缩短到 1 秒级。早期阶段不因缺视频段而回收引擎（等 2xx 定案）。
						if cs.video != nil {
							if early, perr := media.ParseSDP(res.Body()); perr == nil {
								if vinfo, verr := early.Media("video"); verr == nil && !vinfo.Rejected {
									if cs.video.ApplyAnswer(vinfo) {
										p.startVideo(cs, cs.videoSend)
									}
								}
							}
						}
						logx.Logger().Info("早期媒体已建立（回铃音）", "callId", cs.id)
					}
				}
				// earlyMedia 一并上报：界面据此决定要不要放本地回铃音
				p.sink.OnRinging(cs.id, earlyMedia)
			}
			return nil
		},
	})

	if err != nil {
		if errors.Is(err, context.Canceled) {
			// 本地取消（挂断）。CANCEL 与对端 200 存在竞态（RFC 3261 §9.1）：
			// 若 CANCEL 期间对端已回 2xx，需补 ACK + BYE 结束会话，
			// 否则对端会一直重传 200 并保持通话（bug 修复）。
			if res := cs.dialogC.InviteResponse; res != nil && res.IsSuccess() {
				logx.Logger().Info("取消后收到对端 2xx，补发 ACK + BYE", "callId", cs.id)
				_ = cs.dialogC.Ack(context.Background())
				if err := cs.dialogC.Bye(context.Background()); err != nil {
					logx.Logger().Warn("发送 BYE 失败", "err", err)
				}
				_ = cs.dialogC.Close()
			}
			return
		}
		// 非 2xx：忙/拒绝/无应答，或底层错误（如取消时 CANCEL 发不出去）。
		// 只有**最终响应**（>=200）才拿它当失败原因——InviteResponse 里可能只是
		// 早先收到的 183（曾把"CANCEL 发送失败"显示成"183 Session Progress"，
		// 排查时完全误导）。
		reason := err.Error()
		if res := cs.dialogC.InviteResponse; res != nil && res.StatusCode >= 200 {
			reason = fmt.Sprintf("%d %s", res.StatusCode, res.Reason)
		}
		logx.Logger().Warn("呼叫失败", "callId", cs.id, "reason", reason, "err", err)
		p.sink.OnFailed(cs.id, reason)
		return
	}

	// 2xx：解析 SDP answer，建立媒体
	if res := cs.dialogC.InviteResponse; res != nil {
		if body := res.Body(); len(body) > 0 {
			if err := cs.engine.SDPApplyAnswer(body); err != nil {
				p.sink.OnFailed(cs.id, fmt.Sprintf("SDP 协商失败: %v", err))
				_ = cs.dialogC.Bye(context.Background())
				return
			}
			// 视频：对端可能只接受音频（answer 里 m=video 端口为 0）
			p.applyVideoAnswer(cs, body)
		} else if cs.video != nil {
			// 对端 2xx 没带 SDP（异常对端）：视频无从协商，按"没有视频"处理，
			// 关掉摄像头并让界面收掉视频窗口（视频呼叫拨出即开摄像头，
			// 不收回来就会一直空转）。
			logx.Logger().Info("对端 2xx 未带 SDP，视频按未协商处理", "callId", cs.id)
			p.closeVideo(cs)
			p.notifyVideoState(cs)
		}
	}

	// 本地可能在 2xx 到达后、接通完成前挂断（ctx 已取消）：此时
	// WaitAnswer 已返回不会再发 CANCEL，必须补 ACK + BYE 通知对端，
	// 否则对端一直保持通话（竞态 bug 修复）。
	if ctx.Err() != nil {
		logx.Logger().Info("2xx 后检测到本地挂断，补发 ACK + BYE", "callId", cs.id)
		_ = cs.dialogC.Ack(context.Background())
		if err := cs.dialogC.Bye(context.Background()); err != nil {
			logx.Logger().Warn("发送 BYE 失败", "err", err)
		}
		_ = cs.dialogC.Close()
		return
	}

	// 发送 ACK
	if err := cs.dialogC.Ack(context.Background()); err != nil {
		logx.Logger().Warn("发送 ACK 失败", "err", err)
	}
	cs.established = true
	// 早期媒体（183）阶段引擎可能已启动接收：复用并恢复正常发送
	if !cs.mediaStarted {
		p.attachAudio(cs.engine)
		cs.engine.Start()
		cs.mediaStarted = true
	}
	cs.engine.SetKeepalive(false) // 关闭保活，恢复真实音频发送
	cs.engine.SetMuted(false)
	p.startVideo(cs, cs.videoSend)
	p.logMediaEncryption(cs)
	p.sink.OnEstablished(cs.id)
}

// logMediaEncryption 接通时记一条媒体加密结果（REQ-MEDIA-009）：
// 排查"这通电话到底加没加密、用的哪个套件"时一眼可见（协商期另有 srtp.go 的日志）。
func (p *Phone) logMediaEncryption(cs *callSession) {
	if cs.engine.SRTPActive() {
		logx.Logger().Info("本次通话媒体已加密（SRTP）", "callId", cs.id, "套件", cs.engine.SRTPSuite())
		return
	}
	p.mu.Lock()
	policy := config.NormalizeSRTP(p.acc.SRTP)
	p.mu.Unlock()
	logx.Logger().Info("本次通话媒体为明文 RTP（未加密）", "callId", cs.id, "媒体加密策略", policy)
}

// applyVideoAnswer 处理对端 2xx 里的视频段：协商成功则保留引擎（启动在
// 接通流程里做），被拒绝（m=video 端口为 0 或无该段）则关闭视频引擎，
// 本次通话退化为纯音频。
func (p *Phone) applyVideoAnswer(cs *callSession, body []byte) {
	if cs.video == nil {
		return
	}
	offer, err := media.ParseSDP(body)
	if err != nil {
		logx.Logger().Debug("视频 answer 解析失败，按纯音频处理", "err", err)
		p.closeVideo(cs)
		return
	}
	info, err := offer.Media("video")
	if err != nil || info.Rejected {
		logx.Logger().Info("对端未接受视频，本次通话为纯音频（关闭摄像头与视频窗口）", "callId", cs.id)
		p.closeVideo(cs)
		p.notifyVideoState(cs)
		return
	}
	if !cs.video.ApplyAnswer(info) {
		logx.Logger().Warn("视频协商失败，本次通话为纯音频（关闭摄像头与视频窗口）", "callId", cs.id)
		p.closeVideo(cs)
		p.notifyVideoState(cs)
	}
}

// ---------------------------------------------------------------------------
// 呼入流程
// ---------------------------------------------------------------------------

// handleIncoming 处理呼入 INVITE。
// 单通话限制：呼叫/振铃/通话中时新来电直接回 486（USER_BUSY）。
func (p *Phone) handleIncoming(req *sip.Request, tx sip.ServerTransaction) {
	p.mu.Lock()
	busy := len(p.calls) > 0
	acc := p.acc
	p.mu.Unlock()
	if busy {
		logx.Logger().Info("已有通话进行中，拒绝来电（486 User Busy）", "callId", req.CallID().Value())
		res := sip.NewResponseFromRequest(req, 486, "Busy Here", nil)
		_ = tx.Respond(res)
		return
	}

	// dialog 缓存按当前传输方式的 Contact 准备好——必须在本通登记进 p.calls
	// **之前**（理由同 dial：缓存重建有"通话进行中不换"的保护）
	p.ensureDialogCaches(acc)

	// 解析主叫信息
	from := req.From()
	remote := from.Address.User
	remoteName := from.DisplayName

	callID := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	cs := &callSession{
		id:         callID,
		dir:        "incoming",
		remote:     remote,
		remoteName: remoteName,
		ctx:        ctx,
		cancel:     cancel,
	}
	p.mu.Lock()
	p.calls[callID] = cs
	p.mu.Unlock()

	// 用 dialog server 处理 INVITE（缓存在上面按当前传输方式准备好了）
	dsc := p.dialogServer()
	if dsc == nil {
		logx.Logger().Error("处理呼入 INVITE 失败：dialog 缓存未初始化")
		p.cleanupCall(callID, "failed")
		return
	}
	dlg, err := dsc.ReadInvite(req, tx)
	if err != nil {
		logx.Logger().Error("处理呼入 INVITE 失败", "err", err)
		p.cleanupCall(callID, "failed")
		return
	}
	cs.dialogS = dlg

	// 解析 offer 中的远端 RTP 信息，准备媒体引擎（SDPAnswer 在接听时确定）
	engine, err := media.NewEngine(p.localIP())
	if err != nil {
		logx.Logger().Error("创建媒体引擎失败", "err", err)
		_ = dlg.Respond(500, statusText(500), nil)
		p.cleanupCall(callID, "failed")
		return
	}
	// SDP 宣告地址使用对端可达地址（NAT：注册回显地址 / STUN 公网地址）
	engine.SetSDPIP(p.localHost())
	// 媒体加密策略必须在解析对端 offer **之前**设置（REQ-MEDIA-009）：
	// 对端 offer 带 crypto 而策略允许时要回带 crypto 的 answer
	engine.SetSRTPPolicy(srtpPolicyOf(acc))
	cs.engine = engine
	if body := req.Body(); len(body) > 0 {
		if _, err := engine.SDPAnswer(body); err != nil {
			if errors.Is(err, media.ErrSRTPRequired) {
				// 本机要求媒体必须加密，而对端 offer 没给可接受的 crypto 参数：
				// 直接 488 拒接（不能静默降级成明文，那正是"必要"要避免的）
				logx.Logger().Warn("对端未提供 SRTP 参数而本机要求媒体加密，拒接来电（488）",
					"callId", callID, "err", err)
				engine.Close()
				_ = dlg.Respond(488, statusText(488), nil)
				p.cleanupCall(callID, "failed")
				return
			}
			logx.Logger().Debug("呼入 SDP 解析失败", "err", err)
		}
	}

	// 视频（REQ-VIDEO-004）：解析对端 offer 的视频段并准备好视频引擎，
	// 但**先不启动**——接听时才根据用户点的按钮决定视频段方向。
	// 视频功能不可用（本构建没有 / 总开关关闭，REQ-VIDEO-014）时整个跳过：
	// cs.offer 保持 nil，offer 里的视频段就当我们不认识，answer 自然只回
	// m=audio（与"没启用视频的构建"逐字节一致），来电也不会显示「视频接听」。
	hasVideo := false
	if body := req.Body(); len(body) > 0 && p.videoUsable() {
		if offer, err := media.ParseSDP(body); err == nil {
			cs.offer = offer
			if info, mErr := offer.Media("video"); mErr == nil && !info.Rejected {
				if v, vErr := media.NewVideoEngine(p.localIP()); vErr == nil {
					v.SetSDPIP(p.localHost())
					v.SetSRTPPolicy(srtpPolicyOf(acc)) // 视频独立 m= 行，独立密钥
					if v.ApplyOffer(info) {
						cs.video = v
						hasVideo = true
					} else {
						v.Close()
					}
				} else {
					logx.Logger().Warn("创建视频引擎失败，按纯音频接听", "err", vErr)
				}
			}
		} else {
			logx.Logger().Debug("呼入 SDP 二次解析失败", "err", err)
		}
	}

	// 上报来电事件（核心层会弹窗/推送 WS 事件）
	p.sink.OnIncoming(callID, remote, remoteName, hasVideo)

	// 自动接听决策：
	//   1) INVITE 携带 Call-Info: <...>;answer-after=x（REQ-CALL-015）：
	//      x=0 立即接听，x>0 延迟 x 秒接听（期间先回 180）；
	//   2) 本地自动接听开关开启：立即接听；
	//   3) 否则回 180 等待用户操作。
	answerAfter := parseAnswerAfter(req)
	if answerAfter > 0 {
		// 延迟自动接听：先振铃，x 秒后自动接听
		p.respondRinging(dlg)
		logx.Logger().Info("延迟自动接听", "callId", callID, "delay", answerAfter)
		go func() {
			select {
			case <-time.After(time.Duration(answerAfter) * time.Second):
				logx.Logger().Info("延迟时间到，执行自动接听", "callId", callID)
				if !cs.established {
					// 自动接听（SIP 头 answer-after / 延迟接听）：视频呼叫按**视频接听**
					// 处理（用户要求）——answerSendRecv 打开摄像头双向视频；
					// 摄像头被禁用/不可用时 videoAnswerDirection 自动退化成 recvonly。
					// 这里曾传 false（= 拒绝视频），表现为自动接听只接通音频。
					_ = p.answerIncoming(cs.id, answerSendRecv)
				}
			case <-ctx.Done():
			}
		}()
	} else if answerAfter == 0 || p.autoAnswer.Load() {
		// 立即自动接听（自动接听开关）：视频呼叫按**视频接听**处理（用户要求）
		logx.Logger().Info("自动接听来电", "callId", callID, "answerAfter", answerAfter)
		_ = p.answerIncoming(cs.id, answerSendRecv)
	} else {
		p.respondRinging(dlg)
	}

	// 等待：接听（Answer 中 RespondSDP）/ 拒接（Reject）/
	// 对端取消（dialog 状态结束，sipgo 事务层处理 CANCEL 后通知）。
	// 注意：以 dialog 实际状态判断，不能用 cs.established——RespondSDP
	// 内部先 setState(Established) 触发 StateRead 通知，而 cs.established
	// 在返回后才置位，误判会把正常接听当成对端取消清理掉（bug 修复）。
	select {
	case <-ctx.Done():
	case <-dlg.StateRead():
		// 振铃阶段 dialog 终止（对端 CANCEL / 事务超时）时状态为
		// DialogStateEnded(3)。注意不能用 state < Established 判断——
		// 枚举中 Ended=3 大于 Established=1，该判断对 Ended 恒为 false，
		// 导致 CANCEL 后通话从未被清理（bug 修复）。
		if state := dlg.LoadState(); state == sip.DialogStateEnded {
			logx.Logger().Info("对端取消呼叫（CANCEL）", "callId", callID, "state", state)
			p.cleanupCall(callID, "remote_cancel")
			return
		}
		// 其它状态（Established 等）：本通还在进行，**handler 绝不能现在返回**。
		// sipgo 的 TerminateGracefully 对**可靠传输（TCP/TLS）**会在 handler 返回时
		// 立即 Terminate 事务（UDP 不可靠，会保留到重传定时器），而接听流程此刻正在
		// RespondSDP 里按 RFC 6026 阻塞等 ACK——事务一终止就报 "transaction
		// terminated"，我们把刚接通的通话当失败清掉。真机表现：TLS 来电接听后
		// 约 0.5 秒自动挂断（UDP 一直正常，就是靠"事务被保留"侥幸躲过）。
		// 等本通真正结束（挂断/对端 BYE/清理都会取消 cs.ctx）。
		<-ctx.Done()
	}
}

// respondRinging 回 180 振铃。
func (p *Phone) respondRinging(dlg *sipgo.DialogServerSession) {
	if err := dlg.Respond(sip.StatusRinging, "Ringing", nil); err != nil {
		logx.Logger().Debug("发送 180 失败", "err", err)
	}
}

// parseAnswerAfter 解析 INVITE 的 Call-Info 头中的 answer-after 参数。
// 返回 -1 表示未指定。
// 头格式：Call-Info: <sip:xxx>;answer-after=0
func parseAnswerAfter(req *sip.Request) int {
	for _, h := range req.GetHeaders("Call-Info") {
		v := h.Value()
		// 在 <...> 之后的参数中查找 answer-after=N
		if idx := strings.Index(v, "answer-after="); idx >= 0 {
			rest := v[idx+len("answer-after="):]
			num := ""
			for _, c := range rest {
				if c >= '0' && c <= '9' {
					num += string(c)
				} else {
					break
				}
			}
			if num != "" {
				if n, err := strconv.Atoi(num); err == nil && n >= 0 {
					return n
				}
			}
		}
	}
	return -1
}

// handleReInvite 处理通话中的 in-dialog re-INVITE（对端保持/恢复）：
// 解析对端 offer 的媒体方向（对端保持时为 a=recvonly），按镜像方向
// 回 200 + SDP answer；对端只发不收（sendonly）或完全停发（inactive）
// 时本端停止发送媒体。
func (p *Phone) handleReInvite(req *sip.Request, tx sip.ServerTransaction) {
	cs := p.matchByCallID(req.CallID().Value())
	if cs == nil || cs.engine == nil || !cs.established {
		logx.Logger().Warn("re-INVITE 无匹配通话", "callId", req.CallID().Value())
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
		return
	}

	// 方向必须**按媒体类型分别取**：对端可能只改视频段的方向，
	// 用「第一段」的旧写法会把视频方向误当成音频方向（多 m= 行引入的坑）。
	remoteDir := media.SDPDirectionFor(req.Body(), "audio")
	// 视频段方向单独镜像；对端 re-INVITE 里 m=video 端口为 0 表示拒收视频。
	videoDir := ""
	if cs.video != nil {
		if vinfo, vErr := media.ParseMedia(req.Body(), "video"); vErr == nil && !vinfo.Rejected {
			cs.video.SetRemoteHold(vinfo.Dir == "sendonly" || vinfo.Dir == "inactive")
			if vinfo.Dir != "inactive" {
				videoDir = media.MirrorDirection(vinfo.Dir)
			}
		}
	}
	answer, err := cs.engine.SDPAnswerWithDirection(req.Body(), media.MirrorDirection(remoteDir))
	if err != nil {
		// 对端在重协商里去掉了 SRTP 而本机要求加密（REQ-MEDIA-009）：
		// 用 488 明确拒绝，不能用 500（那是"本端出错"，这里是协商不接受）
		if errors.Is(err, media.ErrSRTPRequired) {
			logx.Logger().Warn("对端 re-INVITE 去掉了 SRTP 而本机要求媒体加密，拒绝（488）",
				"callId", cs.id, "err", err)
			_ = tx.Respond(sip.NewResponseFromRequest(req, 488, statusText(488), nil))
			return
		}
		logx.Logger().Warn("re-INVITE SDP 协商失败", "callId", cs.id, "err", err)
		_ = tx.Respond(sip.NewResponseFromRequest(req, 500, "Server Internal Error", nil))
		return
	}
	// 含视频时按 offer 的媒体顺序重组 answer
	if videoDir != "" && cs.video != nil {
		if offer, oErr := media.ParseSDP(req.Body()); oErr == nil {
			answer = media.SDPAnswerFor(cs.engine, cs.video, offer, cs.engine.PayloadType(), videoDir)
		}
	}

	// 回 200 在后台执行：2xx 的重传与 ACK 等待不能阻塞事务回调
	go func() {
		res := sip.NewSDPResponseFromRequest(req, []byte(answer))
		if err := tx.Respond(res); err != nil {
			logx.Logger().Debug("re-INVITE 200 发送失败", "callId", cs.id, "err", err)
			return
		}
		// 吸收 ACK（避免事务层 ACK missed 告警）
		select {
		case <-tx.Acks():
		case <-time.After(10 * time.Second):
		}
	}()

	// 对端只发不收（sendonly）或完全停发（inactive）→ 本端停止发送；
	// 对端保持（recvonly，只想收）→ 本端照常发送
	cs.engine.SetRemoteHold(remoteDir == "sendonly" || remoteDir == "inactive")
	logx.Logger().Info("对端 re-INVITE 已处理", "callId", cs.id, "remoteDir", remoteDir)
}

// handleIncomingCancel 对端取消振铃中的呼入。
func (p *Phone) handleIncomingCancel(req *sip.Request, tx sip.ServerTransaction) {
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	_ = tx.Respond(res)
	// 找到对应通话并终止（按 Call-ID 匹配）
	target := p.matchByCallID(req.CallID().Value())
	if target != nil && !target.established {
		p.cleanupCall(target.id, "remote_cancel")
	}
}

// handleRemoteBye 对端挂断。
func (p *Phone) handleRemoteBye(req *sip.Request, tx sip.ServerTransaction) {
	if tx != nil {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		_ = tx.Respond(res)
	}

	target := p.matchByCallID(req.CallID().Value())
	if target != nil {
		p.cleanupCall(target.id, "remote_hangup")
	}
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

// getCall 取通话会话。
func (p *Phone) getCall(callID string) *callSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[callID]
}

// matchByCallID 按 SIP Call-ID 匹配会话：呼入用 dialogS、呼出用 dialogC
// （此前仅匹配 dialogS，呼出通话的对端 BYE 被忽略——bug 修复）。
func (p *Phone) matchByCallID(callID string) *callSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, cs := range p.calls {
		var csCallID string
		if cs.dialogS != nil && cs.dialogS.InviteRequest != nil {
			csCallID = cs.dialogS.InviteRequest.CallID().Value()
		} else if cs.dialogC != nil && cs.dialogC.InviteRequest != nil {
			csCallID = cs.dialogC.InviteRequest.CallID().Value()
		}
		if csCallID != "" && csCallID == callID {
			return cs
		}
	}
	return nil
}

// endCallImmediate 立即结束通话的本地状态：从会话表移除、释放媒体、
// 上报核心层（界面即时恢复）。不取消会话上下文、不关闭 dialog——
// 这两个动作涉及信令时序（如 603 必须先于上下文取消发出），由
// 调用方（cleanupCall / Hangup 的信令 goroutine）负责。
func (p *Phone) endCallImmediate(callID, reason string) *callSession {
	p.mu.Lock()
	cs := p.calls[callID]
	delete(p.calls, callID)
	p.mu.Unlock()
	if cs == nil {
		return nil
	}
	if p.audio != nil {
		p.audio.DetachEngine()
	}
	// 视频引擎先关（停采集/停解码），再关音频端口。闭环幂等，
	// 与 cleanupCall / Phone.Close 并发调用无副作用。
	if v := cs.video; v != nil {
		vrx, vtx, vlost := v.Stats()
		logx.Logger().Info("视频媒体统计", "callId", cs.id, "rx", vrx, "lost", vlost, "tx", vtx)
	}
	p.closeVideo(cs)
	if cs.engine != nil {
		// 收包统计（Info 级）：回铃音等早期媒体排查用——rx=0 说明
		// 对端 RTP 未到达（网络/地址问题），rx>0 说明到达但播放链路问题
		rx, lost, tx := cs.engine.Stats()
		logx.Logger().Info("通话媒体统计", "callId", cs.id, "rx", rx, "lost", lost, "tx", tx)
		cs.engine.Close()
	}
	p.sink.OnTerminated(cs.id, reason)
	return cs
}

// cleanupCall 结束通话：endCallImmediate + 取消会话上下文 + 关闭 dialog。
func (p *Phone) cleanupCall(callID, reason string) {
	cs := p.endCallImmediate(callID, reason)
	if cs == nil {
		return
	}
	if cs.cancel != nil {
		cs.cancel()
	}
	// 关闭 dialog 做缓存清理。Close 不发送任何信令（BYE/CANCEL 由
	// 信令路径负责）、幂等且线程安全（sync.Map 删除），与后台信令
	// goroutine 并发调用无副作用——此前远程挂断场景 dialog 泄漏在
	// cache 中（bug 修复）。
	if cs.dialogS != nil {
		_ = cs.dialogS.Close()
	}
	if cs.dialogC != nil {
		_ = cs.dialogC.Close()
	}
}

// localIP 本机 IP（SDP 用）：取默认路由接口地址。
func (p *Phone) localIP() string {
	conn, err := net.Dial("udp", p.localAddr())
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// localAddr 账户服务器地址（用于确定出口 IP）。
func (p *Phone) localAddr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return net.JoinHostPort(p.acc.Server, strconv.Itoa(p.acc.Port))
}

// localHost 本机 Contact/SDP 主机：STUN 公网地址优先（启用时），
// 其次本机出口 IP，最后主机名。
// 注意：内部的 localIP() 会做一次 UDP 拨号探测、并再取一次 p.mu（localAddr），
// 所以公共地址必须在**放开锁之后**才取。
func (p *Phone) localHost() string {
	// 显式配置的"声明 IP"优先级最高（REQ-ACC-023）：配了它就不再走 STUN/
	// 出口探测——用户既然手填了，就以它为准（Contact 与 SDP 都用这里）。
	if ip := p.declaredIP(); ip != "" {
		return ip
	}
	p.mu.Lock()
	pub := p.publicIP
	p.mu.Unlock()
	if pub != "" {
		return pub
	}
	if ip := p.localIP(); ip != "" {
		return ip
	}
	return p.client.Hostname()
}

// declaredIP 配置里显式声明的 IP（REQ-ACC-023），未配置为空串。
func (p *Phone) declaredIP() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return config.NormalizePublicIP(p.acc.PublicIP)
}

// applyDeclaredIP 把配置的声明 IP 应用到**已经建好的** sipgo Client 上
//（Via 的 sent-by）。Contact 与 SDP 走 localHost()，不用在这里管。
// 账户变更（Register）时调用，因此保存配置即生效，不必重启。
func (p *Phone) applyDeclaredIP(acc config.Account) {
	setClientHost(p.client, config.NormalizePublicIP(acc.PublicIP))
}

// parseTarget 解析呼叫目标：纯号码补全为 sip:号码@域名（未配域名则用服务器）。
func parseTarget(target string, acc config.Account) (sip.Uri, error) {
	if strings.HasPrefix(strings.ToLower(target), "sip:") || strings.HasPrefix(strings.ToLower(target), "sips:") {
		u := sip.Uri{}
		if err := sip.ParseUri(target, &u); err != nil {
			return u, fmt.Errorf("目标 URI 非法: %w", err)
		}
		if u.Host == "" {
			u.Host = uriHostOf(acc)
		}
		return u, nil
	}
	// 纯号码
	if strings.ContainsAny(target, " \t\r\n") {
		return sip.Uri{}, fmt.Errorf("号码包含非法字符")
	}
	if d := config.NormalizeDomain(acc.Domain); d != "" {
		// 配了域名：URI 用域名（不带端口），实际发往 Server:Port（见 dccInvite）
		return sip.Uri{User: target, Host: d}, nil
	}
	return sip.Uri{User: target, Host: acc.Server, Port: acc.Port}, nil
}

// statusText 常用 SIP 状态码的简短原因短语。
func statusText(code int) string {
	switch code {
	case 180:
		return "Ringing"
	case 200:
		return "OK"
	case 486:
		return "Busy Here"
	case 488:
		return "Not Acceptable Here"
	case 500:
		return "Server Internal Error"
	case 603:
		return "Decline"
	default:
		return "OK"
	}
}

// 确保实现 core.Telephony 接口。
var _ core.Telephony = (*Phone)(nil)
