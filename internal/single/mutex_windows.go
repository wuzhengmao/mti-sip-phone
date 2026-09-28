//go:build windows

package single

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// acquireLock Windows：创建命名互斥体，已存在则说明已有实例在运行。
func acquireLock(lockPath string) (func(), error) {
	name := `Local\mti-sip-phone-single-instance`
	handle, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(name))
	// ERROR_ALREADY_EXISTS = 183：已有实例在运行（此时 handle 仍有效，需关闭）
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(handle)
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, fmt.Errorf("创建互斥体失败: %w", err)
	}
	return func() { windows.CloseHandle(handle) }, nil
}
