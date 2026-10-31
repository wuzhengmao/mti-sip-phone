# SDK 动态加载方案（bootstrap + SDK）

> **读者**：接入方前端、以及维护这套加载机制的软电话开发者。
> **目标**：理解「稳定 bootstrap + 动态加载 SDK」的实现方式，并会接入与验证。
> **配套**：[docs/sdk-guide.md](sdk-guide.md)（SDK 本身怎么用）、[docs/ws-protocol.md](ws-protocol.md)（协议）、[docs/auto-update.md](auto-update.md)（自动更新）。

---

## 1. 要解决的问题

以前应用把 `sip-phone-sdk.js` **打进自己的包里**，带来两个痛点：

1. **SDK 每次升级都要重新打包/发版应用**——SDK 和业务代码耦合在一起；
2. **软电话更新后要刷新页面才生效**——软电话自动更新替换了内置 SDK，但页面里跑的还是旧副本，得手动刷新。

方案：把 SDK 拆成「**极小的 bootstrap**（几乎不变）+ **从软电话动态加载的 SDK**」。应用只打包 bootstrap，SDK 每次从本机软电话加载；WS 断线重连后自动比对版本+构建时间，变了就重新加载 SDK，**免打包、免刷新**。

```
你的应用 ──引用──▶ sip-phone-bootstrap.js（稳定，打进应用包）
                        │ 1. 探测/拉起软电话（sip:// 协议）
                        │ 2. 从软电话加载 SDK：http://127.0.0.1:<port>/sdk/sip-phone-sdk.js?v=<version>|<buildTime>
                        ▼
                   SipPhoneSDK（动态加载，随软电话更新）
                        │ 3. WS 重连后重新比对 version|buildTime，变了就重载
                        ▼
                   本地软电话（托盘常驻）
```

---

## 2. 实现方式

### 2.1 软电话侧

| 能力 | 说明 |
|---|---|
| SDK 与 bootstrap 内嵌 | `web/sdk/sip-phone-sdk.js` / `sip-phone-bootstrap.js` 经 `go:embed` 内嵌（`web/embed.go`），HTTP 直接提供 `/sdk/sip-phone-sdk.js`、`/sdk/sip-phone-bootstrap.js`（一律 `Cache-Control: no-cache`） |
| 版本 + 构建时间 | `core.Version`（版本）与 `core.BuildTime`（ISO 8601，`build.sh`/`build.bat` 经 `-ldflags` 注入）。两者拼成 SDK 指纹 |
| 状态接口 | `GET /api/status` 返回 `version` + `buildTime`（另含 `os`/`arch`），供 bootstrap 探测与指纹比对 |
| CORS | HTTP 响应统一带 `Access-Control-Allow-Origin: *`（`internal/wsapi/server.go` 的 `withCORS`，并处理 OPTIONS），让应用页面能跨源 `fetch /api/status` |
| WS 端口 | 默认 8765，被占用自动 +1（`FindFreeWSPort`）；沙箱实例（`-instance N`）端口起点 = 8765 + 10×N |

### 2.2 bootstrap 侧（`sdk/sip-phone-bootstrap.js`）

对外 5 项能力（`ensurePhone` / `load` / `connect` 返回 Promise，另两个是同步工具方法）：

| 方法 | 作用 |
|---|---|
| `ensurePhone(options)` | 只确保软电话运行，返回 `{httpBase, version, buildTime}` |
| `load(options)` | 确保软电话 + 从软电话加载 SDK，返回 `{client, version, buildTime, httpBase}` |
| `connect(options)` | 完整生命周期：确保软电话 → 加载 SDK → 建连；**断线后把控制交回 bootstrap**（探测/扫描/拉起，指数退避）再重连，连上后比对指纹**热更新 SDK**。**同步返回一个稳定的门面 `Facade`**（不是 SDK 实例，见下） |
| `setLaunchUri(uri)` | 动态设置/更新 `sip://` 拉起地址（应用连上拿到配置后再告知；断线自动拉起用），返回当前生效地址 |
| `launchFromGesture()` | **在用户点击处理函数里调用**，重新拉起最近一次的 `sip://` 地址（绕过浏览器的「必须用户手势」限制）；有可用地址则发起拉起并返回 true，否则返回 false |

关键实现点：

- **地址解析走 `new URL`**：`ws(s)://` 与 `http(s)://` 的互转、`host:port` 的拆分都用 URL 解析，不靠字符串截断——
  IPv6（`ws://[::1]:8765/ws`）里的冒号不会被当成端口，`wss://` 也会正确映射成 `https://` 探测。
