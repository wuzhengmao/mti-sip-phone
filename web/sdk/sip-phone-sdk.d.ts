/**
 * mti-sip-phone 网页端 JS SDK 类型声明
 */

declare class SipPhoneSDK {
  constructor(options?: {
    /** 软电话 WS 地址，默认 ws://127.0.0.1:8765/ws */
    url?: string;
    /**
     * 拉起软电话的系统协议 URI（如 sip://账号:密码@服务器）。
     * 设置后：首次 WS 连接失败时自动通过该 URI 拉起软电话，
     * 之后失败不再自动拉起（每页会话仅一次）。
     */
    schemaUri?: string;
    /** 断线自动重连，默认 true */
    autoReconnect?: boolean;
    /** 重连间隔毫秒，默认 3000 */
    reconnectDelay?: number;
    /**
     * 更新站点根地址（如 http://update.example.com）。设置后 SDK 连上软电话会
     * **定时**查询该站点有没有更高版本，有则自动告知软电话（system.notifyUpdate）；
     * 不传 = 不自动查询。查询结果见 update.check 事件。
     */
    updateUrl?: string;
    /** 更新站点上的应用 ID，默认 'mti-sip-phone' */
    appId?: string;
    /** 查询间隔毫秒，默认 5 分钟（钳制到 60 秒 ~ 24 小时） */
    updateInterval?: number;
    /** 发布通道（如 beta），默认站点默认通道 */
    updateChannel?: string;
    /**
     * 是否接受预发布版；不传 = 自适应（本地版本含 "-" 如 1.0.5-rc 时视为 true，
     * 正式版只看正式版）。
     */
    updatePrerelease?: boolean;
  });

  /** 是否已连接 */
  readonly connected: boolean;

  /** 建立连接 */
  connect(): Promise<void>;

  /** 主动断开（不再自动重连） */
  close(): void;

  /**
   * 订阅事件；'*' 订阅全部。
   * 事件名：call.incoming / call.ringing / call.established / call.terminated /
   *         call.failed / call.muted / call.audio（麦克风/扬声器开关）/
   *         call.dialing / call.video / video.web /
   *         register.state / system.state / system.update /
   *         video.frame（订阅视频后逐帧推送）/ update.check /
   *         connected / disconnected
   * 注：SDK 本地事件（connected / disconnected / update.check）不会派发给 '*' 订阅者。
   */
  on(event: string, fn: (data: any) => void): this;

  /** 取消订阅 */
  off(event: string, fn: (data: any) => void): this;

  /** 拨号；options.video=true 发起视频呼叫 */
  dial(target: string, options?: { video?: boolean }): Promise<{ callId: string }>;

  /** 视频呼叫（dial 的便捷写法） */
  dialVideo(target: string): Promise<{ callId: string }>;

  /** 接听；options.video=true 视频接听（打开摄像头双向视频） */
  answer(callId: string, options?: { video?: boolean }): Promise<{ callId: string }>;

  /** 视频接听（answer 的便捷写法） */
  answerVideo(callId: string): Promise<{ callId: string }>;

  /** 挂断 */
  hangup(callId: string): Promise<{ callId: string }>;

  /** 拒接，code 默认 486 */
  reject(callId: string, code?: number): Promise<{ callId: string }>;

  /** 保持（静音）切换 */
  mute(callId: string, muted: boolean): Promise<{ callId: string }>;

  /** 麦克风开关（纯本地动作；状态变化推 call.audio 事件） */
  muteMic(callId: string, muted: boolean): Promise<{ callId: string }>;

  /** 扬声器开关（纯本地动作，继续接收） */
  muteSpeaker(callId: string, muted: boolean): Promise<{ callId: string }>;

  /** 发送 DTMF（RFC 4733，通话接通后有效）；digit 为单个字符：0-9 * # A-D */
  sendDTMF(callId: string, digit: string): Promise<{ callId: string }>;

  /** 视频总开关（纯本地动作，REQ-VIDEO-008）；状态见 call.video 事件 */
  setVideo(callId: string, enabled: boolean): Promise<{ callId: string }>;

