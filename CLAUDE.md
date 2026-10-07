# CLAUDE.md

本文件为 Claude Code 提供项目说明，目标是快速、准确地理解与修改本代码库。

## 项目概述

mti-sip-phone：Go 跨平台 SIP 软电话（Windows x64 / x86 / ARM64，Linux x64 / arm64）。常驻系统托盘，支持 SIP 注册、呼出/呼入、PCMA/PCMU/Opus 通话、保持、DTMF、通信录；提供本地 WebSocket 控制接口（网页拨号/接听/挂断、事件推送）与 JS SDK；支持 sip:// 协议拉起与开机自启。

当前开发版本 **1.0.15**（1.0.14 已定版，含 Windows 386 摄像头采集、Windows 产物静态链接、Linux 交叉 CXX 修复）。1.0.15 进行中：**仅托盘无 UI 版（`-tags noui`，产物 `mti-sip-phone-no-ui`，REQ-UI-041）** 与 **网页控制台四个页面（REQ-WEB-001~004 + REQ-WS-016~019 + REQ-UPD-020/021）**。

<details>
<summary>1.0.14 已完成内容（点击展开）</summary>

1.0.14（1.0.13 已定版并合并到 master，含 **SIP 信令 TLS(SIPS) 传输（REQ-ACC-006）**——传输新增 tls、sips:// 拉起、本端自签 TLS 监听、TCP/TLS 信令保活，以及真机联调修出的来电接听/振铃取消/长通话挂断等一批 TCP/TLS 信令缺陷；此前 1.0.12 的 G.722 宽频、视频总开关等一并包含）；另有崩溃现场记录（REQ-LOG-009）、**媒体加密 SRTP（REQ-MEDIA-009）**、音频预处理 AEC/NS/AGC（REQ-MEDIA-007）、窗口位置记忆（REQ-UI-040）。

</details>
版本号从 1.0.0 起，**不自动升版**——升版须经用户明确确认。开发构建的版本号带 `-rc`（见下），
正式版由发布脚本以 `SIP_PHONE_RELEASE=1` 构建。

真实环境：OpenSIPS 2.4.11（10.168.4.40，明文 15060 / **TLS 15061**——自签证书 CN=sip.mti-sh.cn，账号 2999 的传输在配置页切 udp/tls），对端 FreeSWITCH（10.168.4.118，桥接话机 2901）。话机在 NAT 后面（本机是 192.168.3.15，服务端看到的源地址是 **10.168.5.10**）——入向请求只能靠服务端复用本端开出去的连接送回来。OpenSIPS 配了 rtpengine 但服务未运行（"no available proxies"属正常现象，不用处理）；LAN 环境建议关闭 STUN。

## 技术栈

