//go:build windows

package selfinstall

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// createDesktopShortcut 在用户桌面创建指向 dst 的快捷方式（.lnk，覆盖同名旧项）。
// 经 PowerShell 的 WScript.Shell COM 生成（Windows 10+ 恒有 PowerShell）：
// 桌面目录解析（OneDrive 重定向等）放脚本里做，避免在 Go 侧调
// SHGetKnownFolderPath；整体 10 秒超时，失败由调用方只告警不阻断。
// 快捷方式名固定为「MTI 软电话」（shortcutName，两个变体共用）。
func createDesktopShortcut(dst string) error {
	// PowerShell 单引号字符串：' 翻倍转义；Desktop 目录与 .lnk 名称都在脚本内拼接
	script := fmt.Sprintf(
		`$d=[Environment]::GetFolderPath('Desktop');`+
			`$s=(New-Object -ComObject WScript.Shell).CreateShortcut((Join-Path $d '%s.lnk'));`+
			`$s.TargetPath='%s';$s.WorkingDirectory='%s';$s.Save()`,
		psQuote(shortcutName), psQuote(dst), psQuote(filepath.Dir(dst)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-WindowStyle", "Hidden", "-Command", script)
	// GUI 子系统进程拉起 powershell 也会建控制台窗口：HideWindow 防止闪窗
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("生成桌面快捷方式失败: %w（输出: %s）", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// psQuote 转义 PowerShell 单引号字符串（' 翻倍）。
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }
