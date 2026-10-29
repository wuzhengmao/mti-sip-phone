# 变更记录

本项目遵循 [语义化版本](https://semver.org/lang/zh-CN/) 规范，版本号格式为 `主版本号.次版本号.修订号`。

> **版本号变更需经明确确认后方可执行**，不接受自动升版。
> 开发过程中的变更**记入当前开发版本的段落**（标题日期写「待发布」），
> 封版发布时再整理该段落并改为实际发布日期。

## [待发布]

### 新增

- **macOS 平台支持（构建 / 安装 / 协议 / 打包 / CI）**：
  - **构建**：`./build.sh --target darwin`（或 `darwin-arm64` / `darwin-amd64` / `macos`）在 macOS 宿主上**一次出 amd64 与 arm64 两个架构**（Apple clang 的 `-arch` 跨编，不需要第二台机器），完整版与仅托盘无 UI 版各一份：`dist/sip-phone-darwin-<arch>`、`dist/sip-phone-no-ui-darwin-<arch>`。固定为基础版（G.711/G.722 + 音频设备 + 界面/托盘 + 信令 + WS）——macOS 摄像头采集未实现（`internal/video/camera_other.go`），第三方视频/Opus/AEC 静态库也还没有 darwin 版本。另外修正：非 Linux 宿主不再误编出 `linux-*` 产物（`linux-native` 目标加宿主判断）。
  - **自安装（REQ-UPD-025）**：裸二进制直接运行时在 `~/Library/Application Support/mti-sip-phone/` 生成 **`.app` 应用包**（完整版 `MTI软电话.app` / 无 UI 版 `MTI软电话-无UI版.app`）——可执行文件进 `Contents/MacOS/`、`Info.plist` 写明 bundle 标识/版本/`LSUIElement`/麦克风与摄像头用途/`CFBundleURLTypes`、`Contents/Resources/AppIcon.icns` 由 `internal/appicon.ICNS`（纯 Go，多尺寸 PNG 内嵌）生成——再从包内可执行文件重启；桌面放指向该包的符号链接。**已在任一 `.app` 内不再搬迁**（DMG 挂载点 `/Volumes/…` 与 `AppTranslocation` 的临时路径除外，仍会装到固定目录）。`Info.plist`/图标内容没变就不重写，避免每次启动刷新 LaunchServices 的图标缓存。
  - **sip:// / sips:// 协议（REQ-PROTO-006）**：`Info.plist` 的 `CFBundleURLTypes` 声明两个 scheme → `lsregister -f` 让 LaunchServices 收录 → `LSSetDefaultHandlerForURLScheme` 设为默认处理器。**URL 走苹果事件而不是命令行参数**：新增 `internal/macurl` 在 main 最早期装上 `NSAppleEventManager` 的 `kAEGetURL` 处理器（事件早于核心组装时先缓存、接上回调后补发），接到后复用 `handleLaunchArgs`（写账户配置 → 注册）。裸二进制（不在 `.app` 内）注册会得到明确错误。
  - **开机自启**：`~/Library/LaunchAgents/cn.mti.sip-phone.plist`（`RunAtLoad` + `LimitLoadToSessionType=Aqua`），只写文件、不扰动 launchd 的已加载状态。
  - **打包（REQ-REL-007）**：`scripts/package-macos.sh` 出新版——`--arch universal` 用 `lipo` 合并两个架构并**重新 ad-hoc 签名**（lipo 会让切片签名失效，Apple Silicon 上签名无效的二进制无法执行），`.app` 再整体 ad-hoc 签名；`Info.plist` 补齐 `sip`/`sips` URL scheme（与自安装同一套字段）；DMG 内含「应用程序」快捷方式。
  - **CI（REQ-REL-008）**：`.github/workflows/build.yml` 在 macOS runner 上跑 `go vet ./...` → `./build.sh --target darwin` → 关键包单测 → `--version` 冒烟 → `package-macos.sh --arch universal --variant both` → 校验 `lipo -info` / `plutil -lint` → **运行期冒烟**（裸二进制自安装成 `.app`、包内进程启动、无崩溃记录、`lsregister` 收录、LaunchAgent 生成、`open sip://` 后日志出现「收到苹果事件的 sip:// 链接」）→ 上传 DMG 与冒烟截图/日志。触发器收紧为 **push 到 master + PR**，且只在这些路径变化时跑：`internal/**`、`cmd/**`、`web/**`、`go.mod`、`go.sum`、`build.sh`、`scripts/package-macos.sh`、`.github/workflows/build.yml`（私仓 macOS 分钟 10 倍计费）；dev 上要验证在 Actions 页面手动 Run workflow。
  - **自动更新**：macOS 替换包内可执行文件后重做 ad-hoc 签名（`internal/updater/resign_darwin.go`，失败只告警）。
  - **新增 `-version` 命令行开关**：打印版本与目标平台后退出（不需要图形会话，供 CI 冒烟）。
- **`internal/macbundle`**：macOS 应用包的目录结构、`Info.plist`、`AppIcon.icns` 与 LaunchServices 注册的统一实现（自安装、协议注册、打包脚本三处同一套字段与名字）。
- **macOS 窗口行为（`internal/ui/topmost_darwin.go` + `.m`，AppKit）**：通话面板吸附工作区右下角、悬浮层级（`NSFloatingWindowLevel`）、`collectionBehavior` 不进 Mission Control 与 Cmd+` 切换、拖动跟随并记住位置（REQ-UI-040 沿用同一份配置）；更新提示窗同样悬浮。坐标统一为「主屏左上原点的物理像素」（与 Windows/Linux 保存的位置同口径），Cocoa 的左下原点/point 换算集中在 `.m` 里。注意 AppKit 只能在主线程调用，全部走 `fyne.DoAndWait` 且由后台 goroutine 发起；`endCallPanelDrag` 不等待拖动会话退出（否则与主线程互等）。
- **发布接入 macOS（`scripts/release.sh --darwin-ci`）**：一次发布 = 本地容器编 10 个 Windows/Linux 产物 + GitHub Actions 编 macOS（两个架构的裸二进制 + universal DMG，两个并行），下载回来合并上传。发布物新增 `mti-sip-phone-macos-x64` / `-arm64`（裸 Mach-O，自动更新的下载目标）与 `mti-sip-phone-macos-universal.dmg`（`kind=installer`，首次安装包）；`release.json` 的 `kind` 按产物输出。
- **macOS 自动更新的下载校验**：`checkExecutableMagic` 补 Mach-O 魔数判断（单架构 + universal），此前 darwin 分支等于不校验。
- **`internal/appicon.ICNS`**：纯 Go 生成 macOS `.icns`（icp4/icp5/icp6/ic07/ic08/ic09 六档 PNG 内嵌），无需 sips/iconutil，可跨平台单测。

## [1.0.18] - 2026-10-25

### 新增

- **CTI 按钮「在途转圈」的状态恢复修复**：耳语→监听这类按钮在指令回执前就收到面板更新时，转圈结束会用点击前的快照覆盖新状态（桌面/网页卡片上「耳语」转一圈又变回「耳语」）。现在：桌面在途期间保留待恢复项，面板重建换图标时同步更新（`setCTIButtonIcon`），回执只恢复**最新**图标；网页在途结束后按**当前面板**重新渲染按钮，不再恢复旧 `innerHTML`。**话务条与通话卡片统一机制**：`runCTI(btn, fn, okText, id)` 带上动作 id，面板重画后 `attachPending(id, btn)` 把转圈贴到新按钮上（话务条每次面板变化都会整条重建），结束时统一恢复并重新渲染。回归：`TestCTISpinRestoresLatestIcon`。

- **监听耳语态的控制按钮**：耳语状态下「耳语」按钮改为「监听」（**图标换成监听图标**、桌面悬停提示同步改为「监听」），点击调用 `stopWhisper` 恢复静默监听；插话（强插）与强拦照常显示（桌面通话面板与网页控制台一致，用户口径）。SDK 新增 `stopWhisper()`，WS `cti.action` 的动作白名单同步加入 `stopWhisper`。

- **`CALL_PANEL` 新增 `owner`：按发起方决定谁能看到操作按钮**（协议 §16.2）：`owner` 是当前操作的发起方分机——咨询是咨询方、三方是发起方（咨询方/强插班长）、监听是班长；`callMode=normal` 时没有。现在**咨询角色以 owner 为准**：`callMode=consult` 且 `owner` 不是本机时（被咨询方）按普通通话显示、不出现任何咨询相关控制（三方/转移/结束咨询），只有 owner 显示；`callMode=three-way` 时**只有 owner 显示移除/强制挂断（X）**，其他参与方只看到号码。桌面（`Call.CTI.Owner`）与网页（`callPanel.owner`）都按它判定；`owner` 缺失的老服务端仍回退「`callList` 里有 `callType=consult` 的腿」。回归：`TestServerConsultOwnerWins` / `TestServerThreeWayOwner` / `TestServerMonitorOwner`。

- **对外面板保持协议属性名、不丢弃话机未用到的属性**：`call.panel` 的 `callPanel`/`conferencePanel` 现在与 CTI 协议一一对应（SDK 原样交给应用，应用也许要用）——`callList` 每项保留 `callerNo`/`calledNo`/`intercom`/`skillGroup`/`callType`/`callStatus`（**不再改名为 `state`**）/`startTime`/`ringingTime`/`offhookTime`，`callPanel` 增加 `agent`/`owner`；会议面板改为协议口径 `{confId, owner（主持人号码）, memberList:[{member:{number,name,note}, intercom, owner, state, timestamp}]}`（主持人仍排第一行）。唯一认可的改写是 `peer`/`member` 合并成 `{number,name,note}` 对象，且**协议已经给了名称时不带通信录备注**——只有名称为空、回退到通信录时才同时给出 name 与 note（用户口径）；监听另有本端补充的 `target`；顶层 `callPanel` 不再单列 `skillGroup`（腿里已经带了）。SDK 类型声明、`docs/ws-protocol.md`、`docs/sdk-guide.md` 同步更新；`sdk.on('call.panel', …)` 增加类型重载，`SipCallPanelState` 会被 IDE 正确推断（之前 `on` 只有 `any`，该类型声明实际没被引用）。回归：`TestServerCallPanel`（`ringingTime`/`offhookTime`/`intercom` 等原样保留）、`TestServerConferencePanel`、`TestPanelPartyNoteOnlyOnFallback`、`TestPanelJSONKeepsProtocolFields`。

- **咨询转移后桌面按钮不显示**：2999 咨询 2901 再做咨询转移，服务端面板回到 `callMode=normal`，网页正常显示咨询/转移/会议，桌面却没有。原因是剩余话务的 INVITE 带着 `Call-Info: call-type=consult`（`Call.Consult=true`），桌面据此把入口藏掉了。现在**服务端面板是权威口径**：应用每次通话面板时清掉本地 `Call.Consult`（是否咨询腿由 `callMode`/`callType` 决定），桌面按翻译后的 `TalkState` 正常显示。回归：`TestServerPanelClearsConsultFlag`。

- **咨询相关的网页按钮修正**：① 来电振铃改以**本地 SIP 通话**判定（`call.state=ringing` + incoming）——被咨询方（2901）的咨询腿面板 state 是 `ringing`，之前只认 `incoming` 导致**没有接听按钮**（桌面正常）；② **咨询发起方（2999）不再显示通用「挂断」**：与桌面一致，退出咨询走各方行上的「取消咨询/结束咨询」；咨询腿振铃/拨号时文案为「取消咨询」，接通后为「结束咨询」。CDP 实测：被咨询方振铃 `接听|拒接`；咨询振铃 `取消咨询`、无挂断；咨询通话 `三方通话|咨询转移` + `结束咨询`、无挂断。

- **咨询角色按服务端面板判定**：`callMode=normal` 才显示「咨询/转移/会议」；`callMode=consult` **且 `callList` 里有 `callType=consult` 的腿**才是咨询发起方（显示三方通话/咨询转移/结束咨询），仅 callMode=consult 没有该腿的是**被咨询方**——按普通通话显示，不出现咨询行与咨询按钮（2901 作为被咨询方时桌/网页都不再错误显示咨询入口）。网页据此判定（`isConsultingPanel`），桌面经 `Call.CTI` 翻译（被咨询方保持双方通话 + `ConsultInProgress`）。回归：`TestServerConsultConsultee`。

- **协议 2026-10 修订：话务面板改由服务端整份下发**：细粒度 `MSP_EVENT_CALL_*` / `CONSULT_*` / `MONITOR_*` / `THREE_WAY_*` / `CONFERENCE_*` 不再下发，改为 `MSP_CALL_PANEL_UPDATED`（`callId/agent/callType/callMode/state/startTime/failCause/callList[...]`）与 `MSP_CONFERENCE_PANEL_UPDATED`（`confId/owner/memberList[...]`）。话机**整份覆盖**面板并发布 `call.panel`（桌面通话面板/话务条、网页控制台与 SDK 都据此渲染）：`callId` 为空关闭通话面板、会议 `memberList` 为空退出会议界面（挂断本端）、`failCause` 翻成中文经 `cti.failed` 提示。服务端面板同时翻译进 `Call.CTI`，桌面既有面板布局（咨询/三方/监听/会议）直接复用；旧细粒度事件仅作老服务端兜底（收到过服务端面板后忽略）。回归：`TestServerCallPanel` / `TestServerConsultPanel` / `TestServerConferencePanel` / `TestServerConferenceEnded`。

- **过滤网关的占位显示名**：FreeSWITCH 等网关把出局腿 `From` 的 display-name 固定写成 `Outbound Call`，导致通话记录/通话面板显示成「91381003 Outbound Call」。现在统一过滤占位名（`core.CleanDisplayName`：Outbound Call / OutboundCall / Outbound / Inbound Call / Unknown / Anonymous，大小写与多余空格不敏感）：**新记录写入时过滤，老记录读取时也过滤**，界面回退到号码/通信录名称。回归：`TestCleanDisplayName`、`TestPlaceholderRemoteName`。

- **CONSULT_RINGING/CONSULT_CONNECTED 也发给被咨询方**（协议修订）：`applyMSPEvent` 现在对三个咨询事件统一判断——**只有咨询发起方（本机 = `consulter`）才切换咨询面板**，被咨询方与原通话另一方保持双方通话、只置 `ConsultInProgress`（隐藏咨询/转移/会议入口），`CONSULT_FINISHED/FAILED` 恢复；body 缺 `consulter` 时沿用已有状态，避免把发起方误判。回归：`TestConsultConsulteeUnaffected`。

- **会议成员退出事件 `CONFERENCE_MEMBER_REMOVED`**（协议 §16.6 新增）：被移出/退出的成员不再收到 `CONFERENCE_MEMBERS_CHANGED`（变化后的列表里已经没有他），改为收到这条事件；话机收到即**挂断本端、关闭通话面板**（`member` 不是本机时忽略；旧字段 `telNo` 兼容；`CONFERENCE_FINISHED`/`CONFERENCE_MEMBER_REMOVED` 已不含名称字段，实现只用号码）。回归：`TestConferenceMemberRemovedHangsUp`。

- **协议修订：CALL_INCOMING 取消 + 技能组标签**：来电改为只看 `CALL_RINGING`（外线呼入分配到坐席时带 `agent`/`agentName`/`skillGroup`；老服务端仍发 `CALL_INCOMING` 的继续兼容）；`skillGroup` 有值时在**对端号码右侧显示一个小的技能组标签**（桌面通话面板是圆角小标签，网页控制台是 `.tag`，随 `call.panel.callList[].skillGroup` 下发）。回归：`TestCallLifecycleEvents`（CALL_RINGING/CALL_INCOMING 兼容 + skillGroup）、`TestPanelTwoParty`（腿带 skillGroup）。

- **通话面板快照事件 `call.panel`**（REQ-WS-023 / REQ-SDK-010）：核心在每次话务/坐席状态变化以及连接建立时推送 `{agentMode, isBusy, callPanel, conferencePanel}`——`callPanel` 带 `callId`、`startTime`、`title`（拨号中/振铃中/咨询呼叫中…）、`state`（dialing/ringing/incoming/talking/consult-dialing/consult-ringing/consult-talking/three-way/monitoring/whispering）与 `callList`（`peer`/`target`、子状态、开始时间，监听时 peer=主叫、target=被叫）；会议时 `conferencePanel` 带 `confId`、`startTime`、`owner` 与 `members`（主持人第一行、成员状态与加入时间）；**`callPanel` 与 `conferencePanel` 互斥**：会议接通后只推 `conferencePanel`；被叫在会议邀请振铃阶段只有 `callPanel`（来电），CTI 提前下发的会议事件不生成会议面板，接通后才切换。**没有通话时两个 panel 都是 `null`**。应用/SDK 直接基于它构建通话面板，不必解析 `call.*` / `cti.*` 明细（SDK 无需新方法：`sdk.on('call.panel', fn)`）。网页控制台的通话控制卡片与话务条改为由该快照驱动（标题、各方、会议成员、按钮显隐都由 `callPanel.state`/`callList`/`conferencePanel` 决定），无快照时回退旧口径。回归：`TestPanelTwoParty` / `TestPanelConsultAndThreeWay` / `TestPanelMonitor` / `TestPanelConference` / `TestPanelAgentMode` / `TestPanelEmpty`。

- **AGENT_ONLINE 的 `status` 对齐**（协议 §16.1）：`MSP_EVENT_AGENT_ONLINE` 新增 `status`（CTI 侧记录的话机状态：`free`/`dialing`/`ringing`/`talking`/`holding`，大小写不敏感）。话机收到后与本地 `callStatus()` 比对，**不一致时立即补发一次 `MSP_HEARTBEAT`** 上报本地真实状态（不再等下一个心跳周期）；一致时不动，`status` 缺失（老服务端）时同样不动作。回归：`TestAgentOnlineStatusMismatch`。顺带去掉 UI 侧重复的「坐席模式变化」日志（core 的 `setAgentOnline` 已记录，同一事件原来在日志里出现两条一模一样的行）。

- **会议成员会控按钮的只读样式**：普通成员（非主持人）视角的 禁视频/禁音/禁听 是只读的——按钮禁用且**置灰（opacity .45）、鼠标为默认箭头、悬停不出现底色**（之前 `disabled` 了但仍显示手型和 hover 高亮，看起来像能点）。

- **网页与心跳的调整**：① **迷你 DTMF 拨号盘**（话务条拨号盘按钮弹出的那个）收窄为约 3:4 的竖长比例（内容宽 110px）；**呼叫/咨询/转移对话框里的号码盘保持原样**（用户要求）；② 修复**话务条的咨询/转移（以及会议「添加成员」）按钮点了没反应**——`mk()` 的点击处理器调用动作时没传事件对象，动作里的 `e.clientX` 抛错；③ 网页**未连接软电话时顶栏坐席状态徽章隐藏**（断线快照点了也发不出去），断线事件同时刷新徽章。

- **网页显示 SIP 呼叫失败原因**：`call.failed`（SIP 信令失败，`reason` 为 `486 Busy Here`、`408 Request Timeout`、`SDP 协商失败: …` 这类原文）在网页控制台 toast「呼叫失败：<原因>」，与桌面 `showCallFailed` 一致；同时修复 store 处理结束事件时的 `const last` 赋值 `TypeError`（网页没拿到当前通话快照时，整个 `callEnded` 事件会被抛掉、卡片与提示都不更新）。**CTI 接管的话务不再重复提示**：收到过 `CALL_RINGING` 的话务，`call.failed` 带 `ctiManaged=true`，桌面/网页都不显示 SIP 原因（如「603 Decline」），改由随后 `CALL_FAILED` 的 `cti.failed` 中文消息（「呼叫未接通」）提示——此前两条会同时出现（用户实测）。

- **CTI 失败原因提示 + 网页「呼叫中」卡片残留修复**：① `CALL_FAILED` 的 `refuse`（呼叫未接通）/ `cancel`（呼叫已取消）/ `takeover`（呼叫被代接）与 `CONSULT_FAILED` 的 `refuse`（咨询未接通）/ `cancel`（咨询已取消；协议把原 `fail` 改为 `cancel`，旧值仍兼容）由 core 翻成中文，经新的 **`cti.failed`** 总线/WS 事件下发（`{callId, kind, reason, message}`）：桌面提示条与网页控制台 toast 直接显示（桌面按当前可见窗口落在通话面板或主窗体下缘）。**本端信令先结束**（真机常见：对端 CANCEL → 本端 `call.failed`，CTI 的 `CALL_FAILED` 几十毫秒后才到、此时已无活动通话）时，按 CTI 话务 ID 找回刚结束的那通电话补发提示，同一通只提示一次（`Call.CTI.failNoticed`）。② 修复网页在通话结束后仍显示「呼叫中」通话卡片（话机 UI 已空闲、心跳上报 `free` 也正常）：core 的 `calls` 快照保留已结束的通话，网页把 `system.state` 里的第一条/迟到的 `call.audio`、`call.video` 快照当成当前通话；现在 store 只认 `ringing`/`established` 状态（`setCall`），重连快照取最后一条进行中的通话，并在没有通话时同步收起通话卡片与话务条。

- **CTI 双方通话生命周期事件（CALL_*，协议 §16.2）**：接入 `MSP_EVENT_CALL_STARTED / CALL_RINGING / CALL_INCOMING / CALL_CONNECTED / CALL_FINISHED / CALL_FAILED`——这些事件提供 SIP 信令里没有的属性（CTI 侧 `callId`、双方名称 `callerName`/`calledName`、接听坐席 `agentName`）：
  - `Call.CTI` 新增 `mspCallId`、`callPhase`（`dialing`/`ringing`/`talking`）、`agent`/`agentName`，双方主被叫复用 `callerNo/calledNo` 及名称字段（`NameOf` 可查）。
  - 对端显示名：按 `agent`/`caller`/`called` 判定本机之外的一方，号码与本地 SIP 对端一致时把 `xxxName` 写进 `Call.PeerName`（桌面通话面板与网页控制台的对端展示都自动跟随）。
  - **`CALL_RINGING` 时通话面板状态由「呼叫中」改为「振铃中」**（服务端外呼时 SIP 侧不一定有 180；桌面 UI 与网页控制台同步）。
  - **`CALL_FINISHED` / `CALL_FAILED` 表示话务已结束：本端挂断，通话面板随 `call.terminated` 关闭**；事件里的 `callId` 与本通记录的 `mspCallId` 不一致时视为别的话务/迟到事件并忽略，避免误挂下一通电话。
  - 咨询/三方/监听/会议进行中收到基础话务事件时只补 `callId`，不把面板改回双方通话。
- **CTI 协议文档**：`MSP_EVENT_CONSULT_FINISHED` 去掉未使用的 `success` 字段（实现侧从未读取，无需改动代码）。

- **网页控制台导航与拨号入口细节**：① **去掉导航栏的「在线座席」**（列表只保留在拨号对话框的在线座席页签）；② 导航当前页改为**下划线高亮**（与对话框页签一致，不再是实心按钮）；③ 通话控制卡片空闲时的「呼叫」按钮加上呼叫图标；④ 顶栏应用名由 `mti-sip-phone` 改为「**MTI 软电话**」（与桌面版一致）。

- **网页控制台：图标居中与拨号框自动关闭**：① 带图标的文字按钮改为 `inline-flex + align-items:center`，图标与文字纵向精确居中（实测中心相差 1px 内）；② 拨号对话框里点「呼叫/视频呼叫」成功后**自动关闭**（之前遗漏了 close，框一直留着）。

- **网页控制台：按钮图标与 MSP_ACTION 在途反馈**：① 通话卡片里的「结束会议」改为**红色**；② 通话卡片、话务条与拨号对话框的**所有文字按钮都带图标**（与桌面 UI 同一套 PNG/SVG 图标），**图标颜色与文字一致**（白字配白图标，SVG 用 `currentColor`）；③ 点击 MSP_ACTION 类按钮（咨询/转移/会议/三方/转移/监听三件套/结束会议、逐方的结束咨询/强制挂断/会议会控/移除、拨号对话框内的 CTI 动作）时，图标位置换成**旋转圆圈**、按钮禁用；同一时间只允许一条在途，期间点其它动作只提示「上一个操作尚未完成」，结果返回后恢复（与 UI 行为一致）。

- **话务条会议成员列表交互**：成员行行距略微加大（上下各 6px）；列表的展开/收起改为**鼠标进入/离开控制**（点列表里的会控按钮触发重画后仍保持展开，鼠标离开才关闭）。

- **网页控制台：会议行对齐 / 话务条悬停列表**：① **桌面 UI 的「取消保持」底色改回黄色**（原来切成蓝色，用户要求保持黄色）；② 网页会议行**首列固定 30px**（移除按钮 / 主持人图标 / 空位同列并居中），各行号码左缘对齐、移除与主持人图标中心对齐；③ 话务条悬停成员列表**与方块之间不再有间隙**（鼠标可以直接移入，不再一移就消失），列表**固定 340px 宽**（不随号码/名称长短变化），行内同样保留固定首列、号码与按钮对齐。

- **网页控制台：监听名称 / 保持状态 / 会议列表细节**：① 监听行主叫只取 `callerNo + callerName`，缺名称时**不再回退成监听座席自己的名字**（用户实测）；② 修复"保持"按下不变"取消保持"：`call.muted` 事件的负载只有 `{callId, muted}`（不带 call 快照），网页 store 现在把它并回 `state.call`；取消保持仍为**黄色**；③ 会议列表：**主持人永远第一行**（桌面 UI 与网页一致）、**非视频会议不显示禁视频**、会控顺序 禁视频/禁音/禁听、**移除成员按钮黄色且在最左边**、主持人名字左侧显示主持人图标并**悬停提示「主持人」**（桌面 UI 同步加上提示）；④ 话务条悬停展开的成员列表改用与通话列表**同一套行**（顺序、左侧图标、会控一致）。

- **网页控制台再修（座席行对齐/三方文案/会议入口）**：① 在线座席行的状态与技能组 tag **右对齐**并留出与操作按钮的间距；② 咨询状态下话务条按钮名改为「三方通话」；③ 话务条的拨号盘按钮改为**蓝色底**；④ 「会议」按钮**点击直接转成会议**（`cti.action addConferenceMembers` 空成员，与桌面 UI 行为一致），进入会议模式后 owner 才显示「添加成员」，点它才打开拨号对话框。WS 侧 `addConferenceMembers` 允许空 `members`（`removeConferenceMembers` 仍要求至少一个），回归测试同步。

- **网页控制台续修（在线座席行/监听行/按钮样式）**：① 拨号对话框的在线座席行改为与 UI 相同：左侧座位图标、中间座席号/名称、**右上状态、右下技能组 tag**（多组时首组 + `more` 悬停看其余）；② 咨询/转移/会议等话务文字按钮统一**蓝色**；③ **监听改为一行**，按 UI 显示"主叫 → 被叫"（矢量箭头），去掉逐方的结束监听按钮，统一用底部挂断（文案「结束监听」）；④ `CONSULT_RINGING` 状态显示为**咨询振铃中**（原为咨询呼叫中）；⑤ **通话方右侧的按钮恢复为图形按钮**（与 UI 一致：结束咨询=红色方块、强制挂断/移除=红色圆圈减号、会议会控=麦克风/扬声器/摄像头图标），全局按钮仍为文字。

- **网页控制台拨号弹层与按钮样式修订**：① 拨号对话框与迷你 DTMF 盘改为**跟随鼠标点击位置浮现、无遮罩**，点弹层外面关闭；页签改成 **Edge 风格**（下划线）；关闭按钮改为 **X**；打开时**不聚焦输入框**（避免建议下拉挡住按钮），同时保留对话框内**直接键入数字/退格**；迷你 DTMF 盘去掉底部大「关闭」。② 通话控制卡片与话务条的操作按钮改为**文字按钮**（仅拨号盘保留图形按钮），图形按钮统一用桌面 UI 的同一份 PNG（`web/img/*.png`，随二进制内嵌并加入静态白名单）；在线座席行左侧改用**座位图标**（绿/黄/红按状态着色）。已验证：弹层坐标、键盘输入/退格、外部点击关闭、文字按钮与图标资源。

- **网页控制台：拨号对话框 + 多方通话面板/话务条**（REQ-WEB-006，七项）：① 通话页删除常驻拨号卡片，改为**拨号对话框**（点遮罩自动关闭），由「拨号/咨询/转移/会议（添加成员）」唤出；② 对话框分**拨号盘**（输入+建议+号码盘+随模式变化的动作按钮，无按键音/提示行）与**在线座席**（仅坐席模式；行内按钮规则同桌面在线座席页）；③ 两方接通后通话卡片与话务条的保持/挂断左侧新增**咨询/转移/会议**按钮，打开对话框；④ 最左侧新增**迷你拨号盘**按钮，发送 DTMF；⑤ 通话控制卡片改为多方：状态+计时一行，其后每方一行、其控制按钮在行右侧（结束咨询/强制挂断/会议成员会控/结束监听），全局操作在最后一行；⑥ 话务条按方分块、块右侧为图形按钮，**会议只显示主持人块并悬停展示成员列表**；⑦ 来电振铃去掉话务条的挂断（与拒接同义）。配套 WS `cti.action` 补会议成员会控动作（`member` 参数）、`consultThreeWay`/`consultTransfer` 的 target 改为可选（缺省用当前咨询），SDK 新增 `setConferenceMemberMuted/Deafened/VideoMuted`，`ctiAction` 支持对象参数。新增 `web/js/callui.js`、`web/js/dialpad.js`（均加入静态白名单）。

- **CTI 协议 1.0.18：seq 改字符串 + MSP_UNSUPPORTED**：① `MSP_ACTION` / `MSP_HEARTBEAT` / `MSP_RESULT` 的 `seq` 全部改为**字符串**（`core.CTIResult.Seq`、`Telephony.SendCTICommand` 返回值同步改类型；解析用 `flexString` 兼容老服务端下发的数字）；`cti.action` / `cti.listAgents` 的回包与 `cti.result` 事件里 `seq` 也是字符串。② 新增 `MSP_UNSUPPORTED` 通知：收到后**停止发送 `MSP_HEARTBEAT` 并退出坐席模式**；`MSP_HEARTBEAT` 的 **SIP 响应不是 200** 同样处理（`sip.SendHeartbeat` 返回 `core.ErrCTINotifyUnsupported`，核心层 `handleCTIUnsupported` 统一收口）。回归：`TestCTIUnsupported`、`TestFlexStringCode`。

- **SDK 与控制台补齐 CTI 能力**（REQ-WS-022 / REQ-SDK-009 / REQ-WEB-005）：① **WS 新增** `cti.action`（consult/consultThreeWay/transfer/consultTransfer/monitor/takeover/exitThreeWay 带 `target`，addConferenceMembers/removeConferenceMembers 带 `members`，finish*/whisper/intervene/intercept/setBusy/setIdle 无参数）与 `cti.listAgents`——两者都**等 MSP_RESULT 后同步回包**（不再让页面自己对齐 seq），错误码 `cti_failed`/`cti_busy`/`cti_timeout`/`cti_not_agent`；`cti.agent`/`cti.state`/`cti.result` 事件随总线转发，`system.state` 带坐席字段。② **SDK 新增** `listAgents()`、`setAgentBusy()/setBusy()/setIdle()`、通用 `ctiAction()` 与逐一封装（consult/consultThreeWay/transfer/consultTransfer/monitor/takeover/exitThreeWay/addConferenceMembers/removeConferenceMembers/finishConsult/finishConference/whisper/intervene/intercept），d.ts 补 `SipAgent`、`SipCTIActionResult`、`SipCTIState`。③ **控制台新增**：顶栏**坐席徽章**（空闲绿/置忙黄，点击置忙/置闲）与「在线座席」页（搜索/刷新、状态圆点、技能组 tag + more、按座席状态显示呼叫/视频/监听/代接、页内话务动作模式：咨询/转移/会议只在通话接通后可选，动作成功后回到直接模式并刷新）；本机座席过滤；通话条接通后优先显示咨询中/三方通话中/监听中/会议中。

- **在线座席操作按钮改按座席自身状态显示 + 代接**（REQ-ACC-035 修订）：① **非通话状态**：座席**空闲** → 呼叫/视频呼叫；座席**通话中** → 监听（仅班长模式；空闲座席不再多显示监听）；座席**振铃中** → **代接**（普通模式、班长模式都有，新增 `icons/代接.png` 图标，点击下发 CTI 动作 `takeover`，参数 `target`）；其余状态（呼叫中/置忙/离席/示忙/话机未连接/离线）不显示按钮。② **复用模式只有空闲座席可操作**：咨询 → 咨询/三方；转移 → 直接转移/咨询转移；会议 → **加入会议**（悬停提示从「会议呼叫」改为「加入会议」，动作仍是 `addConferenceMembers`）；非空闲座席不显示。③ 本机通话中（未进入拨号复用）仍全部隐藏；通信录/通话记录行不受影响。回归：`TestListRowActionsByState`。

- **托盘菜单「登录账号 / 退出账号」+ 服务端注销通知 + SDK/控制台 register/unregister**（REQ-ACC-024 / REQ-WS-021 / REQ-SDK-008）：① 托盘右键菜单在「显示主界面」下面按**当前注册状态**动态显示——已注册/注册中为「**退出账号**」（发 expires=0、停注册周期，并**把「启用账户」置 false**，下次启动不再自动注册）、未注册/注册失败为「**登录账号**」（启用账户并立即注册）；**共用「启用账户」这一个配置项**（与配置页勾选、`system.setAccount` 是同一开关，不再另设「启动自动注册」）。② 支持服务端**注销通知**（私有控制协议的 `MSP_UNREGISTER`，见下 REQ-ACC-025）：校验来源为注册服务器（`regPeerIP` 优先）后，**记 Warn 日志、退出账号（取消注册、停周期、启用账户=false）**；来源不符/非本事件忽略。③ **SDK** 新增 `register()` / `unregister()`（= 登录/退出账号），WS 新增 `system.register` / `system.unregister`；网页顶栏**注册徽章可点击**（与托盘菜单行为一致）。④ sip:// 拉起仍强制启用账户并注册。WS 断线自动注销（REQ-WS-020）与这些动作共用同一配置项，手动退出后重连不会恢复注册。

- **服务端私有控制协议**（REQ-ACC-025，协议见 `docs/cti-sip-phone-protocol.md`）：在 REQ-ACC-024 的注销通知之上，把整条「SIP NOTIFY `Event: MSP_*`」链路补全为 5 个事件——`MSP_DIAL`（远程拨号）、`MSP_ANSWER`（远程接听）、`MSP_HANGUP`（远程挂断）、`MSP_SET_AUTOANSWER`（远程改自动接听开关）、`MSP_UNREGISTER`（注销，即原 `mti-sip-unregister` 改按协议命名）。实现：NOTIFY handler 内先同步回 200 再异步执行（`handleControlNotify`）；只接受注册服务器来源（`regPeerIP` 优先，判断不了放行并告警）；REGISTER 的 `Allow` 头补 `NOTIFY`（否则 OpenSIPS `lookup("location","m")` 按方法过滤回 511）。**视频能力一律静默降级**：`withVideo=true` 但话机视频不可用（`VideoUsable()` = 构建支持 + 配置总开关）或呼入本身不含视频（`Call.HasVideo`）时，降级为纯音频拨号/接听，只记 Info 日志、不报错。新增 `core.RemoteCommand` + `EventSink.OnRemoteCommand`（`internal/core/telephony.go`），`core.App.OnRemoteCommand` 执行（含 `AnswerOrVideo`/`ringingIncomingCall`/`activeCall` 辅助）。

- **坐席模式（MSP_EVENT_AGENT_ONLINE / AGENT_OFFLINE）**（REQ-ACC-028）：话机收到 `MSP_EVENT_AGENT_ONLINE`（body 带 `expires`，默认 50 秒；`displayName` 为坐席显示名）才进入**坐席模式**，每次新的 ONLINE 都重置计时，超过 `expires` 未续期自动退出；收到 `MSP_EVENT_AGENT_OFFLINE` 立即退出。**只有坐席模式下才在通话面板第一行显示高级话务按钮**（咨询/转移/会议），并且**只在两方通话已接通时显示**——拨号中/振铃中即使在线也不显示。**坐席显示名**：进入坐席模式后窗口标题与托盘提示在「mti-sip-phone - 2999」后追加 `displayName`；主窗体「已注册」后面显示一个电脑小图标表示坐席在线（退出坐席模式即隐藏），鼠标悬停显示「座席已登录 - displayName」（无 displayName 时只显示「座席已登录」）。状态变化发布 `cti.agent` 事件（含 `displayName`），`system.getState` 新增 `agentOnline` / `agentDisplayName`；退出坐席模式时会一并退出可能停留的拨号复用（咨询/转移/加会）；**登出账号（托盘「退出账号」/网页徽章/SDK unregister/服务端 MSP_UNREGISTER/断线自动注销）也会立即退出坐席模式**。

- **反向话务控制（in-dialog SIP INFO）+ 通话面板复杂话务状态**（REQ-ACC-026，协议见 `docs/cti-sip-phone-protocol.md` 第二/三部分）：① 通话中沿当前 dialog 发 INFO 上报 `consult`/`consultTransfer`/`consultThreeWay`/`finishConsult`/`exitThreeWay`/`transfer`/`whisper`/`intervene`/`intercept`/会议成员控制等动作，body 为 `{seq, extension, action, ...}`；**同一时间只允许一条在途**，**2 秒内没收到 `MSP_RESULT` 即判失败**，结果按 `seq` 对齐（超时与回执竞态只第一个生效）。② REGISTER 的 Contact 追加 `;msp=support` 做能力协商。③ 收到 `MSP_EVENT_*` 更新 `Call.CTI` 话务状态（twoParty/consult/threeWay/monitor/conference），通话面板据此重建：两方通话保持左侧加咨询/转移/会议；号码与状态两行对换、呼出号码后绿色动态箭头；咨询/三方/监听/会议各有对应行与按钮；三方只列两个对端（不显示自己）；会议成员行用矢量图标表示通话状态，owner 可点击切换成员的静音/禁听/禁视频并有移除(X)，普通成员只看状态、自己的会控在最下一排；主窗体拨号框可复用于咨询/转移/加会（复用期间只输入、不发 DTMF）。

- **`docker/build.bat`（Windows 一键容器构建入口）**：cmd / PowerShell 里直接跑，参数与 `docker/build.sh` 一致、原样透传（内部自动找 git-bash，写法与 `scripts/release.bat` 同款）——`docker\build.bat --libs`（首次编第三方库）/ 无参数（出全部 10 个产物）/ `--build-image` / `--shell`。`.bat` 注释刻意用英文：cmd 按 ANSI/GBK 代码页解析批处理，UTF-8 中文注释行会被拆碎成乱码命令片段（实测）；中文说明都在 `docker/README.md` 与 build.sh。另修复 `release.sh` / `build.sh` 的版本号解析在 Windows 检出（CRLF 行尾）下失效的问题——sed 的 `"$` 锚点被行尾 `\r` 挡住，末尾改 `.*$` 吸收。

- **反向控制改走 out-of-dialog NOTIFY + 话机主动心跳**（协议更新，REQ-ACC-026 / REQ-ACC-028）：① 话机 → CTI 的咨询/转移/三方/监听/会议等指令从 **in-dialog SIP INFO** 改为 **out-of-dialog NOTIFY**（`Event: MSP_ACTION`，body 不变；不再要求正在通话，monitor/takeover 等可在通话外发起），结果仍经正向 NOTIFY 的 `MSP_RESULT` 按 `seq` 对齐（2 秒超时、单条在途不变）。② 新增 **`MSP_HEARTBEAT`**（`Event: MSP_HEARTBEAT`，body `{seq, extension, expires, agentMode, displayName}`）：**注册成功后由话机主动周期发送**，注销即停；间隔可配置（配置页「CTI 心跳间隔（秒）」/ `account.ctiHeartbeat`，默认 20 秒；网页 WS `system.setAccount` 同样支持），`expires` = 间隔 × 2.5。③ `MSP_EVENT_AGENT_ONLINE` **不再带 expires、不再过期**（坐席状态的保持改由心跳维持），只有 `AGENT_OFFLINE`、本地登出或注销才退出坐席模式。

- **话务事件带名称字段**（协议 §16 名称字段约定）：`MSP_EVENT_*` 里每个号码字段现在都伴随 `xxxName`（`consulterName`/`consulteeName`/`callerName`/`thirdPartyName`/`ownerName`/`monitorName`/`targetName`，会议成员是 `displayName`），仅已登录坐席才有值。话务面板显示号码时**优先用这个名称**（「号码 名称」），为空才回退通信录的名称，再没有就只显示号码；被咨询方、三方对端、监听双方、会议成员全部生效。

- **通话面板各状态布局定稿**（REQ-ACC-026 补充，按用户给的布局图重排）：① 呼叫中 / 来电 / 通话中仍是三行，**来电第二行的红色按钮为「拒接」**，第三行按钮顺序改为 **摄像头 → 麦克风 → 扬声器**；② **咨询**：行1 状态（接通后右侧 [三方通话][转移]），行2 原对端（拨号阶段号码后跟绿色动箭头），行3 被咨询方 + [取消咨询]/[结束咨询]，行4 时长；接通文案「咨询通话中」；③ **三方**：行1 状态 + [退出三方]，第 2/3 行是另外两方 + [强制挂断]（仅 owner，exitThreeWay），普通成员不显示；④ **监听**：行1 状态 + [耳语][插话][强拦]，行2 「监听方 >>> 对端」+ [结束监听]，行3 时长，耳语态文案「监听耳语中」；⑤ **会议**：行1 状态 + [结束会议]（owner）/[退出会议]（成员）；成员行 = 号码（owner 号码后带主持人标记）+ [禁视频][禁音][禁听]（owner 可点击切换，成员只读），owner 另加 [移除]；正在呼入的成员显示 `>>>` + [取消呼叫]；移除了「加人」入口和成员视角的会控底排。⑥ **`MSP_EVENT_CONFERENCE_FINISHED` 等同于收到 BYE**：本端直接挂断这通电话，通话面板随之关闭（服务器先发 BYE 也一样）。

- **通话面板按钮的可读性**（REQ-ACC-026 补充，用户反馈）：① **图标改用矢量图库原样内嵌**——主窗体/托盘/方向/通信录/通话记录按钮用 **iconfont 集合 50996（mti-* 系列）**，通话面板话务图标用 **SVG.zip 里的原始 SVG**，全部直接内嵌成单色 SVG（`icons_mti.go` 的 `mtiSVG`，白/绿/红按用途替换 fill），不再手画、不转 PNG；咨询=SVG.zip 的咨询图形、三方=三方通话、会议=会议、转移=转接等；② **主窗体底部的按钮改暗绿底白字/白图标**（`colorCallGreen` #2e7d32）：「呼叫」「视频呼叫」以及拨号复用（咨询/转移）的两个动作按钮，后者还改成**左右等宽并排**（原来上下两行）；顶栏「已注册」、托盘/窗口话机图标等**其它绿色保持原样**（试过统一成暗绿，观感不好，已回退）；③ **状态文案**：咨询中 / 三方通话中 / 监听中 / 会议中，**回到两方自动恢复「通话中」**（原来会停在上一个话务状态），并修掉"保持事件把咨询中冲成通话中"；④ **三方面板**：第一行只留「退出三方」按钮（提示"退出三方通话"），第 2/3 行只有 owner 带 X（exitThreeWay），普通成员不带按钮；⑤ **修拨号时号码被截成「…」**（`callNameBox.MinSize` 误用了截断后的文本宽度），拨号箭头放大一号并固定宽度不再抖动；② **面板上每个按钮都有悬停文字提示**（挂断/接听/视频接听/保持（保持中显示「取消保持」）/扬声器（滚轮调音量，0% 关闭）/麦克风/视频/咨询/转移/会议/三方通话/咨询转移/结束咨询/耳语/拦截/强插/添加成员/结束会议/移除成员/挂断该方/成员静音·禁听·禁视频（已生效时提示「取消…」）；③ **主窗体拨号复用的按钮图标随动作变化**，并把第二动作放到主按钮**下方**：发起咨询（咨询图标）/发起三方（三方图标）、直接转移（转移图标）/咨询转移（咨询图标）、加入会议（加号图标）。

- **坐席置忙/置闲 + 坐席按钮 + 图标颜色（REQ-ACC-034，协议 §9.3 / §10 / §16.1）**：① 新增 CTI 动作 `setIdle` / `setBusy`（无参数，结果走 `MSP_RESULT`；CTI 之后会另推 `MSP_EVENT_AGENT_ONLINE` 同步最新 `isBusy`）。② `MSP_HEARTBEAT` 与 `MSP_EVENT_AGENT_ONLINE` 增加 `agentMode`（`none`/`normal`/`dial_out`/`monitor`）与 `isBusy`：心跳如实上报本地坐席模式与置忙状态；ONLINE 同步两者，其中 `agentMode=none` 表示"话机不该处于坐席模式"，按退出坐席处理（老 CTI 不带该字段时按普通模式处理，不会误退出）。③ 主窗体「已注册」后的坐席图示换成 **`icons/座席.png`** 的**可点击按钮**：绿色 = 空闲、黄色 = 置忙，悬停显示「座席空闲/置忙 - 显示名（点击置忙/置闲）」，非坐席模式（agentMode=none）隐藏；点击切换 `setBusy`/`setIdle`（复用 CTI 指令的在途互斥与失败提示，结果由 CTI 推回的 ONLINE 驱动）。④ `agentMode=monitor`（班长模式）时坐席按钮旁显示**监听按钮**（交互待定，仅展示并悬停说明）。⑤ 自动接听按钮从文字按钮改为 **`icons/自动接听开启.png` 图形按钮**：开启绿色、关闭灰色，文案走悬停提示（打开/关闭）。⑥ **应用、窗口与托盘图标颜色**：未注册与**注册中一律红色**，已注册且坐席空闲绿色，**已注册且坐席置忙黄色**（新增 `IconPhoneYellow` / `appicon.PhoneYellowPNG`，无 UI 版托盘同样支持）；应用图标、主窗口（标题栏）图标与托盘图标都随状态更新，**顶栏的注册圆点与「已注册」文字也一起变**（置忙黄、空闲绿，文字仍显示「已注册」）。**已知限制：任务栏按钮的图标不跟随**——Windows 的任务栏按钮只在创建时读一次窗口图标，之后只改 `WM_SETICON` 只影响标题栏；试过"撤下/挂上窗口强制重建按钮"、"自己造 HICON 同步 WM_SETICON 再重建"（并做过实测轮询：窗口图标确实已换，任务栏仍是旧色），Explorer 仍沿用缓存的应用图标，故这段逻辑已回退（避免标题栏图标异常）;续注册（registering）时托盘仍保持上一次确定状态的颜色，避免定时续注册闪红；**顶栏状态圆点/「注册中」文字的颜色与托盘统一：注册中也是红色**（原来圆点是黄色），置忙黄、空闲绿。

- **托盘图标尽量常显在通知区（不被收进 ^）**：Windows 没有官方 API，通知区哪些图标常显由外壳按用户设置决定。程序启动、外壳登记完托盘图标后（约 3 秒），把 `HKCU\Control Panel\NotifyIconSettings\<id>` 里属于本程序（`ExecutablePath` 与当前 exe 一致，支持外壳的已知文件夹 GUID 前缀写法）的条目置为 `IsPromoted=1`——**尽力而为**：Windows 11（内部版本 ≥ 22000）生效，生效时机由外壳决定（通常重启资源管理器或下次登录时应用）；Windows 10 的托盘设置是二进制 `IconStreams` 块，不去动它。失败/未找到只记日志。

- **在线座席页 + 列表行话务按钮（REQ-ACC-035，协议 §9.5 / §11）**：① 主窗体坐席状态按钮右侧新增「座位表」图标（`icons/座位表.png`，仅坐席模式显示），点击打开**在线座席页**；② 页面调用 CTI 动作 `listAgents` 拉取在线坐席（结果走 `MSP_RESULT.data`，按座席号排序），顶部搜索框按**座席号/显示名/技能组**过滤、右上角刷新按钮重新拉取；③ 列表与通信录同款两行（上=座席号、下=显示名），左侧座席图标按状态着色：绿=空闲、黄=呼叫中/振铃中/通话中、红=置忙或异常，悬停显示状态文字；左侧座席图标**不再弹悬停提示**（行内已有状态文字；纯展示图标也不显示悬停轮廓），操作按钮**左侧、靠右**再显示两行信息：上层**座席状态**（文字再左移 4px）、下层**技能组**（以 **tag** 显示：只显示第一个，多个时再加一个 `more` tag，**悬停 more 显示其余技能组**）；④ 行内操作按钮随状态变化：空闲=呼叫/视频呼叫（**班长模式**在呼叫左边多一个「监听」——图标比其它行内图标略大，原主窗体监听按钮移除）；**本机座席不显示在列表里**；`miss_phone` 的状态提示为「话机未连接」、**通话中隐藏**、咨询复用=咨询/三方、转移复用=直接转移/咨询转移、会议复用=会议呼叫——点击与"主窗体输入号码后点动作按钮"完全一致（`runCTI` + 记入最近呼叫）；⑤ **在线座席、通信录、通话记录在通话中都可打开**（原来会被拒绝），行内按钮按上述规则显示；⑥ `MSP_RESULT` 解析补上 `action` 与 `data`（原来只取 seq/success/code/message，`listAgents` 的结果因此能落到界面）。

### 修复

- **`build.sh --target` 目标名**：未知目标（如 `linux-amd64`）原来会静默跳过全部构建、只打印「构建完成」；现在直接报错退出并列出可用目标，同时支持 `linux-amd64` / `linux-arm64` 架构别名（与本机架构相同 → `linux-native`，对侧 → `linux-cross`）。补上脚本可执行位（git 里原为 644）。

- **通话记录保存并优先显示座席显示名**（REQ-CALL-007 补充）：从在线座席列表直接呼出时，把座席的 `displayName` 写进通话记录的新列 `display_name`（SQLite user_version 2；旧库打开时自动 `ALTER TABLE` 补列，旧记录为空）。记录展示名称的优先级变为**座席显示名 > 通信录名称 > SIP 显示名**（桌面通话记录页、网页通话记录与回拨都沿用该名称：桌面回拨走 `onCallButtonNamed`，网页 `call.dial{name}`）。`calllog.query` 返回新增 `displayName` 字段。回归：`TestDisplayNameRoundTrip`、`TestLegacySchemaGetsDisplayNameColumn`、`TestHistoryDisplayPrefersDisplayName`。

- **监听面板主被叫行：号码完整显示、名称可截断 + 悬停看全文**：原来整行是"号码 名称"一个文本，空间不足时按左到右截断，电话号码先被截成 `1381…`（用户实测）。现在拆成 [左号码][左名称] → [右号码][右名称]（`monitorPeerLayout`）：**号码永远按完整宽度显示**，名称按剩余空间等比分配、放不下才补省略号；名称被截断时**悬停显示完整名称**（`newHoverClipNote`，未截断不弹提示）。回归：`TestMonitorPeerLayoutKeepsNumbersFull`。

- **座席列表直接呼出时优先显示座席显示名**（REQ-ACC-035 补充）：从在线座席页点「呼叫/视频呼叫」时把该座席的 `displayName` 带进呼叫（`core.DialNamed`/`DialVideoNamed` → `Call.PeerName`），通话面板/通话条的名称优先级为**座席显示名 > 通信录名称 > SIP 显示名**；普通拨号行为不变。网页同步：`call.dial` 新增可选 `name` 参数，SDK `dial(target, {video?, name?})` / `dialVideo(target, name)`，在线座席页直接带上 `displayName`。回归：`TestDialNamedPeerName`。

- **三方返回两方时对端号码按 FINISHED 的 caller/called/agent 判定**（REQ-ACC-032 修订）：此前只按"剩余两方里不是本机的一方"取，遇到 agent 字段与实际对端不一致时面板会显示错的一方。现在按用户口径：**agent 存在且 = 本机 → 对端取 caller；caller = 本机 → 取 called；called = 本机 → 取 caller**（矛盾组合下回退到"剩余两方里不是本机的一方"），对端换了时沿用三方面板里该方名称。回归：`TestThreeWayFinishedConvergesPeer`（新增 agent 场景）。

- **取消「启用服务端心跳检测」（optchk，REQ-ACC-020/021）**：按用户要求移除设置项与整套实现——`account.optchk` 配置字段、注册报文 User-Agent 的 `optchk` 标记、传输层 OPTIONS 计数与间隔自学习、2.5 周期漏检后置未注册并立即重注册；桌面设置页与网页配置页的勾选项一并删掉（旧配置文件里的 `optchk` 字段被忽略）。与 CTI 的 `MSP_HEARTBEAT` 无关。

- **主叫是本机分机号的通话不再记入通话记录**（REQ-CALL-007 补充）：CTI 平台在监听/会议等场景会以本机号码作为主叫回呼话机，之前这些来电会把通话记录刷满「自己呼自己」的条目。现在 `OnIncoming` 在写记录前用 `NormalizeNumber` 比较主叫与本机分机号（`core.App.selfCaller`），相同则不写开始记录（通话本身照常振铃/接听/挂断）；配置里没有分机号时不拦。回归：`TestCallLogSkipSelfCaller`。

- **强拦按钮配色 + 拨号复用期间点坐席按钮误关主窗体**：① 监听面板的「强拦」按钮底色从黄色改为与耳语/插话一致的蓝色；② 主窗体处于咨询/转移/加会的**拨号复用**状态时，任何一条 CTI 成功回执都会收起主窗体——用户点了「座席状态」按钮（`setBusy`/`setIdle`）也被当成复用动作完成，窗口莫名其妙关掉。现在只有**该复用模式自己的动作**（咨询/三方、直接转移/咨询转移、会议加人）回执才收尾，其余指令（坐席置忙/置闲、`listAgents` 等）不影响主窗体。回归：`TestCTIDialAction`。

- **监听面板改显示"主叫 → 被叫" + 三处细节**（REQ-ACC-036）：① `MSP_EVENT_MONITOR_STARTED` 新增 `callerNo/callerName/calledNo/calledName`（被监听话务的双方），监听面板第二行改为显示**主被叫**——`callerNo` 是 `target` 时显示 `callerNo → calledNo`，否则显示 `callerNo → target`（名称优先协议、回退通信录）；中间的 `>>>` 换成**矢量箭头图标**；耳语/强拦不重复携带，沿用 STARTED 的值。② **通话中自动接听按钮保持可点**（原来随设置/呼叫按钮一起禁用）。③ 「三方通话」图标略放大：通话面板 20→22px、列表行 14→16px。

- **图标刷新真因：换资源后位图不重画**：Fyne 的 `canvas.Image` 在**只换 `Resource`** 时 `Refresh()` 不会重新栅格化 SVG（同尺寸直接命中旧的位图缓存）——表现为「禁用视频后图标一直是蓝的」，**转圈动画也因此"转了但画面不变"**（面板/主窗体按钮每帧换图标资源，屏幕上却一动不动，这正是前几轮"看不到转圈"的真正原因；音量按钮因为布局里尺寸变化侥幸避开了）。修复：`compactButton.applyIconColor` 换完资源后做一次**整数像素**的 Resize 抖动强制重栅格化（`rerasterIcon`）。回归：`TestCompactButtonIconReraster`。另：主窗体「呼叫」按钮构造时文字改为白色（此前初始一帧是黑字，要等一次状态刷新才变白）；禁用灰按主题取色——**深色主题浅灰、浅色主题深灰**（原来直接用主题禁用色，深色主题下近黑看不清）。

- **禁用样式统一 + 图标微调**：① **禁用样式两个窗口一致**：一律"图标置灰 + 背景透明"——咨询面板里的只读「保持中」指示不再画灰底块（只留灰色图标），主窗体的紧凑按钮禁用时也不再变灰底（文字一并置灰，恢复时还原底色/文字色）；② 麦克风/扬声器禁用时用**同一字形 + 斜杠**的禁用图标（斜杠是叠在同一个字形上，位置与开状态完全一致，不会跳）；③ 退出咨询/结束会议图标在原位置基础上右移 1px、下移 2px；④ 麦克风/扬声器的图标与右侧进度条**整体**左移 2px。

- **心跳上报 `status`（协议扩展，REQ-ACC-033）+ 面板细节调整**：① `MSP_HEARTBEAT` 的 body 新增 `status`（**协议约定的字符串**）：`free`（空闲）/ `dialing`（呼出：呼叫中或回铃振铃）/ `ringing`（呼入振铃）/ `talking`（通话中）/ `holding`（保持中，与保持按钮同步）；**话务状态一变化立即补发一条心跳**（否则最多要等一个心跳周期才同步给 CTI）。② 面板图标微调：接听/视频接听/咨询/转移/退出咨询/强制挂断图标缩到 16px；强制挂断改黄色；麦克风/扬声器图标左移 2px。③ **转圈反馈修到可见**：主窗体拨号复用流程（发起咨询/转移/加会、发起三方/咨询转移）改为用**主窗体上的动作按钮**转圈，且主窗体**不再立刻隐藏**——等 MSP_RESULT 回来才收起（成功）或停在原页显示错误（失败）；同时加了 CTI 指令/转圈/收尾的日志，便于排查。② 音量/麦克风的竖直进度条与图标间距收到 2px。③ 麦克风/扬声器开/关**改用同一个字形**（只换颜色：蓝/红）——原来换成"斜线"图标时字形不同，视觉上像图标往上跳了一截。④ 「结束咨询」图标下移 2px（原图画得偏上）；「三方」图标裁掉空白边后等比放大并按 20px 显示（原图有效区域宽扁，显得小）。⑤ 强制挂断/移除成员的图标换成**圆圈里一个减号**（程序生成，白色图形按红色着色）。

- **通话面板按钮样式改版（尝试性改动）**：① 面板上的所有按钮改成主窗体配置按钮那种**无底色**样式——平时背景透明（= 窗体背景），鼠标移上去才显示淡填充 + 1px 轮廓；图标颜色沿用原来的按钮底色（挂断/关闭红、接听绿、咨询/三方/会议蓝、保持黄），强制挂断/移除成员也别搞小号了，统一成标准按钮。② 音量/麦克风原来的"圆周填充"改成**竖直进度条**：贴在按钮右缘内侧、高度与图标一致，从底部按百分比填充，麦克风 100%→200% 仍是蓝色段之上叠橙色段；滚轮调值、点击开关的行为不变。③ 消息条底色改**淡一点的红色**。改动前的界面代码已备份（`D:\cache\temp\ui-backup-20261021-143353\` + `git stash create` 快照 `e22322e`），效果不好可直接回滚。

- **CTI 指令的"在途"交互 + 结果错误提示（REQ-ACC-026 补充）**：① **MSP_RESULT 解析兼容数字错误码**：协议里 `code` 写作字符串，CTI 实际下发的是数字（如 `{"code":770007,...}`），按 string 解析会让整条回执解不出来、被静默丢弃，界面只能干等超时——新增 `flexString`（字符串/数字都收）。② 点击下发指令的按钮后，按钮图标**立即**变成**旋转圆圈**（8 帧程序生成的白色圆弧，先同步换第一帧再起定时器）直到收到 MSP_RESULT；结果来得太快（几十毫秒）时也**至少显示 500ms**，否则用户根本看不到反馈；期间该按钮点不动、点其它话务按钮提示「上一个操作尚未完成」（协议本来也只允许一条在途）。**"点了没转圈"的真因**：主窗体拨号复用流程（发起咨询/转移/加会）调用时没有按钮对象（btn=nil），而"在途"锁当初是用按钮指针当标志的——nil 时锁根本没建立。现在在途状态独立成 `ctiInFlight`，没有按钮也照样锁住（同一时间只允许一条指令）。③ 失败/超时改用**窗口下缘居中的消息条**显示（背景色与窗体明显区分：普通提示深灰蓝、错误淡红），普通提示停留 1 秒、错误消息 2 秒——原来借用悬停提示样式，太小看不出来，而且失败时只写日志。回归：`TestFlexStringCode`、`TestCTISpinFrames`。

- **强制挂断 / 移除成员按钮改为统一风格 + 半号尺寸**：图标换成从用户提供的挂断图标**抠出白色话机字形**（`newIconPNGKeyedWhite`：按"白度"做 alpha，去掉红色圆盘），按钮用项目统一的"红底圆角 + 白图标"样式（不再是一整块红色圆盘）；尺寸缩到标准面板按钮的**一半**（18px）、**圆角 1px**，外套 36×36 盒子保证纵向居中、与第一行最后一个按钮左右居中对齐。

- **强制挂断 / 移除成员改用挂断图标 + 三方结束恢复话务入口**：① 三方通话的「强制挂断」（原白色 X）与会议成员行的「移除成员」改用**圆圈减号**图标（程序生成，见上一条）；未再使用的 `remove-member.png`、`hangup.png` 与 `IconCTIRemoveMember` 一并删除。② **三方转回两方时清掉「咨询腿」标记**（`Call.Consult=false`）：坐席模式下咨询/转移/会议入口恢复显示（此前从咨询腿进入三方、再回到两方的通话会一直隐藏这三个按钮）。

- **三方通话字段与结束逻辑（协议 §16.3 调整，REQ-ACC-032）**：① `MSP_EVENT_THREE_WAY_STARTED` 改按 **`firstParty`/`secondParty`**（三方建立时原通话的双方）与 `thirdParty` 解析（原来用的 `callerNo`/`calledNo` 已废弃），面板仍只列除自己以外的两方、名称取对应的 `xxxName`；② `MSP_EVENT_THREE_WAY_FINISHED` 改为携带**结束后的剩余两方** `callerNo`/`calledNo`（可选 `agent`：该话务的接听坐席）——话机据此收敛为两方：**不在 `callerNo`/`calledNo`/`agent` 集合里的那一方即被移除的一方**，若本机当前对端正是它（例如发起方退出三方、另外两方继续通话），对端换成剩余的另一方并清掉旧名称，否则保持不变。回归：`TestThreeWayFinishedConvergesPeer`（换对端 / 保持对端两个分支）、`TestApplyMSPEvent`（新字段）。

- **通信录/通话记录按钮禁用时不变灰**（用户实测：通话中这两个顶栏按钮仍是白色，只有设置按钮变灰）：Fyne 的 `buttonRenderer` 只在 `Disabled() && svg.IsResourceSVG(icon)` 时才换用 `theme.NewDisabledResource`——**位图（PNG）图标不会自动变灰**（上游 TODO）。修复：给两个 PNG 图标各备一份按 `theme.ColorNameDisabled` 染色的灰色版本（`disabledPNG`），`applyMainCallMode` 开关时用 `setIconButtonEnabled` 手动换图标，观感与设置按钮一致。

- **CTI 拨号复用也记「最近呼叫」**：主窗体拨号框被咨询/转移/加会复用后，发出的号码（第一动作与第二动作）在指令**发送成功**后同样记进「最近呼叫」（`core.AddRecentDial`，持久化）——下次点拨号框的下拉就能直接选到，与普通拨号一致。

- **咨询期间非发起方隐藏话务入口（`MSP_EVENT_CONSULT_STARTED` 推给相关方，REQ-ACC-031）**：协议 §16.2 里 STARTED 现在也发给**原通话双方 + 被咨询方**——非咨询发起方（body 的 `consulter` 与本机分机号不一致）收到后**不切换面板**（仍是双方通话），只隐藏咨询/转移/会议入口（`CTIState.ConsultInProgress`），避免咨询期间误操作；`MSP_EVENT_CONSULT_FINISHED` 随状态复位自动恢复这些入口。`CONSULT_RINGING`/`CONSULT_CONNECTED` 仍只发给咨询方，不受影响。另修：`cti.state` 事件**只在「通话中」重建面板**——被咨询方会在咨询腿还振铃时先收到 CONSULT_STARTED，原来一律按 `incallButtons` 重建会把振铃面板错误变成"通话中"（接听/拒接按钮消失、误以为已接通）。回归：`TestConsultStartedOtherParty`。

- **咨询转接完成事件（`MSP_EVENT_CONSULT_TRANSFER`，协议 §16.2，REQ-ACC-030）**：事件同时发给转移方与被咨询方，二者动作不同——**被咨询方**接管原通话：话务状态复位成双方通话、**对端号码与名称换成原通话的另一方**（`callerNo`/`calledNo` 里不是本机的那一个，名称取对应的 `xxxName`），并清掉「咨询腿」标记，使通话面板第一排的咨询/转移/会议入口**恢复显示**（此前因 `call-type=consult` 被隐藏）；**转移方**（body 的 `transferor` 与本机分机号一致）收到即**挂断本通话、结束话务**。`cti.state` 事件带上更新后的对端，界面收到后先刷新对端展示再重建面板。回归：`TestConsultTransfer`（含"本机是主叫时取被叫为对端"）、`TestConsultTransferTransferorHangsUp`。

- **INVITE 的 Call-Info：对端名称与呼叫类型**（REQ-ACC-029）：① **呼入**解析 `Call-Info: <sip:2901@10.168.4.40>;display-name=%E6%B5%8B%E8%AF%951;call-type=consult`——`display-name`（URL 解码）作为对端名称（`Call.PeerName`），**显示时优先于通信录名称**（通话面板、视频窗口标题、未接来电通知、网页控制台 `display.peer` 口径一致）；② `call-type=consult` 表示这是服务端推来的**咨询腿**（`Call.Consult`）：接通后通话面板第一排的咨询/转移/会议入口**不显示**（这通本身就是别人咨询的一方）；③ **呼出** INVITE 带上 `Call-Info: <sip:被叫@域>;display-name=<通信录里的被叫名称，百分号编码>;call-type=call`——没有名称时就只带 `call-type`（用户要求"如果有的话"），被叫名称由 core 经 `SetPeerNameLookup` 注入（sip 层拿不到通信录）；④ `answer-after`（REQ-CALL-015）改为与它们共用同一套 Call-Info 参数解析。回归：`TestParseCallInfo` / `TestBuildCallInfo`（汉字百分号编码、名字里的 `;`/`=`/空格必须转义）/ `TestIncomingCallInfo`（带 Call-Info 的来电，核心层拿到 PeerName 与 Consult）/ `TestCallDisplayPrefersPeerName`。

- **坐席状态图标 + 呼叫中指示**：① 主窗体「已注册」后面的坐席在线图标换成 iconfont 的显示器图标（`internal/ui/assets/agent-online.png`，原来是自己拼的 Material SVG）；② 原来的拨号绿色动箭头（`dialingArrows` 的 ▶▶▶ 动画）与会议成员行的 `>>>` 标记整段删除——中途试过用户提供的「呼叫中」三圆点图标，按用户要求最终**不显示任何呼叫中指示**（号码后面保持干净）。③ 三方面板第一行的「退出三方」按钮图标改成与「退出咨询/退出会议」一致的 `exit-conference`（原来借用挂断的白色方块）。

- **复杂话务（4 行）结束后重新来电，通话面板回不到 3 行高度**（用户实测：上次 4 行面板关闭后，新的来电面板变成 170 高、三行内容居中、上下大片留白）。根因是**先量高度、后换内容**：`setCallPanelHeight(3)` 在旧行还挂在面板上时就被调用，而 Fyne 的 `SetFixedSize(true)` 会跑 `fitContent()`，按**当前内容**的最小尺寸把窗口顶回去——4 行内容的最小高是 170，144 的 3 行高度根本设不进去（窗口最终停在 170，与用户截图逐像素吻合）。修复：调高度前**先把旧行卸掉**（`rebuildCallRows` 清空 `callRows.Objects` 后再复位高度；`rebuildTalkRows` 开头同样先清空，覆盖会议成员减少这类"从高往低"的缩放），并把高度计算抽成 `callPanelHeightFor(n)` / `applyCallPanelHeight`，在注释里写明这个必须先卸内容的前提。

- **坐席图标悬停提示"一会有一会没"**（REQ-ACC-028）：日志显示提示已弹出（`显示提示`），屏幕上却什么都不变；重新注册/最小化恢复后又能显示。根因是 Fyne 2.8 的渲染优化只登记**可见**对象的画布归属（`EnsureMinSize` 走 `WalkVisibleObjectTree`），而提示框在隐藏期间从未被遍历过——`cache.GetCanvasForObject` 对它的内部容器、底色、文字全部返回 nil，`Show()` 之后所有 `Refresh()` 都找不到画布、无法把画布标脏，于是**不会重绘**，直到别的事件（点击、窗口重排、重新注册）触发一次整帧重绘才"突然出现"；隐藏时同理（提示会残留）。修复：显示/隐藏时都**用恒可见的图层控件本身**刷新画布（`canvas.Refresh(l)`），并显式跑一次布局（底色/文字在首次显示前是 0 尺寸，不能等"下一次布局"）。回归：`TestTooltipLayerShowsOnFirstShow`。

- **开机自动注册完成后托盘菜单仍显示「登录账号」**：托盘菜单按注册状态动态切换（REQ-ACC-024），但它的刷新只挂在 `register.state` 事件上——**开机自动注册的 register.state 事件发生在界面订阅之前**，界面启动后菜单停在初始的「未注册 → 登录账号」，顶栏文字/圆点却已经是对的，直到下一次续注册（约 3.5 分钟）才纠正。修复：抽出 `syncInitialState()`（注册状态 + 坐席模式，均从 `core.State()` 读）并在 **`buildTray` 之前**调一次——Fyne 的托盘就绪回调会用 `buildTray` 传进去的**首份菜单**覆盖此前的所有更新（`driver_desktop.go` 的 `runSystray` 捕获了首次的 `m`），所以首份菜单本身就得是对的；订阅前后各再同步一次，覆盖"订阅空档"里的状态变化。

- **NAT 后的 UDP 话机收不到对端 BYE（一方挂断后另一方一直挂着）**：sipgo 的出站请求（REGISTER/INVITE）走**临时 UDP socket**，而 Contact 宣告的是**监听 socket**的端口——临时 socket 有 NAT 映射（响应能回来），监听端口却从未发出过包、NAT 上没有映射，服务端按 Contact 把 in-dialog 请求（BYE/INFO）发到「NAT 公网地址:监听端口」时被 NAT 直接丢弃。真机现象：主实例（2999）挂断后，另一实例（1381003）的通话一直挂着（媒体已停、信令收不到），日志抓包确认 BYE 根本没到达。修复：`internal/sip/udpkeepalive.go` 在 **UDP 传输且宣告了 NAT 地址（声明 IP 或 STUN 公网 IP）** 时，定期（20 秒）**从监听 socket 本身**向注册服务器发 CRLF 保活包，建立并刷新「公网地址:监听端口」的 NAT 映射；注册成功后立即补发一次。验证：实例 2（Contact `10.168.5.10:40032`）拨 110、主实例接听后挂断，实例 2 立即收到 `BYE sip:1381003@10.168.5.10:40032` 并以 `remote_hangup` 结束（修复前一直停留在 established）。**使用说明**：UDP + NAT 环境需要在配置页把「声明 IP」设为服务端看到的公网地址（本例 10.168.5.10），否则 Contact 仍指向内网地址、BYE 无处可送。

- **仅托盘无 UI 版（`-tags noui`）的托盘菜单缺「登录账号 / 退出账号」**（REQ-ACC-024 补全）：完整版把这项加在了 `internal/ui` 的 `trayMenu()`，而 no-ui 版的托盘是另一套实现（`internal/tray`，直接用 fyne.io/systray），之前只有「打开控制台 / 退出」，登录/退出账号只能去网页点徽章。现在 `internal/tray` 同样提供该菜单项（位于「打开控制台」下面）：`tray.SetState` 改收注册状态字符串（`registered`/`registering`/`unregistered`/`failed`），内部**只换图标颜色**（仍只有 registered 变绿，registering 保持原色）+ 按状态用 `SetTitle` **就地刷新菜单项标签**（registered/registering → 退出账号，其余 → 登录账号），点击经新增的 `tray.Options.OnAccount(signedIn)` 回调交给 `gui_noui.go` 执行 `app.SetRegistered(!signedIn)`。回归：`TestAccountLabel` 钉标签判定，`TestTooltipText` 改用新签名。

- **多显示器缩放不同时，主窗体跨屏移动后内容偏移**（用户实测：窗口移到另一块屏后内容挤在窗口下半部）：两块屏缩放不同（如 200% ↔ 150%）时，Fyne（2.8.1）的 canvas scale 会随窗口所在显示器变化，但**固定尺寸窗口的物理尺寸与画布重排不同步**——窗口物理尺寸错乱（实测 333×656 → 441×864，不成比例）、内容整体偏移。已用同版本 Fyne 的最小程序复现确认是框架层行为。修复：**每秒轻量轮询 `Canvas().Scale()`**（`Settings().Scale()` 在自动模式下恒为 0、没有变化通知可监听），**首轮无条件重设一次固定尺寸**（纠正"窗口创建早于缩放检测"的初始错误）、之后只在缩放真正变化时重设——逼 Fyne 按新 scale 重算窗口物理尺寸与画布，验证：跨屏后窗口物理尺寸严格按新比例（主屏 200% → 666×1311、副屏 150% → 502×986，=320×620 DIP×scale+边框）。缩放相同的机器（恒 1.0）不受影响。
- **媒体端口与信令出站端口"撞号"导致信令"只出不进"**（`internal/sip` 测试偶发 5s 超时的真因，也是**多实例同时呼叫**的同一隐患）：媒体引擎的 RTP/RTCP 原绑**具体 IP**（出口 IP，如 127.0.0.1），而 sipgo 的信令出站 UDP socket 绑**通配（0.0.0.0）**——Windows/Linux 都允许两者**同端口共存**（不同具体度），内核随机端口一旦撞号，对端回给出站 socket 的响应（`dst=具体IP:P`）会被"绑定更具体"的媒体 socket **吸走**，出站永远收不到任何包（REGISTER 的 405、INVITE 的 180 全丢，~3-8% 概率；媒体选端口在后时命中，信令出站后分配时也命中——跨实例同样发生）。修复：**媒体 socket 一律改绑通配（0.0.0.0）**——与信令出站成为"同形态真冲突"：RTP 端口由内核分配（只挑完全空闲的端口）、RTCP 端口（P+1）撞上就绑失败重选，**撞号在分配层被内核整体排除，不分实例、不分先后**（SDP 宣告地址是与绑定独立的 `sdpIP`，不受影响）。**同时修复信令监听的 TOCTOU**：`FindFreeSIPPort`/`FindFreeTLSPort` 的"试绑探测→释放→稍后真绑"改为**探测即绑定**（新增 `BindSIPListenerPair`/`BindFreeTCPPort` 与 `Phone.ServeUDP/ServeTCP`，TLS 走 `tls.NewListener` + `ServeTLS`）——多实例几乎同时启动时不会再出现"各自探测到同一空闲端口、后绑定者失败"（表现为该实例收不到呼入）。验证：`TestOutgoingCancelDuringRinging` 300 次连跑全过（修复前 ~10 次失败）；`TestBindRTPRTCPInvariant` 断言"返回端口已被通配占用"——若实现回退成具体绑定，通配重绑会成功、测试立即抓住。
- **sipgo v1.6.0 的 `WaitAnswer` 取消竞态 nil panic 会杀死整个进程**：挂断取消 ctx 时 WaitAnswer 走 `inviteCancel` → 内部 `s.Do(cancelReq)` 在事务 Terminate 窗口（`ClientTx.Terminate()` **先 `close(done)`、后设 `fsmErr`**）命中 `case <-tx.Done(): return nil, tx.Err()`，`tx.Err()` 尚为 nil → 返回 **(nil, nil)** → inviteCancel 的 `if err != nil` 不成立、直接在 `res.StatusCode` 上 nil 解引用（v1.6.0 内部 bug，上游未见修复）。信令 goroutine 的 panic 会让**整个进程**退出（托盘版是 GUI 子系统、无控制台——表现为"软电话凭空消失"）。`outgoingFlow` 已加 **recover 兜底**：记 Error 日志 + 走既有失败清理；因为 panic 发生在 **CANCEL 已发出之后**（等响应时），通话收尾不受影响。
- **对端挂断（BYE / CANCEL）按 Call-ID 匹配失配会漏删会话**（用户集成测试反应话机不稳定）：`handleRemoteBye` / `handleIncomingCancel` 原是按 Call-ID **精确**匹配，匹配不上就静默返回——会话残留在 `p.calls` 里，卡死单通话限制（新来电 486、新呼出被拒、界面停在通话中、媒体泄漏）。现在这两处走新的 `matchForTeardown`：先精确匹配，失配时**大小写不敏感再匹配**（中间设备规范化 Call-ID 大小写是最常见失配源；单通话限制下该降级安全），再不行只在「p.calls 里唯一会话且它的 Call-ID 取不到（dialog 未记录 INVITE）」时兜底按它处理；**其余失配宁可不删也不误删**（迟到的旧 BYE 与新通话并存时兜底删除会误杀新通话），但打 WARN 日志带双方 Call-ID 便于定位真正失配原因。回归测试：大小写翻转的 BYE 能正常结束通话、失配兜底三态。
- **换账号时旧账号在服务端不会立即过期**（REQ-ACC-011 修订）：`p.Register` 换成另一个账号（服务器/端口/传输/账号/认证 ID 任一变化）时，原来只是取消本端旧注册循环、服务端旧绑定要等自然过期（默认 300 秒）——这段时间打旧号码仍会送到本机。现在**先给旧账号发 expires=0 的 REGISTER** 让它立即过期再注册新账号：顺序为「先停旧循环（防它续注册）→ 在切配置之前发注销（Contact 与注册时一致，服务端按 Contact URI 匹配删除）→ 静默路径（不发注册状态，界面不闪未注册；也不记心跳对端）→ 尽力而为（最多等 2 秒，失败只记日志、不阻断新注册）」；纯改密码/显示名/STUN 等的重注册不打扰服务端（`sameRegistrar` 判定）。附带修复：认证（401）路径此前**不解析服务端返回的 Expires**、一律回退传入值——配置里 Expires=0 时 registerLoop 会算出 0 秒续注册周期而无限重发（测试暴露）；现在认证与直连两条路径统一走 `parseRegisterExpires`。
- **会议 / 咨询面板细节**（用户反馈）：① 咨询在**拨号或振铃阶段**收到 `MSP_EVENT_CONSULT_FINISHED` 时，面板直接回到双方通话、用户看不到任何结果——现在按阶段补一条错误消息条：拨号中 →「咨询失败」、振铃中 →「咨询未接通」（已接通后结束不提示；`consultEndNotice`，回归 `TestConsultEndNotice`）。② 「取消 / 结束咨询」按钮改用**方形挂断图标**（与挂断按钮同款，去掉之前为 `exit-conference` 图标单独加的偏移与缩放）。③ 会议**主持人标记显示在成员行最左边**（原来跟在号码后面）。④ owner 第一行在「结束会议」左侧新增**「添加成员」**按钮（复用主窗体拨号框，`ctiDialMode="conference"`）。⑤ **非视频会议不显示「禁视频」**（本通通话没有协商视频时会控无意义）。⑥ 成员会控按钮（禁视频 / 禁音 / 禁听）改成**与其他话务按钮一致的无底色样式**——不再画彩色底块，图标本身按状态着色（已生效红、否则灰），普通成员的只读图标同样处理；按钮尺寸与图标（36px / 16px）同「移除成员」一致。图标还按状态区分：**未生效用不带斜杠的普通图标**，生效后才换成带斜杠的版本（原来一直用斜杠图标，成员明明没被禁音也显示一个斜杠）。⑦ 会议中正在呼叫的成员行，「取消呼叫」改用**方形挂断图标**（与挂断按钮同款），动作仍是 `removeConferenceMembers`。⑧ 会议成员行的「移除成员」按钮移到**最左边、与主持人标记同列**（首列宽 = 面板按钮尺寸，里面放移除按钮 / 主持人标记 / 空位，三者同列）；非 owner 视角、主持人自己那行、正在呼入的行都留同宽空位——移除按钮与主持人图标对齐，各行号码左边缘也严格对齐（`conferenceLeadLayout`，回归 `TestConferenceLeadLayoutAlignsNumbers`）。⑨ 会议成员列表事件按**整份列表覆盖**重画面板（多余的条目移除、缺少的补齐、状态重设；`CONFERENCE_MEMBER_CHANGED` 单数写法也一并识别——实际下发偶见单数），会议开始时先清空旧成员列表。⑩ **自己那一行也显示会控**（原来主持人自己那行什么都不显示）：owner 视角可点击切换（对自己也生效），普通成员视角只读。⑪ owner 的会议面板第一行在「结束会议」右侧再加**「退出会议」**按钮（只挂断本端通话、不结束会议；成员视角原有的「退出会议」不变）。

- **应用名文案统一为「MTI 软电话」**：主窗口标题（「MTI 软电话 - 本机号码 坐席名」）、托盘悬停提示与托盘菜单标题由 `mti-sip-phone` 改为 `MTI 软电话`；Windows 桌面通知抬头（AUMID 的 DisplayName）同步；Windows 侧按标题前缀查找主窗口的地方（置顶、任务栏图标刷新、前台提升）一并改用新名称。

- **顶栏小按钮细节**（用户反馈）：① 自动接听图案在按钮内**下移 2px**（按钮位置不变）；② 班长模式的**监听按钮略放大**（16→18px）；③ 坐席按钮与「已注册」文字的间距按**视觉间距**补齐（15px，与"座席↔监听"图标间距一致，原来只有 3px 显得太近）；④ 坐席、监听、自动接听三个按钮加上与通信录/设置等顶栏按钮一致的**悬停背景轮廓**（淡填充 + 1px 描边），图标在命中区/悬停背景里**水平垂直居中**（原来误用了只做纵向居中的布局，图标会偏左）。

## [1.0.17] - 2026-10-14

### 新增

- **Windows 可执行文件图标（绿色话机）**（REQ-UI-042）：此前 Windows 产物没有 exe 文件图标，资源管理器里显示 Go 默认图标。现在构建时自动嵌入绿色话机图标（与托盘/窗口同一份 SVG 真相源 `internal/appicon.PhoneGreenSVG()` 光栅化生成，一次性产出 `assets/app-icon.ico` 与 `cmd/sip-phone/rsrc_windows_{amd64,386,arm64}.syso` 提交进仓库）——完整版、无 UI 版、控制台版三个 Windows 产物全部生效，**构建脚本零改动**（Go 自动链接包目录下与目标匹配的 `.syso`）；改图标时用 `tools/gen-appicon` 重新生成 ico、再用 go-winres 重新生成 syso。
- **自安装到固定数据目录**（REQ-UPD-024）：首次从"临时位置"（下载目录等）运行时，程序把自身复制到固定目录、**在桌面与应用菜单生成指向副本的快捷方式「MTI 软电话」**（Windows 经 PowerShell WScript.Shell 生成 .lnk；Linux 写 .desktop——**桌面与应用菜单（`~/.local/share/applications`）各一份**（GNOME 默认关闭桌面图标、本地化桌面目录可能对不上，菜单入口最可靠）；**桌面目录按 `xdg-user-dir`/`~/.config/user-dirs.dirs` 解析**（本地化目录如「~/桌面」也能命中），**ELF 二进制不能内嵌图标**，所以把绿色话机 256px PNG（与托盘同一真相源，光栅化统一提到 `appicon.Rasterize` 供 tray/图标工具/自安装共用）写到程序旁由 .desktop 的 `Icon=` 引用，桌面那份尽力 `gio set metadata::trusted true`（GNOME 43+ 未标记的桌面快捷方式双击不执行、图标显示成通用齿轮）；快捷方式失败只告警不阻断），并从固定目录重启——Windows 是 `%APPDATA%\mti-sip-phone`（与配置目录同一处）、Linux 是 `~/.local/share/mti-sip-phone`，之后自更新替换、开机自启、sip:// 协议注册、更新后重启都围绕这个固定位置，不再受"下载产物被清理"影响。行为细节：已在该目录则直接运行（不复制）；复制/启动失败**回退原地运行**（绝不因此启动失败）；`go run` 的临时 exe 与 `SIP_PHONE_NO_RELOCATE=1` 环境变量跳过（开发逃生口）；复制保留原文件名（完整版与无 UI 版互不覆盖）。检查点在**单实例锁之前**——旧进程不碰锁直接退出、新进程正常拿锁，与自更新重启的竞态口径一致。多实例沙箱各槽位共用同一个 exe 位置，不受影响。
- **SDK `sipUri` / `unregisterOnDisconnect` 参数与 WS 断线自动注销**（REQ-SDK-007 / REQ-WS-020）：① SDK 构造参数 `schemaUri` **改名 `sipUri` 并扩展**——保留"首次连不上时经系统协议拉起软电话（每页一次）"的既有行为，**新增**"每次 WS 连接成功后按 URI 里的注册信息登录 SIP 服务器"（解析口径与 `internal/protocol.ParseSipURL` 一致；与软电话现有注册相同（服务器/端口/传输/账号/认证 ID）时自动忽略，不打断通话）。② 新增构造参数 `unregisterOnDisconnect`（默认 false）与 WS 方法 **`system.setUnregisterOnDisconnect`**：开启后**所有** WS 连接断开并超过 3 秒仍未回来（延迟确认窗口，页面刷新/瞬时断网不触发）→ 自动取消 SIP 注册；**呼叫/振铃/通话中**自动排队、等话务结束再注销；**等待期间重连即取消排队**，并恢复已被自动取消的注册。实现见 `internal/wsapi/unreg.go`（连接计数 + 定时器 + 事件等待的状态机，回归测试钉着四个阶段的完整流程）。

### 修复

- **Linux 应用菜单里出现一个显示为 "mti-sip-phone" 的重复条目**（用户实测）：那是 sip:// 协议的**处理器 .desktop**（`~/.local/share/applications/mti-sip-phone.desktop`，负责「网页里点 sip:// 链接拉起软电话」），它没写 `NoDisplay=true`，于是和自安装写的「MTI 软电话」一起混进应用菜单。现在补上 `NoDisplay`（scheme 处理器按惯例不进菜单；协议注册每次启动重写该文件，下次启动菜单里就只剩「MTI 软电话」）。

## [1.0.16] - 2026-10-09

### 新增

- **sip:// 拉起支持 `updatesite` 参数**（REQ-UPD-022）：`sip://账号@服务器?updatesite=https://…` 拉起时把更新站点写进**本实例**的更新配置（只改 URL，应用 ID/周期/预览版保持原值）；**省略 = 不改配置**；非法站点只记告警日志、不中断账户拉起。写入走 `SetUpdateConfig` 全套（校验/落盘/立即检查/广播同步桌面与网页）。
- **诊断页拉起链接按当前配置自动附加 `updatesite` 参数**（REQ-UPD-022）：生成链接时自动带上配置里的更新站点（无输入框），配置改动（含其它网页保存）后缓存即时刷新；链接里站点经百分号编码，不会破坏参数结构。
- **网页配置页自动更新卡片新增「使用主站点」按钮**（REQ-UPD-023）：把官方站点填进「更新站点」输入框、**不自动保存**（与桌面版一致）。官方地址单一真相源移到 `config.PrimaryUpdateSite`（桌面/网页共用），网页经 `system.state.updatePrimarySite` 下发、不硬编码。
- **容器化交叉编译（信创系统支持）**：新增 `docker/`——一个镜像一次产出**全部 10 个产物**（完整版 + 无 UI 版 × 5 平台），构建基线固定 **Debian 10 / glibc 2.28**：信创两大桌面（统信 UOS 专业版 **2.28**、银河麒麟 V10 **2.31**）的兼容下限都覆盖到了——此前用较新发行版编的 Linux 产物在这两类系统上会报 `GLIBC_2.3x not found`。镜像已发布在 Docker Hub **`wuzm219/msp-builder`**，换台机器 `docker pull` 即用。
  - **怎么用**：`bash docker/build.sh --build-image`（首次构建镜像）→ `--libs`（首次编音视频静态库）→ 无参数（日常出 10 个产物）；不用 Docker Desktop 之外的依赖（WSL 3.0 的 `wslc` 因多个子命令卡死暂不使用，保留 `CONTAINER_CLI` 入口）。**发版脚本 `release.sh` 已统一走容器编译**（正式版把 `SIP_PHONE_RELEASE=1` 透传进容器），不再按平台分 build.bat / build.sh——后者留给本机开发自测。
  - **兼容性口径**：glibc 2.28 基线的产物在 glibc ≥ 2.28 的系统上都能跑（glibc 严格向后兼容）；**第三方静态库（视频/Opus/AEC）必须在同一镜像内编译**（脚本已保证）；Windows 产物不受影响（门槛仍由 Go 工具链决定：Windows 10+）。**龙芯（LoongArch）/ 申威平台仍无产物**——架构层面未支持，与 glibc 无关。
  - **实现要点**（详见 `docker/README.md` 与 Dockerfile 注释）：llvm-mingw 钉在 20230320（最后一个提供 ubuntu-18.04 包、宿主 glibc 要求 2.27 的版本）；archive 源必须带 security（基础镜像的 libc6 是 +deb10u3、main 里只有 +deb10u1）；容器内源码编 nasm 2.16（buster 的 2.14 编不了 FFmpeg 的 x86 汇编，只影响两个 amd64 目标）；老基线下 glfw 只编 X11 后端（`build.sh` 新增 `SIP_PHONE_GLFW_X11=1` 开关，libwayland 1.16 编不了 glfw 3.4 的 Wayland 后端）——与项目「Linux 固定走 X11/Xwayland」的既有口径一致。

### 修复

- **关闭摄像头后设备列表不更新**（桌面/网页同源）：摄像头枚举原来走 mediadevices 管理器**启动时的一次性注册**，而该管理器没有注销 API——已关闭/拔出的设备永远留在列表里，刷新也不消失。现在 dshow 驱动不再注册进管理器：枚举每次实时走 C 侧 `listCamera`、打开按设备路径直接新建驱动实例，应用运行期间关闭/拔出/插入摄像头**刷新设备即生效**。附带收益：设备 ID 从「每次启动随机 UUID」变成 DirectShow 设备路径（稳定标识）。

## [1.0.15] - 2026-10-07

### 新增

- **仅托盘无 UI 版（`mti-sip-phone-no-ui`，构建标签 `noui`）**：同一份代码多出的第二个构建变体，**只启动系统托盘、不创建任何窗口**（没有主界面、通话面板、视频窗口、更新提示窗）。托盘**左键**用系统默认浏览器打开网页控制台，**右键**菜单是「打开控制台」「退出」，图标仍按注册状态变色（已注册绿、其余红）。它**不链接 fyne/GLFW/OpenGL**（托盘直接用 `fyne.io/systray`，图标沿用桌面版同一份 SVG、由 `internal/appicon` 运行期光栅化成 PNG/ICO），产物小一大截（Windows amd64 45.7 MB → 27.9 MB），**Linux 交叉构建也不再需要 X11/Wayland/GL 开发包**；产物随完整版一起出 5 个平台（Windows amd64/386/arm64 + Linux amd64/arm64）。
  - **与完整版是替代关系**（用户确认）：共用同一份配置目录、单实例锁、`sip://` 协议注册与开机自启项，同一台机器上只能运行一个。
  - **自更新不串味**（REQ-UPD-021）：`update.appId` 的默认值按变体取（完整版 `mti-sip-phone` / 无 UI 版 `mti-sip-phone-no-ui`），配置加载时把"另一个变体的默认值"当成未设置改回来——否则无 UI 版会去查完整版的发布物、把**自己**换成完整版 exe。
- **网页控制台（取代原「测试页面」）**：**单页外壳 + hash 路由**（`#/call`、`#/history`、`#/contacts`、`#/settings`、`#/diag`），一条 WS 连接贯穿所有视图（视频订阅是连接级的，整页跳转会丢订阅），顶部常驻**通话条**保证切到任何页面都能接听/挂断/保持。页面：通话（拨号盘 + 按键音 + 通话控制 + 视频订阅）、通话记录、通信录、配置、**诊断**（运行信息 + 事件日志 + sip:// 拉起链接生成器，链接打开即按当前配置自动填好并生成）。它同时是仅托盘无 UI 版的**唯一界面**，并覆盖桌面版的全部功能（原先"网页不做通信录增删改查 / 不查询设备"的口径一并作废）。
  - 网页资源由白名单静态处理器提供（`/`、`/index.html`、`/history`、`/contacts`、`/settings`、`/diag`、`/test.html` 都返回控制台；`/css/app.css`、`/js/*.js` 单独列白名单），一律 `no-cache`；未知路径 404。
  - 前端安全：服务端数据一律 `textContent` 渲染（通信录名称/备注来自导入文件），诊断日志默认不回显 `video.frame`；对端展示（通话条 / 通话卡片 / 通话记录）统一成「**号码 名称 - 备注★**」，与桌面口径一致。
  - **顶栏全局更新徽章**：有可用更新 / 正在下载 / 待安装 / 失败时在「已连接 · 已注册」后面亮起（`发现新版本 vX` / `更新下载中 N%` / `更新待安装` / `更新失败`），点它跳到「通话」页并把更新卡片滚进视野——无 UI 版没有桌面提示窗，这是它唯一的更新提示入口。
- **网页端补齐桌面版的功能缺口（新增 26 个 WS 方法）**：
  - **通话记录**（REQ-WS-017）：`calllog.query` / `calllog.delete` / `calllog.clear`，时间戳统一毫秒整数（0 = 未发生），命中通信录时记录带 `contact:{name,note,starred}`，删除后广播 `calllog.changed`。
  - **通信录增删改查**（REQ-WS-018）：`contact.list`（带 `query` 时**在服务端按拼音过滤**）/ `contact.save` / `contact.delete` / `contact.setStarred` / `contact.clear`，写入统一走 `mutateContacts` 并广播 `contact.changed`。
  - **配置读写**（REQ-WS-019）：`system.getConfig`（脱敏快照，**密码只回 passwordSet**）、`system.setAccount` / `setAudioConfig` / `setVideoConfig` / `setLogConfig` / `setWSPort` / `setAutoStart`（只改传入字段，校验失败不落盘，成功即落盘并广播 `system.config`），另有 `system.restart`、`call.setVideoPreview`。`core.App` 新增 `AudioControl` 注入接口（core 不能 import `internal/audio`，会循环）。
  - **设备枚举**（REQ-WS-016）：`audio.devices` / `video.devices`，**异步回包 + 单飞 + 2 秒缓存 + 3 秒超时**（音频服务不可达时 miniaudio 能阻塞十几秒）；新增 `audio.ready` 事件（`Manager.OnReady` 改成支持多个回调）。
  - **更新可由网页触发**（REQ-UPD-020，修订 REQ-UPD-009）：`update.start` / `update.cancel` / `update.apply` / `update.status` 与 `system.checkUpdateNow`；仍然**必须由用户点击**、不做静默安装，安装与重启都是**先回响应再异步执行**。
  - `system.getState` 增加 `recentDial`（拨号下拉）、`noUI`（是否仅托盘无 UI 版）、`instance`（实例槽位）。
- **拨号框边打边查通信录**（REQ-UI-024 修订，桌面与网页一致）：输入时弹出建议——**通信录匹配**（拼音/音头/汉字/号码/备注，与通信录页的搜索**同一套匹配器**，拼音逻辑从 `internal/ui` 提到 `internal/contactsearch` 共用）+ **最近呼叫**里包含该串的；点某一项把**号码**填进输入框（不直接拨号）。桌面版输入框**去掉原来的下拉箭头**，**获得焦点且内容为空时直接列出最近呼叫**，**失焦收起**；网页端原来的「最近」下拉与「快搜」输入框并入这一个输入框。
- **更新站点上的第二个应用**：`mti-sip-phone-no-ui` 作为**独立应用**发布（`release/release-no-ui.conf` + `release/app-no-ui.json`，独立上传令牌），`scripts/release.sh` 新增 `--no-ui` 切换配置、元数据与产物清单。

### 修复

- **「诊断」页曾经整页空白**：加这一页时漏掉了配置页的 `</section>`，导致它被嵌在（隐藏的）配置页里面——DOM 里查得到、画面上什么都没有。已补回闭合标签并核对了五个 section 的平级关系。
- **常驻通话条在没通话时也显示成一条黄色空条**（前面一个「—」）：`hidden` 属性只是 UA 的 `display:none`，被作者样式里的 `.call-bar{display:flex}` 盖掉了；补 `[hidden]{display:none !important}` 兜底（no-ui 下两个勾选项"该藏没藏"是同一个坑）。
- **拨号建议下拉的两个真机缺陷**：① 原来弹 `PopUpMenu`，弹出后再打字就无效（得先关掉下拉）——根因是 Fyne 把顶层 overlay 当成**独立的键盘焦点域**（画布有 overlay 时 `focusManager()` 返回 overlay 自己的，`FocusManager.Focus` 又只认自己那棵树里的对象），换成不碰焦点的 `widget.PopUp` 也一样；现在下拉是**窗口内容 Stack 里的顶层图层**，输入框始终保有焦点。② 修好显示后**点选不中**——Fyne 的鼠标是 press/release 两次独立命中测试（按下先 `Unfocus`、抬起再重新找对象、只有同一个对象才派发 `Tapped`），失焦立刻隐藏会让抬起那一下命中到下面的页面；改为**失焦延迟 250ms 收起**，点中了哪一行则取消该定时器。两条都有回归测试钉着。
- **本地回显白做转换**：媒体引擎的回显默认开着，而仅托盘无 UI 版（以及 `-headless`）根本没有回显窗口，采集循环每帧白做一次 YUV→RGBA（640×480×25fps ≈ 30MB/s）。现在电话层按"有没有注册桌面视频出口"决定回显默认值，网页订阅本地画面不受影响。
- **网页刷新时右下角刷出一屏"未连接软电话"**：各视图的首次加载在 WS 握手完成前就发请求，必然失败；现在视图加载先看连接状态（连上后自动重跑），请求失败也区分"离线类"（只写控制台，SDK 会自动重连）与真实错误。
- **网页通信录编辑窗的「设为常用」勾选框跑到中间**：`.field > input{width:100%}` 把 checkbox 也撑满整行；已让弹窗里的勾选项成为一个整体。
- **通话记录页显示两个号码**：原来把"显示名 + 号码"直接拼起来，而 SIP 显示名常常就是号码本身；现在按桌面口径渲染「号码 名称 - 备注★」（备注直接显示、不再悬停），方向列改用**图标**（来电/呼出箭头 + 视频通话叠一个摄像头图标，未接标红），时长拆成**「振铃/拨号」与「通话时长」两列**。
- **通话页布局**：删掉通话控制卡片里重复的拨号盘（拨号卡片那个未接通填号、接通后直接发 DTMF）；远端/本地画面改为**并排两列**（单路最高 200px），整页不再出现竖向滚动条；每路画面自己的旋转/镜像按钮移进该路表头与之对齐。

### 变更

- **网页接入可选「允许远程连接」**（REQ-WS-005 修订，`ws.allowRemote`，**默认关**）：勾选后 WS 服务监听 **0.0.0.0**、不再拒绝非回环来源，其他 IP 也能用浏览器打开控制台（拨号/通话记录/通信录/配置/重启全部开放）。⚠️ 接口**没有鉴权**，放开等于把控制权交给整个网段——桌面与网页配置页都写明风险（桌面只留「只在受信任网络使用」的勾选说明，网页勾选时二次确认），启动时打 WARN 日志；绑定地址在启动时确定，**改完需重启**。`system.setWSPort` 相应扩展为可只改端口或只改开关。
- **沙箱实例的 WS 端口起点改为 8765 + 10×槽位**（REQ-NFR-007）：`-instance N` 首次启动从 **8765+10N** 起找空闲端口（槽位 1 → 8775、槽位 2 → 8785…），主实例仍是 8765（此前所有槽位都从 8775 起步、靠"+1"错开，端口与槽位对不上号）；已有实例配置里保存的端口不受影响。
- **术语统一**：网页那套界面统一叫「**控制台**」（托盘菜单、页面标题与文案、文档与更新站点描述里的「控制页 / 控制端」全部改名）。
- **去掉网页配置页的「输出到控制台」勾选项**：桌面配置页本来就没有这一项，而托盘/no-ui 版是 GUI 子系统构建、根本没有控制台（勾了什么都看不到）。配置项 `log.console` 保留（控制台版/终端里跑时有用，命令行与 `system.setLogConfig` 仍可设），只是两边的界面都不再提供。
- **文档**：`docs/requirements.md` 新增 `REQ-UI-041`、`REQ-WEB-001~004`、`REQ-WS-016~019`、`REQ-UPD-020/021`，并修订 `REQ-WS-005` / `REQ-WS-008` / `REQ-WS-015` / `REQ-UPD-009` / `REQ-UI-024` / `REQ-NFR-007`；`docs/ws-protocol.md`、`docs/sdk-guide.md`、`docs/user-manual.md`（新增「仅托盘无 UI 版」「让别的电脑也能打开控制台」两节）、`docs/design.md`、`docs/auto-update.md`、`README.md`、`CLAUDE.md` 同步新方法、新事件、新变体与第二个应用的发布流程。

## [1.0.14] - 2026-10-07

### 修复

- **Windows 32 位（386）版本恢复摄像头采集**：此前 386 产物没有任何采集后端（设置页摄像头选择置灰、视频通话只能看对方），根子是上游 mediadevices（v0.10.0）的 DirectShow 桥 `camera_windows.hpp` 里 `SampleCB` / `BufferCB` 两个回调漏写 `STDMETHODCALLTYPE`，与 mingw `qedit.h` 里 `ISampleGrabberCB` 的 `__stdcall` 在 i686 上是真实的调用约定冲突、编不过（x86_64 上该属性被忽略，所以 amd64 一直无事）。现在把这份驱动**复制进仓库**（`internal/video/dshow`，MIT，来源与改动见文件头注释）并补上调用约定标记，**amd64 与 386 共用同一份实现**（不再用上游的 camera 包）。实测：两个架构都能枚举设备、打开并采到帧（386 在 64 位 Windows 上经 WOW64 运行，用的是系统自带的 32 位 DirectShow 组件）。
- **Windows 产物改为静态链接，不再依赖 MinGW 运行时 DLL**：amd64 托盘版与控制台版此前动态依赖 `libwinpthread-1.dll` / `libstdc++-6.dll` / `libgcc_s_seh-1.dll` —— 它们是 **MinGW 工具链的运行时、Windows 并不自带**，而发布包（tar.gz 里只有 exe）不附带这些 DLL，在没装 MinGW 的机器上会直接报"缺少 libwinpthread-1.dll"启动失败；386 目标因历史原因一直是静态链接，本次 amd64 与控制台版一并加上 `-extldflags -static`。现在两个 Windows 产物的导入表只剩 `ole32` / `quartz` 这类**系统组件**（DirectShow 用），单文件可直接分发。
- **两处 Linux 侧构建缺陷**：① `internal/ui/topmost_linux.go` 用了 `sync` / `sync/atomic` 却没写进 import——**Linux 专有文件在 Windows 上根本不参与编译**（`build.bat`、`go test`、`go vet` 全都看不见），到 Linux 上跑 `build.sh` 才报 `undefined: sync`；② Linux 交叉（amd64 ⇄ arm64）未显式给 `CXX`，AEC 的 C++ 桥（`apm_bridge.cc`）进来后链接驱动落到**宿主**的 `/usr/bin/g++`，拿 aarch64 目标文件去链接、报 `Relocations in generic ELF (EM: 183)`——现在交叉目标显式使用目标架构的 g++，缺 `g++-<triple>` 包时**跳过该目标并打印安装提示**（只有交叉 gcc 是不够的：C++ 源要 cc1plus、`-lstdc++` 要目标架构的 libstdc++）。

### 新增

- **音频预处理：回声消除 / 降噪 / 自动增益**（REQ-MEDIA-007，webrtc-audio-processing 1.3）：外放时的自激回声、风扇/键盘底噪、说话远近导致的音量忽大忽小，现在由 WebRTC 的音频处理模块（AEC3 + NS + AGC）在本地处理。**默认开回声消除与降噪，自动增益默认关**（它会与通话面板的手动麦克风增益较劲）；设置页「音频设备」卡片里新增「音频预处理」小节三个勾选框，**改动立即保存、从下一通电话生效**（音频流在每通电话开始时重建）。
  - **处理点在全双工音频回调里**：参考信号就是"本拍即将播放的样本"，采集样本按 AEC → NS → AGC 处理后才进编码；APM 只吃定长 10ms 帧，回调的任意帧长（WASAPI 共享模式不保证）由一层分帧缓冲累积/切帧，采集路径因此多出不到 10ms 的延迟。8 kHz（G.711/Opus）与 16 kHz（G.722）都支持。
  - **构建**：静态库（abseil + webrtc-audio-processing）由 `scripts/build-audio-libs.sh` 的 APM 目标生成到 `third_party/audio/<target>/{include,lib}`，构建脚本探测到 `libwebrtc-audio-processing-1.a` 就自动加 `apm` 标签；**缺库时该构建降级为无预处理**（三个开关置灰），与 Opus 各自独立降级。Windows（WinLibs，含 386）与 Linux（含 arm64 交叉）都已验证；补丁随仓库放在 `scripts/patches/webrtc-audio-processing-1.3/`（新增一条：glibc 2.39 起没有 `<sys/prctl.h>`，Linux 上必须改）。
  - **实测体积**：exe 增加约 0.9 MB（amd64 45.4→46.3 MB、386 40.6→41.5 MB）；AEC 效果回归测试 `go test -tags apm ./internal/audio/`（合成延迟衰减回声，抑制 ≈27 dB）。
  - 已知限制：`mobile_mode`（移动端省 CPU 档）与 NS/AGC 同时开启会让 APM 的 ProcessStream 返回 -11（实测 8k/16k 均如此），因此使用默认的桌面档 AEC3。
- **窗口位置记忆 + 视频窗口锁定**（REQ-UI-040 / REQ-VIDEO-015）：
  - **通话面板**：无边框窗口没有标题栏可拖，改在**顶部居中画一条 50% 宽、2px 高的细线**当“抓手”（距顶约 4px，深色主题深灰、浅色主题浅灰）——**鼠标移上去变成四向箭头的“可移动”光标**（Fyne 标准光标没有 move 这种，用自绘位图：黑色主体 + 1px 白描边，深浅背景都看得清），按住这条线拖动即可移动窗口。抓取区域只有那一小条（比细线略高、容易抓，且不伸进第一行内容），不抢面板按钮的点击与名称备注的悬停。**面板不带锁**（随时可拖）。
  - **视频窗口**：工具栏新增**锁按钮**（默认**锁上**，锁定时窗口既不能移动也不能用边框改大小：标题栏拖动、双击最大化、系统菜单的移动/大小、Win+方向键与贴靠全部无效，**关闭与最小化照常**；锁的状态不持久化，会话内记忆）。
  - **两个窗口的位置都记住**：用户拖动结束就落盘（`ui.callPanel` / `ui.videoWindow` 的 `moved`+`x`/`y`，各实例各自一份），下次弹出、退出重启后都固定在同一位置；**没移动过时仍走原来的默认摆放**（通话面板吸附工作区右下角、多实例按槽位整列左移；视频窗口贴通话面板正上方、右边缘对齐，尺寸变化时照样重新对齐）。视频窗口的**「置顶」**与**本地回显的旋转/水平镜像**一并持久化，**远端画面的旋转/镜像不记录**（每次启动归零）；桌面按钮与网页 `video.setTransform` 任一侧改动都会写进配置。
  - **拖动实现**（两处共用）：按**屏幕绝对光标**做锚点（Windows `GetCursorPos` + `SetWindowPos`；Linux/X11 `XQueryPointer` + `XMoveWindow`），不用 Fyne `DragEvent` 的窗口相对坐标——窗口跟着光标走时相对坐标会自反馈，表现为抖动或跟不动。拖动会话跑在自己的 goroutine 里（~60Hz 跟随），UI 线程只负责开始/结束。
  - **锁定实现**（只针对视频窗口）：它是带系统标题栏的普通窗口，Windows 侧装**窗口过程钩子**拦 `WM_NCLBUTTONDOWN`(HTCAPTION/边框)、`WM_SYSCOMMAND`(SC_MOVE/SC_SIZE/SC_MAXIMIZE) 与 `WM_WINDOWPOSCHANGING`（兜住 Win+方向键/贴靠这类外壳直接 `SetWindowPos` 的路径，程序化移动与全屏切换经放行标志不受影响），并用 `WM_EXITSIZEMOVE` 记录用户拖动结束；只拦消息、**不碰窗口样式**（改了会让 GLFW 的边框缓存失准）。Linux/X11 客户端拦不住 WM 的移动，用"显示期间轮询位置、被挪走就挪回"兜底，尺寸改用 `WM_NORMAL_HINTS` min=max 让 WM 直接拒绝缩放。
  - **越界回退**：保存的位置若已不在当前屏幕范围内（拔掉副屏、改分辨率），摆放时回退默认位置并记一条日志，**但不清除配置值**（屏幕回来还能用）。坐标一律物理像素、屏幕绝对、窗口矩形左上角；`config.save` 补了互斥锁（临时文件名固定，并发保存会写出半截配置）。
  - 内部：`internal/ui/ui_window_pos.go`（纯逻辑：可达性判定与拖动位移，带单测）、`internal/ui/topmost_*.go` 三个平台实现、`config.UIConfig` 新增字段与 `config.NormalizeVideoRotation`。
- **SIP 域名可配**（REQ-ACC-022）：SIP 账户新增「域名」输入框（可选，留空 = 用 SIP 服务器地址，**历史行为一字不变**）。配了域名后，**URI 里的域**用它——REGISTER 的 R-URI/From/To、呼出 INVITE 的 From 与 To/R-URI（拨纯号码时补成 `sip:号码@域名`），这样 AOR 与身份就是服务端认的域名；**报文仍发往 Server:Port**（域名多半只在服务器内部能解析，本机不一定解析得了）：注册用 sipgo 的 `SetDestination`，呼叫用首条 `Route: <sip:服务器:端口;lr>`（与既有的出站代理同一手法，**代理配置优先**）。Contact 不受影响（永远是对端如何找到本机）。域名可写 `sip:`/`sips:` 前缀、`user@`、端口，规范化时自动去掉。
- **声明 IP：Via/Contact/SDP 三处统一可配**（REQ-ACC-023）：SIP 账户新增「声明 IP」——**可输入的下拉**（与主窗体拨号框同款）：**留空 = 自动**（默认，与从前一致：STUN 公网地址 → 本机出口 IP → hostname），下拉候选是本机可用网卡 IPv4，也可直接手填（必须是 IP 字面量——SDP 的 `c=` 行写域名非法，填错当场提示而不是静默退回自动）。候选**过滤掉回环、链路本地与虚拟网卡**（Hyper-V/WSL/VMware/VirtualBox/Docker/VPN/TAP，按 MAC 厂商前缀与网卡名双重判定），并**按可达性排序：公网地址在前、RFC1918 内网地址在后**（本机实测候选从一堆虚拟地址收敛到真实网卡那一个）。指定后该地址**优先于 STUN**，并同时出现在 **Contact 主机、SDP 的 `c=`/`o=`、Via 的 sent-by** 三处。Via 是通过反射写 sipgo 的 `Client.host`（与 rport 动态切换同一手法，版本不匹配时只告警、重启后仍生效），**保存账户即生效**；它只改"对外声明"，连接依然绑本机真实网卡。
- **媒体加密：SRTP（SDES，RFC 4568）**（REQ-MEDIA-009）：此前只有信令走 TLS，**通话内容仍是明文 RTP**（抓包能直接听）；现在音频、视频、DTMF 按键音全部加密，并带完整性校验与防重放（改包/重放的包直接丢弃）。配置页新增「媒体加密」四档（`account.srtp`）：
  - **关闭**：完全不协商 SRTP；对端单方面要求加密也不接受（本次通话明文，由对端决定是否拒接）。
  - **支持**（**默认**）：本端发起呼叫时**不声明** SRTP；来电的对端要求加密（offer 带 `a=crypto`）时**配合加密**并回带 crypto 的 answer。
  - **建议**：本端呼叫时在 offer 里声明 SRTP，对端不接受则按明文继续（界面日志会明确写"未加密"）。
  - **必要**：必须加密——呼入的 offer 没有可接受的 crypto 回 **488 Not Acceptable Here**；呼出时对端 answer 没接受则本次呼叫判失败（挂断并显示原因）；对端在重协商（re-INVITE）里去掉了 crypto 同样回 488。
  - **界面**：配置页新增「媒体加密」下拉（关闭/支持（默认）/建议/必要），下面按**一档一行**列出各档含义（主窗体固定 320 DIP 宽，长句会被折到难读的位置）；原先那条长提示「SIPS 只加密信令；通话媒体（声音/视频）需另设「媒体加密」」精简为「**SIPS 只加密信令**」。两处提示都有回归测试卡着行宽。
  - **协议**：密钥随 SDP 的 `a=crypto:… inline:<base64(主密钥‖盐)>` 交换（SDES），**密钥方向按 RFC 4568 §6.1**——SDP 里的密钥是"写这条 SDP 的一方加密它自己发出的媒体"用的，所以收发各一把、单向；answer **复用对端选中那条的 tag 与套件**，只换自己的密钥。套件支持六种（`AES_CM_128_HMAC_SHA1_80` 优先，另有 AES-256-CM、SHA1_32 与 `AEAD_AES_128/256_GCM`）；对端不认识的套件名/带 lifetime/MKI/会话参数的行一律跳过，不认识的 offer 就退回明文。
  - **实现要点**：每个 m= 行（音频、视频各一段）用**独立的密钥**；密钥用 crypto/rand、**每通电话新生成**、通话内重协商复用；接收侧开**重放保护**（窗口 64，pion 的默认是**关**的，必须显式打开）；打洞包、早期媒体保活、DTMF、RTCP（SRTCP）全部走加密（漏一个明文包对端会判流失败）；换流（183→200 换 SSRC/序号跳变）时重置接收侧序号/ROC 状态，否则表现为"接通后解密全失败、没声音"。
  - **密钥不进日志**：SIP 信令记录（REQ-LOG-008）会把整份 SDP 落盘，`a=crypto` 的密钥已在脱敏规则里抹掉（套件名保留，排查看得出用的什么算法）。
  - ⚠️ **SDES 的密钥在 SDP 里明文传输**，安全性依赖信令加密——配置页提示「建议与 TLS 传输同时使用」。**只加密媒体，不改变 SIP 信令的加密方式**（那是 REQ-ACC-006 的 TLS）。不做 DTLS-SRTP、ZRTP、客户端证书。
  - 内部：新增 `internal/media/srtp.go`（套件表、密钥生成与 `a=crypto` 解析、收发上下文、四档策略的状态机）；`internal/media/srtp_test.go`（套件往返、篡改/重放拒绝、offer/answer 协商、四档策略语义、真实 UDP 收发）；`config.NormalizeSRTP`；`logx.MaskSecrets` 新增 crypto 密钥脱敏（含回归测试）。依赖新增 `github.com/pion/srtp/v3`（纯 Go，无 CGO）。
- **崩溃现场记录**（REQ-LOG-009，排查能力）：多实例联调时沙箱实例「用着用着就没了」，而**日志停在半截、一句线索都没有**。根因是**这类退出根本不产生任何日志**——托盘版是 GUI 子系统构建（`-H=windowsgui`，**没有控制台**），未捕获的 panic、运行期致命错误（并发 map 读写、栈溢出）、cgo 里的非法内存访问，都由 Go 运行期打印到 **stderr** 后 `exit(2)`：GUI 子系统下 stderr 无处可去，消息直接蒸发；Windows 事件日志也不会有记录（进程是"**自己**退出"的，不是被异常终止，所以既没有 Application Error、也没有 WER 报告）。现在：
  - `debug.SetCrashOutput`（Go 1.24+）把运行期崩溃输出**另写一份**到日志目录的 `crash.log`（含全部 goroutine 栈与「fatal error: unexpected signal」这类 cgo 崩溃现场）；**下次启动**自动归档为 `crash-<时间>.log`（最多留 9 份）并在主日志里打一条 WARN「上次运行的崩溃现场已归档（上次是异常退出）」——**从此"崩溃"与"被杀掉/正常退出"可以区分**；`crash.log` 里只有启动标题行（上次正常退出）时直接清空重来，不会误报。标题行写明版本、平台、pid、实例槽位与启动参数，一个文件里躺着多次运行也分得清。
  - **stderr 没有去处时**（GUI 子系统从资源管理器/快捷方式启动，那正是托盘版的常见启动方式）把 `os.Stderr` 接到日志目录的 `stderr.log`，收下 Fyne/GLFW 这类直接往标准错误写的信息；**有控制台、或被显式重定向**（`> 日志 2>&1`）时一律不动，免得把用户自己要的输出抢走。
  - 需求文档同步：REQ-LOG-009。
  - **SIP 信令记录不受日志等级影响**（澄清，无行为变更）：勾选「记录 SIP 信令」后，信令走的是**独立的输出目标**（`logx.traceOut`，直接写日志文件），根本不经过 slog 的等级过滤——把日志级别调到 warn/error 也照样逐条落盘。原先那条开启提示写着"（DEBUG 级）"，容易被误读成受级别控制，文案已改为「不受日志级别影响」，并补了一条回归测试（等级=error 时信令仍必须落盘）钉住该行为。
- **新增 Windows on ARM64 构建目标**（`dist/sip-phone-windows-arm64.exe`，Win/Linux 两个构建脚本都支持）：arm64 版 Windows 电脑（Surface Pro X、骁龙本）此前**没有产物**——GCC 系的 mingw-w64 根本没有 `aarch64-w64-mingw32` 目标（`gcc-mingw-w64-*`、WinLibs、MSYS2 的 mingw64/mingw32 都出不了它，MSYS2 的 clangarm64 是"ARM64 机器上的本机环境"，不是 x64 上的交叉），必须换成 **llvm-mingw**（clang + lld + mingw-w64 sysroot，解压即用，官方提供 Windows 与 Linux 宿主的包）。两个脚本的探测顺序是 PATH → `LLVM_MINGW=<解压目录>` → 默认位置，**探测不到就跳过该目标并打印安装步骤**（不影响其余产物）。产物导入表只有系统 DLL（UCRT，无 libwinpthread/libgcc），单文件可直接分发。
  - **该目标目前是基础版**：视频、Opus、音频预处理（AEC/NS/AGC）三套静态库都还没有 arm64 版本，需要另为 `aarch64-w64-mingw32` 编库（OpenH264 与 abseil 是 C++，而 llvm-mingw 带的是 libc++ 而非 libstdc++，构建脚本的 `-lstdc++` 得跟着改），因此脚本刻意**不对该目标做库探测**，固定构建为 G.711/G.722 + 音频设备 + 界面 + 信令 + WS 的基础版。
  - 已知限制：**尚未在真机（Windows on ARM64 设备）上运行验证**，目前只完成编译验证与 PE 头检查（ARM64 / GUI 子系统）；x64 Windows 跑不了 ARM64 程序，验收需要对应设备。
- **文档交付：用户使用手册与 SDK 开发指南**：新增 `docs/user-manual.md`（面向最终用户：安装与运行环境、配置页逐项说明、拨号/来电/通话/视频/通话记录/通信录/托盘、`sip://` 拉起与多开、自动更新、故障排查与数据文件位置）与 `docs/sdk-guide.md`（面向前端集成者：5 分钟上手、全部 API 与事件参考、视频订阅专章、**16 条避坑清单**、调试建议）。两份文档逐项对照实现核校（改掉若干"想当然"的描述：托盘是**单击**唤出、界面上并无视频分辨率设置项、SIP 账户需点「保存并注册」等）。同时按**五种产物**（Windows x64/x86/ARM64、Linux x64/arm64）统一梳理了各文档的平台清单与能力矩阵（含"无视频能力的构建里视频入口全部隐藏、配置页视频设备区置灰"的口径），把 Go 版本要求更正为 **1.24+**、补充「**Windows 10 及以上**」的运行要求（Go 1.22 起官方不再支持 Win7/8），并修正 REQ-UI-002（托盘交互按实现为单击）。

## [1.0.13] - 2026-10-06

### 修复

- **TCP/TLS 来电接听后约 0.5 秒自动失败**（REQ-ACC-006）：MicroSIP 呼入、app 点接听，通话建立不起来（日志 `发送 200 OK 失败: transaction terminated`）。根因在 sipgo 的事务生命周期：`DialogServerSession.RespondSDP` 发完 200 后要按 RFC 6026 **阻塞等 ACK**，而 sipgo 的 `Server.TerminateGracefully` 对**可靠传输（TCP/TLS）会在 handler 返回时立即终止 INVITE 事务**（UDP 因为不可靠，会把事务留到重传定时器）——我们的 `handleIncoming` 在 `dlg.StateRead()`（接听时状态变 Established）一响就返回了，事务随之终止、等 ACK 被打断成 "transaction terminated"，接听流程把这通刚接通的电话当失败清理掉。现在 handler 收到非 Ended 的状态后**继续等本通真正结束**（`<-ctx.Done()`）。这条 UDP 侧测不出来（事务被保留，纯属侥幸），已加 `TestIncomingOverTLS`（TLS 来电 → 自动接听 → 建立成功 → 本端挂断的 BYE 送到对端）钉住。
- **TCP/TLS 下振铃中挂断（CANCEL）发不出去**（REQ-ACC-006）：app 呼叫对端、对方还在振铃时挂断，本端挂了但**对端一直振铃**。CANCEL 必须按 **INVITE 的路径**发（RFC 3261 §9.1：目的地址、Call-ID、CSeq、Via branch 都与 INVITE 一致），而 sipgo 构造 CANCEL 时走的是 dialog 的 `buildReq`，会把**响应里的 Record-Route** 当成 Route 头塞进去——CANCEL 因此被发到 Record-Route 指向的地址（真机那条腿连不上），静默丢失。现在挂断振铃中的呼出时，先把**临时响应**（1xx）上的 Record-Route 摘干净（2xx 的不能摘，那是补发 ACK/BYE 要用的路由集），CANCEL 即回到 INVITE 的原始目的地。⚠️ 摘必须用 `stripRecordRoutes`：sipgo 的 `RemoveHeader` **大小写敏感、且只删第一条**，真机的响应上可能有多条。回归测试 `TestCancelWhileRingingOverTLS`。
- **接通许久后对端挂断、本端不知道（TCP/TLS）**（REQ-ACC-006 真机缺陷）：服务端日志 `ERROR:core:tcp_connect_blocking_timeout ... [server=10.168.5.10:59436] (111) Connection refused` / `proto_tls: connect failed` / `tm:msg_send ... failed`——服务端要送对端的 BYE 时**连接表里已经没有那条连接**，只能按 Contact 地址重新拨号，而 NAT 后面的地址外面拨不进来。原因是**连接被服务端的空闲超时关掉了**：OpenSIPS 的 `tcp_connection_lifetime` 是分钟级，而我们的续注册周期是 `0.7×expires`（默认 210 秒），两次注册之间连接就凉了（所以"刚接通一切正常、时间一长就不行"）。现在**客户端做信令保活**：TCP/TLS 传输下每 30 秒在注册用的那条连接上发一个 **CRLF**（RFC 5626 §3.5.1 的 keepalive，服务端回 CRLF/"pong"，非 SIP 载荷、读钩子直接忽略），刷新服务端的空闲计时。测试：`TestConnKeepaliveSendsCRLF`（真实 sipgo 服务端 + 连接字节 tee，断言 CRLF 确实发出）、`TestKeepaliveLoopSkipsUDP`。**这一步是客户端该做的标准动作，不依赖服务端配置**。
- **排查便利：ACK/BYE 发送失败的日志由 Debug 提到 Warn**：这两条以前在 info 级日志下**完全看不见**（真机上表现为"接通 30 秒后被对端挂断，本端日志毫无线索"——实际是 ACK 发不出去）。现在失败会直接打出目标地址与错误。
- **TCP 的 Contact 补上 `transport=tcp`**：Contact 是"对端以后怎么找到我"，而 `sip:` URI 不带 `transport` 参数时**默认按 UDP**（呼出 INVITE 的 R-URI 一直带着它，Contact 漏了）。UDP 仍然不加参数（缺省即 UDP）。
- **服务端心跳检测整套只在 UDP 传输生效**（REQ-ACC-020 修订）：连接型传输（TCP/TLS）另有保活机制可用，所以 ①注册报文的 User-Agent **只有 udp 才带 `optchk` 标记**；②sip 层的**心跳计数与间隔学习**（传输层读钩子）与**超时重新注册**（netwatch 的 2.5 周期判定）**在 tcp/tls 下一并停用**（此前只要开关打开就无条件计数）；③设置页「启用服务端心跳检测**（仅 UDP）**」在 tcp/tls 下**置灰不可选**，切回 udp 自动恢复，已选值保留不被改写。测试：`TestUserAgentOptChkOnlyUDP`、`TestHeartbeatOnlyForUDP`。
- 排查便利：呼叫失败原因只在拿到**最终响应**（≥200）时用它（曾把"CANCEL 发送失败"显示成 `183 Session Progress`，底层错误被吞掉）；信令日志的方向行带上**传输与本地/远端地址**（如 `[SIP 发出 TLS 192.168.3.15:53605 → 10.168.4.40:15061]`），排查"这条消息到底发到哪条腿/哪个连接上"时非常关键。

> 说明：TLS/NAT 真机联调期间试过两批**客户端侧绕过**——注册 Contact 加 `;ob` / RFC 5626 outbound（`reg-id`/`+sip.instance`/`Supported: outbound`），以及"in-dialog 请求按 `r2=on` 那条 Record-Route 选腿并改写传输"。真机抓包定位到根因是 **OpenSIPS 路由配置**把 in-dialog 请求（ACK/BYE）按 **UDP** 发往我们连接的端口（抓包里是 `UDP 15060 → 10.168.5.10:52369` + 紧跟 `ICMP Port unreachable`），属服务端配置问题，**客户端的这些变通已全部回滚**，不留在代码里。

### 新增

- **SIP 信令 TLS(SIPS) 传输**（REQ-ACC-006）：传输方式新增 **`tls`**——配置页「传输」下拉、配置文件 `account.transport`、协议链接三条入口（`sip://…?transport=tls`、以及新增的 **`sips://账号:密码@服务器:端口`** 前缀，sips:// 即 TLS、端口缺省 **5061**；前缀比查询参数优先）。注册、呼出（INVITE/ACK/BYE）、呼入信令全走 TLS：R-URI 与 Contact 用 **`sips:`**、Contact 带 **`transport=tls`**、Via 协议为 **TLS**。
  - **本端 TLS 监听**（接收 TLS 呼入）：传输为 tls 时**按需启动**（不用 TLS 的构建不生成证书、不多占端口），端口在 40000–50000 里另找一个空闲 TCP 端口（TLS 跑在 TCP 上，不能与明文 TCP 监听共用端口）；证书是**自动生成的自签名证书**（RSA 2048，SAN 含本机各网卡 IP、hostname 与 localhost，有效期 10 年），持久化在实例数据目录 `tls/local-{cert,key}.pem`（重启不变、多实例各一份，服务端要固定信任也有东西可加）。
  - **服务器证书默认不校验**（内网/自建服务器多半自签，开箱即用优先）：配置页新增「校验服务器证书」勾选（仅传输=tls 时显示），勾上后按**系统根证书**校验证书链与主机名；核验开关**即时生效**（不重建 UA）。**不做客户端证书（mTLS）**。
  - **只加密信令**：媒体仍是明文 RTP（SRTP 是另一个需求 REQ-MEDIA-009），配置页在 TLS 选项旁写明这句，避免误解。
  - `sip://` 与 `sips://` 都注册为系统协议（Windows 两个方案各一个 ProgID + UrlAssociations，Linux .desktop 两个 MimeType + mimeapps 两条默认项）；协议拉起、单实例参数转发都识别 `sips://`。
  - 测试页「URI 拉起软电话」新增「传输」下拉（udp/tcp/tls）：tls 生成 `sips://…:5061` 链接，tcp 生成 `sip://…?transport=tcp`。
  - 内部：新增 `internal/sip/tls.go`（客户端 TLS 配置 + 自签证书生成/复用）、`internal/sip/keepalive.go`（TCP/TLS 信令保活，每 30 秒 CRLF）、`internal/sip/dialogroute.go`（CANCEL 的 Record-Route 清理，见「修复」）；`Account.TLSVerify`（零值 = 不校验）；`config.NormalizeTransport`/`DefaultPortForTransport` 统一传输判定；dialog 缓存（dcc/dsc）按传输方式与 Contact 重建（sipgo 要求每种传输一个实例；**通话进行中不换**，避免 in-dialog 请求匹配不上）；网络变化后的"丢弃旧长连接"（REQ-CALL-009）从只认 tcp 扩展到 tls。
  - 测试：`TestRegisterOverTLS`（注册走 TLS，R-URI/Contact 均为 sips:、Via 为 TLS）、`TestDialOverTLS`（INVITE/ACK/BYE 走 TLS）、`TestIncomingTLSConnection`（对端按 Contact 连本端 TLS 监听并拿到响应）、`TestIncomingOverTLS`/`TestCancelWhileRingingOverTLS`（见「修复」）、`TestClientTLSVerifyToggle`（默认不校验可连自签、开启校验后拒自签）、`TestSelfSignedCertReuse`（证书复用与私钥权限）、`TestContactForTransport`、`TestParseSipsURL`/`TestSplitURL`、`TestTransportNormalize`。
  - 需求文档同步：REQ-ACC-006 细则、REQ-ACC-002（传输加 TLS）、REQ-PROTO-001/002（sips://）。
  - **真机验证**：对真实 OpenSIPS（10.168.4.40 的 TLS 监听 **15061**，自签证书 CN=sip.mti-sh.cn）注册成功，Contact 为 `sips:2999@192.168.3.15:<端口>;transport=tls`；经服务器呼出「打给自己」时，服务器沿 TLS 把 INVITE 送回了本端 TLS 监听（单通话限制回 486，属预期）；另用两个沙箱实例完成一次完整 TLS 通话（拨出 → 振铃 → 接听 → 通话建立 → 双方挂断），SIP 信令记录里 INVITE/BYE 的请求行与 Via 都是 `sips:`/`TLS`。呼入方向（对端拨入、本端接听、对端挂断）受服务端路由配置影响，见「修复」末尾的说明。

## [1.0.12] - 2026-10-05

### 新增

- **「启用视频通话」总开关**（REQ-VIDEO-014）：配置页「视频设备」卡片顶部（摄像头选择**之上**）新增勾选项，**默认勾选**。取消勾选后视频功能**整体隐藏**——配置页的摄像头选择**置灰不可选**（但仍照常枚举并回显当前选择，关一下开关不该把用户选好的摄像头"弄丢"）、主窗体「视频呼叫」按钮不显示（呼叫按钮恢复占满整行）、**通话记录与通信录**行内的视频呼叫按钮不显示、来电只显示「接听」（不显示「视频接听」，事件里也不带视频）、**媒体协商的 SDP 不含 m=video**（呼出 offer 不创建视频引擎；带视频的来电按纯音频处理，answer 只回 m=audio，与"没启用视频的构建"逐字节一致）、网页 `video.subscribe` / `call.dial{video:true}` / `call.answer{video:true}` 返回新错误码 **`video_unsupported`**，SDK 的 `subscribeVideo` Promise 失败（并忘掉订阅参数，断线重连不再偷偷重试）。开关**即时生效并保存**（配置文件 `video.disabled`，**零值即启用**：旧配置没有该字段、沙箱实例的空白配置都保持启用）；本构建没有视频能力时该勾选框置灰不可点。
- 内部：`core.App` 新增 `VideoCallEnabled()` / `VideoUsable()`（构建能力 × 总开关）与 `SetVideoCallEnabled()`，`Telephony` 接口同步扩展；视频入口的显隐、SDP 的 m=video、网页订阅统一以 `VideoUsable()` 为准。
- **音频编解码新增 G.722（REQ-MEDIA-011，7 kHz 宽频）**：64 kbit/s 模式 1、静态 PT 9、`a=rtpmap:9 G722/8000`（**RTP 时钟是 8000**，不是它的 16 kHz 采样率）。
  - **纯 Go 实现**（`internal/media/g722`：QMF 滤波组 + 两个子带的自适应 ADPCM），**不引入 CGO、静态库或第三方依赖**，任何构建（含 `-tags noaudio` 纯 Go 构建）都带；正确性由 **FFmpeg 参考实现生成的 golden 向量**钉死——编码逐字节一致、解码逐样本一致（向量在 `internal/media/g722/testdata/`，生成方法写在测试文件头部）。
  - **按需 16 kHz**：`Codec` 接口新增 `SampleRate()`（PCM 管线采样率，**与 `ClockRate()` 不是一回事**），媒体引擎按当前协商编解码器的采样率工作——G.711/Opus 通话恒 8 kHz（与历史行为一字不变），**只有协商到 G.722 的通话**把音频设备、播放缓冲、诊断 WAV 整条管线切到 16 kHz 真宽频。设备开流速率取 `engine.SampleRate()`；**183 早期媒体与 200 OK 之间换到不同采样率**（或 re-INVITE 换编码）时，音频层按队列自动关流重开（速率没变不重开），并清空旧速率的播放缓冲与采集半帧。
  - **SDP 优先级排在 Opus 之前**（PCMA > PCMU > G.722 > Opus，真机与 FreeSWITCH 互通验证后按用户要求定的）：对端同时提供 G.722 与 Opus 时走 G.722（7 kHz 宽频、固定 64 kbps），而对端有 G.711 时仍按老规矩走 G.711（默认通话行为不变）；answer 复用对端 PT，无 rtpmap 时按静态号 9 回退。
  - 时间戳步长 160（20 ms @8 kHz 时钟）与 PCM 帧长 320（20 ms @16 kHz）各归各；丢包仍补静音（不做 PLC，与 G.711 一致）；编码器/解码器是包级共享实例，实现 `resetter`（通话开始/换流/换编码时复位）。
  - 修既有问题：`-tags "headless noaudio"` 纯 Go 构建编不过（多实例那次改动给 `runGUI` 加了实例槽位参数，headless 桩没跟着改）。

### 变更

- **构建脚本改名**：`scripts/build-audio-lib.sh` / `.bat` → **`scripts/build-audio-libs.sh` / `.bat`**，与视频的 `build-video-libs.*` 命名对齐（构建脚本、文档与代码注释里的引用一并更新）。**已生成的 `third_party/audio/<目标>` 不受影响，不需要重新生成库**；沿用旧文件名的命令按新名字敲即可。

## [1.0.11] - 2026-10-05

### 新增

- **多实例（沙箱槽位）**（REQ-NFR-007）：**没有配置开关**（槽位只能显式指定，不带参就是主实例），用 `-instance N`（1–99）或 `sip://账号:密码@服务器:端口?instance=N` 可再启动实例——**槽位 0 是主实例**（目录与行为完全不变），**槽位 ≥1 用独立数据目录** `instances/N/`（配置、通话记录、通信录、日志、单实例锁各自一份），首次启动是**空白默认配置**、WS 端口从 **8775** 起找（被占用仍自动 +1 并存入该实例自己的配置）；沙箱实例**不做**系统级协议/自启注册（避免覆盖主实例的），sip:// 拉起仍转发给主实例；**测试页**的「URI 拉起软电话」卡片新增「实例槽位」输入，填 1–99 生成的链接会带 `?instance=N`（该实例在跑就转发给它，没跑就用它的独立数据目录启动），留空/0 = 主实例。**窗口按槽位从右往左分列**：列宽 = 通话面板宽 + 边距，两个实例的通话面板紧挨着排（槽位 1 在主实例左边），视频窗口右边缘对齐自己那一列的面板。用途：多端点联调（例如两个账号互相打视频做对比）。

### 修复

- **摄像头禁用时看不到对方画面（连视频窗口都不出现）**（REQ-VIDEO-005）：逐层排查（服务器侧抓包 + 本端日志对齐）确认——对端 FreeSWITCH 先把媒体发到我们 SDP 里宣告的地址，**只有收到该腿的有效媒体后**才把发送目标切到它实际观察到的源地址（对称 RTP / 媒体锁定）。摄像头禁用时本端在视频流上一个包都不发，对端就一直把画面发往本机内网地址（NAT 场景不可达，且出向端口会被路由器改写、无法预先宣告），于是永远收不到。**现在摄像头不可用时发送一路占位画面**（320×240 静止深灰、5fps、每秒一个关键帧），对端即正常回送画面；对端屏幕上是一块静止深灰，不是本端画面，本地也没有回显（仍不采集摄像头）。
- **视频画面出得慢**（REQ-VIDEO-003）：**收到 183 早期媒体（含 m=video）就提前协商并启动视频引擎**（原来只在 2xx 之后才开始发）。对端在早期媒体阶段就可能开始送画面，而且本端越早在该腿发东西、对端越早把发送目标切到我们的真实地址——实测"接通后才出画面"从 8 秒级（不发/只在接通后发）压到 1.5 秒内、且锁定发生在接通之前，与对端软终端的行为一致。剩余数百毫秒~数秒的延迟在源端（对端编码器的关键帧节奏），已用"换客户端复现"确认与本端无关。
- **振铃期窗口尺寸与本端画面不匹配**（REQ-VIDEO-006）：振铃期窗口里只有本地回显，但窗口尺寸沿用默认值（首次之后还会沿用**上一通电话**留下的尺寸——比如上一通按对端流定的大小），表现为"本地画面装在一个不匹配的框里、连旋转画面也不跟着变"，接通后由对端画面纠正。现在振铃期**按本地画面尺寸适配窗口**（旋转本地画面时帧尺寸宽高互换，窗口形状随之重设）；接通后由对端画面接管。
- **视频窗口不随分辨率变化适配**（REQ-VIDEO-006）：原来只在**首帧**定一次窗口尺寸，振铃期的早期媒体画面（如回铃视频 352×288）会把尺寸定死，接通后换成对端真实分辨率（如 192×128）不再调整。现在**分辨率变化即重设**（首帧、通话中换分辨率/换流都覆盖），尺寸没变时仍直接早退、不重复定位窗口。

- **测试页**：视频卡片的「订阅视频」「取消订阅」合并为一个**可切换按钮**（订阅后变「取消订阅」，再点即退订，按钮配色随状态切换）。
- **振铃期的视频窗口与回显**（REQ-VIDEO-006/007，用户要求）：本端**不推流**（摄像头禁用/不可用）时，振铃期**不显示**视频窗口（早期媒体/回铃画面即使到了也不展示），接通后才显示对端画面；本端**在推流**时，振铃期窗口里**本地回显铺满整个窗口**（不画描边、不显示对端画面），**接通后回显缩回右上角小窗**并开始显示对端画面。
- **测试页**：通话控制卡片里视频通话的「视频开关」按钮从扬声器控制那一行移到**「挂断」之后**（REQ-TEST-004）。
- **.gitignore**：忽略 `*.pcap`（服务器抓包分析产物，与 `rtp_rx.wav` 同类，勿入库）。
- **视频订阅新增 `keepWindow`（REQ-VIDEO-009 修订）**：`video.subscribe` 可带 `keepWindow`（默认 `false`）——订阅后是否**保持软电话的视频窗口显示**：不传/false 时照旧关闭窗口（画面在网页看），传 `true` 则窗口继续显示（桌面与网页同时看）；多连接按"任一订阅者没要求保持就隐藏"汇总，`video.web` 事件与 `system.state` 的订阅状态新增 `hideWindow` 字段；JS SDK 的 `subscribeVideo` 支持 `keepWindow`；测试页视频卡片新增「保持软电话视频窗口显示」勾选框。

- **需求文档整理（docs/requirements.md）**：修正编号与状态——「183 回铃音」（呼出接收早期媒体）此前与「通话面板扬声器/麦克风开关」**撞用 REQ-UI-023**，现回铃音改用新号 **REQ-UI-039**（REQ-UI-023 保留给开关，代码与 ws-protocol.md 的引用不动）；删除重复的 REQ-UI-031 表行、REQ-WS-014 与 REQ-TEST-006 行内重复的单元格；章节顺序整理为 4.11 NFR → 4.12 视频 → 4.13 发布 → 4.14 通信录（原 4.14 插在 4.11 前），REQ-VIDEO-011 归位到 010 之后；**REQ-ACC-021（服务端心跳自学习与超时恢复）与 REQ-NFR-003（网络抖动/短暂中断自动恢复）验证通过，状态由「待验证」改为「已完成」**；1.0.0 交付范围的「排除项」按实际交付情况重写（仍待开发的只剩 TLS / AEC·NS·AGC / G.722 / SRTP / 录音 / 长时间通话稳定性）。
- **README 与 SDK 类型声明同步**：当前状态表更新（补视频、通话记录、通信录、音量控制、自动更新自检等已交付项，后续版本行改为仅列 TLS/SRTP/AEC/G.722/录音/长时间稳定性）、构建示例版本号 1.0.5 → 1.0.11、更新链路描述改为「软电话自己定时查询站点」（原写的是 SDK 查询，REQ-UPD-019 已取消）、目录结构补齐缺失模块；`web/sdk/sip-phone-sdk.d.ts` 的事件注释去掉已废弃的 `update.check` 并补齐现有事件（video.transform / system.ready / system.updateConfig / system.autoAnswer）。

## [1.0.10] - 2026-10-05

### 新增

- **自动更新：查询搬到软电话自己（REQ-UPD-010/017/018/019）**：以前「有没有新版本」由网页端 SDK 定时查询、查到后经 WS 告知软电话——**页面没打开就不会检查**。现在：
  - **配置页新增「自动更新」卡片**：更新站点（**留空 = 不检查**）、应用 ID（默认 `mti-sip-phone`）、检查周期（分钟，默认 5，可填 1–1440）、接受预览版（默认否），外加「立即检查」按钮（结果文字化显示）。**配了站点就会自动检查**：启动即查一次、之后按周期查，查到新版本照旧弹右下角提示窗 + 主窗体提示行，**下载与重启仍必须由用户在界面上点击**（不变）。
  - **网页可写这份配置**：WS 新增 `system.setUpdateConfig`（字段可选、只改传入的）、事件 `system.updateConfig`，`system.getState` 增加 `updateConfig` 字段；JS SDK 新增 `setUpdateConfig(cfg)`，构造参数 `updateUrl`/`updateAppId`/`updateIntervalMinutes`/`updatePrerelease` 会在连上后自动写入一次（业务前端统一管理更新策略）。
  - **SDK 只保留强制推送**：移除 `checkUpdate`/`listReleases`/`setUpdateSource`/定时器/`update.check` 事件及相关构造参数；`notifyUpdate(info)` 保留且**恒为强制推送**（不受版本高低与预览版约束）。
  - **测试页**：自动更新卡片保留（最新版本 / 最新稳定版 / 每行「推送」），但**查询改由页面自己 `fetch` 站点接口**；新增「写入软电话配置」按钮把卡片里的值写进软电话。
  - 另一处顺带修掉的问题：首次检查在界面订阅事件之前完成时，更新提示窗不会弹（事件已发过、同版本又被去重挡掉）——界面启动时补取一次更新状态。

- **音量控制（REQ-MEDIA-006）**：通话面板的**扬声器/麦克风按钮**支持**鼠标滚轮**调节——不新增桌面控件。悬停按钮上向上滚调大、向下调小（每格 5%），**按钮底色按百分比圆周填充**（从正上方起顺时针扫过，12→3→6→9；按钮外形不变）：**100% 满蓝**，麦克风 100%→200% 的增益段在蓝色之上再叠**橙色**（200% 整块橙）；**0% 即禁用**，按钮变成浅灰底 + 红色关图标（与原「关」的外观一致），麦克风停发、扬声器停播；100% 时与原来的蓝底白图标观感一致。滚动时在**指针处弹出百分比提示**（约 1 秒后收起）。量程**扬声器 0–100%、麦克风 0–200%**（麦克风可额外放大），点击按钮仍在 0% 与上次非零值之间切换；状态**会话内记忆**（通话之间保留，重启回默认 100%）。增益/音量在媒体引擎的采集与播放路径上生效（麦克风增益放大后削波），提示音（回铃音/振铃/按键音）不受影响。
  - **网页侧（REQ-WS-013）**：新增 `call.setMicGain` / `call.setSpeakerVolume`（百分比整数，0 = 禁用），JS SDK 提供 `setMicGain` / `setSpeakerVolume`；**测试页通话控制卡片新增两个音量滑块**（拖动即调、通话状态实时跟随）；`call` 快照新增 `micGain` / `speakerVolume`，网页与桌面面板是同一份状态。**事件合并**：滚轮/拖滑块连续调节时 `call.audio` 最多 150ms 一条（首次立即发、最后一次必发），不再把事件总线刷爆、也不再把测试页日志刷屏。
  - **测试页**：通话控制卡片里**扬声器、麦克风各占一行**（左边开关按钮、右边音量滑块 + 百分比），通话计时挪到「通话中」文字后面。
  - **诊断**：`SIP_PHONE_AUDIODUMP=1` 时新增 `mic-<RTP端口>.wav`（含增益的采集样本），每秒汇总新增**采集峰值 / 采集RMS / 麦克风增益%**——判断"增益有没有生效、是不是已经削波"用。

## [1.0.9] - 2026-10-04

### 新增

- **通信录（REQ-CONTACT-001~005 / REQ-UI-032~038）**：新增本地通信录（号码 / 名称 / 备注 / 常用），存**独立的 SQLite 库**（配置目录 `contacts.db`，与通话记录分开、互不影响），启动即载入内存，界面与网页查询都是即时响应。
  - **页面**：主窗体顶栏新增「通信录」按钮（位于**「通话记录」右侧**，通话中随其它按钮一并禁用），与通话记录页一样切在主窗体内、不新开窗口。**两个 tab**：常用联系人 / 所有联系人（后者顶部带搜索框）；**一条联系人两行**——第一行是号码 + 金色 **★**（常用标记），第二行是名称 + 备注（备注过长自动截断补省略号，不会挤乱布局），行右侧是纵向居中的「呼叫」「视频呼叫」按钮。页面顶部四个按钮：**增加 / 导入 / 导出 / 清空**（清空为主题错误红 + 垃圾桶图标，二次确认）。列表滚动分页（每批 50 行）；多选手势与通话记录页一致（单击 / Shift + 单击 / 按住拖动）。
  - **号码去重**：号码按规范化比较（`sip:` 前缀、尖括号、`;user=phone`、`@host`、空格与 `-()[]_./` 分隔符统一归一，`2999` 与 `sip:2999@10.168.4.40:5060` 视为同一人）；新增或改号撞上已有号码时**当场在对话框里提示「该号码已在通信录中」且不关窗**；**名称必填**。
  - **搜索**（所有联系人 tab）：支持**音头 / 全拼 / 汉字任意组合**匹配名称——`zs`、`zhangsan`、`zhangs`、`张san`、`张三` 都能命中「张三」，多音字任一读音都算；同时模糊匹配**号码与备注**（输入 `138` 命中 `+86 138-0000-0000`）。排序按**名称拼音**（空名称排最前，刚建好还没起名的条目一眼能看到），同音再按号码。
  - **右键菜单**：设为常用 / 取消常用 / 修改 / 删除；多选时可**批量设为常用 / 批量取消常用 / 批量删除**——选中项全是常用就不显示「批量设为常用」，全不是就不显示「批量取消常用」，批量项不显示条数。**右键的条目没被选中时先单选它**再弹菜单，已选中则保持原选中集合不变（与资源管理器一致）。
  - **CSV 导入导出**：导出为**标准 CSV**（表头 `号码,名称,备注,常用`，RFC 4180 转义，**UTF-8 带 BOM**——中文 Windows 上 Excel 双击打开不乱码）；导入**按号码去重**，已存在的只覆盖号码/名称/备注、**常用标志只增不减**（本地已标就保持，本地没标而文件标了就变成常用），没有的整条新增；解析容忍 BOM、无表头、列序不同、列数不齐与不规范引号，完成后提示「新增 N 条、覆盖 M 条、跳过 K 行」。文件对话框优先用系统原生（Windows / Linux 桌面环境），不可用时回退内置。
  - **联动显示**：通话记录行与通话面板都在**号码右边**显示通信录名称（常用联系人在名称右边再加 ★），**名称上鼠标悬停**弹出备注提示；视频窗口标题与未接来电通知同样优先用通信录名称（通知仍保留号码）。**通话记录的右键菜单**新增「添加到通信录 / 编辑联系人 / 删除联系人」——号码已在通信录里时显示后两项（删除需确认），否则打开新增对话框并**预填该号码**；保存/删除后记录页立刻刷新。
- **通信录的网页接口（REQ-WS-015 / REQ-CONTACT-004/005 / REQ-SDK-006 / REQ-TEST-007）**：
  - 通话事件与 `system.getState` 的 `calls[]` 新增 **`contact`** 字段（`{name, note, starred}`，命中通信录时才有），网页据此在号码旁显示名称、★ 与备注。
  - **JSON REST 接口**：`GET /api/contacts` 返回 `{count, contacts:[{number,name,note,starred}]}`；`POST /api/contacts` 接收数组或整个对象（导出→改→导入可直接回灌），返回 `{added,updated,skipped,total}`——网页可直接 XHR/fetch 收发结构化数据，不用自己解析 CSV。
  - **CSV 接口**：`GET /api/contacts.csv` / `POST /api/contacts/import`（一键下载/上传，不受 WS 消息大小限制）；WS 侧另有 `contact.export` / `contact.import` 供需要走 WS 的客户端。
  - **JS SDK** 新增 `exportContacts` / `importContacts` / `downloadContacts`（浏览器一键下载 CSV）/ `importContactsFromFile` / `exportContactsJSON` / `importContactsJSON`；**测试页**新增通信录卡片（四个按钮两行两列对齐 + 结果行），通话卡片的对端名称优先显示通信录名称与 ★。

### 变更

- **顶栏注册状态改为显示状态文字**（已注册 / 注册中 / 注册失败 / 未注册）：文字颜色跟随状态圆点、加粗、字号与「自动接听」按钮一致；**本机号码不再显示在顶栏**（窗口标题与配置页仍能看到）。
- **通话面板三行微调**：面板高度 140 → **144px**；第 2、3 行比第一行整体**下移 4px**；三行文字整体**右移 2px**。三行等高（= 按钮尺寸），没按钮的行也和有按钮的行一样高、左边缘对齐。
- **信息/错误/确认弹窗改为自拼内容**：不再用 Fyne 自带的文本对话框（它会在右上角画一个巨大的半透明图标水印，压住标题且高度虚高）；**确认框正文左侧增加警告图标**，四类弹窗宽度统一（约 270px，不再撑满窗口）。
- **通信录的新增/修改对话框撑到 300px 宽**（原来按内容最小尺寸铺在主窗体里，字段窄得看不清）。
- **测试页**：通信录卡片移到**左列**（三栏布局的第一栏、「通话控制」卡片下方）。
- **依赖**：新增 `github.com/mozillazg/go-pinyin`（纯 Go、MIT，带去字典约 +1~2MB，无 CGO、不影响交叉编译），用于通信录的拼音搜索与排序。

### 修复

- **测试页「导出 CSV」报错**：SDK 从 WS 地址推导 HTTP 基址时把 `ws://` 原样当成了 HTTP 基址（`fetch` 不支持 ws 协议，必然 "Failed to fetch"），现在显式换成 `http:`/`https:`。
- **顶栏注册状态过一会儿又变回本机号码**：注册状态事件分支里还留着一处旧的「设置本机号码」，每次续注册都会把状态文字覆盖掉。

## [1.0.8] - 2026-10-04

### 新增

- **通话记录（REQ-CALL-007 / REQ-UI-031）**：新增本地通话记录，存 **SQLite**（配置目录下 `call-log.db`，驱动 `modernc.org/sqlite`——纯 Go、无 CGO，四个构建目标都不受影响，代价是每个产物约 +4MB）。
  - **记录口径**：区分呼入/呼出与是否接通；**已接通**显示通话时长，**未接通**来电显示振铃时长、呼出显示拨号时长；未接来电标红（"未接"复用核心既有的判定——本端拒接与振铃中本端挂断不算未接，因此状态按原因细分：来电为**未接听 / 拒接 / 已接通**、呼出为**未接通 / 取消 / 已接通**，不会把拒接错标成已接通）。开始、接通、结束三个时刻在核心层直接落库，且**先写记录再发结束事件**，通话一结束界面刷新就能看到。
  - **界面**：主窗体顶栏「通话记录」按钮位于**「配置」左侧**（通话中随「配置」一并禁用），**与配置页一样切在主窗体内、不新开窗口**。页面紧凑、字号小、竖屏手机式，**一条记录两行**：行首是**方向图标**（实心话机 + 粗箭头，↗ 呼出 / ↙ 呼入，**绿色=已接通、红色=未接通**；视频通话在号码右边加一个与文字同色的小摄像头标记），第一行是号码/显示名与右侧的开始时间，第二行是「状态 · 振铃/拨号时长（· 通话时长）」，行右侧是蓝底白图标的「呼叫」「视频呼叫」按钮。顶部一行是返回图标 + 标题 + 居右的「清空通话记录」（二次确认），其下是按号码筛选的输入框（输入防抖 300ms，`%`/`_` 当普通字符）。列表文字统一用主题前景色，行距 2px、行间一条细分割线（深色主题深灰、浅色主题浅灰）。
  - **列表交互**：**滚动加载**（滚到距底部约 4 行自动拉下一批，每批 50 条），没有页码、不显示总条数；**多选不设复选框/全选**——单击选中一条（再点取消）、**Shift + 单击**增减、**按住拖动**选中连续区间，选中行以主题选中色高亮；**列表里没有删除按钮，删除走右键菜单**（删这一条 / 删选中的 N 条，右键不改变选中状态）。
  - **健壮性**：同一通话按 `call_id` 幂等——SIP 层对未接通的呼出会上报两次结束（`OnFailed` 后 defer 还会再报一次 `OnTerminated`），只认第一次、保留更具体的原因（如 `486 Busy Here`）；进程崩溃或自更新重启留下的"进行中"记录在下次启动清扫为「已中断」（结束时刻取 `MAX(开始, 接通)`，时长不会为负）；号码/显示名来自 SIP 报文，落库前按 rune 截断、展示前清理控制字符；数据库打不开只降级为"本次运行不记录"，不阻断启动。
- **弹窗支持指定父窗口**：新增 `showInfoIn/showErrorIn/showConfirmIn`（原三个函数变成传主窗体的薄封装）。

### 变更

- **主窗体顶栏**新增「通话记录」按钮（「配置」左侧），通话中与「配置」、自动接听、呼叫按钮一并禁用（REQ-UI-030）。
- **配置页顶栏改为与通话记录页同款**：由一个「← 返回」文字按钮改为「返回图标 + 标题『配置』」，两页风格统一。
- 紧凑按钮新增 `SetIconSize` / `SetCornerRadius`；`newCompactTextOnlyButton` 补上 `baseBg`（此前禁用后再启用会把背景还原成透明）。
- **依赖**：新增 `modernc.org/sqlite v1.34.0`（纯 Go SQLite 驱动，windows-386/amd64 与 linux-amd64/arm64 均已验证，交叉编译无需额外工具链）。

## [1.0.7] - 2026-10-04

### 新增

- **媒体诊断探针（开发者用，默认关闭）**：设 `SIP_PHONE_AUDIODUMP=1` 启动后，每通电话在日志里每秒输出一行「【诊断】媒体汇总 / 音频回调汇总」（收包、写入/补静音样本、播放回调次数、回调请求与实际读出样本、缓冲深度、累计截断；音频侧另有回调次数与帧长范围），并把**网络收到的声音**（`rx-<RTP端口>.wav`）与**用户实际听到的声音**（`play-<RTP端口>.wav`）落进日志目录，可用 `python tools/wavscan.py <wav>` 看时间轴与波形跳变。采集侧不阻塞音频回调（写盘在后台 goroutine），不开启时零开销。定位"回铃音断续"就是靠它比出了"rx 正常、play 被切"。
- **未接来电桌面通知**（REQ-UI-010）：呼入**振铃过但没接**（对端取消/挂断、协商失败）时弹系统通知，三行分别为：应用名（Windows 取 AUMID 注册表的 DisplayName，固定「mti-sip-phone 软电话」）/ 标题「未接来电」/ 正文「显示名（号码） - 振铃 N 秒」（时长按界面计时器算，不足 1 秒按 1 秒，超过 1 分钟用「N 分 N 秒」）。**本端拒接与振铃中本端挂断不弹**（用户当场处理了），忙时被 486 直接拒掉的来电也不弹（那条路径连界面都不经过）；同一通话重复上报结束只弹一次。判定在核心层（`call.terminated`/`call.failed` 事件新增 `missed` 字段），网页端与将来的通话记录共用同一答案。
  - **Windows**：便携 exe 没有「开始菜单快捷方式」，AUMID 未注册时系统 toast 会**静默失败**，所以首次发通知前写一次 `HKCU\Software\Classes\AppUserModelId\com.mti.sip-phone`（DisplayName/ShowInSettings，免管理员，幂等）。**没有直接用 Fyne 的通知实现**：它把标题/正文用 Go 的 `%q` 拼进 PowerShell 脚本，而 PowerShell 的转义符是反引号——正文里的对端显示名完全由主叫控制，等于一条命令注入通道。现在脚本是编译期常量、标题/正文/应用 ID 全部走环境变量（不参与命令解析），模板用 `ToastText02` 不带图（仓库图标是 SVG，Windows toast 只认 PNG/JPG）。
  - **Linux**：走 Fyne 的 D-Bus 通知（`org.freedesktop.Notifications`），无守护进程时只记日志；该调用是同步的，已放到后台 goroutine，不阻塞 Fyne 主线程。
  - 已知限制：通知不支持点击唤起程序；Windows 依赖 PowerShell，Linux 依赖通知守护进程。

- **系统休眠唤醒 / 网络切换后自动恢复注册**（REQ-CALL-009）：此前续注册靠 `0.7×expires` 的定时器，而它走**单调时钟**——系统挂起期间该时钟不走，醒来后租约可能早已在服务端过期（服务端把来电送到失效的 Contact，本机却还显示"已注册"）；网络切换换掉本机出口地址后，也要等下一次续注册（最长几十分钟）才纠正。新增**注册看护**（`internal/sip/netwatch.go`）：每 5 秒巡检两件事——**墙钟跳变**（相邻两次巡检的墙钟间隔远超巡检周期即判为休眠唤醒）与**到 SIP 服务器的出向源地址变化 / 网络从不可达恢复**——命中即刻重新注册；注册失败退避等待期间收到信号同样立刻重试（网络一恢复就重注册，不必等满退避）。STUN 启用时先重探公网地址再注册（旧 NAT 映射已失效；重探失败则清空公网地址、回退本机地址注册，避免"显示已注册但不可达"）；TCP 传输下另丢弃 sipgo 复用的旧信令连接（UDP 出向 socket 是通配绑定、可自愈；TCP 连接死亡后不会被清出池，不丢弃会一直重发到死连接）。顺带修复：关掉 STUN 重新注册时 Contact 仍带上一次的公网地址；配置切换后旧 STUN 探测结果可能污染新账户的 Contact。

- **服务端心跳检测（REQ-ACC-020/021）**：设置页新增「启用服务端心跳检测」（默认开启），开启后注册报文的 **User-Agent 带 `optchk` 标记**（Contact 不变），服务端据此定期发 OPTIONS 探测；**OPTIONS 无需专门应答**——sipgo 默认回 405，实测服务端接受（曾试过自己回 200，在 NAT/rport 场景下与登记地址对不上、反被判探测失败）。另从**传输层**统计探测并自学习间隔：至少 5 条心跳取平均武装判定、亚秒级重发不计样本、间隔突增 >3× 视为探测重启重学；**超过 2.5 个周期**没收到探测即判为**服务端掉线**——先把注册状态置**未注册**（界面圆点与托盘图标变红、原因"服务端心跳超时"），再立即重新注册（服务端把本机摘掉后不必等 0.7×expires 的续注册），触发后复位、等下一条心跳重新武装；服务器不发 OPTIONS 时恒不触发。

### 修复

- **SIP 信令日志重复记录**：`internal/sip/tracer.go` 的全局跟踪器（sipgo 传输层钩子）本就记录每一条收发报文，而 `phone.go` 里还散着 11 处 `logx.TraceSIP(...)` 把同样的报文又记一遍，日志里每条信令都是两份（且散点不受「SIP 信令记录」开关控制）。现只保留全局跟踪器（唯一出口、受开关控制），并顺带过滤非 SIP 载荷——服务端/NAT 每约 10 秒发的四个 0 字节保活包此前也被当成"空报文"记进日志。
- **第一声回铃音"断续"（真因：日志轮转把接收循环与音频线程卡住）**：根因在 `internal/logx` 的轮转实现——`gzipFile` 里 `return os.Remove(src)` 在 `defer in.Close()` **之前**执行，而 Windows 上 Go 打开文件不带 `FILE_SHARE_DELETE`（`syscall_windows.go` 的 sharemode 只有 READ|WRITE），自己的读句柄没关就删文件**必然失败**。后果是雪球式恶化：归档"压完却删不掉"→ 日志文件永远不截断（实测卡在 42.8MB）→ 尺寸持续超过 10MB 上限 → **之后每次写日志都再完整轮转一次**（把整个文件重新 gzip 一遍）且全程持着写锁 → 写日志的线程（SIP、界面、注册看护、**RTP 接收循环**）被随机卡住数百毫秒（原始日志里同一个接收循环迭代内相邻两行日志相差 **494ms**）。通话开始时的日志突发（183 的 SIP trace + 媒体日志）正好撞上轮转：接收循环停摆 → 对端媒体包在 socket 缓冲区排队 → 恢复后成批涌入（实测 161 包/秒、写入 25760 样本/秒）→ 播放缓冲超深（>500ms）→ `trimQuiet` 在"整段都是音调"里找不到安静窗口（切点能量比=1.00）→ **从第一声回铃音中间切掉 320ms**，听感即"断续的两声嘟"；第二声之后因流已平稳、截断落进停顿而正常。修复：轮转改为**关闭 → 改名切走（改名即完成截断）→ 立即开新文件继续写 + 后台 goroutine 压缩与归档移位**，不再持锁做重活；改名失败（日志被别的程序以无共享删除的方式打开）时退化为清空当前文件，连清空都不行则降级为"不再轮转"，**绝不回到"每次写都重压整个文件"的循环**；`Write` 补上句柄为空时的兜底。回归测试 `internal/logx/rotate_test.go`（正常轮转必须切断当前文件；改名被占用必须走清空退路）。验证：修复后同一位置收包 50/s、音频回调 50/s、读出 8000 样本/s、缓冲深度恒 160 且零截断，play/rx 两份 WAV 完全一致。
  - 同时保留前一轮的健壮性改进：播放缓冲超深截断**在缓冲区最旧一段里挑"最安静"的窗口丢弃**（语音与提示音的停顿足够容纳），找不到安静位置才退回最旧端并在接缝处做 2ms 交叉淡化——它不解决上述根因，但能避免"切在音调正中"这类最刺耳的失败模式。
  - 另修正一个观察到的**日志归档误导现象**：此前 `.1.gz~.9.gz` 内容完全相同、且每次轮转 mtime 全变（源文件从不截断 → 每次压的都是同一份越来越大的文件；归档序列移位改写了所有归档的 mtime）。修复后归档各不相同。
- **RTP/RTCP 端口配对不符 RFC 3550**：此前随机端口拿到奇数就直接用（RTP 奇数、RTCP = RTP+1），而服务端/网关普遍按「RTP 偶数、RTCP = RTP+1」配对，奇数 RTP 会被判为不规范（个别实现不予互通）。现在命中奇数端口时重选，音频与视频两对端口都按该约定分配。

## [1.0.6] - 2026-10-03

### 新增

- **拨号按键音 + 键盘发 DTMF**：点按拨号盘按键、或在**未聚焦输入框时**于主窗体任意位置敲数字都会回显按键音（**在号码输入框里输入数字不响、也不发 DTMF**）；**通话中**同时向对端发送 DTMF（RFC 4733）。按键音按 ITU-T 双音多频频率**合成**（8kHz 单声道 120ms，首尾 5ms 淡入淡出防爆音），走独立的提示音播放设备、与通话媒体流互不干扰；连续按键复用同一设备（播完后空闲 1.5 秒自动关闭），避免每次按键重开设备带来的滞后感。通话面板上敲数字同样发 DTMF。
- **通话中可唤出主窗体**：通话期间主窗体不再"禁止显示"，从托盘单击即可唤出，但**只放开号码按键与键盘数字**（都发 DTMF）——输入框（含最近号码下拉）、设置、自动接听、呼叫/视频呼叫按钮一律禁用并强制回到拨号盘页，通话结束后整体恢复。
- **DTMF 的网页接口**：WS 新增 `call.dtmf`（`{callId, digit}`）、SDK 新增 `sendDTMF(callId, digit)`；**测试页通话控制卡片新增 DTMF 拨号盘**（3×4，通话接通后显示）。
- **收到对端 DTMF 会发声**：RFC 4733 telephone-event 是**事件包不是音频**，此前收到就丢掉了，用户完全听不到对端按键（而自己发的按键音是本地合成的，所以对端听得到、自己听不到对端——不是网络问题）。现在按对端 SDP 声明的负载类型号识别，收到后本地合成按键音播放；同一事件的冗余重传只播一次。

### 修复

- **通话结束时的主窗体显示规则**（两次按用户实测修正）：**通话前打开的，通话结束一律打开**（不管通话中开关过没有）；通话前没打开的，看通话中的最后状态——自己打开又没关就保持打开，自己关掉了就不显示。
- **`call.dtmf` 一度返回 `unknown_method`**：新增 WS 方法时漏把它加进 handler 外层的方法分派列表（内层分支写了但不生效），由新增的 `TestDTMFFlow` 当场发现。

## [1.0.5] - 2026-10-03

### 新增

- **接入更新站点**：新增 `release/` 目录（`release.conf`：站点地址 / 应用 ID / 上传令牌；`app.json`：应用级元数据；`icon.svg`：图标）与 `scripts/release.sh`（Windows 用 `scripts/release.bat`，走 git-bash）**一键发布**：编译 → 按站点《应用发布规范 v1》打包（发布产物 + `release.json` + `CHANGELOG.md` + `SHA256SUMS`，打成 tar.gz）→ 先经站点接口写入应用级元数据（`PUT …/metadata`、`PUT …/icon`，不再直接操作归档目录），再上传发布包。产物文件名固定为 `<应用ID>-<平台>-<架构>[.exe]`（**不含版本号**，同一平台的下载地址跨版本不变）。上传前校验产物新鲜度（跳过上次构建留下的旧二进制），缺平台的产物显式列出；上传发布包（几十 MB）时显示实时进度条。
- **JS SDK 定时查询更新站点**：构造参数新增 `updateUrl`（**地址由网页初始化 SDK 时告知**）、`appId`、`updateInterval`、`updateChannel`、`updatePrerelease`；连上软电话后立即查一次并按间隔定时查询，查到更高版本自动经 `system.notifyUpdate` 告知软电话（软电话只弹提示，是否下载/重启仍由用户在软电话界面上确认）。新增 `checkUpdate()`、`setUpdateSource()` 方法与 `update.check` 本地事件（可区分已是最新 / 有新版本但本平台无产物 / 已告知过 / 未连接 / 站点 404 / 网络或超时）。预发布策略自适应：本地版本含 `-`（如 `1.0.5-rc`）时接受预发布，正式版只看正式版。另有 `listReleases()`（列出站点上的最新版本与最新稳定版，各带本平台产物信息，不做版本比较）与 `notifyUpdate({..., force: true})`（**强制推送**，跳过版本高低检查）。定时查询间隔默认 **5 分钟**（钳制 60 秒 ~ 24 小时），测试页初始化时设为 1 分钟便于联调。
- **强制推送更新**：WS `system.notifyUpdate` 新增可选 `force` 参数，为真时跳过"版本必须更高"的检查（平台一致性等其余校验照旧），可把同版本甚至更低版本推给软电话（联调、回滚用）。**自动更新不受影响**：SDK 的定时查询不带该参数，仍按版本高低决定要不要提示。
- **测试页**：自动更新卡片新增「更新站点 / 应用 ID / 通道」输入与「检查更新」按钮——点一下列出站点的**最新版本**与**最新稳定版**，每行带一个黄底白字的「推送」按钮（强制推送，列表文字用小一号字）；展示定时查询结果与软电话侧的更新状态。原来那个手动填版本号/下载地址的「告知新版本」离线演示已去掉（改用站点上的真实版本）。
- **构建**：`build.bat` / `build.sh` 读 `internal/core/buildinfo.go` 的基础版本号，**开发构建注入 `-rc`**（如 `1.0.5-rc`，非正式版），`SIP_PHONE_RELEASE=1` 的正式构建注入 `1.0.5`（发布脚本自动区分）；构建输出打印本次版本号。

### 修复

- **更新提示窗不再占用任务栏**：GLFW 会给每个顶层窗口都加上 `WS_EX_APPWINDOW`（作用是强制生成任务栏按钮），所以 splash 窗口虽然是 `WS_POPUP`，照样在任务栏留一条标题；而且这条按钮一旦被外壳建出来，之后改样式或隐藏窗口都收不回去（点「稍后」关掉提示窗后任务栏还残留一条点不动的按钮）。现在在**第一次显示的同一步内**完成「显示 → 立刻隐藏 → 换成 `WS_EX_TOOLWINDOW` → 再显示」，外壳来不及建按钮（实测：弹窗显示中与关闭后任务栏都没有它的按钮，反复弹出也不累积）。**通话面板同样按这个模式处理**，也不再占任务栏条目（视频窗口是带标题栏的普通窗口，保留条目）。
- **更新提示窗的图标改为绿色话机**：此前它没有自己的窗口图标，会继承应用图标——未注册时是红色话机。现在窗口显式设绿色话机图标，应用图标本身也固定为绿色话机；红色只留给托盘图标承担"未注册 / 注册失败"的异常提醒（REQ-UI-011 不变：托盘图标在启动时按注册状态显式设置一次）。

### 变更

- **版本比较支持预发布标识**：`1.0.5-rc < 1.0.5`、`1.0.5-rc > 1.0.4`、`1.0.5-rc < 1.0.5-rc.1`（`-` / `_` 引入预发布标识，逐段比较；`+` 之后为构建元数据不参与比较）。这样开发构建（带 `-rc`）在推送正式版时，即使版本号数字相同也会收到更新通知。原有行为不变：无法解析的版本号一律放行（裁决权在前端）。

## [1.0.4] - 2026-10-03

### 新增

- **音频编解码新增 Opus**：SDP 里排在 PCMA/PCMU 之后（本端 offer 固定 PT 96、rtpmap `opus/48000/2`）——双方都支持 G.711 时仍优先走 PCMA/PCMU，只在对方仅有 Opus 时才用它。编解码器按 **8kHz 窄带**工作（音频设备与播放缓冲本身就是 8kHz，不引入重采样），对端发来的宽带 / 全带包也能正常解码；丢包时用 libopus 的**丢包隐藏（PLC）**外推替代样本，比补静音自然。Opus 由第三方静态库（libopus）提供，某个目标缺库时自动降级为无 Opus（SDP 不宣告该编码，其余功能不受影响）。
- **构建**：新增 `scripts/build-audio-libs.sh` / `.bat`（音频第三方库的统一入口，产物在 `third_party/audio/<目标>`，不入库）；构建脚本探测到库就自动启用 Opus，**与视频的启用互相独立**；构建时逐目标打印本次能力（如 `CGO+音频+视频`，缺库时标出「无视频」「无Opus」，并提示补齐命令）。

### 变更

- **视频编码的负载类型号固定**：本端 offer 的 PT 由 H264=96 / VP8=97 / VP9=98 / H263-1998=99 调整为 **H264=99 / H263-1998=98 / VP8=100 / VP9=101**（只影响本端 offer 的号；作为被叫应答时仍按 RFC 3264 复用对端 offer 里的号，协商优先级顺序 H264 > VP8 > VP9 > H263 不变）。音频 Opus 用 96，与视频分属不同媒体行，互不冲突。

### 修复

- **Linux 上交叉编译 Windows 版时链接失败**：libopus 默认开启 FORTIFY_SOURCE，`memcpy`/`memset` 因此引用 glibc 的加固符号 `__memcpy_chk`/`__memset_chk`，而 mingw 的运行库不提供、最终链接又不经 gcc 的默认链接规格（不会自动补 `-lssp`），报 `undefined reference to __memcpy_chk`（Windows 本机构建恰好不触发，只在 Linux 主机交叉时暴露）。现对 mingw 目标关闭该库的 FORTIFY_SOURCE，Linux 目标保持原样。
- **协商到动态负载类型的编码时，应答里的编码名与时钟率写错**：此前按本端静态号反查编码名（Opus 等动态 PT 会退化成 PCMA、时钟率写死 8000），现在直接用协商结果生成应答（含 `opus/48000/2` 的时钟率与声道数）。仅影响对端 offer 用动态 PT 的编码，此前本端只有静态 PT 的 G.711，故未暴露。

## [1.0.3] - 2026-10-03

### 新增

- **视频通话**（呼出 / 呼入、双向）：
  - **编解码**：H264（OpenH264 编码、FFmpeg 解码）、VP8 / VP9（libvpx 编码、FFmpeg 解码）、H263-1998（FFmpeg），SDP 按 H264 > VP8 > VP9 > H263 协商；视频是与音频平级的独立 RTP/RTCP 端口对，丢包整帧丢弃并重置解包器、等下一个关键帧，新流首包即请求关键帧（PLI），对方尚未出画面时也会催。
  - **界面**：视频窗口显示对端画面，右上角为本地回显（双击全屏、标题栏可置顶、显示时自动带到最前）；呼叫按钮旁新增「视频呼叫」，来电含视频时弹窗显示「视频接听」，通话面板新增「关闭视频」开关（纯本地动作，不发 SIP 信令）；本地回显可单独关闭——只影响本地显示，不影响推送到远端、也不影响网页订阅。窗口尺寸按对端视频宽高等比放到 **640×640 内的最大尺寸**（长边 640，小分辨率也放大显示，不再按屏幕大小计算）；远端画面与本地回显各有一套**旋转 / 水平镜像**（旋转 90°/270° 时宽高互换，窗口形状立即跟着变；桌面与网页共用一份状态，重连不丢）。
  - **摄像头**：设置页枚举并选择设备（Windows DirectShow、Linux V4L2），支持「禁用」（禁用后不推送本地视频、仍可查看对方）。Windows 32 位版本的摄像头采集因上游调用约定限制不可用（编解码与收发正常），设置页会把摄像头选择置灰并提示。
  - **构建**：视频静态库由 `scripts/build-video-libs.sh` / `.bat` 按目标平台一次性生成（windows-amd64 / windows-386 / linux-amd64 / linux-arm64），构建脚本探测到 5 个库齐全即自动启用视频，缺失则自动降级为无视频构建（音频与界面功能不受影响，界面隐藏视频入口）。
- **网页订阅视频画面**：WS 新增 `video.subscribe`（按通道传参：远端必订、本地可选，各可设最大宽高 / 帧率 / JPEG 质量）、`video.unsubscribe`、`video.setTransform`（旋转 / 水平镜像），画面以**二进制帧**（通道 + 序号 + JPEG）逐帧推送；订阅后软电话自动关闭视频窗口，退订或连接断开自动恢复，重连后自动续订。新增 `video.web` / `video.transform` 事件。JS SDK 新增 `subscribeVideo({ remote, local, ... })`（**直接传 `<video>` 元素**，SDK 内部完成渲染，参数缺省取元素显示尺寸、两路可各带各的参数，附 `onStats` 回调）、`unsubscribeVideo()`、`rotateVideo` / `mirrorVideo`。`/sdk/*` 静态资源禁缓存，升级软电话后浏览器不会再用到旧 SDK。
- **测试页**：新增视频呼叫 / 视频接听、远端与本地画面（订阅后显示，带分辨率与实时帧率角标）、通话中的麦克风 / 扬声器 / 视频开关、画面旋转 / 镜像按钮、订阅参数选择；改为三列布局（视频与事件日志在右侧）。
- **自动接听开关**：状态改由核心维护，软电话界面按钮与网页 `system.setAutoAnswer` **共用一份**（谁改都广播 `system.autoAnswer`，另一端按钮同步），`system.getState` 增加 `autoAnswer` 字段；测试页新增自动接听开关。视频来电自动接听时按「视频接听」接通。
- **本地提示音**：新增本机回铃音与遇忙音（无早期媒体时播放本地回铃音，遇忙播遇忙音）。

### 修复

- **视频互通：对端 H264 只声明 `packetization-mode=0`（单 NAL 模式）时被误拒**。该参数说的是对端自己怎么发包，不影响本端接收；此前直接跳过 H264 → 200 OK 回 `m=video 0`（等于拒绝视频）→ 接通后两边都看不到画面（实测 FreeSWITCH 呼入的视频电话即此形态）。现在照常接受，answer 的 fmtp 回对端声明的模式。
- **视频来电「自动接听」只接通音频**：自动接听（SIP 头 `answer-after` 与自动接听开关）此前按「不接受视频」处理（answer 里 `m=video 0`，对端随之不再发视频）；现按「视频接听」接通——打开摄像头双向视频，摄像头被禁用或不可用时退化为只收不发。普通「接听」按钮仍是"不接受视频"。
- **32 位（windows-386）视频通话闪退**：FFmpeg 的 AVX 去块滤波在 i686 上必然崩溃（接通、对端画面开始解码的瞬间整个进程退出，GUI 子系统无控制台故无任何可见输出），现对该目标的 FFmpeg 关闭 x86 汇编（`--disable-x86asm`），解码走 C 回退。
- **视频窗口**：还没有对端画面时点旋转 / 镜像没有反应、窗口形状不变；退订网页视频后窗口回来时会被浏览器窗口挡在下面。现在旋转立即按新宽高调整窗口形状，窗口显示时主动带到最前。
- **本地回铃音 / 挂断音 / 遇忙音走错设备**：此前发到振铃设备，现全部走用户选择的扬声器设备（来电振铃仍用振铃设备）。
- **Linux 目录选择**：设置页「选择目录...」改用系统文件夹选择对话框（依次尝试 zenity / kdialog / yad），不再受 320px 主窗体尺寸限制；用户取消与"选择器不可用"区分处理（取消不再误弹内置对话框），都不可用时才回退内置对话框。
- **Linux 更新后自动重启**：某个环境下更新下载并替换成功后，进程会卡在 UI 退出阶段（日志停在「WS 服务停止」之后），导致"拉起新进程"永远执行不到、表现为更新后程序退出但没自动启动。现增加退出看门狗兜底：重启时先起一个脱离会话的看门狗，等待本进程退出（最多 10 秒），仍存活且确认是同一可执行文件时强制结束并拉起新进程；正常退出时不受影响。新进程在 Unix 侧另以 setsid 脱离父进程会话，父进程退出/终端关闭不再波及它。

## [1.0.2] - 2026-10-01

### 新增

- **自动更新**（由业务前端驱动，软电话自身不查询更新服务器）：
  - WS 协议：状态快照与 `system.getState` 增加版本号与编译目标平台/架构（`version`/`os`/`arch`）供前端查询对应架构的更新；新增 `system.notifyUpdate` 方法（前端告知「版本号 + 下载地址 + 可选 sha256」，版本不高于当前返回 `update_not_newer`）；新增 `system.update` 事件推送更新状态（进度、等待通话、失败原因）。
  - 软电话侧：收到通知后右下角弹出更新提示窗，同时主窗体呼叫按钮上方出现更新按钮（`更新 v1.0.3 来源 <主机名>`）；点击后自动下载（进度条、可选 sha256 校验与可执行文件魔数校验、失败给中文提示且可重试）、替换当前可执行文件（旧文件备份 `.old`、失败回滚）并自动重启；通话中则等通话结束后自动重启；启动时清理更新残留文件。**是否下载/重启均由用户在软电话界面上确认**（WS 只能告知，不能触发），提示窗显示下载来源主机名。
  - JS SDK：新增 `notifyUpdate()`；SDK 类型声明与测试页面同步（新增「自动更新」演示卡片，可模拟告知新版本）。
- **设置页日志目录**：选择目录改用系统文件夹选择对话框（Windows 原生，独立窗口、不受 320px 主窗体尺寸限制；取消不会额外弹出内置对话框）；新增「打开日志目录」按钮，用系统文件管理器打开当前生效的日志目录。

### 修复

- **托盘图标不再闪红**：定时续注册（以及 STUN 就绪触发的重注册）期间图标保持绿色，只有注册失败才转红；首次注册中仍为红色（此前每次续注册都会瞬间转红再转绿）。
- **重启流程**：本地端口修改后的重启此前会释放单实例锁并拉起新进程、却不退出旧实例（新旧并存、WS 端口漂移）；现统一走退出收尾（先关闭 WS/SIP 监听、注销 SIP、释放单实例锁，再拉起新进程），自更新重启同样走该路径。
- **窗口右下角定位**：句柄重试改为后台执行（此前同步阻塞 UI 线程，极端情况下会让更新提示窗/通话面板定位与去装饰失效）。

## [1.0.1] - 2026-10-01

### 新增

- **通话面板**：时长行右侧新增扬声器/麦克风开关按钮（呼叫/振铃/通话三态显示）：默认都打开，点击切换开/关。扬声器关=纯本地停止播放（继续接收 RTP，183 回铃音同样生效）；麦克风关=纯本地停止发送（不发 SIP 信令，区别于保持的 re-INVITE）。开关状态会话内记忆（再次弹出通话面板沿用上次状态），不持久化，应用重启后恢复默认都打开。
- **通话面板窗口形态**：去除窗口装饰（标题栏与关闭按钮），由程序自动放置屏幕右下角并置顶；来电时以边框往返灰度闪烁提示。
- **主窗体输入交互**：输入框内回车或主窗体任意位置按回车直接呼出；主窗体任意位置可直接输入数字/星号/井号，退格删除末尾字符；呼出后号码视为完成输入，下次点号码盘清空重新输入。
- **Linux 桌面完善**：通话面板在 X11 下与 Windows 行为一致（去装饰、右下角吸附、不进任务栏）；Linux 会话默认固定 X11（Xwayland）后端，规避 Wayland 下窗口不能自定位/去装饰/置顶与点击错位的问题。
- **跨平台构建**：新增 32 位 Windows（386）托盘产物（静态链接，免 MinGW 运行时 DLL）；Linux 侧新增对侧架构交叉构建（amd64 ⇄ arm64）与 Windows 交叉构建（build.sh），工具链或图形栈开发包缺失时自动跳过并打印准备步骤。
- **配置页版本信息**：设置页底部常驻一行，显示版本号、编译目标平台/架构与构建时间（构建时间由构建脚本注入）。
- **sip:// 协议注册增强**：Windows 按「默认应用」规范完整注册（ProgID `mti-sip-phone.sip` + Capabilities\UrlAssociations + RegisteredApplications），mti-sip-phone 成为系统「设置 → 默认应用 → 按协议选择默认值 → SIP」的正式候选；若 sip:// 被其他程序（如 linphone）抢占，可在系统设置中一键改回。

### 变更

- **WS 鉴权简化**：取消 token 方案，服务仅绑定 127.0.0.1 并拒绝非回环来源（REQ-WS-005/012）；JS SDK 与测试页面同步简化，清理 token 相关死代码与过期文档。
- **界面细节**：呼叫按钮固定配色（呼叫=绿底黑字，挂断=红底白字，不随主题变化）；通话期间（呼叫/振铃/通话中）从托盘唤出主窗口被拦截，避免遮挡通话面板。

### 修复

- **Linux 音频线程模型**：音频管理器改为后台单 worker 队列，设备操作与提示音不再阻塞 UI/SIP 线程（音频服务不可达时此前会卡住界面）；修复提示音与设备启停互锁导致的死锁（PulseAudio 实测设备启动不返回，通话无声）。
- **Wayland 会话**：关闭 GLFW 的 libdecor 窗口装饰（其释放顺序违反 xdg-decoration 协议，wlroots 合成器会断开客户端连接，表现为窗口显示不出来、托盘退出失效）。
- **32 位 Windows 构建**：修复 32 位 user32.dll 无 `SetWindowLongPtrW` 导出导致的通话面板静默崩溃（回退 `SetWindowLongW`）；修复 build.bat 在 if 块内的语法错误导致构建中断。

## [1.0.0] - 2026-09-29

首个发布版本（P0 范围：Windows / Linux，x86_64 与 arm64）。

### 新增

- **账户与注册**：SIP 注册（Digest 认证、expires×0.7 续注册、失败指数退避）、退出注销、rport 开关与 STUN NAT 穿透、本地 SIP 监听（UDP/TCP，随机空闲端口）。
- **呼叫控制**：呼出（INVITE → 180/183 → 200 → ACK → BYE，认证重试、取消与 2xx 竞态补偿）、呼入（振铃/接听/拒接 486/603、对端 CANCEL/BYE）、保持（re-INVITE sendonly/sendrecv，处理对端 re-INVITE）、单通话限制（忙时新来电自动 486 USER_BUSY）、DTMF 发送（RFC 2833）。
- **媒体与音频**：PCMA/PCMU 编解码（PCMA 优先）、RTP/RTCP 收发、样本粒度抖动缓冲（丢包静音填充、媒体流切换重置）、183 早期媒体回铃音（含 NAT 保活）、全双工音频（malgo/miniaudio）、音频设备枚举与选择、振铃/挂断提示音。
- **桌面界面**：系统托盘常驻（关闭窗口=隐藏到托盘，单击唤出）、拨号盘、通话面板（呼叫/振铃/通话三态、计时/保持/静音/挂断）、来电振铃动效（边框多彩闪烁）、设置页（账户/音频/WS 端口/开机自启）、开机自动启动（Windows 注册表 / Linux XDG autostart）。
- **WS 控制**：本地 WebSocket 接口（连接即推状态快照、拨号/接听/挂断/保持等方法、事件推送、心跳保活、端口可配置且被占用自动 +1）、JS SDK、内嵌测试页面。
- **协议与集成**：sip:// 协议注册与拉起、单实例参数转发（内部随机 token）。
- **配置与日志**：JSON 配置（密码 AES-256-GCM 加密落盘）、日志按大小滚动 + gzip 归档、SIP 信令跟踪与敏感信息脱敏。

### 说明

- 通话记录、录音、SRTP、回音消除（AEC）、G.722/Opus 等 P1/P2 需求留待后续版本。
