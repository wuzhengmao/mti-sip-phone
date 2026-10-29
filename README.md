# mti-sip-phone

跨平台 SIP 软电话（Go 实现）：常驻系统托盘，支持注册、呼叫、振铃、接听、挂断；通过本地 WebSocket 接口供**网页控制台**（通话 / 通话记录 / 通信录 / 配置 / 诊断五个页面）与 JS SDK 控制。

两个构建变体：**完整版**（Fyne 桌面界面 + 托盘）与**仅托盘无 UI 版**（`mti-sip-phone-no-ui`：只有托盘、不创建任何窗口，全部操作走浏览器里的控制台）。

- 架构设计：[docs/design.md](docs/design.md)（新同学先看这份）
- 用户使用手册：[docs/user-manual.md](docs/user-manual.md)（面向最终用户：安装、配置、使用、故障排查）
- SDK 开发指南：[docs/sdk-guide.md](docs/sdk-guide.md)（面向前端：网页集成、API、避坑）
- 变更记录：[changelog.md](changelog.md)
- 需求文档：[docs/requirements.md](requirements.md)
- WS 协议：[docs/ws-protocol.md](docs/ws-protocol.md)
- 自动更新与更新站点接入：[docs/auto-update.md](docs/auto-update.md)

## 目标平台

| 系统 | 架构 |
| --- | --- |
| Windows | x86（32 位）/ x86_64 / arm64（基础版，无视频/Opus/AEC） |
| Linux | x86_64 / arm64 |
| macOS | arm64 / x86_64（基础版：无视频/Opus/AEC；可打 universal 包） |

> **运行要求**：Windows 10 及以上（Go 1.24+ 的运行时不再支持 Windows 7/8——它用了 `ProcessPrng`
> 这类 Win10 才有的 API）；Windows 产物是静态单文件，不依赖任何第三方 DLL。
> Linux 产物依赖系统的图形/音频库（任何带桌面的发行版都具备），glibc 不低于构建机的版本。
> macOS 要求 12 及以上；应用未做 Apple 开发者签名/公证，首次打开需右键「打开」或
> `xattr -cr /Applications/MTI软电话.app` 清除隔离属性。

## 构建与运行

依赖：Go 1.24+、gcc（CGO，Windows 用 MinGW-W64 / MSYS2；Linux 用系统 gcc）。
fyne 的 OpenGL 绑定依赖 CGO，需要**目标平台**的 gcc 工具链：Windows GUI 可以在
Linux 上用 mingw-w64 交叉构建（glfw 的 C 源码与 mingw 专用头随 go-gl/glfw 模块
自带，malgo 自带 miniaudio.c，无需 Windows SDK）；反向不成立——glfw 的 Linux
后端需要 X11/Wayland/GL 开发头与 pkg-config，Linux GUI 只能在 Linux 环境
（Linux 本机 / WSL2 / Docker）里构建。

