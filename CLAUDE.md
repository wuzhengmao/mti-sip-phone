# CLAUDE.md

本文件为 Claude Code 提供项目说明，目标是快速、准确地理解与修改本代码库。

## 项目概述

mti-sip-phone：Go 跨平台 SIP 软电话（Windows / Linux，x86_64 + arm64）。常驻系统托盘，支持 SIP 注册、呼出/呼入、PCMA/PCMU 通话、保持、DTMF；提供本地 WebSocket 控制接口（网页拨号/接听/挂断、事件推送）与 JS SDK；支持 sip:// 协议拉起与开机自启。

当前版本 **1.0.3**（dev 开发中；1.0.2 已发布到 master）。版本号从 1.0.0 起，**不自动升版**——升版须经用户明确确认。

真实环境：OpenSIPS 2.4.11（10.168.4.40:15060，账号 2999），对端 FreeSWITCH（10.168.4.118，桥接话机 2901）。OpenSIPS 配了 rtpengine 但服务未运行（"no available proxies"属正常现象，不用处理）；LAN 环境建议关闭 STUN。

## 技术栈

| 领域 | 选型 | 说明 |
| --- | --- | --- |
| SIP 信令 | github.com/emiago/sipgo v1.6.0 | UDP/TCP，DialogClientSession / DialogServerSession |
| 媒体 | pion/rtp、pion/rtcp、pion/sdp/v3、pion/stun、github.com/zaf/g711 | RTCP 为 v1.2.x 新 API（CompoundPacket/Goodbye，非旧版 Bye） |
| 音频设备 | github.com/gen2brain/malgo v0.11.26 | miniaudio 绑定，**CGO**；`-tags noaudio` 切空后端（纯 Go 构建/测试） |
| GUI | fyne.io/fyne/v2 v2.8.1 | 要求 UI 运行在主线程 |
| WebSocket | github.com/coder/websocket | 仅监听 127.0.0.1，无 token 鉴权 |
| 其他 | google/uuid、golang.org/x/sys（Windows 注册表） | |

## 构建、测试与运行

```bash
# Windows 一键构建（自动探测 WinLibs gcc 作为 CGO 工具链），产物输出到 dist/：
#   dist/sip-phone-windows-amd64.exe          托盘版（GUI 子系统无控制台，CGO+音频）
#   dist/sip-phone-console.exe                控制台版（-headless 无界面模式/命令行调试）
#   dist/sip-phone-windows-386.exe            32 位托盘版（需 i686 工具链，缺失则跳过该目标）
build.bat

# Linux 一键构建（本机 GUI 版 + 交叉 Windows GUI / 对侧 Linux 架构版，均 CGO+音频，输出 dist/）：
#   dist/sip-phone-linux-<本机arch>           本机 Linux 托盘版
#   dist/sip-phone-linux-<对侧arch>           交叉版（amd64 主机→arm64，arm64 主机→amd64；需对侧
#                                             架构的交叉 gcc + 图形栈开发包，缺失则跳过并打印准备步骤）
#   dist/sip-phone-windows-amd64.exe          Windows amd64 托盘版（交叉，需 x86_64-w64-mingw32-gcc）
#   dist/sip-phone-windows-386.exe            Windows 386 托盘版（交叉，静态，需 i686-w64-mingw32-gcc）
./build.sh

# 测试：CGO 包（audio）需要 gcc。用 build.bat 同款工具链：
export CC="<WinLibs mingw64/bin/gcc.exe 路径>"   # build.bat 输出里可见
export CGO_ENABLED=1
go test ./internal/media/ ./internal/wsapi/ ./internal/sip/ ./internal/ui/ ./internal/updater/

# 运行
dist/sip-phone-windows-amd64.exe       # 托盘常驻；关闭主窗口=隐藏到托盘
dist/sip-phone-console.exe -headless   # 无界面模式（仅 WS 服务，供服务器）
```

测试页地址：`http://127.0.0.1:<WS端口>/`（默认 8765，占用自动 +1 并保存配置）。

**交叉编译说明**：fyne 的 OpenGL 绑定依赖 CGO，需要**目标平台**的 gcc 工具链。

- **Linux → Windows GUI**：用 mingw-w64 直接交叉（`build.sh` 已内置，
  `-H=windowsgui`）。glfw 的 C 源码与 mingw 专用头（`glfw/deps/mingw`）随
  go-gl/glfw 模块自带，malgo 自带 miniaudio.c，链接目标只有 mingw 自带的
  系统导入库，**无需 Windows SDK**。386 目标加 `-extldflags -static` 免 DLL。
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

