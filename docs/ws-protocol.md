# 本地 WebSocket 控制协议

> 适用版本：1.0.1。网页通过本机 WebSocket 连接 SIP 软电话，实现拨号、接听、挂断等控制，并接收振铃、挂断等事件。
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
| `call.dial` | `{ "target": "1001" }` | `{ "callId": "..." }` | 拨号；target 为号码或 SIP URI |
| `call.answer` | `{ "callId": "..." }` | `{ "callId": "..." }` | 接听 |
| `call.hangup` | `{ "callId": "..." }` | `{ "callId": "..." }` | 挂断 |
| `call.reject` | `{ "callId": "...", "code": 486 }` | `{ "callId": "..." }` | 拒接；code 为 SIP 状态码，默认 486 |
| `call.mute` | `{ "callId": "...", "muted": true }` | `{ "callId": "..." }` | 静音切换（仅通话建立后有效） |
| `system.getState` | `{}` | 见下 | 查询当前状态 |

### system.getState 返回示例

```json
{
  "version": "1.0.1",
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
  ]
}
```

## 4. 事件

| 事件 | 负载 | 说明 |
| --- | --- | --- |
| `system.state` | 与 getState 返回相同 | 连接建立时推送状态快照 |
| `call.incoming` | `{ "callId", "call" }` | 来电振铃 |
| `call.ringing` | `{ "callId", "call" }` | 呼出已振铃（180/183） |
| `call.established` | `{ "callId", "call" }` | 通话建立 |
| `call.terminated` | `{ "callId", "call", "reason" }` | 通话结束；reason：`local_hangup` / `remote_hangup` / `rejected` |
| `call.failed` | `{ "callId", "call", "reason" }` | 呼叫失败；reason：`busy` / `no_answer` / `not_found` 等 |
| `call.muted` | `{ "callId", "muted" }` | 静音状态变化 |
| `register.state` | `{ "state", "account", "reason" }` | 注册状态变化 |

通话对象（`call`）字段：`id`、`direction`（incoming/outgoing）、`remote`、`remoteName`、`state`（ringing/established/terminated）、`muted`。

## 5. 错误码

| 错误码 | 含义 |
| --- | --- |
| `unknown_method` | 未知方法 |
| `invalid_params` | 参数缺失或非法 |
| `not_ready` | 电话能力尚未接入（SIP 层未启动） |
| `call_not_found` | 通话不存在 |
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

## 7. 安全说明

- WS 服务仅监听 127.0.0.1，不对外网暴露，且拒绝非回环来源连接。
- 连接不做鉴权（已按用户要求取消 token）：**本机任何进程或网页均可控制软电话**，安全性完全依赖回环监听。
- 若需更高强度隔离，后续版本可增加来源端口白名单或恢复 token 鉴权（P1）。
