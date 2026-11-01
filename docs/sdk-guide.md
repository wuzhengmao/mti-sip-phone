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

SDK 由软电话自身提供，**不需要把整份 SDK 打进页面**。推荐用**引导器**（bootstrap）加载：
应用只放一份极小的 `sip-phone-bootstrap.js`，SDK 每次从软电话动态加载，软电话更新后
**页面免刷新自动换新 SDK**（原理与更多用法见 [sdk-bootstrap.md](sdk-bootstrap.md)）。

```html
<!-- 引导器：从软电话自身下载（也可以打进你的应用包） -->
<script src="http://127.0.0.1:8765/sdk/sip-phone-bootstrap.js"></script>
<script>
  // 同步返回稳定的**门面**（不是 SDK 实例）：方法/属性始终指向当前实例，
  // 热更新换实例后自动切换，业务代码不用改；事件只订阅一次。
  const sdk = SipPhoneBootstrap.connect({
    onClient: (client, meta) => console.log('SDK 已加载', meta),  // 可选
  });
  sdk.on('call.incoming', () => sdk.answer());
</script>
```

如果不需要热更新，也可以直接引 SDK（简单场景）：

```html
<script src="http://127.0.0.1:8765/sdk/sip-phone-sdk.js"></script>
<script>
  const sdk = new SipPhoneSDK({ url: 'ws://127.0.0.1:8765/ws' });
  sdk.connect();
</script>
```

最小可用页面（**引导器版**，推荐）：

```html
<!DOCTYPE html>
<html lang="zh-CN">
<body>
  <button id="dial">呼叫 1001</button>
  <button id="hangup" disabled>挂断</button>
  <button id="relaunch" hidden>重新拉起软电话</button>
  <p id="status">未连接</p>

  <script src="http://127.0.0.1:8765/sdk/sip-phone-bootstrap.js"></script>
  <script>
    // 同步返回稳定的门面（不是 SDK 实例）：下面所有调用都经它转发到当前实例，
    // SDK 热更新换实例后自动切换，业务代码不需要改。
    const sdk = SipPhoneBootstrap.connect({
      url: 'ws://127.0.0.1:8765/ws',              // 可选，默认就是它
      sipUri: 'sip://2999@10.168.4.40:15060',     // 可选：软电话没在跑时用它拉起
      onClient: (client, meta) => console.log('SDK 已加载', meta.version, meta.buildTime),
      // 断线后的自动拉起可能被浏览器拦下（外部协议需要用户手势）：出个按钮让用户点
      onLaunchBlocked: () => { relaunch.hidden = false; },
    });

    // 事件只订阅一次：门面会在每个新实例上自动重挂
    sdk.on('connected',    () => status.textContent = '已连接软电话');
    sdk.on('disconnected', () => status.textContent = '已断开');

    sdk.on('register.state', (s) => {
      status.textContent = '注册状态：' + s.state;   // registered / registering / failed / unregistered
    });

    // 来电：这里先自动接听演示；实际产品应展示接听/拒接按钮
    sdk.on('call.incoming', () => {
      sdk.answer().catch((e) => console.error('接听失败', e.message));
    });

    sdk.on('call.established', (data) => {
      hangup.disabled = false;
      status.textContent = '通话中：' + data.call.remote;
    });

    sdk.on('call.terminated', () => {
      hangup.disabled = true;
      status.textContent = '通话结束';
    });

    dial.onclick = () => sdk.dial('1001');
    hangup.onclick = () => sdk.hangup();

    relaunch.onclick = () => {
      relaunch.hidden = true;
      SipPhoneBootstrap.launchFromGesture();   // 点击本身携带用户手势
    };
  </script>
</body>
</html>
```

最小可用页面（**直接引 SDK 版**，不需要热更新时用）：

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

    sdk.on('connected',    () => status.textContent = '已连接软电话');
    sdk.on('disconnected', () => status.textContent = '已断开');

    sdk.on('register.state', (s) => {
      status.textContent = '注册状态：' + s.state;   // registered / registering / failed / unregistered
    });

    // 来电：这里先自动接听演示；实际产品应展示接听/拒接按钮
    sdk.on('call.incoming', (data) => {
      sdk.answer().catch((e) => console.error('接听失败', e.message));
    });

    sdk.on('call.established', (data) => {
      hangup.disabled = false;
      status.textContent = '通话中：' + data.call.remote;
    });

    sdk.on('call.terminated', () => {
      hangup.disabled = true;
      status.textContent = '通话结束';
    });

    dial.onclick = () => sdk.dial('1001');
    hangup.onclick = () => sdk.hangup();

    sdk.connect().catch((e) => status.textContent = '连接失败：' + e.message);
  </script>