  /** 自动接听开关（与软电话界面按钮共用一份状态；变更推 system.autoAnswer 事件） */
  setAutoAnswer(enabled: boolean): Promise<{ enabled: boolean }>;

  /** 查询状态（注册状态、通话列表、版本与平台、更新状态、自动接听开关） */
  getState(): Promise<{
    version: string;
    os: string;
    arch: string;
    autoAnswer: boolean;
    register: { state: string; account: string; reason: string };
    calls: Array<{
      id: string;
      direction: 'incoming' | 'outgoing';
      remote: string;
      remoteName: string;
      state: 'ringing' | 'established' | 'terminated';
      muted: boolean;
      micMuted: boolean;
      speakerMuted: boolean;
      hasVideo: boolean;
      video: {
        negotiated: boolean;
        enabled: boolean;
        previewOn: boolean;
        sending: boolean;
        width: number;
        height: number;
      };
    }>;
    update?: SipUpdateStatus;
  }>;

  /**
   * 告知软电话有新版本可用（自动更新）。
   * 软电话只提示用户，是否下载/重启由用户在软电话界面上确认。
   * version 不高于软电话当前版本时返回错误码 update_not_newer。
   */
  notifyUpdate(info: SipUpdateInfo): Promise<{ phase: string; version: string; currentVersion: string }>;

  /**
   * 立即向更新站点查一次版本（不必等定时器）。结果同时经 update.check 事件派发；
   * 未配置 updateUrl 或尚未连接软电话时返回 rejected Promise。
   */
  checkUpdate(): Promise<SipUpdateCheckResult>;

  /**
   * 列出更新站点上的最新版本与最新稳定版（各带本平台可用的下载地址），
   * 供页面展示或手动「推送更新」用。**不做版本比较**——软电话侧默认仍会
   * 拦下不高于当前版本的通知，要强推需在 notifyUpdate 里带 force: true。
   */
  listReleases(): Promise<{
    /** 软电话当前版本 */
    current: string;
    /** 最新版本（含预发布）；站点上没有版本时为 null */
    latest: SipSiteRelease | null;
    /** 最新稳定版；通道里只有预发布时同样为 null */
    latestStable: SipSiteRelease | null;
  }>;

  /**
   * 运行期修改更新站点配置（改地址/应用 ID/间隔/通道/预发布），改完立即生效：
   * 已连接时重启定时器并马上查一次；url 置空 = 停止自动查询。
   * prerelease 传 null = 恢复自适应。
   */
  setUpdateSource(options: {
    url?: string;
    appId?: string;
    interval?: number;
    channel?: string;
    prerelease?: boolean | null;
  }): { url: string; appId: string; interval: number; channel: string; prerelease: boolean | null };

  /**
   * 订阅视频画面（REQ-VIDEO-009）。**SDK 自己把画面渲染到传入的 <video>**，
   * 页面不用处理帧回调；关闭视频/挂断/退订时 SDK 会把画面清成黑底。
   *
   *   await sdk.subscribeVideo({
   *     remote: document.getElementById('remoteVideo'),  // 必传
   *     local:  document.getElementById('localVideo'),   // 可选：不传 = 不订阅本地回显
   *     fps: 30, quality: 75,
   *     onStats: (channel, stats) => { ... }
   *   });
   *
   * 参数越界会被软电话钳制到合法范围，返回值里是生效值。
   */
  subscribeVideo(options: SipVideoSubscribeOptions): Promise<SipVideoSubscription>;

  /** 取消本连接的视频订阅（软电话恢复显示视频窗口），画面清成黑底；返回两条通道都已退订 */
  unsubscribeVideo(): Promise<{ remote: boolean; local: boolean }>;

  /** 清空已订阅的画面（黑底）；关闭视频/挂断/退订时 SDK 会自动调用 */
  clearVideo(): void;

  /**
   * 画面旋转：每次顺时针 +90°（90°/270° 时宽高互换，<video> 的框跟着变）。
   * **状态存在软电话侧**：桌面视频窗口的按钮会同步变，断线重连/退订都不丢。
   * @param channel 默认 'remote'
   * @returns 旋转后的完整状态（两条通道）
   */
  rotateVideo(channel?: SipVideoChannelName): Promise<SipVideoTransforms>;

