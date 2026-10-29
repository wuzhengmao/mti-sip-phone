package updater

import (
	"fmt"
	"os"
	"time"

	"mti-sip-phone/internal/logx"
)

// 更新文件后缀：<exe>.new.tmp 下载中 → <exe>.new 就绪 → <exe>.old 旧文件备份。
const (
	newSuffix = ".new"
	oldSuffix = ".old"
	tmpSuffix = ".tmp"
)

// replaceExe 用 <exe>.new 替换当前可执行文件：
// 现文件改名为 .old（Windows 上运行中的镜像允许改名，不能覆盖）→ 新文件
// 就位；第二步失败回滚，避免留下"没有可执行文件"的状态。
func (u *Updater) replaceExe() error {
	if u.exePath == "" {
		return fmt.Errorf("无法确定当前可执行文件路径")
	}
	newPath := u.exePath + newSuffix
	oldPath := u.exePath + oldSuffix

	if _, err := os.Stat(newPath); err != nil {
		return fmt.Errorf("更新文件不存在，请重新下载: %w", err)
	}
	// 上次运行的残留备份（正常启动时已由 Cleanup 清理）
	if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("无法删除旧的备份文件（%s）: %w", oldPath, err)
	}

	if err := renameWithRetry(u.exePath, oldPath); err != nil {
		return fmt.Errorf("无法备份当前程序（可能被杀毒软件占用，请关闭后重试）: %w", err)
	}
	if err := renameWithRetry(newPath, u.exePath); err != nil {
		if rbErr := renameWithRetry(oldPath, u.exePath); rbErr != nil {
			logx.Logger().Error("更新回滚失败", "err", rbErr, "backup", oldPath)
		}
		return fmt.Errorf("替换程序文件失败: %w", err)
	}
	// macOS：替换后的可执行文件要重做 ad-hoc 签名（否则应用包签名失效，
	// Apple Silicon 上可能无法再启动）。失败只告警，不阻止更新。
	resignAfterReplace(u.exePath)
	return nil
}

// renameFile 重命名实现，测试可替换以注入失败场景。
var renameFile = os.Rename

// renameWithRetry 重命名重试：杀毒软件/索引器可能短暂占用文件。
func renameWithRetry(oldPath, newPath string) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = renameFile(oldPath, newPath); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// Cleanup 清理更新残留文件（启动时调用，须在取得单实例锁之后，
// 避免删到另一个实例正在使用的文件）。
func (u *Updater) Cleanup() {
	if u.exePath == "" {
		return
	}
	for _, suffix := range []string{oldSuffix, newSuffix, newSuffix + tmpSuffix} {
		p := u.exePath + suffix
		if err := os.Remove(p); err == nil {
			logx.Logger().Info("已清理更新残留文件", "path", p)
		} else if !os.IsNotExist(err) {
			logx.Logger().Warn("清理更新残留文件失败", "path", p, "err", err)
		}
	}
}
