package sip

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/logx"
)

// 注册看护（REQ-CALL-009）：系统休眠唤醒 / 网络切换后自动恢复注册。
//
// 为什么需要看护：registerLoop 的续注册定时器走单调时钟——系统挂起期间
// 该时钟不走（Linux CLOCK_MONOTONIC 如此，Windows QPC 视硬件而定），
// 醒来后租约很可能已在服务端过期，服务端把来电送到失效的 Contact，
// 本机界面却还显示"已注册"；网络切换则换掉本机出口地址，服务端登记的
// 旧 Contact 同样失效，要等下一次续注册（最长 0.7×expires）才纠正。
//
// 做法：每 5 秒巡检两件事，命中就经 kick 通道通知注册循环立即重新注册：
//  1. 墙钟跳变（休眠唤醒）：相邻两次巡检的墙钟间隔远超巡检周期；
//  2. 出向源地址变化 / 网络从不可达恢复（网络切换）。
//
// 用轮询而非平台事件（Windows PowerRegisterSuspendResumeNotification、
// Linux D-Bus login1 / netlink）：成本与依赖不成比例，轮询同样覆盖这两类事件。
const (
	watchDefaultTick  = 5 * time.Second  // 巡检周期
	watchDefaultGap   = 20 * time.Second // 墙钟跳变阈值（≥4×巡检周期）
	watchProbeTimeout = 3 * time.Second  // 出向地址探测超时（DNS/路由阻塞兜底）
)

// watchRegistration 注册看护协程：跟随注册上下文生命周期，只读 Phone 的
// 看护注入字段（watchTick / watchGap / watchNow / watchProbe，零值取默认），
// 事件经 kickCh 通知注册循环（通道约定见 Register）。
func (p *Phone) watchRegistration(ctx context.Context, acc config.Account, kickCh chan<- string) {
	tick := p.watchTick
	if tick <= 0 {
		tick = watchDefaultTick
	}
	gapLimit := p.watchGap
	if gapLimit <= 0 {
		gapLimit = watchDefaultGap
	}
	now := p.watchNow
	if now == nil {
		now = time.Now
	}
	probe := p.watchProbe
	if probe == nil {
		probe = probeOutboundIP
	}

	// 初始 STUN 公网地址探测（REQ-ACC-017）：探测成功后立即重新注册一次，
	// 让 Contact 携带公网地址（NAT 穿透）。
	if acc.STUNEnabled && acc.STUNServer != "" {
		if ip, err := stunDiscoverCtx(ctx, acc.STUNServer); err != nil {
			logx.Logger().Warn("STUN 探测失败", "err", err)
		} else {
			p.applyPublicIP(ctx, acc, ip)
			p.sendKick(kickCh, "STUN 公网地址就绪")
		}
	}
	if ctx.Err() != nil {
		return
	}

	// 基线探测（只记录、不判定）：若等到首个巡检才建基线，注册后前一个
	// 巡检周期内发生的网络切换会被当成基线，永远检测不到。
	target := net.JoinHostPort(acc.Server, strconv.Itoa(acc.Port))
	lastIP, lastOK := "", false
	if acc.Server != "" {
		if ip, err := probe(ctx, target); err == nil {
			lastIP, lastOK = ip, true
		}
	}
	lastWall := now()

	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// 1) 休眠唤醒：比较相邻两次巡检的**墙钟**间隔。
		// ⚠️ 必须用 UnixNano 相减，不能用 ts.Sub(lastWall) / time.Since——
		// 它们走单调时钟，而系统挂起期间单调时钟不走，恰好检测不到要检测的
		// 那段"丢失的时间"（写成 Sub 会静默失效，单测也测不出来）。
		ts := now()
		wallGap := time.Duration(ts.UnixNano() - lastWall.UnixNano())
		lastWall = ts
		var reasons []string
		if wallGap > gapLimit {
			reasons = append(reasons, "系统休眠唤醒（墙钟跳变 "+wallGap.Round(time.Second).String()+"）")
		}

		// 2) 网络切换：出向源地址变化，或从不可达恢复。探测失败期间不覆盖
		// 基线地址（网络恢复后与变化前的地址比较），且只在失败转换时告警一次。
		if acc.Server != "" {
			ip, err := probe(ctx, target)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return
				}
				if lastOK {
					logx.Logger().Warn("网络不可达，等待恢复", "err", err)
				}
				lastOK = false
			case !lastOK:
				reasons = append(reasons, "网络恢复（出口地址 "+ip+"）")
				lastIP, lastOK = ip, true
			case ip != lastIP:
				reasons = append(reasons, "网络出口地址变化 "+lastIP+" → "+ip)
				lastIP, lastOK = ip, true
			}
		}

		if len(reasons) == 0 {
			continue
		}
		reason := strings.Join(reasons, "；")
		logx.Logger().Info("检测到需要立即重新注册", "reason", reason)
		// 网络已变：STUN 公网地址与 TCP 信令连接都可能失效，先刷新再通知注册循环
		p.refreshPublicIP(ctx, acc)
		p.resetClientConnection(ctx, acc)
		p.sendKick(kickCh, reason)
	}
}

