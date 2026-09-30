# CLAUDE.md

本文件为 Claude Code 提供项目说明，目标是快速、准确地理解与修改本代码库。

## 项目概述

mti-sip-phone：Go 跨平台 SIP 软电话（Windows / Linux，x86_64 + arm64）。常驻系统托盘，支持 SIP 注册、呼出/呼入、PCMA/PCMU 通话、保持、DTMF；提供本地 WebSocket 控制接口（网页拨号/接听/挂断、事件推送）与 JS SDK；支持 sip:// 协议拉起与开机自启。

当前版本 **1.0.1**。版本号从 1.0.0 起，**不自动升版**——升版须经用户明确确认。

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
#   dist/sip-phone-windows-386.exe            32 位托盘版（需 i686 工具链；缺失时改出
#                                             dist/sip-phone-windows-386-headless.exe 纯 Go 回退版）
#   dist/sip-phone-linux-amd64-headless       无界面版（WS 服务，纯 Go）
#   dist/sip-phone-linux-arm64-headless       无界面版（WS 服务，纯 Go）
build.bat

# Linux 一键构建（本机 GUI 版 CGO+音频 + 交叉无界面纯 Go 产物，输出 dist/）：
./build.sh

# 测试：CGO 包（audio）需要 gcc。用 build.bat 同款工具链：
export CC="<WinLibs mingw64/bin/gcc.exe 路径>"   # build.bat 输出里可见
export CGO_ENABLED=1
go test ./internal/media/ ./internal/wsapi/ ./internal/sip/

# 运行
dist/sip-phone-windows-amd64.exe       # 托盘常驻；关闭主窗口=隐藏到托盘
dist/sip-phone-console.exe -headless   # 无界面模式（仅 WS 服务，供服务器）
```

测试页地址：`http://127.0.0.1:<WS端口>/`（默认 8765，占用自动 +1 并保存配置）。

**交叉编译说明**：fyne 的 OpenGL 绑定依赖 CGO，无目标平台 gcc 工具链时
无法交叉编译 GUI 产物；构建脚本的交叉目标均使用 `-tags "headless noaudio"`
（跳过界面层与音频后端，纯 Go，仅 WS 服务）。各平台的 GUI 版请在对应
平台本机构建（Windows amd64 见 build.bat；Linux 见 build.sh）。Windows 386
GUI 需 32 位 MinGW，build.bat 依次探测 `C:\msys64\mingw32\bin\i686-w64-mingw32-gcc.exe`
（MSYS2 `mingw-w64-i686-gcc`）与 `%USERPROFILE%\tools\winlibs-i686\mingw32\bin\i686-w64-mingw32-gcc.exe`
（WinLibs i686 posix-dwarf-ucrt 解压即用）；两者都缺则回退出 386 无界面版。

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
  wsapi/      本地 WS 控制服务（方法分发、事件推送、心跳）
  ui/         Fyne 界面：主窗口/拨号盘/设置页/通话面板/托盘（topmost_windows.go 为 Win32 置顶等）
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

## 重要约定

- **交流、文档、注释、提交信息、界面文案一律中文**。
- **Git 工作流**：开发在分支上进行（工作分支 dev，master 为收口分支）；**会话中修改的代码不得自动提交**——提交与 push 必须经用户人工确认后执行，且**不自动合并到 master**（合并 master 需用户明确指示）。
- 版本 1.0.1 不自动升版；changelog.md 只记录版本之间的变化（开发过程不记）。
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
- **媒体/NAT**：对称 NAT 下 UDP 映射需持续出向流量保活（早期媒体阶段 SetKeepalive 持续发静音帧）；SDP 宣告地址与 socket 绑定地址分离（SetSDPIP）；183→200 可能换 SSRC/端口（引擎已自动处理，勿移除 playPush 的重置逻辑）。
- **测试 flakiness**：`internal/sip` 全量并行跑时偶发 5s 超时失败（信令测试间资源干扰，与具体改动无关）；分组或单跑稳定。验证方式：`go test -run "<Test名>"` 单跑 + 分组复跑。
- **malgo 回调帧长不固定**：WASAPI 共享模式下回调 frameCount 不保证等于 20ms（160 样本），不要假设固定帧长读写引擎。
- **build.bat（cmd 解析）**：`if (...)` 块内的 `echo` 文本不要同时出现括号与尾部 `...`——cmd 会把 `(...)` 当命令组，报「此时不应有 ...。」并中断整个构建（不回显 errorlevel）。多分支输出用 `goto` 标签而不要塞进 if/else 块。
- **32 位 Win32 调用**：`SetWindowLongPtrW` / `GetWindowLongPtrW` 是 **64 位专属导出**，32 位 user32.dll 里没有该符号，`LazyProc.Call` 找不到过程会直接 panic；GUI 子系统（`-H=windowsgui`）无控制台，panic 无任何可见输出，表现为「一操作就静默崩溃」。`topmost_windows.go` 的 `setCallWindowClamp` 用 `Find()` 失败回退到 `SetWindowLongW`（32 位 LONG_PTR 与 LONG 同宽，语义等价）。新增任何 Win32 调用前先确认该导出在 32 位下存在。
- **rtp_rx.wav**：根目录与 internal/sip/ 下的抓包调试产物，已被 .gitignore 忽略，勿误提交。
