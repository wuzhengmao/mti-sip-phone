# 本地 WebSocket 控制协议

> 适用版本：1.0.7。网页通过本机 WebSocket 连接 SIP 软电话，实现拨号、接听、挂断等控制，并接收振铃、挂断等事件。
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
| `call.setVideo` | `{ "callId": "...", "enabled": true }` | `{ "callId": "..." }` | 视频总开关（纯本地动作，REQ-VIDEO-008）：关 = 不显示远端、不推送本地、视频窗口关闭；状态变化推 `call.video` |
| `call.dtmf` | `{ "callId": "...", "digit": "5" }` | `{ "callId": "..." }` | 发送 DTMF（RFC 4733 telephone-event，REQ-CALL-006）：`digit` 为单个字符 `0-9 * # A-D`，通话接通后才有效；参数缺失或 `digit` 不是单字符返回 `invalid_params` |
| `system.getState` | `{}` | 见下 | 查询当前状态（版本、平台、注册、通话、更新状态、自动接听开关） |
| `system.setAutoAnswer` | `{ "enabled": true }` | `{ "enabled": true }` | 自动接听开关（REQ-CALL-014）：与软电话界面上的「自动接听」按钮**共用一份状态**，变更后广播 `system.autoAnswer`，桌面按钮与网页据此同步。视频来电自动接听时按「视频接听」接通（REQ-VIDEO-004） |
| `system.notifyUpdate` | `{ "version": "1.1.0", "url": "http://dl.x/app.exe", "sha256": "…", "notes": "…", "os": "windows", "arch": "amd64", "force": false }` | `{ "phase": "available", "version": "1.1.0", "currentVersion": "1.0.7" }` | 告知软电话有新版本可用（自动更新，见 6.3）；`sha256`/`notes`/`os`/`arch` 可选；`force: true` = **强制推送**，跳过"版本必须更高"的检查（REQ-UPD-015，页面对着手动推送/回滚用；SDK 的定时查询不带它） |
| `video.subscribe` | `{ "remote": { "maxWidth": 640, "maxHeight": 480, "fps": 30, "quality": 75 }, "local": { "maxWidth": 160, "maxHeight": 120 } }` | 见下 | 订阅视频画面（REQ-VIDEO-009）：**必定订阅远端画面**（`remote` 不传也有默认参数），`local` 可选——不传就是**不订阅本地回显**；两条通道各带各的参数（本地回显通常小一号）。订阅期间**软电话自动关闭视频窗口**，画面以二进制帧推送（见 4.1） |
| `video.unsubscribe` | `{}` | `{ "local": false, "remote": false }` | 取消本连接的全部视频订阅（两条通道都已退订）；软电话恢复显示视频窗口 |
| `video.setTransform` | `{ "channel": "remote", "rotate": true }` / `{ "channel": "local", "mirror": true }` / `{ "channel": "remote", "rotation": 90 }` | `{ "remote": { "rotation": 90, "mirror": false }, "local": { "rotation": 0, "mirror": false } }` | 画面旋转/水平镜像（REQ-VIDEO-012）：`rotate: true` = 顺时针 +90°，`rotation` 直接设角度，`mirror` 设水平镜像；`channel` 为 `remote`（远端画面，可省）或 `local`（本地回显）。**状态存在软电话侧、桌面与网页共用一份**，谁改都广播 `video.transform` |

### system.getState 返回示例

