//go:build windows

package main

// startRestartWatchdog Windows 侧不需要看门狗：实测退出收尾能正常拉起
// 新进程（见 spawnNewProcessIfRequested），这里保持空实现。
func startRestartWatchdog(exePath string, args []string) {}
