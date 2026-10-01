//go:build !windows

package ui

// showSystemFolderDialog 非 Windows 平台：没有内置的系统目录选择器
// （Linux 需要 zenity/kdialog 等外部程序），返回 ok=false 由调用方
// 回退到 Fyne 内置对话框。
func showSystemFolderDialog(title, initial string) (string, folderDialogResult) {
	return "", folderDialogUnavailable
}
