//go:build darwin

package selfinstall

import (
	"path/filepath"
	"strings"

	"mti-sip-phone/internal/macbundle"
	"mti-sip-phone/internal/variant"
)

// installAppBundle macOS 的安装形态：建一个 .app 应用包，把可执行文件复制进
// Contents/MacOS/，写 Info.plist（含 sip/sips URL scheme）与图标。返回包内
// 可执行文件路径（用它拉起新进程，进程就带着应用包身份运行，Dock/菜单栏图标
// 与协议注册都成立）。
func installAppBundle(exeSrc, targetDir string) (string, error) {
	root := filepath.Join(targetDir, macbundle.AppName(variant.NoUI))
	if err := macbundle.Ensure(root, exeSrc, variant.NoUI); err != nil {
		return "", err
	}
	return macbundle.ExecutablePath(root, variant.NoUI), nil
}

// inInstalledAppBundle 已在稳定的 .app 包内：/Applications、固定数据目录、
// 用户手工放的任意位置都算正式安装。例外两处（都不算，仍要装到固定目录）：
//   - /Volumes/…：DMG 挂载点，推出后就没了；
//   - …/AppTranslocation/…：从 DMG/下载目录直接运行被系统"搬迁"到随机只读路径。
func inInstalledAppBundle(path string) bool {
	if _, ok := macbundle.RootOf(path); !ok {
		return false
	}
	p := filepath.ToSlash(path)
	if strings.HasPrefix(p, "/Volumes/") {
		return false
	}
	return !strings.Contains(p, "/AppTranslocation/")
}
