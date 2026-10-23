# 本地 WebSocket 控制协议

> 适用版本：1.0.15。网页通过本机 WebSocket 连接 SIP 软电话，实现拨号、接听、挂断等控制，并接收振铃、挂断等事件。
>
> 本文档面向网页前端开发者；JS SDK 已封装本协议，通常无需直接读写报文。

## 1. 连接

| 项 | 说明 |
| --- | --- |
| 地址 | `ws://127.0.0.1:8765/ws`（端口以软电话配置为准）。配置页「网页接入」可勾选 **允许远程连接**（`ws.allowRemote`，默认关）：勾选后监听 `0.0.0.0`、其他 IP 也能连，见下方「安全说明」 |
| 鉴权 | 无（连接即可用），安全性由「仅监听回环地址 + 拒绝非回环连接」保证 |
| 安全 | 服务仅绑定 127.0.0.1，且拒绝非回环来源连接 |
| 心跳 | 客户端 60 秒内无消息会被断开；可定期发送任意非请求消息保活 |

软电话同时提供 HTTP 服务：

- `GET http://127.0.0.1:8765/` —— **网页控制台**（开箱即用的控制界面：通话 / 通话记录 / 通信录 / 配置 / 诊断五个页面）
- `GET http://127.0.0.1:8765/sdk/sip-phone-sdk.js` —— JS SDK
- `GET /css/app.css`、`GET /js/*.js` —— 控制台的样式与脚本（白名单提供，一律 `no-cache`）
- `/index.html`、`/history`、`/contacts`、`/settings`、`/diag`、`/test.html` 都返回控制台（hash 路由 `#/call`、`#/history`、`#/contacts`、`#/settings`、`#/diag`）

> **仅托盘无 UI 版**（产物 `mti-sip-phone-no-ui`）只有托盘、没有窗口，上面这套网页就是它的唯一界面：左键点托盘图标即用系统默认浏览器打开控制台。协议与完整版**完全一致**。

## 2. 消息格式

所有消息均为 JSON 文本帧。

### 2.1 请求（客户端 → 软电话）

```json
{ "id": 1, "method": "call.dial", "params": { "target": "1001" } }
```

`id` 为客户端自定义值（数字或字符串），用于关联响应。

### 2.2 响应（软电话 → 客户端）

```json
{ "id": 1, "type": "response", "ok": true, "data": { "callId": "abc" } }
```

失败时：

```json
{ "id": 1, "type": "response", "ok": false, "error": { "code": "call_not_found", "message": "通话不存在" } }
```

### 2.3 事件（软电话 → 客户端）

```json
{ "type": "event", "event": "call.incoming", "data": { "call": { ... }, "callId": "abc" } }
```

事件无 `id`，所有已连接客户端都会收到。

## 3. 请求方法

