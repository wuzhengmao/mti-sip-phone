//go:build linux

package ui

import (
	"errors"
	"os/exec"
	"strings"
)

// Linux 的文件打开/保存对话框：依次尝试桌面环境常见的选择器
// （zenity / kdialog / yad）——与目录选择（folderdlg_linux.go）同一套路。
// 都不可用时返回 fileDialogUnavailable，由调用方回退到 Fyne 内置对话框；
// 用户取消（这些程序的退出码 1）返回 fileDialogCancelled。

// csvFilter zenity/yad 的文件过滤器（只列 CSV）。
const csvFilter = "CSV 文件 | *.csv"

// showSystemFileOpenDialog 选择要打开的文件。
func showSystemFileOpenDialog(title string) (string, fileDialogResult) {
	cands := []struct {
		bin  string
		args []string
	}{
		{"zenity", []string{"--file-selection", "--title=" + title, "--file-filter=" + csvFilter}},
		{"kdialog", []string{"--title", title, "--getopenfilename", ".", "CSV 文件 (*.csv)"}},
		{"yad", []string{"--file-selection", "--title=" + title, "--file-filter=" + csvFilter}},
	}
	return runFileDialog(cands)
}

// showSystemFileSaveDialog 选择保存位置（默认文件名 defaultName）。
func showSystemFileSaveDialog(title, defaultName string) (string, fileDialogResult) {
	cands := []struct {
		bin  string
		args []string
	}{
		{"zenity", []string{"--file-selection", "--save", "--confirm-overwrite",
			"--title=" + title, "--file-filter=" + csvFilter, "--filename=" + defaultName}},
		{"kdialog", []string{"--title", title, "--getsavefilename", defaultName, "CSV 文件 (*.csv)"}},
		{"yad", []string{"--file-selection", "--save",
			"--title=" + title, "--file-filter=" + csvFilter, "--filename=" + defaultName}},
	}
	return runFileDialog(cands)
}

// runFileDialog 依次尝试候选程序，返回第一个可用的结果。
func runFileDialog(cands []struct {
	bin  string
	args []string
}) (string, fileDialogResult) {
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
