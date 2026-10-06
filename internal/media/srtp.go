package media

// 媒体加密：SDES-SRTP（RFC 4568，REQ-MEDIA-009）。
//
// 两条最容易踩错、改代码前必须记住的口径：
//
//  1. **密钥方向**（RFC 4568 §6.1）：SDP 里 a=crypto 的密钥是「**写这条 SDP 的
//     一方用来加密它自己发出的媒体**」的密钥。所以本端**发送**用自己写进 SDP 的
//     密钥，**接收**用对端 SDP 里给的密钥——不是同一把，也别搞反。
//     密钥是单向的，crypto-suite（算法套件）是双向的。
//  2. **answer 必须复用 offer 中选中那条 crypto 的 tag 与 crypto-suite**，
//     只把自己新生成的密钥填进去（tag/suite 由 offerer 决定）。
//
// 加密覆盖 RTP 与 RTCP（音频、视频、DTMF 事件包全都走 RTP，因此一并加密）；
// SIP 信令不归它管——那是传输层 TLS 的职责（REQ-ACC-006）。SDES 的密钥在 SDP
// 里明文传输，**只有配合信令加密才有实际意义**（界面与文档都要这么写）。

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"

	"mti-sip-phone/internal/logx"
)

// SRTPPolicy 媒体加密策略（配置项 account.srtp）。
type SRTPPolicy int

const (
	// SRTPOff 关闭：完全不协商 SRTP，对端要求也按明文应答。
	SRTPOff SRTPPolicy = iota
	// SRTPSupported 支持（默认）：本端**不主动声明**，对端要求（offer 带 crypto）
	// 则配合加密。
	SRTPSupported
	// SRTPPreferred 建议：本端 offer 声明 SRTP，但对端不接受时按明文继续。
	SRTPPreferred
	// SRTPRequired 必要：本端要求 SRTP，对端不支持（offer/answer 里没有可接受的
	// crypto 行）则本次呼叫失败——呼出挂断、呼入回 488。
	SRTPRequired
)

// String 中文名（日志与错误信息用）。
func (p SRTPPolicy) String() string {
	switch p {
	case SRTPOff:
		return "关闭"
	case SRTPSupported:
		return "支持"
	case SRTPPreferred:
		return "建议"
	case SRTPRequired:
		return "必要"
	default:
		return "未知"
	}
}

// offersSRTP 本端发起呼叫时是否在 offer 里声明 SRTP。
func (p SRTPPolicy) offersSRTP() bool { return p == SRTPPreferred || p == SRTPRequired }

// acceptsSRTP 对端要求加密时本端是否配合（关闭策略下一律明文）。
func (p SRTPPolicy) acceptsSRTP() bool { return p != SRTPOff }

// requiresSRTP 是否强制要求加密（对端不支持即失败）。
func (p SRTPPolicy) requiresSRTP() bool { return p == SRTPRequired }

// ErrSRTPRequired 本端要求 SRTP 但对端没有可接受的 crypto 行。
var ErrSRTPRequired = errors.New("对端未提供可接受的 SRTP 加密参数，而本机要求媒体必须加密")

// ---------------------------------------------------------------------------
// crypto-suite
// ---------------------------------------------------------------------------

// cryptoSuite 一个加密套件：SDP 里的名字 + pion 的 profile。
// 算法细节（密钥长度、盐长度、认证标签）全部由 profile 推导，不手写常量。
type cryptoSuite struct {
	name    string
	profile srtp.ProtectionProfile
}

