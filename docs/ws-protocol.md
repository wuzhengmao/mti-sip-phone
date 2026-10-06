# 本地 WebSocket 控制协议

> 适用版本：1.0.11。网页通过本机 WebSocket 连接 SIP 软电话，实现拨号、接听、挂断等控制，并接收振铃、挂断等事件。
>
> 本文档面向网页前端开发者；JS SDK 已封装本协议，通常无需直接读写报文。

## 1. 连接

| 项 | 说明 |
| --- | --- |
| 地址 | `ws://127.0.0.1:8765/ws`（端口以软电话配置为准） |
| 鉴权 | 无（连接即可用），安全性由「仅监听回环地址 + 拒绝非回环连接」保证 |
| 安全 | 服务仅绑定 127.0.0.1，且拒绝非回环来源连接 |
| 心跳 | 客户端 60 秒内无消息会被断开；可定期发送任意非请求消息保活 |

软电话同时提供 HTTP 服务：

- `GET http://127.0.0.1:8765/` —— 测试页面（开箱即用）
- `GET http://127.0.0.1:8765/sdk/sip-phone-sdk.js` —— JS SDK

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
| `call.dial` | `{ "target": "1001", "video": false }` | `{ "callId": "..." }` | 拨号；target 为号码或 SIP URI；`video: true` 发起视频呼叫（REQ-VIDEO-001） |
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
| `system.getState` | `{}` | 见下 | 查询当前状态（版本、平台、注册、通话、更新状态、**自动更新配置 updateConfig**、自动接听开关） |
| `contact.export` | `{}` | `{ "csv": "...", "count": 12 }` | 导出通信录 CSV 文本（REQ-WS-015）：表头 `号码,名称,备注,常用`，**UTF-8 带 BOM**（Excel 直接打开不乱码）。网页端不做通信录的增删改查 |
| `contact.import` | `{ "csv": "号码,名称,备注,常用\n2999,张三,前台,是\n" }` | `{ "added": 1, "updated": 0, "skipped": 0, "total": 12 }` | 导入通信录 CSV：按号码去重，**已存在的只覆盖号码/名称/备注，常用标志只增不减**（本地已标就保持、本地没标而 CSV 标了则变常用），不存在的整条新增；没有号码或整行解析失败的行计入 `skipped`。参数缺失返回 `invalid_params`，通信录不可用返回 `contacts_unavailable`。单条消息上限 4MB |
| `system.setAutoAnswer` | `{ "enabled": true }` | `{ "enabled": true }` | 自动接听开关（REQ-CALL-014）：与软电话界面上的「自动接听」按钮**共用一份状态**，变更后广播 `system.autoAnswer`，桌面按钮与网页据此同步。视频来电自动接听时按「视频接听」接通（REQ-VIDEO-004） |
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
  "autoAnswer": false
}
```

- `version` / `os` / `arch`：软电话版本号与编译目标平台/架构——前端据此到业务平台查询该架构下是否有更高版本。
- `update`：自动更新状态，无可用更新时省略（字段说明见 4. 事件 `system.update`）。
- `video`：画面旋转/水平镜像（REQ-VIDEO-012），远端与本地回显各一份；网页连上（含断线重连）即可据此把按钮和渲染状态对齐。
- `autoAnswer`：自动接听开关当前状态（与软电话界面的按钮共用一份，可用 `system.setAutoAnswer` 修改）。

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

由**业务前端**调用的**强制推送**通道（REQ-UPD-019）：把某个版本告知软电话，软电话弹提示、由用户在界面上确认后下载替换重启（**WS 只能告知，不能触发下载或重启**，REQ-UPD-009）。

- `version` 与 `url` 必填；`sha256`/`notes`/`os`/`arch` 可选（os/arch 提供时须与软电话自身一致）。
- 版本不高于当前返回 `update_not_newer`；参数非法返回 `invalid_params`；正在下载/已就绪时再来一条返回 `internal`。
- 同版本重复告知幂等（已在下载/就绪的同版本直接忽略）。
- 「有没有新版本」**不在网页侧裁决**：软电话自己按 `system.setUpdateConfig` 的配置定时查询更新站点（REQ-UPD-017）；本方法用于页面手动推送（联调/回滚），SDK 的 `notifyUpdate` 恒带 `force`。

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

通话对象（`call`）字段：`id`、`direction`（incoming/outgoing）、`remote`、`remoteName`、`state`（ringing/established/terminated）、`muted`（保持）、`micMuted`、`speakerMuted`、`micGain`（0–200 的百分比）、`speakerVolume`（0–100 的百分比）、`hasVideo`、`video`（见上）。

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

- WS 服务仅监听 127.0.0.1，不对外网暴露，且拒绝非回环来源连接。
- 连接不做鉴权（已按用户要求取消 token）：**本机任何进程或网页均可控制软电话**，安全性完全依赖回环监听。
- 更新相关：`system.notifyUpdate`（强制推送）与 `system.setUpdateConfig` 都只能"告知/配置"，**不能触发下载或重启**——下载必须由用户在软电话界面上点击，且提示窗会显示下载地址的主机名供用户判断来源。软电话自己查站点只读不写，不携带任何凭据。
- **安全边界按现状固定**（增强方案**待定**）：只做「仅监听 `127.0.0.1` + 拒绝非回环来源」这一层，**不加额外鉴权**；来源端口白名单、token 鉴权等更强隔离已讨论，暂缓实施。部署时请按此前提使用（不要在有不可信本地程序/网页的机器上无人值守运行）。
