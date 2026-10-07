//go:build windows

package tray

import (
	"bytes"
	"image"

	"github.com/fyne-io/image/ico"
)

// encodeIcon Windows 的 systray 只吃 ICO（fyne.io/systray 的
// SetIcon 文档：iconFilePath should be the path to a .ico for windows），
// 与 Fyne driver 的 toOSIcon 同一口径。
func encodeIcon(img image.Image) ([]byte, error) {
	buf := &bytes.Buffer{}
	if err := ico.Encode(buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