- **探测用 `/api/status` 而非 WS**：`fetch` 能区分三种结果——`phone`（返回含 `version` 的 JSON）、`occupied`（端口有服务但不是软电话）、`free`（连接被拒）。这样「软电话已运行、端口被占用」不会被误判成空闲而重复拉起。
- **端口扫描沿用 httpBase 的主机**：默认回环，配置了远程地址（「允许远程连接」）或 `httpUrl` 时扫同一台机器的相邻端口，不会误连本机另一个实例。
- **端口探测**：从 `url` 端口（默认 8765）往上扫，最多 10 个——软电话默认 8765、占用自动 +1、沙箱实例 +10×槽位，10 个内基本能命中。
- **拉起走 `sip://` 协议**：拉起前先探测一个**空闲端口**，经 `?listen=<端口>` 告诉软电话绑它，拉起后**只等这个已知端口**（不再扫端口）；软电话侧把 `listen` 作为 WS 端口的起始值（被占用才 +1 并保存）。找不到空闲端口时回退扫描起点（不是硬编码 8765，沙箱实例不会被拉成主实例的端口）。
- **账号/密码走查询参数**：浏览器会拦截 URL 里带 `user:pass@` 的子资源请求（`embedded credentials are blocked`），因此引导器拉起时把 userinfo 里的账号/密码改写成 `?account=&password=`（原有 `transport`/`instance`/`updatesite` 参数保留）。软电话两种形式都支持，userinfo 优先。**拉起日志里的密码会脱敏**（`password=***`）。
- **指纹与缓存戳**：`fingerprint = version + '|' + buildTime`，加载 SDK 时作为 `?v=` 缓存戳，保证拿到最新一份。
- **自动重载**：`connect()` 内部钩住 SDK 的 `connected` 事件，每次（重）连上后重新 `fetch /api/status` 比对指纹，变了就重新注入 SDK 脚本、`new SipPhoneSDK(...)` 重建连接，并回调 `onClient(facade, meta)`（入参是门面，不是实例）。**换实例前对旧实例调用 `destroy()`**（`close()` + 清事件订阅/未完成请求/心跳/视频渲染器，停掉 canvas 的 MediaStream、解绑 `<video>`），旧实例随后可被回收。加载/热更新都会打日志：`[bootstrap] 已加载 SDK v… build=…`、`检测到新构建（旧 → 新），重新加载 SDK`、`SDK 已热更新：v… build=…`、`已连接软电话 … SDK 无变化`。
- **重启/安装更新走关闭帧**：软电话在重启前用关闭码 **4409** 关闭 WS（`reason` = `restart`/`update`），
  引导器据此**只做原端口重连**（短退避轮询 `/api/status`，不扫描、不拉起，因此不受浏览器用户手势限制），
  连上后再比对指纹按需热更新；原端口若被别的服务占了（连续 3 次 `occupied`）就提前回落到常规流程，120 秒还没回来同样回落。
- **断线后把控制交回 bootstrap**：SDK 的 `autoReconnect` 会被引导器关掉（`o.autoReconnect=false`），改由 bootstrap 在 `disconnected` 事件里循环「探测 `/api/status` → 扫描端口 → 必要时拉起 `sip://` → 等待」这一套；软电话回来后更新端口、重连，连上再比对指纹按需热更新。**一次断线只拉起一次**：首轮没找到软电话时尝试拉起，失败后后续轮次只做探测/扫描（不再反复拉起，避免反复弹系统确认框；无用户手势时浏览器本来也会拒绝），失败按**指数退避**（1s → 2s → … → 上限 30s）重试；**连上后重置该标志**，下一次断线可以再尝试拉起一次。用户手动启动软电话同样会被探测到并连上。
- **首次失败也不放弃**：首次 `_ensurePhone` 失败（软电话没在跑、自动拉起被拦）时 `ready` 只 **reject 一次**，
  引导器随即转入后台重试（**只探测/扫描，不再重复拉起** + 指数退避）；软电话起来后它**补上 SDK 加载与实例创建**，
  页面照样收到 `connected`。所以「先打开页面、再手动起软电话」也能自动连上（历史缺陷：以前首次失败就彻底停了）。

### 2.2.1 ⚠️ 浏览器限制：拉起外部协议必须有用户手势

Chrome/Edge 对**外部协议（`sip://`）拉起**强制要求 *transient user activation*（约 5 秒激活窗口）。
定时器、断线回调里的**自动拉起会被静默拒绝**，控制台报：

```
Not allowed to launch 'sip://...' because a user gesture is required.
```

因此：