| 方法 | 参数 | 成功返回 | 说明 |
| --- | --- | --- | --- |
| `call.dial` | `{ "target": "1001", "video": false, "name": "" }` | `{ "callId": "..." }` | 拨号；target 为号码或 SIP URI；`video: true` 发起视频呼叫（REQ-VIDEO-001）；`name`（可选）= **对端显示名**（如在线座席列表的 `displayName`），呼叫期间**优先于通信录名称**显示，为空时行为不变 |
| `call.answer` | `{ "callId": "...", "video": false }` | `{ "callId": "..." }` | 接听；`video: true` 视频接听——打开摄像头双向视频（REQ-VIDEO-004） |
| `call.hangup` | `{ "callId": "..." }` | `{ "callId": "..." }` | 挂断 |
| `call.reject` | `{ "callId": "...", "code": 486 }` | `{ "callId": "..." }` | 拒接；code 为 SIP 状态码，默认 486 |
| `call.mute` | `{ "callId": "...", "muted": true }` | `{ "callId": "..." }` | 保持（静音）切换（仅通话建立后有效） |
| `call.muteMic` | `{ "callId": "...", "muted": true }` | `{ "callId": "..." }` | 麦克风开关（纯本地动作，不发 SIP 信令）；状态变化推 `call.audio`（REQ-UI-023） |
| `call.muteSpeaker` | `{ "callId": "...", "muted": true }` | `{ "callId": "..." }` | 扬声器开关（纯本地动作，继续接收）；状态变化推 `call.audio` |
| `call.setMicGain` | `{ "callId": "...", "gain": 150 }` | `{ "callId": "..." }` | 麦克风增益（REQ-MEDIA-006，纯本地动作）：`gain` 为 **0–200 的百分比整数**，**0 = 禁用**（等同把麦克风开关关掉），大于 0 时同时解除禁用；与桌面通话面板的滚轮是同一份状态，变更推 `call.audio`。越界或缺失返回 `invalid_params` |
| `call.setSpeakerVolume` | `{ "callId": "...", "speakerVolume": 80 }` | `{ "callId": "..." }` | 扬声器音量（REQ-MEDIA-006，纯本地动作）：`speakerVolume` 为 **0–100 的百分比整数**，**0 = 禁用**，大于 0 时同时解除禁用；变更推 `call.audio` |
| `call.setVideo` | `{ "callId": "...", "enabled": true }` | `{ "callId": "..." }` | 视频总开关（纯本地动作，REQ-VIDEO-008）：关 = 不显示远端、不推送本地、视频窗口关闭；状态变化推 `call.video` |
| `call.dtmf` | `{ "callId": "...", "digit": "5" }` | `{ "callId": "..." }` | 发送 DTMF（RFC 4733 telephone-event，REQ-CALL-006）：`digit` 为单个字符 `0-9 * # A-D`，通话接通后才有效；参数缺失或 `digit` 不是单字符返回 `invalid_params` |
| `system.getState` | `{}` | 见下 | 查询当前状态（版本、平台、注册、通话、更新状态、**自动更新配置 updateConfig**、**官方更新站点 updatePrimarySite**、自动接听开关） |
| `contact.export` | `{}` | `{ "csv": "...", "count": 12 }` | 导出通信录 CSV 文本（REQ-WS-015）：表头 `号码,名称,备注,常用`，**UTF-8 带 BOM**（Excel 直接打开不乱码）。网页端的增删改查见 `contact.list/save/delete/setStarred/clear`（REQ-WS-018） |
| `contact.import` | `{ "csv": "号码,名称,备注,常用\n2999,张三,前台,是\n" }` | `{ "added": 1, "updated": 0, "skipped": 0, "total": 12 }` | 导入通信录 CSV：按号码去重，**已存在的只覆盖号码/名称/备注，常用标志只增不减**（本地已标就保持、本地没标而 CSV 标了则变常用），不存在的整条新增；没有号码或整行解析失败的行计入 `skipped`。参数缺失返回 `invalid_params`，通信录不可用返回 `contacts_unavailable`。单条消息上限 4MB |
| `calllog.query` | `{ "remote": "1001", "limit": 50, "offset": 0 }` | `{ "records": [...], "total": 357, "hasMore": true }` | 分页查询通话记录（REQ-WS-017），**新的在前**；`remote` 按号码子串筛选，`limit` 默认 50、上限 200（越界钳制）。时间戳一律**毫秒整数**，`0` 表示未发生（`answeredAt: 0` = 未接通，`endedAt: 0` = 进行中）；`displayName` 是**座席显示名**（在线座席列表直接呼出时记录，页面展示时优先于通信录名称）。每条记录在号码命中通信录时带 `contact: {name, note, starred}`（口径同桌面通话记录页：号码规范化后查 core 的内存快照），页面据此显示「号码 名称★」与备注悬停。数据库未接入返回 `calllog_unavailable` |
| `calllog.delete` | `{ "callIds": ["abc", "def"] }` | `{ "deleted": 2 }` | 按 callId 删除若干条通话记录；空数组返回 `invalid_params`。成功后广播 `calllog.changed` |
| `calllog.clear` | `{}` | `{ "deleted": 357 }` | 清空全部通话记录（不可撤销）。成功后广播 `calllog.changed` |
| `contact.list` | `{ "query": "zs" }`（可选） | `{ "count": 12, "total": 12, "contacts": [{ "number", "name", "note", "starred" }] }` | 列出联系人（REQ-WS-018）。给了 `query` 就**在软电话侧过滤**：支持**音头拼音 / 全拼 / 汉字混合**（`zs`/`zhangsan`/`张三`/`张san` 都能命中「张三」）、号码与备注的子串、号码的纯数字写法（`138` 命中 `+86 138-0000-0000`）——与桌面通信录页是**同一份实现**（`internal/contactsearch`，网页端没有拼音字典）。结果按**拼音排序**（空名称最前，同音再按规范化号码）；`count` 是本次返回条数、`total` 是通信录总数 |
| `contact.save` | `{ "prevNumber": "1001", "number": "1002", "name": "张三", "note": "", "starred": true }` | `{ "contact": {...}, "count": 12 }` | 新增或修改联系人：`prevNumber` 非空 = 改号（传改号前的号码），`starred` 缺省 = 保持原状（新增时为 false）。号码为空/重复返回 `invalid_params`；返回的是规范化后的生效值（号码归一、名称/备注截断） |
| `contact.delete` | `{ "numbers": ["1001", "sip:1002@host"] }` | `{ "deleted": 2, "count": 10 }` | 按号码（任意写法，见「去重键」）删除 |
| `contact.setStarred` | `{ "numbers": ["1001"], "starred": true }` | `{ "updated": 1, "count": 12 }` | 批量设置/取消常用（★） |
| `contact.clear` | `{}` | `{ "deleted": 12, "count": 0 }` | 清空通信录（不可撤销） |
| `audio.devices` | `{}` | `{ "mics": [{"id","name"}], "speakers": [...], "ready": true, "preprocessSupported": true }` | 枚举音频设备（REQ-WS-016）。**异步回包**：软电话侧单飞 + 2 秒缓存 + 3 秒超时；超时先回空列表 + `ready: false`（页面应提示"枚举中"而不是当成没有设备） |
| `video.devices` | `{}` | `{ "cameras": [{"id","name"}], "supported": true, "cameraSupported": true }` | 枚举摄像头（`cameraSupported: false` = 本平台没有采集后端，只能接不能发） |
| `system.getConfig` | `{}` | 见下 | 配置快照（REQ-WS-019）：账户（**不含密码**，只有 `passwordSet`）、音频、视频、日志、`ws.port` 与 `ws.actualPort`、自动更新、开机自启 |
| `system.setAccount` | account 字段子集 + `"password": "..."` / `"clearPassword": true` | 生效后的账户快照（不含密码） | 修改 SIP 账户：**只改传入的字段**；密码**只写不读**——传 `password` 改密码、传 `clearPassword: true` 清空，都不传则保持原密码。校验失败（端口越界、传输方式非法、`publicIP` 不是 IP 字面量、启用账户但缺服务器/账号…）返回 `invalid_params` 且**不落盘不改内存**。成功后立即按新配置重新注册 |
| `system.setAudioConfig` | `{ "micId": "...", "speakerId": "...", "ringSpeakerId": "...", "aec": true, "ns": true, "agc": false }` | 生效后的音频配置（含 `ready` / `preprocessSupported`） | 修改音频配置（字段可选）：设备选择**立即生效**，AEC/NS/AGC（REQ-MEDIA-007）从**下一通电话**生效 |
| `system.setVideoConfig` | `{ "enabled": true, "cameraId": "", "cameraName": "...", "width": 640, "height": 480, "fps": 25, "bitrateKbps": 512 }` | 生效后的视频配置（含 `supported` / `cameraSupported` / `cameraDisabled`） | 修改视频配置（字段可选）：`enabled`（总开关）与摄像头**立即生效**，采集参数从**下一通电话**生效。宽高 64–1920、帧率 1–30、码率 64–20480，越界返回 `invalid_params` |
| `system.setLogConfig` | `{ "level": "debug", "dir": "", "file": true, "console": false, "sipTrace": false }` | 生效后的日志配置 | 修改日志配置（字段可选）：级别与 SIP 信令记录**立即生效**，写文件/控制台**重启后生效**；级别非 debug/info/warn/error 返回 `invalid_params`。注：`console`（把日志另写一份到 stderr）**桌面与网页界面都不提供**（GUI 子系统构建没有控制台，看不出效果），只有控制台版/终端里跑时才有意义；界面不传它，配置值保持不变 |
| `system.setWSPort` | `{ "port": 8801 }` | `{ "port": 8801, "actualPort": 8765, "restartRequired": true }` | 修改本地 WS 端口（**重启后生效**）；`actualPort` 是当前进程实际监听的端口（启动时被占用会自动 +1）。配合 `system.restart` |
| `system.setAutoStart` | `{ "enabled": true }` | `{ "enabled": true }` | 开/关开机自启（立即注册/移除系统项）。沙箱实例（`-instance N`）不支持，返回 `config_unavailable` |
| `system.setUnregisterOnDisconnect` | `{ "enabled": true }` | `{ "enabled": true }` | **WS 断开自动取消注册**（REQ-WS-020）：应用级开关，默认关、不持久化（SDK 的 `unregisterOnDisconnect` 参数每次连上后下发，最后一次设置生效）。开启后，**所有** WS 连接断开并超过 3 秒仍未回来 → 自动取消 SIP 注册（`system.setAccount` 的 enabled 语义，等同关闭「启用账户」）；**呼叫/振铃/通话中**自动**排队**，等话务结束再注销；等待期间有连接回来（重连）→ **取消**排队任务，并恢复已被自动取消的注册（重新注册）。页面刷新/瞬时断网因 3 秒延迟窗口不触发 |
| `system.restart` | `{}` | `{ "restarting": true }` | 重启软电话程序。⚠️ **先回响应再重启**：resolve 之后进程随时退出、WS 断开 |
| `system.checkUpdateNow` | `{}` | `{ "skipped": false, "upToDate": true, "latestVersion": "", "notified": false, "reason": "", "text": "已是最新版本", "currentVersion": "1.0.15" }` | 立即检查一次更新站点（不等下一个周期，20 秒超时） |
| `update.status` | `{}` | 更新状态对象（同 `system.update` 事件负载） | 查询更新状态（页面一般用事件，这里供初始化） |
| `update.start` | `{}` | 更新状态对象 | **开始下载**已告知的新版本（REQ-UPD-020，进度走 `system.update` 事件） |
| `update.cancel` | `{}` | 更新状态对象 | 取消下载 |
| `update.apply` | `{}` | `{ "phase": "ready", "applying": true }` | 安装已下载好的更新并重启。要求当前 `phase === "ready"`，否则返回 `invalid_params`；⚠️ **先回响应再安装**（安装会替换 exe 并重启） |
| `call.setVideoPreview` | `{ "callId": "...", "enabled": true }` | `{ "callId": "..." }` | 本地视频回显开关（纯本地动作，REQ-VIDEO-009）：无 UI 版没有回显窗口，此方法与桌面口径保持一致；网页看本地画面用 `video.subscribe` 的 `local` |
| `system.setAutoAnswer` | `{ "enabled": true }` | `{ "enabled": true }` | 自动接听开关（REQ-CALL-014）：与软电话界面上的「自动接听」按钮**共用一份状态**，变更后广播 `system.autoAnswer`，桌面按钮与网页据此同步。视频来电自动接听时按「视频接听」接通（REQ-VIDEO-004） |
| `system.register` | `{}` | `{ "enabled": true, "state": "registered" }` | **登录账号**（REQ-ACC-024 / REQ-WS-021）：与托盘菜单「登录账号」一致——**启用账户**并**落盘**，然后立即向服务器注册。返回只表示"已接受"（注册在后台进行，结果经 `register.state` 事件回显） |
| `system.unregister` | `{}` | `{ "enabled": false, "state": "unregistered" }` | **退出账号**（REQ-ACC-024 / REQ-WS-021）：发 expires=0、停注册周期，并**落盘禁用账户**——之后启动/断线重连都不再自动注册（直到 `system.register` 或 sip:// 拉起）。与托盘菜单「退出账号」一致 |
| `cti.action` | `{ "action": "takeover", "target": "2002" }`（带 target）/ `{ "action": "addConferenceMembers", "members": ["2002"] }`（带 members）/ `{ "action": "finishConsult" }`（无参数） | `{ "action": "takeover", "seq": "12" }`（带返回值的动作另有 `data`） | **CTI 反向控制指令**（REQ-ACC-026 / REQ-ACC-034）：经 out-of-dialog NOTIFY（`Event: MSP_ACTION`）发给 CTI，**等 `MSP_RESULT` 回来后同步回包**（最多约 3 秒）。动作与参数见下。失败：平台回 success=false → `cti_failed`（message 带平台错误码）；2 秒内无回执 → `cti_timeout`；已有指令在途 → `cti_busy`；非坐席模式 → `cti_not_agent`；动作/参数不合法 → `invalid_params`。结果同时以 `cti.result` 事件广播 |
| `cti.listAgents` | `{}` | `{ "agents": [{ "agent", "displayName", "acd", "agentMode", "agentStatus" }], "count": 3 }` | **在线座席列表**（REQ-ACC-035，CTI 动作 `listAgents`）：等 `MSP_RESULT` 后把 `data` 归一化成固定字段并按座席号排序。`agentStatus`：`free` / `dialing` / `ringing` / `talking` / `busy` / `leave` / `not_ready` / `free_offhook` / `miss_phone` / `offline`。**非坐席模式返回 `cti_not_agent`**。本机座席由页面自行过滤（与本机号码规范化后比较） |

