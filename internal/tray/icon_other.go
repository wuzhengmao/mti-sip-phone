//go:build !windows

package tray

import "image"

// encodeIcon 非 Windows 平台（Linux 的 StatusNotifier / macOS）用 PNG：
// fyne.io/systray 的 unix 实现只 import 了 image/png 解码器。
func encodeIcon(img image.Image) ([]byte, error) {
	return encodePNG(img)
}
