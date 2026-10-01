//go:build !windows

package main

import "syscall"

// detachSysProcAttr 让被拉起的新进程脱离父进程的会话（setsid）：
// 父进程退出、控制终端关闭或进程组收到信号时，新进程不受影响。
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
