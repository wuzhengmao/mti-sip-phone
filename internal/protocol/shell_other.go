//go:build !windows

package protocol

// refreshShellAssociations Linux 上由 update-desktop-database 完成，无需额外刷新。
func refreshShellAssociations() {}
