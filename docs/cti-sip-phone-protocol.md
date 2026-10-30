# CTI ↔ mti-sip-phone 私有控制协议

CTI 与 mti-sip-phone 软电话之间的双向私有控制协议：

- **正向（CTI → 话机）**：通过出话路外的 SIP NOTIFY 推送控制指令（拨号/接听/挂断/自动接听/注销/操作结果）。
- **反向（话机 → CTI）**：通过 out-of-dialog SIP NOTIFY 上报控制指令（咨询/转移/三方/监听/代接/会议等）与心跳。

## 一、正向：CTI → 话机（SIP NOTIFY）

### 1. 传输链路

```
CTI --ESL(sendevent NOTIFY)--> FreeSWITCH --SIP NOTIFY--> OpenSIPS --SIP NOTIFY--> mti-sip-phone
```

- CTI 通过 Event Socket（ESL）的顶层 `sendevent` 命令向 FreeSWITCH 触发 `NOTIFY` 事件。
- FreeSWITCH 的 `dscc` profile 将 NOTIFY 发给 `<分机>@<opensips>`。
- OpenSIPS 按注册 location 查表转发到话机实际 contact（`lookup("location","m")`）。
- 话机无需订阅，收到的是 unsolicited NOTIFY。

### 2. 报文格式

SIP NOTIFY 关键头：

| 头 | 取值 | 说明 |
|---|---|---|
| Request-URI | `sip:<分机>@<opensips>` | OpenSIPS 转发时会改写成话机实际 contact |
| From | `sip:cti@<opensips>` | 固定来源 |
| Event | `<事件名>` | 协议事件名（见 §3） |
| Subscription-State | `terminated;reason=noresource` | FreeSWITCH 自动补充，话机应忽略 |
| Content-Type | `application/json` | body 为 JSON |
| Content-Length | N | |

body 为 JSON 对象，字段见各事件定义；无参数的事件 body 为空。

### 3. 事件定义

| 事件名 | body | 话机动作 |
|---|---|---|
| `MSP_DIAL` | `{"destNo":"<号码>","withVideo":true/false}` | 对 `destNo` 发起呼叫（`withVideo` 是否视频） |
| `MSP_ANSWER` | `{"withVideo":true/false}` | 接听当前来电（`withVideo` 是否视频接听） |
| `MSP_HANGUP` | 无 | 挂断当前通话 |
| `MSP_SET_AUTOANSWER` | `{"autoAnswer":true/false}` | 更新自动接听开关 |
| `MSP_UNREGISTER` | `{"reason":"<原因>"}` | 注销 SIP 注册（发 `REGISTER` 且 `Expires: 0`） |
| `MSP_RESULT` | `{"seq":"<字符串>","action":"<动作>","success":true/false,"code":"<错误码>","message":"<错误信息>"}` | 操作处理结果（见 §11） |
| `MSP_UNSUPPORTED` | `{"extension":"2001","reason":"not_extension","timestamp":N}` | 停止发送 `MSP_HEARTBEAT` 并退出坐席模式（见 §13） |

### 4. 话机侧实现要求

1. REGISTER 的 `Allow` 头必须包含 `NOTIFY`，否则 OpenSIPS 的 `lookup("location","m")` 按方法过滤会查不到 contact 并回 `511 Not Found`。
2. 收到 out-of-dialog NOTIFY 时按 `Event` 头匹配事件名，忽略 `Subscription-State`。
3. 匹配成功后回 `200 OK`（而不是 405），未匹配到已知事件时也建议回 `200` 或 `481/405` 由实现自定。
4. 解析 JSON body 执行动作：
   - `MSP_DIAL`：对 `destNo` 发起呼叫（`withVideo=true` 发起视频呼叫）。
   - `MSP_ANSWER`：接听当前振铃通话（`withVideo=true` 视频接听）。
   - `MSP_HANGUP`：挂断当前通话。
   - `MSP_SET_AUTOANSWER`：更新自动接听开关。
   - `MSP_UNREGISTER`：向 OpenSIPS 发 `REGISTER`（`Expires: 0`）注销并清理本地注册态。
   - `MSP_RESULT`：按 §11 处理操作的结果回执。
   - `MSP_UNSUPPORTED`：停止发送 `MSP_HEARTBEAT` 并退出坐席模式（见 §13）。

### 5. 示例

#### 5.1 MSP_UNREGISTER（坐席登出通知注销）

