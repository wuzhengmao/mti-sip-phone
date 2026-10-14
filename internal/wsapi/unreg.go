package wsapi

import (
	"sync"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// unregDelay 所有 WS 连接断开后、真正取消注册前的延迟确认窗口：
// 页面刷新/瞬时断网会在窗口内重连（重连即取消），不触发注册抖动。
//
// 测试会把窗口缩到 100ms 跑完整流程，而连接断开/定时器触发都在后台
// goroutine，直接覆写变量与读取构成数据竞争（race 检测器实测抓出），
// 所以经读写锁存取。
var (
	unregDelayMu sync.RWMutex
	unregDelay   = 3 * time.Second
)

// setUnregDelay 覆写延迟窗口（测试用）。
func setUnregDelay(d time.Duration) {
	unregDelayMu.Lock()
	unregDelay = d
	unregDelayMu.Unlock()
}

// unregDelayValue 读取延迟窗口。
func unregDelayValue() time.Duration {
	unregDelayMu.RLock()
	defer unregDelayMu.RUnlock()
	return unregDelay
}

// unregGuard 「WS 断开自动取消注册」（REQ-WS-020）的状态机。
//
// 由 SDK 的 unregisterOnDisconnect 参数经 system.setUnregisterOnDisconnect
// 开启（应用级开关，最后一次设置生效；默认关、不持久化——SDK 每次连上重发）。
// 开关开启时，**所有** WS 连接断开并超过 unregDelay 仍未回来 → 自动取消 SIP
// 注册；若正在呼叫/振铃/通话（core.App.HasActiveCall），取消注册**排队**、
// 等话务结束再执行；等待期间有连接回来（重连）就把排队任务**取消**，并恢复
// 已被自动取消的注册（重新注册）。
//
// 锁口径：mu 只做快速簿记（连接数/标志/定时器），**不持 mu 调 PatchAccount**
// ——SIP 注销/注册在服务器不可达时会阻塞数十秒（拨号重试），持锁会把连接
// 登记与恢复注册一起卡死。后端调用统一经 opMu 串行（注销→恢复的顺序由它
// 保证：断线时先注销、连接回来时恢复排队在其后）。
type unregGuard struct {
	mu           sync.Mutex
	opMu         sync.Mutex // PatchAccount 后端调用串行化（可能长时间阻塞）
	app          *core.App
	on           bool          // 开关（system.setUnregisterOnDisconnect）
	conns        int           // 当前活跃的 WS 连接数
	timer        *time.Timer   // 连接数归零后的延迟确认定时器
	busy         bool          // 排队中：等话务结束再注销
	unregistered bool          // 已由本机制取消注册（连接回来时恢复）
	wake         chan struct{} // 状态变化唤醒等待中的 watcher（buffer=1）
}

// newUnregGuard 创建状态机。
func newUnregGuard(app *core.App) *unregGuard {
	return &unregGuard{app: app, wake: make(chan struct{}, 1)}
}

// kick 唤醒 watcher 重新检查状态（非阻塞投递，与注册看护同款手法）。
func (g *unregGuard) kick() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// setOn 开关（system.setUnregisterOnDisconnect）。关闭时取消延迟定时器与排队。
func (g *unregGuard) setOn(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.on == on {
		return
	}
	g.on = on
	if !on {
		if g.timer != nil {
			g.timer.Stop()
			g.timer = nil
		}
		g.busy = false
		g.kick()
		logx.Logger().Debug("断线自动注销已关闭")
	}
}

// connOpened 有 WS 连接建立：取消延迟/排队任务，恢复被自动取消的注册。
// 恢复在后台 goroutine 里做（SIP 注册可能长时间阻塞，不能卡住连接处理）。
func (g *unregGuard) connOpened() {
	shouldRestore := false
	g.mu.Lock()
	g.conns++
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	if g.busy {
		g.busy = false
		g.kick()
		logx.Logger().Debug("网页已重连：取消排队的断线注销")
	}
	if g.unregistered {
		g.unregistered = false
		shouldRestore = true
	}
	g.mu.Unlock()
	if shouldRestore {
		go g.restore()
	}
}

// connClosed 有 WS 连接断开：全部断开且开关开启时，延迟确认后取消注册。
func (g *unregGuard) connClosed() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.conns--
	if g.conns > 0 || !g.on || g.unregistered || g.busy {
		return
	}
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(unregDelayValue(), g.onDisconnectConfirmed)
}

// onDisconnectConfirmed 延迟窗口结束仍无连接：执行注销或排队。
func (g *unregGuard) onDisconnectConfirmed() {
	g.mu.Lock()
	if g.conns > 0 || !g.on || g.unregistered || g.busy {
		g.mu.Unlock()
		return // 窗口内重连了/开关关了/已注销/已在排队
	}
	if g.app.HasActiveCall() {
		// 呼叫/振铃/通话中：取消注册排队，等话务结束再执行
		g.busy = true
		g.kick()
		g.mu.Unlock()
		go g.watchCallEnd()
		return
	}
	g.mu.Unlock()
	g.doUnregister()
}

// watchCallEnd 排队等待：话务结束后执行注销；期间连接回来/开关关闭即取消。
func (g *unregGuard) watchCallEnd() {
	sub := g.app.Bus().Subscribe()
	defer g.app.Bus().Unsubscribe(sub)
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if ev.Name != "call.terminated" && ev.Name != "call.failed" {
				continue
			}
			done := false
			g.mu.Lock()
			if g.busy && !g.app.HasActiveCall() {
				g.busy = false
				done = true
			}
			g.mu.Unlock()
			if done {
				g.doUnregister()
				return
			}
		case <-g.wake:
			g.mu.Lock()
			busy := g.busy
			g.mu.Unlock()
			if !busy {
				return // 重连/开关关闭：排队任务已取消
			}
		}
	}
}

// doUnregister 执行自动注销（幂等；失败回退标志，下次断线还会再试）。
func (g *unregGuard) doUnregister() {
	g.mu.Lock()
	if g.unregistered {
		g.mu.Unlock()
		return
	}
	g.unregistered = true
	g.mu.Unlock()

	g.opMu.Lock()
	defer g.opMu.Unlock()
	if _, err := g.app.PatchAccount(core.AccountPatch{Enabled: boolPtr(false)}); err != nil {
		g.mu.Lock()
		g.unregistered = false
		g.mu.Unlock()
		logx.Logger().Warn("断线自动取消注册失败", "err", err)
		return
	}
	logx.Logger().Info("网页已全部断开：已自动取消 SIP 注册（网页重新连接后恢复）")
}

// restore 恢复被自动取消的注册（重新启用账户）。经 opMu 与注销串行——
// 先注销后恢复的顺序由此保证；SIP 注册可能长时间阻塞，只能放后台跑。
func (g *unregGuard) restore() {
	g.opMu.Lock()
	defer g.opMu.Unlock()
	if _, err := g.app.PatchAccount(core.AccountPatch{Enabled: boolPtr(true)}); err != nil {
		logx.Logger().Warn("网页重连：恢复注册失败", "err", err)
		return
	}
	logx.Logger().Info("网页重连：已恢复 SIP 注册")
}

// boolPtr 造 *bool（core.AccountPatch 的指针字段）。
func boolPtr(v bool) *bool { return &v }
