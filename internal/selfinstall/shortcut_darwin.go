//go:build darwin

package selfinstall

import (
	"fmt"
	"os"
	"path/filepath"

	"mti-sip-phone/internal/macbundle"
)

// createDesktopShortcut macOS：在桌面放一个指向 .app 包的**符号链接**。
// Finder 会按应用包显示图标（带小箭头），双击即启动；删除链接不影响应用本体
// （仍在固定数据目录里，自更新替换的也是那一份）。
// 没有桌面目录（服务器/受限账户）时静默跳过，不随便建目录。
func createDesktopShortcut(dst string) error {
	root, ok := macbundle.RootOf(dst)
	if !ok {
		return fmt.Errorf("可执行文件不在 .app 包内，无法生成桌面快捷方式")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	desk := filepath.Join(home, "Desktop")
	if fi, err := os.Stat(desk); err != nil || !fi.IsDir() {
		return nil
	}
	link := filepath.Join(desk, filepath.Base(root))
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			// 桌面上已有同名真实文件/目录：不覆盖用户的文件
			return fmt.Errorf("桌面已有同名项且不是符号链接，跳过: %s", link)
		}
		if target, err := os.Readlink(link); err == nil && target == root {
			return nil // 已指向同一个包
		}
		_ = os.Remove(link)
	}
	if err := os.Symlink(root, link); err != nil {
		return fmt.Errorf("创建桌面链接失败: %w", err)
	}
	return nil
}
