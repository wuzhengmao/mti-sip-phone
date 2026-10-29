// Package macbundle macOS 应用包（.app）的固定安装形态：目录结构、Info.plist、
// 图标，以及向 LaunchServices 注册（含 sip:// / sips:// 协议处理器）。
//
// 为什么需要它：macOS 上**裸二进制**既没有程序图标、也无法注册自定义协议——
// 这两样都由 LaunchServices 从 `Xxx.app/Contents/Info.plist` 读取。所以
// Linux 的「把二进制复制到固定目录 + 桌面快捷方式」在 macOS 上落成「建一个
// .app 包」：
//
//	<固定目录>/MTI软电话.app/Contents/MacOS/mti-sip-phone-no-ui
//	<固定目录>/MTI软电话.app/Contents/Info.plist        （含 CFBundleURLTypes）
//	<固定目录>/MTI软电话.app/Contents/Resources/AppIcon.icns
//
// 打包脚本 scripts/package-macos.sh 产出的 DMG 里是同一套结构（同样的 bundle
// 标识、图标文件名与 Info.plist 字段），两条安装路径行为一致。
package macbundle

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mti-sip-phone/internal/appicon"
	"mti-sip-phone/internal/core"
)

// Bundle 标识：与 scripts/package-macos.sh 保持一致。
// 两个变体是替代关系（共享配置目录），但 bundle 分开——否则后安装的会
// 覆盖前一个的图标与协议注册。
const (
	bundleIDFull = "cn.mti.sip-phone"
	bundleIDNoUI = "cn.mti.sip-phone-no-ui"

	displayFull = "MTI软电话-完整版"
	displayNoUI = "MTI软电话"

	exeFull = "mti-sip-phone"
	exeNoUI = "mti-sip-phone-no-ui"

	iconFileName = "AppIcon.icns"
	// plistIconName 不带扩展名：CFBundleIconFile 的口径。
	plistIconName = "AppIcon"
)

// DisplayName bundle 显示名（Finder 与 Dock 里看到的名字）。
func DisplayName(noUI bool) string {
	if noUI {
		return displayNoUI
	}
	return displayFull
}

// AppName bundle 目录名（「MTI软电话.app」）。
func AppName(noUI bool) string { return DisplayName(noUI) + ".app" }

// ExecutableName bundle 内可执行文件名（不含架构后缀）。
func ExecutableName(noUI bool) string {
	if noUI {
		return exeNoUI
	}
	return exeFull
}

// BundleID 本变体的 bundle 标识。
func BundleID(noUI bool) string {
	if noUI {
		return bundleIDNoUI
	}
	return bundleIDFull
}

// ExecutablePath bundle 内可执行文件的完整路径。
func ExecutablePath(root string, noUI bool) string {
	return filepath.Join(root, "Contents", "MacOS", ExecutableName(noUI))
}

// InfoPlistPath bundle 的 Info.plist 路径。
func InfoPlistPath(root string) string { return filepath.Join(root, "Contents", "Info.plist") }

// HasOwnPlist 判断应用包里已有**本项目**的 Info.plist（两个变体之一）。
// 用于"只刷新自己的包，不动别人塞进来的应用"。
func HasOwnPlist(root string) bool {
	data, err := os.ReadFile(InfoPlistPath(root))
	if err != nil {
		return false
	}
	s := string(data)
	return strings.Contains(s, "<string>"+bundleIDFull+"</string>") ||
		strings.Contains(s, "<string>"+bundleIDNoUI+"</string>")
}

// NeedsRefresh 应用包是否需要（重新）写入 plist 与图标：
// 没有 plist（裸包）、本项目的旧包缺 CFBundleURLTypes（早期版本没有声明
// sip/sips）、或图标文件丢失。别人的应用包一律返回 false（不去动它）。
func NeedsRefresh(root string) bool {
	data, err := os.ReadFile(InfoPlistPath(root))
	if err != nil {
		return true
	}
	if !HasOwnPlist(root) {
		return false
	}
	if !strings.Contains(string(data), "CFBundleURLTypes") {
		return true
	}
	_, statErr := os.Stat(IconPath(root))
	return statErr != nil
}

// IconPath bundle 的图标文件路径。
func IconPath(root string) string {
	return filepath.Join(root, "Contents", "Resources", iconFileName)
}

// RootOf 从任意路径里找出所在的 *.app 包根（如 /Applications/MTI软电话.app）。
// 不在应用包内（裸二进制等）返回 false。
func RootOf(path string) (string, bool) {
	p := filepath.Clean(path)
	for {
		if strings.HasSuffix(strings.ToLower(filepath.Base(p)), ".app") {
			return p, true
		}
		parent := filepath.Dir(p)
		if parent == p { // 到根了
			return "", false
		}
		p = parent
	}
}