```sip
NOTIFY sip:2001@10.168.4.160:15060 SIP/2.0
From: <sip:cti@10.168.4.160:15060>
To: <sip:2001@10.168.4.160:15060>
Event: MSP_UNREGISTER
Subscription-State: terminated;reason=noresource
Content-Type: application/json
Content-Length: 19

{"reason":"logout"}
```

#### 5.2 MSP_DIAL（远程拨号）

```sip
NOTIFY sip:2001@10.168.4.160:15060 SIP/2.0
From: <sip:cti@10.168.4.160:15060>
To: <sip:2001@10.168.4.160:15060>
Event: MSP_DIAL
Subscription-State: terminated;reason=noresource
Content-Type: application/json
Content-Length: 63

{"destNo":"91381005","withVideo":false}
```

### 6. CTI 侧入口

- 通用推送：`POST /cti/notify`，body `{extensionNo, eventPackage, body}`。
- 话机控制：`POST /phone/{extensionNo}/dial?destNo=...`、`/phone/{extensionNo}/answer`、`/phone/{extensionNo}/hangup`、`/phone/{extensionNo}/auto-answer?autoAnswer=...`、`/phone/{extensionNo}/unregister?reason=...`。
- 坐席登出自动通知注销：由 `mti.cti.freeswitch.notify-unregister-on-logout=true` 开关控制。

## 二、反向：话机 → CTI（SIP NOTIFY）

### 7. 传输链路

```
mti-sip-phone --SIP NOTIFY--> OpenSIPS --SIP NOTIFY--> FreeSWITCH --ESL CUSTOM sofia::notify事件--> CTI
```

- 话机在**任意时刻**（通话中或空闲）发送 out-of-dialog SIP NOTIFY，经 OpenSIPS 到达 FreeSWITCH。
- FreeSWITCH 的 `dscc` profile 需开启 `accept-unsolicited-notify=true`（CTI 定制补丁），对无订阅的 NOTIFY fire `CUSTOM` 事件（Event-Subclass `sofia::notify`）并回 `200 OK`（未开启时回 `481`）。
- CTI 监听 `CUSTOM sofia::notify`，按 `Event` 头区分心跳（`MSP_HEARTBEAT`）与话务操作（`MSP_ACTION`）。
- 保持/取回/挂断等话机本地基础能力由话机自身实现，不走本协议。

### 8. 报文格式

SIP NOTIFY 关键头：

| 头 | 取值 | 说明 |
|---|---|---|
| Event | `MSP_ACTION` / `MSP_HEARTBEAT` | 操作指令 / 心跳 |
| Content-Type | `application/json` | body 为 JSON |
| Call-ID / From / To | 新 dialog 标识 | out-of-dialog，无需关联现有通话 |
| Request-URI | 任意（如 `<本机分机>@<域>` 或本机 contact） | OpenSIPS 将**话机发起**的 out-of-dialog NOTIFY 转发到 FreeSWITCH（来自 FreeSWITCH 的 NOTIFY 仍按 location 投递到话机） |

> `MSP_HEARTBEAT` 是**单向**的（话机 → CTI），CTI 永远不会向话机发送 `MSP_HEARTBEAT`。话机若收到入站 `MSP_HEARTBEAT`，说明信令被路由回了自身（OpenSIPS 未配置 NOTIFY 转发），应忽略并排查路由。

`MSP_ACTION` 的 body 为 JSON 对象：