- **首次拉起**通常没问题——它紧跟在用户点击「连接」之后，还在手势窗口内；
- **断线后的自动拉起会被拦截**。引导器**仍然会尝试拉起**（不预先判断手势）；拉起后若在超时内等不到
  `/api/status`，才回调 `onLaunchBlocked`，并且**在通知时才按当时的手势决定 `reason`**：
  `navigator.userActivation.isActive === false` → `'no_user_gesture'`（被浏览器拦），否则 `'launch_failed'`
  （软电话未安装/启动失败/用户取消系统确认框）。之后仍按指数退避继续重试，但**不再重复拉起**
  （一次断线只拉一次；连上后重置，下一次断线再试一次）——手动起软电话也能被探测到并连上。
  应用可据此提供按钮，用户点击时调用 `SipPhoneBootstrap.launchFromGesture()` 完成拉起（点击本身携带用户手势）。

无法绕过（这是浏览器安全策略，只有企业策略 `AutoLaunchProtocolsFromOrigins` 能放行白名单来源）。

### 2.3 SDK 侧（`sdk/sip-phone-sdk.js`）

- 新增 `getBuildInfo()`：返回 `{version, buildTime}`（来自 `system.getState` 的 `version`/`buildTime`），用于直观判断当前加载的是哪一版 SDK。
- 新增 `destroy()`：销毁实例（供引导器热更新时调用），见上。
- 其余公开 API 不变（`connect/close/dial/answer/hangup/...`），见 [docs/sdk-guide.md](sdk-guide.md)。

> **视频订阅由门面自动恢复**：`sdk.subscribeVideo(opts)` 成功后门面记住参数，热更新换实例后
> 自动在新实例上重订（同一实例的断线重连由 SDK 内部恢复）；`sdk.videoSubscribed` 可查状态，
> `unsubscribeVideo()` 会清掉记录、之后不再自动恢复。**通话结束不会清掉订阅**（订阅是连接级的）。

---

## 3. 开发怎么用（demo）

### 3.1 外部应用：最省事——`connect()`

```html
<script src="sip-phone-bootstrap.js"></script>
<script>
  // 同步返回**稳定的门面**：方法/属性始终落在当前实例上，热更新换实例时自动切换，
  // 事件订阅只登记一次（bootstrap 会在每个新实例上重挂）。
  const sdk = SipPhoneBootstrap.connect({
    url: 'ws://127.0.0.1:8765/ws',                 // 可选，默认 8765
    // 可选：软电话未运行时用它拉起。账号/密码会被改写成 ?account=&password=（浏览器拦 user:pass@）
    sipUri: 'sip://2999:pwd@10.168.4.40:15060',
    // 可选：首次连接与 SDK 热更新后各回调一次（入参是门面，不是 SDK 实例）
    onClient: (client, meta) => console.log('已加载 SDK', meta.version, meta.buildTime),
    // 断线后自动拉起被浏览器拦截（需要用户手势）：显示一个按钮，点击时调用 launchFromGesture()
    onLaunchBlocked: () => {
      const btn = document.getElementById('relaunch');
      btn.hidden = false;
      btn.onclick = () => { btn.hidden = true; SipPhoneBootstrap.launchFromGesture(); };
    },
  });

  sdk.on('call.incoming', () => sdk.answer());   // 只订阅一次
  sdk.on('call.established', (d) => console.log('通话中', d.call.remote));

  // 首次连接成功后拨号（首次失败会 reject，但引导器会在后台继续重试，
  // 连上时照常触发 connected 事件——状态以事件为准）
  sdk.ready.then(() => sdk.dial('1001')).catch((e) => console.warn('首次连接失败:', e.message));
</script>
```

要点：

- **拿到的 `sdk` 是门面，不是 SDK 实例**——SDK 的所有方法/属性都经它转发，用法与 `new SipPhoneSDK()`
  完全一致；热更新换实例后**业务代码不用改**，也接触不到实例本身。
- **事件只订阅一次**：门面把订阅登记在自己身上，换实例时自动重挂（不再需要用 `onClient` 重挂）。
  视频订阅同理：门面记住 `subscribeVideo` 的参数，热更新后自动重订。
- `onClient(facade, meta)` 是可选的观察点（打日志/做提示用），首次连接与每次热更新各回调一次。
- `sdk.close()` / `sdk.destroy()` 会**停止引导**（断开且不再重连/拉起）。

### 3.2 细粒度：`load()` / `ensurePhone()`

需要自己控制建连时机时用：

```js
// 只加载 SDK 类 + 指纹，自己建连（不热更新：自己管理实例生命周期）
const api = await SipPhoneBootstrap.load({
  url: 'ws://127.0.0.1:8765/ws',
  sipUri: 'sip://2999:pwd@10.168.4.40:15060',
});
console.log(api.version, api.buildTime, api.httpBase);
const client = new api.client({ url: 'ws://127.0.0.1:8765/ws' });
client.connect();

// 只确保软电话运行（比如应用启动时预热）
const meta = await SipPhoneBootstrap.ensurePhone({ sipUri: 'sip://2999:pwd@10.168.4.40:15060' });
```

### 3.3 TypeScript 接入