</body>
</html>
```

三件事记住就够起步：**`connect()`**（引导器版由引导器内部完成，需要等首次连上就 `await sdk.ready`——
首次失败会 reject，但引导器仍会在后台重试，连上时照常触发 `connected`；
直接引 SDK 时必须自己 `await sdk.connect()`）、**通话类操作不用传 `callId`**（软电话是**单通话**设计，
自己知道当前通话，不在通话会返回 `call_not_found`；`dial` 仍返回 `{callId}` 供显示）、
**事件用 `on('call.xxx', fn)` 订阅**。

## 3. 连接与生命周期

```js
const sdk = new SipPhoneSDK({
  url: 'ws://127.0.0.1:8765/ws',   // 默认值即此；端口随软电话配置变，见 §8.1
  autoReconnect: true,             // 默认 true：断线每 3 秒重连一次
  reconnectDelay: 3000,
  sipUri: 'sip://1001:密码@10.0.0.10',  // 可选：连不上就拉起 + 连上就登录，见下
  unregisterOnDisconnect: false,   // 可选：WS 断开自动取消注册，见下
  autoAnswer: true,                // 可选：连上后开启自动接听（不传 = 不改软电话配置）
});
await sdk.connect();
```

- **`connect()` 必须在业务操作前调用**（一般页面加载即调）。重复调用安全：已连接时直接 resolve。
- **`sipUri`**（REQ-SDK-007，原 `schemaUri` 改名并扩展）有两重作用：
  1. **连不上就拉起**：首次 WS 连接失败时（说明软电话没在跑），SDK 用该 URI 走系统
     `sip://` 协议**拉起软电话**（浏览器弹系统确认框）。**每页会话只尝试一次**；拉起后靠
     `autoReconnect` 自动连上。
  2. **连上就登录**：每次 WS 连接成功后，以 URI 里的注册信息（账号/密码/服务器/端口/
     传输）登录 SIP 服务器；**与软电话现有注册相同时自动忽略**（不打断通话、不重复注册）。
  `sips://` 前缀 = TLS 信令（默认端口 5061），`?transport=` 可显式指定传输。
  注意 URI 里的明文密码会出现在页面源码里，公共设备慎用。
- **`unregisterOnDisconnect`**（REQ-WS-020）：为 `true` 时，WS 断开（页面关闭/断网）后
  软电话**自动取消 SIP 注册**，重新连上后自动恢复注册。软电话侧按「**所有**连接断开后
  延迟确认（3 秒）」执行——页面刷新/瞬时断网不触发；**呼叫/振铃/通话中自动排队**，等
  话务结束才注销；等待期间重连则**取消**排队任务。默认 `false`；开关是应用级的（多个
  页面同时连时最后一次设置生效）。
- **`autoAnswer`**：为 `true` 时，连上软电话后开启自动接听（`false` = 关闭）。**不传 =
  不改软电话的自动接听配置**——只在构造参数里显式给了 `true`/`false` 才下发；断线重连后
  会重新下发（与 `unregisterOnDisconnect` 同口径，应用级、不持久化）。
- **断线行为**：连接断开时所有未完成的请求会 reject（`连接已断开`）；订阅过的事件在重连后
  依然有效；**视频订阅会被 SDK 自动恢复**（记住最近一次的订阅参数）。
- **`close()`**：主动断开且不再重连（页面卸载时可调）。
- **`destroy()`**：销毁实例——`close()` 之外再清事件订阅、未完成请求、心跳与视频渲染器
  （停掉 canvas 的 MediaStream、解绑 `<video>`）。引导器热更新换新 SDK 时会对旧实例调用它。
- **用引导器时**（`SipPhoneBootstrap.connect`）：**它返回的是稳定的门面，不是 SDK 实例**；
  **连接的建立/重连由引导器负责**（SDK 的 `autoReconnect` 被关掉，软电话退出后由引导器探测/拉起
  并按指数退避重连，不会对着死端口反复重试）。热更新换实例时门面自动切换，并**自动重挂已登记的
  事件订阅**——所以业务代码只写一次即可，也接触不到 SDK 实例。需要知道"第一次什么时候连上"用
  `await sdk.ready`；`sdk.close()` / `sdk.destroy()` 会停止引导（不再重连/拉起）。
  **`ready` 首次失败会 reject 一次，但引导器不放弃**：它会继续在后台探测（拉起只试一次，不再反复弹系统确认框；
  用户手动启动软电话、
  或点「重新拉起」按钮后都能自动连上），之后的状态以 `connected` / `disconnected` 事件为准。
  软电话**重启/安装更新**时连接会以关闭码 **4409** 断开，引导器只做原端口重连（不扫描/不拉起），
  连上后自动按指纹热更新 SDK——页面无需刷新。
  **视频订阅也由门面保管**：`sdk.subscribeVideo(...)` 成功后门面记住参数，热更新换实例后自动重订
  （同一实例的断线重连由 SDK 内部恢复）；`sdk.videoSubscribed` 可查当前是否在订阅，取消订阅会清掉记录。
