# 本地 WebSocket 控制协议

> 适用版本：1.0.3。网页通过本机 WebSocket 连接 SIP 软电话，实现拨号、接听、挂断等控制，并接收振铃、挂断等事件。
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
| `call.answer` | `{ "callId": "..." }` | `{ "callId": "..." }` | 接听 |
| `call.hangup` | `{ "callId": "..." }` | `{ "callId": "..." }` | 挂断 |
| `call.reject` | `{ "callId": "...", "code": 486 }` | `{ "callId": "..." }` | 拒接；code 为 SIP 状态码，默认 486 |
| `call.mute` | `{ "callId": "...", "muted": true }` | `{ "callId": "..." }` | 静音切换（仅通话建立后有效） |
| `system.getState` | `{}` | 见下 | 查询当前状态（版本、平台、注册、通话、更新状态） |
| `system.notifyUpdate` | `{ "version": "1.1.0", "url": "http://dl.x/app.exe", "sha256": "…", "notes": "…", "os": "windows", "arch": "amd64" }` | `{ "phase": "available", "version": "1.1.0", "currentVersion": "1.0.3" }` | 告知软电话有新版本可用（自动更新，见 6.3）；`sha256`/`notes`/`os`/`arch` 可选 |

### system.getState 返回示例

```json
{
  "version": "1.0.3",
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
  }
}
```

- `version` / `os` / `arch`：软电话版本号与编译目标平台/架构——前端据此到业务平台查询该架构下是否有更高版本。
- `update`：自动更新状态，无可用更新时省略（字段说明见 4. 事件 `system.update`）。

### system.notifyUpdate 说明

- 由**业务前端**在查询到更高版本后调用；软电话自身不查询更新服务器。
- `version` 不高于软电话当前版本 → 失败，错误码 `update_not_newer`（视为正常结果，不必告警）。
- 参数缺失、`url` 非 http/https、`os`/`arch` 与软电话不一致 → `invalid_params`。
- 受理后软电话会在右下角弹出提示窗并在主界面显示更新提示行；**是否下载/重启由用户在软电话界面上确认**，本方法不会自动下载。
- 重复告知同一版本为幂等操作（仍返回 ok）。

## 4. 事件

| 事件 | 负载 | 说明 |
| --- | --- | --- |
| `system.state` | 与 getState 返回相同 | 连接建立时推送状态快照 |
| `call.dialing` | `{ "callId", "call" }` | 呼出已发起（本地拨号按钮与 WS 拨号共用） |
| `call.incoming` | `{ "callId", "call" }` | 来电振铃；`call.hasVideo` 为 true 表示对端带视频 |
| `call.ringing` | `{ "callId", "call" }` | 呼出已振铃（180/183） |
| `call.established` | `{ "callId", "call" }` | 通话建立 |
| `call.terminated` | `{ "callId", "call", "reason" }` | 通话结束；reason：`local_hangup` / `remote_hangup` / `rejected` |
| `call.failed` | `{ "callId", "call", "reason" }` | 呼叫失败；reason：`busy` / `no_answer` / `not_found` 等 |
| `call.muted` | `{ "callId", "muted" }` | 静音状态变化 |
| `call.video` | `{ "callId", "call" }` | 视频状态变化（协商成功/开关切换/拿到对端分辨率）；状态见 `call.video` |
| `register.state` | `{ "state", "account", "reason" }` | 注册状态变化 |
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

> 视频**画面**不经 WS 推送（本阶段）；需求 9 的网页订阅留到下一阶段实现。

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

通话对象（`call`）字段：`id`、`direction`（incoming/outgoing）、`remote`、`remoteName`、`state`（ringing/established/terminated）、`muted`。

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

### 6.3 自动更新（版本上报 → 前端告知新版本）

```
业务前端（网页）          软电话                  业务平台
  |---- WS 连接 ---------> |                       |
  |<--- system.state ----- |（含 version/os/arch）  |
  |---------- 查询最新版本（version/os/arch） ----->|
  |<--------- 有更高版本：版本号 + 下载地址 --------|
  |-- system.notifyUpdate >|                       |
  |<-- 响应 {phase:available}                      |
  |<--- system.update -----|（右下角提示窗 + 主窗体提示行；等待用户点击）
  |                        |== 用户点击「立即更新」==
  |<--- system.update -----|（downloading，progress 递增）
  |<--- system.update -----|（ready；无通话则自动重启，通话中等待通话结束）
  |         ...软电话替换可执行文件并重启，WS 断开，重连后重新上报...
```

## 7. 安全说明

- WS 服务仅监听 127.0.0.1，不对外网暴露，且拒绝非回环来源连接。
- 连接不做鉴权（已按用户要求取消 token）：**本机任何进程或网页均可控制软电话**，安全性完全依赖回环监听。
- `system.notifyUpdate` 只能"告知"，不能触发下载或重启：下载必须由用户在软电话界面上点击，且提示窗会显示下载地址的主机名供用户判断来源。
- 若需更高强度隔离，后续版本可增加来源端口白名单或恢复 token 鉴权（P1）。
