//go:build !windows && !darwin

package selfinstall

import (
	"os"
	"path/filepath"
)

// TargetDir 其它平台的固定数据目录：$XDG_DATA_HOME 或 ~/.local/share 下的
// mti-sip-phone。配置在 ~/.config，二进制按 XDG 规范放数据目录。
func TargetDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "mti-sip-phone"), nil
}