- **心跳**：SDK 每 25 秒自动发一条空消息，避免被软电话读超时断开（读超时 180s，已给
  Chrome 后台节流留足余量）。自己实现 WS 客户端时也必须做这件事。

## 4. 核心概念

| 概念 | 说明 |
| --- | --- |
| **callId** | 每通电话的唯一标识（来自事件负载的 `data.callId` 或 `dial()` 的返回值）。**通话类方法不再需要你传 `callId`**——软电话是单通话，自己定位当前通话；`callId` 主要用于展示/日志/通话记录 |
| **Promise 错误** | 所有方法返回 Promise；失败时 reject 一个 `Error`，**`err.code`** 是错误码（见 §7），`err.message` 是中文描述 |
| **事件** | 软电话广播给**所有**已连接页面；`sdk.on('事件名', fn)` 订阅，`on('*')` 订阅全部（本地事件 `connected`/`disconnected` 除外） |
| **通话状态** | `ringing`（振铃）→ `established`（通话中）→ `terminated`（结束）；呼出另有 `call.dialing` |
| **单通话限制** | 软电话同一时刻只支持**一路通话**：通话中再 `dial` 会失败，新来电会被自动拒掉（486） |

## 5. API 参考

### 5.1 通话控制

| 方法 | 说明 |
| --- | --- |
| `dial(target, {video, name})` | 拨号。`target` 为号码或 SIP URI；`video: true` 发起**视频呼叫**；`name` = 对端显示名（如在线座席列表的 `displayName`，**优先于通信录名称**，为空照旧）。返回 `{callId}` |
| `answer({video})` | 接听。`video: true` = **视频接听**（开摄像头双向视频）；普通接听 = 不接受视频（对端也不再发） |
| `hangup()` | 挂断。话机按当前通话状态自动决定：**来电振铃 → 拒接（486）**、**呼出振铃 → 取消（CANCEL）**、**通话中 → 挂断（BYE）** |
| `hold()` / `unhold()` | **保持 / 取消保持**（走 SIP re-INVITE，对端会看到"保持"）。与麦克风开关是两回事 |
| `sendDTMF(digit)` | 发送 DTMF（RFC 4733），`digit` 为单字符 `0-9 * # A-D`；**仅通话接通后有效** |

### 5.2 音频控制（纯本地动作，不发 SIP 信令）

| 方法 | 说明 |
| --- | --- |
| `muteMic(muted)` | 麦克风开关（关 = 不发送，对端听不到） |
| `muteSpeaker(muted)` | 扬声器开关（关 = 不播放，仍继续接收） |
| `setMicGain(0..200)` | 麦克风增益，百分比整数。**0 = 禁用**（等同关麦克风），>0 自动解除禁用 |
| `setSpeakerVolume(0..100)` | 扬声器音量，百分比整数。**0 = 禁用** |

### 5.3 视频

| 方法 | 说明 |
| --- | --- |
| `setVideo(enabled)` | 本次通话的**视频总开关**（关 = 不看远端、不推本地、视频窗口关闭） |
| `subscribeVideo(opts)` | 订阅画面并**由 SDK 渲染到 `<video>`**，见 §6 |
| `unsubscribeVideo()` | 取消本连接的全部订阅（软电话恢复显示自己的视频窗口） |
| `rotateVideo(channel?)` / `mirrorVideo(channel?)` | 画面旋转 +90° / 水平镜像开关；状态**存在软电话侧**，桌面窗口的按钮会同步变化 |
| `videoTransforms()` | 当前的旋转/镜像状态（`{remote:{rotation,mirror}, local:{...}}`） |
| `clearVideo()` | 把画面清成黑底（挂断/关闭视频时 SDK 会自动调用） |

### 5.4 通信录

> 1.0.15 起**增删改查已开放**（原来只有导入导出）——无 UI 版没有桌面界面，通信录只能从
> 网页维护。