```json
{"seq":"1","extension":"2001","action":"consult","target":"2002"}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `seq` | 是 | 指令序列号（**字符串**）：话机自行生成，同一分机内不重复即可（无需递增/连续）；CTI 原样回显在 `MSP_RESULT` 里 |
| `extension` | 是 | 坐席号（话机自身注册的分机号），CTI 以它定位坐席 |
| `action` | 是 | 操作名（见 §9） |
| 其余字段 | 按动作 | 见 §9 各动作参数 |

> 坐席号直接由 `extension` 字段给出（话机天然知道自己的分机号），CTI 据此定位坐席及其当前话务。

### 9. 动作定义

`target` 表示目标分机/号码；以 `SK:` 前缀表示技能组（如 `SK:110`）。

#### 9.1 咨询 / 转移

| action | 参数 | 对应 CTI 接口 |
|---|---|---|
| `consult` | `target`（必） | `consult(extension, target)` |
| `consultTransfer` | `target`（可选，缺省用当前咨询） | `consultTransfer(extension)` 或 `consultTransfer(extension, target)` |
| `consultThreeWay` | `target`（可选，缺省用当前咨询） | `consultThreeWay(extension)` 或 `consultThreeWay(extension, target)` |
| `finishConsult` | — | `finishConsult(extension)`（结束咨询，恢复双方通话） |
| `exitThreeWay` | `target`（必，要挂断的通话方号码） | `exitThreeWay(extension, target)`（三方发起方强制挂断任意一方） |
| `transfer` | `target`（必，`SK:xxx` 表示技能组） | `transfer(extension, target)`（盲转） |

#### 9.2 监听 / 代接

| action | 参数 | 对应 CTI 接口 |
|---|---|---|
| `monitor` | `target`（必，被监听坐席号） | `monitor(extension, target)`（班长在通话外发起监听） |
| `stopMonitor` | — | `stopMonitor(extension)` |
| `whisper` | — | `whisper(extension)`（进入耳语，仅被监听方听到班长） |
| `stopWhisper` | — | `stopWhisper(extension)`（退出耳语，恢复静默监听） |
| `intervene` | — | `intervene(extension)`（强插，与通话双方形成三方） |
| `intercept` | — | `intercept(extension)`（强拦，接替被监听的通话） |
| `takeover` | `target`（必，被代接分机号） | `takeover(extension, target)`（代接另一分机的振铃来电） |

- `monitor`：班长（`agentMode=MONITOR`）在通话外发起对被监听坐席的监听；`target` 须处于通话中。
- 进入监听后，`whisper` / `intervene` / `intercept` 由班长发起。

#### 9.3 坐席状态

| action | 参数 | 对应 CTI 接口 |
|---|---|---|
| `setBusy` | — | `setBusy(extension)`（坐席置忙） |
| `setWork` | `workSeconds`（可选，整理时长，缺省取服务端上限） | `setWork(extension, workSeconds)`（话后整理） |
| `setIdle` | — | `setIdle(extension)`（坐席置闲） |
| `leave` | — | `leave(extension)`（坐席离席，`isLeave=true`，不再参与话务分配） |
| `resume` | — | `resume(extension)`（坐席复席，`isLeave=false`，重新进入分配队列） |

- 处理结果通过 `MSP_RESULT` 回执；置忙/整理/置闲、离席/复席成功后 CTI 会额外推送 `MSP_EVENT_AGENT_ONLINE`（见 §16.1）同步最新 `isBusy`/`isWork`/`isLeave`。
- `setWork` 只能在坐席**正在呼入通话中**（`callId` 为呼入且已接通）时发起，否则回 `AGENT_NOT_ANSWER_INCOMING`；通话结束后坐席进入整理态（`isBusy=true`、`isWork=true`），持续 `workSeconds` 后自动置闲。
- **话后整理是一种特殊的忙，可以被置忙覆盖**：整理态下 `setBusy` 转为普通置忙（`isWork=false`，整理计时取消）；`setIdle` 立即结束整理态；离席（`leave`）优先于整理态。

#### 9.4 会议

| action | 参数 | 对应 CTI 接口 |
|---|---|---|
| `addConferenceMembers` | `members`（必，数组） | 若坐席不在会议：`startConference(extension, members)`（一次调用，当前双方通话转会议并加入 `members`）；已在会议则 `addConferenceMembers(extension, members)` |
| `removeConferenceMembers` | `members`（必，数组） | `removeConferenceMembers(extension, members)` |
| `muteConferenceMember` | `member`（必） | `muteConferenceMember(extension, member)` |
| `unmuteConferenceMember` | `member`（必） | `unmuteConferenceMember(extension, member)` |
| `deafConferenceMember` | `member`（必） | `deafConferenceMember(extension, member)` |
| `undeafConferenceMember` | `member`（必） | `undeafConferenceMember(extension, member)` |
| `vmuteConferenceMember` | `member`（必） | `vmuteConferenceMember(extension, member)` |
| `unvmuteConferenceMember` | `member`（必） | `unvmuteConferenceMember(extension, member)` |
| `finishConference` | — | `finishConference(extension)` |

#### 9.5 坐席列表

| action | 参数 | 说明 |
|---|---|---|
| `listAgents` | — | 返回当前在线坐席列表（已过滤 `not_ready`/`miss_phone`/`offline` 的坐席），结果通过 `MSP_RESULT.data` 下发（见 §11） |

`MSP_RESULT.data` 为数组，每项字段：

| 字段 | 说明 |
|---|---|
| `agent` | 坐席号 |
| `displayName` | 坐席显示名（`agentInfo.displayName`，可能为空） |
| `acd` | 技能组，格式 `110:1,122:1`（技能组:等级，逗号分隔，可能为空） |
| `agentMode` | 坐席模式（小写）：`normal` / `dial_out` / `monitor` |
| `agentStatus` | 坐席状态（小写）：`free` / `ringing` / `talking` / `free_offhook` / `dialing` / `busy` / `leave` / `work`(整理中)；`not_ready`(未连接) / `miss_phone`(无话机) / `offline`(离线) 的坐席不会出现在列表中 |

### 10. 心跳（MSP_HEARTBEAT）

话机周期性发送心跳，保持自身可用状态并上报坐席模式。

SIP NOTIFY 关键头：

| 头 | 取值 |
|---|---|
| Event | `MSP_HEARTBEAT` |
| Content-Type | `application/json` |

body：

```json
{"seq":"1","extension":"2001","expires":60,"status":"free","agentMode":"normal","isBusy":false,"isWork":false,"isLeave":false,"displayName":"张三"}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `seq` | 是 | 心跳序列号（**字符串**）：同一分机内不重复即可（无需递增/连续），用于集群去重 |
| `extension` | 是 | 坐席号 |
| `expires` | 是 | 心跳有效期（秒）：CTI 在该秒数内未收到新心跳，将话机判定为不可用 |
| `status` | 是 | 话机当前状态（字符串，大小写不敏感）：`free` 空闲 / `dialing` 拨号 / `ringing` 振铃 / `talking` 通话 / `holding` 保持 |
| `agentMode` | 是 | 话机当前坐席模式：`none` 未登录坐席 / `normal` 普通模式 / `dial_out` 外拨模式 / `monitor` 班长模式 |
| `isBusy` | 是 | 话机当前的置忙状态（boolean，true=置忙） |
| `isWork` | 否 | 话机当前是否处于**话后整理态**（boolean）：整理态是一种特殊的忙，此时 `isBusy=true`、`isWork=true`；整理结束或人工置忙/置闲后 `isWork=false`。缺省（不携带）表示话机未上报，CTI 跳过该项比对 |
| `isLeave` | 否 | 话机当前是否**离席**（boolean）：CTI 的 `leave`/`resume` 分别置 `true`/`false`。缺省表示话机未上报，CTI 跳过该项比对 |
| `displayName` | 否 | 话机侧记录的坐席显示名（可为空） |

