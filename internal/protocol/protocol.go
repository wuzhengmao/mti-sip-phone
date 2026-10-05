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

	"mti-sip-phone/internal/logx"
)

// Scheme 协议名。
const Scheme = "sip"

// Account 从 sip:// URI 解析出的注册信息。
type Account struct {
	Server    string
	Port      int
	Account   string
	Password  string
	Transport string // udp / tcp，URI 查询参数 transport 指定
	// Instance 实例槽位（查询参数 instance=N，0 = 主实例）：≥1 时用独立数据目录
	// 启动一个沙箱实例（多实例联调/对比用，见 config.InstanceDir 与
	// config.AllowMultiInstance）。
	Instance int
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
	// 多实例（沙箱槽位）：sip://账号:密码@服务器:端口?instance=2
	// 缺省/0 = 主实例；槽位 ≥1 时由本进程使用独立数据目录启动（见 config.InstanceDir）。
	if inst := u.Query().Get("instance"); inst != "" {
		if n, err := strconv.Atoi(inst); err == nil && n > 0 && n <= 99 {
			acc.Instance = n
		}
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

// registerWindows 的实现在 register_windows.go（构建标签 windows），
// 避免在非 Windows 平台导入 x/sys/windows/registry（该包仅支持
// Windows，导入会导致 Linux 交叉编译失败）。

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
