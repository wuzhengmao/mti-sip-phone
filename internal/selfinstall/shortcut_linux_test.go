//go:build linux

package selfinstall

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateDesktopShortcutLinux 端到端：桌面与应用菜单各一份 .desktop（名称
// 「MTI 软电话」）、Icon 指向 exe 旁的 icon.png、图标 PNG 真实画出像素。
func TestCreateDesktopShortcutLinux(t *testing.T) {
	desk := t.TempDir() // "桌面目录"（真实存在）
	cfgBase := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgBase)
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // 应用菜单目录隔离到临时目录
	if err := os.WriteFile(filepath.Join(cfgBase, "user-dirs.dirs"),
		[]byte("XDG_DESKTOP_DIR=\""+desk+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exeDir := t.TempDir()
	dst := filepath.Join(exeDir, "mti-sip-phone-linux-amd64")
	if err := os.WriteFile(dst, []byte("fake-exe"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := createDesktopShortcut(dst); err != nil {
		t.Fatalf("生成桌面快捷方式失败: %v", err)
	}

	// 桌面与应用菜单两份 .desktop：名称与关键字段
	iconPath := filepath.Join(exeDir, "icon.png")
	for _, path := range []string{
		filepath.Join(desk, shortcutName+".desktop"),
		filepath.Join(os.Getenv("XDG_DATA_HOME"), "applications", shortcutName+".desktop"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf(".desktop 文件不存在（%s）: %v", path, err)
		}
		content := string(data)
		if !strings.Contains(content, "Name="+shortcutName) {
			t.Fatalf(".desktop 缺少 Name=%s：%s", shortcutName, content)
		}
		if !strings.Contains(content, "Exec=\""+dst+"\"") {
			t.Fatalf(".desktop 的 Exec 应指向 %q：%s", dst, content)
		}
		if !strings.Contains(content, "Icon="+iconPath) {
			t.Fatalf(".desktop 的 Icon 应指向 %q：%s", iconPath, content)
		}
	}

	// 图标 PNG 存在、可解码且真的画出了像素
	f, err := os.Open(iconPath)
	if err != nil {
		t.Fatalf("icon.png 不存在: %v", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("icon.png 解码失败: %v", err)
	}
	opaque := 0
	for y := 0; y < img.Bounds().Dy(); y += 8 {
		for x := 0; x < img.Bounds().Dx(); x += 8 {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				opaque++
			}
		}
	}
	if opaque < 100 {
		t.Fatalf("图标几乎全透明（采样不透明像素 %d）", opaque)
	}
}