| 方法 | 说明 |
| --- | --- |
| `listContacts(filter?)` | 列表 `{count, total, contacts:[{number,name,note,starred}]}`；`filter.query` 交给软电话过滤——支持**音头拼音 / 全拼 / 汉字混合**（`zs`/`zhangsan`/`张三`/`张san` 命中「张三」）、号码与备注子串、号码纯数字写法，结果按拼音排序（与桌面通信录页同一份实现） |
| `saveContact(contact, prevNumber?)` | 新增或修改（`prevNumber` = 改号前的号码）；号码重复/为空以 `invalid_params` 拒绝 |
| `deleteContacts(numbers)` | 按号码（任意写法，自动规范化）删除 |
| `setContactsStarred(numbers, starred)` | 批量设置/取消常用（★） |
| `clearContacts()` | 清空（不可撤销） |

导入/导出（合并语义：按号码去重、只覆盖号码/名称/备注、**常用标志只增不减**）：

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
| `getState()` | 状态快照：版本/构建时间/平台、注册状态、通话列表（含音频/视频/通信录字段）、更新状态、自动接听开关、`noUI`（是否仅托盘无 UI 版）、`instance`（实例槽位）、`agentMode`（坐席模式，none=不在线） |
| `getBuildInfo()` | 当前加载的 SDK 对应的软电话版本与构建时间（`{version, buildTime}`），用于观察是否已热更新到最新构建 |
| `getCallPanel()` | 查询当前通话面板快照（`call.panel` 负载 `SipCallPanelState`），两个 panel 为 null 表示无通话 |
| `setAutoAnswer(enabled)` | 自动接听开关（与软电话界面按钮**共用一份状态**，变更广播 `system.autoAnswer`） |
| `register()` | **登录账号**（REQ-ACC-024）：启用账户并立即注册；结果经 `register.state` 事件回显。与托盘菜单「登录账号」一致 |
| `unregister()` | **退出账号**（REQ-ACC-024）：发 expires=0、停注册周期并禁用账户（之后启动/重连不再自动注册，直到 `register()` 或 sip:// 拉起） |
| `setUpdateConfig(cfg)` | 写入软电话的**自动更新配置**（站点 URL / 应用 ID / 周期 / 预览版）。**软电话据此自己定时查询**更新站点 |
| `notifyUpdate(info)` | **强制推送**某个版本（联调用）：软电话弹提示，**由用户在软电话界面上确认后**才下载重启 |

> `notifyUpdate` 只是"告知"（联调/回滚时的强制推送通道）。**下载与安装是另一组方法**
> （见 5.8），且必须由用户在页面上点击——软电话不做静默安装。

### 5.6 通话记录

| 方法 | 说明 |
| --- | --- |
| `queryCallLog({remote?, limit?, offset?})` | 分页查询（新的在前）：`remote` 按号码子串筛选，`limit` 默认 50、上限 200；返回 `{records, total, hasMore}` |
| `deleteCallLog(callIds)` | 删除若干条 |
| `clearCallLog()` | 清空（不可撤销） |

记录字段：`callId/direction/remote/remoteName/displayName/startedAt/answeredAt/endedAt/hasVideo/missed/reason`，
`displayName` 是**座席显示名**（座席列表直接呼出时记录，展示时优先于通信录名称）。
命中通信录时另有 `contact: {name, note, starred}`（软电话侧按号码规范化解析，与桌面通话记录页同一口径）。
**时间戳是毫秒整数，`0` 表示未发生**（`answeredAt: 0` = 未接通，`endedAt: 0` = 进行中）。

展示口径（与桌面一致）：已接通显示通话时长（`answeredAt→endedAt`），未接通来电显示振铃时长
（`startedAt→endedAt`）、呼出显示拨号时长；`missed: true` 标红。

### 5.7 配置读写