```bash
# 版本号：构建脚本从 internal/core/buildinfo.go 读基础版本。开发构建自动加 -rc
# （如 1.0.13-rc，非正式版）；正式版：SIP_PHONE_RELEASE=1 build.bat（发布脚本会设）。

# Windows：一键构建（自动探测 WinLibs gcc），产物输出到 dist/：
build.bat
#   每个 GUI 目标都出两份：完整版 sip-phone-<os>-<arch> 与仅托盘无 UI 版 sip-phone-no-ui-<os>-<arch>
#   dist/sip-phone-windows-amd64.exe          托盘版（GUI 子系统无控制台，CGO+音频，静态）
#   dist/sip-phone-no-ui-windows-amd64.exe    仅托盘无 UI 版（同上，只是没有窗口，静态）
#   dist/sip-phone-console.exe                控制台版（-headless 无界面模式/命令行调试，静态）
#   dist/sip-phone-windows-386.exe            32 位托盘版（需 i686 工具链，缺失则跳过该目标；静态）
#   dist/sip-phone-no-ui-windows-386.exe      32 位仅托盘无 UI 版
#   dist/sip-phone-windows-arm64.exe          ARM64 托盘版（需 llvm-mingw，缺失则跳过；
#                                             视频/Opus/AEC 静态库暂无 arm64 版 → 基础版）
#   dist/sip-phone-no-ui-windows-arm64.exe    ARM64 仅托盘无 UI 版

# Linux：一键构建（本机 GUI 版 + 交叉 Windows GUI / 对侧 Linux 架构版，均 CGO+音频，输出 dist/）
# 交叉 Windows 需 mingw-w64：apt install gcc-mingw-w64-x86-64 gcc-mingw-w64-i686
# 交叉 Windows arm64 需 llvm-mingw（GCC 系没有 aarch64 目标，解压即用，缺则跳过并打印步骤）
# 交叉对侧 Linux 架构（amd64⇄arm64）需对应交叉 gcc + 图形栈开发包（缺啥脚本会打印准备步骤）
./build.sh
#   dist/sip-phone-linux-<本机arch>[/-no-ui-]       本机 Linux 托盘版 / 仅托盘无 UI 版
#   dist/sip-phone-linux-<对侧arch>[/-no-ui-]       交叉 Linux 托盘版 / 仅托盘无 UI 版
#   dist/sip-phone-windows-amd64.exe                  Windows amd64 托盘版（交叉，静态）
#   dist/sip-phone-no-ui-windows-amd64.exe            Windows amd64 仅托盘无 UI 版
#   （386/arm64 同理，各出两份）
#   注：仅托盘无 UI 版不链接 fyne/GLFW，所以 Linux 交叉**不需要图形栈开发包**

# macOS（须在 macOS 上构建；一次出 amd64 + arm64，完整版与无 UI 版各一份）
./build.sh --target darwin          # 也可 darwin-arm64 / darwin-amd64 / macos
#   dist/sip-phone-darwin-<arch> / dist/sip-phone-no-ui-darwin-<arch>
# 打成 .app + DMG（默认仅托盘无 UI；--variant both 同时出完整版）
./scripts/package-macos.sh --arch universal
#   dist/macos/MTI软电话.app（完整版）、MTI软电话-无UI版.app、MTI-SIP-Phone-*-darwin-universal.dmg
#   universal = lipo 合并两架构后重新 ad-hoc 签名；DMG 拖进「应用程序」即安装
# 也提供 GitHub Actions（.github/workflows/build.yml）：push 到 master 或 PR 且
# 改动涉及代码/构建脚本时自动跑；dev 上要验证在 Actions 页面手动 Run workflow；
# 发版时 release.sh --darwin-ci 会自己触发，不依赖 push。

# 手动构建（Windows 需将 MinGW 的 gcc 加入 PATH，或用 CC 指定）
go build -ldflags "-s -w -H=windowsgui" -o dist/sip-phone.exe ./cmd/sip-phone  # 无控制台

# 运行
dist/sip-phone-windows-amd64.exe        # 托盘常驻；关闭主窗口=隐藏到托盘
dist/sip-phone-console.exe -headless    # 无界面模式（仅 WS 服务，供服务器）
```

启动后：

- 系统托盘常驻：单击托盘图标唤出主界面（拨号 / 配置），关闭窗口=最小化到托盘；
  **仅托盘无 UI 版**则左键点托盘 → 用系统默认浏览器打开控制台
- 网页控制台：http://127.0.0.1:8765/ （WS 端口可配置，被占用自动 +1；无 token 鉴权）
- 配置文件：`%APPDATA%/mti-sip-phone/config.json`（Windows）/ `~/.config/mti-sip-phone/config.json`（Linux）/ `~/Library/Application Support/mti-sip-phone/config.json`（macOS）
- 日志文件：配置目录下 `logs/sip-phone.log`（按大小滚动，含 SIP 信令）
- sip:// 协议：网页链接 `sip://账号:密码@服务器:5060` 拉起软电话并写入注册信息

