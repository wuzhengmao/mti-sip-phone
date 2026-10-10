//go:build windows

package selfinstall

import (
	"os"
	"path/filepath"
)

// TargetDir Windows 的固定数据目录：%APPDATA%\mti-sip-phone。
// os.UserConfigDir 在 Windows 上即 %APPDATA%，与配置目录 config.ConfigDir()
// 是同一处——一个应用一个文件夹。
func TargetDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mti-sip-phone"), nil
}
