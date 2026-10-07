//go:build headless && !noui

package main

import (
	"context"

	"mti-sip-phone/internal/audio"
	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

// runGUI headless 构建不含界面层（供纯 Go 交叉编译，无 CGO 依赖）。
// 该构建只支持 -headless 运行模式（本地 WS 服务），runGUI 不会被
// 调用；此函数仅为满足编译。
//
// 参数表必须与 gui.go 的 !headless 版本逐一对齐（含最后的实例槽位 slot）——
// 多实例那次改动漏了这个桩，纯 Go 构建（-tags "headless noaudio"）直接编不过。
func runGUI(app *core.App, audioMgr *audio.Manager, cfg *config.Config, cfgPath string, ctx context.Context, restart func() error, slot int) {
	panic("headless 构建不含界面层")
}