| 方法 | 说明 |
| --- | --- |
| `getConfig()` | 配置快照：账户（**不含密码**，只有 `passwordSet`）、音频（含 `ready`）/ 视频（含 `supported`/`cameraSupported`）、日志、`ws{port, actualPort}`、更新、开机自启 |
| `setAccount(patch)` | 改 SIP 账户：**只改传入的字段**；密码只写不读——`password: "..."` 改密码、`clearPassword: true` 清空，都不传 = 不改。保存后立即重新注册 |
| `listAudioDevices()` | 枚举音频设备 `{mics, speakers, ready, preprocessSupported}`。**`ready: false` = 还在枚举/不可用**（软电话侧最多等 3 秒先回空列表），页面应提示稍后重试而不是当成"没有设备" |
| `setAudioConfig(patch)` | 设备（`micId`/`speakerId`/`ringSpeakerId`）立即生效；`aec`/`ns`/`agc`（音频预处理）**下一通电话生效** |
| `listVideoDevices()` | 枚举摄像头 `{cameras, supported, cameraSupported}` |
| `setVideoConfig(patch)` | `enabled`（总开关）与摄像头立即生效；`width`/`height`/`fps`/`bitrateKbps` **下一通电话生效** |
| `setLogConfig(patch)` | 级别与 `sipTrace` 立即生效；`file`/`console` 重启后生效 |
| `setWSPort(port)` | 返回 `{port, actualPort, restartRequired}`；**重启后生效**，配合 `restartApp()` |
| `setAutoStart(enabled)` | 开/关开机自启（沙箱实例返回 `config_unavailable`） |
| `restartApp()` | 重启软电话。⚠️ resolve 之后进程随时退出、连接断开 |

### 5.8 触发更新（下载 / 安装）

```js
sdk.on('system.update', (st) => {
  // st.phase: idle / available / downloading / ready / failed
  if (st.phase === 'available') sdk.startUpdate();        // 开始下载
  if (st.phase === 'ready')     sdk.applyUpdate();        // 安装并重启
});
await sdk.checkUpdateNow();   // 立即检查站点（不用等下一个周期）
```

| 方法 | 说明 |
| --- | --- |
| `updateStatus()` | 更新状态快照（一般用事件，这里供初始化） |
| `startUpdate()` / `cancelUpdate()` | 开始 / 取消下载 |
| `applyUpdate()` | 安装并重启（要求 `phase === 'ready'`，否则 `invalid_params`） |
| `checkUpdateNow()` | 立即检查一次更新站点（20 秒超时） |

⚠️ 两点务必写进你的 UI：**必须由用户点击**（不要自动调用）；`applyUpdate()` 成功后
进程会替换自身并重启，**页面连接随即断开**。

> **仅托盘无 UI 版例外**（REQ-UPD-027）：无 UI 版通常不开控制台，软电话**自己**会下载并安装
> 新版本，有通话时等通话结束再重启。页面对这种实例不需要（也不应该）自动调用
> `startUpdate()` / `applyUpdate()`，`system.update` 事件照常推送进度。

### 5.9 CTI 坐席与话务（REQ-ACC-026 / REQ-ACC-034 / REQ-ACC-035）

坐席相关能力只在**坐席模式**（CTI 推过 `MSP_EVENT_AGENT_ONLINE`）下可用；非坐席模式调用
会被拒绝（code `cti_not_agent`）。指令走 out-of-dialog SIP NOTIFY（`Event: MSP_ACTION`），
**SDK 方法都会等平台回执（MSP_RESULT）后才 resolve**，不必自己维护 seq 与超时。

```js
const { agents } = await sdk.listAgents();   // 在线座席
await sdk.setAgentBusy(true);                // 置忙（false = 置闲；同 setIdle()/setBusy()）
await sdk.leave();                           // 离席（isLeave=true）
await sdk.resume();                          // 复席（isLeave=false，重新进入分配队列）
await sdk.setWork(60);                       // 话后整理（可选 workSeconds 整理时长秒数）
await sdk.takeover('2002');                  // 代接 2002 的振铃来电
await sdk.monitor('2002');                   // 班长监听
await sdk.stopMonitor();                     // 结束监听
await sdk.consult('2002');                   // 咨询（转移/三方见下表）
```

| 方法 | 说明 |
| --- | --- |
| `listAgents()` | 在线座席：`{agents:[{agent,displayName,acd,agentMode,agentStatus}], count}`（按座席号排序） |
| `setAgentBusy(busy)` / `setBusy()` / `setIdle()` | 坐席置忙/置闲；生效状态经 `cti.agent` 事件回显 |
| `leave()` / `resume()` | 坐席离席 / 复席（`isLeave` 置 `true`/`false`）；生效状态经 `cti.agent` 事件回显 |
| `setWork(workSeconds?)` | 坐席话后整理（`isWork=true`）；`workSeconds` 为整理时长（秒，可选，缺省取服务端上限）；生效状态经 `cti.agent` 事件回显 |
| `setConferenceMemberMuted(member, muted)` / `setConferenceMemberDeafened(member, deafened)` / `setConferenceMemberVideoMuted(member, muted)` | 会议成员会控（owner，参数 `member` 为成员号码） |
| `ctiAction(action, arg?)` | 通用入口：`arg` 为字符串 = `target`、数组 = `members`、对象 = 直接并入参数（如 `{member: "2002"}`） |
| `consult(target)` / `consultThreeWay(target)` | 咨询 / 咨询转三方 |
| `transfer(target)` / `consultTransfer(target)` | 直接转移 / 咨询转移 |
| `monitor(target)` / `stopMonitor()` / `takeover(target)` | 监听 / 结束监听 / 代接 |
| `exitThreeWay(target)` | 三方通话里强制挂断某一方（仅 owner） |
| `addConferenceMembers(members)` / `removeConferenceMembers(members)` | 加/减会议成员 |
| `finishConsult()` / `finishConference()` | 结束咨询 / 结束会议（owner） |
| `whisper()` / `stopWhisper()` / `intervene()` / `intercept()` | 监听耳语 / 退出耳语（恢复静默监听）/ 插话（强插）/ 强拦 |

