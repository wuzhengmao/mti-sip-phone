# mti-sip-phone 架构设计文档

> **面向读者**：新加入本项目的开发同学（以及需要快速定位代码的 AI）。读完应该能回答三个问题：
> **东西在哪**（目录地图）、**为什么这么设计**（关键决策与代价）、**改一处会牵动谁**（依赖关系）。
>
> 具体动手前请另外读两份：[`CLAUDE.md`](../CLAUDE.md) 的「已知陷阱」（踩过的坑清单，改相关代码前必读）、
> [`docs/requirements.md`](requirements.md)（需求条目与状态）、[`docs/ws-protocol.md`](ws-protocol.md)（对外 WS 协议）。
> 自动更新/更新站点接入另见 [`docs/auto-update.md`](auto-update.md)。

---

## 一、一分钟版

- **是什么**：Go 写的跨平台桌面 **SIP 软电话**（Windows / Linux；amd64 / 386 / arm64），常驻系统托盘，
  支持注册、呼出/呼入、保持、DTMF、语音与视频通话；对外提供**本地 WebSocket 控制接口**（网页拨号/接听/挂断、事件推送、视频订阅）与 **JS SDK**；支持 `sip://` 协议拉起与开机自启。
- **形态**：**单进程**——GUI、SIP 信令、媒体收发、本地 WS 服务都在同一个进程里；WS 只监听 `127.0.0.1`。
- **构建产物**（`build.bat` / `build.sh`，输出到 `dist/`；Windows 的三个都**静态链接** mingw 运行时，
  是不依赖任何第三方 DLL 的单文件，Linux 产物依赖系统图形/音频库）：`sip-phone-windows-amd64.exe`（托盘版，无控制台）、
  `sip-phone-console.exe`（控制台版，`-headless` 无界面模式）、`sip-phone-windows-386.exe`（32 位托盘版）、
  `sip-phone-windows-arm64.exe`（Windows on ARM64 托盘版，需 llvm-mingw，无视频/Opus/AEC 的基础版）、
  `sip-phone-linux-<arch>`（Linux 托盘版）。
- **发布**：到独立的「软件更新中心」站点，用 `scripts/release.sh` 一键发布（见 auto-update.md）。

## 二、技术选型（以及为什么）

| 领域 | 选型 | 说明 / 为什么 |
| --- | --- | --- |
| 语言 | Go 1.24+ | 单二进制分发、跨平台交叉编译、并发模型适合"信令 + 媒体 + 界面"三线并行 |
| SIP 信令 | `github.com/emiago/sipgo` | 事务层/对话层/Digest 认证开箱可用；`DialogClientSession`/`DialogServerSession` 管 INVITE 对话 |
| RTP/RTCP/SDP | `pion/rtp`、`pion/rtcp`、`pion/sdp/v3`、`pion/stun` | 纯 Go、可读可改；RTCP 用 v1.2.x 新 API（CompoundPacket/Goodbye） |
| 语音编解码 | `zaf/g711`（PCMA/PCMU）+ **libopus**（cgo） | G.711 是必选；Opus 走官方库（FFmpeg 自带的标记 experimental，官方不认可用） |
| 视频编解码 | **FFmpeg**（解码）+ **OpenH264 / libvpx**（编码）+ **swscale**（色彩/缩放），全部 cgo | 解码用 FFmpeg 原生解码器免依赖；编码不用 FFmpeg 的对应封装（能力/许可考虑） |
| 音频设备 | `gen2brain/malgo`（miniaudio 绑定，cgo） | 一次绑定同时覆盖 Windows WASAPI / Linux ALSA-Pulse；`-tags noaudio` 可切空后端做纯 Go 构建与测试 |
| 界面 | `fyne.io/fyne/v2` v2.8.1 | 托盘/置顶/中文开箱可用；**代价**：UI 必须跑在主线程、OpenGL 绑定依赖 CGO |
| WebSocket | `github.com/coder/websocket` | 只用到服务端，API 简洁、支持 `TryLock` 这类我们需要的细粒度控制 |
| 其它 | `google/uuid`、`golang.org/x/sys` | 呼叫 ID；Windows 注册表（协议注册/开机自启） |

**最大的一条约束**：GUI（fyne→GLFW）、音频设备（malgo→miniaudio）、Opus/视频编解码都是 **CGO**。
于是"交叉编译到某平台"就必须有**该平台**的 gcc 工具链——构建与分发的全部复杂度都源于此，
细节见 [`CLAUDE.md`](../CLAUDE.md) 的构建章节（mingw 交叉、arm64 交叉、第三方静态库）。

## 三、整体架构

