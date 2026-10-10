// Package selfinstall 自安装（REQ-UPD-024）：把程序自身复制到固定数据目录、
// 生成桌面快捷方式，并从固定目录运行。
//
// 场景：用户从网上下载 exe 后直接运行，下载目录里的文件随时可能被清理工具
// 删掉——自更新替换、开机自启、sip:// 协议注册都指向一个可能消失的文件。
// 启动早期检查：当前 exe 不在固定数据目录里时，复制过去、在桌面生成指向
// 固定目录副本的快捷方式、用同一批参数从新位置拉起并退出当前进程。
//
// 必须放在单实例锁之前调用：本进程不碰锁直接退出，新进程正常拿锁，
// 与自更新重启（先放锁再拉起）的竞态口径一致。
package selfinstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"mti-sip-phone/internal/logx"
)

// skipEnv 自安装逃生口：设置了该环境变量（任意非空值）就原地运行不复制，
// 供开发调试使用。
const skipEnv = "SIP_PHONE_NO_RELOCATE"

// shortcutName 桌面快捷方式的固定名称（用户要求）。两个变体是替代关系
// （同机只能跑一个），共用这一个快捷方式，后安装的覆盖指向。
const shortcutName = "MTI 软电话"

// execStart 启动新进程的可注入点（测试替换，避免真的拉起进程）。
var execStart = func(cmd *exec.Cmd) error { return cmd.Start() }

// createShortcut 生成桌面快捷方式的可注入点（测试替换为空操作）。
var createShortcut = createDesktopShortcut

// tempDir 系统临时目录（测试替换——测试自身的临时目录就在系统临时目录下，
// 不替换的话复制路径永远被跳过规则挡住）。
var tempDir = os.TempDir

// Relocate 必要时把 exePath 复制到固定数据目录并从那里拉起（args 原样透传）。
// 返回值：
//   - (true, dst, nil)：新进程已启动，调用方应立即退出；
//   - (false, "", nil)：无需迁移（已在固定目录，或被逃生口/临时目录规则跳过）；
//   - (false, "", err)：迁移失败，调用方应原地继续运行——自安装失败
//     绝不能导致启动失败。
func Relocate(exePath string, args []string) (relocated bool, dst string, err error) {
	// 1. 逃生口（开发调试）
	if os.Getenv(skipEnv) != "" {
		return false, "", nil
	}

	// 2. 解析符号链接后取绝对路径（Linux 上 exe 可能是 symlink；Windows 无副作用）
	real, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		return false, "", fmt.Errorf("解析可执行文件路径失败: %w", err)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return false, "", fmt.Errorf("获取可执行文件绝对路径失败: %w", err)
	}

	// 3. go run / go test 的临时 exe 位于系统临时目录下，不复制
	// （否则会把开发产物塞进固定目录、盖掉正式文件）
	if temp := tempDir(); temp != "" && underDir(real, temp) {
		return false, "", nil
	}

	// 4. 已在固定数据目录 → 无事可做
	target, err := TargetDir()
	if err != nil {
		return false, "", fmt.Errorf("获取固定数据目录失败: %w", err)
	}
	if sameDir(filepath.Dir(real), target) {
		return false, "", nil
	}

	// 5. 复制到固定数据目录（保留原文件名：完整版与无 UI 版不同名、互不覆盖）
	if err := os.MkdirAll(target, 0o755); err != nil {
		return false, "", fmt.Errorf("创建固定数据目录失败: %w", err)
	}
	dst = filepath.Join(target, filepath.Base(real))
	// 目标已存在且与源是同一文件（大小写差异/符号链接导致路径不同）→ 已就位
	if fi, err := os.Stat(dst); err == nil {
		if sf, err2 := os.Stat(real); err2 == nil && os.SameFile(fi, sf) {
			return false, "", nil
		}
	}
	if err := copyFile(real, dst); err != nil {
		return false, "", err
	}

	// 6. 桌面快捷方式（便利项）：指向固定目录的副本——下载产物被清理后
	// 用户仍能从桌面启动。失败只告警不阻断（程序本体已在固定目录）。
	if err := createShortcut(dst); err != nil {
		logx.Logger().Warn("生成桌面快捷方式失败", "err", err, "dst", dst)
	}

	// 7. 从新位置拉起（参数原样透传），成功后本进程退出
	cmd := exec.Command(dst, args...)
	cmd.SysProcAttr = detachAttr()
	if err := execStart(cmd); err != nil {
		return false, "", fmt.Errorf("启动固定目录副本失败: %w", err)
	}
	return true, dst, nil
}

// copyFile 把 src 复制到 dst（经临时文件 + rename，目标原子替换）。
// dst 被运行中的实例锁住时（Windows：运行中的 exe 不能覆盖），删除目标重试
// 一次，仍失败则返回错误由调用方回退原地运行。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("读取自身失败: %w", err)
	}
	tmp := dst + ".tmp"
	defer os.Remove(tmp)
	// 0755：Linux 上下载来的文件可能没有执行位
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		// 目标可能被另一个运行中的实例锁住：删掉重试一次
		if rmErr := os.Remove(dst); rmErr != nil {
			return fmt.Errorf("替换目标失败: %w（删除旧目标也失败: %v）", err, rmErr)
		}
		if err2 := os.Rename(tmp, dst); err2 != nil {
			return fmt.Errorf("替换目标失败: %w", err2)
		}
	}
	return nil
}

// sameDir 判断两个目录是否为同一目录（Windows 路径大小写不敏感）。
func sameDir(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// underDir 判断 path 是否位于 dir 内（或等于 dir）。
func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