失败错误码：`cti_failed`（平台拒绝，`message` 里带平台错误码）、`cti_busy`（已有指令在途，
同一时间只允许一条）、`cti_timeout`、`cti_not_agent`。

坐席状态统一走 **`cti.agent` 事件**（负载 `{displayName, agentMode, isBusy, isWork, isLeave}`，
**`agentMode=none` 表示不在坐席模式**）；`getState()` 里只留 `agentMode`（none = 不在线）。
**状态归纳**（离席优先级最高）：`isLeave=true` → 离席，
`isBusy=true && isWork=true` → 整理中，`isBusy=true` → 置忙，否则空闲。座席状态颜色约定与桌面一致：
空闲绿、置忙黄、整理中蓝、离席黄。在线座席列表（`listAgents`）的 `agentStatus` 颜色口径另见其字段说明：
`free` 绿、`dialing`/`ringing`/`talking` 黄、其余（`busy`/`work`/`leave`/`not_ready`/`miss_phone`/`offline`…）红。

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
| `connected` / `disconnected` | SDK 本地事件（**不派发给 `*`**） | `disconnected` 负载 `{code, reason}`：软电话**重启/安装更新**前会用关闭码 **4409** 关闭连接（`reason` = `restart`/`update`），其余为浏览器/网络的关闭码（如 `1006`）。用引导器时这些都由它处理，应用只需刷新自己的连接状态 |
| `system.state` | 每次连接建立时 | 与 `getState()` 相同（含 `video` 旋转/镜像、`autoAnswer`） |
| `register.state` | 注册状态变化 | `{state, account, reason}`；`state`: `registered`/`registering`/`failed`/`unregistered` |
| `call.dialing` | 呼出已发起 | `{callId, call}` |
| `call.incoming` | 来电振铃 | `{callId, call}`；`call.hasVideo`、命中的 `call.contact{name,note,starred}` |
| `call.ringing` | 呼出振铃（180/183） | `earlyMedia: true` = 对端已下发回铃音 |
| `call.established` | 通话建立 | `{callId, call}` |
| `call.terminated` | 通话结束 | `reason`: `local_hangup`/`remote_hangup`/`remote_cancel`/`rejected`；`missed: true` = 未接来电 |
| `call.failed` | 呼叫失败 | `reason` 是 SIP 信令的原因原文（如 `486 Busy Here`、`408 Request Timeout`、`SDP 协商失败: …`），可直接展示；网页控制台显示「呼叫失败：`<reason>`」。`ctiManaged=true`（CTI 接管，收到过 `CALL_RINGING`）时**不要**显示 SIP 原因——`cti.failed` 会给出统一中文消息 |
| `call.muted` | 保持状态变化 | `{callId, muted}` |
| `call.audio` | 麦克风/扬声器/音量变化 | `call.micMuted` / `speakerMuted` / `micGain` / `speakerVolume`；**事件合并，最多 150ms 一条** |
| `call.video` | 视频状态变化 | `call.video`: `{negotiated, enabled, previewOn, sending, width, height}` |
| `video.web` | 网页订阅状态变化 | `{active, local, remote, viewers, hideWindow}` |
| `video.transform` | 旋转/镜像变化（桌面或网页改的） | `{remote:{rotation,mirror}, local:{...}}` |
| `system.autoAnswer` | 自动接听开关变化 | `{enabled}` |
| `system.updateConfig` | 自动更新配置变化 | `{url, appId, intervalMinutes, prerelease}` |
| `contact.changed` | 通信录写入（桌面、网页、CSV/JSON 导入都算） | `{action, total}`；多标签/桌面据此刷新 |
| `calllog.changed` | 通话记录被删除/清空 | `{action, deleted}`（新增记录仍由 `call.terminated`/`call.failed` 驱动） |
| `system.config` | 配置被改写 | `{section, values}`，`section`: `account`/`audio`/`video`/`log`/`ws`/`autoStart` |
| `audio.ready` | 音频后端后台初始化完成 | `{ready}`；据此刷新设备下拉（无 UI 版没有桌面界面，这是唯一信号） |
| `cti.agent` | 坐席状态变化 | `{displayName, agentMode, isBusy, isWork, isLeave}`；`agentMode=none` 表示不在线（连接建立时在 `system.state` 之后、`call.panel` 之前补推一份） |
| `cti.state` | 通话的话务状态变化（双方通话生命周期 CALL_*，以及咨询/三方/监听/会议） | `{callId, call}`；`call.cti` 是完整 `CTIState`（`talkState` 等；双方通话另有 `mspCallId`（CTI 侧话务 ID）、`callPhase`（`dialing`/`ringing`/`talking`）、主被叫与接听坐席的名称字段）。`callPhase=ringing` 时把状态显示为「振铃中」；`CALL_FINISHED`/`CALL_FAILED` 由软电话挂断，页面收到 `call.terminated` 关掉通话卡片即可 |
| `cti.failed` | CTI 话务失败（CALL_FAILED / CONSULT_FAILED） | `{callId, kind, reason, message}`；`kind=call`（refuse 呼叫未接通 / cancel 呼叫已取消 / takeover 呼叫被代接）、`kind=consult`（refuse 咨询未接通 / cancel 咨询已取消），`message` 已翻成中文，直接 toast 即可 |
| `call.panel` | **通话面板快照**（REQ-WS-023 / REQ-SDK-010，由 CTI 的 `MSP_CALL_PANEL_UPDATED`/`MSP_CONFERENCE_PANEL_UPDATED` 整份面板驱动） | `{agentMode, isBusy, callPanel, conferencePanel}`。应用可直接用它渲染自己的通话面板。**字段保留协议名、不丢弃话机未用到的属性**（`callStatus`/`callerNo`/`calledNo`/`intercom`/`ringingTime`/`offhookTime` 等原样交给应用），唯一的改写是 `peer`/`member` 合并成 `{number,name,note}` 对象：`callPanel` = `{callId, agent?, callType?, callMode?, owner?, state, startTime, title, failCause?, callList}`。`callPanel.owner` 是**当前操作的发起方**（咨询=咨询方、三方=发起方、监听=班长；`callMode=normal` 时没有）：`normal` 才显示咨询/转移/会议入口；`consult` 时只有 owner 显示三方/咨询转移/结束咨询（被咨询方按普通通话显示）；`three-way` 时只有 owner 显示移除/强制挂断。`callPanel.title` 是状态文案、`callPanel.state` 决定显示哪些按钮（`dialing`/`ringing`/`incoming`/`talking`/`consult-dialing`/`consult-ringing`/`consult-talking`/`three-way`/`monitoring`/`whispering`）、`callList` 每项 = `{callerNo?, calledNo?, peer:{number,name,note}, intercom?, skillGroup?, callType?, callStatus, startTime, ringingTime?, offhookTime?, target?（监听时的被叫）}`。会议时 `conferencePanel` = `{confId, owner（主持人号码）, memberList:[{member:{number,name,note}, intercom, owner, state, timestamp}]}`（`member` 与 `peer` 同为对象；主持人第一行；`state` 透传 dialing/ringing/talking/holding 与会控 muted/deafened/vmuted）。**`peer`/`member` 的 `note` 只在协议没有给名称、回退到通信录时才有**（此时 `name` 也来自通信录）；协议已经给了名称时只有 `name`、不带备注。**两个 panel 互斥**：会议接通后只有 `conferencePanel`；被叫在会议邀请振铃阶段只有 `callPanel`（来电），接通后才切换。**两个 panel 都为 `null` 表示没有进行中的通话**（关闭面板） |
| `cti.result` | CTI 指令结果（MSP_RESULT） | `{seq, success, code, message, action, data?}`；用 `cti.action` 时通常不需要它 |
| `system.update` | 自动更新状态变化 | `phase`: `idle`/`available`/`downloading`/`ready`/`failed`；含 `progress`、`error` 等 |

