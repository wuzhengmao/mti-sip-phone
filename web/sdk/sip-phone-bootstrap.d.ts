/*!
 * sip-phone-bootstrap 类型声明（TypeScript）
 *
 * 运行时实现见同目录 sip-phone-bootstrap.js：本文件只描述类型、不产生运行时代码。
 *
 * 作用：应用只打包这份极小的引导器，真正的 SDK 每次从本机软电话动态加载——SDK 升级
 * 无需重新打包应用；软电话更新后 WS 重连会自动比对版本/构建时间并重载 SDK，页面无需刷新。
 *
 * **本文件自包含**：不 import/reference sip-phone-sdk.d.ts，单独拷贝也能通过类型检查
 * （避免「只带 bootstrap 声明」时编译报 TS2307）。`connect()` 返回的是**门面 `Facade`**
 * （不是 SDK 实例）：SDK 的方法/属性都经它转发到当前实例，热更新换实例时调用方无需感知。
 *
 *   import SipPhoneBootstrap = require('./sip-phone-bootstrap');
 *   const sdk = SipPhoneBootstrap.connect({
 *     url: 'ws://127.0.0.1:8765/ws',
 *     sipUri: 'sip://2999:pwd@10.168.4.40:15060',
 *     onClient: (client, meta) => {                // client: Facade（与直接用 SDK 的写法一致）
 *       console.log(meta.version, meta.buildTime);
 *     },
 *   });
 *   await sdk.ready;                               // 首次连接成功
 *   sdk.on('call.incoming', () => sdk.answer());   // 只订阅一次，热更新后自动重挂
 *
 * <script> 引入时暴露全局 SipPhoneBootstrap（由 export as namespace 提供）：
 * 把本文件纳入编译（tsconfig 的 include/files，或 /// <reference path="..." />）即可获得
 * 全局类型；在模块文件里访问该全局需开启 allowUmdGlobalAccess。
 *
 * 注意：导入路径不要带 .js 扩展名（与本目录 sip-phone-sdk.d.ts 的约定相同）。
 */
export = SipPhoneBootstrap;
export as namespace SipPhoneBootstrap;

declare namespace SipPhoneBootstrap {
  /**
   * 确保本地软电话正在运行：先探测 url/httpUrl 对应的端口，不可达时从该端口向上扫描；
   * 找到已运行的软电话直接返回，否则经 sip:// 协议拉起并等待就绪。
   *
   * @returns 软电话实际监听的 HTTP 根地址与版本/构建时间
   */
  function ensurePhone(options?: EnsureOptions): Promise<EnsureResult>;

  /**
   * 确保软电话运行并从它动态加载 SDK，返回 SDK 构造函数与指纹（**不建连**）。
   * 需要自己控制建连时机时用它：拿到 client 后自行 new + connect()。
   *
   * @typeParam C 客户端类型（默认 ClientLike；传 SipPhoneSDK 可得完整 SDK 类型）
   */
  function load<C = ClientLike>(options?: EnsureOptions): Promise<LoadResult<C>>;

  /**
   * 完整生命周期：确保软电话 → 从软电话动态加载 SDK → 建连；断线由引导器接管重连，
   * 连上后比对 version|buildTime 热更新 SDK。
   *
   * **返回一个稳定的门面 `Facade`（同步返回，不是 Promise）**：调用方拿到后一直用它
   * （`facade.on(...)` / `facade.dial(...)` / `facade.connected` …）。热更新换 SDK 实例时
   * 门面内部自动切换并重挂已登记的事件订阅，**业务代码不必改写，也不会接触到 SDK 实例**。
   * 首次连接结果走 `facade.ready`（Promise，失败 reject）；`onClient(facade, meta)` 可选，
   * 首次连接与每次热更新后各回调一次（用于打日志等）。
   *
   * **首次失败不等于放弃**：`ready` 只 reject 一次（应用据此提示/出「重新拉起」按钮），
   * 引导器随后转入后台重试（探测 → 扫描 → 拉起，指数退避），连上后照常触发 `connected`
   * 事件——所以「用户手动启动软电话后自动连上」不需要应用做任何事。
   */
  function connect(options?: ConnectOptions): Facade;