// cryptoSuites 本端支持/提供的套件，**顺序即优先级**（写进 offer 的顺序）。
// AES_CM_128_HMAC_SHA1_80 是事实标准（Asterisk/FreeSWITCH/MicroSIP 都支持），
// 恒排第一；其余排后面由对端按需选，对端不认识的名字它会跳过。
var cryptoSuites = []cryptoSuite{
	{"AES_CM_128_HMAC_SHA1_80", srtp.ProtectionProfileAes128CmHmacSha1_80},
	{"AES_256_CM_HMAC_SHA1_80", srtp.ProtectionProfileAes256CmHmacSha1_80},
	{"AES_CM_128_HMAC_SHA1_32", srtp.ProtectionProfileAes128CmHmacSha1_32},
	{"AES_256_CM_HMAC_SHA1_32", srtp.ProtectionProfileAes256CmHmacSha1_32},
	{"AEAD_AES_128_GCM", srtp.ProtectionProfileAeadAes128Gcm},
	{"AEAD_AES_256_GCM", srtp.ProtectionProfileAeadAes256Gcm},
}

// suiteByName 按 SDP 里的名字找套件（不区分大小写）。
func suiteByName(name string) (cryptoSuite, bool) {
	for _, s := range cryptoSuites {
		if strings.EqualFold(s.name, name) {
			return s, true
		}
	}
	return cryptoSuite{}, false
}

// ---------------------------------------------------------------------------
// 本端密钥与 a=crypto 行
// ---------------------------------------------------------------------------

// cryptoKey 本端一条 a=crypto 需要的全部信息：tag + 套件 + 主密钥/盐。
type cryptoKey struct {
	tag   int
	suite cryptoSuite
	key   []byte
	salt  []byte
}

// newCryptoKey 生成一条本端 crypto：主密钥与盐用 crypto/rand，
// 长度取自套件本身（不硬编码：AEAD 系列的盐是 12 字节、AES-CM 是 14 字节）。
func newCryptoKey(tag int, s cryptoSuite) (*cryptoKey, error) {
	keyLen, err := s.profile.KeyLen()
	if err != nil {
		return nil, err
	}
	saltLen, err := s.profile.SaltLen()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, keyLen+saltLen)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("生成 SRTP 密钥失败: %w", err)
	}
	return &cryptoKey{tag: tag, suite: s, key: buf[:keyLen], salt: buf[keyLen:]}, nil
}

// line 本端这条 crypto 的 SDP 属性值（a=crypto: 后面那串）。
// 格式：<tag> <套件名> inline:<base64(key||salt)>
func (k *cryptoKey) line() string {
	material := make([]byte, 0, len(k.key)+len(k.salt))
	material = append(material, k.key...)
	material = append(material, k.salt...)
	return fmt.Sprintf("%d %s inline:%s", k.tag, k.suite.name,
		base64.StdEncoding.EncodeToString(material))
}

// peerCrypto 对端一条已解析的 crypto 行。
type peerCrypto struct {
	tag   int
	suite cryptoSuite
	key   []byte
	salt  []byte
	raw   string
}

// parseCryptoAttr 解析一条 a=crypto 的属性值：
//
//	<tag> <crypto-suite> inline:<base64(key||salt)>[|<lifetime>[|<MKI>:<len>]] [session-params]
//
// 返回 ok=false 表示「本端不支持/不认识」——调用方应当跳过这条、继续看下一条
//（RFC 4568 要求忽略不认识的 crypto 属性，而不是整条 offer 作废）。
// 目前**不实现** lifetime / MKI / 会话参数（UNENCRYPTED_SRTP 等）：
// 带这些的行一律跳过，宁可退回明文也不做半吊子的「只认证不加密」。
func parseCryptoAttr(value string) (*peerCrypto, bool) {
	fields := strings.Fields(value)
	if len(fields) < 3 {
		return nil, false
	}
	// 会话参数（第 4 个字段起）本端一个都不支持，直接跳过这条
	if len(fields) > 3 {
		return nil, false
	}
	tag, err := strconv.Atoi(fields[0])
	if err != nil || tag <= 0 {
		return nil, false
	}
	suite, ok := suiteByName(fields[1])
	if !ok {
		return nil, false
	}
	if !strings.HasPrefix(strings.ToLower(fields[2]), "inline:") {
		return nil, false // 只支持 inline 密钥方式
	}
	rest := fields[2][len("inline:"):]
	if strings.Contains(rest, "|") {
		// 带 lifetime / MKI：跳过（见函数注释）
		return nil, false
	}
	material, err := decodeBase64(rest)
	if err != nil {
		return nil, false
	}
	keyLen, err := suite.profile.KeyLen()
	if err != nil {
		return nil, false
	}
	saltLen, err := suite.profile.SaltLen()
	if err != nil {
		return nil, false
	}
	if len(material) != keyLen+saltLen {
		return nil, false
	}
	return &peerCrypto{
		tag:   tag,
		suite: suite,
		key:   material[:keyLen],
		salt:  material[keyLen:],
		raw:   value,
	}, true
}

