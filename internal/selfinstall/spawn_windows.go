//go:build windows

package selfinstall

import "syscall"

// detachAttr Windows 侧无需额外脱离设置（与 cmd/sip-phone 重启同口径）。
func detachAttr() *syscall.SysProcAttr { return nil }
