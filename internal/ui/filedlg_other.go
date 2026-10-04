//go:build !windows && !linux

package ui

// 其他平台没有系统文件选择器：返回不可用，由调用方回退到 Fyne 内置对话框。

// showSystemFileOpenDialog 选择要打开的文件。
func showSystemFileOpenDialog(title string) (string, fileDialogResult) {
	return "", fileDialogUnavailable
}

// showSystemFileSaveDialog 选择保存位置。
func showSystemFileSaveDialog(title, defaultName string) (string, fileDialogResult) {
	return "", fileDialogUnavailable
}