// decodeBase64 base64 解码（容忍缺省填充：部分实现不带 '='）。
func decodeBase64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

// selectPeerCrypto 从对端给的一串 a=crypto 里挑一条本端支持的。
// **按对端给出的顺序**（RFC 4568 §5.1.2：属性顺序即优先级）。
func selectPeerCrypto(values []string) (*peerCrypto, bool) {
	for _, v := range values {
		if pc, ok := parseCryptoAttr(v); ok {
			return pc, true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// 收发上下文
// ---------------------------------------------------------------------------

// srtpStream 一个媒体段的收发加密上下文（RTP 与 RTCP 各一个方向一套。
// pion 的 Context 同时管 SRTP/SRTCP，收发各持一个实例 + 一把锁：
// 发送侧会被采集回调与 RTCP 发送循环并发调用，接收侧被 RTP/RTCP 两个接收
// 循环并发调用，pion 不保证并发安全，锁必须自己上）。
type srtpStream struct {
	suite cryptoSuite

	txMu   sync.Mutex
	tx     *srtp.Context
	txKey  []byte
	txSalt []byte

	rxMu   sync.Mutex
	rx     *srtp.Context
	rxKey  []byte
	rxSalt []byte
}

// newSRTPStream 用「本端发送密钥 + 对端接收密钥」建立收发上下文。
func newSRTPStream(suite cryptoSuite, txKey, txSalt, rxKey, rxSalt []byte) (*srtpStream, error) {
	tx, err := newSRTPContext(suite, txKey, txSalt)
	if err != nil {
		return nil, err
	}
	rx, err := newSRTPContext(suite, rxKey, rxSalt)
	if err != nil {
		return nil, err
	}
	return &srtpStream{
		suite: suite,
		tx:    tx, txKey: txKey, txSalt: txSalt,
		rx: rx, rxKey: rxKey, rxSalt: rxSalt,
	}, nil
}

// newSRTPContext 建立一个 SRTP 上下文。
//
// ⚠️ pion 的 CreateContext **默认关闭重放保护**（SRTPNoReplayProtection），
// 必须显式打开：否则抓包重放一个旧包会被当正常包播出，「防重放」这个卖点是假的。
func newSRTPContext(suite cryptoSuite, key, salt []byte) (*srtp.Context, error) {
	return srtp.CreateContext(key, salt, suite.profile,
		srtp.SRTPReplayProtection(srtpReplayWindow),
		srtp.SRTCPReplayProtection(srtpReplayWindow))
}

// srtpReplayWindow 重放保护窗口（包数）。64 与 RFC 3711 的推荐量级一致。
const srtpReplayWindow = 64

// protectRTP 加密一个完整的 RTP 包（含头部；头部只认证不加密）。
func (s *srtpStream) protectRTP(pkt []byte, h *rtp.Header) ([]byte, error) {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	return s.tx.EncryptRTP(nil, pkt, h)
}

// unprotectRTP 解密并校验一个完整的 SRTP 包，返回明文 RTP 包。
func (s *srtpStream) unprotectRTP(pkt []byte, h *rtp.Header) ([]byte, error) {
	s.rxMu.Lock()
	defer s.rxMu.Unlock()
	return s.rx.DecryptRTP(nil, pkt, h)
}

// protectRTCP 加密一个完整的 RTCP 包（SRTCP，尾部附索引与认证标签）。
func (s *srtpStream) protectRTCP(pkt []byte, h *rtcp.Header) ([]byte, error) {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	return s.tx.EncryptRTCP(nil, pkt, h)
}

// unprotectRTCP 解密并校验一个 SRTCP 包。
func (s *srtpStream) unprotectRTCP(pkt []byte, h *rtcp.Header) ([]byte, error) {
	s.rxMu.Lock()
	defer s.rxMu.Unlock()
	return s.rx.DecryptRTCP(nil, pkt, h)
}

// resetRx 重建接收上下文（清空 ROC / 序号 / 重放窗口状态）。
// 换流时调用：183 早期媒体 → 200 新流会换 SSRC 或让序号大跳变，接收侧
// 沿用旧的序号推断会算出错误的 ROC，表现为「接通后解密全部失败、没有声音」。
func (s *srtpStream) resetRx() {
	s.rxMu.Lock()
	defer s.rxMu.Unlock()
	if ctx, err := newSRTPContext(s.suite, s.rxKey, s.rxSalt); err == nil {
		s.rx = ctx
	}
}

// ---------------------------------------------------------------------------
// 一个媒体段的加密状态
// ---------------------------------------------------------------------------

// cryptoState 一个媒体段（m= 行）的 SDES-SRTP 状态机。
// 音频 Engine 与视频 VideoEngine 各持一份：每个 m= 行有**独立的密钥**
// （RFC 4568：crypto 是媒体级属性）。
type cryptoState struct {
	mu     sync.RWMutex
	policy SRTPPolicy
	// local 本端写进 SDP 的 crypto 行。offer 时是全部候选（一整套件一行），
	// answer 时只有选中那一条。
	local []*cryptoKey
	// peer 对端选中/提供的行（本端解密用它）。
	peer   *peerCrypto
	stream *srtpStream
	// answered 本端在 answer 里是否要写 crypto 行（被叫协商成功后才写）。
	answered bool
	// errCount 加解密失败计数（限频日志用，见 logCryptoErr）。
	errCount atomic.Uint64
}

// setPolicy 设置策略（协商前调用；零值 = 关闭，与历史行为一致）。
func (c *cryptoState) setPolicy(p SRTPPolicy) {
	c.mu.Lock()
	c.policy = p
	c.mu.Unlock()
}

// active 当前是否已启用加密。
func (c *cryptoState) active() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stream != nil
}

// suiteName 当前套件名（未加密时空串）。
func (c *cryptoState) suiteName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.stream == nil {
		return ""
	}
	return c.stream.suite.name
}

// streamRef 取当前收发上下文（nil = 明文）。返回的指针在通话内不会被替换，
// 只有 resetRx 会换掉它内部的接收上下文（自身有锁保护）。
func (c *cryptoState) streamRef() *srtpStream {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stream
}

// offerLines 本端 offer 要写的 a=crypto 行（属性值，不含 "a=crypto:" 前缀）。
// 策略不允许主动声明（关闭/支持）时返回 nil。密钥**每通电话生成一次**，
// 之后的 re-INVITE（保持/恢复）复用同一批——中途换密钥只会带来风险。
func (c *cryptoState) offerLines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.policy.offersSRTP() {
		return nil
	}
	if c.local == nil {
		keys := make([]*cryptoKey, 0, len(cryptoSuites))
		for i, s := range cryptoSuites {
			k, err := newCryptoKey(i+1, s) // tag 从 1 开始，顺序即优先级
			if err != nil {
				logx.Logger().Warn("生成 SRTP 密钥失败，本次不加密", "err", err)
				return nil
			}
			keys = append(keys, k)
		}
		c.local = keys
	}
	return cryptoKeyLines(c.local)
}

