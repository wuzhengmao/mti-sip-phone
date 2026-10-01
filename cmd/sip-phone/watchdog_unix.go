//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"mti-sip-phone/internal/logx"
)

// startRestartWatchdog 启动一个脱离会话的看门狗进程（sh）：等待本进程
// 退出（最多 10 秒），若仍存活且确认还是本程序（/proc/<pid>/exe 指向
// 本程序或更新留下的 .old），则强杀，随后拉起新进程。
//
// 用途是兜底：正常路径由退出收尾（spawnNewProcessIfRequested）拉起新进程，
// 但实测某个环境下（Debian X11）更新完成后进程卡在 UI 退出阶段，
// "拉起新进程"那一步永远执行不到，表现为"更新后程序退出但没自动启动"。
// 看门狗与主流程互不依赖：正常退出时它只是多起一个实例（被单实例锁
// 拦截、转发参数后自行退出），卡死时它负责收尾。
func startRestartWatchdog(exePath string, args []string) {
	if exePath == "" {
		return
	}
	pid := os.Getpid()
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, shellQuote(a))
	}
	exe := shellQuote(exePath)
	script := fmt.Sprintf(`for i in $(seq 1 20); do
  kill -0 %d 2>/dev/null || break
  sleep 0.5
done
if kill -0 %d 2>/dev/null; then
  case "$(readlink -f /proc/%d/exe 2>/dev/null)" in
    %s|%s.old) kill -9 %d 2>/dev/null ;;
  esac
  sleep 1
fi
exec %s %s
`, pid, pid, pid, exe, exe, pid, exe, strings.Join(quoted, " "))

	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 脱离会话，父进程退出不影响
	if err := cmd.Start(); err != nil {
		logx.Logger().Warn("启动重启看门狗失败", "err", err)
		return
	}
	logx.Logger().Debug("重启看门狗已启动", "watchdogPid", cmd.Process.Pid)
}

// shellQuote 用单引号包裹参数（POSIX shell 专用）。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