**`cti.action` 的动作与参数**（与桌面通话面板/在线座席页的按钮一一对应）：

| 动作 | 额外参数 | 说明 |
| --- | --- | --- |
| `consult` | `target` | 发起咨询（被咨询方 = target） |
| `consultThreeWay` | `target`（可选，缺省用当前咨询） | 咨询转三方 |
| `transfer` | `target` | 直接转移 |
| `consultTransfer` | `target`（可选，缺省用当前咨询） | 咨询转移 |
| `monitor` | `target` | 监听坐席（班长模式） |
| `takeover` | `target` | 代接坐席的振铃来电 |
| `exitThreeWay` | `target` | 三方通话里强制挂断某一方（仅 owner） |
| `addConferenceMembers` | `members`（可为空数组） | 邀请加入会议；**空数组 = 直接把当前两方通话转成会议**（与桌面 UI 的「会议」按钮一致） |
| `removeConferenceMembers` | `members`（至少一个号码） | 从会议移除成员 |
| `muteConferenceMember` / `unmuteConferenceMember` | `member` | 会议成员禁音/取消禁音（owner） |
| `deafConferenceMember` / `undeafConferenceMember` | `member` | 会议成员禁听/取消禁听（owner） |
| `vmuteConferenceMember` / `unvmuteConferenceMember` | `member` | 会议成员禁视频/取消禁视频（owner） |
| `finishConsult` / `finishConference` | — | 结束咨询 / 结束会议（owner） |
| `whisper` / `intervene` / `intercept` | — | 监听耳语 / 插话（强插）/ 强拦 |
| `setBusy` / `setIdle` | — | 坐席置忙 / 置闲（结果经 `cti.agent` 事件回显） |