| 领域 | 选型 | 说明 |
| --- | --- | --- |
| SIP 信令 | github.com/emiago/sipgo v1.6.0 | UDP/TCP/**TLS**（TLS 自带，标准库 crypto/tls，无新依赖；REQ-ACC-006 见 `internal/sip/tls.go`），DialogClientSession / DialogServerSession |
| 媒体 | pion/rtp、pion/rtcp、pion/sdp/v3、pion/stun、**pion/srtp/v3**（媒体加密，纯 Go）、github.com/zaf/g711、libopus（cgo） | RTCP 为 v1.2.x 新 API（CompoundPacket/Goodbye，非旧版 Bye）；音频扩展编解码里 **G.722 是纯 Go**（`internal/media/g722`，任何构建都带），Opus 由 `-tags audio` 启用（cgo 静态库，见下） |
| 音频设备 | github.com/gen2brain/malgo v0.11.26 | miniaudio 绑定，**CGO**；`-tags noaudio` 切空后端（纯 Go 构建/测试） |
| GUI | fyne.io/fyne/v2 v2.8.1 | 要求 UI 运行在主线程 |
| WebSocket | github.com/coder/websocket | 仅监听 127.0.0.1，无 token 鉴权 |
| 托盘（无 UI 版） | fyne.io/systray（**已是完整版的直接依赖**）+ fyne-io/oksvg + srwiley/rasterx + fyne-io/image（图标 SVG 光栅化与 ICO 编码，均已在 go.sum） | 仅托盘版**不引入 fyne/v2 与 GLFW/OpenGL**；Linux/Win 侧纯 Go，只有 macOS 用 cgo |
| 其他 | google/uuid、golang.org/x/sys（Windows 注册表） | |

## 构建、测试与运行

```bash
# 构建脚本按目标打印本次能力（如 CGO+音频+视频，缺库时标出无视频/无Opus）；视频/Opus 取决于
# 第三方静态库是否已生成（见下），缺哪个就降级为哪个没有。
#
# **两个构建变体**（每个目标各出一份，脚本自动跑两遍；REQ-UI-041）：
#   full（默认）= 完整版：Fyne 桌面界面 + 托盘
#   noui（-tags noui）= 仅托盘无 UI 版：只起系统托盘、**不创建任何窗口**，全部操作走网页
#        控制台（托盘左键用系统默认浏览器打开 http://127.0.0.1:<WS端口>/ ）
#   产物名：dist/sip-phone-<os>-<arch>[.exe] / dist/sip-phone-no-ui-<os>-<arch>[.exe]
#   （发布到站点后按 APP_ID 改名：mti-sip-phone-no-ui-windows-x64.exe）
#   no-ui 变体**不链接 fyne/GLFW/OpenGL**，所以它**不需要图形栈**——Linux 交叉因此不需要
#   X11/Wayland/GL 那 11 个 .pc，Windows 交叉也彻底不碰 glfw（仍需 mingw：音频是 CGO）。
#   纯 Go 自检（不需要 CGO）：GOOS=linux CGO_ENABLED=0 go build -tags "noui noaudio" ./cmd/sip-phone
# 版本号：脚本从 internal/core/buildinfo.go 读基础版本，**开发构建自动加 -rc**（如 1.0.11-rc），
# 正式构建用 SIP_PHONE_RELEASE=1（发布脚本会设）；不经脚本的 go build/go run 是基础版本号。
# Windows 一键构建（自动探测 WinLibs gcc 作为 CGO 工具链），产物输出到 dist/：
#   dist/sip-phone-windows-amd64.exe          托盘版（GUI 子系统无控制台，CGO，静态）
#   dist/sip-phone-console.exe                控制台版（-headless 无界面模式/命令行调试，静态）
#   dist/sip-phone-windows-386.exe            32 位托盘版（需 i686 工具链，缺失则跳过该目标；静态）
#   dist/sip-phone-windows-arm64.exe          ARM64 托盘版（需 llvm-mingw，缺失则跳过该目标；
#                                             无视频/Opus/AEC 静态库 → 基础版，见「交叉编译说明」）
build.bat

# Linux 一键构建（本机 GUI 版 + 交叉 Windows GUI / 对侧 Linux 架构版，均 CGO，输出 dist/）：
#   dist/sip-phone-linux-<本机arch>           本机 Linux 托盘版
#   dist/sip-phone-linux-<对侧arch>           交叉版（amd64 主机→arm64，arm64 主机→amd64；需对侧
#                                             架构的交叉 gcc + 图形栈开发包，缺失则跳过并打印准备步骤）
#   dist/sip-phone-windows-amd64.exe          Windows amd64 托盘版（交叉，静态，需 x86_64-w64-mingw32-gcc）
#   dist/sip-phone-windows-386.exe            Windows 386 托盘版（交叉，静态，需 i686-w64-mingw32-gcc）
#   dist/sip-phone-windows-arm64.exe          Windows ARM64 托盘版（交叉，基础版，需 llvm-mingw，缺失则跳过）
./build.sh

# 测试（推荐）：固定路径运行器——把测试二进制构建到仓库内 .testbin/ 再执行。
# Windows 防火墙按"程序路径"放行，go test 每次换临时目录会被当成新程序反复弹窗，
# 这个脚本让路径固定（放行一次即可）。会自动探测 WinLibs gcc，参数见脚本头部：
./scripts/test.sh                                  # 默认全部测试包
./scripts/test.sh -p ./internal/media/ -- -run SRTP -v   # -- 之后是 go test 的测试参数
./scripts/test.sh -p ./internal/sip/

# 测试（等价的手工方式）：CGO 包（audio）需要 gcc，用 build.bat 同款工具链：
export CC="<WinLibs mingw64/bin/gcc.exe 路径>"   # build.bat 输出里可见
export CGO_ENABLED=1
go test ./internal/media/ ./internal/wsapi/ ./internal/sip/ ./internal/ui/ ./internal/updater/
# 带 Opus / 视频的包还要 -tags（需先跑 scripts/build-audio-libs.bat / build-video-libs.bat
# 生成静态库，并把 third_party/{audio,video}/<目标>/{include,lib} 经
# CGO_CFLAGS/CGO_LDFLAGS 传入，或直接用 GOFLAGS="-tags=video" ./scripts/test.sh）：
go test -tags audio ./internal/media/

# 运行
dist/sip-phone-windows-amd64.exe       # 托盘常驻；关闭主窗口=隐藏到托盘
dist/sip-phone-console.exe -headless   # 无界面模式（仅 WS 服务，供服务器）

# 音频/媒体排查（可选，默认关闭、零开销）：设 SIP_PHONE_AUDIODUMP=1 启动，
# 每通电话在日志里每秒输出一行【诊断】（收包/写入/读出样本、播放回调次数、
# 缓冲深度、累计截断、采集峰值/采集RMS/麦克风增益%），并把"网络收到的声音"、
# "实际播放的声音"与"送编码器的采集声音（含麦克风增益）"分别落成日志目录下的
# rx-<RTP端口>.wav / play-<RTP端口>.wav / mic-<RTP端口>.wav——三者一比就知道
# 问题出在收、播、还是麦克风。分析：python tools/wavscan.py <wav>
```

测试页地址：`http://127.0.0.1:<WS端口>/`（默认 8765，占用自动 +1 并保存配置）。

### 发布到更新站点

更新站点是独立的「软件更新中心」（源码在另一个仓库，规范见其 `/docs/release-spec`、
`/docs/upload`、`/docs/api`）。本仓库侧的发布入口：

```bash
./scripts/release.sh --pre --dry-run   # 预发布：只编译打包（版本 1.0.11-rc），不上传
./scripts/release.sh --pre             # 预发布：真实上传（站点标记为预发布）
./scripts/release.sh                   # 正式版：SIP_PHONE_RELEASE=1 编译，版本 1.0.11
# Windows 用 scripts/release.bat（同样的参数，内部走 git-bash）
```

- 配置在 `release/release.conf`：`UPDATE_SITE`（当前 `https://update.wuzm219.cn`）/ `APP_ID`
  （mti-sip-phone）/ `UPLOAD_TOKEN`（由站点 `UPLOAD_SECRET` 按应用 ID 推导）/ 可选的
  `CA_CERT`（内部自签名站点用，curl `--cacert`）、`INSECURE_TLS`；同名环境变量可覆盖，
  对着本地站点试跑：`UPDATE_SITE=https://localhost:8443 CA_CERT=... ./scripts/release.sh --pre`。
- 站点启用 TLS（HTTP 端口 301 跳 HTTPS）时 `UPDATE_SITE` **必须直接写 https 地址**：
  上传是 POST，跟随 301 会退化成 GET。站点用自签名证书时浏览器里的 SDK 查询会被证书拦下，
  需要把证书装进系统/浏览器信任库（生产站点是 Let's Encrypt 证书，无此问题）。
- **应用级元数据走站点接口，不要直接动归档目录**：
  `release/app.json` --PUT--> `/api/v1/apps/<app>/metadata`（**整份替换**，没写的字段会清空，
  所以该文件要写全；`icon` 字段别手写——图标接口会自己维护它）；
  `release/icon.svg` --PUT--> `/api/v1/apps/<app>/icon`（类型按内容判定，文件名站点定）。
  两个接口与上传同令牌，未配置 UPLOAD_SECRET 的站点返回 403。
- 版本目录内容（打成 **tar.gz** 上传，git-bash 没有 zip）：
  `mti-sip-phone-<平台>-<架构>[.exe]`（**文件名不含版本号**，同一平台下载地址跨版本不变）、
  `release.json`（artifacts 显式声明 os/arch/kind/sha256/size）、`CHANGELOG.md`（从根
  changelog.md 抽取该版本段）、`SHA256SUMS`。站点按版本目录区分版本，文件名只要含平台/架构词。
- **发布用的最终产物统一在 Linux 机器上跑 `build.sh` 交叉编译**（工具链齐全时一次产出全部
  5 个平台：Windows x64/x86/ARM64 + Linux 本机与对侧架构，**不做 CI 矩阵**）；Windows 上的
  `build.bat` 仅供本机开发自测（只能出 Windows 产物）。
  产物 mtime 早于本次构建开始的会被跳过（防止把上次的旧二进制当新版本发出去）。

**交叉编译说明**：fyne 的 OpenGL 绑定依赖 CGO，需要**目标平台**的 gcc 工具链。

- **Linux → Windows GUI**：用 mingw-w64 直接交叉（`build.sh` 已内置，
  `-H=windowsgui`）。glfw 的 C 源码与 mingw 专用头（`glfw/deps/mingw`）随
  go-gl/glfw 模块自带，malgo 自带 miniaudio.c，链接目标只有 mingw 自带的
  系统导入库，**无需 Windows SDK**。**两个 mingw 目标都加 `-extldflags -static`**：
  mingw 运行时（libwinpthread/libstdc++/libgcc）是 **MinGW 工具链的东西、Windows
  并不自带**，不静态化的话产物必须在装了这些 DLL（或装了 MinGW）的机器上才启动得
  起来；静态化后导入表只剩 ole32/quartz 这类系统组件（DirectShow 用）。
  产物要求 **Windows 10 及以上**：Go 1.22 起官方就不再支持 Win7/8（运行时要
  `ProcessPrng` 这类 Win10 才有的 API），属工具链的硬限制，别再答复"能不能支持 Win7"。
  Debian/Ubuntu 的 mingw-w64 工具链在 **amd64 与 arm64 主机上都有包**，
  所以 arm64 构建机同样能出这两个 Windows 产物，无需禁用。
- **Linux 交叉对侧架构 GUI（amd64 ⇄ arm64）**：glfw 的 Linux 后端会同时编
  X11 与 Wayland（除非 `-tags x11`/`wayland`），go-gl/gl 又走 `pkg-config: gl`，
  因此需要目标架构的交叉 gcc（`aarch64-linux-gnu-gcc` / `x86_64-linux-gnu-gcc`）
  + 目标架构的 11 个 `.pc`（libx11/libxrandr/libxinerama/libxcursor/libxi/
  libxxf86vm/libgl/libwayland/libxkbcommon 的 `:arm64`/`:amd64` 开发包）。
  build.sh 会按本机架构自动选目标并检测，缺失时跳过该目标并打印准备步骤；
  `CROSS_CC` / `CROSS_PKG_CONFIG_LIBDIR` 可覆盖工具链与 pkg-config 目录
  （自建 sysroot 用）。产物链接构建机 sysroot 的 glibc——目标机 glibc 过旧
  会报 GLIBC_x.y not found。
- **Windows → Linux GUI**：无原生交叉路径——glfw 的 Linux 后端同时编 X11 与
  Wayland（除非 `-tags x11`/`wayland`），外加 `pkg-config: gl`，需要 X11/
  Wayland/GL 的开发头与 .pc 文件。只能在 Linux 环境里构建：Linux 本机、
  WSL2，或 Docker 的 fyne-cross。
- 构建脚本**不再产出无界面产物**（用户不需要）；`-tags "headless noaudio"`
  仅保留供手动纯 Go 构建与测试。
- Windows 386 GUI 需 32 位 MinGW，build.bat 依次探测 `C:\msys64\mingw32\bin\i686-w64-mingw32-gcc.exe`
  （MSYS2 `mingw-w64-i686-gcc`）与 `%USERPROFILE%\tools\winlibs-i686\mingw32\bin\i686-w64-mingw32-gcc.exe`
  （WinLibs i686 posix-dwarf-ucrt 解压即用）；两者都缺则跳过 386 目标。
- **Windows arm64 GUI 只能用 llvm-mingw**（`https://github.com/mstorsjo/llvm-mingw`，
  解压即用、无需 Docker/虚拟机）：**GCC 系的 mingw-w64 没有 aarch64-w64-mingw32 目标**，
  `gcc-mingw-w64-*`、WinLibs、MSYS2 的 mingw64/mingw32 都出不了它（MSYS2 的 clangarm64 是
  **ARM64 机器上的本机环境**，不是 x64 上的交叉）。包按宿主分：Windows 取
  `llvm-mingw-<版本>-ucrt-x86_64.zip`，Linux 取 `...-ucrt-ubuntu-22.04-{x86_64,aarch64}.tar.xz`，
  内含一份 clang + lld + mingw-w64 sysroot，靠 `--target=aarch64-w64-mingw32` 出目标码
  （工具名 `aarch64-w64-mingw32-clang{,++}`，posix 线程模型）。两个构建脚本的探测顺序都是
  PATH → `LLVM_MINGW=<解压目录>` → 默认位置（build.bat 是 `%USERPROFILE%\tools\llvm-mingw`，
  build.sh 另试 `$HOME/tools/llvm-mingw`、`/opt/llvm-mingw`、`/usr/local/llvm-mingw`），
  **探测不到就跳过该目标并打印上述安装步骤**（不会让整个构建失败）。
  该产物的 glfw/miniaudio 都是架构无关的 C，直接编过；导入表只有系统 DLL（UCRT），
  无 libwinpthread/libgcc 依赖。**视频/Opus/AEC 的静态库都没有 arm64 版本**（要另为
  aarch64-w64-mingw32 编库，且 OpenH264/abseil 是 C++、llvm-mingw 带的是 libc++ 而非
  libstdc++，脚本里的 `-lstdc++` 得跟着改），所以该目标**固定为基础版**（G.711/G.722 +
  音频设备 + 界面 + 信令 + WS）：build.bat/build.sh 都刻意**不对它做库探测**，免得打印
  「跑 build-video-libs 生成」这种对该目标无效的提示。

> 注意：`internal/protocol` 的 Windows 注册表实现按构建标签拆分在
> `register_windows.go`（导入 x/sys/windows/registry 会破坏 Linux
> 交叉编译，勿合并回 protocol.go）。`cmd/sip-phone` 的 GUI 组装同理
> 拆分在 `gui.go`（构建标签 !headless）。

## 目录结构与职责

```
cmd/sip-phone/main.go   入口：命令行/单实例/协议注册/开机自启/组装各层/GUI 主循环
internal/
  config/     JSON 配置加载与保存（密码 AES-256-GCM 加密、原子写入）
  logx/       slog 日志：滚动+gzip 归档、信令跟踪 TraceSIP（自动脱敏）、崩溃现场记录
              （crash.go/crash_test.go：debug.SetCrashOutput → crash.log，REQ-LOG-009）
  core/       应用核心：状态机、事件总线（发布-订阅）、Telephony 抽象接口。
              calllog.go 定义通话记录接口与采集点（REQ-CALL-007），实现在
              internal/calllog：单 worker FIFO 队列串行执行全部 SQL、
              call_id 主键幂等、启动清扫上次遗留的"进行中"记录
  calllog/    通话记录 SQLite 存储（纯 Go 驱动 modernc.org/sqlite，无 CGO）
  contacts/   通信录 SQLite 存储（同一套单 worker 骨架；主键是规范化号码，
              写操作由 core 串行"写库 + 重建内存快照"，导入时常用标志只增不减）
  contactsearch/ 通信录的拼音索引/搜索/排序（REQ-UI-035）：音头/全拼/汉字/号码/
              备注/纯数字。**桌面通信录页与网页控制台共用这一份**（网页端没有拼音
              字典，别在 JS 里再实现一遍），改这里两边同时生效
  sip/        SIP 电话层（phone.go 为核心：注册/呼叫/保持全部信令与竞态处理；
              netwatch.go 为注册看护：休眠唤醒/网络切换后立即重新注册，REQ-CALL-009；
              tls.go 为 TLS 传输：客户端配置 + 自签证书 + 本端 TLS 监听，REQ-ACC-006）
  media/      媒体引擎：RTP/RTCP 收发、PCMA/PCMU/Opus/G.722 编解码、播放环形缓冲、SDP 协商，
              媒体加密 SRTP（srtp.go，REQ-MEDIA-009）
  media/g722/  G.722 纯 Go 算法（QMF + 双子带自适应 ADPCM；golden 向量在 testdata/）
  audio/      音频设备：malgo 后端（全双工采集/播放、设备枚举/选择、提示音）
  wsapi/      本地 WS 控制服务（方法分发、事件推送、心跳、网页视频订阅 video.go）
  updater/    自动更新：前端经 WS 告知新版本 → 用户确认后下载（sha256/
              可执行魔数校验）→ 替换当前可执行文件（.old 备份）→ 重启；
              向总线发布 system.update 事件
  ui/         Fyne 界面：主窗口/拨号盘/设置页/通话面板/更新提示窗/托盘。
              通话面板与更新提示窗的去装饰/右下角吸附/不进任务栏按平台
              拆分：topmost_windows.go（Win32）、topmost_linux.go（X11：
              _MOTIF_WM_HINTS 去装饰 + _NET_WM_STATE SKIP_TASKBAR +
              _NET_WORKAREA 定位）、topmost_other.go（其余平台空实现）
  ui/ui_update.go  更新提示窗 + 主窗体提示行（REQ-UPD-003）
  ui/folderdlg_windows.go  系统文件夹选择对话框（IFileOpenDialog，Windows）
  ui/folderdlg_other.go    其他平台回退（用 Fyne 内置对话框）
  protocol/   sip:// 协议解析与系统协议注册（Windows 注册表 / Linux .desktop）
  single/     单实例（命名互斥体/flock）+ 第二实例参数转发（随机 token）
  autostart/  开机自启（Windows HKCU Run / Linux XDG autostart，其他平台 no-op）
web/          网页控制台（**内嵌进二进制**，改完必须重新构建 + 重启才生效）
  index.html  控制台外壳（单页 + hash 路由 #/call #/history #/contacts #/settings）
  css/app.css 样式（含原测试页的基础样式）
  js/         common.js（DOM/格式化/toast/弹窗/离线错误静默）、icons.js（图标路径）、
              store.js（SDK 单例 + 状态 + 事件扇出）、callbar.js（常驻通话条）、
              app.js（路由与启动）、views/{call,history,contacts,settings,diag}.js
  sdk/        JS SDK（sip-phone-sdk.js + .d.ts，无 token；更新既有强制推送
              notifyUpdate，也能触发下载/安装（update.*），查询在软电话侧）
release/      发布到更新站点的配置与元数据：release.conf + app.json（完整版）、
              release-no-ui.conf + app-no-ui.json（仅托盘无 UI 版，release.sh --no-ui）、
              icon.svg（两个应用各写一次）；app.json 经站点 metadata 接口整份替换
scripts/      第三方库构建（build-video-libs.*、build-audio-libs.*）、固定路径测试运行器
              （test.sh，防火墙只弹一次）与一键发布（release.sh/.bat）
docs/         design.md（架构设计：分层与依赖、各模块职责、关键决策、目录地图——新同学先看这份）、
              requirements.md（需求）、ws-protocol.md（WS 协议）、auto-update.md（自动更新与
              更新站点接入：规范摘要、REST API、参考实现、接入清单）、
              user-manual.md（用户使用手册：面向最终用户）、sdk-guide.md（SDK 开发指南：
              面向前端集成者；**改 SDK/WS 的对外行为后这两份要同步**）；changelog.md 在仓库根目录
```

## 分层架构与事件流

```
main（组装）
 ├─ ui（Fyne 界面）── 用户操作 → app.Dial / app.Hangup / ...
 ├─ wsapi（WS 服务）── 网页请求 → app 方法
 └─ sip.Phone（Telephony 接口实现）
      ├─ media.Engine（每通通话一个：RTP/RTCP/抖动缓冲）
      └─ audio.Manager（全局单例：AttachEngine 对接媒体引擎与音频设备）
```

- **事件流单向**：sip 层 → `p.sink`（core.App 回调：OnIncoming/OnRinging/OnEstablished/OnFailed/OnTerminated）→ 事件总线 → UI 与 WS 订阅者。
- **自动更新**：**软电话自己**按配置的周期查更新站点（`internal/updater/checker.go`，配置见 `config.Update`：站点/应用 ID/周期/预览版，配置页可改、网页可经 WS `system.setUpdateConfig` 写，REQ-UPD-017/018）→ 查到更高版本经 `updater.Notify` 校验并发布 `system.update` 事件 → UI 弹右下角提示窗 + 主窗体提示行 → **用户点击后**才下载、替换 exe 并重启（WS 不能触发下载/重启）。下载/替换的进度与失败原因同样经总线事件到 UI 与网页。业务前端仍可经 WS `system.notifyUpdate` **强制推送**某个版本（`force` 跳过版本高低检查，测试页「推送」按钮用，REQ-UPD-019）；SDK 不再查询站点。
- **media.Engine 与音频设备解耦**：音频回调调用 `FeedMic`（任意帧长，内部按 20ms 帧累积发送）/ `ReadPlayback`（任意帧长，内部从样本环形缓冲取）。
- **播放缓冲**（media/jitter.go）：样本粒度环形缓冲 8 秒容量；RTP 按序号处理——乱序/过期丢弃、丢包填静音（PLC 用静音而非循环旧帧）、SSRC 切换或序号大跳变（>2 秒）重置缓冲（183 早期媒体 → 200 新媒体流的关键）。
- **保持语义**：本端保持发 re-INVITE（offer `a=sendonly`，恢复 `a=sendrecv`）；对端 re-INVITE 按方向镜像应答（对端 `recvonly` → 本端 `sendonly` 且停止发送）。

## 关键信令流程（sip/phone.go）

- **呼出**：Dial → INVITE（SDP offer，PCMA > PCMU > Opus 优先）→ WaitAnswer（180/183 回调里 OnRinging；183 带 SDP 即早期媒体：SDPApplyAnswer → SetMuted(true)+SetKeepalive(true) → attachAudio+Start）→ 2xx → SDPApplyAnswer → ACK → SetKeepalive(false)+SetMuted(false) → OnEstablished。
- **呼入**：handleIncoming → 忙检查（`len(p.calls)>0` 直接 486 Busy Here）→ SDPAnswer 解析 offer → OnIncoming → 180 / 自动接听 → Answer 后台 goroutine RespondSDP（200+SDP，阻塞等 ACK）→ attachAudio+Start → OnEstablished。
- **挂断竞态处理**（多处历史 bug，勿简化）：
  - 呼入振铃中挂断=拒接：**603 必须先于 ctx cancel 发出**（handleIncoming 退出后事务被终止，603 发不出去）；
  - 呼出 CANCEL 与对端 2xx 竞态：收到 2xx 后必须补 ACK+BYE（RFC 3261 §9.1）；
  - 2xx 已收但本地已挂断：补 ACK+BYE。
- **保持 re-INVITE**：TransactionRequest + 手动读响应 + 手动 ACK。**ACK 必须复用 re-INVITE 请求自身的 From/CallID/CSeq 头**（用初始 INVITE 的头会导致对端校验失败——呼入保持无法取消的历史 bug）；OnRetransmission 补发 ACK；事务保留 5 秒后 Terminate。

## 仅托盘无 UI 版（`-tags noui`，REQ-UI-041）

同一份代码的第二个变体：**只起系统托盘，不创建任何窗口**（无主界面/通话面板/视频窗口/
更新提示窗）。托盘左键 → 系统默认浏览器打开网页控制台（`http://127.0.0.1:<WS端口>/`，
点一次开一个、1 秒防抖），右键菜单两项：「打开控制台」「退出」。图标仍按注册状态变色。

- **分流只在 `cmd` 层**：`gui.go`（`!headless && !noui`）、`gui_noui.go`（`noui`）、
  `gui_headless.go`（`headless && !noui`）三份实现同一个 `runGUI` 签名，
  **参数表必须逐一对齐**（历史踩坑见 gui_headless.go 头注释）。`internal/ui` 一行不改。
- **托盘在 `internal/tray`**：直接用 `fyne.io/systray`（Fyne 托盘就是它），
  **不要调 `SetSystemTrayWindow`**（那是给 Fyne 窗口用的，会把左键回调改成显示窗口）；
  左键用 `systray.SetOnTapped`，且**必须在 `systray.Run` 之前设置**（Linux 注册
  StatusNotifierItem 时按"有没有左键回调"决定 `ItemIsMenu`）。图标：Windows 要 **ICO**、
  Linux 要 **PNG**，由 `internal/appicon` 的同一份 SVG 运行期光栅化（与 Fyne 内部同一套
  oksvg/rasterx 算法），渲染失败退化为纯色圆点（图标缺失会让人连右键菜单都点不到）。
- **变体标识在 `internal/variant`**（叶子包）：`NoUI` / `UpdateAppID` /
  `OtherUpdateAppID`（应用名不区分变体：托盘提示与完整版一样是 `mti-sip-phone`）。两个变体**共用同一份配置目录**（用户确认是替代关系，同机只能跑一个），
  所以 `config.Load` 归一化时把"另一个变体的默认应用 ID"当成未设置改回本变体默认
  （REQ-UPD-021）——否则无 UI 版会查完整版的发布物，把**自己**（`updater` 替换的是
  `os.Executable()`）换成完整版 exe。
- **无 UI 版没有桌面视频出口**：媒体引擎的本地回显默认值改由
  `core.App.HasDesktopVideoSink()` 决定（`sip.Phone.startVideo` 里注入），否则采集循环
  每帧白做一次 YUV→RGBA（≈30MB/s）却没人看。网页订阅本地画面走 `LocalVideoWanted`，
  不受影响。
- **发布是站点上的独立应用**：`release/release-no-ui.conf`（APP_ID `mti-sip-phone-no-ui`
  + 独立令牌）+ `release/app-no-ui.json`；`scripts/release.sh --no-ui` 切换配置与产物清单。

## 视频呼叫

**编解码分工**（`internal/video`，cgo）：解码全部走 FFmpeg 原生解码器（h264/h263/vp8/vp9，
无需外部库）；编码 H264 用 OpenH264、VP8/VP9 用 libvpx、H263 用 FFmpeg；
色彩转换与缩放用 swscale。

**静态库不由仓库携带**，由 `scripts/build-video-libs.sh`（Linux 宿主，支持 mingw/aarch64 交叉）
或 `scripts/build-video-libs.bat`（Windows，走 git-bash + WinLibs）一次性生成到
`third_party/video/<target>/{include,lib}`（已 gitignore）。**库按目标分别生成**：不带参数只出
本机原生目标，交叉目标（windows-amd64 / windows-386 / linux-对侧arch）要用 `all` 或显式指定，
否则那些产物**静默降级为无视频**。`build.bat` / `build.sh` 探测到链接
所需的 5 个 `.a` 齐全就加 `-tags video` 并传 `CGO_CFLAGS`/`CGO_LDFLAGS`；**缺失则自动降级为
无视频构建**（音频完全不受影响，UI 隐藏视频入口）。视频给最终二进制加约 10MB（链接器按需拉取，静态库本身 48MB）。

FFmpeg 的 configure **刻意不启用 libvpx/libopenh264**（只解码），因此不需要 pkg-config
——WinLibs 工具链里没有 pkg-config，这是方案能落地的前提；编码直接由 cgo 链接那两个库。

**音频 Opus（`internal/media/codec_opus.go`，cgo + libopus）**：编码用官方 libopus（FFmpeg
自带的 `ff_opus_encoder` 标着 `AV_CODEC_CAP_EXPERIMENTAL`，官方不认为可用于生产，故不用）。
静态库由 `scripts/build-audio-libs.sh` / `.bat` 生成到 `third_party/audio/<target>/{include,lib}`
（已 gitignore，音频第三方库的统一入口，以后加 G.722 等也走这里）。配置与视频**完全独立**：
探测到 `lib/libopus.a` 就加 `-tags audio` 并注入 `CGO_CFLAGS`/`CGO_LDFLAGS`，缺失则降级为
**无 Opus**（SDP 不宣告，仍走 PCMA/PCMU），两个库都在时标签是 `-tags video,audio`（需要 cgo 静态库的音频扩展统一用 `audio` 标签；**纯 Go 的扩展如 G.722 不进这个标签**）。
libopus 构建很轻（秒级，无 nasm/pkg-config 依赖，x86 的 SIMD 走编译器 intrinsics）。

**音频 G.722（`internal/media/g722/`，纯 Go）**：ITU-T G.722 模式 1（64 kbit/s、7 kHz 宽频、
静态 PT 9、**RTP 时钟 8000**）。**纯 Go 实现**——不加 cgo、不占 `-tags audio`、不需要
`third_party/`，任何构建（含 `-tags noaudio` 的纯 Go 构建）都能用；正确性靠 FFmpeg 参考
实现一次性生成的 **golden 向量**（`internal/media/g722/testdata/`）钉死：编码逐字节一致、
解码逐样本一致（生成命令写在 `g722_test.go` 头部，改算法后要重新核对）。

**按需 16 kHz 管线（REQ-MEDIA-011）**：G.722 的 PCM 是 16 kHz，而 G.711/Opus 的管线是
8 kHz，所以 `Codec` 接口有 `SampleRate()`（**PCM 管线采样率，与 `ClockRate()` 不是一回事**）：
- 引擎按**当前协商编解码器**的 `SampleRate()` 工作，`Engine.SampleRate()` 是唯一出口；
  音频设备开流（`backend_malgo` 的 `deviceConfig.SampleRate`）、播放缓冲的深度阈值/容量、
  诊断 WAV 的头部与上限、深度日志都按它换算——**G.711/Opus 通话恒 8 kHz，行为与历史一字不变**。
- **G.722 在 `supportedCodecs` 里排在 Opus 之前**（PCMA > PCMU > G.722 > Opus）：对端有 G.711
  时仍走 G.711（与历史一致），同时给出 G.722 与 Opus 时选 G.722。真机验证：在 FreeSWITCH 侧
  把该腿 codec 限定为 G.722 打一通，日志应出现 `SDP 协商完成 codec=G722` +
  `音频流已启动 … 采样率=16000`。
- **`FrameSize()` = `SampleRate()/50`（20ms），`TimestampStep()` = `ClockRate()/50`**：
  G.722 是 320 / 160（PCM 帧长是时间戳步长的两倍，与 Opus 的 160 / 960 是同一类"两个率"问题，
  方向相反）。`TestCodecRateInvariants` 对全部编解码器断言这两条不变量。
- 协商换速率（183 早期媒体 G.711 → 200 换 G.722、re-INVITE 换编码）时引擎会
  `ring.SetRate` + 清采集半帧，并经 `SetSampleRateListener` 让 `audio.Manager` 把设备
  **关流重开**（复用 `restartStream`；速率没变不重开，保持/取消保持不会白断一次音频）。
- 丢包补静音（不做 PLC，与 G.711 一致）；编解码器是**包级共享实例**，实现了 `resetter`，
  通话开始/换流/换编码时复位。

**五个目标的可行性**（Windows 的三个都是交叉编译，宿主机位数无关）：

| 目标 | FFmpeg | libvpx | OpenH264 | 摄像头 |
| --- | --- | --- | --- | --- |
| windows-amd64 | ✓ | ✓ | ✓ | ✓ |
| windows-386 | ✓ | ✓ | ✓ | ✓（见下） |
| windows-arm64 | —— | —— | —— | ——（基础版：三套静态库无 arm64 版本，无视频即无摄像头） |
| linux-amd64 / arm64 | ✓ | ✓ | ✓ | ✓（V4L2） |

**Windows 摄像头是仓库内自带的一份 DirectShow 驱动**（`internal/video/dshow`，
从 mediadevices v0.10.0 的 `pkg/driver/camera` 复制，MIT）：上游那份在 i686 上
**编不过**——`camera_windows.hpp` 的回调 `HRESULT SampleCB(...)` 漏了
`STDMETHODCALLTYPE`，而 mingw 的 `qedit.h` 里 `ISampleGrabberCB::SampleCB` 带
`__stdcall`；x86_64 上 `__stdcall` 被忽略所以能编，**i686 上是真实的调用约定冲突**。
复制品只补了这两处声明（`SampleCB`/`BufferCB` 的声明与定义都加
`STDMETHODCALLTYPE`），**amd64 与 386 共用**，别再改回上游的 camera 包。
⚠️ 用 `-D__stdcall=` 之类的宏强行编会让整份 COM 调用变 cdecl，运行期必崩，不要试。

**32 位（windows-386）的 FFmpeg 必须 `--disable-x86asm`**（脚本里已加）：不关的话
H.264 解码会崩在 FFmpeg 自己的 AVX 去块滤波里——gdb 实测栈是
`ff_deblock_v_luma_8_avx` ← `loop_filter` ← `decode_slice` ← `h264_decode_frame`
← `avcodec_send_packet`（`Exception 0xc0000005`）。**症状极具迷惑性**：
32 位版音频/信令/界面全正常，只在**视频呼叫接通、对端画面开始解码**的瞬间整个
进程闪退（GUI 子系统无控制台，panic/信号都没有可见输出）。关掉后解码走 C
回退，速度略降但 386 本就是兼容目标。复核方式：`GOARCH=386 go test -tags video
-ldflags "-extldflags -static" ./internal/media/`（要带 386 的 CGO_CFLAGS/LDFLAGS，
不加 `-extldflags -static` 会报 `0xc000007b` 起不来）。

Windows 宿主上 i686 工具链通常不在 PATH 里，两个脚本都按 `build.bat` 的位置探测：
`C:\msys64\mingw32\bin` 与 `%USERPROFILE%\tools\winlibs-i686\mingw32\bin`。

**关键设计**：

- 视频是**与音频平级的独立 RTP/RTCP 端口对**（`media.VideoEngine`），SDP 里两个 m= 行。
- **视频帧不走事件总线**（总线是容量 64 的 channel、满即丢）：走 `core.VideoSink` 直连 UI，
  媒体层每帧同步回调，UI 侧用「存最新帧 + CAS 合并刷新」保证主线程刷新最多一个在途。
- **需求 7/8 的视频开关是纯本地动作**（停采集/停发送/停渲染），不发 SIP 信令——与麦克风/
  扬声器开关同语义，规避 re-INVITE 的 ACK/CSeq 竞态。
- 摄像头由 `cameraSupervisor` 单协程串行启停（Windows DirectShow 同一设备不能并发打开），
  且**必须先 `StartCamera` 给出采集参数再放开开关**，否则会先用空 ID 抢开默认摄像头。

**网页订阅视频（REQ-VIDEO-009）**：

- `core.App` 的视频出口是**恒定多播对象** `core.videoDemux`（`internal/core/video.go`）：
  主槽给界面（`SetVideoSink`），附加订阅者走 `AddVideoSink`（wsapi 视频中枢）。
  sip 层在通话开始时**取一次出口并捕获进闭包**，所以出口对象不能换——
  早期是单槽、后注册的会把先注册的顶掉（`wsapi.New` 早于 `ui.New`，必踩）。
  出口**恒不为 nil**（无订阅者时静默丢帧）。
- `internal/wsapi/video.go` 的 `videoHub`：按「通道 + 参数」分组编码（同一组只编码一次），
  RGBA → 等比缩放（`x/image/draw`）→ JPEG（stdlib）；**最新帧槽容量 1**，
  编码跟不上就丢旧帧；推送写锁 `TryLock` 抢不到就丢帧（慢客户端不拖住编码循环）。
- 订阅是**连接级**的（`video.subscribe` / `video.unsubscribe`），断开自动退订；
  **远端恒订阅**（`remote` 恒为 true，`local` 才可选）——否则会出现"桌面窗口被隐藏、
  远端画面却没人看"的糊涂状态；
  画面经 **WS 二进制消息**推送：`[通道 1B][序号 4B 大端][JPEG]`（通道 1=远端 2=本地）。
- 订阅期间软电话**不显示视频窗口**：`App.SetWebVideoViewer` 聚合多连接状态 → 发布
  `video.web` 事件 → UI 置 `webVideoOn`。`wantVideoWindow()` 与 `renderVideoFrame()`
  **都要拦**——后者里有直接 `Show()` 的分支，只挡前者窗口会被逐帧显示回来。
- SDK 的 `subscribeVideo({remote, local?, ...})` **直接收 `<video>` 元素**并自行渲染
  （内部 canvas + `captureStream()` 喂元素；`local` 不传 = 不订阅本地回显，
  参数缺省取元素显示尺寸，两路可各带各的参数），关闭视频/挂断/退订自动清成黑底；
  页面拿到的只有 `onStats` 回调与 `video.frame`（低级接口，需要自定义显示时才用）
  （MSE 不支持 MJPEG，逐帧 blob URL 开销大），渲染跟不上直接丢帧。

## 重要约定

- **交流、文档、注释、提交信息、界面文案一律中文**。
- **Git 工作流**：开发在分支上进行（工作分支 dev，master 为收口分支）；**会话中修改的代码不得自动提交**——提交与 push 必须经用户人工确认后执行，且**不自动合并到 master**（合并 master 需用户明确指示）。
- **改完代码就重新构建 + 重启应用**：每次改完代码，跑 `build.bat`（Linux 上 `./build.sh`）重新构建，并重启正在运行的软电话实例（先结束旧进程再启动新产物；运行中的 exe 在 Windows 上不能被覆盖，顺序不能反）。**重启只针对"本来就在跑的那几个"**：先 `Get-CimInstance Win32_Process … | Select CommandLine` **记录下当前实例（含 `-instance N` / `sip://…` 之类的启动参数）**，kill 之后**按原参数把这一组拉起来**——不要无条件启动 3 个实例（用户没开的别替他开）。**不需要推送到更新站点**——发版只在用户明确要求时做（见上「发布到更新站点」），日常改动一律不发。
- **简单的界面改动（文案、占位提示、字段增删）不用自动截图确认**——用户自己会看（2026-10-06 明确要求："这些简单的 ui 改动，不用每次都自动点击截图确认"）。合成鼠标截图那套探针（`D:\cache\temp\guiprobe\probe.ps1`）只在**布局/交互类改动**（新页面、自定义布局、定位、滚轮）或**用户报障需要复现**时才用。
- 版本 1.0.11 不自动升版；开发过程的变更记入 changelog.md 当前开发版本的段落（标题日期写「待发布」），封版时整理该段并改为实际发布日期。
- 弹窗按钮文案「确定/取消」；**弹窗的 parent 必须是当前可见的窗口**——Fyne 把对话框画在父窗口的画布上，主窗体隐藏到托盘时弹在主窗体上用户根本看不见。默认走 `a.dialogParent()`（主窗体不可见且通话记录窗口开着就用它），从记录窗口发起的用 `showXxxIn(a.historyW, …)`；**永远不要传 nil**（dialog 的 parent 为 nil 直接 panic）。
- **通话记录（REQ-CALL-007）的写入口径**：开始/接通/结束三个时刻在 `core.App` 里**直接入队**（不订阅事件总线——总线容量 64、满即丢，通话记录不能建立在会丢事件的东西上），且**先写记录再 `publishCall`**（界面收到 `call.terminated` 会立刻查库刷新，顺序反了就查不到新记录）。同一通话重复上报结束的幂等由存储层的 `ended_at=0` 条件兜住，**不要**在核心层再加"是否首次结束"的状态。库里时间戳 0 表示"未接通/进行中"（0 与 `time.Time{}` 的互转只在 `internal/calllog` 的 `toMillis/fromMillis` 里做，别把 1970 年漏到界面）。
- **通信录（REQ-CONTACT-*）的查找走内存快照，不要逐行查库**：启动时把全量联系人载入 `core.App` 的内存快照（`contactMap` 号码 → 条目 + `contactList` 全量），通话记录行/通话面板/视频窗口标题/WS 事件负载的查找都是 O(1) 读内存。写操作统一走 `App.mutateContacts`——**"写存储 + 重建快照"必须整体串行**（`contactWriteMu`），否则两个并发写入方（界面 + 多个 WS 连接）可能让先写的快照后于后写的完成，把旧数据当最新发布出去。`core.Call.Contact` 是**发布事件时即时解析**的（不在 upsert 时就地写 `a.calls` ——那会和 State() 的读路径构成数据竞争）。
- **号码匹配一律走 `core.NormalizeNumber`**（去 `sip:`/尖括号/`;user=phone`/`@host`/空格与 `-()[]_./`、转小写），去重键、查找键、CSV 合并键三处共用一份实现；显示仍用原始号码。已知限制：不做国码归并（`+86…` 与 `138…` 是两个号码）。
- **悬停提示（备注 tip）不能用 `widget.PopUp` / `canvas.Overlays()`**：Fyne 的指针命中测试一旦发现画布上有 overlay，就**只查 overlay 层、不再回落内容层**（`internal/driver/util.go` 的 `FindObjectAtPositionMatching`：`if overlay != nil { 只遍历 overlay } else { 遍历 roots }`）——overlay 自己是 Hoverable，鼠标一动就把被提示控件的悬停状态抢走（提示闪烁）；做成非 Hoverable 则整个界面在提示显示期间收不到指针事件（提示再也关不掉）。正确做法是**窗口内容 Stack 的顶层图层**（`ui_tooltip.go` 的 `tooltipLayer`：自绘圆角底色 + `plainText`，都不是 Hoverable/Tappable → 命中测试穿透、绘制在最上层且不被滚动容器裁剪）。主窗体的 `mainContent` 因此是 `NewStack(页面, 提示图层)`，切页只换 `Objects[0]`；切页/窗口隐藏时要显式 `hideTip()`（指针可能没离开那一行，MouseOut 不会来）。
- **图标要跟随主题变色就只能用 `fill`**：Fyne 的 `ThemedResource`（`theme.NewColoredResource`）只替换 SVG 里的 **fill**（`internal/svg` 的 `replaceFillColor`），`stroke` 画的线不会变色——所以"线稿"风格的图标要用**细长矩形/细条拼描边**（通信录图标就是这么画的，见 `icons.go` 的 `contactsIconSVG`）。另：`canvas.Image` 渲染 SVG 时必须给显式尺寸，否则可能只画出一个点。
- **SDK 里从 WS 地址推导 HTTP 基址，不能直接取 `new URL(ws://…).origin`**：非 http(s) 协议的 origin 会把 scheme 原样带上（`ws://127.0.0.1:8765`），拿去 `fetch` 直接 "Failed to fetch"。要显式换成 `http:`/`https:`（`sip-phone-sdk.js` 的 `httpBase()`）。
- **通信录的 ★ 是自绘 SVG 图标，不要用文字 "★"**：Fyne 的主题图标集里没有星形，默认字体的回退链在精简的 Linux 环境不保证有 U+2605（会渲染成方框）。`IconStar` 与顶栏入口 `IconContacts` 都在 `icons.go`，用 `markAfterLayout`（已支持任意个标记）跟在名称后面。
- **Windows 原生文件对话框的 CLSID 别抄网上的**：`CLSID_FileSaveDialog` 是 **{C0B4E2F3-BA21-4773-8DBA-335EC946EB8B}**（网上常见的 `C0A651F5-…` 在本机注册表里不存在，`CoCreateInstance` 返回 `REGDB_E_CLASSNOTREG` 0x80040154，表现为"点了导出什么都不发生"）。保存/打开都用 `IID_IFileDialog` 取接口即可（`IFileSaveDialog` 由它派生，基础方法槽位一致：Show(3)/SetFileTypes(4)/SetOptions(9)/SetFileName(15)/SetTitle(17)/GetResult(20)/SetDefaultExtension(22)），取消要与不可用分开（`HRESULT_FROM_WIN32(ERROR_CANCELLED)`）。验证方法：起一个 goroutine 调对话框，主 goroutine `FindWindowW` 按标题找窗口再 `WM_CLOSE` 关掉（见会话内的一次性验证程序）。
- **`coder/websocket` 单条消息默认读上限 32KB**（`read.go` 的 `defaultReadLimit`）：通信录导入是一整份 CSV，超限会**直接断开连接、没有任何业务错误**。服务端在 `Accept` 后 `conn.SetReadLimit(4<<20)` 放开（仅回环监听，没有滥用面）。 不过**网页的一键导入导出走的是 HTTP**（`GET /api/contacts.csv`、`POST /api/contacts/import`），完全绕开这个限制；WS 的那两个方法保留给需要走 WS 的客户端。
- **网页控制台是「单页外壳 + hash 路由」**（`web/index.html` + `web/js/views/*`，`#/call`
  `#/history` `#/contacts` `#/settings`）：**不要改回多个独立 HTML 页面**——视频订阅是
  **连接级**的（断开即退订），整页跳转会丢订阅、`video.web` 事件抖动；而且无 UI 版没有
  桌面通话面板，切页时必须还能接听/挂断（靠常驻通话条 `web/js/callbar.js`）。
  网页资源**全部内嵌**在二进制里：改完必须**重新构建 + 重启**（否则看的还是旧页面），
  由 `internal/wsapi/server.go` 的 `pageFiles` 白名单静态处理器提供（一律 `no-cache`），
  **新增文件要同时加白名单**（`/js/views/diag.js` 就是这么加上去的），否则 404。
  另：**用 `display` 布局的元素别指望 `hidden` 属性**——作者样式会盖掉 UA 的
  `display:none`（`.call-bar` 就这么在没通话时显示了一条黄色空条），要用 `hidden`
  就得配一条 `[hidden] { display: none; }`。
  对端展示（通话条/通话卡片/通话记录）**统一走 `App.display.peer()`**（「号码 名称 - 备注★」，
  备注直接显示、不用 tooltip）；`system.state.noUI` 告诉页面当前是不是仅托盘无 UI 版
  （那边没有桌面视频窗口：自动订阅视频、隐藏「保留窗口」、不给取消订阅）。
  视图加载**必须先看 `store.isConnected()`**：页面刚打开时 WS 还没连上，抢跑请求会以
  "尚未连接软电话"失败、右下角一次刷出好几条 toast（用户实测过）。服务端数据一律用 `textContent` 渲染（通信录名称/
  备注来自用户导入的文件，拼 innerHTML 等于开 XSS）。
- **网页侧的新能力一律走 core 的方法，不要在 wsapi 里直接改 `cfg`**：配置类方法
  （`system.setAccount/setAudioConfig/setVideoConfig/setLogConfig/setWSPort/setAutoStart`）
  统一是"只改传入字段（指针区分未传）→ 校验（失败 `invalid_params` 且不落盘不改内存）→
  改内存 → 落盘 → 广播 `system.config`"，实现在 `internal/core/settings.go`。
  `core.App` 拿不到 `audio.Manager`（会循环依赖），音频能力经 `core.AudioControl` 注入
  （`internal/audio/control.go` 适配，main 里 `app.SetAudioControl(...)`）。
- **会终止进程的动作必须"先回响应、再异步执行"**（`system.restart`、`update.apply`）：
  先动手的话响应发不出去，页面只看到连接断开、不知道成没成。`update.apply` 还要前置校验
  `phase == ready`（异步回包后没法改口）。
- **需求变更同步更新 docs/requirements.md 与 changelog.md。**
- 单通话限制：呼叫/振铃/通话中禁止新呼出（返回错误）；新来电直接 486 USER_BUSY。
- **网页接入的远程开关（REQ-WS-005 修订）**：`ws.allowRemote`（默认 **false**）为真时
  `wsapi.ListenAndServe` 绑 **0.0.0.0** 且 `remoteAllowed()` 不再拒绝非回环来源（四处校验
  统一走它）。**接口没有鉴权**，放开等于把控制权交给整个网段——界面与文档都写明风险，
  网页勾选要二次确认，启动打 WARN。绑定地址在启动时确定（改完必须重启），
  `FindFreeWSPort` 也随之按实际要绑的地址探测。别把这个开关和"信令 TLS/媒体 SRTP"混起来，
  它是**网页控制接口的暴露范围**。
- **多实例（沙箱槽位，REQ-NFR-007）**：默认单实例（不带参数就是主实例，行为一字不变）；`-instance N`（1–99）或 `sip://…?instance=N` 可再启动实例。**槽位 0 = 主实例，用原目录与原行为**；槽位 ≥1 用 `<配置目录>/instances/N/`（配置/记录/通信录/日志/锁各自一份），首次启动生成空白配置、WS 端口起点 = **8765 + 10×N**（槽位 1 → 8775、槽位 2 → 8785…；被占用再 +1，之后以该实例配置里保存的端口为准，可在控制台配置页改）。沙箱实例**不做** sip:// 协议与开机自启注册（只由主实例做，否则会互相覆盖），sip:// 拉起仍转发给主实例。
- WS 端口可配置（默认 8765），被占用自动 +1 并保存；无 token 鉴权（仅回环）。
- SDP 编解码优先级：PCMA > PCMU > Opus；Opus 本端 offer 固定 PT 96（rtpmap `opus/48000/2`），answer 仍复用对端 PT；**RTP 端口按 RFC 3550 取偶数、RTCP = RTP + 1**（`bindRTPRTCP` 命中奇数就重选，音频/视频两对端口都走它），SDP 用 `a=rtcp` 宣告 RTCP 端口。
- 视频 PT 固定（**只作用于本端 offer**）：H264=99 / H263-1998=98 / VP8=100 / VP9=101；优先级顺序不变
  （H264 > VP8 > VP9 > H263-1998），m= 行按优先级列出（`RTP/AVP 99 100 101 98`）。answer 必须复用对端 PT。
- **TLS 传输（REQ-ACC-006）的口径**：配置页「传输」多一个 `tls`；协议链接 `sips://账号:密码@服务器:端口`
  也表示 TLS（默认端口 5061，前缀优先于 `?transport=`）。**服务器证书默认不校验**（内网自签开箱即用），
  勾选「校验服务器证书」后按系统根证书 + 主机名校验；**不做客户端证书（mTLS）**；本端 TLS 监听的证书是
  自动生成的自签名证书（`<实例目录>/tls/local-{cert,key}.pem`，RSA 2048、SAN 含本机各网卡 IP）。
  **只加密信令**：媒体是否加密由「媒体加密」决定（SRTP，REQ-MEDIA-009，默认「支持」= 本端不主动
  声明、对端要求才配合），界面与文档都要写清楚两者是两件事。
- **媒体加密 SRTP（REQ-MEDIA-009）的口径**：**SDES-SRTP（RFC 4568）**——密钥随 SDP 的
  `a=crypto:… inline:<base64(主密钥‖盐)>` 交换，**只加密 RTP/RTCP**（音频、视频、DTMF 事件包；
  SIP 信令不归它管）。四档 `account.srtp`（配置页「媒体加密」下拉）：
  **关闭**（不协商，对端要求也按明文应答）/ **支持**（默认：本端 offer **不带** crypto，来电带
  crypto 则配合）/ **建议**（offer 带 crypto，对端不接受则明文继续）/ **必要**（对端没有可接受的
  crypto 就失败：呼入 488、呼出挂断、重协商去掉 crypto 回 488）。六个套件按序写在 offer 里
  （`AES_CM_128_HMAC_SHA1_80` 第一），answer **复用对端选中的 tag 与套件、只换自己的密钥**；
  不认识的套件/带 lifetime/MKI/会话参数的行一律跳过。**不做** DTLS-SRTP（RFC 5763/5764）、ZRTP、
  客户端证书。SDES 的密钥在 SDP 里明文传，**只有配合 TLS 信令才有意义**（配置页已提示）。
- **SIP 域名（REQ-ACC-022）与声明 IP（REQ-ACC-023）的口径**：
  - 域名只改 **URI 里的域**（REGISTER 的 R-URI/From/To、呼出 INVITE 的 From 与 To/R-URI，
    纯号码补成 `sip:号码@域名`），**网络目的地永远是 Server:Port**——域名多半只在服务器内部
    解析，本机不一定解析得了：注册走 `req.SetDestination(serverDest(acc))`（401 重试会原样
    带上，`cloneRequest` 里有 `SetDestination`），呼叫走首条 `Route: <sip:服务器:端口;lr>`
    （与出站代理同一手法，**代理配置优先**）。`req.Destination()` 是先看 Route、再看 R-URI
    ——这是 sipgo 里唯一能把"URI 指向谁"与"报文发给谁"分开的办法。留空 = 用 Server（历史行为）。
  - 「声明 IP」在界面上是**可输入的下拉**（`widget.SelectEntry`，与主窗体拨号框同款）：
    候选由 `sip.LocalIPv4s()` 给出——**滤掉回环与虚拟网卡**（Hyper-V/WSL/VMware/
    VirtualBox/Docker/VPN/TAP，按 MAC 前缀 + 网卡名双重判定）与链路本地地址，
    并**按可达性排序（公网 → CGNAT → RFC1918 私网）**；留空 = 自动，也可手填任意 IP。
    误伤真实网卡比漏掉虚拟网卡更糟（用户会以为"列表是空的"），加名字关键词时要克制。
  - 声明 IP 同时写三处：Contact 主机、SDP 的 `c=`/`o=`（`SetSDPIP`）、**Via 的 sent-by**
    （`setClientHost` 反射写 sipgo 的私有 `Client.host`，与 `setClientRPort` 同一手法与降级策略，
    保存账户即生效）。**只改"声明"不改绑定**：transport 层只在 Via 没写主机时才用连接地址回填
    （sipgo `overrideSentBy`，且要求 Host 与 Port 都为 0 才回填）。优先级：声明 IP > STUN > 出口 IP
    ——`localHost()` 里声明 IP 先返回，所以配了它之后 STUN 虽仍在探测（netwatch 照旧重探/清空），
    结果**不再影响** Contact 与 SDP。

## 已知陷阱（修改相关代码前必读）

- **设备枚举（`audio.devices` / `video.devices`）必须异步回包**：miniaudio 在音频服务
  不可达时能阻塞十几秒，同步做会把页面的那次请求挂死。服务端 `enumGuard` 做**单飞 +
  2 秒缓存 + 3 秒超时**（超时先回空列表 + `ready:false`，页面显示"枚举中"）。异步/乱序
  回包是安全的：写路径有 `wsConn.wmu` 串行化，SDK 的 `_request` 按 `id` 配对。
- **`audio.Manager.OnReady` 支持多个回调**（原来是 `m.onReady = fn`，后注册的顶掉先注册的）：
  桌面的设置页刷新与 main 的 `audio.ready` 事件广播现在各注册一份。改回单回调会让
  "无 UI 版网页刷新不了设备列表"或"桌面下拉不刷新"，二选一失灵。
- **sipgo**：
  - `DialogStateEnded=3 > DialogStateEstablished=1`，振铃结束判断必须用 `== DialogStateEnded`，不能用 `< Established`；
  - 2xx 的 ACK 是独立事务走 OnAck handler；RespondSDP 按 RFC 6026 阻塞等待 ACK（最长 64×T1=32s），必须放后台 goroutine；
  - 事务层对 CANCEL 自动回 200+487；ServerTx 无最终响应时 TerminateGracefully 会立即 Terminate（后续 Respond 必失败）；
  - Timer_I=T4=5s：re-INVITE 事务需手动延迟 Terminate 防止重传风暴。
- **呼入 handler 在通话结束前绝不能返回**（真机缺陷：TLS 来电接听后约 0.5 秒自动挂断）：sipgo 的
  `Server.TerminateGracefully` 对**可靠传输（TCP/TLS）会在 handler 返回时立即 Terminate 事务**（UDP 不可靠，
  会保留到重传定时器），而 `DialogServerSession.RespondSDP` 发完 200 后要按 RFC 6026 **阻塞等 ACK**
  （最长 32 秒）——handler 一返回，事务就没了，等 ACK 被打断成 `transaction terminated`，接听流程把这通
  刚接通的电话当失败清理掉。所以 `handleIncoming` 在收到非 Ended 的 dialog 状态（Established）后要
  **继续 `<-ctx.Done()` 等本通真正结束**。这条 UDP 侧测不出来（事务被保留，纯属侥幸），只有 TLS/TCP
  会暴露；回归测试 `TestIncomingOverTLS`。
- **sipgo 的传输层"只认 `req.Transport()`"，不会从 URI 的 `sips:` 推断**（`transport_layer.go` 的
  `getTransport(NetworkToLower(req.Transport()))`）：REGISTER 靠 `req.SetTransport("TLS")`，INVITE/ACK/BYE
  走 dialog 层——`sipgo/dialog_ua.go` 只在 **R-URI 带 `transport=` 参数**时才设置它。所以加/改传输方式时，
  `dccInvite` 里的 `recipient.UriParams.Add("transport", …)` 与 `sendRegister` 的 `SetTransport` 两处都要顾到，
  漏一处就会静默退回默认的 UDP（握手都不发生，只是报文发去了错误的传输）。
- **TLS 的客户端配置不能"改字段"，要用 `VerifyConnection` 读原子开关**：UA 只在启动时创建一次，sipgo 把
  `*tls.Config` 的**指针**捕获进拨号闭包（`sip.TransportTLS.init`），之后没法替换；运行中直接改
  `InsecureSkipVerify`/`RootCAs` 又与并发握手构成数据竞争。所以 `internal/sip/tls.go` 固定
  `InsecureSkipVerify=true`（只关掉 Go 自己的校验），真正的校验放在 `VerifyConnection` 里按
  `tlsVerifyFlag`（atomic.Bool）逐次决定——`VerifyConnection` **在 InsecureSkipVerify=true 时照样会被调用**
  （`crypto/tls/handshake_client.go`，它在 if/else 之外），且 `cs.ServerName` 就是 sipgo 按目标主机填的 SNI。
- **dialog 缓存（dcc/dsc）必须按传输方式重建，但通话进行中不能换**：sipgo 明说"每种传输一个实例"
  （缓存里存着 Contact 头与进行中的 dialog），传输/本机地址变了要重建，否则新呼叫带着旧传输的 Contact；
  而换掉正在跑的缓存会让 in-dialog 请求（ACK 匹配、BYE）找不到会话——`ensureDialogCaches` 因此有
  `len(p.calls) > 0` 的刹车（首次创建不受限：呼入路径是先登记通话再建缓存）。
- **本端 TLS 监听要另占一个端口**：TLS 跑在 TCP 上，不能与明文 TCP 监听共用端口，所以
  `FindFreeTLSPort` 在 40000–50000 里另找一个空闲 TCP 端口，Contact 里宣告的是它（明文时是
  `FindFreeSIPPort` 那个）。监听只在传输为 tls 时**按需启动**（`Register` 里 `ensureTLSListener`），
  失败只影响"接收 TLS 呼入"（出向注册/呼叫照常），不要因此让启动失败。
- **`sips://` 里不含 `sip://` 子串**：协议链接的前缀匹配（`protocol.SplitURL`/`urlPrefixes`）必须
  **把 `sips://` 放在前面**逐个查，否则 `strings.Index(arg,"sip://")` 在 sips 链接上直接落空。
- **CANCEL 不能带对话的路由集**（真机缺陷：振铃中挂断后对端一直振铃）：RFC 3261 §9.1 要求 CANCEL 与
  INVITE 同目的地（Call-ID/CSeq/Via branch 也一致），而 sipgo 构造 CANCEL 时走的是 dialog 的 `buildReq`，
  会把**响应里的 Record-Route** 当 Route 头塞进去 → CANCEL 被发到 Record-Route 指向的地址；那条腿未必
  连得上（TLS 注册时它是明文口），CANCEL 就静默丢了。修法：挂断振铃中的呼出时（`Hangup` 里 `cs.cancel()`
  之前）把**临时响应**（1xx）上的 Record-Route 摘掉——**2xx 的不能摘**，那是补发 ACK/BYE 要用的路由集。
  ⚠️ 摘必须用 `stripRecordRoutes`：sipgo 的 `RemoveHeader` **大小写敏感且只删第一条**。
  另外失败原因只在拿到**最终响应**（≥200）时用它，否则记底层错误（曾把"CANCEL 发送失败"显示成
  `183 Session Progress`，排查时完全误导）。
- **"接通约 30 秒被对端挂断" 还有一个服务端来源：Record-Route 里的地址不对**（2026-10-06 真机）：
  对端每 0.5/1.5/3.5 秒重传 200 OK、32 秒（Timer H）后发 BYE，说明 **ACK 没送到**。除了本端传输选错，
  还要看 200 OK 的 `Record-Route` —— 服务端若把 TLS 接口宣告成 **`0.0.0.0:15061`**（socket 的
  advertised 地址没配好）或记成**对端的地址**（`record_route()` 取错），客户端按 RFC 取路由集顶层
  Route 后就会把 ACK 发往 `0.0.0.0`（本机 loopback/不可路由）→ 静默丢失。排查口诀：**先看 200 OK
  的 Record-Route 地址是不是服务端自己的真实地址**，再看 ACK 到底发到了哪个 `IP:端口`（信令日志
  方向行带地址）。另外双 Record-Route 的正常形态是**只有一条带 `r2=on`**（标"接收接口"），两条都带
  是服务端配置有问题。
- **TCP/TLS 连接要自己做保活（CRLF），否则"接通很久后对端挂断本端不知道"**（真机缺陷，2026-10-06）：
  服务端的连接有**空闲超时**（OpenSIPS `tcp_connection_lifetime` 分钟级），而续注册周期是 `0.7×expires`
  （默认 210 秒）——连接会在两次注册之间被关掉；之后服务端要送 in-dialog 请求（对端 BYE/ACK）时
  连接表里已无该连接，只能**按 Contact 重新拨号**，NAT 后面拨不进来（日志：
  `tcp_connect_blocking_timeout ... Connection refused` → `proto_tls: connect failed` →
  `tm:msg_send ... failed`）。所以 `keepaliveLoop` 在 TCP/TLS 下**每 30 秒发一个 CRLF**
  （RFC 5626 §3.5.1；服务端回 CRLF/"pong"，非 SIP 载荷被读钩子忽略）刷新服务端空闲计时。
  症状的关键特征是"**刚接通时一切正常，过一段时间才出问题**"——排查这类问题先看时间维度。
- **NAT 后面的 TCP/TLS 客户端：服务端必须把请求送回"本端开出去的那条连接"**（真机联调结论，2026-10-06）：
  TLS 注册在 OpenSIPS 上一切正常（注册/呼出/被叫都能通），但**对端发起的 in-dialog 请求（ACK、对端的 BYE）
  到不了**——抓包证据是 OpenSIPS 把 ACK/BYE 按 **UDP** 发到我们那条 TLS 连接的源端口（`UDP 15060 →
  10.168.5.10:52369`），紧跟一条 `ICMP Port unreachable`（那个端口上没有任何 socket 在监听）。根因是
  **服务端的路由配置**（`fix_nated_contact()` 把 Contact 改写成"看到的源地址:端口"、以及/或者强制
  `force_send_socket` 到 UDP），属服务端问题——客户端侧试过的 `;ob`/RFC 5626 outbound、按 `r2=on` 选腿
  等变通**已全部回滚**，不要重新加回来。**排查这类问题的正解是抓包**：看请求到底从哪个 socket、以哪种
  协议发往哪个地址:端口，一眼就能分辨"没收到"是服务端没发、发错地方、还是发错传输。
- **NAT 后面要让服务端把注册绑定到本端连接（RFC 5626 outbound）**（真机缺陷：来电接听后进不了
  通话状态、对端挂断本端不知道）：location 表里如果我们的条目**没有 `ob`**，服务端只对**初始**请求复用
  我们开出去的那条连接，**in-dialog 请求（ACK / 对端 BYE）却按 Contact 地址直连**——那是 NAT 上的一条
  出向映射，外面拨不进来，于是 ACK/BYE 全丢（`RespondSDP` 等不到 ACK → 界面不进"通话中"）。
  修法：**Contact URI 上加 `ob` 参数**（`<sips:账号@IP:端口;transport=tls;ob>`，tcp 同样加；udp 不加）——
  服务端认的就是 Contact 里的 `ob`（同机 MicroSIP 也只是加了 `;ob`）；再按 RFC 5626 补 `Supported: outbound`
  与 Contact 的 `reg-id=1`/`+sip.instance`（**已回滚，见下条**）。
- **注册看护（`internal/sip/netwatch.go`，REQ-CALL-009）的休眠检测必须用墙钟**：判断"系统刚休眠唤醒"要比较相邻两次巡检的 `UnixNano()` 差；**别写成 `ts.Sub(last)` / `time.Since`**——它们走**单调时钟**，而系统挂起期间单调时钟不走（Linux `CLOCK_MONOTONIC` 明确如此，Windows QPC 视硬件），恰好检测不到要检测的那段"丢失的时间"（写成 Sub 会静默失效，注入假时钟的单测同样测不出来，只有真机休眠能验）。另外三条约束：① 看护的 kick 通道是 buffer=1 + `select default` 非阻塞投递、**任何一方都不 close**（改成阻塞发送会把看护卡死）；② 上网基线要**进 ticker 循环前**先探一次（只记录不判定），否则注册后首个巡检周期内的网络切换永远检测不到；③ 网络变化后启用 STUN 时**重探失败要清空 `publicIP`**（回退本机地址），留着失效的公网地址注册会得到"显示已注册但不可达"。TCP/TLS 传输另需丢弃 sipgo 复用的连接（UDP 通配绑定可自愈，长连接死了不会被清出池；池键按传输分类，取 TLS 连接要用 `"tls"` 作 network）。
- **画布上只要有 overlay，键盘焦点就换了"域"**（踩过：拨号建议下拉）：
  `canvas.Focused()`/`canvas.Focus()` 走的是 `focusManager()`，而它**有 overlay 时返回
  overlay 自己的焦点管理器**（`internal/driver/*/canvas.go`），`FocusManager.Focus(obj)`
  又要求 obj 在**它自己那棵内容树**里，找不到就静默失败。于是只要挂一个
  `widget.PopUp`/`PopUpMenu`，主内容里的输入框就再也收不到键入——`PopUpMenu` 的
  `Show()` 还会 `canvas.Focus(自己)`（够狠），而手动 `canvas.Focus(输入框)` 也救不回来
  （输入框不在 overlay 的树里）。真机现象是"弹出下拉后再打字就无效，得先关掉下拉"。
  **要"浮层 + 继续输入"只能用窗口内容 Stack 的顶层图层**（`ui_tooltip.go` 与
  `ui_dial_suggest.go` 都是这个套路：图层 MinSize 归零、自绘底色 + 普通 Button 行、
  布局里把绝对坐标换算成图层内坐标）。
  **顺带一条：图层里的东西被"失焦"藏掉时一定要延迟**——Fyne 的鼠标是 press/release
  两次独立命中测试（`glfw/window.go` 的 `mouseClicked`：按下先 Unfocus、抬起再重新找
  对象、只有与按下同一个对象才派发 `Tapped`）。在 `FocusLost` 里立刻隐藏，会让
  "点下拉里的行"永远点不中（点的那一下先失焦 → 图层没了 → 抬起命中到下面的页面）。
- **Fyne 的键盘分发是"二选一"，别指望覆写子类能收到键**：驱动先把键入交给**当前聚焦的控件**，**只有没有控件聚焦时**才走 canvas 的 `OnTypedKey`/`OnTypedRune`（见 `internal/driver/glfw/window.go` 的键处理）。踩过：① 拨号框的按键音钩子做成"继承 `widget.SelectEntry` 覆写 `TypedRune`"——SelectEntry 聚焦时 `FocusGained` 会把焦点转给**它内部的 Entry**，覆写的方法永远收不到键（表现为输入框里敲数字没有任何回调）；所以"输入框里敲数字要响/要发 DTMF"这种需求**做不到也别做**——本项目的约定恰好相反：**输入框里输入数字不响也不发 DTMF**（用户明确要求），按键音只属于"点号码盘"与"未聚焦输入框时在主窗体敲数字"两条路径。② 想让通话中键盘数字发 DTMF，则必须先把输入框 `Disable()`（禁用后不可聚焦，键才会落到 canvas 回调）——通话中主窗体正是这么做的。
- **Fyne**：UI 必须主线程（goroutine 运行会托盘失效+线程告警）；dialog 的 parent 传 nil 直接 panic（主窗口未显示时不要弹 dialog）；`SetSystemTrayWindow` 会覆盖 CloseIntercept（需重新接管）并注册单击回调；Window 无 Visible() 查询 API（主窗口显示状态用全局标识 mainShown 跟踪）。
- **Fyne 托盘菜单**：菜单最后一项若不带 `IsQuit` 标记，Fyne 会自动补一个本地化的 `Quit` 项（`driver_desktop.go` 的 `addMissingQuitForMenu`，按 `lang.L("Quit")` 比对标签，中文标签匹配不上）。本项目的「退出」项经 `markTrayQuit` 处理——**仅 Linux 打标记**，Windows 保持多一个 Quit 的现状（用户要求）。新增托盘菜单项时注意最后一项的角色。
- **窗口图标与托盘图标是两回事，别互相牵连**：GLFW 建的窗口不设自己的图标就**继承应用图标**（`fyne.App.SetIcon`）——应用图标固定绿色话机（用户要求：标准 app 图标是绿话机，红色只作异常提醒），所以**每个窗口都要自己 `SetIcon(IconPhoneGreen)`**，否则未注册时会顶着红色话机出现（更新提示窗就这么漏过：任务栏那条标题的图标是红的）。反过来，托盘图标在 **Fyne 的托盘就绪回调**里取：显式 `SetSystemTrayIcon` 过就用它，**没设过才退回应用图标**（`internal/driver/glfw/driver_desktop.go` 的 `runSystray`）——应用图标改绿之后必须在 `SetSystemTrayMenu` 之后补一次 `updateTrayIcon()`，否则托盘丢掉"未注册红"的提醒（REQ-UI-011）。`SetSystemTrayIcon` 在托盘就绪前调用只会让底层库记一条 "tray not ready yet"，图标已存下、就绪时照样生效。
- **splash 窗口也会进任务栏（Windows）**：GLFW 给**每个**顶层窗口都设 `WS_EX_APPWINDOW`（`glfw/src/win32_window.c` 的 `getWindowExStyle` 写死），这个扩展样式的作用就是"强制生成任务栏按钮"，会压过 `WS_POPUP` 本来的不占任务栏语义——更新提示窗因此带着一条任务栏标题（用户实测）。**关键是改样式的时机**：只在窗口显示之后再异步改（曾用"显示 → 定位协程里换样式 + hide/show 刷新"）**不够**——外壳是状态驱动地扫描任务栏窗口，往往已经按 APPWINDOW 把按钮建出来了，之后改样式、隐藏都不会把它收回去（用户实测：点「稍后」关掉提示窗后，任务栏上那条按钮还残留着点不动）。正确做法见 `topmost_windows.go` 的 `showUpdateWindow`：在**第一次显示的同一步内**走 Show（首次 Show 才创建 GLFW 窗口）→ 立刻 `w.Hide()` → `setToolWindowStyle(hwnd)`（APPWINDOW 换成 TOOLWINDOW）→ `w.Show()`——外壳还没来得及处理"显示"，窗口已经是工具窗口，按钮根本不会建。`positionUpdateWindow` 里的 `setToolWindowStyle` 只是幂等兜底。⚠️ 读写扩展样式用 `Get/SetWindowLongPtrW`，32 位 user32 没有这两个导出（`LazyProc.Call` 会 panic 且 GUI 子系统无可见输出），必须按 `Find()` 结果回退 `Get/SetWindowLongW`。**通话面板与更新提示窗都按工具窗口显示（不占任务栏）**——面板走 `showCallWindow`、提示窗走 `showUpdateWindow`，两者都进 `showWindowAsTool`；只有视频窗口保留任务栏条目（它是带标题栏、可拖动的普通窗口，用户可能要从任务栏找它）。
- **音频管理器（internal/audio）的线程模型**：后端初始化在后台 goroutine，`NewManager` 最多等 `initWait`(2s) 就返回；所有会碰后端的操作（开关音频流、播放提示音）都排入单 worker 的 FIFO 队列后立即返回，调用方（UI 线程、SIP 线程）不会被设备操作阻塞。**不要持 `m.mu` 调用后端**——miniaudio 的设备操作在音频服务不可达时会长时间阻塞（`PULSE_SERVER=tcp:localhost` 实测 15 秒），持锁会把 UI 线程的 `enqueue`/`SetMic` 一起卡死；正确做法是先取状态快照、释放锁再调后端（见 `startStreamFor`/`stopStream`）。设备枚举 `Devices()` 保持同步，调用方需自行放进后台 goroutine（界面侧见 `refreshDevices`）。后端内部同理：`malgoBackend.toneMu` 只护提示音状态，**不得跨越 `malgo.InitDevice`/`device.Start`/`device.Uninit` 持有**——miniaudio 启动/销毁设备要等音频回调线程，而回调要拿 `toneMu`，持锁即死锁（树莓派 PulseAudio 实测 `ma_device_start` 永不返回，音频 worker 卡死，之后所有音频操作排队不执行、通话无声）。
- **通话面板窗口控制按平台拆分**：`ui_call.go`/`ui.go` 只调用平台函数（`moveCallWindowToCorner`、`callWindowTitle`、`markTrayQuit`、面板尺寸常量），实现分别在 `topmost_windows.go`（Win32）与 `topmost_linux.go`（X11）——**勿把任一侧的逻辑写进共享文件**。Linux 侧靠 `driver.NativeWindow.RunNative` 取 X11 窗口号（回调收到 `driver.X11WindowContext`），需经 `fyne.DoAndWait` 在主线程取；`fyne.DoAndWait` 不可在主线程调用（会 panic），故 `moveCallWindowToCorner` 内部起 goroutine。Wayland 会话下 X11 手段全部无效（不能自定位、去装饰看合成器）——所以 Linux 默认把后端固定到 X11（见下方「Linux 桌面后端」条）。X11 侧还额外把 `_NET_WM_STATE` 写成**窗口属性**（不只发 ClientMessage），因为 Xwayland 下没有独立 X11 窗口管理器处理消息。但**任务栏条目在 labwc 下无法由客户端去掉**（实测属性/消息都不生效）：任务栏走 foreign-toplevel 协议，该协议没有 skip-taskbar 字段，`_NET_WM_STATE_SKIP_TASKBAR` 不会透传。要隐藏只能用 labwc 自己的窗口规则（`~/.config/labwc/rc.xml`，labwc ≥0.6.4）：`<windowRule title="SIP" skipTaskbar="yes" skipWindowSwitcher="yes"/>`——按 `title` 匹配（面板标题恒以 `SIP` 开头，主窗体是 `mti-sip-phone …`），**勿按 `identifier` 匹配**（Xwayland 下两窗口 WM_CLASS 相同，会连主窗体一起隐藏）。用户已确认该条目可接受，不要再在客户端代码里尝试修它。
- **服务端心跳（REQ-ACC-020/021）与 OPTIONS 的关系，别想当然**：**整套机制只对 UDP 传输生效**（`heartbeatWanted()`：开关打开 **且** 传输是 udp；`optchk` 标记、传输层计数、间隔学习、2.5 周期超时重注册四处都受它管，设置页在 tcp/tls 下把勾选项置灰——用户要求）。启用后只是注册报文的 **User-Agent 带 `optchk`**，服务端据此定期发 OPTIONS 探测；**OPTIONS 不要专门处理——sipgo 默认回 405，实测 OpenSIPS 接受**。曾实现过"逐字回抄的 200 + Contact + 重传缓存"，服务端仍判探测失败并摘注册（NAT/rport 下它登记的是改写后的外部地址，与我们的 Contact 对不上）。**心跳计数必须挂在传输层读钩子（`readFilter`）上**：服务端的探测复用**同一个 Via branch**，sipgo 会把 Timer J（32 秒）内相同 branch 的请求当**重传**、不再回调 handler——按 handler 计数会把 10 秒的心跳看成 ~40 秒。超时判定是 **2.5 个周期**（`hbOverdueFactor`）：先置**未注册**（托盘/圆点变红、原因"服务端心跳超时"），再立即重新注册，触发一次后复位。
- **信令日志唯一出口是 `internal/sip/tracer.go` 的全局跟踪器**（sipgo 传输层钩子 + 「SIP 信令记录」开关）：**业务代码里不要再散点调 `logx.TraceSIP`**，否则同一条报文在日志里出现两份（历史缺陷，已清理 11 处）；跟踪器里另有"非 SIP 载荷不记"的过滤（挡掉服务端/NAT 的四个 0 字节保活包）。
- **「软件凭空消失、日志停在半截」= 崩溃输出没有去处，不是没崩**：托盘版是 GUI 子系统
  （`-H=windowsgui`），**没有控制台**——未捕获的 panic、运行期致命错误（并发 map 读写、
  栈溢出）、cgo 里的非法内存访问都只写 **stderr** 后 `exit(2)`；Windows 事件日志**也不会有
  记录**（那是进程"**自己**退出"的，既没有 Application Error 也没有 WER 报告）。所以
  「事件日志里干干净净」**不能**说明没崩。`internal/logx/crash.go`（REQ-LOG-009）现在把这份
  输出落盘到日志目录 `crash.log`（下次启动归档为 `crash-<时间>.log`，并在主日志里 WARN
  「上次是异常退出」）——排查「实例跑着跑着没了」第一站就是它：**空文件 = 不是崩溃**（正常
  退出或被 taskkill 杀掉）。改这里注意：`debug.SetCrashOutput` 内部 **dup** 句柄且**同一进程
  内不会释放**，所以同进程里重复安装（测试）要先 `SetCrashOutput(nil, …)`，否则 Windows 下
  `crash.log` 一直被占、归档改名必失败。
- **日志轮转：Windows 上删除/改名文件前必须先关自己的句柄**（`internal/logx/logx.go` 的 `rotate`/`gzipFile`）。Windows 要求**所有**句柄都带 `FILE_SHARE_DELETE` 才允许删/改名，而 Go 的 `os.OpenFile` 只申请 `FILE_SHARE_READ|FILE_SHARE_WRITE`（`syscall_windows.go` 的 sharemode）——**自己的句柄没关，`os.Remove`/`os.Rename` 必然失败**。历史缺陷写成 `return os.Remove(src)`（在 `defer in.Close()` 之前执行），于是"压缩完成却删不掉源文件"：日志文件永不截断（实测卡在 42.8MB）→ 尺寸一直超上限 → **之后每次写日志都再完整轮转一次**（整个文件重新 gzip 一遍，且全程持写锁）→ 写日志的线程（SIP/界面/**RTP 接收循环**）被随机卡住数百毫秒（实测同一接收循环迭代内相邻两行日志相差 494ms）。症状极具迷惑性：通话开始时的日志突发撞上轮转 → 接收循环停摆、对端媒体包在 socket 里排队后成批涌入 → 播放缓冲超深截断 → **"第一声回铃音断续"**（真因，找了很久）。另外那条路径还会让 `.1.gz~.9.gz` **内容完全相同且每次轮转 mtime 全变**（每次压的都是同一份越来越大的文件）。现在的做法：关闭 → **改名切走**（改名即完成截断）→ 立即开新文件继续写 + 后台 goroutine 压缩与归档移位（不持锁做重活）；改名失败退化为清空当前文件，连清空都不行则降级为"不再轮转"，**绝不能回到"每次写都重压整个文件"的循环**。改这里务必跑 `go test ./internal/logx/`（有回归测试）。
- **媒体/NAT**：对称 NAT 下 UDP 映射需持续出向流量保活（早期媒体阶段 SetKeepalive 持续发静音帧）；SDP 宣告地址与 socket 绑定地址分离（SetSDPIP）；183→200 可能换 SSRC/端口（引擎已自动处理，勿移除 playPush 的重置逻辑）。
- **SRTP（`internal/media/srtp.go`，改之前必读）**：
  - **密钥方向搞反 = 双向全解不开**：RFC 4568 §6.1 的口径是「**SDP 里的 key 是写这条 SDP 的一方
    用来加密它自己发出的媒体**的」——本端发送用**自己**写进 SDP 的密钥，接收用**对端**给的密钥；
    密钥单向、套件双向。answer 还必须**复用 offer 里选中那条的 tag 与 crypto-suite**，只换密钥。
  - **pion 的 `CreateContext` 默认 `SRTPNoReplayProtection`**（重放保护是**关**的），必须显式传
    `SRTPReplayProtection/SRTCPReplayProtection`；否则「防重放」是假的。pion 的 `Context`
    **不保证并发安全**：收发各一个实例、各自带锁（发送侧还会被采集回调与 RTCP 循环并发调用）。
  - **加密启用后不能再有明文出向包**：打洞（`punchHole`）、早期媒体保活、DTMF（`SendDTMF` 自己
    `WriteToUDP`，不经过 `sendPacket`）、RTCP（SRTCP，明文 RTCP 会被对端丢弃 → PLI 发不出去 =
    黑屏）全都要加密。因此**协商必须早于 `setRemote`/`punchHole`**（`SDPAnswerWithDirection`/
    `SDPApplyAnswer`/视频的 `ApplyOffer`/`ApplyAnswer` 里都是这个顺序，别调换）。
  - **每个 m= 行独立密钥**：音频与视频各持一份 `cryptoState`（视频在 `VideoEngine` 上），
    视频段要有自己的 crypto 行；只加密音频不加密视频会被严格的对端拒（488）。
  - **换流（183→200 换 SSRC / 序号大跳变）要 `resetRx`**：接收侧的序号/ROC 推断沿用旧状态会算出
    错误的 ROC，现象是「接通后解密全部失败、没有声音」。反过来，**重协商时若对端的密钥参数没变就
    不要重建上下文**（重建同样清掉 ROC，通话超过约 22 分钟、序号绕回一次后就会解不开）——
    `samePeerCrypto` 的短路就是为这个。
  - **重协商必须重复带 `a=crypto`**（SDES 口径：重协商里不出现 crypto = 取消加密）；非「必要」
    策略下按明文降级并打 Warn，「必要」策略直接回 488。
  - **策略是四档不是开关**：`关闭` 模式下**连对端递过来的加密也不能接受**（`acceptsSRTP()` 的
    判断一开始漏了，被 `TestSRTPRequiredFailsWhenAnswerLacksCrypto` 抓出来）；「支持」= 本端
    offer 不带 crypto（不加密时 SDP 与历史**逐字节一致**，golden 测试钉着）。
  - **密钥绝不能进日志**：SIP 信令记录会把整份 SDP 落盘，`logx.MaskSecrets` 里有一条专门抹
    `a=crypto:… inline:<base64>` 的规则（套件名保留），加新日志/新脱敏规则时别绕开它。
- **自更新重启时序**（勿简化）：重启请求只标记 `restartRequested` 并取消 ctx，**释放单实例锁与拉起新进程必须放在退出收尾**（`shutdown` 之后、进程退出之前，见 main.go 的 `spawnNewProcessIfRequested`）——顺序反了会让新进程因锁冲突转发参数后退出，或因 WS 端口仍被占用而 +1 写回配置、SIP 双注册。历史缺陷：旧 `onRestart` 释放锁+拉起进程后不退出旧进程（已修复）。Windows 上运行中的 exe **能改名不能覆盖**，替换流程为 exe→.old、.new→exe、失败回滚（`internal/updater/replace.go`，已用真实进程验证）。
- **更新提示窗与通话面板都吸附右下角**：通话期间隐藏更新提示窗（`setCallUI` 非空闲分支），通话结束后按 `updatePopupPending` 补弹；不要在两者都可见时做偏移摆放。
- **测试 flakiness**：`internal/sip` 全量并行跑时偶发 5s 超时失败（信令测试间资源干扰，与具体改动无关）；分组或单跑稳定。验证方式：`go test -run "<Test名>"` 单跑 + 分组复跑。
- **malgo 回调帧长不固定**：WASAPI 共享模式下回调 frameCount 不保证等于 20ms（160 样本），不要假设固定帧长读写引擎。
- **build.bat（cmd 解析）**：`if (...)` 块内的 `echo` 文本不要同时出现括号与尾部 `...`——cmd 会把 `(...)` 当命令组，报「此时不应有 ...。」并中断整个构建（不回显 errorlevel）。多分支输出用 `goto` 标签而不要塞进 if/else 块。
- **平台专有文件在别的平台上根本不参与编译，本机的绿灯说明不了任何事**：`*_linux.go`（以及 `_other.go`）被构建约束排除，Windows 上的 `build.bat`、`go test`、`go vet` **全都看不见它们**。踩过：`internal/ui/topmost_linux.go` 用了 `sync`/`atomic` 却没写进 import（2026-10，窗口位置记忆那次加的代码），Windows 侧一路正常，用户跑到 Linux 上 `build.sh` 才报 `undefined: sync`。**改了 linux 专有文件必须到 Linux 上编一次再提交**。Windows 上能做的最小交叉检查是 `GOOS=linux CGO_ENABLED=0 go build -tags noaudio ./internal/...`（只覆盖不带 cgo 的包；`internal/ui` 因 fyne 依赖 cgo 查不到，只能靠 Linux 构建）。另外可以机械扫一遍：找出「用了 `xxx.` 却没 import」的文件（写个一次性脚本比对 import 列表即可，比逐个 file 目测可靠）。
- **32 位 Win32 调用**：`SetWindowLongPtrW` / `GetWindowLongPtrW` 是 **64 位专属导出**，32 位 user32.dll 里没有该符号，`LazyProc.Call` 找不到过程会直接 panic；GUI 子系统（`-H=windowsgui`）无控制台，panic 无任何可见输出，表现为「一操作就静默崩溃」。`topmost_windows.go` 的 `setCallWindowClamp` 用 `Find()` 失败回退到 `SetWindowLongW`（32 位 LONG_PTR 与 LONG 同宽，语义等价）。新增任何 Win32 调用前先确认该导出在 32 位下存在。
- **Linux 桌面后端：Wayland + libdecor（必读）**：`internal/ui/glfw_linux.go` 的 `prepareGLFW()`（在 `ui.Run()` 开头调用，必须早于第一个窗口创建）做两件事：
  - **关闭 libdecor**（`glfw.InitHint(glfw.WaylandLibdecor, glfw.WaylandDisableLibdecor)`）：GLFW 在 Wayland 下默认优先用它画装饰，而 libdecor 释放 frame 时先销毁 xdg_toplevel、后销毁 decoration，违反 xdg-decoration 协议，wlroots 0.17+ 的合成器（labwc/sway）会判定协议错误并**断开该客户端的 Wayland 连接**。连锁反应极具迷惑性：连接已死 → 新建窗口的 EGL 建面必然失败（报错文案是假失败的 `EGL: Failed to create window surface: Success`）→ 而 go-gl 绑定的 `acceptError` 会把 PlatformError **吞成 nil error**，Fyne 因此留下没有 viewport 的僵尸窗口（通话面板显示不出来、之后主窗体也拉不起来），同时主循环卡死在 `glfwPollEvents`，托盘退出与 SIGTERM 全失效（只能 kill -9），但 SIP/WS goroutine 照常工作。
  - **Wayland 会话 + X 显示真的可连接时，把后端固定为 X11**（`os.Setenv("FYNE_PLATFORM","x11")`，探测用 `x11DisplayAvailable()`）：Wayland 协议不允许客户端自定位窗口、去装饰、自行置顶，通话面板只能居中带标题栏；且新映射窗口拿不到指针焦点（合成器防抢焦点，`RequestFocus` 被拒），空窗期里 fyne 的指针位置停在旧值上，**点任何位置都会按旧坐标命中控件**（实测表现为刚弹出时点「接听」却挂断）。走 X11（Xwayland）后这些按 X11 语义正常工作。用户已设 `FYNE_PLATFORM` 时不覆盖；无 `DISPLAY` 或连不上（Xwayland 未运行）时回落原生 Wayland。
  **排查用 `FYNE_PLATFORM=x11|wayland` 强制后端**（Fyne 自带开关，会转成 GLFW 平台提示）；`env -u WAYLAND_DISPLAY` **无效**——GLFW 平台探测失败后会回落到 libwayland 的默认 `wayland-0`，仍旧走 Wayland。
- **视频：answer 必须复用对端的 PT 号**。原来 `negotiateSDP` 返回本地 PT 直接拿去生成 answer，
  音频因为 PCMA/PCMU 是静态 8/0 侥幸无事；视频全是动态 PT（本端固定 98–101），按本地号回会协商串味。
  现在 `Engine.activePT` 记录「线上实际使用的号」（offerer=本端号，answerer=对端号）。
- **视频：answer 的 m= 行数量与顺序必须与 offer 一致**（RFC 3264），不接受视频时输出
  `m=video 0 RTP/AVP ...` 拒绝段而不是省略该行，否则 FreeSWITCH 部分版本回 488。
- **视频：方向必须按媒体类型取**（`SDPDirectionFor(body, "audio")`）。原来只读
  `MediaDescriptions[0]`，对端若把 video 排在前面、或只改视频段方向，音频方向会被误判。
- **视频：关键帧判定必须在完整访问单元上做**，不能用第一个分片——H264 首个负载是
  STAP-A(SPS,PPS)，里面没有 IDR，按分片判会把真正的关键帧误判成帧间帧，等关键帧状态下
  永远等不到、画面一直黑屏。
- **视频：`codecs.H264Packet` 有状态**（内部 `fuaBuffer` 累积 FU-A 分片），换流/重协商/
  关闭视频后再开都必须换新实例（`dep.Reset()`）。
- **视频：SDP 的 rtpmap 编码名不要用 `Codec.RTPMap()`**——它返回 `"PCMA/8000"`，而
  `buildSDP` 会自己拼 `/<ClockRate>`，用它会输出非法的 `a=rtpmap:8 PCMA/8000/8000`。
- **视频：`pion/rtp/codecs` 的 VP9Payloader 会用 VP9 帧头解析器判断是否关键帧**，
  喂合成数据会直接返回 0 个包——VP9 的往返测试必须用真实编码数据（见
  `internal/media/video_cgo_test.go`，需 `-tags video`）。
- **OpenH264 的 C 接口是「vtable 指针的指针」**（`typedef const ISVCEncoderVtbl* ISVCEncoder`），
  调用形式是 `(*svc)->Method(svc, ...)`；C++ 下没有这层间接，照抄 Pion 的 bridge.cpp 会编不过。
  profile/level 在**空间层**上（`sSpatialLayers[0].uiProfileIdc`），不在 SEncParamExt 顶层。
- **视频静态库：四个目标共用一份源码树，切目标前必须清干净生成物**。
  `scripts/build-video-libs.sh` 的 `purge_objects` 会删 `*.o` / `*.a` / `*.d` /
  `*_rtcd.h`，**不要图省事去掉**。踩过两次，症状都极具迷惑性（configure 显示
  target 正确、编译零报错，只在链接时炸）：
  1. 上一目标残留的 `.o` 时间戳比源码新 → make 直接跳过重编 → **32 位库里装进
     64 位目标文件**，链接报一堆 undefined reference；
  2. libvpx 的 `vpx_dsp_rtcd.h` 等由 `rtcd.pl` 按架构生成，沿用 64 位的表会引用
     只存在于 `fwd_txfm_ssse3_x86_64.asm` 的符号 → 32 位链接报 `_ssse3` 未定义。
- **视频静态库：链接必须带 `-lstdc++`**（OpenH264 是 C++ 实现，用到
  `operator delete(void*, unsigned int)` 这类 C++14 定长释放）。Windows 上本包的
  构建含 C++ 源（摄像头 dshow_windows.cpp），Go 会自动改用 g++ 链接；**Linux 上
  本包没有 C++ 源、Go 一律用 `gcc` 链接**，就中招了（实测 Debian 报
  `undefined reference to 'operator new(unsigned long)'` / `__gxx_personality_v0`）。
  两处都在 `internal/video/ffmpeg.go` 的 `#cgo LDFLAGS` 里显式加上。