CTI 处理：

1. 记录最后心跳时间与 `expires`。
2. 超过 `expires` 秒未收到新心跳，将坐席状态置为 `OFFLINE`（话机状态不变）；下次收到心跳或话机的任意 NOTIFY 信令时恢复。
3. **话机状态对齐**：比对 `status` 与 CTI 侧记录的话机状态，不一致时以话机上报为准**强制刷新** CTI 侧状态，并联动刷新坐席状态（如 `FREE` → 坐席空闲、`TALKING` → 坐席通话中）。
   - 为容忍信令延迟（话机侧状态更新慢于 CTI），CTI 会记录每次话机状态变更的时间：若 CTI 侧状态最近一次变更**不足 3 秒**，本轮不刷新，等待话机侧信令追上。
   - 若话机状态由**非空闲**被强制更新为**空闲**，CTI 会检查该分机是否仍有残留话务，有则挂断对应通道并强制复位本地话务状态。
4. 检测 `agentMode`/`isBusy`/`isWork`/`isLeave`/`displayName` 与 CTI 侧坐席属性是否一致，不一致时推送 `MSP_EVENT_AGENT_ONLINE`（携带 CTI 侧最新的 `agentMode`/`isBusy`/`isWork`/`isLeave`/`displayName`）或 `MSP_EVENT_AGENT_OFFLINE` 同步（见 §16）。
5. 若 `extension` 不是本机分机号（不匹配分机号规则），CTI 不处理该心跳，并回推一次 `MSP_UNSUPPORTED`（`reason=not_extension`）；话机收到后应停止心跳并退出坐席模式（见 §13）。

心跳**无** `MSP_RESULT` 回执。

### 11. 响应（MSP_RESULT）

- **SIP 层**：FreeSWITCH 对 NOTIFY 回 `200 OK`（SIP 事务层 ACK，仅表示信令送达，与 CTI 处理结果无关）。
- **应用层**：CTI 处理完 `MSP_ACTION` 后，通过正向 NOTIFY `MSP_RESULT` 把结果回给话机（`To` 为发指令的坐席）：