> 注意：`internal/protocol` 的 Windows 注册表实现按构建标签拆分在
> `register_windows.go`（导入 x/sys/windows/registry 会破坏 Linux
> 交叉编译，勿合并回 protocol.go）。`cmd/sip-phone` 的 GUI 组装同理
> 拆分在 `gui.go`（构建标签 !headless）。

## 目录结构与职责

```
cmd/sip-phone/main.go   入口：命令行/单实例/协议注册/开机自启/组装各层/GUI 主循环
internal/
  config/     JSON 配置加载与保存（密码 AES-256-GCM 加密、原子写入）
  logx/       slog 日志：滚动+gzip 归档、信令跟踪 TraceSIP（自动脱敏）
  core/       应用核心：状态机、事件总线（发布-订阅）、Telephony 抽象接口
  sip/        SIP 电话层（phone.go 为核心：注册/呼叫/保持全部信令与竞态处理）
  media/      媒体引擎：RTP/RTCP 收发、PCMA/PCMU 编解码、播放环形缓冲、SDP 协商
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
web/
  test.html   测试页面（内嵌进二进制，右列事件日志）
  sdk/        JS SDK（sip-phone-sdk.js + .d.ts，无 token）
docs/         requirements.md（需求）、ws-protocol.md（WS 协议）、changelog.md
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
- **自动更新**：业务前端（网页）经 WS `system.notifyUpdate` 告知新版本（软电话自身不查更新服务器）→ `internal/updater` 校验并发布 `system.update` 事件 → UI 弹右下角提示窗 + 主窗体提示行 → **用户点击后**才下载、替换 exe 并重启（WS 不能触发下载/重启）。下载/替换的进度与失败原因同样经总线事件到 UI 与网页。
- **media.Engine 与音频设备解耦**：音频回调调用 `FeedMic`（任意帧长，内部按 20ms 帧累积发送）/ `ReadPlayback`（任意帧长，内部从样本环形缓冲取）。
- **播放缓冲**（media/jitter.go）：样本粒度环形缓冲 8 秒容量；RTP 按序号处理——乱序/过期丢弃、丢包填静音（PLC 用静音而非循环旧帧）、SSRC 切换或序号大跳变（>2 秒）重置缓冲（183 早期媒体 → 200 新媒体流的关键）。
- **保持语义**：本端保持发 re-INVITE（offer `a=sendonly`，恢复 `a=sendrecv`）；对端 re-INVITE 按方向镜像应答（对端 `recvonly` → 本端 `sendonly` 且停止发送）。

## 关键信令流程（sip/phone.go）

- **呼出**：Dial → INVITE（SDP offer，PCMA 优先）→ WaitAnswer（180/183 回调里 OnRinging；183 带 SDP 即早期媒体：SDPApplyAnswer → SetMuted(true)+SetKeepalive(true) → attachAudio+Start）→ 2xx → SDPApplyAnswer → ACK → SetKeepalive(false)+SetMuted(false) → OnEstablished。
- **呼入**：handleIncoming → 忙检查（`len(p.calls)>0` 直接 486 Busy Here）→ SDPAnswer 解析 offer → OnIncoming → 180 / 自动接听 → Answer 后台 goroutine RespondSDP（200+SDP，阻塞等 ACK）→ attachAudio+Start → OnEstablished。
- **挂断竞态处理**（多处历史 bug，勿简化）：
  - 呼入振铃中挂断=拒接：**603 必须先于 ctx cancel 发出**（handleIncoming 退出后事务被终止，603 发不出去）；
  - 呼出 CANCEL 与对端 2xx 竞态：收到 2xx 后必须补 ACK+BYE（RFC 3261 §9.1）；
  - 2xx 已收但本地已挂断：补 ACK+BYE。
- **保持 re-INVITE**：TransactionRequest + 手动读响应 + 手动 ACK。**ACK 必须复用 re-INVITE 请求自身的 From/CallID/CSeq 头**（用初始 INVITE 的头会导致对端校验失败——呼入保持无法取消的历史 bug）；OnRetransmission 补发 ACK；事务保留 5 秒后 Terminate。

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

**四个目标的可行性**（都是交叉编译，宿主机位数无关）：

| 目标 | FFmpeg | libvpx | OpenH264 | 摄像头 |
| --- | --- | --- | --- | --- |
| windows-amd64 | ✓ | ✓ | ✓ | ✓ |
| windows-386 | ✓ | ✓ | ✓ | **不可用**（见下） |
| linux-amd64 / arm64 | ✓ | ✓ | ✓ | ✓（V4L2） |

Windows 386 的摄像头**编不过**，是上游限制：mediadevices 的 `camera_windows.hpp`
回调 `HRESULT SampleCB(...)` 漏了 `STDMETHODCALLTYPE`，而 mingw 的 `qedit.h` 里
`ISampleGrabberCB::SampleCB` 带 `__stdcall`。x86_64 上 `__stdcall` 被忽略所以能编，
**i686 上是真实的调用约定冲突**。用 `-D__stdcall=` 强行编会让整份 COM 调用变 cdecl，
运行期必崩，不要试。所以 386 走 `camera_other.go`：编解码可用、摄像头不可用。

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
- 测试页用 `sdk.createVideoRenderer(videoEl)`：canvas + `captureStream()` 喂 h5 `<video>`
  （MSE 不支持 MJPEG，逐帧 blob URL 开销大），渲染跟不上直接丢帧。

## 重要约定

- **交流、文档、注释、提交信息、界面文案一律中文**。
- **Git 工作流**：开发在分支上进行（工作分支 dev，master 为收口分支）；**会话中修改的代码不得自动提交**——提交与 push 必须经用户人工确认后执行，且**不自动合并到 master**（合并 master 需用户明确指示）。
- 版本 1.0.3 不自动升版；changelog.md 只记录版本之间的变化（开发过程不记）。
- 弹窗按钮文案「确定/取消」。
- 需求变更同步更新 docs/requirements.md 与 changelog.md。
- 单通话限制：呼叫/振铃/通话中禁止新呼出（返回错误）；新来电直接 486 USER_BUSY。
- WS 端口可配置（默认 8765），被占用自动 +1 并保存；无 token 鉴权（仅回环）。
- SDP 编解码优先级：PCMA > PCMU；RTCP 端口 = RTP 端口 + 1（SDP `a=rtcp` 宣告）。

## 已知陷阱（修改相关代码前必读）

- **sipgo**：
  - `DialogStateEnded=3 > DialogStateEstablished=1`，振铃结束判断必须用 `== DialogStateEnded`，不能用 `< Established`；
  - 2xx 的 ACK 是独立事务走 OnAck handler；RespondSDP 按 RFC 6026 阻塞等待 ACK（最长 64×T1=32s），必须放后台 goroutine；
  - 事务层对 CANCEL 自动回 200+487；ServerTx 无最终响应时 TerminateGracefully 会立即 Terminate（后续 Respond 必失败）；
  - Timer_I=T4=5s：re-INVITE 事务需手动延迟 Terminate 防止重传风暴。
- **Fyne**：UI 必须主线程（goroutine 运行会托盘失效+线程告警）；dialog 的 parent 传 nil 直接 panic（主窗口未显示时不要弹 dialog）；`SetSystemTrayWindow` 会覆盖 CloseIntercept（需重新接管）并注册单击回调；Window 无 Visible() 查询 API（主窗口显示状态用全局标识 mainShown 跟踪）。
- **Fyne 托盘菜单**：菜单最后一项若不带 `IsQuit` 标记，Fyne 会自动补一个本地化的 `Quit` 项（`driver_desktop.go` 的 `addMissingQuitForMenu`，按 `lang.L("Quit")` 比对标签，中文标签匹配不上）。本项目的「退出」项经 `markTrayQuit` 处理——**仅 Linux 打标记**，Windows 保持多一个 Quit 的现状（用户要求）。新增托盘菜单项时注意最后一项的角色。
- **音频管理器（internal/audio）的线程模型**：后端初始化在后台 goroutine，`NewManager` 最多等 `initWait`(2s) 就返回；所有会碰后端的操作（开关音频流、播放提示音）都排入单 worker 的 FIFO 队列后立即返回，调用方（UI 线程、SIP 线程）不会被设备操作阻塞。**不要持 `m.mu` 调用后端**——miniaudio 的设备操作在音频服务不可达时会长时间阻塞（`PULSE_SERVER=tcp:localhost` 实测 15 秒），持锁会把 UI 线程的 `enqueue`/`SetMic` 一起卡死；正确做法是先取状态快照、释放锁再调后端（见 `startStreamFor`/`stopStream`）。设备枚举 `Devices()` 保持同步，调用方需自行放进后台 goroutine（界面侧见 `refreshDevices`）。后端内部同理：`malgoBackend.toneMu` 只护提示音状态，**不得跨越 `malgo.InitDevice`/`device.Start`/`device.Uninit` 持有**——miniaudio 启动/销毁设备要等音频回调线程，而回调要拿 `toneMu`，持锁即死锁（树莓派 PulseAudio 实测 `ma_device_start` 永不返回，音频 worker 卡死，之后所有音频操作排队不执行、通话无声）。
- **通话面板窗口控制按平台拆分**：`ui_call.go`/`ui.go` 只调用平台函数（`moveCallWindowToCorner`、`callWindowTitle`、`markTrayQuit`、面板尺寸常量），实现分别在 `topmost_windows.go`（Win32）与 `topmost_linux.go`（X11）——**勿把任一侧的逻辑写进共享文件**。Linux 侧靠 `driver.NativeWindow.RunNative` 取 X11 窗口号（回调收到 `driver.X11WindowContext`），需经 `fyne.DoAndWait` 在主线程取；`fyne.DoAndWait` 不可在主线程调用（会 panic），故 `moveCallWindowToCorner` 内部起 goroutine。Wayland 会话下 X11 手段全部无效（不能自定位、去装饰看合成器）——所以 Linux 默认把后端固定到 X11（见下方「Linux 桌面后端」条）。X11 侧还额外把 `_NET_WM_STATE` 写成**窗口属性**（不只发 ClientMessage），因为 Xwayland 下没有独立 X11 窗口管理器处理消息。但**任务栏条目在 labwc 下无法由客户端去掉**（实测属性/消息都不生效）：任务栏走 foreign-toplevel 协议，该协议没有 skip-taskbar 字段，`_NET_WM_STATE_SKIP_TASKBAR` 不会透传。要隐藏只能用 labwc 自己的窗口规则（`~/.config/labwc/rc.xml`，labwc ≥0.6.4）：`<windowRule title="SIP" skipTaskbar="yes" skipWindowSwitcher="yes"/>`——按 `title` 匹配（面板标题恒以 `SIP` 开头，主窗体是 `mti-sip-phone …`），**勿按 `identifier` 匹配**（Xwayland 下两窗口 WM_CLASS 相同，会连主窗体一起隐藏）。用户已确认该条目可接受，不要再在客户端代码里尝试修它。
- **媒体/NAT**：对称 NAT 下 UDP 映射需持续出向流量保活（早期媒体阶段 SetKeepalive 持续发静音帧）；SDP 宣告地址与 socket 绑定地址分离（SetSDPIP）；183→200 可能换 SSRC/端口（引擎已自动处理，勿移除 playPush 的重置逻辑）。
- **自更新重启时序**（勿简化）：重启请求只标记 `restartRequested` 并取消 ctx，**释放单实例锁与拉起新进程必须放在退出收尾**（`shutdown` 之后、进程退出之前，见 main.go 的 `spawnNewProcessIfRequested`）——顺序反了会让新进程因锁冲突转发参数后退出，或因 WS 端口仍被占用而 +1 写回配置、SIP 双注册。历史缺陷：旧 `onRestart` 释放锁+拉起进程后不退出旧进程（已修复）。Windows 上运行中的 exe **能改名不能覆盖**，替换流程为 exe→.old、.new→exe、失败回滚（`internal/updater/replace.go`，已用真实进程验证）。
- **更新提示窗与通话面板都吸附右下角**：通话期间隐藏更新提示窗（`setCallUI` 非空闲分支），通话结束后按 `updatePopupPending` 补弹；不要在两者都可见时做偏移摆放。
- **测试 flakiness**：`internal/sip` 全量并行跑时偶发 5s 超时失败（信令测试间资源干扰，与具体改动无关）；分组或单跑稳定。验证方式：`go test -run "<Test名>"` 单跑 + 分组复跑。
- **malgo 回调帧长不固定**：WASAPI 共享模式下回调 frameCount 不保证等于 20ms（160 样本），不要假设固定帧长读写引擎。
- **build.bat（cmd 解析）**：`if (...)` 块内的 `echo` 文本不要同时出现括号与尾部 `...`——cmd 会把 `(...)` 当命令组，报「此时不应有 ...。」并中断整个构建（不回显 errorlevel）。多分支输出用 `goto` 标签而不要塞进 if/else 块。
- **32 位 Win32 调用**：`SetWindowLongPtrW` / `GetWindowLongPtrW` 是 **64 位专属导出**，32 位 user32.dll 里没有该符号，`LazyProc.Call` 找不到过程会直接 panic；GUI 子系统（`-H=windowsgui`）无控制台，panic 无任何可见输出，表现为「一操作就静默崩溃」。`topmost_windows.go` 的 `setCallWindowClamp` 用 `Find()` 失败回退到 `SetWindowLongW`（32 位 LONG_PTR 与 LONG 同宽，语义等价）。新增任何 Win32 调用前先确认该导出在 32 位下存在。
- **Linux 桌面后端：Wayland + libdecor（必读）**：`internal/ui/glfw_linux.go` 的 `prepareGLFW()`（在 `ui.Run()` 开头调用，必须早于第一个窗口创建）做两件事：
  - **关闭 libdecor**（`glfw.InitHint(glfw.WaylandLibdecor, glfw.WaylandDisableLibdecor)`）：GLFW 在 Wayland 下默认优先用它画装饰，而 libdecor 释放 frame 时先销毁 xdg_toplevel、后销毁 decoration，违反 xdg-decoration 协议，wlroots 0.17+ 的合成器（labwc/sway）会判定协议错误并**断开该客户端的 Wayland 连接**。连锁反应极具迷惑性：连接已死 → 新建窗口的 EGL 建面必然失败（报错文案是假失败的 `EGL: Failed to create window surface: Success`）→ 而 go-gl 绑定的 `acceptError` 会把 PlatformError **吞成 nil error**，Fyne 因此留下没有 viewport 的僵尸窗口（通话面板显示不出来、之后主窗体也拉不起来），同时主循环卡死在 `glfwPollEvents`，托盘退出与 SIGTERM 全失效（只能 kill -9），但 SIP/WS goroutine 照常工作。
  - **Wayland 会话 + X 显示真的可连接时，把后端固定为 X11**（`os.Setenv("FYNE_PLATFORM","x11")`，探测用 `x11DisplayAvailable()`）：Wayland 协议不允许客户端自定位窗口、去装饰、自行置顶，通话面板只能居中带标题栏；且新映射窗口拿不到指针焦点（合成器防抢焦点，`RequestFocus` 被拒），空窗期里 fyne 的指针位置停在旧值上，**点任何位置都会按旧坐标命中控件**（实测表现为刚弹出时点「接听」却挂断）。走 X11（Xwayland）后这些按 X11 语义正常工作。用户已设 `FYNE_PLATFORM` 时不覆盖；无 `DISPLAY` 或连不上（Xwayland 未运行）时回落原生 Wayland。
  **排查用 `FYNE_PLATFORM=x11|wayland` 强制后端**（Fyne 自带开关，会转成 GLFW 平台提示）；`env -u WAYLAND_DISPLAY` **无效**——GLFW 平台探测失败后会回落到 libwayland 的默认 `wayland-0`，仍旧走 Wayland。
- **视频：answer 必须复用对端的 PT 号**。原来 `negotiateSDP` 返回本地 PT 直接拿去生成 answer，
  音频因为 PCMA/PCMU 是静态 8/0 侥幸无事；视频全是动态 PT（96–99），按本地号回会协商串味。
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
  `operator delete(void*, unsigned int)` 这类 C++14 定长释放）。Windows 上 386 目标因
  摄像头被排除、Go 改用 `gcc` 链接（amd64 有 C++ 摄像头源所以自动走 g++）；**Linux 上
  本包没有 C++ 源、Go 一律用 `gcc` 链接**，同样中招（实测 Debian 报
  `undefined reference to 'operator new(unsigned long)'` / `__gxx_personality_v0`）。
  两处都在 `internal/video/ffmpeg.go` 的 `#cgo LDFLAGS` 里显式加上。