- **cgo 前导块（`/* ... */`）里的 `//` 注释中不能出现星号加斜杠**——那是 C 块注释的
  结束符，会把整个前导块提前截断，后面的注释文字被当成 Go 代码解析，报一堆
  `non-declaration statement outside function body` / `invalid character '（'`
  （踩过：注释里写 `pthread_` 星号 `sem_` 星号）。改完前导块用 `gofmt -e` 当场查。
- **视频静态库：Windows 链接必须显式 `-lwinpthread`**。mingw 下 libvpx 检测到
  pthread.h 就用 pthread 线程模型，其 `.a` 里有大量 `pthread_*`/`sem_*` 未定义符号
  （实测 70 处）。WinLibs 是 **posix** 线程模型的工具链，gcc/g++ 驱动会**隐式**带上
  libwinpthread（产物本身因 `-extldflags -static` 并不依赖 `libwinpthread-1.dll`），
  本机构建因此从不暴露；Debian 的 mingw 不会自动带，交叉链接报
  `undefined reference to 'pthread_mutex_lock'` / `'sem_post'`。已在
  `#cgo windows LDFLAGS` 显式加上（重复链接同一库是空操作，对两条路径都无副作用）。
- **交叉 Windows 必须显式给 `CXX`**（`build.sh` 已设成与 CC 同前缀的 g++，并在带视频
  时检查它存在）。Go 的 `cfg.DefaultCXX` 对交叉目标返回裸 `"g++"`，也就是**宿主的**
  g++；而 cgo 编 C++ 与「包里含 C++ 源时的链接驱动」都走 CXX。不带视频时 Windows
  交叉没有 C++ 源，所以一直没暴露；`-tags video` 引入摄像头的 C++ 源
  （`internal/video/dshow/dshow_windows.cpp`）后就变成「用 Linux 的 g++ 编 Windows
  源码」——`<iostream>`、`<unistd.h>` 都能找到、**唯独 `<dshow.h>` 找不到**（实测，
  报错极具误导性），链接同样会落到宿主 g++。Debian 上 mingw 的 g++ 是独立包
  （`g++-mingw-w64-*`）。
  **Linux 交叉（amd64 ⇄ arm64）同理，而且更容易中招**：2026-10 之前仓库里一个 C++
  源都没有，所以那条路径一直没设 CXX 也没事；`internal/audio/apm_bridge.cc`（AEC，
  `-tags apm`）进来之后，linux 交叉的链接驱动就变成了**宿主的** `/usr/bin/g++`——
  拿 aarch64 的目标文件去链接，报 `Relocations in generic ELF (EM: 183)` /
  `file in wrong format`，错误里唯一的人话只有 `/usr/bin/g++` 那一行（实测）。
  `build.sh` 的 linux 交叉现在也显式给 CXX（`${CROSS_TRIPLE}-g++`，可用 `CROSS_CXX`
  覆盖），并在「开了视频/AEC 却找不到它」时**跳过该目标并提示装
  `g++-aarch64-linux-gnu`**——C++ 源要 cc1plus、`-lstdc++` 要目标架构的 libstdc++，
  两者都在这个包里（只有交叉 gcc 是不够的）。
