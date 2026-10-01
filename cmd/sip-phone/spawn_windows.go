//go:build windows

package main

import "syscall"

// detachSysProcAttr Windows 侧无需额外脱离设置（实测新进程可正常独立运行），
// 保持不加参数的默认行为。
func detachSysProcAttr() *syscall.SysProcAttr {
	return nil
}