| 情况 | `MSP_RESULT` body |
|---|---|
| 成功 | `{"seq":"<字符串>","action":"<动作>","success":true}` |
| 成功（带返回值） | `{"seq":"<字符串>","action":"<动作>","success":true,"data":<结果>}` |
| 失败 | `{"seq":"<字符串>","action":"<动作>","success":false,"code":"<错误码>","message":"<错误信息>"}` |

- `seq` 原样回显请求里的字符串序列号，用于话机把回执与请求对齐；`action` 一并回显便于排查。
- `code` 为**错误码名称**（与 CTI 内部错误码枚举一致），如 `AGENT_NOT_IN_CALL`（坐席不在通话中）、`AGENT_NOT_IN_CONSULT`（坐席不在咨询中）、`AGENT_NOT_ANSWER_INCOMING`（坐席没有正在接听的呼入，`setWork` 前置条件）、`TARGET_AGENT_IS_BUSY`（目标坐席正忙）等；话机按字符串原样展示/记录即可，无需识别具体取值。
- `data`：仅带返回值的动作携带（目前为 `listAgents`），失败时不携带。示例：
  `{"seq":"7","action":"listAgents","success":true,"data":[{"agent":"2001","displayName":"张三","acd":"110:1","agentMode":"normal","agentStatus":"free"}]}`
- 失败示例：坐席不在通话中时发起咨询 → `{"seq":"3","action":"consult","success":false,"code":"AGENT_NOT_IN_CALL","message":"坐席不在通话中"}`。

### 12. 示例

#### 12.1 发起咨询

```sip
NOTIFY sip:2001@10.168.4.160:15060 SIP/2.0
Event: MSP_ACTION
Content-Type: application/json
Content-Length: 36

{"seq":"1","extension":"2001","action":"consult","target":"2002"}
```

#### 12.2 盲转技能组

```sip
NOTIFY sip:2001@10.168.4.160:15060 SIP/2.0
Event: MSP_ACTION
Content-Type: application/json
Content-Length: 81

{"seq":"2","extension":"2001","action":"transfer","target":"SK:110"}
```

#### 12.3 心跳

```sip
NOTIFY sip:2001@10.168.4.160:15060 SIP/2.0
Event: MSP_HEARTBEAT
Content-Type: application/json
Content-Length: 73

{"seq":"3","extension":"2001","expires":60,"status":"free","agentMode":"normal","isBusy":false,"isWork":false,"isLeave":false,"displayName":"张三"}
```

#### 12.4 失败回执（坐席不在通话中）

```sip
NOTIFY sip:2001@10.168.4.160:15060 SIP/2.0
Event: MSP_RESULT
Content-Type: application/json
Content-Length: 109

{"seq":"1","action":"consult","success":false,"code":"AGENT_NOT_IN_CALL","message":"坐席不在通话中"}
```

### 13. 话机侧实现要求

1. 心跳：进入坐席模式后周期性发送 `MSP_HEARTBEAT`，`expires` 建议取发送间隔的 2~3 倍；`status`/`agentMode`/`isBusy`/`displayName` 均以话机本地实时状态为准，`isWork`/`isLeave` 回显 CTI 最近一次 `MSP_EVENT_AGENT_ONLINE` 给出的值（不支持时可省略）；`seq` 为字符串且需保证不重复。
2. 操作指令：任意时刻发送 `MSP_ACTION`（无需在通话中），`Content-Type` 固定 `application/json`，body 携带 `seq`（字符串，不重复）、`extension`、`action` 及该动作所需参数。
3. 收到 FreeSWITCH 回的 `200 OK` 仅表示信令送达；业务结果以 `MSP_RESULT` 为准（按 `seq` 对齐）。
4. 保持/取回/挂断由话机本地完成，无需经本协议上报 CTI。
5. 收到 `MSP_UNSUPPORTED` 后：立即停止发送 `MSP_HEARTBEAT`，退出坐席模式并忽略后续 `MSP_EVENT_*` / `MSP_*_PANEL_UPDATED`。该消息**不一定由心跳触发**，服务端可能在任意时刻主动下发（例如将来全局关闭 MSP 特性），话机需随时可处理。

#### 13.1 CTI 侧实现

