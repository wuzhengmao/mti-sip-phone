package sip

import (
	"context"
	"strings"
	"testing"

	"mti-sip-phone/internal/config"
)

// TestRoundRobinRegistrar 各服务器对等（REQ-ACC-038）：out-of-dialog 出站从
// **已注册**目标里轮询选择；只注册一个时恒选它。
func TestRoundRobinRegistrar(t *testing.T) {
	reg := func(server string) config.Account {
		return config.Account{Server: server, Port: 5060, Account: "1001"}
	}
	key := func(server string) string {
		return config.RegistrarKey(reg(server))
	}

	// 两个都注册成功 → 轮询交替
	p := &Phone{
		acc:        reg("10.0.0.1"),
		registrars: []config.Account{reg("10.0.0.1"), reg("10.0.0.2")},
		regs: map[string]regResult{
			key("10.0.0.1"): {ok: true},
			key("10.0.0.2"): {ok: true},
		},
	}
	for i, want := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.1"} {
		got, ok := p.roundRobinRegistrar()
		if !ok || got.Server != want {
			t.Fatalf("第 %d 轮应选 %s，实际 %s（ok=%v）", i+1, want, got.Server, ok)
		}
	}

	// 只有 10.0.0.1 注册成功 → 恒选它
	p2 := &Phone{
		acc:        reg("10.0.0.1"),
		registrars: []config.Account{reg("10.0.0.1"), reg("10.0.0.2")},
		regs: map[string]regResult{
			key("10.0.0.1"): {ok: true},
			key("10.0.0.2"): {ok: false},
		},
	}
	for i := 0; i < 3; i++ {
		got, ok := p2.roundRobinRegistrar()
		if !ok || got.Server != "10.0.0.1" {
			t.Fatalf("仅 10.0.0.1 注册时应恒选它，实际 %s（ok=%v）", got.Server, ok)
		}
	}

	// 都还没注册成功 → 不能出站：返回 ok=false
	p3 := &Phone{
		acc:        reg("10.0.0.1"),
		registrars: []config.Account{reg("10.0.0.1"), reg("10.0.0.2")},
		regs:       map[string]regResult{},
	}
	if _, ok := p3.roundRobinRegistrar(); ok {
		t.Fatal("尚无任何服务器注册成功时，roundRobinRegistrar 应返回 ok=false")
	}
}

// TestRoundRobinPreferPrimary 「优先使用主节点」（account.preferPrimary）：
// 主节点已注册 → 恒走主节点；主节点未注册 → 只在已注册的备用节点里轮询。
func TestRoundRobinPreferPrimary(t *testing.T) {
	reg := func(server string) config.Account {
		return config.Account{Server: server, Port: 5060, Account: "1001"}
	}
	key := func(server string) string {
		return config.RegistrarKey(reg(server))
	}

	// 主节点 + 两个备用节点都注册 → 恒选主节点
	p := &Phone{
		acc:        config.Account{Server: "10.0.0.1", Port: 5060, Account: "1001", PreferPrimary: true},
		registrars: []config.Account{reg("10.0.0.1"), reg("10.0.0.2"), reg("10.0.0.3")},
		regs: map[string]regResult{
			key("10.0.0.1"): {ok: true},
			key("10.0.0.2"): {ok: true},
			key("10.0.0.3"): {ok: true},
		},
	}
	for i := 0; i < 3; i++ {
		got, ok := p.roundRobinRegistrar()
		if !ok || got.Server != "10.0.0.1" {
			t.Fatalf("主节点已注册时应恒选主节点，实际 %s（ok=%v）", got.Server, ok)
		}
	}

	// 主节点未注册、两个备用都注册 → 在备用之间轮询（不选未注册的主节点）
	p2 := &Phone{
		acc:        config.Account{Server: "10.0.0.1", Port: 5060, Account: "1001", PreferPrimary: true},
		registrars: []config.Account{reg("10.0.0.1"), reg("10.0.0.2"), reg("10.0.0.3")},
		regs: map[string]regResult{
			key("10.0.0.1"): {ok: false},
			key("10.0.0.2"): {ok: true},
			key("10.0.0.3"): {ok: true},
		},
	}
	for i, want := range []string{"10.0.0.2", "10.0.0.3", "10.0.0.2"} {
		got, ok := p2.roundRobinRegistrar()
		if !ok || got.Server != want {
			t.Fatalf("主节点未注册时应轮询备用，第 %d 轮应选 %s，实际 %s（ok=%v）", i+1, want, got.Server, ok)
		}
	}

	// 主节点未注册、只有一个备用注册 → 恒选该备用
	p4 := &Phone{
		acc:        config.Account{Server: "10.0.0.1", Port: 5060, Account: "1001", PreferPrimary: true},
		registrars: []config.Account{reg("10.0.0.1"), reg("10.0.0.2")},
		regs: map[string]regResult{
			key("10.0.0.1"): {ok: false},
			key("10.0.0.2"): {ok: true},
		},
	}
	if got, ok := p4.roundRobinRegistrar(); !ok || got.Server != "10.0.0.2" {
		t.Fatalf("主节点未注册且仅一个备用注册时应选它，实际 %s（ok=%v）", got.Server, ok)
	}
}

// TestDialBusyDuringCall 单通话限制（信令层兜底）：已有通话（呼出/振铃/通话中）
// 时，向任何 SIP 服务器发起新呼叫都直接在电话层被拒、不发第二个 INVITE——
// 与呼入直接回 486 同口径。
func TestDialBusyDuringCall(t *testing.T) {
	p := &Phone{calls: map[string]*callSession{"active": {id: "active"}}}
	_, err := p.dial(context.Background(), "1000", false)
	if err == nil || !strings.Contains(err.Error(), "已有通话") {
		t.Fatalf("话务中应拒绝新呼叫，实际 err=%v", err)
	}

	// 无通话时不应命中 busy 分支（未配置账户时落到「未配置」错误，而非「已有通话」）
	p2 := &Phone{calls: map[string]*callSession{}}
	if _, err := p2.dial(context.Background(), "1000", false); err != nil &&
		strings.Contains(err.Error(), "已有通话") {
		t.Fatalf("无通话时不应报 busy，实际 err=%v", err)
	}
}