## 发布到更新站点

站点是独立的「软件更新中心」（当前部署在 https://update.wuzm219.cn，发布规范见其
`/docs/release-spec`、`/docs/upload`、`/docs/api`）。本仓库的发布入口是 `scripts/release.sh`
（Windows 用 `scripts\release.bat`，内部走 git-bash）：

```bash
./scripts/release.sh --pre --dry-run   # 先试跑：编译 + 打包（版本 1.0.13-rc），不上传
./scripts/release.sh --pre             # 预发布：上传 1.0.13-rc（站点标记为预发布）
./scripts/release.sh                   # 正式版：以 1.0.13 编译并上传
./scripts/release.sh --darwin-ci       # 同时经 GitHub Actions 编 macOS 产物并一起发布
```

- **macOS 产物**（`--darwin-ci`）：默认使用 git 凭据助手里 github.com 的令牌（能 `git push`
  到该仓库就有），也可用环境变量 `GH_TOKEN` 显式指定。脚本先触发
  `.github/workflows/build.yml`（正式版带 `SIP_PHONE_RELEASE=1`），和本地容器
  编译**并行**，跑完自动下载两个架构的裸二进制与 universal DMG，一起打包上传。发布物新增
  `mti-sip-phone-macos-x64` / `-arm64`（裸 Mach-O，自动更新用）与
  `mti-sip-phone-macos-universal.dmg`（首次安装包）。

- 站点地址 / 应用 ID / 上传令牌（可选站点证书）在 `release/release.conf`，环境变量可覆盖。
  站点启用 TLS 时地址要直接写 `https://主机:端口`（HTTP 端口只会 301 跳转，POST 跟随跳转
  会退化成 GET）；内部自签名站点填 `CA_CERT` 让 curl 校验。
- 发布物：`mti-sip-phone-<平台>-<架构>[.exe]`（**文件名不含版本号**，同一平台的下载地址
  跨版本不变）、`release.json`、`CHANGELOG.md`（取自 changelog.md 的对应版本段）、
  `SHA256SUMS`，打成 tar.gz 上传。
- 应用级信息走站点接口写入（不直接动归档目录）：`release/app.json`
  → `PUT /api/v1/apps/<应用ID>/metadata`（整份替换，字段要写全），`release/icon.svg`
  → `PUT /api/v1/apps/<应用ID>/icon`。
- **软电话自己按配置的周期查询更新站点**（配置页「自动更新」卡片：站点 / 应用 ID / 检查周期 /
  接受预览版；站点留空 = 不检查），查到更高版本弹右下角提示窗，**下载与重启由用户在软电话
  界面上确认**；网页可经 WS `system.setUpdateConfig` 写这份配置、用 `system.notifyUpdate`
  强制推送指定版本（联调/回滚，不受版本高低约束）。

**发布用的产物统一用容器出**（`bash docker/build.sh`）：一个镜像一次产出全部 10 个产物
（完整版 + 无 UI 版 × 5 平台），构建基线 Debian 10 / glibc 2.28（信创系统可用的下限），
详见 [docker/README.md](docker/README.md)；`build.bat` / `build.sh` 留给本机开发自测。
开发构建的版本号带 `-rc`，正式版发布时数字相同也会收到更新通知。

## 当前状态（开发中）

