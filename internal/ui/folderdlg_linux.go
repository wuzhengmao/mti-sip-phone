//go:build linux

package ui

import (
	"errors"
	"os/exec"
	"strings"
)

// showSystemFolderDialog Linux：依次尝试桌面环境常见的系统目录选择器
// （zenity / kdialog / yad）。Fyne 自带的 dialog.FolderOpen 只能内嵌在
// 主窗体画布内（主窗体固定 320px 宽，空间很小）。
// 都不可用时返回 fileDialogUnavailable，由调用方回退到内置对话框；
// 用户取消（这些程序的退出码 1）返回 fileDialogCancelled。
func showSystemFolderDialog(title, initial string) (string, fileDialogResult) {
	type candidate struct {
		bin  string
		args []string
	}
	cands := []candidate{
		{"zenity", []string{"--file-selection", "--directory", "--title=" + title}},
		{"kdialog", []string{"--title", title, "--getexistingdirectory", initial}},
		{"yad", []string{"--file-selection", "--directory", "--title=" + title}},
	}
	if initial != "" {
		// zenity/yad 用 --filename 预选目录
		cands[0].args = append(cands[0].args, "--filename="+strings.TrimSuffix(initial, "/")+"/")
		cands[2].args = append(cands[2].args, "--filename="+strings.TrimSuffix(initial, "/")+"/")
	}
	for _, c := range cands {
		if _, err := exec.LookPath(c.bin); err != nil {
			continue
		}
		out, err := exec.Command(c.bin, c.args...).Output()
		if err == nil {
			if path := strings.TrimSpace(string(out)); path != "" {
				return path, fileDialogPicked
			}
			continue
		}
		// 退出码非 0 = 用户取消（zenity/kdialog/yad 的惯例）
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fileDialogCancelled
		}
		// 其他错误（无法启动等）：换下一个候选
	}
	return "", fileDialogUnavailable
}
