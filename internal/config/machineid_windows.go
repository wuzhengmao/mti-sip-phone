//go:build windows

package config

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// windowsMachineID 读取 Windows 注册表 MachineGuid 作为本机标识。
func windowsMachineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	id, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}