- **视频静态库：FFmpeg 必须 `--disable-autodetect`**（只关 `--disable-libdrm` 不够）。
  autodetect 类外部库不受 `--disable-everything` 约束，会按宿主装的开发包自动启用：
  踩过的是 libdrm（走 pkg-config，Linux 构建机装了 libdrm-dev 就把 `hwcontext_drm.o`
  编进 `libavutil.a`，链接报 `undefined reference to 'drmGetVersion'`），同类还有
  zlib/bzlib/lzma/xlib/vaapi/vdpau/vulkan/cuda/nvdec 等——**Windows 宿主没有
  pkg-config 所以从不触发**，交叉 mingw/arm64 时宿主 pkg-config 尤其容易误判。
  全关不影响我们启用的组件；线程（pthreads/w32threads）不在 AUTODETECT 之列。
- **视频静态库：libvpx 的 x86 目标硬性要求 nasm/yasm，没有降级路径**。libvpx 1.15 的
  configure 只认 `--as={yasm|nasm|auto}`，**没有 `--disable-asm`**（脚本原先的「找不到
  汇编器就关闭汇编」分支直接让 configure 报 `Unknown option "--disable-asm"` 失败），
  且 x86 下 `.asm` 源文件必编，缺汇编器时 libvpx 自己就 die。Debian/Ubuntu 上
  `sudo apt install nasm`。失败时脚本会 `rm -rf` 掉该目标目录，`build.sh`/`build.bat`
  也必须逐个检查 5 个 `.a`（libavcodec/swscale/avutil/vpx/openh264，与
  `internal/video/ffmpeg.go` 的 `#cgo LDFLAGS` 对应）才算「库齐全」——**只看 `lib`
  目录存在会把半成品当可用**，`-tags video` 后编译立刻报缺头文件（实测
  `fatal error: wels/codec_api.h`）。
