//go:build windows

package protocol

import (
	"fmt"

	"golang.org/x/sys/windows/registry"

	"mti-sip-phone/internal/logx"
)

// Windows 注册表常量：ProgID、"默认应用"注册名与 Capabilities 声明路径。
const (
	progIDSIP        = "mti-sip-phone.sip"                   // sip:// 处理器 ProgID
	appRegistryName  = "mti-sip-phone"                       // RegisteredApplications 应用名
	capabilitiesPath = `Software\mti-sip-phone\Capabilities` // Capabilities 声明路径
)

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
