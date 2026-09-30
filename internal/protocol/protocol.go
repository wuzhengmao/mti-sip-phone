// Package protocol 实现 sip:// 自定义协议：注册系统协议处理器并解析 URI。
//
// 网页通过 sip://account:password@host:port 拉起软电话，携带注册信息。
//   - Windows：写 HKCU\Software\Classes（sip 方案 + ProgID + Capabilities 声明），
//     成为「默认应用」候选；默认处理器由用户在系统设置中手动选择（防劫持机制）
//   - Linux：写 ~/.local/share/applications 下的 .desktop 文件 + mimeapps.list
package protocol

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"

	"mti-sip-phone/internal/logx"
)

// Scheme 协议名。
const Scheme = "sip"

// Windows 注册表常量：ProgID、"默认应用"注册名与 Capabilities 声明路径。
const (
	progIDSIP         = "mti-sip-phone.sip"                   // sip:// 处理器 ProgID
	appRegistryName   = "mti-sip-phone"                       // RegisteredApplications 应用名
	capabilitiesPath  = `Software\mti-sip-phone\Capabilities` // Capabilities 声明路径
)

// Account 从 sip:// URI 解析出的注册信息。
type Account struct {
	Server   string
	Port     int
	Account  string
	Password string
	Transport string // udp / tcp，URI 查询参数 transport 指定
}

// ParseSipURL 解析 sip:// URI：sip://account:password@host:port?transport=tcp
// 账号与密码支持 URL 编码。
func ParseSipURL(raw string) (*Account, error) {
	if !strings.HasPrefix(strings.ToLower(raw), "sip://") {
		return nil, fmt.Errorf("不是合法的 sip:// 链接")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("链接解析失败: %w", err)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("缺少服务器地址")
	}
	acc := &Account{
		Server:  u.Hostname(),
		Port:    5060,
		Account: u.User.Username(),
	}
	if pwd, ok := u.User.Password(); ok {
		acc.Password = pwd
	}
	if port := u.Port(); port != "" {
		if p, err := strconv.Atoi(port); err == nil && p > 0 && p < 65536 {
			acc.Port = p
		}
	}
	if tr := u.Query().Get("transport"); tr != "" {
		tr = strings.ToLower(tr)
		if tr == "udp" || tr == "tcp" {
			acc.Transport = tr
		}
	}
	if acc.Transport == "" {
		acc.Transport = "udp"
	}
	if acc.Account == "" {
		return nil, fmt.Errorf("缺少账号（格式应为 sip://账号:密码@服务器:端口）")
	}
	return acc, nil
}

// RegisterProtocol 注册系统协议处理器。exePath 为当前可执行文件路径。
// 重复调用幂等（覆盖写入）。
func RegisterProtocol(exePath string) error {
	switch runtime.GOOS {
	case "windows":
		return registerWindows(exePath)
	case "linux":
		return registerLinux(exePath)
	default:
		return fmt.Errorf("暂不支持的系统: %s", runtime.GOOS)
	}
}

