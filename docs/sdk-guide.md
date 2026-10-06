# mti-sip-phone JS SDK 开发指南

> 面向**在网页上集成软电话**的前端开发者。本文覆盖：快速上手、全部 API、视频订阅、
> 事件参考，以及一组**真实踩过的坑**（第 8 节，建议通读）。
>
> 更底层的 WebSocket 报文协议见 [ws-protocol.md](ws-protocol.md)——用 SDK 通常不需要看它。

## 1. 是什么 / 前提

SDK（`sip-phone-sdk.js`，无依赖）把本机运行的 **mti-sip-phone 软电话**接进你的网页：
网页拨号、接听、挂断、通话中控制（麦克风/扬声器/音量/DTMF/视频）、**把视频画面渲染到
页面上的 `<video>`**、通信录导入导出、写软电话的自动更新配置。

前提只有一个：**软电话正在本机运行**（托盘常驻）。SDK 连的是它的本地 WebSocket
（默认 `ws://127.0.0.1:8765/ws`），不经过网络、没有鉴权、不需要后端配合。

> 安全边界（框架提供，开发者需知晓）：WS 与 HTTP 都**只监听 127.0.0.1** 并拒绝非回环
> 来源；**本机任何进程或网页都能控制软电话**，这是产品设计（已取消 token 鉴权）。

## 2. 快速上手（5 分钟）

SDK 由软电话自身提供，**不需要在页面里放一份**：

```html
<script src="http://127.0.0.1:8765/sdk/sip-phone-sdk.js"></script>
```

最小可用页面：

```html
<!DOCTYPE html>
<html lang="zh-CN">
<body>
  <button id="dial">呼叫 1001</button>
  <button id="hangup" disabled>挂断</button>
  <p id="status">未连接</p>

  <script src="http://127.0.0.1:8765/sdk/sip-phone-sdk.js"></script>
  <script>
    const sdk = new SipPhoneSDK({ url: 'ws://127.0.0.1:8765/ws' });
    let callId = null;

    sdk.on('connected',    () => status.textContent = '已连接软电话');
    sdk.on('disconnected', () => status.textContent = '已断开');

    sdk.on('register.state', (s) => {
      status.textContent = '注册状态：' + s.state;   // registered / registering / failed / unregistered
    });

    // 来电：这里先自动接听演示；实际产品应展示接听/拒接按钮
    sdk.on('call.incoming', (data) => {
      callId = data.callId;
      sdk.answer(callId).catch((e) => console.error('接听失败', e.message));
    });

    sdk.on('call.established', (data) => {
      callId = data.callId;
      hangup.disabled = false;
      status.textContent = '通话中：' + data.call.remote;
    });

    sdk.on('call.terminated', () => {
      callId = null;
      hangup.disabled = true;
      status.textContent = '通话结束';
    });

    dial.onclick = () => sdk.dial('1001').then((r) => { callId = r.callId; });
    hangup.onclick = () => callId && sdk.hangup(callId);

    sdk.connect().catch((e) => status.textContent = '连接失败：' + e.message);
  </script>
</body>
</html>
```

三件事记住就够起步：**`connect()` 必须显式调用**（构造函数不会自动连）、
**一切操作都要 `callId`**（来自事件或 `dial` 的返回值）、**事件用 `on('call.xxx', fn)` 订阅**。

## 3. 连接与生命周期

```js
const sdk = new SipPhoneSDK({
  url: 'ws://127.0.0.1:8765/ws',   // 默认值即此；端口随软电话配置变，见 §8.1
  autoReconnect: true,             // 默认 true：断线每 3 秒重连一次
  reconnectDelay: 3000,
  schemaUri: 'sip://1001:密码@10.0.0.10',  // 可选：首次连不上时自动拉起软电话，见下
});
await sdk.connect();
```