| `system.setUpdateConfig` | `{ "url": "https://update.example.com", "appId": "mti-sip-phone", "intervalMinutes": 5, "prerelease": false }` | 生效后的完整配置 `{url, appId, intervalMinutes, prerelease}` | 写入软电话的**自动更新配置**（REQ-UPD-018）：软电话据此自己定时查询更新站点（**url 留空 = 不检查**，周期 1–1440 分钟，prerelease 默认 false）。字段**都是可选的**，只改传入的那些；写入后落盘、立即生效（配了站点马上查一次）并广播 `system.updateConfig`。url 非 http/https 或周期越界返回 `invalid_params` |
| `system.notifyUpdate` | `{ "version": "1.1.0", "url": "http://dl.x/app.exe", "sha256": "…", "notes": "…", "os": "windows", "arch": "amd64", "force": false }` | `{ "phase": "available", "version": "1.1.0", "currentVersion": "1.0.11" }` | 告知软电话有新版本可用（自动更新，见 6.3）；`sha256`/`notes`/`os`/`arch` 可选；`force: true` = **强制推送**，跳过"版本必须更高"的检查（REQ-UPD-015，页面对着手动推送/回滚用；SDK 的定时查询不带它） |
| `video.subscribe` | `{ "remote": { "maxWidth": 640, "maxHeight": 480, "fps": 30, "quality": 75 }, "local": { "maxWidth": 160, "maxHeight": 120 }, "keepWindow": false }` | 见下 | 订阅视频画面（REQ-VIDEO-009）：**必定订阅远端画面**（`remote` 不传也有默认参数），`local` 可选——不传就是**不订阅本地回显**；两条通道各带各的参数（本地回显通常小一号）。**`keepWindow`**（默认 `false`）：订阅后是否仍**保持软电话的视频窗口显示**——不传/false 时软电话自动关闭视频窗口（画面在网页看），传 `true` 则窗口继续显示（两处同时看）。画面以二进制帧推送（见 4.1）。**视频通话被关闭时不建立订阅，直接返回 `video_unsupported`**（REQ-VIDEO-014） |
| `video.unsubscribe` | `{}` | `{ "local": false, "remote": false }` | 取消本连接的全部视频订阅（两条通道都已退订）；软电话恢复显示视频窗口 |
| `video.setTransform` | `{ "channel": "remote", "rotate": true }` / `{ "channel": "local", "mirror": true }` / `{ "channel": "remote", "rotation": 90 }` | `{ "remote": { "rotation": 90, "mirror": false }, "local": { "rotation": 0, "mirror": false } }` | 画面旋转/水平镜像（REQ-VIDEO-012）：`rotate: true` = 顺时针 +90°，`rotation` 直接设角度，`mirror` 设水平镜像；`channel` 为 `remote`（远端画面，可省）或 `local`（本地回显）。**状态存在软电话侧、桌面与网页共用一份**，谁改都广播 `video.transform` |

### system.getState 返回示例

```json
{
  "version": "1.0.11",
  "os": "windows",
  "arch": "amd64",
  "register": { "state": "registered", "account": "1001", "reason": "" },
  "calls": [
    {
      "id": "demo-123",
      "direction": "outgoing",
      "remote": "1002",
      "remoteName": "",
      "state": "established",
      "muted": false
    }
  ],
  "update": {
    "phase": "available",
    "info": { "version": "1.1.0", "url": "http://dl.x/app.exe" },
    "host": "dl.x",
    "progress": 0
  },
  "video": {
    "remote": { "rotation": 90, "mirror": false },
    "local": { "rotation": 0, "mirror": true }
  },
  "autoAnswer": false,
  "updateConfig": { "url": "https://update.wuzm219.cn", "appId": "mti-sip-phone", "intervalMinutes": 5, "prerelease": false },
  "updatePrimarySite": "https://update.wuzm219.cn",
  "recentDial": ["1001", "1002"],
  "noUI": false,
  "instance": 0
}
```