// refreshPublicIP 网络变化后重新探测 STUN 公网地址（启用时）：旧 NAT 映射
// 多半已失效。重探失败则清空 publicIP（回退本机出口地址注册）——继续带着
// 已失效的公网地址注册会得到"注册成功但不可达"，比注册失败更隐蔽。
func (p *Phone) refreshPublicIP(ctx context.Context, acc config.Account) {
	if !acc.STUNEnabled || acc.STUNServer == "" {
		return
	}
	ip, err := stunDiscoverCtx(ctx, acc.STUNServer)
	if err != nil {
		logx.Logger().Warn("网络变化后 STUN 重探失败，改用本机地址注册", "err", err)
		p.applyPublicIP(ctx, acc, "")
		return
	}
	p.applyPublicIP(ctx, acc, ip)
}

// applyPublicIP 写入 STUN 公网地址。必须校验上下文与账户：配置切换
// （重新 Register）或退出后，旧看护的探测结果不得污染新账户的 Contact
// （config.Account 是纯值结构，可直接比较）。
func (p *Phone) applyPublicIP(ctx context.Context, acc config.Account, ip string) {
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.acc != acc {
		return
	}
	p.publicIP = ip
}

// resetClientConnection 网络变化后丢弃 sipgo 复用的出向连接（仅 TCP）。
//
// UDP 出向 socket 是通配绑定（0.0.0.0:随机端口），切换网络后 OS 会按当前
// 路由重新选源地址、源端口不变，可自愈；TCP 则按远端地址复用连接，连接
// 死亡后不会因发送失败被清出池（sipgo 只在读循环出错时清理），不丢弃的话
// 每次重发 REGISTER 都写进死连接，Linux 上要黑洞十几分钟。
//
// 尽力而为：解析不到池键（域名/SRV 等）就跳过，不影响后续 kick 重注册。
func (p *Phone) resetClientConnection(ctx context.Context, acc config.Account) {
	if !strings.EqualFold(strings.TrimSpace(acc.Transport), "tcp") || acc.Server == "" {
		return
	}
	host := acc.Server
	if net.ParseIP(host) == nil {
		lookupCtx, cancel := context.WithTimeout(ctx, watchProbeTimeout)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
		if err != nil || len(addrs) == 0 {
			return
		}
		host = addrs[0].IP.String()
		for _, a := range addrs { // 池键用解析后的地址，优先取 IPv4
			if a.IP.To4() != nil {
				host = a.IP.To4().String()
				break
			}
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(acc.Port))
	c, err := p.client.TransportLayer().GetConnection("tcp", addr)
	if err != nil {
		return // 池中没有连接（尚未建连或已被读循环清理）
	}
	if err := c.Close(); err != nil {
		logx.Logger().Debug("丢弃旧 TCP 信令连接失败", "remote", addr, "err", err)
		return
	}
	logx.Logger().Info("网络变化：已丢弃旧 TCP 信令连接，下次注册重新建连", "remote", addr)
}

// sendKick 非阻塞投递"立即重新注册"信号：通道已有待处理信号时合并为一次
// （看护永不阻塞）；注册循环取消后无人消费，投递自动丢弃。
func (p *Phone) sendKick(kickCh chan<- string, reason string) {
	select {
	case kickCh <- reason:
	default:
	}
}

// probeOutboundIP 探测到 target 的出向源 IP：UDP Dial 只做路由选择、不实际
// 发包（也不要求对端可达），与 localIP() 同源，保证探测结果与 Contact 一致。
// 网络不可达时返回错误。
func probeOutboundIP(ctx context.Context, target string) (string, error) {
	d := net.Dialer{Timeout: watchProbeTimeout}
	conn, err := d.DialContext(ctx, "udp", target)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok && a.IP != nil {
		return a.IP.String(), nil
	}
	return "", fmt.Errorf("出向地址不可用")
}
