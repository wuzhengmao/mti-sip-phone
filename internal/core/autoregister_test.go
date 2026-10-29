package core

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mti-sip-phone/internal/config"
)

// regTel 记录注册/注销与呼叫控制调用的 Telephony 桩
// （REQ-ACC-024 / REQ-ACC-025 测试用）。
type regTel struct {
	Telephony
	mu     sync.Mutex
	regs   int
	unregs int
	last   config.Account

	// 视频能力（供远端控制指令的"静默降级"测试切换）
	videoSupported bool
	videoEnabled   bool

	// 心跳（MSP_HEARTBEAT）记录
	hbMu      sync.Mutex
	hbs       int
	hbMode    string
	hbBusy    bool
	hbExpires time.Duration
	hbName    string
	hbStatus  string
	hbErr     error // 非 nil 时 SendHeartbeat 返回它（模拟服务端不支持 NOTIFY）

	// 远端指令实际调到的动作
	dialed, dialedVideo     []string
	answered, answeredVideo []string
	hangups                 []string
	autoAnswer              *bool
}

func (r *regTel) Register(_ context.Context, acc config.Account) error {
	r.mu.Lock()
	r.regs++
	r.last = acc
	r.mu.Unlock()
	return nil
}

func (r *regTel) Unregister(context.Context) error {
	r.mu.Lock()
	r.unregs++
	r.mu.Unlock()
	return nil
}

// ConfigSnapshot 会查询视频能力（SetRegistered 后广播 system.config 要用），
// 内嵌的 nil Telephony 接口会 panic，这里显式给出"无视频"的桩值。
func (r *regTel) VideoSupported() bool   { return r.videoSupported }
func (r *regTel) CameraSupported() bool  { return r.videoSupported }
func (r *regTel) VideoCallEnabled() bool { return r.videoEnabled }

func (r *regTel) Dial(_ context.Context, target string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dialed = append(r.dialed, target)
	return "stub-call", nil
}

func (r *regTel) DialVideo(_ context.Context, target string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dialedVideo = append(r.dialedVideo, target)
	return "stub-call", nil
}

func (r *regTel) Answer(callID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answered = append(r.answered, callID)
	return nil
}

func (r *regTel) AnswerVideo(callID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answeredVideo = append(r.answeredVideo, callID)
	return nil
}

func (r *regTel) Hangup(callID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hangups = append(r.hangups, callID)
	return nil
}

func (r *regTel) SetAutoAnswer(v bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoAnswer = &v
	return nil
}

func (r *regTel) counts() (regs, unregs int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.regs, r.unregs
}

// newRegTestApp 构造带有效账户的 App（注册动作由 regTel 记录）。
func newRegTestApp(t *testing.T) (*App, *regTel) {
	t.Helper()
	cfg := config.Default()
	cfg.Account.Server = "10.0.0.1"
	cfg.Account.Account = "1001"
	cfg.Account.Enabled = true
	app := New(cfg, nil, NewBus())
	app.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	tel := &regTel{}
	app.SetTel(tel)
	return app, tel
}

// TestSetRegistered 登录/退出账号（REQ-ACC-024，复用「启用账户」）：
//   - false → 落盘 enabled=false 并**立即注销**（发 expires=0、停周期）；
//   - true  → 落盘 enabled=true 并**立即注册**；
//   - 重复设置同一值是幂等的（NOTIFY 重传不会重复注销）。
func TestSetRegistered(t *testing.T) {
	app, tel := newRegTestApp(t)
	sub := app.Bus().Subscribe()
	defer app.Bus().Unsubscribe(sub)

	if err := app.SetRegistered(false); err != nil {
		t.Fatalf("SetRegistered(false) 失败: %v", err)
	}
	if app.Config().Account.Enabled {
		t.Fatal("退出账号后「启用账户」应为 false")
	}
	if _, unregs := tel.counts(); unregs != 1 {
		t.Fatalf("退出账号应注销一次，实际 %d 次", unregs)
	}
	// 必须广播 system.config（account 段）：网页配置页的「启用账户」勾选
	// 靠它自动取消（用户实测：不广播时勾选不刷新）。
	if !hasAccountConfigEvent(sub) {
		t.Fatal("退出账号后应广播 system.config（section=account），否则网页勾选不刷新")
	}

	if err := app.SetRegistered(true); err != nil {
		t.Fatalf("SetRegistered(true) 失败: %v", err)
	}
	if !app.Config().Account.Enabled {
		t.Fatal("登录账号后「启用账户」应为 true")
	}
	waitReg(t, tel, 1)

	// 幂等：再次 true 不应再发起注册（状态未变）
	if err := app.SetRegistered(true); err != nil {
		t.Fatalf("重复 SetRegistered(true) 失败: %v", err)
	}
	if regs, _ := tel.counts(); regs != 1 {
		t.Fatalf("重复登录不应再次注册，实际注册 %d 次", regs)
	}
}

// TestStartRespectsEnabled 启动注册只由「启用账户」控制（REQ-ACC-024）：
// 未启用时 App.Start 不发起注册；启用时发起。
func TestStartRespectsEnabled(t *testing.T) {
	app, tel := newRegTestApp(t)
	app.Config().Account.Enabled = false
	app.Start(context.Background())
	time.Sleep(100 * time.Millisecond) // 给后台注册 goroutine 一点时间（没有就更好）
	if regs, _ := tel.counts(); regs != 0 {
		t.Fatalf("未启用账户时启动不应注册，实际注册 %d 次", regs)
	}

	app2, tel2 := newRegTestApp(t)
	app2.Start(context.Background())
	waitReg(t, tel2, 1)
}

// hasAccountConfigEvent 非阻塞读取总线事件，判断是否出现
// system.config（section=account）。
func hasAccountConfigEvent(sub <-chan Event) bool {
	for {
		select {
		case ev := <-sub:
			if ev.Name != "system.config" {
				continue
			}
			if m, ok := ev.Data.(map[string]any); ok && m["section"] == "account" {
				return true
			}
		default:
			return false
		}
	}
}

// waitReg 轮询等待注册调用达到 want 次（注册在后台 goroutine 里进行）。
func waitReg(t *testing.T, tel *regTel, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if regs, _ := tel.counts(); regs >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	regs, _ := tel.counts()
	t.Fatalf("等待注册 %d 次超时，实际 %d 次", want, regs)
}

// SendHeartbeat 记录坐席心跳（MSP_HEARTBEAT）。
func (r *regTel) SendHeartbeat(agentMode string, isBusy bool, displayName, status string, acw, leave bool, expires time.Duration) error {
	r.hbMu.Lock()
	defer r.hbMu.Unlock()
	r.hbs++
	r.hbMode = agentMode
	r.hbBusy = isBusy
	r.hbExpires = expires
	r.hbName = displayName
	r.hbStatus = status
	return r.hbErr
}

// heartbeat 返回心跳次数与最近一次的内容。
func (r *regTel) heartbeat() (int, string, bool, time.Duration, string) {
	r.hbMu.Lock()
	defer r.hbMu.Unlock()
	return r.hbs, r.hbMode, r.hbBusy, r.hbExpires, r.hbName
}

// heartbeatStatus 最近一次心跳上报的话机状态（status 字段）。
func (r *regTel) heartbeatStatus() string {
	r.hbMu.Lock()
	defer r.hbMu.Unlock()
	return r.hbStatus
}
