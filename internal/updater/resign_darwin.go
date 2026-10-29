//go:build darwin

package updater

import (
	"os/exec"

	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/macbundle"
)

// resignAfterReplace macOS 自更新的收尾：被替换的可执行文件位于 .app 应用包内，
// 应用包的签名（_CodeSignature）与新二进制的哈希对不上。Go 链接器会给 arm64
// 产物自带 ad-hoc 签名（Apple Silicon 的硬性要求），但应用包层级仍要重签一次，
// 否则 codesign 校验失败、个别情况下系统拒绝启动。
//
// 只做 ad-hoc（--sign -）：没有开发者证书，公证/Developer ID 签名不在本项目的
// 分发方式内。codesign 缺失或失败只记日志——更新本身已完成。
func resignAfterReplace(exePath string) {
	target := exePath
	if root, ok := macbundle.RootOf(exePath); ok {
		target = root // 签整个应用包，包内二进制一并重签
	}
	if out, err := exec.Command("codesign", "--force", "--deep", "--sign", "-", target).CombinedOutput(); err != nil {
		logx.Logger().Warn("更新后重新签名失败（应用仍可尝试启动）", "target", target,
			"err", err, "output", string(out))
		return
	}
	logx.Logger().Info("更新后已重新 ad-hoc 签名", "target", target)
}
