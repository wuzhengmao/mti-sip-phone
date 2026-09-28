// Package core 实现应用核心：事件总线、通话状态机与电话能力抽象。
//
// 分层设计：
//
//	GUI / WS 接口层  ←订阅事件、调用方法
//	      ↓
//	   core.App（状态机 + 事件总线）
//	      ↓
//	core.Telephony（抽象接口，由 internal/sip 实现；演示模式用 FakeTelephony）
package core

import (
	"sync"

	"mti-sip-phone/internal/logx"
)

// Event 统一事件结构。Name 为事件名（如 "call.incoming"），Data 为事件负载。
type Event struct {
	Name string
	Data any
}

// Bus 发布-订阅事件总线：所有订阅者收到全部事件，按事件名自行过滤。
// 通道缓冲 64 条；订阅者消费过慢时丢弃并记录告警（避免阻塞主流程）。
type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewBus 创建事件总线。
func NewBus() *Bus {
	return &Bus{subs: make(map[chan Event]struct{})}
}

// Subscribe 订阅事件，返回接收通道；用 Unsubscribe 取消订阅。
func (b *Bus) Subscribe() chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe 取消订阅并关闭通道。
func (b *Bus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
	close(ch)
}

// Publish 发布事件。订阅者缓冲满时丢弃本次事件（事件层允许丢失，状态以查询为准）。
func (b *Bus) Publish(name string, data any) {
	ev := Event{Name: name, Data: data}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			logx.Logger().Warn("事件订阅者消费过慢，丢弃事件", "event", name)
		}
	}
}