### 3.1 分层与组装

`main` 只做**组装与生命周期**，业务被切成互不认识的几层（依赖方向单向向下）：

```
cmd/sip-phone/main.go            进程入口：命令行/单实例/协议注册/开机自启/组装/退出收尾
        │  组装（构造函数注入，不用全局单例）
        ▼
 ┌──────────────┬───────────────┬────────────────────────────┐
 │ internal/ui  │ internal/wsapi│  internal/sip (Telephony)  │
 │ Fyne 界面     │ 本地 WS 服务   │  注册/呼叫/保持/DTMF 信令    │
 └──────┬───────┴───────┬───────┴───────────┬────────────────┘
        │               │                   │
        │  事件总线 core.Bus（单向：下 → 上）  │
        ▼               ▼                   ▼
 ┌──────────────────────────────────┐  ┌──────────────────────┐
 │ internal/core  （状态机 + 接口）    │  │ internal/media       │
 │  App / Bus / Telephony / Updater  │  │  每通话一个 Engine：   │
 │  VideoSink / EventSink            │  │  RTP/RTCP/编解码/抖动  │
 └──────────────────────────────────┘  └──────────┬───────────┘
        ▲                                         │ FeedMic / ReadPlayback
        │ internal/updater（下载/替换/重启）         ▼
        │                              internal/audio（全局 Manager）
        │                              设备枚举/选择、全双工流、提示音
        └──────────────────────────────────────────────
```

三条"接入面"最终都落到同一套核心：

1. **桌面 UI**（Fyne）：用户操作 → `ui` 调 `core.App` 的方法；
2. **网页控制台/SDK**（本地 WS）：`wsapi` 收到请求 → 调同样的 `core.App` 方法；
3. **`sip://` 协议拉起**（浏览器点链接）：第二实例把参数转发给第一实例 → `main.handleLaunchArgs` 写入账户配置并注册。

**两个构建变体**（同一份代码，构建标签区分）：

| 变体 | 标签 | 界面 | 托盘 |
| --- | --- | --- | --- |
| 完整版（默认） | 无 | `internal/ui`（Fyne 主窗口 + 通话面板 + 视频窗口 + 更新提示窗） | Fyne 托盘（`internal/ui/buildTray`） |
| **仅托盘无 UI 版** | `noui` | **没有**（不编入 fyne/GLFW/OpenGL） | `internal/tray`（直接用 `fyne.io/systray`，左键开网页、右键菜单） |

变体的分流只在 `cmd` 层：`gui.go`（`!headless && !noui`）、`gui_noui.go`（`noui`）、`gui_headless.go`（`headless && !noui`）三份实现同一个 `runGUI` 签名。`internal/ui` 一行都不改——它本来就只被 `gui.go` 引用。变体相关的常量都在 `internal/variant`（叶子包，供 `internal/config` 与 `internal/tray` 用；放 core 会与 config 循环依赖）。

### 3.2 关键规则：core 只有接口，谁也不能反向依赖

`internal/core` 只定义**接口与状态**，具体实现由 `main` 注入（构造参数或 `SetXxx`）：

| 接口 | 定义处 | 实现 | 作用 |
| --- | --- | --- | --- |
| `Telephony` | `core/telephony.go` | `internal/sip.Phone` | 注册/呼叫/接听/挂断/保持/DTMF/静音/设备选择 |
| `EventSink` | `core/telephony.go` | `core.App` 自己 | sip 层把"来电/振铃/接通/失败/结束"回调进核心 |
| `CallLog` | `core/calllog.go` | `internal/calllog` | 通话记录落库（SQLite）；开始/接通/结束三个时刻由 `core.App` 直接入队，**不走事件总线**（总线满即丢） |
| `ContactStore` | `core/contacts.go` | `internal/contacts` | 通信录落库（SQLite，独立 `contacts.db`）；主键是**规范化号码**，写操作一律「写库 + 重建内存快照」串行执行，界面与 WS 读内存快照（O(1)） |
| `VideoSink` | `core/telephony.go` | `internal/ui` | 视频帧**直连**界面（不走事件总线，见 5.5） |
| `Updater` | `core/update.go` | `internal/updater` | 自动更新状态机 |

这样一来：`sip`/`media`/`ui`/`wsapi` 都只 import `core`，互相**不 import**（`ui` 直接持有 `audio.Manager` 属于"装配层特权"，见 4.7）。
好处是可测试（`internal/wsapi` 的测试就塞了个 `stubTel`）、可替换，坏处是新增能力的样板代码多一点。

### 3.3 事件流：单向、单源

