//go:build !windows

package logx

// stderrUsable 非 Windows 平台恒认为 stderr 有去处（终端、systemd journal、
// 桌面的 .desktop 启动都能收），**不重定向**——控制台输出是这些平台的正常
// 排查手段，不能被抢走。
func stderrUsable() bool { return true }
