//go:build !windows

package config

// windowsMachineID 非 Windows 平台无注册表可用（machineID 按
// runtime.GOOS 分发，非 Windows 走 linuxMachineID）。
func windowsMachineID() string {
	return ""
}