  /** 画面水平镜像开关（本地回显常用作自拍镜像）。状态同 rotateVideo */
  mirrorVideo(channel?: SipVideoChannelName): Promise<SipVideoTransforms>;

  /** 当前已知的旋转/镜像状态（远端 + 本地回显） */
  videoTransforms(): SipVideoTransforms;

}

/** 通话事件负载中的通话对象 */
interface SipCall {
  id: string;
  direction: 'incoming' | 'outgoing';
  remote: string;
  remoteName: string;
  state: 'ringing' | 'established' | 'terminated';
  muted: boolean;
  /** 麦克风静音（纯本地动作） */
  micMuted: boolean;
  /** 扬声器静音（纯本地动作） */
  speakerMuted: boolean;
  /** 本次通话涉及视频（呼入时对端 offer 含视频段；呼出时本端发起了视频呼叫） */
  hasVideo: boolean;
  video: {
    /** 视频已协商成功 */
    negotiated: boolean;
    /** 视频总开关（通话面板的「关闭视频」） */
    enabled: boolean;
    /** 本地回显开关（只影响本地显示，不影响推送到远端） */
    previewOn: boolean;
    /** 本次通话要推送本地视频（意图，不随回显开关变化） */
    sending: boolean;
    /** 对端视频分辨率（未收到帧时为 0） */
    width: number;
    height: number;
  };
}

/** 视频通道名 */
type SipVideoChannelName = 'local' | 'remote';

/** 一条通道的旋转/镜像 */
interface SipVideoTransform {
  /** 旋转角度（0/90/180/270，顺时针；90/270 时宽高互换） */
  rotation: number;
  /** 水平镜像 */
  mirror: boolean;
}

/** 两条通道的旋转/镜像（状态在软电话侧，桌面与网页共用一份） */
interface SipVideoTransforms {
  remote: SipVideoTransform;
  local: SipVideoTransform;
}

/** 每秒一次的画质统计（onStats 回调参数） */
interface SipVideoStats {
  /** 每秒帧数 */
  fps: number;
  /** 每秒码率（kbit/s） */
  kbps: number;
  /** 当前画面宽 */
  width: number;
  /** 当前画面高 */
  height: number;
}

/** 单条视频通道：可以直接给 <video> 元素，也可以给带参数的对象（两路各要各的） */
type SipVideoChannel = HTMLVideoElement | SipVideoChannelOptions;

interface SipVideoChannelOptions {
  /** 目标 <video> 元素（SDK 自动渲染，自动播放） */
  el: HTMLVideoElement;
  /** 该通道推流的最大宽度（64–1920），不传取顶层默认，再取 <video> 元素显示宽度 */
  maxWidth?: number;
  /** 该通道推流的最大高度（64–1920），不传取顶层默认，再取 <video> 元素显示高度 */
  maxHeight?: number;
  /** 该通道帧率（1–30），不传取顶层默认，再取默认 30 */
  fps?: number;
  /** 该通道 JPEG 质量（30–95），不传取顶层默认，再取默认 75 */
  quality?: number;
  /** 该通道的统计回调 */
  onStats?: (channel: SipVideoChannelName, stats: SipVideoStats) => void;
}

/** subscribeVideo 参数 */
interface SipVideoSubscribeOptions {
  /** 远端画面（必传） */
  remote: SipVideoChannel;
  /** 本地回显（可选；不传 = 不订阅本地画面） */
  local?: SipVideoChannel;
  /** 两路共用的默认最大宽度 */
  maxWidth?: number;
  /** 两路共用的默认最大高度 */
  maxHeight?: number;
  /** 两路共用的默认帧率 */
  fps?: number;
  /** 两路共用的默认 JPEG 质量 */
  quality?: number;
  /** 两路共用的统计回调（每秒一次） */
  onStats?: (channel: SipVideoChannelName, stats: SipVideoStats) => void;
}