// answerLines 本端 answer 要写的 a=crypto 行：只有被叫协商成功时才写
//（主叫路径 offer 的密钥已经写过了，answer 由 SDPAnswerFor 组装时同样要带上）。
func (c *cryptoState) answerLines() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.answered {
		return nil
	}
	return cryptoKeyLines(c.local)
}

// cryptoKeyLines 取一串 cryptoKey 的 SDP 属性值。
func cryptoKeyLines(keys []*cryptoKey) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.line()
	}
	return out
}

// applyRemoteOffer 本端**被叫**（收到 offer）：从对端 offer 的 crypto 行里
// 选一条，生成/复用本端密钥（tag 与套件必须跟选中那条一致），建立收发上下文。
//
// 返回 ErrSRTPRequired 表示策略要求加密但对端没给可接受的参数——调用方
// 应当拒接（回 488），而不是静默降级。
func (c *cryptoState) applyRemoteOffer(values []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 策略为「关闭」时**一律不协商**：对端单方面要求加密也不接受（这正是
	// "关闭"的含义——对端若把加密当必要条件，由它自己拒接）。
	var pc *peerCrypto
	var ok bool
	if c.policy.acceptsSRTP() {
		pc, ok = selectPeerCrypto(values)
	}
	if !ok {
		if c.policy.requiresSRTP() {
			logx.Logger().Warn("对端 offer 未提供可接受的 SRTP 参数，本机要求加密，拒绝本次呼叫",
				"策略", c.policy.String(), "对端 crypto 行", len(values))
			return ErrSRTPRequired
		}
		switch {
		case len(values) > 0 && !c.policy.acceptsSRTP():
			logx.Logger().Warn("对端要求媒体加密，但本机媒体加密已关闭，本次通话为明文（对端可能因此拒接）",
				"对端 crypto 行", len(values))
		case len(values) > 0:
			logx.Logger().Warn("对端要求媒体加密，但本端不支持其加密套件，本次通话为明文",
				"对端 crypto 行", len(values))
		case c.stream != nil:
			// 重协商（re-INVITE）里对端把 crypto 去掉了 = 要求取消加密。
			// 非"必要"策略下按明文继续，但必须留下明确日志：这是一次**降级**。
			logx.Logger().Warn("对端在重协商中取消了媒体加密，本次通话降级为明文")
		}
		c.clearLocked()
		return nil
	}
	// 密钥参数没变（对端重发/re-INVITE 原样重复）：**保留现有上下文**。
	// 重建会让接收侧的序号/ROC 状态清零，通话超过 ~22 分钟（序号绕回）后
	// 重建一次就可能算出错误的 ROC，表现为"重协商后突然没声音"。
	if c.stream != nil && samePeerCrypto(c.peer, pc) {
		return nil
	}

	// 本端密钥：tag 与套件复用对端选中那条（RFC 4568 §6.1），
	// re-INVITE 时若 tag 未变则复用现有密钥（避免中途换密钥导致丢包/乱序）。
	local := c.localForKeyLocked(pc)
	stream, err := newSRTPStream(pc.suite, local.key, local.salt, pc.key, pc.salt)
	if err != nil {
		if c.policy.requiresSRTP() {
			return fmt.Errorf("%w: %v", ErrSRTPRequired, err)
		}
		logx.Logger().Warn("建立 SRTP 上下文失败，本次通话为明文", "err", err)
		c.clearLocked()
		return nil
	}
	c.local = []*cryptoKey{local}
	c.peer = pc
	c.stream = stream
	c.answered = true
	logx.Logger().Info("媒体加密已启用（本端被叫）", "套件", pc.suite.name, "tag", pc.tag)
	return nil
}

