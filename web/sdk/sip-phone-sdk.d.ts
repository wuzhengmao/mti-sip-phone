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
   *         video.frame（订阅视频后逐帧推送）/ connected / disconnected
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

  /** 视频总开关（纯本地动作，REQ-VIDEO-008）；状态见 call.video 事件 */
  setVideo(callId: string, enabled: boolean): Promise<{ callId: string }>;

  /** 查询状态（注册状态、通话列表、版本与平台、更新状态） */
  getState(): Promise<{
    version: string;
    os: string;
    arch: string;
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
   * 订阅视频画面（REQ-VIDEO-009）。订阅期间软电话不再显示视频窗口，
   * 画面以 JPEG 帧经 WS 二进制消息推来（video.frame 事件）。
   * 参数越界会被钳制到合法范围，返回值里是生效值。
   */
  subscribeVideo(options?: SipVideoOptions): Promise<SipVideoSubscription>;

  /** 取消本连接的视频订阅（软电话恢复显示视频窗口） */
  unsubscribeVideo(): Promise<{ local: boolean; remote: boolean }>;

  /**
   * 创建视频渲染器：把 video.frame 的 JPEG 帧经 canvas 画布流喂给 h5 <video>。
   *
   *   const r = sdk.createVideoRenderer(document.getElementById('remoteVideo'));
   *   sdk.on('video.frame', (f) => { if (f.channel === 'remote') r.render(f.blob); });
   */
  createVideoRenderer(videoEl: HTMLVideoElement, options?: {
    /** 画布流目标帧率，默认 30 */
    fps?: number;
    /** 每秒回调一次 {fps, kbps, width, height} */
    onStats?: (stats: { fps: number; kbps: number; width: number; height: number }) => void;
  }): SipVideoRenderer;

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

/** 订阅视频参数（未指定的取默认：640×640 / 30fps / 质量 75） */
interface SipVideoOptions {
  /** 同时订阅本地画面，默认 true */
  local?: boolean;
  /** 保留字段：远端画面恒订阅（软电话强制），传 false 会被忽略 */
  remote?: boolean;
  /** 推流最大宽度（64–1920），默认 640 */
  maxWidth?: number;
  /** 推流最大高度（64–1920），默认 640 */
  maxHeight?: number;
  /** 帧率（1–30），默认 30 */
  fps?: number;
  /** JPEG 质量（30–95），默认 75 */
  quality?: number;
}

/** video.subscribe 的返回：生效参数 + 当前通话快照 */
interface SipVideoSubscription {
  local: boolean;
  remote: boolean;
  maxWidth: number;
  maxHeight: number;
  fps: number;
  quality: number;
  /** 当前进行中的通话（无则 null） */
  call: SipCall | null;
}

/** 视频渲染器（createVideoRenderer 返回） */
interface SipVideoRenderer {
  /** 渲染一帧（video.frame 事件的 blob） */
  render(blob: Blob): void;
  /** 清空画面（填黑底并清零统计）：关闭视频/挂断/取消订阅时调用，避免定格在最后一帧 */
  clear(): void;
  /** 释放画布流 */
  destroy(): void;
  /** 最近统计值 */
  readonly fps: number;
  readonly kbps: number;
  readonly width: number;
  readonly height: number;
  /** 累计渲染帧数 */
  readonly frames: number;
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
