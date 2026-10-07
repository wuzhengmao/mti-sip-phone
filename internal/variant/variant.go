// Package variant 构建变体标识。
//
// 本仓库有两个构建变体（同一份代码、不同的构建标签）：
//
//   - **完整版**（默认，不带 `noui` 标签）：Fyne 桌面界面 + 系统托盘 + 本地 WS 控制；
//   - **仅托盘无 UI 版**（`-tags noui`，产物 mti-sip-phone-no-ui）：只有系统托盘，
//     不创建任何窗口，所有操作走网页控制台（托盘左键打开浏览器）。
//
// 这里只放**必须按变体区分**的标识，且必须是叶子包（不能 import core/config，
// 否则与 internal/config 形成循环依赖——core 已经 import 了 config）：
//
//   - UpdateAppID：自动更新站点上的应用 ID。**两个变体共用同一份配置文件**，
//     配置里的 update.appId 会被另一个变体写过，因此 config.Load 会按
//     "等于另一个变体的默认值就当作未设置"的规则改写（见 OtherUpdateAppID 与
//     internal/config 的归一化），否则无 UI 版会把自己替换成完整版的 exe
//     （updater 替换的是 os.Executable()，也就是它自己）。
//
// **其余系统级标识刻意不区分**（用户确认两版本是替代关系、不同机共存）：
// 配置目录（config.ConfigDir 固定 mti-sip-phone）、单实例锁、sip:// 协议注册、
// 开机自启项名、通知 AUMID 全部沿用完整版那一套。
package variant

// 变体取值见 variant_full.go（默认构建）与 variant_noui.go（-tags noui）。
