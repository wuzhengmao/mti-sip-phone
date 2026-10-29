// Package macurl macOS 的 sip:// / sips:// 链接接收（kAEGetURL 苹果事件）。
//
// macOS 与 Windows/Linux 的差别：点击 sip:// 链接时系统**不会**把 URL 放进
// 命令行参数，而是投递一个 Apple Event（kInternetEventClass / kAEGetURL）给
// 已注册的 .app——正在运行时直接投给运行中的实例，没运行则先启动再投递。
// 因此只能用 NSAppleEventManager 注册处理器，`os.Args` 那条路在 macOS 上
// 只在"终端里手工带参数启动"时有效。
//
// 处理器必须在 NSApplication 跑起来之前装好（事件在启动完成后才派发，
// 早装才不会漏掉"点链接冷启动"的那一次），所以 main 里分两步：
// Start() 尽早调用，SetHandler() 等核心组装好再接上回调。
package macurl

// Start 安装苹果事件处理器（幂等）。非 macOS 平台为空操作。
//
// 必须在主线程调用（main 的第一个函数），且早于 GUI 主循环。
func Start() { start() }