  /**
   * 在**用户点击事件处理函数里**调用，重新拉起最近一次的 sip:// 地址。
   * 浏览器要求外部协议拉起必须携带用户手势，断线回调里的自动拉起会被拒绝；
   * 此时经 onLaunchBlocked 让应用出按钮，用户点击时调用本方法完成拉起。
   *
   * @returns 有可用的拉起地址返回 true；从未拉起过（无地址）返回 false
   */
  function launchFromGesture(): boolean;

  /**
   * 动态设置/更新拉起地址：应用可能要到连上后（拿到配置）才知道 sip:// 地址，
   * 之后断线重连就按它拉起软电话。
   * @param uri sip:// 或 sips:// 地址；传空则清除（断线只探测、不拉起）
   * @returns 当前生效的拉起地址（null 表示未设置）
   */
  function setLaunchUri(uri?: string): string | null;

  /**
   * load() 返回的 SDK 构造函数所需的类型参数约束（浏览端最小接口）。
   * 传 `SipPhoneSDK` 可得完整 SDK 类型。
   */
  interface ClientLike {
    /** WebSocket 地址 */
    url: string;
    /** 建立连接 */
    connect(): Promise<void>;
    /** 主动断开（不再重连） */
    close(): void;
  }

  /**
   * 门面：对外唯一可见的对象（**不是 SDK 实例**）。除下面列出的成员外，
   * SDK 的全部方法与属性都经它转发到**当前**实例（热更新后自动指向新实例），
   * 所以可以像直接用 `SipPhoneSDK` 一样使用（`sdk.dial(...)` / `sdk.on(...)` / `sdk.connected`）。
   */
  interface Facade {
    /**
     * 首次连接成功的 Promise（resolve 门面本身）。**首次尝试失败时 reject 一次**，但引导器会继续在后台
     * 探测/拉起（之后连上靠 `on('connected')` 得知）；`close()` 也会让它 reject。
     */
    readonly ready: Promise<Facade>;
    /** WebSocket 地址（引导器校准实际端口时会更新它） */
    url: string;
    /** 订阅事件（登记在门面上，换实例后自动重挂；'*' 订阅全部） */
    on(event: string, fn: (data: any) => void): this;
    /** 取消订阅 */
    off(event: string, fn: (data: any) => void): this;
    /** 建立连接（连接由引导器托管，一般不用自己调；保留是为了与直接引 SDK 的写法一致） */
    connect(): Promise<void>;
    /** 主动断开，并**停止**引导器（不再重连/拉起） */
    close(): void;
    /** 同 close()：销毁当前实例并停止引导 */
    destroy(): void;
    /**
     * 订阅视频（参数与 SDK 的 subscribeVideo 相同）。**门面会记住这次订阅**：
     * SDK 热更新换实例后自动按同样参数重新订阅，调用方不必自己处理；
     * 同一实例的断线重连由 SDK 内部恢复。
     */
    subscribeVideo(options: any): Promise<any>;
    /** 取消订阅（同时清掉门面记住的参数，之后不再自动恢复） */
    unsubscribeVideo(): Promise<any>;
    /** 当前是否处于订阅状态（断线重连 / 热更新恢复后仍为 true） */
    readonly videoSubscribed: boolean;
    /** SDK 其余方法与属性（精确类型见 sip-phone-sdk.d.ts） */
    [key: string]: any;
  }

  /** ensurePhone 与 load 的入参。 */
  interface EnsureOptions {
    /** WebSocket 地址（默认 ws://127.0.0.1:8765/ws，端口同时作为端口扫描起点） */
    url?: string;
    /** 软电话的 HTTP 根地址（缺省由 url 推导，如 http://127.0.0.1:8765）；端口扫描沿用同一主机 */
    httpUrl?: string;
    /** sip:// 协议地址：软电话未运行时用它拉起（留空则只能探测，无法拉起） */
    sipUri?: string;
    /** 自动拉起被浏览器以「需要用户手势」拒绝、或拉起后未就绪时回调（一次断线只尝试拉起一次，因此最多回调一次） */
    onLaunchBlocked?: (info: LaunchBlockedInfo) => void;
  }

