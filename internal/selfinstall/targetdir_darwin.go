//go:build darwin

package selfinstall

import (
	"os"
	"path/filepath"
)

// TargetDir macOS 的固定数据目录：~/Library/Application Support/mti-sip-phone。
// os.UserConfigDir 在 macOS 上即该路径，与配置目录（config.ConfigDir）一致——
// 一个应用一个文件夹，自安装的 .app 包也放这里。
func TargetDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mti-sip-phone"), nil
}