// applyRemoteAnswer 本端**主叫**（收到 answer）：按 tag 匹配本端 offer 里
// 选中的那条，用对端给的密钥建立接收方向。
func (c *cryptoState) applyRemoteAnswer(values []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var pc *peerCrypto
	var ok bool
	if c.policy.acceptsSRTP() {
		pc, ok = selectPeerCrypto(values)
	}
	if !ok {
		if c.policy.requiresSRTP() {
			logx.Logger().Warn("对端 answer 未接受 SRTP，本机要求加密，本次呼叫失败",
				"策略", c.policy.String())
			return ErrSRTPRequired
		}
		if len(values) > 0 {
			logx.Logger().Warn("对端 answer 的 SRTP 参数本端不支持，本次通话为明文", "对端 crypto 行", len(values))
		} else if c.stream != nil {
			logx.Logger().Warn("对端在重协商的 answer 中取消了媒体加密，本次通话降级为明文")
		}
		c.clearLocked()
		return nil
	}

	// 对端重发/保持恢复等场景下 answer 会原样重复同一组参数：**保留现有上下文**
	//（同 applyRemoteOffer 的理由——重建会清空接收侧序号/ROC 状态）
	if c.stream != nil && samePeerCrypto(c.peer, pc) {
		return nil
	}
	// answer 里的 tag/suite 必须能在本端 offer 里找到（RFC 4568 §6.1）
	local := findLocalKey(c.local, pc.tag)
	if local == nil || local.suite.name != pc.suite.name {
		if c.policy.requiresSRTP() {
			logx.Logger().Warn("对端 answer 的 crypto 与本端 offer 不匹配，本机要求加密，本次呼叫失败",
				"对端 tag", pc.tag, "套件", pc.suite.name)
			return ErrSRTPRequired
		}
		logx.Logger().Warn("对端 answer 的 crypto 与本端 offer 不匹配，本次通话为明文",
			"对端 tag", pc.tag, "套件", pc.suite.name)
		c.clearLocked()
		return nil
	}
	stream, err := newSRTPStream(pc.suite, local.key, local.salt, pc.key, pc.salt)
	if err != nil {
		if c.policy.requiresSRTP() {
			return fmt.Errorf("%w: %v", ErrSRTPRequired, err)
		}
		logx.Logger().Warn("建立 SRTP 上下文失败，本次通话为明文", "err", err)
		c.clearLocked()
		return nil
	}
	c.local = []*cryptoKey{local}
	c.peer = pc
	c.stream = stream
	c.answered = true
	logx.Logger().Info("媒体加密已启用（本端主叫）", "套件", pc.suite.name, "tag", pc.tag)
	return nil
}