- **`connect()` 必须在业务操作前调用**（一般页面加载即调）。重复调用安全：已连接时直接 resolve。
- **`schemaUri`**：首次 WS 连接失败时（说明软电话没在跑），SDK 会用该 URI 走系统 `sip://`
  协议**拉起软电话**（浏览器弹系统确认框）。**每页会话只尝试一次**；拉起后靠 `autoReconnect`
  自动连上。`sips://` 前缀 = TLS 信令（默认端口 5061）。注意 URI 里的明文密码会出现在
  页面源码里，公共设备慎用。
- **断线行为**：连接断开时所有未完成的请求会 reject（`连接已断开`）；订阅过的事件在重连后
  依然有效；**视频订阅会被 SDK 自动恢复**（记住最近一次的订阅参数）。
- **`close()`**：主动断开且不再重连（页面卸载时可调）。
- **心跳**：SDK 每 25 秒自动发一条空消息，避免被软电话 60 秒读超时断开。自己实现 WS
  客户端时也必须做这件事。

## 4. 核心概念

| 概念 | 说明 |
| --- | --- |
| **callId** | 每通电话的唯一标识。**只有两个来源**：事件负载里的 `data.callId`，或 `dial()` 的返回值。不要自己猜/缓存失效的 ID |
| **Promise 错误** | 所有方法返回 Promise；失败时 reject 一个 `Error`，**`err.code`** 是错误码（见 §7），`err.message` 是中文描述 |
| **事件** | 软电话广播给**所有**已连接页面；`sdk.on('事件名', fn)` 订阅，`on('*')` 订阅全部（本地事件 `connected`/`disconnected` 除外） |
| **通话状态** | `ringing`（振铃）→ `established`（通话中）→ `terminated`（结束）；呼出另有 `call.dialing` |
| **单通话限制** | 软电话同一时刻只支持**一路通话**：通话中再 `dial` 会失败，新来电会被自动拒掉（486） |

## 5. API 参考

### 5.1 通话控制

| 方法 | 说明 |
| --- | --- |
| `dial(target, {video})` | 拨号。`target` 为号码或 SIP URI；`video: true`（或 `dialVideo(target)`）发起**视频呼叫**。返回 `{callId}` |
| `answer(callId, {video})` | 接听。`video: true`（或 `answerVideo(callId)`）= **视频接听**（开摄像头双向视频）；普通接听 = 不接受视频（对端也不再发） |
| `hangup(callId)` | 挂断（振铃中呼出 = 取消；振铃中呼入 = 拒接） |
| `reject(callId, code)` | 拒接，`code` 为 SIP 状态码，默认 `486` |
| `mute(callId, muted)` | **保持/取消保持**（走 SIP re-INVITE，对端会看到"保持"）。与麦克风开关是两回事 |
| `sendDTMF(callId, digit)` | 发送 DTMF（RFC 4733），`digit` 为单字符 `0-9 * # A-D`；**仅通话接通后有效** |

### 5.2 音频控制（纯本地动作，不发 SIP 信令）

| 方法 | 说明 |
| --- | --- |
| `muteMic(callId, muted)` | 麦克风开关（关 = 不发送，对端听不到） |
| `muteSpeaker(callId, muted)` | 扬声器开关（关 = 不播放，仍继续接收） |
| `setMicGain(callId, 0..200)` | 麦克风增益，百分比整数。**0 = 禁用**（等同关麦克风），>0 自动解除禁用 |
| `setSpeakerVolume(callId, 0..100)` | 扬声器音量，百分比整数。**0 = 禁用** |

### 5.3 视频