/** 单条通道的生效参数 */
interface SipVideoParams {
  maxWidth: number;
  maxHeight: number;
  fps: number;
  quality: number;
}

/** video.subscribe 的返回：生效参数 + 当前通话快照 + 画面控制 */
interface SipVideoSubscription {
  /** 远端生效参数 */
  remote: SipVideoParams;
  /** 本地生效参数（未订阅本地画面时不存在） */
  local?: SipVideoParams;
  /** 当前进行中的通话（无则 null） */
  call: SipCall | null;
  /** 画面旋转 +90°（默认远端；90°/270° 宽高互换），返回新状态 */
  rotate(channel?: SipVideoChannelName): Promise<SipVideoTransforms>;
  /** 画面水平镜像开关（默认远端），返回新状态 */
  mirror(channel?: SipVideoChannelName): Promise<SipVideoTransforms>;
  /** 两条通道当前的旋转/镜像状态 */
  transforms(): SipVideoTransforms;
  /** 画面清成黑底 */
  clear(): void;
}

/** 告知软电话的新版本信息（system.notifyUpdate 参数） */
interface SipUpdateInfo {
  /** 最新版本号，须高于软电话当前版本（如 "1.1.0"） */
  version: string;
  /** 下载地址，仅支持 http/https；同一平台/架构的产物 */
  url: string;
  /** 可选：sha256 校验值（十六进制），提供则下载后强校验 */
  sha256?: string;
  /** 可选：更新说明 */
  notes?: string;
  /** 可选：目标平台（如 windows），提供时须与软电话一致 */
  os?: string;
  /** 可选：目标架构（如 amd64），提供时须与软电话一致 */
  arch?: string;
  /**
   * 强制推送：跳过"版本必须更高"的检查（联调/回滚时按指定版本强推）。
   * 只应由页面手动推送时设置；SDK 的定时查询不带这个标记。
   */
  force?: boolean;
}

/** 更新站点上某个版本的摘要（listReleases() 的条目；站点上没有该版本时为 null） */
interface SipSiteRelease {
  version: string;
  /** 版本标题（release.json 的 title） */
  title: string;
  /** 预发布版（版本号含 - 或 _） */
  prerelease: boolean;
  publishedAt: string;
  /** 更新说明（站点上的原文 Markdown） */
  notes: string;
  /** 站点标记的强制更新 */
  mandatory: boolean;
  /** 本平台产物的文件名 / 下载地址 / 校验和 / 字节数；本平台无产物时为前两项空串 */
  file: string;
  url: string;
  sha256: string;
  size: number;
}

/** 一次更新站点查询的结果（update.check 事件负载 / checkUpdate() 返回值） */
interface SipUpdateCheckResult {
  /** 软电话当前版本（查询失败且尚未取到时为空串） */
  currentVersion: string;
  /** 站点上该通道的最新版本（查询失败或无更新时为空串） */
  latestVersion: string;
  updateAvailable: boolean;
  /** 是否已把新版本告知软电话（true 后由软电话弹提示，是否更新由用户点） */
  notified: boolean;
  /**
   * 未告知的原因：up_to_date（已是最新）/ no_download（有新版本但本平台无产物）/
   * already_notified（同版本已告知过）/ update_not_newer / update_in_progress /
   * notify_failed
   */
  reason: '' | 'up_to_date' | 'no_download' | 'already_notified' | 'update_not_newer' |
    'update_in_progress' | 'notify_failed';
  /** 失败说明（中文；成功为空串） */
  error: string;
}

/** 自动更新状态（system.update 事件负载 / getState().update） */
interface SipUpdateStatus {
  /** idle / available / downloading / ready / failed */
  phase: 'idle' | 'available' | 'downloading' | 'ready' | 'failed';
  info: SipUpdateInfo;
  /** 下载地址主机名（界面展示来源） */
  host?: string;
  downloaded?: number;
  total?: number;
  /** 下载进度 0-100 */
  progress?: number;
  /** 已就绪但等通话结束后自动重启 */
  waitingCall?: boolean;
  /** 失败原因（中文，面向用户） */
  error?: string;
}

export = SipPhoneSDK;
