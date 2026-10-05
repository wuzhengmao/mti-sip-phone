/**
 * mti-sip-phone 网页端 JS SDK 类型声明
 */

declare class SipPhoneSDK {
  constructor(options?: {
    /** 软电话 WS 地址，默认 ws://127.0.0.1:8765/ws */
    url?: string;
    /**
     * 拉起软电话的系统协议 URI（如 sip://账号:密码@服务器；sips:// 表示走 TLS，见 REQ-ACC-006）。
     * 设置后：首次 WS 连接失败时自动通过该 URI 拉起软电话，
     * 之后失败不再自动拉起（每页会话仅一次）。
     */
    schemaUri?: string;
    /** 断线自动重连，默认 true */
    autoReconnect?: boolean;
    /** 重连间隔毫秒，默认 3000 */
    reconnectDelay?: number;
    /**
     * 更新站点根地址（如 http://update.example.com）。**提供即写入软电话的
     * 自动更新配置**（REQ-UPD-018）：软电话据此自己定时查询站点、有更新弹提示；
     * 不传 = 不改软电话的更新配置。「有没有新版本」由软电话裁决，SDK 不再查询。
     */
    updateUrl?: string;
    /** 更新站点上的应用 ID（写入软电话配置），默认 'mti-sip-phone' */
    updateAppId?: string;
    /** 检查周期（分钟，写入软电话配置；1–1440，默认 5） */
    updateIntervalMinutes?: number;
    /** 是否接受预览版（写入软电话配置），默认 false */
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
   * 事件名：call.incoming / call.ringing / call.dialing / call.established /
   *         call.terminated / call.failed / call.muted / call.audio（麦克风/扬声器开关）/
   *         call.video / video.web / video.transform /
   *         video.frame（订阅视频后逐帧推送）/
   *         register.state / system.state / system.ready / system.update /
   *         system.updateConfig / system.autoAnswer /
   *         connected / disconnected（SDK 本地事件）
   * 注：SDK 本地事件（connected / disconnected）不会派发给 '*' 订阅者。
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

  /** 麦克风增益（REQ-MEDIA-006，纯本地动作）；gainPercent 0–200，0 = 禁用 */
  setMicGain(callId: string, gainPercent: number): Promise<{ callId: string }>;

  /** 扬声器音量（REQ-MEDIA-006，纯本地动作）；volumePercent 0–100，0 = 禁用 */
  setSpeakerVolume(callId: string, volumePercent: number): Promise<{ callId: string }>;

  /** 发送 DTMF（RFC 4733，通话接通后有效）；digit 为单个字符：0-9 * # A-D */
  sendDTMF(callId: string, digit: string): Promise<{ callId: string }>;

  /** 视频总开关（纯本地动作，REQ-VIDEO-008）；状态见 call.video 事件 */
  setVideo(callId: string, enabled: boolean): Promise<{ callId: string }>;

  /** 自动接听开关（与软电话界面按钮共用一份状态；变更推 system.autoAnswer 事件） */
  setAutoAnswer(enabled: boolean): Promise<{ enabled: boolean }>;

  /**
   * 导出通信录为 CSV 文本（表头「号码,名称,备注,常用」，带 UTF-8 BOM）。
   * 网页端不做增删改查，只提供一键导入/导出。
   */
  exportContacts(): Promise<{ csv: string; count: number }>;

  /**
   * 一键导出：取回 CSV 后由浏览器存成文件（默认 contacts-YYYYMMDD.csv）。
   * 走 HTTP（`GET /api/contacts.csv`），没有 WS 的单条消息大小限制。
   */
  downloadContacts(filename?: string): Promise<{ count: number; filename: string }>;

  /**
   * 导入通信录 CSV：按号码去重，已存在的只覆盖号码/名称/备注（**常用只增不减**），
   * 不存在的整条新增。
   */
  importContacts(csv: string): Promise<SipContactImportResult>;

  /**
   * 导出通信录为 **JSON**（REST：`GET /api/contacts`）——给程序用的结构化数据，
   * 网页可直接 XHR 取来渲染。
   */
  exportContactsJSON(): Promise<{ count: number; contacts: SipContactJSON[] }>;

  /**
   * 导入通信录 **JSON**（REST：`POST /api/contacts`）。可传裸数组或
   * exportContactsJSON() 返回的对象（导出→改→导入可直接回灌）。
   * 合并语义与 CSV 导入一致：按号码去重、只覆盖号码/名称/备注、常用标志只增不减。
   */
  importContactsJSON(contacts: SipContactJSON[] | { count?: number; contacts: SipContactJSON[] } | string):
    Promise<SipContactImportResult>;

  /**
   * 一键导入：弹系统文件选择框选 CSV 文件后合并（走 HTTP POST
   * `/api/contacts/import`，请求体即 CSV，没有 WS 的大小限制）。
   * 用户取消时以 code='cancelled' 拒绝。
   */
  importContactsFromFile(): Promise<SipContactImportResult & { filename: string }>;

  /** 按 File/Blob 导入通信录（HTTP 直传，不弹文件选择框） */
  importContactsFile(file: File | Blob): Promise<SipContactImportResult>;

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
      /** 通信录匹配结果（命中才有）：名称/备注/是否常用（REQ-UI-037） */
      contact?: SipContactInfo;
    }>;
    update?: SipUpdateStatus;
  }>;

