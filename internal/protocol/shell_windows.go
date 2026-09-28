//go:build windows

package protocol

import "syscall"

// refreshShellAssociations 通知 Windows shell 协议关联已变化（SHChangeNotify）。
// x/sys/windows 未导出该函数，这里直接通过 syscall 调用 shell32.dll。
func refreshShellAssociations() {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("SHChangeNotify")
	// SHCNE_ASSOCCHANGED = 0x08000000, SHCNF_IDLIST = 0x0000
	const shcneAssocChanged = 0x08000000
	proc.Call(uintptr(shcneAssocChanged), 0, 0, 0)
}
