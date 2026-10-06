//go:build windows

package logx

import "golang.org/x/sys/windows"

// stderrUsable stderr 句柄是否指向一个真的去处（控制台、管道或文件）。
//
// **只在句柄是空的时候才重定向**：托盘版是 GUI 子系统（`-H=windowsgui`），
// 从资源管理器/快捷方式启动时根本没有标准句柄，往 stderr 写等于写进空气；
// 而**被显式重定向过的 stderr（`cmd > 日志 2>&1`）必须原样保留**——那是用户
// 自己要的输出，抢过来写进我们的文件就成了"日志不见了"。控制台版在终端/管道
// 里跑同样落在这里：句柄有效，不重定向。
func stderrUsable() bool {
	h, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err != nil {
		return false
	}
	return h != 0 && h != windows.InvalidHandle
}