| 方法 | 说明 |
| --- | --- |
| `setVideo(callId, enabled)` | 本次通话的**视频总开关**（关 = 不看远端、不推本地、视频窗口关闭） |
| `subscribeVideo(opts)` | 订阅画面并**由 SDK 渲染到 `<video>`**，见 §6 |
| `unsubscribeVideo()` | 取消本连接的全部订阅（软电话恢复显示自己的视频窗口） |
| `rotateVideo(channel?)` / `mirrorVideo(channel?)` | 画面旋转 +90° / 水平镜像开关；状态**存在软电话侧**，桌面窗口的按钮会同步变化 |
| `videoTransforms()` | 当前的旋转/镜像状态（`{remote:{rotation,mirror}, local:{...}}`） |
| `clearVideo()` | 把画面清成黑底（挂断/关闭视频时 SDK 会自动调用） |

### 5.4 通信录

网页侧**不做增删改查**，只提供导入/导出（合并语义：按号码去重、只覆盖号码/名称/备注、
**常用标志只增不减**）：

| 方法 | 说明 |
| --- | --- |
| `downloadContacts(filename?)` | 一键导出：浏览器直接下载 CSV（走 HTTP，默认 `contacts-YYYYMMDD.csv`） |
| `exportContacts()` | 取回 CSV 文本（WS），自行处理 |
| `importContactsFromFile()` | 一键导入：弹系统文件选择框选 CSV（走 HTTP） |
| `importContactsFile(fileOrBlob)` | 按 File/Blob 导入（配合 `<input type="file">` 或拖拽） |
| `exportContactsJSON()` / `importContactsJSON(x)` | 结构化 JSON 收发（REST），适合程序处理 |

导入结果统一为 `{added, updated, skipped, total}`。

### 5.5 状态与自动更新

| 方法 | 说明 |
| --- | --- |
| `getState()` | 状态快照：版本/平台、注册状态、通话列表（含音频/视频/通信录字段）、更新状态、自动接听开关 |
| `setAutoAnswer(enabled)` | 自动接听开关（与软电话界面按钮**共用一份状态**，变更广播 `system.autoAnswer`） |
| `setUpdateConfig(cfg)` | 写入软电话的**自动更新配置**（站点 URL / 应用 ID / 周期 / 预览版）。**软电话据此自己定时查询**更新站点 |
| `notifyUpdate(info)` | **强制推送**某个版本（联调用）：软电话弹提示，**由用户在软电话界面上确认后**才下载重启 |

> `notifyUpdate` 不触发任何自动下载——这是产品约束（REQ-UPD-009）：**WS 只能"告知"，
> 下载与重启必须由用户点击**。

## 6. 视频订阅（重点）

```js
const sub = await sdk.subscribeVideo({
  remote: document.getElementById('remoteVideo'),   // 必传：远端画面
  local:  document.getElementById('localVideo'),    // 可选：不传 = 不订阅本地回显
  maxWidth: 640, maxHeight: 480,                    // 两路共用默认值（单路可传对象覆盖）
  fps: 15, quality: 70,
  onStats: (channel, s) => console.log(channel, s.fps + 'fps', s.kbps + 'kbps'),
  keepWindow: false,                                // 见下
});

sub.rotate('remote');   // 也返回新的旋转/镜像状态
sub.transforms();
```

- **`remote` 必传且恒订阅**（不传会直接报错；软电话不会出现"窗口藏了、画面没人看"的状态）；
  `local` 不传 = 不订阅本地回显。
- **渲染由 SDK 完成**：内部用 canvas + `captureStream()` 喂给你的 `<video>`（已自动
  `muted` 并尝试 `play()`，绕开浏览器自动播放限制）。画面无音轨。
- 参数只是**上限**：超过 `maxWidth/maxHeight` 等比缩放；`fps` 是上限（编码跟不上就丢帧，
  不排队、延迟不累积）；越界值被**钳制**到合法范围并在返回值里给出生效值（不报错）。
- **`keepWindow`**（默认 `false`）：订阅后软电话**关闭它自己的视频窗口**（画面在网页看）；
  传 `true` 则两处同时显示。**多页面同时订阅时按"任一订阅者没要求保持就隐藏"汇总**。