- `version` / `os` / `arch`：软电话版本号与编译目标平台/架构——前端据此到业务平台查询该架构下是否有更高版本。
- `update`：自动更新状态，无可用更新时省略（字段说明见 4. 事件 `system.update`）。
- `video`：画面旋转/水平镜像（REQ-VIDEO-012），远端与本地回显各一份；网页连上（含断线重连）即可据此把按钮和渲染状态对齐。
- `autoAnswer`：自动接听开关当前状态（与软电话界面的按钮共用一份，可用 `system.setAutoAnswer` 修改）。
- `recentDial`：最近呼叫的号码（最多 5 个、新的在前），供拨号下拉用。
- `noUI`：当前进程是「仅托盘无 UI 版」（REQ-UI-041）时为 true —— 页面据此自动订阅视频、隐藏「本地画面 / 保留软电话窗口」两个勾选项、不允许取消订阅。
- `instance`：本进程的实例槽位（0 = 主实例，≥1 = 沙箱实例，REQ-NFR-007）；诊断页拼 `sip://` 拉起链接时要带上 `?instance=N`。
- `updateConfig`：自动更新配置（`url`/`appId`/`intervalMinutes`/`prerelease`，REQ-UPD-017/018），网页连上就能拿到当前值，可用 `system.setUpdateConfig` 改写。
- `updatePrimarySite`：官方更新站点（REQ-UPD-023），网页配置页「使用主站点」按钮一键填入用——网页不硬编码、由状态快照下发。

### 通信录 HTTP 接口（网页导入导出）

**JSON（REST，REQ-CONTACT-005）** —— 给程序用，网页可以直接 XHR 收发结构化数据：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/contacts` | 返回 `{ "count": N, "contacts": [ {"number","name","note","starred"}, … ] }`（`application/json`） |
| `POST` | `/api/contacts` | 请求体是 **裸数组** `[{...}]` **或**上面那种带 `contacts` 字段的对象（导出→改→导入可直接回灌）；返回 `{added, updated, skipped, total}`。JSON 语法/格式错误返回 **400** + `{"error": "..."}`；请求体上限 8MB |

**CSV**（一键下载/上传，适合人工编辑与表格软件）：


SDK 的 `downloadContacts()` / `importContactsFromFile()` 走这两个 **HTTP** 端点（不是 WS）——CSV 是整份文本，走 HTTP 没有单条消息大小限制，浏览器也能直接流式下载/上传：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/contacts.csv` | 导出通信录 CSV（`text/csv`，带 `Content-Disposition` 文件名与 `X-Contact-Count` 条数头；跨域读取该头需 `Access-Control-Expose-Headers`） |
| `POST` | `/api/contacts/import` | 导入：**请求体就是 CSV 文本**（`Content-Type: text/csv`），返回 `{ "added", "updated", "skipped", "total" }`，失败返回 503 + `{"error": "..."}`；请求体上限 8MB |

两个端点与 WS 一致：**只接受本机回环来源**，无需鉴权；导入的合并语义（按号码去重、常用标志只增不减）与 `contact.import` 完全相同。

### contact.import 说明

- **去重键是"规范化号码"**：`sip:` 前缀、尖括号、`;user=phone`、`@host`、空格与 `-()[]_./` 分隔符都会被归一，所以 `2999` 与 `sip:2999@10.168.4.40:5060` 是同一条。
- **常用标志只增不减**：本地已标常用就保持（文件里的"否"冲不掉它），本地没标而文件标了"是"则变成常用。
- 解析容忍 BOM、无表头（按列序）、表头列序不同、列数不齐、空行与不规范引号；`常用` 列接受 `是/否/1/0/true/false/yes/no`。
- 单条 WS 消息读上限为 **4MB**（框架默认 32KB 不够导入通信录，超过会被直接断开连接）。

### system.notifyUpdate 说明

由**业务前端**调用的**强制推送**通道（REQ-UPD-019）：把某个版本告知软电话，软电话弹提示、由用户在界面上确认后下载替换重启（REQ-UPD-009 的**旧口径**）。⚠️ 1.0.15 起 `notifyUpdate` 仍只是"告知"，但**下载与安装已另有通道**：`update.start` / `update.apply`（REQ-UPD-020），仍必须由用户在可控界面（桌面提示窗或网页按钮）上点击，不做静默安装。

- `version` 与 `url` 必填；`sha256`/`notes`/`os`/`arch` 可选（os/arch 提供时须与软电话自身一致）。
- 版本不高于当前返回 `update_not_newer`；参数非法返回 `invalid_params`；正在下载/已就绪时再来一条返回 `internal`。
- 同版本重复告知幂等（已在下载/就绪的同版本直接忽略）。
- 「有没有新版本」**不在网页侧裁决**：软电话自己按 `system.setUpdateConfig` 的配置定时查询更新站点（REQ-UPD-017）；本方法用于页面手动推送（联调/回滚），SDK 的 `notifyUpdate` 恒带 `force`。

### system.getConfig 返回示例