- OpenSIPS 需将**话机发起**的 out-of-dialog `NOTIFY` 转发到 FreeSWITCH（`opensips.cfg` 主路由：`if (is_method("NOTIFY") && !lb_is_destination("$si","$sp","1"))`，查 `load_balancer` 表取 FS 地址后 `relay`）；否则话机 NOTIFY 会被按 location 查表回环到话机自身。注意必须排除来自 FS 的 NOTIFY，否则 CTI 推给话机的 `MSP_EVENT_*` 会被回环回 FS。
- CTI 监听 ESL `CUSTOM` 事件，按 `Event-Subclass: sofia::notify` 过滤，再按 `Event` 头区分 `MSP_HEARTBEAT` 与 `MSP_ACTION`。
- 解析 body 的 `extension` + `action` + 参数，映射到对应域服务（Call/Consult/Monitor/Conference/Agent）的方法。
- 处理成功/失败后，向该坐席发 `MSP_RESULT` NOTIFY 回执（回显 `seq`）。
- 坐席号会经 CTI 的 `requiresAgent` 校验，非法坐席号会被拒绝并回失败回执。

## 三、通话/会议面板事件

### 14. 用途与能力协商

CTI 在通话/会议状态变化时，向**相关内部分机的话机**推送完整的通话面板 / 会议面板事件（`MSP_CALL_PANEL_UPDATED` / `MSP_CONFERENCE_PANEL_UPDATED`），话机据此显示面板与操作按钮，无需再轮询 CTI 接口。

#### 14.1 能力协商（msp_support）

只有声明支持的 sip-phone 才会收到面板事件，避免打扰普通 SIP 话机：

1. 话机 `REGISTER` 时在 `Contact` 头追加参数 `msp=support`（大小写不敏感），例如：

```sip
Contact: <sip:2001@10.168.4.160:5060;transport=udp;msp=support>;expires=3600
```

2. CTI 周期同步 OpenSIPS `location` 表时解析 `contact`，命中 `msp=support` 则记录该分机支持 MSP 事件（`SipAccountBean.mspSupport=true`，内部写入分机属性 `msp_support`）。
3. 仅当事件接收方的 `msp_support=true` 时才推送；未声明或已下线的话机不推送。
4. `msp_support` 随注册状态刷新：重新注册时按最新 `Contact` 更新，注销后清除。

> 未携带 `msp=support` 的话机行为完全不变，仅接收原本的 `MSP_DIAL/ANSWER/HANGUP/SET_AUTOANSWER/UNREGISTER/RESULT` 控制类事件。

### 15. 报文格式

复用正向 NOTIFY 通道（§1/§2）：

| 头 | 取值 |
|---|---|
| Event | `MSP_CALL_PANEL_UPDATED` / `MSP_CONFERENCE_PANEL_UPDATED` / `MSP_EVENT_AGENT_*`（见 §16） |
| Content-Type | `application/json` |
| Subscription-State | `terminated;reason=noresource`（话机忽略） |

> `MSP_EVENT_*` / `MSP_*_PANEL_UPDATED` 都是**单向通知**，话机无需回执；话机对未识别的事件建议回 `200 OK` 并忽略。

### 16. 事件定义

CTI 推给话机的事件只有三类：坐席状态（`MSP_EVENT_AGENT_ONLINE/OFFLINE`）、通话面板（`MSP_CALL_PANEL_UPDATED`）、会议面板（`MSP_CONFERENCE_PANEL_UPDATED`）。

> **名称字段约定**：面板里的每个号码字段都伴随一个名称字段（如 `callList[].peer`/`peerName`、`memberList[].member`/`memberName`）。
> 名称取该坐席登录时登记的 `displayName`，**仅当该号码是已登录坐席时才有值**；外线号码、网关号码或未登录分机时该字段省略。话机应优先显示名称，为空时回退显示号码。

#### 16.1 坐席状态（agent online / offline）

接收方：该分机的话机（`msp_support=true`）。

| 事件 | body | 话机动作 |
|---|---|---|
| `MSP_EVENT_AGENT_ONLINE` | `{"extension":"2001","displayName":"张三","agentMode":"normal","isBusy":false,"isWork":false,"isLeave":false,"status":"free","timestamp":N}` | 进入坐席状态，并同步坐席模式、置忙/整理态/离席与话机状态 |
| `MSP_EVENT_AGENT_OFFLINE` | `{"extension":"2001","timestamp":N}` | 立即退出坐席状态 |

