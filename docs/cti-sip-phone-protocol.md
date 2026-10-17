# CTI ↔ mti-sip-phone 私有控制协议

CTI 服务通过 SIP NOTIFY（出话路外、无需建立通话 dialog）向 mti-sip-phone 软电话推送控制指令，话机解析后执行对应动作。典型场景：坐席登出时通知话机主动注销、远程拨号/接听/挂断、远程设置自动接听。

## 1. 传输链路

```
CTI --ESL(sendevent NOTIFY)--> FreeSWITCH --SIP NOTIFY--> OpenSIPS --SIP NOTIFY--> mti-sip-phone
```

- CTI 通过 Event Socket（ESL）的顶层 `sendevent` 命令向 FreeSWITCH 触发 `NOTIFY` 事件。
- FreeSWITCH 的 `dscc` profile 将 NOTIFY 发给 `<分机>@<opensips>`。
- OpenSIPS 按注册 location 查表转发到话机实际 contact（`lookup("location","m")`）。
- 话机无需订阅，收到的是 unsolicited NOTIFY。

## 2. 报文格式

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

## 3. 事件定义

| 事件名 | body | 话机动作 |
|---|---|---|
| `MSP_DIAL` | `{"destNo":"<号码>","withVideo":true/false}` | 对 `destNo` 发起呼叫（`withVideo` 是否视频） |
| `MSP_ANSWER` | `{"withVideo":true/false}` | 接听当前来电（`withVideo` 是否视频接听） |
| `MSP_HANGUP` | 无 | 挂断当前通话 |
| `MSP_SET_AUTOANSWER` | `{"autoAnswer":true/false}` | 更新自动接听开关 |
| `MSP_UNREGISTER` | `{"reason":"<原因>"}` | 注销 SIP 注册（发 `REGISTER` 且 `Expires: 0`） |

## 4. 话机侧实现要求

1. REGISTER 的 `Allow` 头必须包含 `NOTIFY`，否则 OpenSIPS 的 `lookup("location","m")` 按方法过滤会查不到 contact 并回 `511 Not Found`。
2. 收到 out-of-dialog NOTIFY 时按 `Event` 头匹配事件名，忽略 `Subscription-State`。
3. 匹配成功后回 `200 OK`（而不是 405），未匹配到已知事件时也建议回 `200` 或 `481/405` 由实现自定。
4. 解析 JSON body 执行动作：
   - `MSP_DIAL`：对 `destNo` 发起呼叫（`withVideo=true` 发起视频呼叫）。
   - `MSP_ANSWER`：接听当前振铃通话（`withVideo=true` 视频接听）。
   - `MSP_HANGUP`：挂断当前通话。
   - `MSP_SET_AUTOANSWER`：更新自动接听开关。
   - `MSP_UNREGISTER`：向 OpenSIPS 发 `REGISTER`（`Expires: 0`）注销并清理本地注册态。

## 5. 示例

### 5.1 MSP_UNREGISTER（坐席登出通知注销）

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

### 5.2 MSP_DIAL（远程拨号）

```sip
NOTIFY sip:2001@10.168.4.160:15060 SIP/2.0
From: <sip:cti@10.168.4.160:15060>
To: <sip:2001@10.168.4.160:15060>
Event: MSP_DIAL
Subscription-State: terminated;reason=noresource
Content-Type: application/json
Content-Length: 39

{"destNo":"91381005","withVideo":false}
```

## 6. CTI 侧入口

- 通用推送：`POST /cti/notify`，body `{extensionNo, eventPackage, body}`。
- 话机控制：`POST /phone/{extensionNo}/dial?destNo=...`、`/phone/{extensionNo}/answer`、`/phone/{extensionNo}/hangup`、`/phone/{extensionNo}/auto-answer?autoAnswer=...`、`/phone/{extensionNo}/unregister?reason=...`。
- 坐席登出自动通知注销：由 `mti.cti.freeswitch.notify-unregister-on-logout=true` 开关控制。