// Ensure 建好（或刷新）一个 .app 包：目录、可执行文件、Info.plist、图标。
// exeSrc 非空时把可执行文件复制到 Contents/MacOS/（与目标同一文件则跳过）。
// 幂等：内容没变就不动文件，避免每次启动都刷新 LaunchServices 的图标缓存。
func Ensure(root, exeSrc string, noUI bool) error {
	macOSDir := filepath.Join(root, "Contents", "MacOS")
	resDir := filepath.Join(root, "Contents", "Resources")
	for _, dir := range []string{macOSDir, resDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建应用包目录失败: %w", err)
		}
	}

	if exeSrc != "" {
		dst := ExecutablePath(root, noUI)
		if !sameFile(exeSrc, dst) {
			if err := copyFile(exeSrc, dst); err != nil {
				return err
			}
		}
	}

	plist := renderPlist(noUI)
	if err := writeIfChanged(InfoPlistPath(root), []byte(plist), 0o644); err != nil {
		return fmt.Errorf("写入 Info.plist 失败: %w", err)
	}

	icns, err := appicon.ICNS(appicon.PhoneGreenPNG())
	if err != nil {
		return fmt.Errorf("生成应用包图标失败: %w", err)
	}
	if err := writeIfChanged(IconPath(root), icns, 0o644); err != nil {
		return fmt.Errorf("写入应用包图标失败: %w", err)
	}
	return nil
}

// Register 让 LaunchServices 立刻收录这个应用包（新装的包不收录时，点击
// sip:// 链接找不到刚装好的处理器）。lsregister 是系统自带工具。
func Register(root string) error {
	const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	if _, err := os.Stat(lsregister); err != nil {
		return fmt.Errorf("未找到 lsregister（系统版本异常）: %w", err)
	}
	out, err := exec.Command(lsregister, "-f", root).CombinedOutput()
	if err != nil {
		return fmt.Errorf("lsregister 注册失败: %w（输出: %s）", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetDefaultURLHandler 把 sip:// 与 sips:// 的默认处理器设成本应用（按 bundle
// 标识）。失败只说明系统要求用户确认，不影响应用已作为处理器被收录。
func SetDefaultURLHandler(bundleID string) error {
	if err := setDefaultHandler("sip", bundleID); err != nil {
		return err
	}
	return setDefaultHandler("sips", bundleID)
}

// renderPlist 生成 Info.plist。字段与 scripts/package-macos.sh 写出的保持一致。
func renderPlist(noUI bool) string {
	luiElement := "false"
	if noUI {
		luiElement = "true" // 仅托盘无 UI：不占 Dock
	}
	name := DisplayName(noUI)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>zh-Hans</string>
	<key>CFBundleExecutable</key>
	<string>%s</string>
	<key>CFBundleIconFile</key>
	<string>%s</string>
	<key>CFBundleIdentifier</key>
	<string>%s</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>%s</string>
	<key>CFBundleDisplayName</key>
	<string>%s</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>%s</string>
	<key>CFBundleVersion</key>
	<string>%s</string>
	<key>LSMinimumSystemVersion</key>
	<string>12.0</string>
	<key>LSUIElement</key>
	<%s/>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSMicrophoneUsageDescription</key>
	<string>MTI 软电话需要使用麦克风进行语音通话。</string>
	<key>NSCameraUsageDescription</key>
	<string>MTI 软电话需要使用摄像头进行视频通话（若已启用视频）。</string>
	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLName</key>
			<string>%s</string>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>sip</string>
				<string>sips</string>
			</array>
		</dict>
	</array>
</dict>
</plist>
`, ExecutableName(noUI), plistIconName, BundleID(noUI), name, name,
		core.Version, plistBuildVersion(), luiElement, BundleID(noUI))
}

// plistBuildVersion CFBundleVersion 只接受数字与点（Apple 的口径），
// 开发版版本号带 -rc，这里剥掉后缀只留数字段。
func plistBuildVersion() string {
	var b strings.Builder
	for _, r := range core.Version {
		if (r >= '0' && r <= '9') || r == '.' {
			b.WriteRune(r)
		} else {
			break
		}
	}
	v := strings.Trim(b.String(), ".")
	if v == "" {
		return "1"
	}
	return v
}

// sameFile 判断两个路径是否指向同一个文件（不存在时为 false）。
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// copyFile 复制可执行文件（临时文件 + rename，保留执行位）。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("读取可执行文件失败: %w", err)
	}
	tmp := dst + ".tmp"
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return fmt.Errorf("写入可执行文件失败: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("就位可执行文件失败: %w", err)
	}
	return nil
}

// writeIfChanged 内容一致时不动文件（LaunchServices 按 mtime 缓存图标与
// plist，每次启动都重写会让图标缓存反复失效）。
func writeIfChanged(path string, data []byte, perm os.FileMode) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	return os.WriteFile(path, data, perm)
}
