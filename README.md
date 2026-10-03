# mti-sip-phone

跨平台 SIP 软电话（Go 实现）：常驻系统托盘，支持注册、呼叫、振铃、接听、挂断；通过本地 WebSocket 接口供网页控制，附带 JS SDK 与测试页面。

- 架构设计：[docs/design.md](docs/design.md)（新同学先看这份）
- 变更记录：[changelog.md](changelog.md)
- 需求文档：[docs/requirements.md](requirements.md)
- WS 协议：[docs/ws-protocol.md](docs/ws-protocol.md)
- 自动更新与更新站点接入：[docs/auto-update.md](docs/auto-update.md)

## 目标平台

| 系统 | 架构 |
| --- | --- |
| Windows | x86（32 位）/ x86_64 |
| Linux | x86_64 / arm64 |

## 构建与运行

依赖：Go 1.23+、gcc（CGO，Windows 用 MinGW-W64 / MSYS2；Linux 用系统 gcc）。
fyne 的 OpenGL 绑定依赖 CGO，需要**目标平台**的 gcc 工具链：Windows GUI 可以在
Linux 上用 mingw-w64 交叉构建（glfw 的 C 源码与 mingw 专用头随 go-gl/glfw 模块
自带，malgo 自带 miniaudio.c，无需 Windows SDK）；反向不成立——glfw 的 Linux
后端需要 X11/Wayland/GL 开发头与 pkg-config，Linux GUI 只能在 Linux 环境
（Linux 本机 / WSL2 / Docker）里构建。

```bash
# 版本号：构建脚本从 internal/core/buildinfo.go 读基础版本。开发构建自动加 -rc
# （如 1.0.5-rc，非正式版）；正式版：SIP_PHONE_RELEASE=1 build.bat（发布脚本会设）。

# Windows：一键构建（自动探测 WinLibs gcc），产物输出到 dist/：
build.bat
#   dist/sip-phone-windows-amd64.exe          托盘版（GUI 子系统无控制台，CGO+音频）
#   dist/sip-phone-console.exe                控制台版（-headless 无界面模式/命令行调试）
#   dist/sip-phone-windows-386.exe            32 位托盘版（需 i686 工具链，缺失则跳过该目标）

# Linux：一键构建（本机 GUI 版 + 交叉 Windows GUI / 对侧 Linux 架构版，均 CGO+音频，输出 dist/）
# 交叉 Windows 需 mingw-w64：apt install gcc-mingw-w64-x86-64 gcc-mingw-w64-i686
# 交叉对侧 Linux 架构（amd64⇄arm64）需对应交叉 gcc + 图形栈开发包（缺啥脚本会打印准备步骤）
./build.sh
#   dist/sip-phone-linux-<本机arch>           本机 Linux 托盘版
#   dist/sip-phone-linux-<对侧arch>           交叉 Linux 托盘版
#   dist/sip-phone-windows-amd64.exe          Windows amd64 托盘版（交叉）
#   dist/sip-phone-windows-386.exe            Windows 386 托盘版（交叉，静态）

# 手动构建（Windows 需将 MinGW 的 gcc 加入 PATH，或用 CC 指定）
go build -ldflags "-s -w -H=windowsgui" -o dist/sip-phone.exe ./cmd/sip-phone  # 无控制台

# 运行
dist/sip-phone-windows-amd64.exe        # 托盘常驻；关闭主窗口=隐藏到托盘
dist/sip-phone-console.exe -headless    # 无界面模式（仅 WS 服务，供服务器）
```

启动后：

- 系统托盘常驻：单击托盘图标唤出主界面（拨号 / 配置），关闭窗口=最小化到托盘
- 测试页面：http://127.0.0.1:8765/ （WS 端口可配置，被占用自动 +1；无 token 鉴权）
- 配置文件：`%APPDATA%/mti-sip-phone/config.json`（Windows）/ `~/.config/mti-sip-phone/config.json`（Linux）
- 日志文件：配置目录下 `logs/sip-phone.log`（按大小滚动，含 SIP 信令）
- sip:// 协议：网页链接 `sip://账号:密码@服务器:5060` 拉起软电话并写入注册信息

## 发布到更新站点

站点是独立的「软件更新中心」（当前部署在 https://update.wuzm219.cn，发布规范见其
`/docs/release-spec`、`/docs/upload`、`/docs/api`）。本仓库的发布入口是 `scripts/release.sh`
（Windows 用 `scripts\release.bat`，内部走 git-bash）：