- `displayName` 为坐席显示名（登录时指定），可能为空。
- `agentMode`：`none` 未登录坐席 / `normal` 普通模式 / `dial_out` 外拨模式 / `monitor` 班长模式。
- `isBusy`：是否置忙（boolean）。
- `isWork`：是否处于**话后整理态**（boolean）。整理态是一种特殊的忙（`isBusy=true`、`isWork=true`）：呼入话务结束后自动进入，或通话中由 `setWork` 主动发起；整理结束或人工置忙/置闲后 CTI 会再推一次 ONLINE 把 `isWork` 置回 `false`。话机据此区分「忙」与「整理中」。
- `isLeave`：是否**离席**（boolean）。CTI 的 `leave`/`resume` 改变该属性并推送 ONLINE。
- `status`：CTI 侧的话机状态（字符串，同 §10 心跳 `status` 取值：`free` / `dialing` / `ringing` / `talking` / `holding`）。话机收到后应与本地状态比对，不一致时立即补发一次 `MSP_HEARTBEAT`（让 CTI 侧尽快收敛到话机本地状态）。
- 话机收到 `MSP_EVENT_AGENT_ONLINE` 后进入**坐席状态**（通话面板可用），收到 `MSP_EVENT_AGENT_OFFLINE` 后退出。
- 未进入坐席状态时，其他 `MSP_EVENT_*` 应忽略。
- **坐席在线状态的保持改由话机心跳维持**（见 §10）：话机周期发送 `MSP_HEARTBEAT`，CTI 据此判定坐席在线状态（超时置 `OFFLINE`）并检测坐席状态一致性。
- 触发时机（仅以下场景推送，不再周期续期）：
  1. **坐席登录**时推送一次 `MSP_EVENT_AGENT_ONLINE`；
  2. **坐席登出**时推送一次 `MSP_EVENT_AGENT_OFFLINE`；
  3. **置忙/置闲**、**话后整理态进入/结束**、**离席/复席**、**坐席模式切换**（普通/外拨/班长）时推送一次 `MSP_EVENT_AGENT_ONLINE`，携带最新的 `agentMode`/`isBusy`/`isWork`/`isLeave`；
  4. **心跳属性不一致**时：话机上报的 `agentMode`/`isBusy`/`isWork`/`isLeave`/`displayName` 与 CTI 侧坐席属性不一致，推送 `MSP_EVENT_AGENT_ONLINE`（话机未进坐席模式、模式/置忙/整理态/离席不一致、显示名不一致）或 `MSP_EVENT_AGENT_OFFLINE`（CTI 已登出但话机仍停留坐席模式）同步。
- 同一分机可能多次收到 ONLINE，话机按幂等处理。
- 若 CTI 判定该号码不是本机分机号，会推送 `MSP_UNSUPPORTED`，话机应退出坐席模式并停止发送 `MSP_HEARTBEAT`（见 §10/§13）。

#### 16.2 通话面板（MSP_CALL_PANEL_UPDATED）

CTI 把计算好的**完整通话面板**推给坐席话机，话机据此显示通话面板与可用操作按钮。原来细粒度的
`MSP_EVENT_CALL_*`、`MSP_EVENT_CONSULT_*`、`MSP_EVENT_MONITOR_*`、`MSP_EVENT_THREE_WAY_*` 事件**不再推送**。

接收方：面板所属坐席（`msp_support=true`）。

| 事件 | body |
|---|---|
| `MSP_CALL_PANEL_UPDATED` | 见下 |

body 字段：

| 字段 | 说明 |
|---|---|
| `callId` | 话务 ID；面板空闲（无通话）时为 `null`/省略，话机应关闭面板 |
| `agent` | 坐席号 |
| `callType` | 通话类型：`incoming` / `outgoing` / `transfer` / `monitor` |
| `callMode` | 话务模式：`normal` / `consult` / `three-way` / `monitor` |
| `owner` | 当前操作的发起方分机；`callMode=normal` 时省略。咨询为咨询方，三方为发起方（咨询方/强插班长），监听为班长。话机据此决定哪些操作按钮可用 |
| `state` | 面板标题状态：`dialing` / `ringing` / `incoming` / `talking` / `consult-dialing` / `consult-ringing` / `consult-talking` / `three-way` / `listening` / `whispering` |
| `startTime` | 总计时起点（毫秒时间戳） |
| `failCause` | 失败原因（仅失败时）：`cancel` / `refuse` / `takeover` / `fail` |
| `callList` | 通话腿列表，每条含 `callerNo`/`calledNo`/`peer`/`peerName`/`intercom`/`skillGroup`/`callType`/`callStatus`/`startTime`/`ringingTime`/`offhookTime` |

- `callMode`/`state` 一起决定面板显示：例如 `consult` + `consult-ringing` 显示「咨询呼叫中」，`three-way` 显示三方通话面板，`monitor` + `whispering` 显示耳语。
- 通话腿的 `callStatus` 另有 `holding`（咨询期间原通话被保持），它不是面板的 `state`。
- 面板为**全量快照**：话机直接以 body 覆盖本地面板，不做增量计算。
- 任何面板变化（拨号/振铃/接通/咨询/三方/监听/耳语/强拦/结束）都会推一次。