```json
{
  "version": "1.0.7",
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

### system.notifyUpdate 说明

- 由**业务前端**在查询到更高版本后调用；软电话自身不查询更新服务器。
- `version` 不高于软电话当前版本 → 失败，错误码 `update_not_newer`（视为正常结果，不必告警）。
- `force: true` 跳过上面这条版本比较（强制推送，REQ-UPD-015）：同版本、甚至更低版本都会被受理，用于联调与回滚；平台一致性等其余校验不变。**SDK 的定时查询不使用它**——自动更新始终按版本高低决定要不要提示。
- 参数缺失、`url` 非 http/https、`os`/`arch` 与软电话不一致 → `invalid_params`。
- 受理后软电话会在右下角弹出提示窗并在主界面显示更新提示行；**是否下载/重启由用户在软电话界面上确认**，本方法不会自动下载。
- 重复告知同一版本为幂等操作（仍返回 ok）。

### video.subscribe 返回示例

```json
{
  "remote": { "maxWidth": 640, "maxHeight": 480, "fps": 30, "quality": 75 },
  "local": { "maxWidth": 160, "maxHeight": 120, "fps": 10, "quality": 75 },
  "call": { "id": "abc", "hasVideo": true, "video": { "negotiated": true, "sending": true, "width": 640, "height": 480 } }
}
```

- 请求里 `remote` / `local` 都是**参数对象、可缺字段**：`{}` 或整个缺省都表示"用默认值"。
- 返回的是**生效参数**（每条通道一份）：越界值会被钳制到合法范围（`maxWidth`/`maxHeight` ∈ [64, 1920]，`fps` ∈ [1, 30]，`quality` ∈ [30, 95]），不报错。
- **远端恒订阅**（不传 `remote` 也照样订阅，否则会出现"桌面窗口被隐藏、远端画面却没人看"的糊涂状态）；`local` 不传 = 不订阅本地回显，此时返回里没有 `local` 字段。
- `call` 为当前进行中的通话快照（无通话时为 `null`）。
- 订阅是**连接级**的：连接断开自动退订；订阅一直有效（跨通话），通话开始后画面自动推来。

## 4. 事件

| 事件 | 负载 | 说明 |
| --- | --- | --- |
| `system.state` | 与 getState 返回相同 | 连接建立时推送状态快照 |
| `call.dialing` | `{ "callId", "call" }` | 呼出已发起（本地拨号按钮与 WS 拨号共用） |
| `call.incoming` | `{ "callId", "call" }` | 来电振铃；`call.hasVideo` 为 true 表示对端带视频 |
| `call.ringing` | `{ "callId", "call", "earlyMedia" }` | 呼出已振铃（180/183）；`earlyMedia: true` 表示本次振铃带**早期媒体**（183+SDP，回铃音由对端下发，软电话不再放本地回铃音） |
| `call.established` | `{ "callId", "call" }` | 通话建立 |
| `call.terminated` | `{ "callId", "call", "reason", "missed" }` | 通话结束；reason：`local_hangup`（本端挂断）/ `remote_hangup`（对端挂断）/ `remote_cancel`（振铃中收到 CANCEL）/ `rejected`（本端拒接）。**`missed: true` = 未接来电**（呼入、从未接通、且非本端当场处理——本端拒接/振铃中挂断不算），桌面端据此弹系统通知（REQ-UI-010） |
| `call.failed` | `{ "callId", "call", "reason", "missed" }` | 呼叫失败；reason：`busy` / `no_answer` / `not_found` / `failed`（协商或媒体失败）等；`missed` 含义同上 |
| `call.muted` | `{ "callId", "muted" }` | 保持（静音）状态变化 |
| `call.audio` | `{ "callId", "call" }` | 麦克风/扬声器开关变化（`call.micMuted` / `call.speakerMuted`，REQ-UI-023） |
| `call.video` | `{ "callId", "call" }` | 视频状态变化（协商成功/开关切换/拿到对端分辨率）；状态见 `call.video` |
| `video.web` | `{ "active", "local", "remote", "viewers" }` | 网页视频订阅状态变化（任一连接订阅/退订时广播）。`active: true` 表示有网页在看画面，此时软电话**不显示视频窗口** |
| `video.transform` | `{ "remote": { "rotation", "mirror" }, "local": { "rotation", "mirror" } }` | 画面旋转/水平镜像变化（桌面视频窗口或网页任一侧改动都会广播）；重连时也可从 `system.state` 的 `video` 字段取当前值 |
| `register.state` | `{ "state", "account", "reason" }` | 注册状态变化 |
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

通话对象（`call`）字段：`id`、`direction`（incoming/outgoing）、`remote`、`remoteName`、`state`（ringing/established/terminated）、`muted`（保持）、`micMuted`、`speakerMuted`、`hasVideo`、`video`（见上）。

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

### 6.3 自动更新（SDK 定时查询更新站点 → 前端告知新版本）

```
业务前端（网页/SDK）      软电话                  更新站点
  |---- WS 连接 ---------> |                       |
  |<--- system.state ----- |（含 version/os/arch）  |
  |---------- 查询最新版本（version/os/arch） ----->|   连上后立即查一次，
  |<--------- 有更高版本：版本号 + 下载地址 --------|   之后按 SDK 的间隔定时查
  |-- system.notifyUpdate >|                       |
  |<-- 响应 {phase:available}                      |
  |<--- system.update -----|（右下角提示窗 + 主窗体提示行；等待用户点击）
  |                        |== 用户点击「立即更新」==
  |<--- system.update -----|（downloading，progress 递增）
  |<--- system.update -----|（ready；无通话则自动重启，通话中等待通话结束）
  |         ...软电话替换可执行文件并重启，WS 断开，重连后重新上报...
```

查询由 **JS SDK** 完成（`updateUrl` 在网页初始化 SDK 时告知，`checkUpdate` / `setUpdateSource`）：
站点接口与字段见其《REST API》的 `GET /api/v1/apps/{app}/check`。SDK 只做搬运——
**不比较版本高低**（裁决在站点与软电话），查到的结果经 SDK 本地事件 `update.check` 给页面
（本地事件，不经过 WS，也不在本文档第 4 节的事件表里）。软电话侧没有任何轮询逻辑
（REQ-UPD-009：WS 只能告知，下载与重启必须由用户在软电话界面上点击）。

## 7. 安全说明

- WS 服务仅监听 127.0.0.1，不对外网暴露，且拒绝非回环来源连接。
- 连接不做鉴权（已按用户要求取消 token）：**本机任何进程或网页均可控制软电话**，安全性完全依赖回环监听。
- `system.notifyUpdate` 只能"告知"，不能触发下载或重启：下载必须由用户在软电话界面上点击，且提示窗会显示下载地址的主机名供用户判断来源。
- 若需更高强度隔离，后续版本可增加来源端口白名单或恢复 token 鉴权（P1）。