```bash
./scripts/release.sh --pre --dry-run   # 先试跑：编译 + 打包（版本 1.0.5-rc），不上传
./scripts/release.sh --pre             # 预发布：上传 1.0.5-rc（站点标记为预发布）
./scripts/release.sh                   # 正式版：以 1.0.5 编译并上传
```

- 站点地址 / 应用 ID / 上传令牌（可选站点证书）在 `release/release.conf`，环境变量可覆盖。
  站点启用 TLS 时地址要直接写 `https://主机:端口`（HTTP 端口只会 301 跳转，POST 跟随跳转
  会退化成 GET）；内部自签名站点填 `CA_CERT` 让 curl 校验。
- 发布物：`mti-sip-phone-<平台>-<架构>[.exe]`（**文件名不含版本号**，同一平台的下载地址
  跨版本不变）、`release.json`、`CHANGELOG.md`（取自 changelog.md 的对应版本段）、
  `SHA256SUMS`，打成 tar.gz 上传。
- 应用级信息走站点接口写入（不直接动归档目录）：`release/app.json`
  → `PUT /api/v1/apps/<应用ID>/metadata`（整份替换，字段要写全），`release/icon.svg`
  → `PUT /api/v1/apps/<应用ID>/icon`。
- 网页端 SDK 定时查询更新站点（初始化时传 `updateUrl`），查到更高版本会经 WS 通知软电话，
  由用户在软电话界面上确认后下载更新；页面可通过 `update.check` 事件看到查询结果。

Windows 上一键只出 Windows 产物；Linux 产物需在 Linux 机器上 `./build.sh` 之后再发布。
开发构建的版本号带 `-rc`，正式版发布时数字相同也会收到更新通知。

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

构建脚本（`build.bat` / `build.sh`）产出的组合：

- **windows/amd64 GUI**：Windows 本机构建（`build.bat`，MinGW，CGO+音频）；也可在 Linux 上用 mingw-w64 交叉（`build.sh`）
- **windows/386 GUI**：需 32 位 MinGW（MSYS2 `mingw-w64-i686-gcc` 或 WinLibs i686），工具链缺失则该目标跳过
- **linux/amd64 GUI**：在 Linux 本机构建（`./build.sh`，需 X11/Wayland/GL 开发包）；也可在 arm64 上交叉
- **linux/arm64 GUI**：在 arm64 本机构建；或在 amd64 上交叉（`./build.sh` 按本机架构自动选目标，需目标架构的交叉 gcc 与 X11/Wayland/GL 开发包，缺啥会打印准备步骤）
- **windows/arm64**：暂未纳入脚本，需 llvm-mingw（`aarch64-w64-mingw32-gcc`）或 CI `windows-11-arm` runner
- 无界面纯 Go 构建（脚本已不产出，标签仍保留）：`-tags "headless noaudio"`（跳过界面层与音频后端，仅本地 WS 服务）

平台差异：通话面板的「无标题栏 + 吸附工作区右下角 + 不进任务栏」在 Windows 用 Win32 实现，Linux 用 X11 实现（`_MOTIF_WM_HINTS` / `_NET_WM_STATE` / `_NET_WORKAREA`），**仅在 X11（含 XWayland）会话生效**——Wayland 不允许客户端自定位窗口、去装饰也由合成器决定。托盘的「退出」项在 Linux 上会标记为退出项（Fyne 不再自动追加 Quit 项），Windows 侧保持原样。

## 网页接入示例

```html
<script src="http://127.0.0.1:8765/sdk/sip-phone-sdk.js"></script>
<script>
  const sdk = new SipPhoneSDK();
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
  wsapi/          本地 WS 控制服务（事件推送 + 心跳）
  sip/            SIP 协议栈（注册/呼叫/保持信令与竞态处理）
  media/          媒体引擎（RTP/RTCP、PCMA/PCMU、抖动缓冲、SDP 协商）
  audio/          音频设备（malgo 全双工、设备枚举/选择、提示音）
  ui/             Fyne 界面（拨号盘/设置页/通话面板/托盘）
  protocol/       sip:// 协议解析与系统协议注册
  single/         单实例 + 参数转发
  autostart/      开机自启
web/
  test.html       测试页面（内嵌进二进制）
  sdk/            JS SDK 与类型声明
docs/             需求、WS 协议、变更记录
```