// registerWindows 按 Windows「默认应用」规范写 HKCU 注册表（无需管理员权限）：
//
//  1. ProgID mti-sip-phone.sip：显示名 + 图标 + 打开命令
//  2. sip 方案指向 ProgID（URL Protocol 标记 + 内联命令兜底）
//  3. Capabilities\UrlAssociations 与 RegisteredApplications 声明，
//     使 mti-sip-phone 出现在「设置 → 默认应用 → 按协议选择默认值 → SIP」候选列表
//
// Windows 8 起默认处理器只能由用户在系统设置中手动选择（UserChoice 防劫持机制），
// 程序不做自动抢占；若 sip:// 被其他程序（如 linphone）占用，用户在
// 「设置 → 默认应用 → 按协议选择默认值 → SIP」中选择 mti-sip-phone 一次即可。
func registerWindows(exePath string) error {
	// 1. ProgID：HKCU\Software\Classes\mti-sip-phone.sip
	pk, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+progIDSIP, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("写 ProgID 注册表失败: %w", err)
	}
	_ = pk.SetStringValue("", "mti-sip-phone SIP 处理器")
	pk.Close()

	iconKey, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+progIDSIP+`\DefaultIcon`, registry.SET_VALUE)
	if err == nil {
		_ = iconKey.SetStringValue("", fmt.Sprintf(`"%s",0`, exePath))
		iconKey.Close()
	}

	progCmdKey, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+progIDSIP+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("写 ProgID 命令注册表失败: %w", err)
	}
	if err := progCmdKey.SetStringValue("", fmt.Sprintf(`"%s" "%%1"`, exePath)); err != nil {
		return err
	}
	progCmdKey.Close()

	// 2. sip 方案：HKCU\Software\Classes\sip，默认值指向 ProgID
	sk, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+Scheme, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("写注册表失败: %w", err)
	}
	// URL Protocol 必须存在且值为空字符串，否则 handler 不会启动
	if err := sk.SetStringValue("URL Protocol", ""); err != nil {
		return err
	}
	if err := sk.SetStringValue("", progIDSIP); err != nil {
		return err
	}
	// 内联命令兜底：ProgID 解析失败时仍可启动
	cmdKey, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+Scheme+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("写命令注册表失败: %w", err)
	}
	if err := cmdKey.SetStringValue("", fmt.Sprintf(`"%s" "%%1"`, exePath)); err != nil {
		return err
	}
	cmdKey.Close()
	sk.Close()

	// 3. Capabilities + RegisteredApplications（「默认应用」候选声明）
	capKey, _, err := registry.CreateKey(registry.CURRENT_USER, capabilitiesPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("写 Capabilities 注册表失败: %w", err)
	}
	_ = capKey.SetStringValue("ApplicationName", appRegistryName)
	_ = capKey.SetStringValue("ApplicationDescription", "mti-sip-phone SIP 软电话")
	capKey.Close()

	assocKey, _, err := registry.CreateKey(registry.CURRENT_USER, capabilitiesPath+`\UrlAssociations`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("写 UrlAssociations 注册表失败: %w", err)
	}
	if err := assocKey.SetStringValue(Scheme, progIDSIP); err != nil {
		return err
	}
	assocKey.Close()

	appsKey, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\RegisteredApplications`, registry.SET_VALUE)
	if err == nil {
		_ = appsKey.SetStringValue(appRegistryName, capabilitiesPath)
		appsKey.Close()
	}

	// 通知 shell 刷新协议关联
	refreshShellAssociations()
	logx.Logger().Info("已注册 sip:// 协议（HKCU）", "progID", progIDSIP)
	return nil
}

// registerLinux 写 .desktop 文件并更新 mimeapps.list。
func registerLinux(exePath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	appsDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return err
	}

	desktop := filepath.Join(appsDir, "mti-sip-phone.desktop")
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=mti-sip-phone
Comment=SIP 软电话
Exec=%s %%u
Terminal=false
Categories=Network;Telephony;
MimeType=x-scheme-handler/%s;x-scheme-handler/%ss;
`, exePath, Scheme, Scheme)
	if err := os.WriteFile(desktop, []byte(content), 0o755); err != nil {
		return fmt.Errorf("写 .desktop 失败: %w", err)
	}

	// mimeapps.list 默认应用（优先级高于 xdg-mime default）
	mimeFile := filepath.Join(home, ".config", "mimeapps.list")
	line := fmt.Sprintf("x-scheme-handler/%s=mti-sip-phone.desktop\n", Scheme)
	if err := upsertLine(mimeFile, "[Default Applications]", line); err != nil {
		return err
	}

	// 更新桌面数据库
	_ = exec.Command("update-desktop-database", appsDir).Run()
	logx.Logger().Info("已注册 sip:// 协议（.desktop）", "file", desktop)
	return nil
}

// upsertLine 在指定 section 下更新一行（存在则替换，不存在则插入 section 末尾）。
func upsertLine(path, section, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")

	key := strings.SplitN(line, "=", 2)[0]
	inSection := false
	insertAt := -1
	found := -1

	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inSection && insertAt == -1 {
				insertAt = i // section 结束位置
			}
			inSection = trimmed == section
			continue
		}
		if inSection {
			if strings.HasPrefix(strings.TrimSpace(l), key+"=") {
				found = i
				break
			}
			if insertAt == -1 && trimmed != "" {
				// 记录 section 内最后一行之后的位置
			}
		}
	}

	if found >= 0 {
		lines[found] = strings.TrimSpace(line)
	} else {
		// 找 section 的最后一行（或 section 头）
		lastSectionLine := -1
		inSection = false
		for i, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				if inSection {
					lastSectionLine = i - 1
					break
				}
				inSection = trimmed == section
				if inSection {
					lastSectionLine = i
				}
				continue
			}
			if inSection {
				lastSectionLine = i
			}
		}
		if lastSectionLine < 0 {
			// section 不存在：追加
			lines = append(lines, "", section, line)
		} else {
			lines = append(lines[:lastSectionLine+1], append([]string{strings.TrimSpace(line)}, lines[lastSectionLine+1:]...)...)
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}