```json
{
  "version": "1.0.15",
  "os": "windows",
  "arch": "amd64",
  "account": {
    "enabled": true, "server": "10.168.4.40", "port": 5060, "expires": 300,
    "rport": true, "stunEnabled": false, "stunServer": "stun.l.google.com:19302",
    "proxy": "", "transport": "tls", "tlsVerify": false, "srtp": "supported",
    "domain": "", "publicIP": "", "account": "2999", "authUser": "", "displayName": "",
    "passwordSet": true
  },
  "audio": { "micId": "", "speakerId": "", "ringSpeakerId": "", "aec": true, "ns": true, "agc": false, "ready": true, "preprocessSupported": true },
  "video": { "enabled": true, "disabled": false, "cameraId": "", "cameraName": "", "width": 640, "height": 480, "fps": 25, "bitrateKbps": 512, "supported": true, "cameraSupported": true, "cameraDisabled": "disabled" },
  "log": { "level": "info", "dir": "C:/Users/x/AppData/Roaming/mti-sip-phone/logs", "file": true, "console": false, "sipTrace": false },
  "ws": { "port": 8765, "actualPort": 8765 },
  "update": { "url": "", "appId": "mti-sip-phone", "intervalMinutes": 5, "prerelease": false },
  "autoStart": true
}
```

- **密码永远不下发**：只有 `passwordSet` 布尔值；写入见 `system.setAccount` 的 `password` / `clearPassword`。
- `audio.ready` / `preprocessSupported`、`video.supported` / `cameraSupported` 反映"本构建/本平台有什么"（无视频构建、无采集后端的平台据此把控件置灰）。
- `ws.actualPort` 是当前进程实际监听的端口，`ws.port` 是配置里的期望值（启动时被占用会自动 +1 并写回配置）。
- 与桌面口径一致：设备/总开关/日志级别立即生效，采集参数与音频预处理**下一通电话生效**，写文件/控制台与 WS 端口**重启后生效**。

### video.subscribe 返回示例

```json
{
  "remote": { "maxWidth": 640, "maxHeight": 480, "fps": 30, "quality": 75 },
  "local": { "maxWidth": 160, "maxHeight": 120, "fps": 10, "quality": 75 },
  "keepWindow": false,
  "call": { "id": "abc", "hasVideo": true, "video": { "negotiated": true, "sending": true, "width": 640, "height": 480 } }
}
```

- 请求里 `remote` / `local` 都是**参数对象、可缺字段**：`{}` 或整个缺省都表示"用默认值"。
- 返回的是**生效参数**（每条通道一份）：越界值会被钳制到合法范围（`maxWidth`/`maxHeight` ∈ [64, 1920]，`fps` ∈ [1, 30]，`quality` ∈ [30, 95]），不报错。
- **远端恒订阅**（不传 `remote` 也照样订阅，否则会出现"桌面窗口被隐藏、远端画面却没人看"的糊涂状态）；`local` 不传 = 不订阅本地回显，此时返回里没有 `local` 字段。
- **`keepWindow`**：默认为 `false`——订阅后软电话**关闭视频窗口**（画面在网页看）；传 `true` 时窗口**继续显示**。多页面同时订阅时按"任一订阅者没要求保持就隐藏"汇总（见 `video.web` 的 `hideWindow`）。
- `call` 为当前进行中的通话快照（无通话时为 `null`）。
- 订阅是**连接级**的：连接断开自动退订；订阅一直有效（跨通话），通话开始后画面自动推来。

## 4. 事件

