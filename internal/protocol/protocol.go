// Package protocol 实现 sip:// / sips:// 自定义协议：注册系统协议处理器并解析 URI。
//
// 网页通过 sip://account:password@host:port 拉起软电话，携带注册信息；
// sips://account:password@host:port 表示走 TLS 传输（REQ-ACC-006）。
//   - Windows：写 HKCU\Software\Classes（sip、sips 两个方案 + ProgID + Capabilities
//     声明），成为「默认应用」候选；默认处理器由用户在系统设置中手动选择（防劫持机制）
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

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/logx"
)

// Scheme 协议名。SchemeSips 为 TLS 版本（sips:// ⇒ transport=tls，REQ-ACC-006），
// 两者都注册为系统协议处理器。
const (
	Scheme     = "sip"
	SchemeSips = "sips"
)

// urlPrefixes URI 前缀，**sips 在前**：sips:// 里不含 "sip://" 子串，
// 但两个前缀都要能识别，顺序保证 sips:// 不会被当成 sip:// 之外的东西。
var urlPrefixes = []string{SchemeSips + "://", Scheme + "://"}

// Account 从 sip:// / sips:// URI 解析出的注册信息。
type Account struct {
	Server    string
	Port      int
	Account   string
	Password  string
	Transport string // udp / tcp / tls：sips:// 前缀或查询参数 transport 指定
	// Instance 实例槽位（查询参数 instance=N，0 = 主实例）：≥1 时用独立数据目录
	// 启动一个沙箱实例（多实例联调/对比用，见 config.InstanceDir 与
	// config.AllowMultiInstance）。
	Instance int
	// UpdateSite 更新站点（查询参数 updatesite，REQ-UPD-022）：拉起时写入
	// **本实例**的更新配置（只改 URL 字段）；空 = 不修改。合法性（http/https）
	// 由 core.SetUpdateConfig 校验，解析层只透传。
	UpdateSite string
}

// ParseSipURL 解析 sip:// / sips:// URI：
//
//	sip://account:password@host:port?transport=tcp
//	sips://account:password@host:port        ⇒ transport=tls（默认端口 5061）
//	sip://account@host?updatesite=https://…  ⇒ 更新站点（省略 = 不改配置）
//
// 账号与密码支持 URL 编码。
func ParseSipURL(raw string) (*Account, error) {
	lower := strings.ToLower(raw)
	sips := strings.HasPrefix(lower, SchemeSips+"://")
	if !sips && !strings.HasPrefix(lower, Scheme+"://") {
		return nil, fmt.Errorf("不是合法的 sip:// / sips:// 链接")
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
	// sips:// 前缀即 TLS（与查询参数相比是更强的声明，参数改不了它）
	if sips {
		acc.Transport = config.TransportTLS
		acc.Port = 5061
	}
	if pwd, ok := u.User.Password(); ok {
		acc.Password = pwd
	}
	if port := u.Port(); port != "" {
		if p, err := strconv.Atoi(port); err == nil && p > 0 && p < 65536 {
			acc.Port = p
		}
	}
	if tr := u.Query().Get("transport"); tr != "" && !sips {
		acc.Transport = config.NormalizeTransport(tr)
	}
	if acc.Transport == "" {
		acc.Transport = config.TransportUDP
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
	// 更新站点（查询参数 updatesite）：sip://账号@服务器?updatesite=https://…
	// 省略/空 = 不改配置；scheme 是否合法由 SetUpdateConfig 校验（此处只透传）。
	acc.UpdateSite = strings.TrimSpace(u.Query().Get("updatesite"))
	return acc, nil
}

// SplitURL 从单个命令行参数中提取 sip:// / sips:// URL。
// Windows 协议拉起时参数形如 "mti-sip-phone:sips://2999:pwd@10.168.4.40:5061"，
// 前面可能带启动器前缀，所以按前缀查找而不是整串比较；大小写不敏感。
func SplitURL(arg string) (string, bool) {
	lower := strings.ToLower(arg)
	for _, p := range urlPrefixes {
		if i := strings.Index(lower, p); i >= 0 {
			return arg[i:], true
		}
	}
	return "", false
}

// FindURL 从命令行参数列表中取第一个 sip:// / sips:// URL（没找到返回 ""）。
func FindURL(args []string) string {
	for _, a := range args {
		if u, ok := SplitURL(a); ok {
			return u
		}
	}
	return ""
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
MimeType=x-scheme-handler/%s;x-scheme-handler/%s;
`, exePath, Scheme, SchemeSips)
	if err := os.WriteFile(desktop, []byte(content), 0o755); err != nil {
		return fmt.Errorf("写 .desktop 失败: %w", err)
	}

	// mimeapps.list 默认应用（优先级高于 xdg-mime default）：sip 与 sips 都指向本程序
	mimeFile := filepath.Join(home, ".config", "mimeapps.list")
	for _, s := range []string{Scheme, SchemeSips} {
		line := fmt.Sprintf("x-scheme-handler/%s=mti-sip-phone.desktop", s)
		if err := upsertLine(mimeFile, "[Default Applications]", line); err != nil {
			return err
		}
	}

	// 更新桌面数据库
	_ = exec.Command("update-desktop-database", appsDir).Run()
	logx.Logger().Info("已注册 sip:// 与 sips:// 协议（.desktop）", "file", desktop)
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