- **cgo 前导块（`/* ... */`）里的 `//` 注释中不能出现星号加斜杠**——那是 C 块注释的
  结束符，会把整个前导块提前截断，后面的注释文字被当成 Go 代码解析，报一堆
  `non-declaration statement outside function body` / `invalid character '（'`
  （踩过：注释里写 `pthread_` 星号 `sem_` 星号）。改完前导块用 `gofmt -e` 当场查。
- **视频静态库：Windows 链接必须显式 `-lwinpthread`**。mingw 下 libvpx 检测到
  pthread.h 就用 pthread 线程模型，其 `.a` 里有大量 `pthread_*`/`sem_*` 未定义符号
  （实测 70 处）。WinLibs 是 **posix** 线程模型的工具链，gcc/g++ 驱动会**隐式**带上
  libwinpthread（所以 Windows 产物动态依赖 `libwinpthread-1.dll`，分发时要一起给），
  本机构建因此从不暴露；Debian 的 mingw 不会自动带，交叉链接报
  `undefined reference to 'pthread_mutex_lock'` / `'sem_post'`。已在
  `#cgo windows LDFLAGS` 显式加上（重复链接同一库是空操作，对两条路径都无副作用）。
- **交叉 Windows 必须显式给 `CXX`**（`build.sh` 已设成与 CC 同前缀的 g++，并在带视频
  时检查它存在）。Go 的 `cfg.DefaultCXX` 对交叉目标返回裸 `"g++"`，也就是**宿主的**
  g++；而 cgo 编 C++ 与「包里含 C++ 源时的链接驱动」都走 CXX。不带视频时 Windows
  交叉没有 C++ 源，所以一直没暴露；`-tags video` 引入 mediadevices 的
  `camera_windows.cpp` 后就变成「用 Linux 的 g++ 编 Windows 源码」——`<iostream>`、
  `<unistd.h>` 都能找到、**唯独 `<dshow.h>` 找不到**（实测，报错极具误导性），链接
  同样会落到宿主 g++。Debian 上 mingw 的 g++ 是独立包（`g++-mingw-w64-*`）。
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
- **视频窗口的尺寸与位置必须一次 `SetWindowPos` 原子完成**，不能"先 Resize
  再定位"。`Window.Resize` 的生效时机不受控（fyne/GLFW 会在随后的帧里再应用
  一次；`moveWindowToCorner` 里那三次延迟校正就是为这个加的），若定位时量到的
  还是旧尺寸，窗口随后按"保持左上角"长高，底边就越过通话面板上沿把面板盖住
  ——用户实测的"通话面板第一行不见了"。见 `placeVideoWindow`：带尺寸时用
  `targetRight + insetR - W` 直接算窗口矩形，并在 150/350/800ms 后只做位置校正。
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
- **视频窗口摆放要直接读通话面板的可见矩形**（`visibleRect` + DWM 扩展框架边界），
  不要自己按工作区+边距算：面板定位用的是**未缩放**的 `callPanelMargin`(16 物理像素)，
  视频窗口若按 `16×缩放` 算，DPI 非 100%（如 125%）时就差 4px。