| 事件 | 负载 | 说明 |
| --- | --- | --- |
| `system.state` | 与 getState 返回相同 | 连接建立时推送状态快照 |
| `call.dialing` | `{ "callId", "call" }` | 呼出已发起（本地拨号按钮与 WS 拨号共用） |
| `call.incoming` | `{ "callId", "call" }` | 来电振铃；`call.hasVideo` 为 true 表示对端带视频。**`call.contact`**（`{name, note, starred}`）在号码命中通信录时出现，页面可据此在号码旁显示名称、★ 与备注提示（REQ-UI-037） |
| `call.ringing` | `{ "callId", "call", "earlyMedia" }` | 呼出已振铃（180/183）；`earlyMedia: true` 表示本次振铃带**早期媒体**（183+SDP，回铃音由对端下发，软电话不再放本地回铃音） |
| `call.established` | `{ "callId", "call" }` | 通话建立 |
| `call.terminated` | `{ "callId", "call", "reason", "missed" }` | 通话结束；reason：`local_hangup`（本端挂断）/ `remote_hangup`（对端挂断）/ `remote_cancel`（振铃中收到 CANCEL）/ `rejected`（本端拒接）。**`missed: true` = 未接来电**（呼入、从未接通、且非本端当场处理——本端拒接/振铃中挂断不算），桌面端据此弹系统通知（REQ-UI-010） |
| `call.failed` | `{ "callId", "call", "reason", "missed" }` | 呼叫失败；reason：`busy` / `no_answer` / `not_found` / `failed`（协商或媒体失败）等；`missed` 含义同上 |
| `call.muted` | `{ "callId", "muted" }` | 保持（静音）状态变化 |
| `call.audio` | `{ "callId", "call" }` | 麦克风/扬声器开关与音量变化（`call.micMuted` / `call.speakerMuted` / `call.micGain` / `call.speakerVolume`，REQ-UI-023 / REQ-MEDIA-006）。**连续调节（桌面滚轮、网页拖滑块）时事件会被合并**：最多 150ms 一条（首次立即发、最后一次必发）——页面不要按"一条事件 = 一步"来计数 |
| `call.video` | `{ "callId", "call" }` | 视频状态变化（协商成功/开关切换/拿到对端分辨率）；状态见 `call.video` |
| `video.web` | `{ "active", "local", "remote", "viewers", "hideWindow" }` | 网页视频订阅状态变化（任一连接订阅/退订时广播）。`active: true` 表示有网页在看画面；`hideWindow: true` 表示软电话**不显示视频窗口**（任一订阅者没要求 `keepWindow` 就为真，全部要求保持时才为假） |
| `video.transform` | `{ "remote": { "rotation", "mirror" }, "local": { "rotation", "mirror" } }` | 画面旋转/水平镜像变化（桌面视频窗口或网页任一侧改动都会广播）；重连时也可从 `system.state` 的 `video` 字段取当前值 |
| `register.state` | `{ "state", "account", "reason" }` | 注册状态变化 |
| `system.updateConfig` | `{ "url", "appId", "intervalMinutes", "prerelease" }` | 自动更新配置变化（本机配置页保存或网页 `system.setUpdateConfig` 写入都会广播，REQ-UPD-018）；重连时也可从 `system.state` 的 `updateConfig` 字段取当前值 |
| `system.autoAnswer` | `{ "enabled": true }` | 自动接听开关变化（桌面按钮点击或网页 `system.setAutoAnswer` 设置都会广播）；重连时也可从 `system.state` 的 `autoAnswer` 字段取当前值 |
| `system.update` | 更新状态对象，见下 | 自动更新状态变化（受理/下载进度/就绪/失败） |
| `contact.changed` | `{ "action": "save|delete|star|clear|import", "total": 12 }` | 通信录发生写入（REQ-WS-018）：**桌面界面、网页、CSV/JSON 导入所有写入路径都广播**，多标签与桌面列表据此刷新 |
| `calllog.changed` | `{ "action": "delete|clear", "deleted": 2 }` | 通话记录被删除/清空（REQ-WS-017）；**新增记录**仍由 `call.terminated` / `call.failed` 驱动刷新 |
| `system.config` | `{ "section": "account|audio|video|log|ws|autoStart", "values": {...} }` | 配置被改写（REQ-WS-019）：本机配置页保存或网页 `system.set*` 写入都会广播，其它网页据此同步表单 |
| `audio.ready` | `{ "ready": true }` | 音频后端后台初始化完成（REQ-WS-016）：网页据此刷新设备列表（无 UI 版没有桌面界面，这是唯一的信号） |
| `cti.agent` | `{ "online", "displayName", "agentMode", "isBusy" }` | **坐席模式/置忙变化**（REQ-ACC-028 / REQ-ACC-034）：`MSP_EVENT_AGENT_ONLINE/OFFLINE` 与网页/桌面的 `setBusy`/`setIdle` 结果都经这里回显；`agentMode` 为 `none`/`normal`/`dial_out`/`monitor`，`agentMode=none` 时 `online=false`。重连时也可从 `system.state` 的 `agentOnline`/`agentDisplayName`/`agentMode`/`agentBusy` 取当前值 |
| `cti.state` | `{ "callId", "call" }` | **通话的复杂话务状态变化**（REQ-ACC-026）：`MSP_EVENT_*`（咨询/三方/监听/会议）翻译后的完整通话快照，`call.cti` 是 `CTIState`（`talkState`：`twoParty`/`consult`/`threeWay`/`monitor`/`conference`，其余字段见协议 §16 与 `docs/cti-sip-phone-protocol.md`） |
| `cti.result` | `{ "seq", "success", "code", "message", "action", "data"? }` | **CTI 指令结果**（MSP_RESULT）：与 `cti.action` 的同步回包等价（桌面界面按 `seq` 对齐，网页/SDK 可直接用 `cti.action` 的 Promise）。超时结果也会走这里（`code: "TIMEOUT"`） |

`call` 对象中的视频相关字段（REQ-VIDEO-*）：

| 字段 | 说明 |
| --- | --- |
| `hasVideo` | 本次通话涉及视频（呼入时对端 offer 含视频段；呼出时本端发起了视频呼叫） |
| `video.negotiated` | 视频已协商成功 |
| `video.enabled` | 视频总开关（对应通话面板的「关闭视频」） |
| `video.previewOn` | 本地回显开关（**只影响本地显示，不影响推送到远端**） |
| `video.sending` | 本次通话要推送本地视频（意图，不随回显开关变化） |
| `video.width` / `video.height` | 对端视频分辨率（未收到帧时为 0） |

更新状态对象字段：

| 字段 | 说明 |
| --- | --- |
| `phase` | `idle` / `available`（已受理，等用户确认）/ `downloading` / `ready`（已下载待重启）/ `failed` |
| `info` | 新版本信息：`version`、`url`（以及可选的 `sha256`/`notes`） |
| `host` | 下载地址主机名（界面展示来源） |
| `downloaded` / `total` | 已下载/总字节数（`total` 为 0 表示服务器未提供长度） |
| `progress` | 下载进度 0-100 |
| `waitingCall` | 已下载完成，等通话结束后自动重启安装 |
| `error` | 失败原因（中文，面向用户展示） |

通话对象（`call`）字段：`id`、`direction`（incoming/outgoing）、`remote`、`remoteName`、`peerName`（INVITE 的 Call-Info display-name，可选；显示名称时优先于通信录）、`consult`（本通是服务端推来的咨询腿，可选）、`state`（ringing/established/terminated）、`muted`（保持）、`micMuted`、`speakerMuted`、`micGain`（0–200 的百分比）、`speakerVolume`（0–100 的百分比）、`hasVideo`、`video`（见上）。

`micGain` / `speakerVolume` 与开关的关系（REQ-MEDIA-006）：**0 就是禁用**；禁用期间这两个字段保留"恢复值"（解禁后回到的音量），当前是否禁用看 `micMuted` / `speakerMuted`。新通话默认都是 100。

### 4.1 视频画面帧（二进制消息）

订阅后画面以 **WS 二进制消息**逐帧推送（JPEG 逐帧，MJPEG 式），不套 JSON：

```
偏移 0:   通道（1 = 远端画面，2 = 本地画面）
偏移 1-4: 序号（uint32 大端，逐帧自增，客户端可据此发现丢帧/乱序）
偏移 5..: JPEG 数据（image/jpeg）
```