- **订阅是连接级的**：连接断开自动退订；SDK 会在重连后**自动恢复**最近一次的订阅。
- 想自定义显示（不用 SDK 的 `<video>` 渲染）时，可监听低级的 `video.frame` 事件：
  `data = {channel, seq, blob(JPEG), size}`；`seq` 逐帧自增，可用来发现丢帧。
- 画面状态（协商成功、分辨率、开关）走 `call.video` 事件，见 §7。
- **视频不可用时会失败**：软电话配置页关了「启用视频通话」总开关、或该构建没有视频功能
  （Windows ARM64 基础版）时，`subscribeVideo` / `dial{video:true}` / `answer{video:true}`
  以 `code === 'video_unsupported'` reject——页面上要做好降级。

## 7. 事件参考

| 事件 | 何时来 | 负载要点 |
| --- | --- | --- |
| `connected` / `disconnected` | SDK 本地事件（**不派发给 `*`**） | — |
| `system.state` | 每次连接建立时 | 与 `getState()` 相同（含 `video` 旋转/镜像、`autoAnswer`） |
| `register.state` | 注册状态变化 | `{state, account, reason}`；`state`: `registered`/`registering`/`failed`/`unregistered` |
| `call.dialing` | 呼出已发起 | `{callId, call}` |
| `call.incoming` | 来电振铃 | `{callId, call}`；`call.hasVideo`、命中的 `call.contact{name,note,starred}` |
| `call.ringing` | 呼出振铃（180/183） | `earlyMedia: true` = 对端已下发回铃音 |
| `call.established` | 通话建立 | `{callId, call}` |
| `call.terminated` | 通话结束 | `reason`: `local_hangup`/`remote_hangup`/`remote_cancel`/`rejected`；`missed: true` = 未接来电 |
| `call.failed` | 呼叫失败 | `reason`: `busy`/`no_answer`/`not_found`/`failed` |
| `call.muted` | 保持状态变化 | `{callId, muted}` |
| `call.audio` | 麦克风/扬声器/音量变化 | `call.micMuted` / `speakerMuted` / `micGain` / `speakerVolume`；**事件合并，最多 150ms 一条** |
| `call.video` | 视频状态变化 | `call.video`: `{negotiated, enabled, previewOn, sending, width, height}` |
| `video.web` | 网页订阅状态变化 | `{active, local, remote, viewers, hideWindow}` |
| `video.transform` | 旋转/镜像变化（桌面或网页改的） | `{remote:{rotation,mirror}, local:{...}}` |
| `system.autoAnswer` | 自动接听开关变化 | `{enabled}` |
| `system.updateConfig` | 自动更新配置变化 | `{url, appId, intervalMinutes, prerelease}` |
| `system.update` | 自动更新状态变化 | `phase`: `idle`/`available`/`downloading`/`ready`/`failed`；含 `progress`、`error` 等 |

通话对象（`call`）字段：`id`、`direction`、`remote`、`remoteName`、`state`、`muted`、
`micMuted`、`speakerMuted`、`micGain`、`speakerVolume`、`hasVideo`、`video{...}`、`contact?`。

## 8. 避坑清单（都是实际踩过的）

1. **构造函数不会自动连接。** 忘了 `await sdk.connect()`，所有方法都会 reject
   `尚未连接软电话`。连接通常比想象中快，但**不要在 connect 之前发请求**。
2. **`callId` 不能猜。** 只在 `data.callId`（事件）和 `dial()` 的返回值里有。接听必须用
   `call.incoming` 里那一个；拿错 ID 会得到 `call_not_found`。
3. **端口不一定是 8765。** 软电话的 WS 端口被占用时会**自动 +1 并保存配置**（8775…），
   页面硬编码 8765 就会连不上。稳妥做法：`url` 由部署配置注入，或以软电话配置页
   显示的端口为准；默认安装且 8765 未被占用时用默认值即可（SDK 的默认值就是它）。