- **无边框窗口一律用 Fyne 的 splash 窗口（`desktop.Driver.CreateSplashWindow`），
  不要在 Win32 侧 `stripWindowChrome` 硬抹样式**。硬抹是**绕过 GLFW** 改窗口的：
  GLFW 缓存的边框尺寸因此永久失准，它仍按"有标题栏"换算客户区，Fyne 于是认为
  画布比真实客户区高出一整条标题栏（"幽灵标题栏"，实测 36 DIP），内容整体下移、
  **排在前面的行被顶出可视区**。现象极具迷惑性：**来电振铃时三行正常**
  （`ringFlashRect` 每 75ms 重绘、视图不断重新同步），**一点接听停止重绘就只剩
  下面两行**（用户实测）。splash 窗口由 GLFW 在创建时就按无边框建立
  （Win32 下 `WS_POPUP`、frameRect 归零），缓存始终自洽。
  通话面板与更新提示窗都已改走这条路，配套删掉了 `stripWindowChrome` /
  `setCallWindowClamp` / `setUpdateWindowClamp` / `clampWndProc` / `subclassWindow` /
  `hideFromTaskbar` / `deleteTaskbarTab` 一整串 Win32 hack；`positionCornerWindow`
  现在**只移动、不改尺寸**（尺寸归 Fyne）。
  注意 `CreateSplashWindow` 是**延迟到 `Show()` 里才建 GLFW 窗口**的（Fyne 的
  `window.create()` 在 `Show` 中调用），所以任何"创建时生效"的 GLFW 窗口提示
  都必须包在 `Show()` 外层，包在 `NewWindow()` 外层是无效的。
