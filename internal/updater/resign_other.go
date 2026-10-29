//go:build !darwin

package updater

// resignAfterReplace 非 macOS 平台无需重签。
func resignAfterReplace(string) {}