| 模块 | 状态 |
| --- | --- |
| 配置（含密码加密存储） | ✅ 已完成 |
| 日志（分级、滚动、脱敏、SIP 信令跟踪） | ✅ 已完成 |
| 本地 WS 控制服务 + 事件推送 | ✅ 已完成 |
| JS SDK + 网页控制台 | ✅ 已完成 |
| SIP 注册（UDP/TCP/TLS + Digest + 续注册 + 退避重试 + 休眠/切网恢复） | ✅ 已完成（UDP/TCP 与真实服务端联调通过；TLS 与本机 TLS 对端联调通过） |
| SIP 呼叫（呼出/呼入/接听/拒接/挂断/保持/DTMF） | ✅ 已完成（与真实服务端联调通过） |
| 媒体引擎（PCMA/PCMU/Opus/G.722 + RTP + 抖动缓冲 + 音量/增益） | ✅ 已完成 |
| 音频设备（枚举/选择/全双工，malgo） | ✅ 已完成 |
| 视频通话（H264/VP8/VP9/H263，含网页订阅） | ✅ 已完成 |
| GUI（托盘 / 拨号盘 / 配置页 / 来电窗口 / 通话面板 / 通话记录页 / 通信录页） | ✅ 已完成 |
| 通话记录 / 通信录（本地 SQLite） | ✅ 已完成 |
| sip:// / sips:// 协议拉起 + 单实例参数转发 | ✅ 已完成 |
| 自动更新（软电话自检更新站点 + 下载替换重启） | ✅ 已完成 |
| SIP 信令 TLS(SIPS)（传输/本端监听/自签证书） | ✅ 已完成 |
| SRTP、AEC/NS/AGC、录音、长时间通话稳定性 | ⏳ 后续版本（P1/P2） |

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

macOS 的平台差异（详见 [CLAUDE.md](CLAUDE.md) 的「macOS（darwin）」一节）：

- **安装形态是 `.app` 应用包**（程序图标与 sip:// 协议只能来自 `Info.plist`）。裸二进制直接运行时会在 `~/Library/Application Support/mti-sip-phone/` 生成应用包（完整版 `MTI软电话.app` / 无 UI 版 `MTI软电话-无UI版.app`）、在桌面放指向它的链接，再从包内重启；已在 `.app` 内（如拖进「应用程序」）则不搬迁。
- **sip:// / sips:// 通过苹果事件接收**（macOS 点链接时系统不传命令行参数），由 `internal/macurl` 在启动早期注册处理器。
- **开机自启**写 `~/Library/LaunchAgents/cn.mti.sip-phone.plist`。
- **通话面板**与 Windows/Linux 一致：无边框、吸附工作区右下角、悬浮在最前、不进 Mission Control/窗口切换器，拖动后记住位置（`internal/ui/topmost_darwin.go`，AppKit）。
- macOS 产物为**基础版**（G.711/G.722 + 音频 + 界面/托盘 + 信令 + WS）：摄像头采集尚未实现，视频/Opus/AEC 静态库暂无 darwin 版本。

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
  core/           应用状态机、事件总线、电话能力抽象接口、通话记录/通信录的采集点
  sip/            SIP 协议栈（注册/呼叫/保持信令与竞态处理、注册看护）
  media/          媒体引擎（RTP/RTCP、PCMA/PCMU/Opus/G.722、抖动缓冲、SDP 协商）
  video/          视频编解码（FFmpeg 解码 + libvpx/OpenH264 编码，cgo）
  audio/          音频设备（malgo 全双工、设备枚举/选择、提示音）
  calllog/        通话记录（SQLite）
  contacts/       通信录（SQLite + 拼音搜索）
  wsapi/          本地 WS 控制服务（事件推送 + 心跳 + 网页视频订阅）
  updater/        自动更新（站点查询、下载、替换重启）
  ui/             Fyne 界面（拨号盘/配置页/通话面板/通话记录页/通信录页/视频窗口/托盘）
  protocol/       sip:// 协议解析与系统协议注册
  single/         单实例 + 参数转发
  autostart/      开机自启
web/            网页控制台（单页五视图：通话/记录/通信录/配置/诊断，内嵌进二进制）+ JS SDK
release/          更新站点发布配置与元数据
scripts/          第三方库构建与一键发布脚本
docs/             需求、架构设计、WS 协议、自动更新、变更记录
```