```
sip.Phone ──(EventSink)──► core.App ──(Bus.Publish)──► core.Bus ──► 每个订阅者
                                                          ├─ ui（刷新界面）
                                                          └─ wsapi（每条 WS 连接各订阅一次，推给网页）
```

- **状态只有一份**（在 `core.App`），界面与网页都是它的投影；`system.getState`/`system.state` 返回同一份快照。
- 事件名与负载见 `docs/ws-protocol.md`；`wsapi` 不做业务判断，只做"分发 + 推送"。
- 总线是**容量 64 的 channel、满即丢**——所以**视频帧绝不能走总线**（见 5.5）。

**网页控制台相关的补充事件**（1.0.15）：`contact.changed`（通信录任何写入路径都会广播，多标签/桌面同步）、`calllog.changed`（记录被删除/清空）、`system.config`（配置被改写，`section` 指明哪一段）、`audio.ready`（音频后端后台初始化完成）。它们都由 `core.App`（而不是 wsapi）广播，因为桌面与网页都是消费者，事件得是同一份。

### 3.4 并发模型（改代码前必须知道）

| 线程 / goroutine | 谁在跑 | 纪律 |
| --- | --- | --- |
| **Fyne 主线程** | 所有 UI 操作 | 任何 goroutine 改 UI 都要 `fyne.Do`；`fyne.DoAndWait` 不可在主线程调用 |
| SIP 收发 goroutine | `internal/sip` | 与 UI 之间只通过 `core.App` 的方法与事件交互 |
| 媒体 goroutine | `internal/media` 的收发循环 | 实时路径，不做阻塞 IO、不写日志刷屏 |
| **音频回调线程** | malgo 回调 | 最高实时性：只做 `FeedMic`/`ReadPlayback` + 提示音混音，**禁止阻塞、禁止持锁调后端**（`internal/audio` 的注释里有两条血泪规则） |
| 每条 WS 连接 | `internal/wsapi` | 请求串行处理；推送用 `TryLock`，抢不到就丢帧（慢客户端不拖住别人） |
| 音频单 worker | `internal/audio.Manager` | 所有设备操作排进 FIFO 队列后立即返回，避免调用方（UI/SIP）被设备阻塞 |

## 四、各模块职责与关系

> 每个模块给出：**职责 / 关键文件 / 依赖 / 被谁依赖**。改代码前先看这一节的"注意"。

### 4.1 `cmd/sip-phone`（入口与组装）

- **职责**：解析命令行（`sip://`、`-headless`）、加载配置与日志、取单实例锁、注册协议与自启、组装各层、进入 GUI 主循环（`gui.go`，`!headless`）、退出收尾。
- **关键函数**：`main`、`shutdown`（注销 SIP、关 WS/SIP 监听）、`spawnNewProcessIfRequested`（**自更新重启只在这里拉起新进程**）、`handleLaunchArgs`（协议参数→账户配置）。
- **注意**：重启时序是硬约束——"释放单实例锁 + 拉起新进程"必须在关闭监听与注销**之后**，否则新进程会因锁冲突自退、或因 WS 端口被占而改配置造成 SIP 双注册。

### 4.2 `internal/core`（状态机 + 接口层）

- **职责**：维护注册状态与**通话列表**（当前限单通话）、发布事件、给上层提供统一方法（`Dial`/`Answer`/`Hangup`/`Mute`/`MuteMic`/`MuteSpeaker`/`SendDTMF`/`SetVideoEnabled`/自动接听/更新转发…）。
- **关键文件**：`app.go`（状态机与方法）、`events.go`（Bus）、`telephony.go`（接口）、`update.go`、`video.go`（视频多播出口）、`buildinfo.go`（版本号/BuildTime）。
- **被谁依赖**：所有包。

### 4.3 `internal/sip`（信令层）

- **职责**：注册（Digest、expires×0.7 续注册、失败指数退避、STUN 就绪后立即重注册、**休眠唤醒/网络切换看护**）、呼出/呼入、保持（re-INVITE）、DTMF 发送、媒体引擎的创建与移交。
- **关键文件**：`phone.go`（全部信令与竞态处理）、`phone_video.go`（视频协商/摄像头联动）、`netwatch.go`（注册看护：墙钟跳变 + 出向地址巡检 → 立即重注册，见 REQ-CALL-009）。
- **注意**：**挂断竞态处理（603 先于 ctx cancel、CANCEL 与 2xx 竞态补 ACK+BYE、保持 re-INVITE 的 ACK 必须复用自己的 From/CallID/CSeq）是历史 bug 的结晶，勿"简化"**。

### 4.4 `internal/media`（媒体引擎）

