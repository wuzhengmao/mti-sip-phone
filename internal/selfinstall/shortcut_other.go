//go:build linux

package selfinstall

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mti-sip-phone/internal/appicon"
)

// createDesktopShortcut 在用户桌面与**应用菜单**（~/.local/share/applications）
// 各写一份指向 dst 的 .desktop 快捷方式（覆盖同名旧项）。Linux 的 ELF 二进制
// 不能内嵌图标——图标靠 .desktop 的 Icon 字段体现，所以把绿色话机（256px
// PNG）写到 exe 旁供 Icon= 引用。桌面目录按 resolveDesktopDir 解析（本地化
// 桌面目录如「~/桌面」也能命中），取不到只跳过桌面那份、应用菜单那份照写
// （GNOME 等桌面默认关闭桌面图标、且本地化目录可能对不上，菜单入口最可靠）。
// 桌面那份写完后尽力执行 `gio set metadata::trusted true`（GNOME 43+ 不信任
// 的 .desktop 双击不执行、图标也显示成通用齿轮；gio 缺失或失败静默忽略）。
// 失败由调用方只告警不阻断。快捷方式名固定为「MTI 软电话」（shortcutName，
// 两个变体共用）。
func createDesktopShortcut(dst string) error {
	// 图标 PNG：256px 绿色话机（iconfont 原始渲染，与托盘/窗口同一份）
	iconPath := ""
	if pngBytes := appicon.PhoneGreenPNG(); len(pngBytes) > 0 {
		p := filepath.Join(filepath.Dir(dst), "icon.png")
		if err := os.WriteFile(p, pngBytes, 0o644); err == nil {
			iconPath = p
		}
	}

	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + shortcutName + "\n" +
		"Exec=\"" + desktopQuote(dst) + "\"\n"
	if iconPath != "" {
		content += "Icon=" + iconPath + "\n"
	}
	content += "Terminal=false\n" +
		"Categories=Network;Telephony;\n"

	// 应用菜单入口（不受桌面图标开关/本地化目录影响）
	if appsDir := appsMenuDir(); appsDir != "" {
		menuPath := filepath.Join(appsDir, shortcutName+".desktop")
		if err := os.WriteFile(menuPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("写入应用菜单 .desktop 失败: %w", err)
		}
	}

	// 桌面快捷方式（桌面目录取不到就跳过，不随便建目录）
	if desk := resolveDesktopDir(); desk != "" {
		path := filepath.Join(desk, shortcutName+".desktop")
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			return fmt.Errorf("写入桌面 .desktop 文件失败: %w", err)
		}
		markDesktopTrusted(path)
	}
	return nil
}

// appsMenuDir 应用菜单目录：$XDG_DATA_HOME 或 ~/.local/share 下的 applications/
// （取不到主目录返回空串）。
func appsMenuDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "applications")
}

// markDesktopTrusted 尽力把 .desktop 标记为受信任（GNOME 43+ 的 Nautilus 对
// 未标记的桌面快捷方式：双击弹「不受信任的启动器」、图标显示为通用齿轮）。
// gio 缺失/失败静默忽略——不影响其它桌面环境。
func markDesktopTrusted(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "gio", "set", path, "metadata::trusted", "true").Run()
}

// desktopQuote 按 .desktop 规范转义 Exec 值（\、"、`、$ 加反斜杠前缀）。
func desktopQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", "\\$")
	return r.Replace(s)
}
