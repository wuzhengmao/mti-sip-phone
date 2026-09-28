# mti-sip-phone

跨平台 SIP 软电话（Go 实现）：常驻系统托盘，支持注册、呼叫、振铃、接听、挂断；通过本地 WebSocket 接口供网页控制，附带 JS SDK 与测试页面。

- 变更记录：[changelog.md](changelog.md)
- 需求文档：[docs/requirements.md](requirements.md)
- WS 协议：[docs/ws-protocol.md](docs/ws-protocol.md)

## 目标平台

| 系统 | 架构 |
| --- | --- |
| Windows | x86_64 / arm64 |
| Linux | x86_64 / arm64 |

## 构建与运行

依赖：Go 1.23+、gcc（CGO，Windows 用 MinGW-W64 / MSYS2；Linux 用系统 gcc）。

```bash
# Windows：一键构建（自动探测 WinLibs gcc）
build.bat
# 产物：
#   bin/sip-phone.exe         GUI 子系统，无控制台窗口，静默运行于托盘
#   bin/sip-phone-console.exe 控制台版，供 -headless 调试

# Linux：
./build.sh

# 手动构建（Windows 需将 MinGW 的 gcc 加入 PATH，或用 CC 指定）
go build -ldflags "-s -w -H=windowsgui" -o bin/sip-phone.exe ./cmd/sip-phone  # 无控制台
go build -tags noaudio -o bin/sip-phone ./cmd/sip-phone                       # 纯 Go 交叉编译

# 运行
./bin/sip-phone                     # 正常模式（托盘 + 主窗口，无控制台）
./bin/sip-phone-console.exe -headless  # 无界面模式（仅 WS 服务，供 Linux 服务器）
```

启动后：

- 系统托盘常驻：单击托盘图标唤出主界面（拨号 / 配置），关闭窗口=最小化到托盘
- 测试页面：http://127.0.0.1:8765/ （token 自动注入，开箱即用）
- 配置文件：`%APPDATA%/mti-sip-phone/config.json`（Windows）/ `~/.config/mti-sip-phone/config.json`（Linux）
- 日志文件：配置目录下 `logs/sip-phone.log`（按大小滚动，含 SIP 信令）
- sip:// 协议：网页链接 `sip://账号:密码@服务器:5060` 拉起软电话并写入注册信息

## 当前状态（开发中）

| 模块 | 状态 |
| --- | --- |
| 配置（含密码加密存储） | ✅ 已完成 |
| 日志（分级、滚动、脱敏、SIP 信令跟踪） | ✅ 已完成 |
| 本地 WS 控制服务 + 事件推送 | ✅ 已完成 |
| JS SDK + 测试页面 | ✅ 已完成 |
| SIP 注册（UDP/TCP + Digest + 续注册 + 退避重试） | ✅ 已完成（集成测试通过） |
| SIP 呼叫（呼出/呼入/接听/拒接/挂断） | ✅ 已完成（集成测试通过） |
| 媒体引擎（PCMA/PCMU + RTP + 抖动缓冲） | ✅ 已完成 |
| 音频设备（枚举/选择/全双工，malgo） | ✅ 已完成（待真机通话验收） |
| GUI（托盘 / 来电置顶窗口 / 通话置顶面板） | ✅ 已完成（待真机验收） |
| sip:// 协议拉起 + 单实例参数转发 | ✅ 已完成（冒烟验证通过） |
| 通话记录、DTMF、录音、SRTP、AEC 等 | ⏳ 后续版本（P1/P2） |

**注册生命周期**：配置了账户后启动即自动注册；按服务端 expires 周期定时续注册；修改账户配置保存后以新配置重新注册；退出程序时自动注销（expires=0）。

## 跨平台出包

目标：Windows / Linux × x86_64 / arm64。本机为 Windows 开发环境：

- **windows/amd64**：本机 `go build`（需 MinGW）
- **linux/amd64、linux/arm64**：Docker + `gcc-aarch64-linux-gnu` 交叉编译（或 CI Linux runner 本机构建）
- **windows/arm64**：llvm-mingw（`aarch64-w64-mingw32-gcc`）或 CI `windows-11-arm` runner
- 音频层可用 `-tags noaudio` 切空后端做纯 Go 构建

## 网页接入示例

```html
<script src="http://127.0.0.1:8765/sdk/sip-phone-sdk.js"></script>
<script>
  const sdk = new SipPhoneSDK({ token: '<软电话设置界面的 token>' });
  sdk.on('call.incoming', (data) => {
    if (confirm('接听 ' + data.call.remote + ' 的来电？')) {
      sdk.answer(data.call.id);
    }
  });
  await sdk.connect();
  await sdk.dial('1001');
</script>
```

## 目录结构

```
cmd/sip-phone/    程序入口
internal/
  config/         配置加载与密码加密存储
  logx/           日志（滚动文件 + 脱敏 + SIP 信令跟踪）
  core/           应用状态机、事件总线、电话能力抽象接口
  wsapi/          本地 WS 控制服务（鉴权 + CORS + 事件推送）
  sip/            SIP 协议栈与媒体层（开发中）
web/
  test.html       测试页面（内嵌进二进制）
  sdk/            JS SDK 与类型声明
docs/             ws-protocol.md 等设计文档
```
