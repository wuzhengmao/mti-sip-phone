//go:build !darwin

package selfinstall

// installAppBundle 非 macOS 平台不走应用包形态（实现见 selfinstall.go 的
// 复制分支）；这个桩只为让 Relocate 的分支在各平台都能编译。
func installAppBundle(exeSrc, targetDir string) (string, error) { return "", nil }

// inInstalledAppBundle 非 macOS 没有 .app 应用包。
func inInstalledAppBundle(string) bool { return false }