**用 `call.panel` 快速搭一个通话面板**（不必解析 `call.*` / `cti.*` 明细）：

```js
sdk.on('call.panel', (p) => {
  if (!p.callPanel && !p.conferencePanel) return hidePanel(); // 没有通话
  if (p.conferencePanel) {                       // 会议（此时没有 callPanel）
    status.textContent = '会议中';
    return renderConference(p.conferencePanel);
  }
  status.textContent = p.callPanel.title;        // 拨号中 / 振铃中 / 咨询呼叫中 / …
  renderParties(p.callPanel.callList);           // [{peer:{number,name,note}, callStatus, startTime, …}]
  renderButtons(p.callPanel.state, p.callPanel.owner); // 按状态 + 发起方决定显示哪些按钮
});
```


通话对象（`call`）字段：`id`、`direction`、`remote`、`remoteName`、`peerName?`（INVITE 的
Call-Info display-name，显示名称时优先于通信录）、`consult?`（本通是服务端推来的咨询腿）、
`state`、`muted`、`micMuted`、`speakerMuted`、`micGain`、`speakerVolume`、`hasVideo`、
`video{...}`、`contact?`、`cti?`（复杂话务状态：`talkState` 与咨询/三方/监听/会议各字段）。

## 8. 避坑清单（都是实际踩过的）