  /**
   * **强制推送**更新（SDK 唯一的更新相关能力）：把指定版本告知软电话，
   * 软电话提示用户、由用户确认后下载替换重启。
   * **不受版本高低与预览版约束**（内部恒带 force）——版本比较由软电话自己
   * 查询站点时进行（配置见 setUpdateConfig）。
   */
  notifyUpdate(info: SipUpdateInfo): Promise<{ phase: string; version: string; currentVersion: string }>;

  /**
   * 写入软电话的**自动更新配置**（REQ-UPD-018）：软电话据此自己定时查询更新
   * 站点（配了 url 才会查），查到新版本弹提示窗。字段可选，只改传入的。
   * 构造参数里给了更新相关项时，连上软电话后会自动写入一次。
   */
  setUpdateConfig(cfg: SipUpdateConfig): Promise<Required<SipUpdateConfig>>;

  /**
   * 订阅视频画面（REQ-VIDEO-009）。**SDK 自己把画面渲染到传入的 <video>**，
   * 页面不用处理帧回调；关闭视频/挂断/退订时 SDK 会把画面清成黑底。
   *
   *   await sdk.subscribeVideo({
   *     remote: document.getElementById('remoteVideo'),  // 必传
   *     local:  document.getElementById('localVideo'),   // 可选：不传 = 不订阅本地回显
   *     fps: 30, quality: 75,
   *     keepWindow: false,                               // 可选：订阅后是否仍显示软电话视频窗口（默认 false）
   *     onStats: (channel, stats) => { ... }
   *   });
   *
   * 参数越界会被软电话钳制到合法范围，返回值里是生效值。
   * **视频通话被关闭时订阅失败**（软电话配置里未勾选「启用视频通话」），
   * Promise 以 `code === 'video_unsupported'` 的错误 reject。
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
  /** 通信录匹配结果（命中才有）：页面可直接显示名称、★ 与备注（REQ-UI-037） */
  contact?: SipContactInfo;
}

/** 通话对象里携带的通信录信息（对端号码在通信录里命中时才有） */
interface SipContactInfo {
  /** 通信录里的显示名称（可能为空串） */
  name: string;
  /** 备注（可能为空串；可作悬停提示） */
  note: string;
  /** 常用联系人（页面上可在名称后加 ★） */
  starred: boolean;
}

/** REST 接口里的联系人 JSON 形状 */
interface SipContactJSON {
  number: string;
  name: string;
  note: string;
  starred: boolean;
}

/** 通信录导入结果 */
interface SipContactImportResult {
  /** 新增条数 */
  added: number;
  /** 覆盖条数（只覆盖号码/名称/备注） */
  updated: number;
  /** 跳过的行数（没有号码或格式不对） */
  skipped: number;
  /** 导入后的通信录总条数 */
  total: number;
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
  /**
   * 订阅后是否**仍保持软电话的视频窗口显示**（REQ-VIDEO-009 修订）。
   * 默认 false = 订阅后软电话隐藏视频窗口（画面在网页看）；
   * 传 true = 软电话的视频窗口继续显示（两处同时看）。
   */
  keepWindow?: boolean;
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
   * SDK 的 notifyUpdate 恒为强制推送，传 false 也不生效。
   */
  force?: boolean;
}

/** 软电话的自动更新配置（REQ-UPD-017/018；system.updateConfig 事件负载） */
interface SipUpdateConfig {
  /** 更新站点根地址；空 = 不自动检查 */
  url?: string;
  /** 应用 ID（站点上的应用标识），默认 'mti-sip-phone' */
  appId?: string;
  /** 检查周期（分钟，1–1440，默认 5） */
  intervalMinutes?: number;
  /** 是否接受预览版，默认 false */
  prerelease?: boolean;
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