4. **`micGain = 0` 是"禁用"，不是"音量 0 但仍可用"。** 0 等同静音；禁用期间字段里保留的是
   "解禁后回到的音量"。做滑块 UI 时把 0 单独处理（显示为"关"）。
5. **`call.audio` 事件是合并过的**（最快 150ms 一条，首次立即、最后一次必发）。不要按
   "一条事件 = 一步调节"来累计 UI 状态——**以事件里的 `call` 快照为准**。
6. **`call.video` / `call.audio` 的 `call` 对象是整份快照**，直接覆盖本地状态即可，别做增量合并。
7. **视频订阅后软电话的视频窗口会消失**，这是设计（画面在网页看）。要保持两处同看传
   `keepWindow: true`；**多页面同时订阅时，只要有一个没要求 keepWindow，窗口就隐藏**。
8. **`video.frame` 不进事件日志**（30fps 会刷屏）；调试时别指望它出现在日志里。
9. **`subscribeVideo` 允许在通话开始前调用**（订阅一直有效，通话开始后画面自动推来）——
   但**视频总开关被关掉时订阅直接失败**（`video_unsupported`），页面要能降级到纯音频。
10. **`dial()` 成功 ≠ 接通**：它只表示 INVITE 已发出。通话状态以 `call.established` /
    `call.failed` / `call.terminated` 为准。
11. **通话中不要重复 `answer()` / `hangup()`**：软电话是幂等的，但你的 UI 状态机会乱；
    以事件为准驱动 UI，而不是以你调过的 API 为准。
12. **DTMF 只在通话接通后有效**，且 `digit` 必须是单个字符（`"12"` 会 `invalid_params`）。
13. **导入通信录的体积**：WS 的 `importContacts` 单条消息上限 4MB；**一键导入/导出
    （`importContactsFromFile` / `downloadContacts`）走 HTTP，上限 8MB 且无 WS 限制**——
    大文件优先用这两个。
14. **页面是 https 时**，`downloadContacts` 等 HTTP 调用访问 `http://127.0.0.1` 一般可行
    （环回地址被视为安全上下文），但若浏览器策略更严，会报 `Failed to fetch`——
    此时改用 WS 侧的 `exportContacts()` / `importContacts(csv)`。
15. **`on('*')` 收不到 `connected` / `disconnected`**（SDK 本地事件单独派发），需要单独订阅。
16. **一个页面一轮通话足够用**：软电话是单通话设计，别做"多人同时拨号"的界面。

## 9. 调试建议

- **开箱即用的测试页**：软电话自带 `http://127.0.0.1:8765/`（完整的拨号/接听/视频/日志面板），
  先用它确认账号与网络没问题，再调自己的页面。
- **页面上常驻显示状态**：`sdk.getState()` + `register.state` 事件足以定位绝大多数问题
  （没注册上就别怪拨号失败）。
- **看软电话的日志**：配置页有「打开日志目录」按钮；排查信令问题可临时勾选「记录 SIP
  信令」，日志里会有完整报文（密码/密钥已脱敏）。
- **错误优先看 `err.code`**：`not_ready`（软电话 SIP 层没起来）、`call_not_found`（callId 错）、
  `video_unsupported`（视频被关）、`invalid_params`（参数错）、`update_not_newer`（推送版本低于当前）。

## 10. 附：SDK 与软电话的版本关系

SDK 随软电话一起发布（内嵌在二进制里，`GET /sdk/sip-phone-sdk.js`），**版本永远与软电话一致**——
不存在单独升级 SDK 的问题。升级软电话后刷新页面即可拿到新 SDK；若页面缓存了旧 SDK，
加查询串（`?v=版本号`）或用 `cache: 'no-store'` 的方式引入。

需要更底层的控制（自己实现 WS 客户端、其他语言接入）时，看 [ws-protocol.md](ws-protocol.md)——
报文格式、方法、事件、错误码与本文一一对应。