- **通话面板自带两个布局，不要换回 Fyne 的 HBox / GridWithRows**：这两个的内部
  间距固定取 `theme.Padding()`（本项目主题为 4），改不了；而 GridWithRows 还会把
  格子拉伸填满可用高度，格子高出内容的部分变成行间富余（实测行距 12px 而列距
  4px，用户反馈"行距偏大且不均匀"）。现在用 `panelRowLayout`（首元素靠左、其余
  靠右，间距 `panelColGap`）与 `panelRowsLayout`（行距 `panelRowGap`、整体纵向
  居中）：**行距 2px、列距 4px**，与窗口高度解耦。
  ⚠️ 别用 `container.NewCenter` 包网格——它连宽度也只给最小宽度，整行会被挤到中间。
- **视频窗口"有内容才显示"与"懒创建"之间有个死锁，别再拆开**：`wantVideoWindow`
  要求先有内容才 Show，而窗口是懒创建的——若 `renderVideoFrame` 在 surface 为空时
  直接返回，帧被丢 → `remoteHasVideo` 永远为 false → 窗口永远不建。摄像头禁用时
  `callSending=false`，窗口只能靠 `remoteHasVideo` 触发，必然踩中（现象：远端画面
  已解码却"没有视频窗口"）。现在协商成功即 `ensureVideoWindow()` 建好窗口对象
  （**不 Show**），且 `renderVideoFrame` 首帧到达时也会就地补建。
