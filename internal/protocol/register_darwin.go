//go:build darwin

package protocol

import (
	"fmt"

	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/macbundle"
	"mti-sip-phone/internal/variant"
)

// registerDarwin macOS 的协议注册：Info.plist 的 CFBundleURLTypes 声明
// sip/sips → lsregister 让 LaunchServices 立刻收录 → 设成这两个 scheme 的
// 默认处理器。图标与 URL scheme 都只认 .app 应用包，所以裸二进制
// （自安装被跳过、SIP_PHONE_NO_RELOCATE=1 的调试运行）注册不了，直接报错。
func registerDarwin(exePath string) error {
	root, ok := macbundle.RootOf(exePath)
	if !ok {
		return fmt.Errorf("当前可执行文件不在 .app 应用包内，无法注册 sip:// 协议")
	}
	if macbundle.NeedsRefresh(root) {
		// 裸包、或早期版本没有声明 sip/sips 的旧包：补齐 plist 与图标
		//（别人的应用包 NeedsRefresh 恒为 false，不会被改写）
		if err := macbundle.Ensure(root, "", variant.NoUI); err != nil {
			return fmt.Errorf("补写应用包信息失败: %w", err)
		}
	}
	if err := macbundle.Register(root); err != nil {
		return err
	}
	bundleID := macbundle.BundleID(variant.NoUI)
	if err := macbundle.SetDefaultURLHandler(bundleID); err != nil {
		// 默认处理器设置可能被系统策略拦下（需要用户在「默认应用」里确认），
		// 但应用已被收录为候选，不影响点击链接拉起；只告警。
		logx.Logger().Warn("设置 sip:// 默认处理器失败（应用已注册为候选）", "err", err, "bundle", bundleID)
	}
	logx.Logger().Info("已注册 sip:// 与 sips:// 协议（.app + LaunchServices）", "bundle", root)
	return nil
}