#### 16.3 会议面板（MSP_CONFERENCE_PANEL_UPDATED）

CTI 把计算好的**完整会议面板**推给参会话机。原来细粒度的 `MSP_EVENT_CONFERENCE_*` 事件**不再推送**。

接收方：会议主持人及所有内部分机成员（`msp_support=true`）。

| 事件 | body |
|---|---|
| `MSP_CONFERENCE_PANEL_UPDATED` | 见下 |

body 字段：

| 字段 | 说明 |
|---|---|
| `confId` | 会议 ID |
| `owner` | 主持人 |
| `memberList` | 完整成员列表，每项含 `member`/`memberName`/`intercom`/`owner`/`state`/`timestamp` |

- 成员 `state`：`dialing` / `ringing` / `talking` / `holding` / `hangup`，以及会控状态 `muted`、`deafened`、`vmuted`（可用 `+` 组合）。
- 面板为**全量快照**，话机直接覆盖本地成员列表。
- 会议结束且存在被强制退出的成员时，会向这些成员推一次 `memberList` 为空的会议面板，话机据此退出会议界面（`confId` 保留，`memberList` 为空）。


### 17. 示例

#### 17.1 通话面板（咨询振铃中）

```sip
NOTIFY sip:2001@10.168.4.160:15060 SIP/2.0
From: <sip:cti@10.168.4.160:15060>
To: <sip:2001@10.168.4.160:15060>
Event: MSP_CALL_PANEL_UPDATED
Subscription-State: terminated;reason=noresource
Content-Type: application/json

{"callId":"...","agent":"2001","callType":"incoming","callMode":"consult","owner":"2001","state":"consult-ringing","startTime":1792381839000,"callList":[{"callerNo":"1381003","calledNo":"01108899","peer":"1381003","callType":"incoming","callStatus":"holding","startTime":1792381839000},{"callerNo":"2001","calledNo":"2002","peer":"2002","peerName":"李四","intercom":true,"callType":"consult","callStatus":"ringing","startTime":1792381839807}]}
```

#### 17.2 会议面板（成员加入）

```sip
NOTIFY sip:2001@10.168.4.160:15060 SIP/2.0
Event: MSP_CONFERENCE_PANEL_UPDATED
Content-Type: application/json

{"confId":"...","owner":"2001","memberList":[{"member":"2001","memberName":"张三","intercom":true,"owner":true,"state":"talking","timestamp":1792381839807},{"member":"2002","memberName":"李四","intercom":true,"owner":false,"state":"dialing","timestamp":1792381839807}]}
```

### 18. 话机侧实现要求

1. `REGISTER` 的 `Contact` 头追加 `;msp=support`（仅当话机实现了本组事件）。
2. 收到 `Event: MSP_CALL_PANEL_UPDATED` / `MSP_CONFERENCE_PANEL_UPDATED` 时，用 body **整份覆盖**本地通话面板/会议面板，回 `200 OK`。
3. `MSP_CALL_PANEL_UPDATED` 的 `callId` 为空表示面板空闲，话机应关闭通话面板；`MSP_CONFERENCE_PANEL_UPDATED` 的 `memberList` 为空表示会议结束，话机应退出会议界面。
4. 面板上的操作按钮由 `callMode` + `state` 决定（如咨询中显示结束咨询/转移/三方，三方显示踢人，监听显示耳语/强插/强拦）。
5. 未识别的事件名忽略并回 `200 OK`（向前兼容）。

### 19. CTI 侧实现

- `SipAccountBean` 增加 `mspSupport`，同步 `location` 表时按 `contact` 是否含 `msp=support` 赋值，并写入分机属性 `msp_support`。
- `CallPanelUpdateServiceImpl` 监听各类 `LocalXXX` 本地事件（`EventScope.LOCAL`、进程内同步，保证顺序），计算每个坐席的 `CallPanel`/`ConferencePanel`，并发布 `LocalCallPanelUpdatedEvent` / `LocalConferencePanelUpdatedEvent`。
- `PhoneServiceImpl` 只把这两个面板事件按接收方分机的 `msp_support` 推成 `MSP_CALL_PANEL_UPDATED` / `MSP_CONFERENCE_PANEL_UPDATED`；其余 `LocalXXX` 事件仅用于进程内计算，不再下发给话机。