// localForKeyLocked 取本端用于该 tag 的密钥：tag+套件相同则复用现有密钥，
// 否则按对端的 tag/套件新生成一条（对端 re-INVITE 换 tag = 要求换密钥）。
func (c *cryptoState) localForKeyLocked(pc *peerCrypto) *cryptoKey {
	if k := findLocalKey(c.local, pc.tag); k != nil && k.suite.name == pc.suite.name {
		return k
	}
	k, err := newCryptoKey(pc.tag, pc.suite)
	if err != nil {
		// 生成失败不该发生（rand 失败）；返回空密钥让上层建上下文时报错
		logx.Logger().Warn("生成 SRTP 密钥失败", "err", err)
		return &cryptoKey{tag: pc.tag, suite: pc.suite}
	}
	return k
}

// samePeerCrypto 对端的 crypto 参数是否与已启用的完全一致（tag/套件/密钥）。
func samePeerCrypto(a, b *peerCrypto) bool {
	if a == nil || b == nil {
		return false
	}
	return a.tag == b.tag && a.suite.name == b.suite.name &&
		bytes.Equal(a.key, b.key) && bytes.Equal(a.salt, b.salt)
}

// findLocalKey 按 tag 找本端 offer 里的密钥。
func findLocalKey(keys []*cryptoKey, tag int) *cryptoKey {
	for _, k := range keys {
		if k.tag == tag {
			return k
		}
	}
	return nil
}

