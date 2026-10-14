//go:build !windows

package selfinstall

import "syscall"

// detachAttr Unix 侧脱离父进程会话（setsid），避免随父进程/终端退出被终止。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