- 只有**订阅的通道**会推：`local: false` 时不会收到本地画面帧。
- 编码参数按订阅参数：大于 `maxWidth`/`maxHeight` 时等比缩放；`fps` 为上限（软电话侧按编码速度丢帧，不排队）；`quality` 为 JPEG 质量。
- 画面帧**不进事件日志**：帧率可达 30fps，混在事件流里会刷屏；画面状态变化仍走 `call.video`。
- 软电话侧永远只编码「最新一帧」（编码跟不上就丢旧帧），延迟不累积。

## 5. 错误码

| 错误码 | 含义 |
| --- | --- |
| `unknown_method` | 未知方法 |
| `invalid_params` | 参数缺失或非法 |
| `not_ready` | 电话能力尚未接入（SIP 层未启动） |
| `call_not_found` | 通话不存在 |
| `video_unsupported` | 视频功能不可用：本构建没有视频，或用户在配置页关闭了「启用视频通话」总开关（REQ-VIDEO-014）。`video.subscribe`、`call.dial{video:true}`、`call.answer{video:true}` 会返回它 |
| `update_not_newer` | 告知的版本不高于软电话当前版本（`system.notifyUpdate`） |
| `calllog_unavailable` | 通话记录不可用（数据库未接入或打开失败） |
| `audio_unavailable` | 音频能力不可用 |
| `config_unavailable` | 该配置项依赖的外部能力未接入（重启 / 开机自启 / 信令开关；沙箱实例的 `system.setAutoStart` 即此） |
| `update_unavailable` | 更新能力未接入 |
| `cti_busy` | 已有一条 CTI 指令在途（同一时间只允许一条，REQ-ACC-026） |
| `cti_timeout` | CTI 指令超时（未等到 `MSP_RESULT`） |
| `cti_failed` | CTI 平台回了失败结果，`message` 里带平台错误信息与错误码 |
| `cti_not_agent` | 当前不在坐席模式，话务动作/在线座席列表无从发起 |
| `internal` | 内部错误 |

## 6. 典型时序

### 6.1 呼出

```
客户端                   软电话                  对方
  |-- call.dial(1001) --> |                       |
  |<- 响应 {callId} ------|                       |
  |                       |------- INVITE ------->|
  |<- call.ringing -------|<------ 180/183 -------|
  |<- call.established ---|<------ 200 OK --------|
  |<- call.terminated ----|<-------- BYE ---------|
  |                       |（reason: remote_hangup）|
```

### 6.2 呼入

```
对方                    软电话                  客户端
  |------- INVITE ------->|                       |
  |                       |<- call.incoming ------|
  |                       |<-- call.answer -------|
  |<------ 200 OK --------|                       |
  |                       |<- call.established ---|
```

### 6.3 自动更新（软电话自己定时查站点 → 提示用户 → 用户确认后更新）

```
软电话                     更新站点                    网页（可选）
  |-- GET /check(version/os/arch) -->|                    |
  |<-- 有更高版本 + 下载地址/sha256 --|                    |
  |    （启动即查一次，之后按配置的周期）                    |
  |-- system.updateConfig 事件广播 ----------------------->|（配置变化时同步）
  |<-- system.setUpdateConfig ---------------------------- |（网页写入配置，REQ-UPD-018）
  |<-- system.notifyUpdate（强制推送，可选）---------------|（业务前端手动推送）
  |== 右下角提示窗 + 主窗体提示行（等待用户点击）==          |
  |-- system.update 事件（available/downloading/ready）--->|
  |== 用户点「立即更新」→ 下载 → 校验 → 替换 → 重启 ==      |
```

查询由**软电话自己**完成（REQ-UPD-017）：配置页可设更新站点（留空 = 不检查）、应用 ID、
检查周期（默认 5 分钟）与是否接受预览版；网页也可用 `system.setUpdateConfig` 写这份配置
（业务前端统一管理更新策略）。站点接口与字段见《自动更新与更新站点接入》的 REST API
（`GET /api/v1/apps/{app}/check`，**不带通道参数**）。

判定权在站点（只看 `updateAvailable`），软电话侧只做"不降级"的兜底比较；
**同一版本 + 同一 sha256 只提示一次**。软电话仍遵守 REQ-UPD-009：
**WS 只能告知，下载与重启必须由用户在软电话界面上点击**。
## 7. 安全说明

- **默认**只监听 127.0.0.1 且拒绝非回环来源连接；连接不做鉴权（已按用户要求取消 token）——
  即**本机任何进程或网页均可控制软电话**，安全性完全依赖回环监听。
- **可选放开**：配置页「网页接入」的 **`ws.allowRemote`「允许远程连接」**（默认**关**）勾选后，
  服务改为监听 **0.0.0.0** 并**不再拒绝非回环来源**，其他 IP 的浏览器也能控制本机软电话
  （拨号/接听/挂断、通话记录与通信录的读写、配置修改与重启，全部开放）。
  ⚠️ **仍然没有鉴权**：等于把这个控制权交给整个可路由网段。桌面配置页与网页配置页都写明了风险，
  网页勾选时会二次确认，软电话启动时会打一条 WARN 日志。绑定地址在**启动时**决定，改完要重启生效。
- 内网/办公网里临时要用可以开；公网可达的环境**不要开**（想远程用，走 SSH/VPN 隧道更稳妥）。
- 更新相关：`system.notifyUpdate`（强制推送）与 `system.setUpdateConfig` 都只能"告知/配置"，**不能触发下载或重启**——下载必须由用户在软电话界面上点击，且提示窗会显示下载地址的主机名供用户判断来源。软电话自己查站点只读不写，不携带任何凭据。
- **安全边界按现状固定**（增强方案**待定**）：只做「仅监听 `127.0.0.1` + 拒绝非回环来源」这一层，**不加额外鉴权**；来源端口白名单、token 鉴权等更强隔离已讨论，暂缓实施。部署时请按此前提使用（不要在有不可信本地程序/网页的机器上无人值守运行）。
