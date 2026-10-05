//go:build windows

package single

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// acquireLock Windows：创建命名互斥体，已存在则说明已有实例在运行。
//
// ⚠️ 互斥体名必须**按锁文件路径派生**：多实例沙箱各槽位用不同的锁文件
// （<配置目录>/instance.lock 与 <配置目录>/instances/N/instance.lock），
// 名字不同才能各拿各的锁。早期是写死的常量，结果是"开了多实例开关、第二个
// 实例照样被判为已有实例在运行"（用户实测）。
func acquireLock(lockPath string) (func(), error) {
	name := `Local\mti-sip-phone-` + lockID(lockPath)
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