- **`callPanelHeight` 用 140 而不是 126**：126 时 3 行 × 36 按钮把行距压成 0，
  按钮上下相连（用户实测"第一行和第二行之间的按钮是连着的"）。140 = 上下留白 8
  + 三行各 36 + 行间 2×8（Fyne 网格自带 4px 行距 + 按钮上下各余 ~2px）。
- **`vPad` 不能用 `container.NewBorder` 拼留白**：Fyne 的 `borderLayout.MinSize` 会
  **额外**加 `theme.Padding()`（本项目主题 4，上下就多 8），每行最小高会变成 46 而不是
  设计注释里的 38，整个面板内容最小高 162 而窗口只有 126——内容比窗口高 36px，全靠
  fyne/GLFW 的"幽灵标题栏高度"侥幸兜着。**那个余量一旦变化（例如开了视频窗口），
  排在前面的行就被挤出可视区**（用户实测的"通话面板第一行不见了"）。现在 `vPad` 用
  自定义 `vPadLayout` 精确控制留白，内容最小高 124 ≤ 126。
  另注：`layout.NewGridLayoutWithRows(n)` 内部是 `{Cols: n}`，行数由
  `ceil(可见对象数 / n)` 现算——所以"少一个可见对象"会直接改变行数，排查时先数对象。
- **视频窗口尺寸上限固定 640px**（`videoMaxW/H`，用户要求）：按对端宽高**等比
  放到 640×640 内的最大尺寸**——长边恒为 640，比 640 小的流也**放大**到窗口
  （对端 192×128 的小流以前只开个 192×128 的小窗，用户要求放大）；只有宽高比
  极端时另一边先顶到 640（900×100 → 640×71）。**不要再按屏幕大小算**（早期按
  工作区 1/4，小屏上窗口被压得太小、大屏上又开得过大）。`videoMinW/H`(160×120)
  现在只是拿不到分辨率时的兜底。
- **视频窗口的尺寸与位置必须一次 `SetWindowPos` 原子完成**，不能"先 Resize
  再定位"。`Window.Resize` 的生效时机不受控（fyne/GLFW 会在随后的帧里再应用
  一次；`moveWindowToCorner` 里那三次延迟校正就是为这个加的），若定位时量到的
  还是旧尺寸，窗口随后按"保持左上角"长高，底边就越过通话面板上沿把面板盖住
  ——用户实测的"通话面板第一行不见了"。见 `placeVideoWindow`：带尺寸时用
  `targetRight + insetR - W` 直接算窗口矩形，并在 150/350/800ms 后只做位置校正。
- **旋转/水平镜像（远端画面与本地回显各一套）是显示层的变换**：桌面在
  `ui.OnVideoFrame`（媒体 goroutine）里用 `transformFrame` 生成新图，**绝不就地
  改原帧**——同一块数据还要给网页订阅的出口（那边有自己的旋转设置）。
  旋转 90°/270° 时宽高互换，窗口尺寸用最新一帧重算（`resizeVideoWindowToLatest`）；
  回显区宽高比跟着变（`previewLayout` 读的是变换后的 `localW/localH`）。
  状态是会话级记忆（不随通话复位），用原子量给媒体 goroutine 读。
  网页侧在 canvas 上用 `rotate/scale` 变换（不吃 CPU），并同步改 `<video>` 的
  `style.aspectRatio` 让框跟着旋转后的宽高。
- **视频：回显开关（`previewOn`）与推送、网页订阅都解耦**。用户明确要求"关闭回显
  只是关闭本地的回显，并不影响到本地视频推送到远端"，后来补充"也不该影响网页
  订阅的本地画面，只要订阅了就该显示，除非禁用视频"。所以本地帧的投递判定是
  `enabled && (previewOn || 网页订阅本地画面)`（`media.VideoEngine.localFramesWanted`，
  网页那一侧由 `core.App.LocalVideoWanted` 经 `SetLocalWantedFunc` 注入逐帧求值）——
  YUV→RGBA 每帧要分配整张图，没人要时就不做这个转换。
  另外 `SetVideoPreview` 既不调
  `syncCamera`（不启停采集）也不改 `sendEnabled`，只门控"本地帧是否投递给界面"；
  采集的启停条件是 `camConfigured && enabled && sendEnabled`（与回显无关）。
- **视频：回显区可见性看"意图"不看"首帧"**。摄像头打开失败时回显区仍要显示（黑底），
  所以可见性由 `enabled && callSending && previewOn` 决定，不能等收到本地帧才显示。
- **视频窗口"有内容才显示"**：`wantVideoWindow()` = 本端在推流且回显开着
  （`callSending && previewOn`，**视频呼叫一拨出就成立**，本地回显立刻可见，
  不等协商/不等对端画面）或对端画面已到（`callHasVideo && remoteHasVideo`）。
  纯音频通话（普通接听、对端无视频）不留空窗口；通话面板的视频开关也只在
  `callHasVideo` 时加进行3。
- **视频呼叫是"拨出即开摄像头"**（用户要求）：`sip.dial` 发完 INVITE 就
  `startVideo`，`applyVideoAnswer` 在三种"对端没有视频"的情形（answer 无视频段 /
  m=video 端口为 0 / 2xx 根本没带 SDP）下 `closeVideo` + `notifyVideoState`——
  后者在引擎已回收时上报**全零状态**，界面据此把窗口和摄像头收回来。
  注意顺序坑：首个 `Sending=true` 状态会在 core 登记通话**之前**上报
  （tel.DialVideo 返回后调用方才 upsertCall），`core.App` 用 `pendingVideo`
  暂存、`upsertCall` 时并入、`DialVideo` 在 `call.dialing` 之后补发一次
  `call.video`（顺序不能反：界面要先有 activeCallID 才认这条事件）。
- **接听的视频策略用 `answerMode` 枚举区分（别用 bool 传）**：`answerNoVideo`
  点普通「接听」→ answer 回 `m=video 0`（用户明确要求：普通接听 = 不接受视频，
  对端也不再发）；`answerSendRecv` 点「视频接听」**和自动接听**（SIP 头
  answer-after / 自动接听开关）→ 摄像头可用时 `sendrecv`，被禁用/无设备时由
  `videoAnswerDirection` 自动退化成 `recvonly`（REQ-VIDEO-005）。
  **踩过**：自动接听曾传 `withVideo=false`（= 拒绝视频），而注释写着 recvonly，
  表现为「视频呼叫自动接听只接通了音频」——`m=video 0`（拒绝，对端也不发）、
  `recvonly`（只收不发）、`sendrecv`（双向）是三个不同概念，别混。
- **视频窗口摆放要直接读通话面板的可见矩形**（`visibleRect` + DWM 扩展框架边界），
  不要自己按工作区+边距算：面板定位用的是**未缩放**的 `callPanelMargin`(16 物理像素)，
  视频窗口若按 `16×缩放` 算，DPI 非 100%（如 125%）时就差 4px。
- **音频：播放缓冲超深截断要丢"最安静"的窗口，不能从最旧端硬切**（`playRing.trimQuiet`）：硬切会把音调从中间切掉，听感是"断续"——实测每通呼出早期媒体建立约 2 秒时都会超深一次（对端回铃音刚开始/设备启动期垫高 ~320ms），被切的恰好是**第一声回铃**（用户报的"第一声回铃断续、第二声正常"就是它）。现在在缓冲区最旧一段里找能量最低的窗口丢（回铃音/语音的停顿足够容纳），找不到安静位置才退回最旧端并在接缝处做 2ms 交叉淡化。
- **音频：播放环形缓冲必须封顶深度**（`playMaxDepth` 500ms → 截到 `playTargetDepth`
  200ms）。读侧（音频回调）恒为实时速率，而写侧会被丢包补静音、对端/网关突发
  一次性垫高；垫高后深度**只增不减**（读多少写多少），表现为"通话越久延迟越大、
  有几秒"。早先容量给到 8 秒，等于允许累积 8 秒延迟。截断时会打
  `播放缓冲超深已截断` 的 Warn（限频 10s），这是排查延迟的第一线索。
- **音量/增益（REQ-MEDIA-006）**：通话面板扬声器/麦克风按钮**滚轮即调音量**，
  0% = 禁用（等同「关」，走 `SetMicMuted`/`SetPlayMuted` 静音标志，**不**下发增益 0
  ——否则别处单独解除静音会得到"没有提示的静音"）。引擎里增益/音量存的是
  **「百分比 − 100」的整数偏移**（`micGainOff`/`playVolOff`）而不是 float32 位模式：
  零值必须等于 100% 单位增益，存位模式时零值恰好是 +0.0（静音）会被误读。
  界面侧同理按**百分比整数**存（`levelState`，0.05 累加 20 次不再等于 1.0）。
  滚轮事件走 `fyne.Scrollable`，但**一格不是 1**——桌面驱动把 yoff 放大成 25
  （`scroll_speed_default.go`），触摸板是连续小量，所以按「累计 DY ÷ 25 取整、
  余数留存」（`wheelSteps`）折算，别写成"一个事件一步"；`|yoff| ≥ 10` 时驱动还会
  乘 125，**单事件步数必须夹取**（`maxWheelSteps`），否则一次滚动就到底。
  按钮底色是**圆周填充**（`compactButton.sweepImage`）：**100% 满蓝**，麦克风
  100%→200% 的增益段在蓝色之上再叠一层**橙色**（`colorLevelBoost`，200% 整块橙）
  ——所以 levelState 的 `level()` 只管"是否满格"，画底色用 `blueLevel()`/`boostLevel()`。
  从正上方起顺时针扫过，
  用 `canvas.Raster` 逐像素画——**Fyne 没有"裁剪到圆角矩形"的能力**，`canvas.Arc`
  满格时是个圆、方形按钮的四个角会露出轨道色，所以这里自己按角度+圆角矩形判定。
  Raster 的生成器拿到的是**设备像素**尺寸（`textureScale`），按 `Size()` 换算比例；
  改 level 后 `Raster.Refresh()` 会让驱动丢掉缓存纹理重新生成（gl 的 `freeTexture`）。
- **滚轮监听不能挂在 `compactButton` 上**：Fyne 的滚轮分发只把事件交给指针下
  **最深的一个** `Scrollable` 且**不冒泡**（glfw 驱动 `processMouseScrolled` →
  `driver.FindObjectAtPositionMatching`），而 `compactButton` 到处都是——通话记录页/
  通信录页的行内「呼叫」按钮就在可滚动列表里，挂上去会把列表的滚轮吞掉。
  音量按钮挂的是透明中继 `scrollRelay`（只实现 `Scrollable`，不实现
  Tappable/Hoverable，排在 Stack 最后），别处零影响。
- **`core.Call` 的 `micGain`/`speakerVolume` 是百分比、且是"恢复值"**：0 表示禁用，
  禁用期间字段保留解禁后回到的音量，当前是否禁用看 `micMuted`/`speakerMuted`；
  新通话在 `upsertCall` 里默认 100（0 有专有含义，不能靠零值）。`upsertCall` 是
  **整份覆盖**，只在通话创建时调用，后续状态一律走 `updateCall`。
- **自动更新：查询在软电话、网页只写配置 + 强制推送（REQ-UPD-017~019）**。软电话按
  `config.Update`（站点/应用 ID/周期/预览版）自己定时查 `GET {站点}/api/v1/apps/{应用}/check`
  （`internal/updater/checker.go`，**不带 channel**，用站点默认通道），查到更高版本走
  `updater.Notify` 同一条链路提示用户。几个坑：① **首次检查要在启动后立即做**，代码里
  用 `first` 标志把首轮 wait 置 0——否则填完站点要干等一个周期；② 首次检查往往**早于界面
  订阅事件**，`system.update` 收不到、同版本又被 `lastNotified` 去重挡掉，界面就永远不弹窗
  ——所以 `ui.Run` 在订阅后要**补取一次 `UpdateStatus()`**；③ 查询结果**不要把站点返回的
  os/arch 塞回 `UpdateInfo`**（站点给的是 `x64/x86`，`Notify` 按 `GOARCH` 严格比对会拒绝）；
  ④ 去重键是「版本|sha256」，改配置时清空。网页侧：`system.setUpdateConfig`（字段可选）
  写配置、`system.notifyUpdate` 强制推送，SDK **不再查询站点**（`notifyUpdate` 恒带 force）。
- **`call.audio` 事件必须合并发布**（`internal/core/audio.go` 的 `audioPublisher`）：
  滚轮/拖滑块一秒能触发十几下，逐条广播会把事件总线（容量 64、满即丢）刷爆、把
  通话状态事件挤掉，也会把网页日志刷屏。合并口径 =「首次立即发 + 窗口内最多补发
  一条（150ms）」，最后一次改动一定会发出去。另外：**静音状态没变就不要调
  `MuteMic`/`MuteSpeaker`**（它们在 core 里会发布事件），0% 的禁用由
  `SetMicGain/SetSpeakerVolume` 内部处理。
- **Opus：RTP 时钟率 48000 与 PCM 帧长 160 不是一回事**。内部 PCM 管线恒为 8kHz
  （设备、播放缓冲、提示音都是 8k），所以编码器/解码器以 `Fs=8000` 创建——窄带工作，
  不需要重采样，对端发宽带/全带包也能解码（libopus 内部降到 8k 输出）。但 SDP 的
  `rtpmap` 与 RTP 时间戳必须按 **48000** 走：`a=rtpmap:96 opus/48000/2`、20ms 帧的
  时间戳步长 **960**。`Codec.FrameSize()` 曾兼任「PCM 帧长」与「时间戳增量」
  （静态 PT 的 G.711 下两者恰好都是 160 才没暴露），现已拆出 **`Codec.TimestampStep()`**，
  `sendPacket` 用它递增 `e.ts`——加新编解码器时别再用 FrameSize 当时戳步长。