声明文件：`sdk/sip-phone-bootstrap.d.ts`（UMD，全局 `SipPhoneBootstrap`）。它是**自包含**的——
不 import/reference `sip-phone-sdk.d.ts`，单独拷贝也能通过编译（避免 TS2307）。`Facade` 用索引签名
转发 SDK 的其余成员（类型为 `any`）；需要精确类型时把 SDK 声明一并纳入工程后自行标注
（例如 `const b: { version: string; buildTime: string } = await sdk.getBuildInfo()`），或改用 `load<SipPhoneSDK>()`
自己建实例。

```ts
import SipPhoneBootstrap = require('./sip-phone-bootstrap');

const sdk = SipPhoneBootstrap.connect({
  url: 'ws://127.0.0.1:8765/ws',
  sipUri: 'sip://2999:pwd@10.168.4.40:15060',
  onClient: (client: SipPhoneBootstrap.Facade, meta: SipPhoneBootstrap.SDKMeta) => {
    console.log(meta.version, meta.buildTime);
    // 门面转发 SDK 的全部方法（`client` 就是 sdk 本身，这里只是类型标注成 Facade）
    client.getBuildInfo().then((b: any) => console.log('实际 build =', b.buildTime));
  },
  onLaunchBlocked: (info: SipPhoneBootstrap.LaunchBlockedInfo) => {
    if (info.reason === 'no_user_gesture') SipPhoneBootstrap.launchFromGesture();
  },
});
```

`connect()` 没有泛型参数（返回的始终是 `Facade`）；`load()` 才有 `load<C>()`，用于拿 SDK 构造函数的精确类型。

### 3.4 观察是否加载到最新 SDK

重新构建并重启软电话后（`buildTime` 会变），页面无需刷新：

```js
// sdk 是 connect() 返回的门面；getBuildInfo 由门面转发到当前实例
const build = await sdk.getBuildInfo();   // { version, buildTime }
// 显示出来，对比「预期版本 + 最新构建时间」即可确认 SDK 是否已热更新
```

引导器自身也会打日志（浏览器控制台）：`[bootstrap] SDK 已热更新：v… build=…` / `已连接软电话 … SDK 无变化`。

### 3.5 本地联调验证

```bash
# 1. 构建并启动软电话（Windows 示例，版本带 -rc）
docker/build.sh --target windows-amd64
./dist/sip-phone-windows-amd64.exe

# 2. 打开一个外部页面接入 SipPhoneBootstrap.connect()，记录 getBuildInfo() 的 build 值。

# 3. 改点代码重新构建、重启软电话（会重新注入新的 BuildTime）：
docker/build.sh --target windows-amd64
#    结束旧进程后按原 sip:// 参数重新拉起

# 4. 回到外部页面，无需刷新：WS 自动重连后，getBuildInfo() 的 build 值应更新为新值。
```

---

## 4. 约定与注意事项

- **SDK 与 bootstrap 都内嵌在软电话里**：`web/sdk/` 下的 `.js`/`.d.ts` 经 `go:embed` 编进二进制，经 `/sdk/*` 提供；改完**必须重新构建 + 重启**软电话才生效。
- **bootstrap 是浏览器专用**：依赖 `fetch` + `<script>` 注入 + `sip://` 拉起，不适用于纯 Node 环境。
- **bootstrap 独占拉起**：`connect()` 会把 `sipUri` 从 SDK 构造参数里摘掉（不传给 SDK），拉起统一由 bootstrap 负责，避免 SDK 自带的拉起逻辑和 bootstrap 重复。
- **一个页面只用一份 `connect()`**：拉起地址与「最近一次拉起地址」在模块内共享（`setLaunchUri` / `launchFromGesture` 都是全局的），多份门面会互相覆盖；确需多个入口时共用一个门面即可。
- **`buildTime` 是开发态热更新的关键**：`-rc` 版本号不变、但每次构建 `buildTime` 都会变，靠它才能区分「同版本不同构建」。
- **回退安全**：10 个端口都探不到空闲端口时就回退扫描起点（软电话自己会再 +1）；拉起失败/加载失败都走 Promise reject（首次失败后引导器仍会在后台重试），由应用自行兜底。
- **内置控制台同样走引导器**：`web/index.html` 只引 `sip-phone-bootstrap.js`，`web/js/store.js` 拿
  `SipPhoneBootstrap.connect` 同步返回的**门面**当单例（方法调用始终转发到当前实例），热更新后界面无感换新；
  连上后从配置派生 `sip://` 经 `setLaunchUri` 交给引导器，软电话退出也能自动拉起。
- **`ready` 只代表「首次连接成功与否」**：首次失败会 reject 一次，但引导器继续在后台重试；
  页面如果要显示连接状态，应以 `connected` / `disconnected` 事件为准（不要只依赖 `ready`）。
