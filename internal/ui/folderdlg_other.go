//go:build !windows && !linux

package ui

// showSystemFolderDialog Windows/Linux 之外的平台：没有系统目录
// 选择器，返回不可用，由调用方回退到 Fyne 内置对话框。
func showSystemFolderDialog(title, initial string) (string, folderDialogResult) {
	return "", folderDialogUnavailable
}
