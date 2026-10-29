//go:build darwin && cgo

package macurl

/*
#cgo darwin LDFLAGS: -framework Cocoa -framework CoreServices
#include <stdlib.h>

void mtiInstallAppleEventHandler(void);
*/
import "C"

import (
	"sync"

	"mti-sip-phone/internal/logx"
)

var state struct {
	mu      sync.Mutex
	handler func(string)
	pending []string
}

//export mtiGoHandleURL
func mtiGoHandleURL(c *C.char) {
	if c == nil {
		return
	}
	dispatch(C.GoString(c))
}

// dispatch 把 URL 交给回调；回调还没接上（核心尚未组装好）时先缓存，
// SetHandler 时补发。事件在主线程回调进来，这里不能做耗时操作。
func dispatch(url string) {
	state.mu.Lock()
	fn := state.handler
	if fn == nil {
		if len(state.pending) < 8 {
			state.pending = append(state.pending, url)
		} else {
			logx.Logger().Warn("sip:// 链接接收队列已满，丢弃", "url", url)
		}
		state.mu.Unlock()
		return
	}
	state.mu.Unlock()
	fn(url)
}

func start() {
	C.mtiInstallAppleEventHandler()
	logx.Logger().Debug("已注册 macOS 苹果事件处理器（sip:// / sips://）")
}

// SetHandler 接上 URL 回调；Start 之后先到的事件按序补发（在调用方 goroutine
// 里同步回调，回调里要自己做异步）。
func SetHandler(fn func(string)) {
	state.mu.Lock()
	state.handler = fn
	pending := state.pending
	state.pending = nil
	state.mu.Unlock()
	for _, u := range pending {
		fn(u)
	}
}