  /**
   * connect 的入参：bootstrap 专属项 + SDK 构造选项（原样透传）。
   * bootstrap 会摘掉 url/httpUrl/sipUri/onClient/onLaunchBlocked/logger（自己的参数），
   * 其余全部传给 SDK；赋给门面的属性（如 `sdk.unregisterOnDisconnect = true`）会被记住并在
   * **每个新实例**上重新套用（热更新后不丢；`url` 例外，它由引导器按实际端口维护）。
   */
  interface ConnectOptions {
    /** WebSocket 地址（默认 ws://127.0.0.1:8765/ws） */
    url?: string;
    /** 软电话的 HTTP 根地址（缺省由 url 推导） */
    httpUrl?: string;
    /** sip:// 协议地址：软电话未运行时用它拉起（不传给 SDK） */
    sipUri?: string;
    /** 首次连接与 SDK 热更新后回调（可选），入参是门面而非 SDK 实例 */
    onClient?: (client: Facade, meta: SDKMeta) => void;
    /** 自动拉起被浏览器拒绝或未生效时回调（应用据此显示「重新拉起」按钮） */
    onLaunchBlocked?: (info: LaunchBlockedInfo) => void;
    /** [SDK 透传] 断线自动重连，默认 true */
    autoReconnect?: boolean;
    /** [SDK 透传] 重连间隔毫秒，默认 3000 */
    reconnectDelay?: number;
    /** [SDK 透传] WS 断开时自动取消 SIP 注册，默认 false */
    unregisterOnDisconnect?: boolean;
    /** [SDK 透传] 连上后设置自动接听开关（不传 = 不改配置） */
    autoAnswer?: boolean;
    /** [SDK 透传] 更新站点根地址（连上后自动写入一次） */
    updateUrl?: string;
    /** [SDK 透传] 更新站点应用 ID，默认 mti-sip-phone */
    updateAppId?: string;
    /** [SDK 透传] 检查周期（分钟，1–1440） */
    updateIntervalMinutes?: number;
    /** [SDK 透传] 是否接受预览版 */
    updatePrerelease?: boolean;
    /** [SDK 透传] 日志函数，默认无 */
    logger?: (msg: string) => void;
    /** SDK 后续新增的构造选项同样原样透传（精确类型以 sip-phone-sdk.d.ts 为准） */
    [key: string]: any;
  }

  /** ensurePhone 的结果。 */
  interface EnsureResult {
    /** 软电话实际监听的 HTTP 根地址（如 http://127.0.0.1:8765） */
    httpBase: string;
    /** 软电话版本号 */
    version: string;
    /** 软电话构建时间（ISO 8601） */
    buildTime: string;
  }

  /** load 的结果。 */
  interface LoadResult<C = ClientLike> {
    /** 动态加载得到的 SDK 构造函数（构造参数与 SDK 一致，精确类型见 sip-phone-sdk.d.ts） */
    client: new (options: any) => C;
    /** 软电话版本号 */
    version: string;
    /** 软电话构建时间（ISO 8601） */
    buildTime: string;
    /** 软电话实际监听的 HTTP 根地址 */
    httpBase: string;
  }

  /** onClient 交给应用的版本/构建时间指纹。 */
  interface SDKMeta {
    /** 软电话版本号 */
    version: string;
    /** 软电话构建时间（ISO 8601） */
    buildTime: string;
  }

  /** 自动拉起失败的原因与地址（onLaunchBlocked 入参）。 */
  interface LaunchBlockedInfo {
    /** 待拉起的 sip:// 地址 */
    uri: string;
    /** 软电话应监听的 HTTP 根地址 */
    httpBase: string;
    /** no_user_gesture = 无用户手势被浏览器拒绝；launch_failed = 已尝试拉起但未就绪 */
    reason: 'no_user_gesture' | 'launch_failed';
  }
}