1. **构造函数不会自动连接。** 忘了 `await sdk.connect()`，所有方法都会 reject
   `尚未连接软电话`。连接通常比想象中快，但**不要在 connect 之前发请求**。
2. **通话类方法不用传 `callId`。** 软电话是**单通话**设计，`answer()` / `hangup()` 等自己
   定位当前通话，不在通话会返回 `call_not_found`；`callId` 只在 `data.callId`（事件）和
   `dial()` 的返回值里，供展示/日志用。
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
17. **用引导器时拿到的不是 SDK 实例**：`SipPhoneBootstrap.connect()` 返回的是稳定门面，方法/属性
    都经它转发；**不要**再去 `onClient` 里拿实例长期缓存（它只是观察点）。视频订阅由门面保管并自动
    恢复，不用自己在 `connected` 里重订。
18. **拉起软电话需要用户手势**：浏览器只允许用户点击后（约 5 秒内）触发外部协议；断线回调里的
    自动拉起会被**静默拒绝**。接 `onLaunchBlocked` 出个「重新拉起」按钮、点击时调
    `SipPhoneBootstrap.launchFromGesture()`。**首次连接失败（`ready` reject）不等于结束**——
    引导器会继续在后台探测（拉起只试一次），之后连上照样触发 `connected`。
19. **控制台/页面的静态资源与 SDK 都内嵌在软电话里**：改了 `web/**` 必须**重建二进制 + 重启**才生效；
    排查"怎么改都不生效"时先确认跑的是不是新构建（`bootstrap` 的日志会打印 `build=` 时间）。

## 9. 调试建议

- **开箱即用的网页控制台**：软电话自带 `http://127.0.0.1:8765/`（通话 / 通话记录 / 通信录 / 配置 / 诊断五页，完全用本 SDK 写的，可直接当示例读），
  先用它确认账号与网络没问题，再调自己的页面。
- **页面上常驻显示状态**：`sdk.getState()` + `register.state` 事件足以定位绝大多数问题
  （没注册上就别怪拨号失败）。
- **看软电话的日志**：配置页有「打开日志目录」按钮；排查信令问题可临时勾选「记录 SIP
  信令」，日志里会有完整报文（密码/密钥已脱敏）。
- **错误优先看 `err.code`**：`not_ready`（软电话 SIP 层没起来）、`call_not_found`（callId 错）、
  `video_unsupported`（视频被关）、`invalid_params`（参数错）、`update_not_newer`（推送版本低于当前）。

## 10. 附：SDK 与软电话的版本关系

SDK 随软电话一起发布（内嵌在二进制里，`GET /sdk/sip-phone-sdk.js`），**版本永远与软电话一致**——
不存在单独升级 SDK 的问题。两种加载方式：

- **引导器（推荐）**：页面只打包 `sip-phone-bootstrap.js`，SDK 每次从软电话动态加载；软电话升级后
  **无需刷新页面**，重连时按 `version|buildTime` 指纹自动加载新 SDK（见 [sdk-bootstrap.md](sdk-bootstrap.md)）。
- **直接引 SDK**：升级软电话后刷新页面即可拿到新 SDK；若页面缓存了旧 SDK，加查询串（`?v=版本号`）
  或用 `cache: 'no-store'` 的方式引入。

需要更底层的控制（自己实现 WS 客户端、其他语言接入）时，看 [ws-protocol.md](ws-protocol.md)——
报文格式、方法、事件、错误码与本文一一对应。