- **职责**：每通电话一个 `Engine`：RTP/RTCP 收发、编解码、**播放环形缓冲**、SDP 协商（offer/answer、PT 复用规则、方向）、DTMF（RFC 4733 收发）、保活与打洞；`VideoEngine` 是**与音频平级的独立端口对**。
- **关键文件**：`engine.go`（音频引擎 + SDP 入口）、`sdp.go`（SDP 组装/解析）、`jitter.go`（播放环形缓冲）、`codec_*.go`（PCMA/PCMU/Opus）、`video_engine.go`/`video_codec.go`（视频）、`srtp.go`（**媒体加密 SDES-SRTP**：套件表、密钥生成、`a=crypto` 解析、四档策略状态机——音频与视频**各持一份** `cryptoState`，每个 m= 行独立密钥）。
- **边界**：**不认识音频设备**——音频回调只需 `FeedMic(任意帧长)` 与 `ReadPlayback(任意帧长)`；也不认识 UI/SIP。
- **注意**：`activePT`（线上实际 PT）与 `Codec.PayloadType()`（本端号）是两回事；`TimestampStep()` 才是时间戳步长（Opus 48000 域）。

### 4.5 `internal/audio`（音频设备层）

- **职责**：后端初始化（后台）、设备枚举与选择、全双工流（对接一个 `media.Engine`）、提示音（振铃/回铃/遇忙/挂断/**DTMF 按键音**）。
- **关键文件**：`manager.go`（对外 API + 单 worker 队列）、`backend_malgo.go`（miniaudio 实现，含提示音设备复用与空闲关闭）、`backend_null.go`（`-tags noaudio`）、`tone.go`+`assets/`（内嵌提示音与 DTMF 合成）。
- **关系**：`ui` 持有它（播放提示音/按键音）、`main` 创建它；通话开始时被 `AttachEngine` 接到引擎上。
- **注意**：`toneMu` **不得跨越** `InitDevice/Start/Uninit` 持有（回调要拿它，持锁即死锁）；不要持 `m.mu` 调后端。

### 4.6 `internal/updater`（自动更新）

- **职责**：接收前端告知的新版本 → 用户确认 → 下载（sha256 + 可执行魔数校验、大小上限、可取消）→ 替换自身（`.old` 备份 + 回滚）→ 请求重启（通话中挂起等待）。
- **关键文件**：`updater.go`（状态机 idle/available/downloading/ready/failed）、`download.go`、`replace.go`、`version.go`（版本比较，含预发布语义）。
- **关系**：由 `core.Updater` 接口暴露给 WS/UI；重启动作最终由 `cmd` 实现。软电话**自己不查更新服务器**（查询在网页 SDK 侧）。
- **多实例联动（REQ-UPD-026）**：替换成功后、重启之前调用 `Options.OnInstalled`，由 `internal/multiinstance` 把「新版本已就位」写进主配置目录的共享标记 `restart-all.json`；各实例的轮询器（`multiinstance.Watch`）据此判断自己在跑旧镜像时，等**没有进行中的通话**再重启。判定用「来源槽位 + 标记时间晚于进程启动 + 可执行文件 sha256」，重启后的新进程不会被同一条标记循环触发。

### 4.7 `internal/wsapi`（本地控制服务）

- **职责**：HTTP 服务（网页控制台 + `/sdk/*` + `/ws` + 通信录 REST）、方法分发（`call.*` / `system.*` / `video.*` / `calllog.*` / `contact.*` / `audio.*` / `update.*`）、事件推送、心跳；**视频中枢** `video.go` 把帧按"通道+参数"分组编码成 JPEG 推送。
- **关键文件**：`server.go`（HTTP/WS 接入、静态资源白名单）、`handler.go`（主分发 + `call.*`）、`handler_calllog.go` / `handler_contacts.go` / `handler_config.go` / `handler_update.go`（分组子分发，主 switch 的 `default` 依次询问）、`protocol.go`（方法名与错误码）、`video.go`（订阅与编码）。
- **注意**：
  - 仅回环、无 token（安全边界见 ws-protocol.md）；新增方法要**同时**加进外层 case 与内层分支，或挂到某个分组分发器上（漏了会返回 `unknown_method`，有测试兜底）。
  - **设备枚举异步回包**（`audio.devices` / `video.devices`）：枚举可能阻塞数秒，服务端放独立 goroutine 做完再回，配 `enumGuard` 单飞 + 2 秒缓存 + 3 秒超时。写路径本来就有 `wmu` 串行化、SDK 按 id 配对响应，所以异步/乱序是安全的。
  - **会终止进程的动作先回响应**（`system.restart`、`update.apply`）：先动手的话响应发不出去，页面只看到连接断开。

- **`/api/status`**（引导器用）：返回版本/构建时间/平台，供 SDK 引导器探测软电话是否运行、生成 SDK 加载指纹并做热更新判断；`/sdk/*` 继续提供内嵌的 `sip-phone-sdk.js` 与 `sip-phone-bootstrap.js`（一律 no-cache）。
### 4.8 `internal/ui`（Fyne 界面，仅完整版）

> 该包**只在完整版构建里编译**（`-tags noui` 时整包不参与，no-ui 版因此不链接 fyne/GLFW/OpenGL）。

- **职责**：主窗口（拨号盘 + 设置页）、**通话面板**（吸附右下角、不占任务栏）、**更新提示窗**（同样吸附右下角）、来电振铃动效、视频窗口与本地回显、托盘（图标颜色 = 注册状态）。
- **关键文件**：`ui.go`（主窗口/拨号盘/设置/托盘/状态刷新）、`ui_call.go`（通话面板与紧凑按钮）、`ui_history.go`（通话记录页：与配置页一样切在主窗体内，紧凑双行列表 + 自绘文本 + 拖动/Shift 多选 + 滚动加载）、`ui_video.go`（视频窗口/回显/变换）、`ui_contacts.go`（通信录页：两个自绘 tab、拼音搜索、CSV 导入导出、右键批量操作）、`ui_tooltip.go`（备注悬停提示图层）、`ui_update.go`（更新提示窗）、`widgets.go`（内页共用：自绘单行文本 `plainText`、标准外观按钮 `plainButton`、统一顶栏 `pageTopBar`）、`theme.go`/`icons.go`/`statusdot.go`。
- **平台拆分（重要）**：窗口控制按平台分文件——`topmost_windows.go`（Win32：窗口句柄定位、工具窗口样式、置顶、带前台）、`topmost_linux.go`（X11：`_MOTIF_WM_HINTS` 去装饰、`_NET_WM_STATE`、`_NET_WORKAREA`）、`topmost_other.go`（空实现）；另有 `folderdlg_*.go`、`glfw_linux.go`（Wayland/libdecor 规避）。**共享文件只调用平台函数，勿把某平台的逻辑写进共享文件**。

### 4.9 其它支撑包

| 包 | 职责 | 注意 |
| --- | --- | --- |
| `internal/variant` | 变体标识（`NoUI` / `AppName` / `UpdateAppID`），按 `noui` 标签分文件 | 叶子包：不能 import core/config（config 要 import 它来做 `update.appId` 的变体归一） |
| `internal/tray` | 仅托盘无 UI 版的托盘：菜单、图标（SVG 运行期光栅化成 PNG/ICO）、打开浏览器 | 直接用 `fyne.io/systray`，**不调 `SetSystemTrayWindow`**（那是 Fyne 窗口用的）；左键点击=开网页 |
| `internal/appicon` | 话机图标的 SVG 源（绿/红），`internal/ui` 与 `internal/tray` 各取所需 | 单一真相源：托盘图标字号/形状两版一致 |
| `internal/video` | cgo 封装：FFmpeg 解码、OpenH264/libvpx/FFmpeg 编码、swscale；`camera_*.go` 按平台实现采集 | 静态库不入库（`scripts/build-video-libs.*` 生成）；`-tags video` 才编入 |
| `internal/calllog` | 通话记录（REQ-CALL-007）的 SQLite 存储：`Start/MarkAnswered/Finish` 三个写入 + 分页查询/删除/清空 | **所有 SQL 排在单 worker 的 FIFO 队列上串行执行**：SIP 线程只做一次 channel send（永不阻塞信令），界面的查询排在写之后（读己之写）；`call_id` 主键 + `Finish` 带 `ended_at=0` 条件保证幂等（SIP 层会重复上报结束）；启动时把上次遗留的"进行中"记录清扫为「已中断」 |
| `internal/contacts` | 通信录（REQ-CONTACT-001）的 SQLite 存储：全量快照、新增/修改（号码可改，撞号报错）、批量设常用、删除、清空、按号码合并导入 | 与 calllog 同一套单 worker 串行骨架，但**没有异步入队**（写入全部来自界面/WS，不是信令线程）；去重判断与写入在同一事务；导入时常用标志**只增不减**（本地 or 导入） |
| `internal/config` | JSON 配置读写（原子写入）、密码 AES-256-GCM 加密 | 明文密码不落盘、只在内存 |
| `internal/logx` | slog 日志：分级、滚动 + gzip 归档、`TraceSIP` 信令跟踪（自动脱敏） | 日志里不得出现密码/摘要 |
| `internal/protocol` | `sip://` 解析与系统协议注册 | Windows 注册表实现单独文件（导入 x/sys/windows 会破坏交叉编译） |
| `internal/single` | 单实例锁 + 第二实例参数转发（随机 token） | Windows 互斥体 / Linux flock |
| `internal/multiinstance` | 多实例协作（REQ-UPD-026）：共享重启标记 `restart-all.json` 的写入/读取与轮询 | 标记在主配置目录、指纹 + 启动时间判定，避免循环重启 |
| `internal/autostart` | 开机自启（HKCU Run / XDG autostart） | 每次启动按配置同步 |
| `web/` | 网页控制台 + JS SDK（都内嵌进二进制，无构建步骤） | 控制台是"网页接入"的活文档；改完必须重建二进制才生效 |
| `release/` + `scripts/release.sh` | 发布配置与一键发布 | 见 auto-update.md |

## 五、关键设计决策（为什么这么做）

> 每条都是"取舍"，理解取舍比记住结论重要。

1. **core 只定义接口、实现反向注入**：换来可测试、可替换、无循环依赖；代价是组装代码集中在 `main`。
2. **状态只有一份 + 事件单向**：UI 与网页天然一致（同一事件源、同一快照）；代价是任何新状态都要"进核心 + 发事件"。
3. **媒体与音频设备解耦**：引擎只认 `FeedMic`/`ReadPlayback`（任意帧长），音频回调不必知道 20ms/编解码；`-tags noaudio` 因此能纯 Go 跑测试。
4. **每通话一个 `media.Engine`**：端口对、SDP、抖动缓冲、编解码实例互不干扰；代价是"跨通话共享的东西"（如 Opus 编解码器实例）要显式复位。
5. **视频帧不走事件总线**：总线满即丢、容量 64，逐帧推送会把它打爆。视频走 `core.VideoSink` **直连**界面，界面用"存最新帧 + CAS 合并刷新"保证主线程最多一个在途。
6. **视频是与音频平级的独立 RTP/RTCP 端口对**（SDP 两个 `m=` 行）：成败互不影响，视频协商失败不影响语音。
7. **视频开关是纯本地动作**（停采集/停发送/停渲染），不发 SIP 信令：规避 re-INVITE 的 ACK/CSeq 竞态；对端仍按 SDP 发送，只是我们不看不发。
8. **竞态处理集中在 `sip/phone.go`**：SIP 的取消/应答/重传竞态是这类项目的 bug 温床，宁可在信令层写厚一点，也不要让 UI 参与仲裁。
9. **UI 必须主线程，平台差异收敛在 `*_windows.go`/`*_linux.go`**：Fyne 的硬约束 + 让共享代码保持"无平台 if"。
10. **无边框窗口用 Fyne 的 splash 窗口，不用 Win32 硬抹样式**：硬抹会让 GLFW 的边框缓存失准（"幽灵标题栏"，内容被顶出可视区）；同理不占任务栏要在"第一次显示的同一步内"换样式。
11. **自动更新：前端查、用户点、退出收尾再拉起**。软电话自己**不查**更新服务器（网页 SDK 定时查），也**不自动**下载/重启；重启时序与替换回滚是硬约束。
12. **发布侧"目录即数据库"**：站点按 `<app-id>/<版本号>/` 目录识别一切，发布脚本负责把规范做对（文件名/元数据/校验和/更新说明），产物文件名**不含版本号**以保持下载地址稳定。
13. **版本号单一真相源**（`core/buildinfo.go`）+ 开发版带 `-rc`：让"推送正式版"能通知到开发机上的 `-rc` 版本；比较规则支持预发布语义。

## 六、一次通话的完整生命周期（把模块串起来）

```
① 呼出：ui/wsapi ──core.App.Dial──► sip.Phone.Dial
        └ INVITE（SDP offer：PCMA > PCMU > Opus；视频呼叫再加 m=video）
        └ 180/183 → core.OnRinging → 事件 call.ringing（183 带 SDP 即早期媒体：先按 answer 应用 SDP，
          静音 + 保活 + 起媒体，避免对端"听得见回铃音却没人接"）
        └ 200 → SDPApplyAnswer（可能换 SSRC/端口）→ ACK → 取消静音与保活 → OnEstablished
   呼入：sip.handleIncoming（忙则 486）→ core.OnIncoming → 事件 call.incoming
        → 用户接听/自动接听 → 180 → Answer（后台 RespondSDP，200+SDP 并等 ACK）→ 同上
② 通话中：媒体引擎收发（抖动缓冲、PLC、RTCP）；保持/静音/扬声器/视频开关都是 core.App 的方法；
        DTMF 发送（按键/键盘/网页）；收到对端 DTMF 时本机合成按键音
③ 挂断：本端 BYE/CANCEL，或对端 BYE/CANCEL → core.OnTerminated(reason) → 事件 call.terminated
        → ui 收面板、audio 停流与提示音、视频窗口收起
④ 收尾：通话中若收到更新就绪 → updater 挂起等待 → 通话结束后替换并重启
```

## 七、目录地图：「我要改 X，去哪？」

| 想做的事 | 去哪 |
| --- | --- |
| 拨号盘 / 设置页 / 托盘 | `internal/ui/ui.go`（`buildDialPage` / `buildSettingsPage` / `buildTray`） |
| 通话面板的按钮与布局 | `internal/ui/ui_call.go`（`setCallUI` 决定三态显示；`panelRowLayout`/`panelRowsLayout` 是自定义布局，别换回 Fyne 的 HBox） |
| 视频窗口/回显/旋转镜像 | `internal/ui/ui_video.go` + `internal/media/video_engine.go` |
| 更新提示窗 | `internal/ui/ui_update.go` + `internal/updater/*` |
| 通话记录（列表窗口 / 落库） | `internal/ui/ui_history.go` + `internal/calllog/calllog.go` + `internal/core/calllog.go`（采集点在 `core/app.go` 的 Dial/OnIncoming/OnEstablished/OnTerminated/OnFailed，**先写记录再发事件**） |
| 通信录（页面 / 落库 / 联动显示） | `internal/ui/ui_contacts.go` + `internal/contacts/contacts.go` + `internal/core/contacts.go`（内存快照与号码规范化）；联动显示见 `callDisplay`（ui_call.go）与 `historyDisplay`（ui_history.go）、未接通知在 `ui_notify.go`；CSV 编解码在 `internal/core/contactcsv.go`（桌面与 WS 共用一份） |
| 注册/呼叫/保持信令 | `internal/sip/phone.go`（先读 CLAUDE.md 的 sipgo 陷阱） |
| 休眠/切网后恢复注册 | `internal/sip/netwatch.go`（墙钟跳变用 `UnixNano` 差，勿改回 `time.Sub`） |
| SDP / 编解码协商 | `internal/media/sdp.go` + `engine.go`（`codecPT`、`activePT`、`SDPDirectionFor`） |
| 媒体加密（SRTP） | `internal/media/srtp.go` + 各引擎的收发路径（`sendPacket`/`recvLoop`/RTCP）——**密钥方向与重协商口径先读 CLAUDE.md 的 SRTP 条**；配置项 `account.srtp`、`config.NormalizeSRTP` |
| 抖动缓冲/延迟 | `internal/media/jitter.go`（注意播放缓冲封顶规则） |
| 音频设备 / 提示音 / 按键音 | `internal/audio/manager.go`、`backend_malgo.go`、`tone.go` |
| WS 方法 / 事件 | `internal/wsapi/{protocol,handler,server}.go`（协议文档同步 `docs/ws-protocol.md`） |
| 网页视频订阅 | `internal/wsapi/video.go` + `web/sdk/sip-phone-sdk.js` |
| 网页控制台（五个页面） | `web/index.html`（外壳，单页 + hash 路由；顶栏应用名「MTI 软电话」）+ `web/css/app.css` + `web/img/*`（与桌面同一套话务图标）+ `web/js/{common,icons,store,callbar,callui,dialpad,app}.js` + `web/js/views/{call,history,contacts,settings,diag}.js`（改完**重建二进制 + 重启**才生效——都是内嵌资源；新增文件要同步 `internal/wsapi/server.go` 的白名单） |
| JS SDK | `web/sdk/sip-phone-sdk.{js,d.ts}`（同样内嵌，改完要重建） |
| SDK 引导器 | `web/sdk/sip-phone-bootstrap.{js,d.ts}`（内嵌，经 `/sdk/*` 提供；应用只打包它，SDK 动态加载并按 `version\|buildTime` 热更新，见 **`docs/sdk-bootstrap.md`**）。拉起走 `sip://`（`?listen=&account=&password=`），断线由引导器接管重连；`GET /api/status` 供其探测 |
| 仅托盘无 UI 版（托盘行为/图标） | `cmd/sip-phone/gui_noui.go` + `internal/tray/*` + `internal/variant/*`（构建标签 `noui`） |
| 构建脚本 / 交叉编译 | `build.bat`、`build.sh`（两者都按 full / noui 两个变体各跑一遍目标）、`scripts/build-*-lib.*`（先读 CLAUDE.md 构建章节） |
| 一键发布到更新站点 | `scripts/release.sh`（**默认两个应用一起发**）+ `release/release.conf`（完整版）/ `release/release-no-ui.conf`（无 UI 版），`--only no-ui` 只发一个（见 auto-update.md） |
| 版本号 | `internal/core/buildinfo.go`（唯一真相源，人工修改） |

## 八、上手第一小时

```bash
# 1) 构建（Windows；Linux 用 ./build.sh）
build.bat                     # 产物在 dist/，开头会打印本次版本与能力（CGO+音频+视频）
# 2) 跑起来
dist/sip-phone-console.exe -headless    # 无界面，仅 WS 服务（调试信令/网页最方便）
dist/sip-phone-windows-amd64.exe        # 托盘版（关闭窗口 = 隐藏到托盘）
# 3) 测试（CGO 包需要 gcc，用 build.bat 输出里的 CC 路径）
export CC="<WinLibs mingw64/bin/gcc.exe>"; export CGO_ENABLED=1
go test ./internal/media/ ./internal/wsapi/ ./internal/sip/ ./internal/ui/ ./internal/updater/
# 或用固定路径运行器（.testbin/，Windows 防火墙只需放行一次，见其头部说明）：
./scripts/test.sh -p ./internal/media/ -- -run SRTP -v
```

**团队约定**（详见 CLAUDE.md「重要约定」）：

- 交流、文档、注释、提交信息、界面文案**一律中文**；开发在 `dev` 分支，`master` 为收口分支。
- **会话中改的代码不自动提交**；提交/push/合并要人工确认。
- **改完代码就重新构建 + 重启应用**（Windows 上运行中的 exe 不能覆盖，必须先结束旧进程）；**不自动推送到更新站点**。
- 需求变更同步 `docs/requirements.md` 与 `changelog.md`；版本号不自动升。

## 九、扩展点（以后往哪加）

| 想加什么 | 大致落点 | 要特别注意 |
| --- | --- | --- |
| 新音频编解码（G.722 已交付，见 `internal/media/g722`） | `internal/media/codec_*.go` + `sdp.go` 的编解码表；**纯 Go 实现直接注册进 `supportedCodecs`**，需要 C 库的才走 `third_party/audio` + `-tags audio` | `SampleRate()`（PCM 管线采样率）、`FrameSize()`（每帧样本数 = SampleRate/50）与 `TimestampStep()`（RTP 时间戳增量 = ClockRate/50）是三件事，G.722（320/160）与 Opus（160/960）是最容易踩的两个反例；有状态就把 `Reset()` 实现成 `resetter` |
| 新 WS 方法 / 事件 | `wsapi/protocol.go` 常量 + `handler.go`（**外层列表 + 内层分支**）+ 协议文档 | 错误码映射 `codeOf`；事件要加进 ws-protocol 的事件表 |
| 新 SDK 能力 | `web/sdk/sip-phone-sdk.js` + `.d.ts` + 控制台演示 | SDK 无构建步骤，改完重建二进制才生效（内嵌） |
| 多路通话 | `core.App` 的 `calls` 已按 map 组织，但**有全局假设**：媒体层 Opus 编解码器是包级共享实例、UI 只显示一个通话面板、按键/DTMF 只作用于 `activeCallID` | 需要一次系统性梳理，别只改半边 |
| 新平台（如 macOS） | 平台文件都按构建标签拆分（`topmost_other.go`、`camera_other.go`…），先补空实现再逐个替换 | 交叉编译仍受 CGO 约束 |
| 更强的安全（WS token 鉴权；媒体的 DTLS-SRTP） | wsapi 接入鉴权；媒体层（DTLS 握手 + `a=fingerprint`/`a=setup`） | SDES-SRTP 已交付（REQ-MEDIA-009）；WS 目前**无鉴权、仅回环**；DTLS-SRTP 见 requirements 的待开发条目 |

## 十、读完还想再深入

| 想了解 | 看哪 |
| --- | --- |
| 踩过的坑（必读，改代码前） | [`CLAUDE.md`](../CLAUDE.md)「已知陷阱」——每条都带现象与根因 |
| 需求条目与状态 | [`docs/requirements.md`](requirements.md) |
| 对外 WS 协议（方法/事件/错误码/时序） | [`docs/ws-protocol.md`](ws-protocol.md) |
| 自动更新与接入更新站点（含核心代码附录） | [`docs/auto-update.md`](auto-update.md) |
| 版本间变化 | [`changelog.md`](../changelog.md) |
