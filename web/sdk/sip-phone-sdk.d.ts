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

  /** 订阅事件；'*' 订阅全部 */
  on(event: string, fn: (data: any) => void): this;

  /** 取消订阅 */
  off(event: string, fn: (data: any) => void): this;

  /** 拨号 */
  dial(target: string): Promise<{ callId: string }>;

  /** 接听 */
  answer(callId: string): Promise<{ callId: string }>;

  /** 挂断 */
  hangup(callId: string): Promise<{ callId: string }>;

  /** 拒接，code 默认 486 */
  reject(callId: string, code?: number): Promise<{ callId: string }>;

  /** 静音切换 */
  mute(callId: string, muted: boolean): Promise<{ callId: string }>;

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
    }>;
    update?: SipUpdateStatus;
  }>;

  /**
   * 告知软电话有新版本可用（自动更新）。
   * 软电话只提示用户，是否下载/重启由用户在软电话界面上确认。
   * version 不高于软电话当前版本时返回错误码 update_not_newer。
   */
  notifyUpdate(info: SipUpdateInfo): Promise<{ phase: string; version: string; currentVersion: string }>;

}

/** 通话事件负载中的通话对象 */
interface SipCall {
  id: string;
  direction: 'incoming' | 'outgoing';
  remote: string;
  remoteName: string;
  state: 'ringing' | 'established' | 'terminated';
  muted: boolean;
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