// clearLocked 回到明文状态（调用方持写锁）。
func (c *cryptoState) clearLocked() {
	c.stream = nil
	c.peer = nil
	c.answered = false
}

// dropEncryption 重协商中把加密去掉（对端新 offer/answer 不再带 crypto）。
// 由协商路径根据策略决定是否允许（"必要"策略下不允许，会先报错拒绝）。
func (c *cryptoState) dropEncryption() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearLocked()
}

// resetRx 换流时重建接收上下文（见 srtpStream.resetRx）。
func (c *cryptoState) resetRx() {
	if s := c.streamRef(); s != nil {
		s.resetRx()
	}
}

// ---------------------------------------------------------------------------
// 加解密入口（引擎的发送/接收路径调用）
// ---------------------------------------------------------------------------

// protectRTP 发送前加密；未启用或失败时返回 nil（调用方丢弃该包——
// 加密启用后**绝不能**再发明文包，对端会因认证失败丢弃甚至挂断）。
func (c *cryptoState) protectRTP(pkt []byte, h *rtp.Header) []byte {
	s := c.streamRef()
	if s == nil {
		return pkt
	}
	out, err := s.protectRTP(pkt, h)
	if err != nil {
		c.logCryptoErr("RTP 加密失败", err)
		return nil
	}
	return out
}

// unprotectRTP 接收后解密；未启用时原样返回。
func (c *cryptoState) unprotectRTP(pkt []byte, h *rtp.Header) ([]byte, bool) {
	s := c.streamRef()
	if s == nil {
		return pkt, true
	}
	out, err := s.unprotectRTP(pkt, h)
	if err != nil {
		c.logCryptoErr("RTP 解密失败", err)
		return nil, false
	}
	return out, true
}

// protectRTCP 发送 RTCP 前加密（未启用时原样返回）。
func (c *cryptoState) protectRTCP(pkt []byte, h *rtcp.Header) []byte {
	s := c.streamRef()
	if s == nil {
		return pkt
	}
	out, err := s.protectRTCP(pkt, h)
	if err != nil {
		c.logCryptoErr("RTCP 加密失败", err)
		return nil
	}
	return out
}

// unprotectRTCP 收到 RTCP 后解密（未启用时原样返回）。
func (c *cryptoState) unprotectRTCP(pkt []byte, h *rtcp.Header) ([]byte, bool) {
	s := c.streamRef()
	if s == nil {
		return pkt, true
	}
	out, err := s.unprotectRTCP(pkt, h)
	if err != nil {
		c.logCryptoErr("RTCP 解密失败", err)
		return nil, false
	}
	return out, true
}

// 解密/加密失败日志限频：对端换了密钥、丢包造成的坏包都会走到这里，
// 逐包打日志会把日志刷爆（每 20ms 一个包）。
const srtpErrLogInterval = 10 // 每 N 次失败打一条

// logCryptoErr 限频记录加解密失败。
func (c *cryptoState) logCryptoErr(msg string, err error) {
	if c.errCount.Add(1)%srtpErrLogInterval != 0 {
		return
	}
	logx.Logger().Warn(msg+"（媒体加密）", "err", err, "累计失败", c.errCount.Load())
}

// protectRTCPPkt / unprotectRTCPPkt 解析 RTCP 头后加/解密（音频与视频引擎共用）。
func protectRTCPPkt(c *cryptoState, pkt []byte) []byte {
	var h rtcp.Header
	if err := h.Unmarshal(pkt); err != nil {
		return nil
	}
	return c.protectRTCP(pkt, &h)
}

func unprotectRTCPPkt(c *cryptoState, pkt []byte) ([]byte, bool) {
	var h rtcp.Header
	if err := h.Unmarshal(pkt); err != nil {
		return nil, false
	}
	return c.unprotectRTCP(pkt, &h)
}