- **Opus：编解码器实例是包级共享的（`supportedCodecs` 是原型），带 C 状态**。
  靠可选接口 `resetter`（`Reset()` → `OPUS_RESET_STATE`）在**通话开始（`Start()`）、
  解码流重置（`playPush` 的 SSRC/大跳变分支）、协商切换编解码器**时复位；本应用限
  单通话所以安全。若将来支持多路通话，必须改成每个 `Engine` 一份实例。
  编码器另有一把锁：采集回调（`FeedMic`）与 SIP 线程（`punchHole`/早期媒体保活）会并发调 `Encode`。
- **Opus：answer 不能用本端 PT 反查编码名**。`audioSection` 现在接收「编解码器 + 线上 PT」
  对（`codecPT`），offer 用各自的固定本端号、answer 用 `activePT`（对端号）；此前的
  `codecByPT(pt).Name()` 在动态 PT 下会兜底成 PCMA、时钟率还会写死 8000。
  `matchAudioCodec` 的「静态号回退」也只认 0–95：对端用 96 号却不写 rtpmap 时不能当 Opus。
- **Opus：丢包用 PLC 而不是补静音**（`plcCodec` 可选接口，`fillLoss`）。最多外推 5 帧
  （100ms），再多就补静音——这是把「丢包补静音」的既有语义按编解码器能力细分，
  G.711 无 PLC，行为与从前逐字节一致。
- **DTMF（RFC 4733）与 Opus 的时钟域不同，这是对的**：telephone-event 在本端 SDP 里
  单独宣告 8000 时钟，`SendDTMF` 的 duration 单位就是 1/8000；而事件包携带的 RTP
  时间戳取自音频流（Opus 下是 48000 域）。duration 只看事件自己的时钟，别把两者统一。
- **CGO_CFLAGS/CGO_LDFLAGS 里的路径，Windows 宿主下必须用 `D:/...` 形式，且这是
  "只有第一个路径会被转换"的坑**：go build 只把列表里**第一个** MSYS 形式（`/d/git/...`）
  的路径转成 Windows 形式，其余原样交给原生 gcc → 找不到头文件。实测：`-tags video,audio`
  时报 `fatal error: opus/opus.h: No such file or directory`，而单独 `-tags audio` 却正常
  （单个路径恰好是"第一个"）。`build.sh` 因此用 `pwd -W` 取 `ABS_ROOT`（与
  `scripts/build-video-libs.sh` 的 `abspath` 同理）；`build.bat` 用 `%CD%` 本来就是
  Windows 形式。手工跑 `go test -tags …` 时同理，别用 `$(pwd)`。
- **音频静态库：libopus 是 autotools 工程（视频那三个库都不是），构建时要绕开三个坑**，
  都在 `scripts/build-audio-libs.sh` 里（前两个是 Windows 专有）：
  1. **MAKE 传裸命令名 + `--disable-dependency-tracking`**：Makefile 递归调用 `$(MAKE)`，
     Windows 上递归是 CreateProcess 按 **Windows PATH** 查程序名，而 git-bash 里 `make`
     只是 shell 层解析（PATH 里实际只有 `mingw32-make.exe`），直接给 `make` 会报
     `CreateProcess(NULL, make all-recursive, ...) failed`；automake 的依赖跟踪 bootstrap
     也栽在同一处。
  2. **给 make 传 8.3 短路径的 SHELL**：automake 的 `LIBTOOL = $(SHELL) $(top_builddir)/libtool`，
     mingw32-make 把 `$(SHELL)` 原样拼进配方行，而 sh.exe 在
     `C:\Program Files\Git\usr\bin\`——空格把命令行切碎，编译时报
     `sh: line 1: C:/Program: No such file or directory`。用 `cygpath -d -w $(command -v sh)`
     得到 `C:/PROGRA~1/...` 消除空格，命令行赋值 `SHELL=` 优先于 Makefile 里的 `/bin/sh`。
  3. **mingw 目标必须关掉 libopus 自带的 FORTIFY_SOURCE**：opus 的 configure 用
     `AX_ADD_FORTIFY_SOURCE` 宏（m4/ax_add_fortify_source.m4）无条件加
     `-D_FORTIFY_SOURCE=2`，于是 `memcpy`/`memset` 被换成 `__memcpy_chk`/`__memset_chk`
     ——glibc 的加固符号，mingw 的 C 运行库没有，而最终链接由 **Go 驱动**（不走 gcc 的
     默认 spec，不会自动补 `-lssp`），Debian/Ubuntu 的 mingw-w64 下报
     `undefined reference to __memcpy_chk`（实测；**WinLibs 恰好能过，所以只在 Linux
     主机交叉 Windows 时暴露**）。脚本对 `*mingw*` 目标预设
     `CFLAGS="-O2 -D_FORTIFY_SOURCE=0"`——那个宏先测「是否已定义」，定义了就跳过。
     Linux 目标保留 fortify（glibc 提供符号）。
- **视频：新流的第一包就要发 PLI 催关键帧**（`handleRTP` 的 `!v.hasSeq` 分支）。
  解码要等 IDR，而不少对端（实测 MicroSIP）只在被请求或很久之后才发——不催的话
  表现为"接通后视频窗口迟迟不出画面，开关一下视频（内部会发 PLI）才出来"。
- **视频：丢包时必须 `dep.Reset()` 并置 `waitKey`**，不能只清访问单元。
  H264 的 FU-A 重组缓冲里留着丢包前的分片，不重置会让丢包后的分片续接上去，
  拼出一个"缝合怪" NAL——解码器照单全收，画面就是花屏。同时发 PLI 催对端出 IDR，
  通常只黑几十毫秒。
- **视频：Windows 摄像头必须按 NV12 → YUY2 逐个格式试**。DirectShow 侧用
  `ISampleGrabber::SetMediaType` 固定格式，摄像头不支持就**整条采集链路失败**。
  很多 USB 摄像头只给 YUY2，固定 NV12 会一台都打不开——症状是「本地回显和发送
  都没有，但远端视频正常」（接收链路与摄像头无关）。
- **视频窗口不置顶**（用户要求）；位置用 `moveVideoWindowAboveCallPanel`：
  **靠 `DwmGetWindowAttribute(DWMWA_EXTENDED_FRAME_BOUNDS)` 的可见矩形做相对修正**，
  不能直接用 `GetWindowRect`——它含不可见的调整边框，比无边框的通话面板在右边
  多出约 8px，表现为"右边缘对不齐"。
- **视频渲染用 `canvas.ImageScaleSmooth`**，不是 `ImageScaleFastest`：后者是最近邻，
  视频窗口通常比源分辨率大，放大后观感发糊/有块（"清晰度不够"的直接来源）。
  GL 路径下平滑只是个纹理过滤开关，开销可忽略。
- **视频窗口的标题前缀不能与通话面板相同**。Windows 侧靠
  `strings.HasPrefix(title, "通话面板")` 找窗口（`findWindowByTitlePrefix`），
  视频窗口若沿用 `callWindowTitle() + " 视频"` 就会被当成通话面板——
  `moveCallWindowToCorner` 会对它去标题栏、`setCallWindowClamp` 把它钳到
  260×126、并拉到右下角压在面板上。用户实测现象是「视频窗口没有标题栏、
  不能拖动、挡在通话面板上、画面一片黑」（260×126 里回显只有 87×42）。
  现在用独立前缀 `视频通话`，并改用 `moveVideoWindowAboveCallPanel` 摆放
  （保留装饰、不锁尺寸、只改位置）。
- **视频：帧出口必须经 `core.App.VideoFrameSink()` 取，不能把 `p.sink` 断言成
  `core.VideoSink`**。`sip.Phone.sink` 是 `core.App`（只实现 `EventSink`），真正实现
  `VideoSink` 的是界面层。断言恒失败 → 出口恒为 nil → 编译运行都不报错，只是
  **本地和远端画面全黑**，排查时极易误判成编解码问题。
- **视频：编码器必须按「实际帧尺寸」建，不是请求尺寸**。摄像头驱动不保证按请求
  分辨率出图（Windows DirectShow 会挑最接近的一档）。若编码器按 640×480 建、
  帧按 352×288 喂，OpenH264/libvpx 会按 640×480 去读一块更小的缓冲——
  **越界读，进程直接崩溃**（"视频接听后有时崩溃"的根因）。`captureLoop` 每帧比对
  尺寸不一致就重建编码器；C 侧 `sp_enc_encode` 还有一道 `inW < e->w` 的硬校验兜底。
- **视频：answer 的视频段必须用对端 PT 号，且不能拿它去查本端编码表**。
  本端表里是 98–101，对端 offer 可能是 100/101/102…；用 `videoCodecByPT(peerPT)` 查
  必然落空 → 段为 nil → 上层输出 `m=video 0`（等于拒绝视频）→ 两边都看不到画面。
  协商结果已存在 `VideoEngine.codec/peerPT` 里，用 `answerSection()` 直接取。
- **视频：对端只声明 `packetization-mode=0`（单 NAL）时不能拒掉整条 m=video**。
  该参数说的是**对端自己怎么发**（对端只发单 NAL），不限制本端接收——解包器
  （pion `codecs.H264Packet`）对单 NAL / STAP-A / FU-A 一视同仁。实测 FreeSWITCH
  呼入视频电话的 offer 就是 `a=fmtp:102 profile-level-id=42000a; packetization-mode=0`
  （FreeSWITCH 的视频是 passthrough，且它根本不解析 H264 fmtp），早期按 RFC 严格
  解读直接跳过 H264 → 200 OK 回 `m=video 0` → **接听后两边全黑**（用户实测）。
  现在照常接受，且 answer 的 fmtp 回**对端声明的模式**（answer 只能是 offer 的子集，
  回本端的 1 会被严格的对端判 488；模板其余参数不动，见 `answerVideoFMTP`）；
  发送侧仍按 pion 的 H264 打包器走（塞不进 MTU 就用 FU-A 分片）——真按模式 0 只发
  单包等于发不出画面。若哪天遇到确实解不了 FU-A 的对端，可给 OpenH264 开
  `SM_SIZELIMITED_SLICE`（`iMaxNalSize` 已在用，现被 slice mode 0 抵消，编码时
  有告警）让每个 NAL 都塞得进 MTU，从而合法地只发单包。
- **视频：采集 goroutine 不计入 `VideoEngine.wg`**。`Close()` 会 `wg.Wait()`，
  若采集侧并发 `wg.Add(1)` 会触发 Go 运行时的
  `sync: WaitGroup misuse: Add called concurrently with Wait` panic。
  启停由 `cameraSupervisor` 串行管理并自行等待（`stopCapture`）。
- **构建脚本：cmd 的 `set` 里 `\t`/`\v` 会被 Python 等工具写成真正的控制字符**，
  表现为 `third_party\video` 变成 `third_party<VT>ideo`，且 `if not exist` 静默判假。
  改 `build.bat` 后务必 `grep | cat -A` 确认。
- **紧凑列表要自绘文本，别用 `widget.Label`**：本项目主题把
  `SizeNameInnerPadding` 提到 12（给拨号盘大按键用，见 `theme.go`），而
  `widget.Label` 的 MinSize 是「文字高 + 2×InnerPadding」——
  **12px 文字的一行标签就有 43px 高**，两行 86px。通话记录行高 46px 却塞两行，
  一开始就表现为**两行文字直接叠在一起**（渲染截图里一眼可见）。行内文本因此
  用自绘的 `plainText`（`internal/ui/widgets.go`，基于 `canvas.Text`，没有内边距）
  + 按可用像素宽度截断补省略号（`Resize` 时才知道分到多宽，`canvas.Text` 自己
  不会截断，不处理会画到相邻控件上）。同一个坑适用于任何"一行要放多个标签"的
  紧凑布局。**按钮别自己照着画**：先试 Fyne 的
  `widget.Button`（`Importance` 选档：`DangerImportance` = 主题错误红）。自绘
  很容易画歪——踩过的坑：文字忘了加粗（Fyne 按钮用的是 `RichTextStyleStrong`）、
  圆角取错尺寸项（按钮是 `SizeNameButtonRadius`）、没有悬浮反馈、用
  `container.NewBorder` 拼内边距被 borderLayout 额外加 `theme.Padding()` 导致
  上下不居中。**但有一个例外要有心理准备**：Fyne 的 `widget.Button.Cursor()`
  返回 `DefaultCursor`（箭头），而本项目的自绘按钮（`compactButton` 等）都是
  手型；要手型只能自绘、或在按钮上叠一层透明的 `Cursorable` 覆盖层（指针取
  "指针下最深的光标对象"）。
  内页顶栏统一用 `pageTopBar`（返回图标 + 标题），
  配置页与通话记录页共用一个样式。
- **SVG 图标里不要用 `<g transform>`**：Fyne 的 SVG 渲染器（fyne-io/oksvg）
  不认这个变换，**整张图会渲染成空白**——同一批图标里，单 `<path>` 的（垃圾桶、
  清空、原有的一堆）都正常，只有带 `<g transform="translate(...) scale(...)">`
  的方向图标全白，排查时很容易误判成"图标文件没打包"或"颜色不对"。
  需要组合图形（如通话记录的方向图标 = 话机/摄像头 + 箭头）就把**每个图元各出
  一张 SVG，再用两个 `canvas.Image` 叠放**（见 `ui_history.go` 的
  `historyDirIconLayout`）。
- **通话记录列表的手势与滚动加载都在"列表容器"这一层做**
  （`internal/ui/ui_history.go` 的 `historyListView`）：
  Fyne 取**指针下最深的匹配对象**（`driver.FindObjectAtPositionMatching` 走完整棵树、
  后匹配的覆盖先匹配的），行本身与行内按钮都不是 `Draggable`，所以整片列表区域的
  按下/拖动都会落到实现了 `Draggable`/`desktop.Mouseable` 的容器上，而且事件坐标
  是相对该容器的——换算行号只需 `y / 行高`，**不需要知道滚动偏移**（这点很关键：
  `widget.List` 不导出滚动偏移，行号映射做不了，所以这里用
  `container.NewVScroll` + 自定行布局，而不是 `widget.List`）。
  - 修饰键（Shift）只有 `desktop.Mouseable` 的 `MouseEvent.Modifier` 有，
    `fyne.PointEvent` **不带修饰键**——所以 Shift 多选必须实现 `Mouseable`。
  - 拖动结束后驱动仍会调一次拖动对象的 `MouseUp`（在 `DragEnd` **之前**），
    用 `dragging` 标志挡掉，否则拖完还会当成一次单击。
  - 滚动加载用 `container.Scroll.OnScrolled`（`widget.List` 把这个回调内部占掉了，
    没有等价物）；一屏没填满时没有滚动事件，加载完要**再主动判一次**是否该继续加载。
  - 选中集合按 `call_id` 存，不按行号（重新加载、筛选、删除都会让行号整体重排）。
- **Fyne 的测试驱动里 `fyne.Do` 是"内联执行"的**（`test/driver.go`：测试都在单线程跑，
  `DoFromGoroutine` 直接调 `f()`），于是后台 goroutine 的 `fyne.Do(刷新界面)` 会和测试
  主线程并发跑同一棵控件树，**表现是字体整形库随机 panic**（`go-text/typesetting`
  的 `index out of range`，每次崩的字符串都不一样）。所以涉及后台加载的界面
  （如通话记录窗口）**不要**写成渲染测试；纯文案/逻辑函数照常测。真实驱动下
  `fyne.Do` 会投递到主循环，不存在这个问题。
- **改完内嵌网页 / JS SDK 后，用无头 Edge 跑一遍页面查运行期错误**：控制台页面与 SDK
  是**内嵌进二进制**的（改完必须重新构建 + 重启，否则页面还是旧的——踩过：SDK 改坏后页面报
  `Uncaught TypeError: this._startHeartbeat is not a function`，本地文件已修好却没重建，
  看页面仍旧报错）。验证命令（Edge 在 Win11 自带，路径见下）：
  `"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe" --headless=new --disable-gpu
  --enable-logging=stderr --v=1 --virtual-time-budget=6000 --dump-dom http://127.0.0.1:8765/`
  → `grep -i uncaught` 看有没有 JS 运行期错误，`--dump-dom` 里能看到 JS 填出来的内容
  （如「当前版本：1.0.12-rc（windows/amd64）」）说明 SDK 连上并跑通了。改 SDK 时另外用脚本
  对一遍「`this.xxx()` 调用是否都有定义」——**大段删代码时最容易连带删掉无关方法**
  （这次就是删更新查询时把 `_emit` / `_startHeartbeat` 一起删了，页面直接白屏）。
- **rtp_rx.wav**：根目录与 internal/sip/ 下的抓包调试产物，已被 .gitignore 忽略，勿误提交。