- **音频：播放环形缓冲必须封顶深度**（`playMaxDepth` 500ms → 截到 `playTargetDepth`
  200ms）。读侧（音频回调）恒为实时速率，而写侧会被丢包补静音、对端/网关突发
  一次性垫高；垫高后深度**只增不减**（读多少写多少），表现为"通话越久延迟越大、
  有几秒"。早先容量给到 8 秒，等于允许累积 8 秒延迟。截断时会打
  `播放缓冲超深已截断` 的 Warn（限频 10s），这是排查延迟的第一线索。
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
  本端表里是 96–99，对端 offer 可能是 100/101…；用 `videoCodecByPT(peerPT)` 查
  必然落空 → 段为 nil → 上层输出 `m=video 0`（等于拒绝视频）→ 两边都看不到画面。
  协商结果已存在 `VideoEngine.codec/peerPT` 里，用 `answerSection()` 直接取。
- **视频：采集 goroutine 不计入 `VideoEngine.wg`**。`Close()` 会 `wg.Wait()`，
  若采集侧并发 `wg.Add(1)` 会触发 Go 运行时的
  `sync: WaitGroup misuse: Add called concurrently with Wait` panic。
  启停由 `cameraSupervisor` 串行管理并自行等待（`stopCapture`）。
- **构建脚本：cmd 的 `set` 里 `\t`/`\v` 会被 Python 等工具写成真正的控制字符**，
  表现为 `third_party\video` 变成 `third_party<VT>ideo`，且 `if not exist` 静默判假。
  改 `build.bat` 后务必 `grep | cat -A` 确认。
- **rtp_rx.wav**：根目录与 internal/sip/ 下的抓包调试产物，已被 .gitignore 忽略，勿误提交。
