/**
 * mti-sip-phone 网页端 JS SDK 类型声明
 */

declare class SipPhoneSDK {
  constructor(options?: {
    /** 软电话 WS 地址，默认 ws://127.0.0.1:8765/ws */
    url?: string;
    /**
     * sip:// 或 sips:// 链接（如 sip://账号:密码@服务器；sips:// 表示走 TLS，
     * 见 REQ-ACC-006）。设置后有两重作用（REQ-SDK-007）：
     * 1. 首次 WS 连接失败时自动通过该 URI 拉起软电话（每页会话仅一次）；
     * 2. 每次 WS 连接成功后，以 URI 里的注册信息登录 SIP 服务器
     *    （与软电话现有注册相同时自动忽略，不打断通话）。
     */
    sipUri?: string;
    /**
     * WS 断开时自动取消 SIP 注册、重新连上后恢复注册（REQ-WS-020），默认 false。
     * 软电话侧按"所有连接断开后延迟确认"执行：呼叫/振铃/通话中自动排队
     * 等话务结束再注销，等待期间重连则取消排队任务。
     */
    unregisterOnDisconnect?: boolean;
    /** 断线自动重连，默认 true */
    autoReconnect?: boolean;
    /** 重连间隔毫秒，默认 3000 */
    reconnectDelay?: number;
   /** 连上软电话后设置自动接听开关（true=自动接听；不传 = 不改软电话配置） */
    autoAnswer?: boolean;
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
   * 订阅**通话面板快照**（REQ-WS-023 / REQ-SDK-010）：负载类型
   * `SipCallPanelState`（两个 panel 都为 null 表示没有通话）。
   */
  on(event: 'call.panel', fn: (data: SipCallPanelState) => void): this;

  /** 订阅坐席状态变化（REQ-ACC-028）：负载 `SipAgentState`。 */
  on(event: 'cti.agent', fn: (data: SipAgentState) => void): this;

  /**
   * 订阅事件；'*' 订阅全部。
   * 事件名：call.incoming / call.ringing / call.dialing / call.established /
   *         call.terminated / call.failed / call.muted / call.audio（麦克风/扬声器开关）/
   *         call.video / call.panel（通话面板快照，见 SipCallPanelState）/
   *         cti.agent（坐席状态变化：{displayName, agentMode, isBusy, isWork, isLeave}，agentMode=none 表示不在线）/
   *         cti.state（通话话务状态变化：{callId, call}）/
   *         cti.failed（CTI 话务失败：{callId, kind, reason, message}）/
   *         cti.result（CTI 指令结果：{seq, success, code, message, action, data?}）/
   *         video.web / video.transform /
   *         video.frame（订阅视频后逐帧推送）/
   *         register.state / system.state / system.ready / system.update /
   *         system.updateConfig / system.autoAnswer /
   *         connected / disconnected（SDK 本地事件）
   * 注：SDK 本地事件（connected / disconnected）不会派发给 '*' 订阅者。
   */
  on(event: string, fn: (data: any) => void): this;

  /** 取消订阅通话面板快照 */
  off(event: 'call.panel', fn: (data: SipCallPanelState) => void): this;

  /** 取消订阅 */
  off(event: string, fn: (data: any) => void): this;

  /** 拨号；options.video=true 发起视频呼叫 */
  dial(target: string, options?: { video?: boolean; name?: string }): Promise<{ callId: string }>;

  /** 接听；options.video=true 视频接听（打开摄像头双向视频） */
  answer(options?: { video?: boolean }): Promise<{ callId: string }>;

  /** 挂断：话机按状态自动决定——来电振铃 → 拒接（486）、呼出振铃 → 取消、通话中 → 挂断 */
  hangup(): Promise<{ callId: string }>;

  /** 保持（走 SIP re-INVITE，对端会看到"保持"） */
  hold(): Promise<{ callId: string }>;

  /** 取消保持 */
  unhold(): Promise<{ callId: string }>;

  /** 麦克风开关（纯本地动作；状态变化推 call.audio 事件） */
  muteMic(muted: boolean): Promise<{ callId: string }>;

  /** 扬声器开关（纯本地动作，继续接收） */
  muteSpeaker(muted: boolean): Promise<{ callId: string }>;

  /** 麦克风增益（REQ-MEDIA-006，纯本地动作）；gainPercent 0–200，0 = 禁用 */
  setMicGain(gainPercent: number): Promise<{ callId: string }>;

  /** 扬声器音量（REQ-MEDIA-006，纯本地动作）；volumePercent 0–100，0 = 禁用 */
  setSpeakerVolume(volumePercent: number): Promise<{ callId: string }>;

  /** 发送 DTMF（RFC 4733，通话接通后有效）；digit 为单个字符：0-9 * # A-D */
  sendDTMF(digit: string): Promise<{ callId: string }>;

  /** 视频总开关（纯本地动作，REQ-VIDEO-008）；状态见 call.video 事件 */
  setVideo(enabled: boolean): Promise<{ callId: string }>;

  /** 自动接听开关（与软电话界面按钮共用一份状态；变更推 system.autoAnswer 事件） */
  setAutoAnswer(enabled: boolean): Promise<{ enabled: boolean }>;

  /**
   * 登录账号（REQ-ACC-024）：启用账户并立即注册。
   * 结果经 `register.state` 事件回显。
   */
  register(): Promise<{ enabled: boolean; state: string }>;

  /**
   * 退出账号（REQ-ACC-024）：发 expires=0、停止注册周期并禁用账户
   * （启动/重连都不会自动恢复，直到再次 register 或 sip:// 拉起）。
   */
  unregister(): Promise<{ enabled: boolean; state: string }>;

  // ---- CTI 坐席/话务（REQ-ACC-026 / REQ-ACC-034 / REQ-ACC-035）----

  /**
   * 在线座席列表（CTI `listAgents`）。非坐席模式以 code='cti_not_agent' 拒绝。
   */
  listAgents(): Promise<{ agents: SipAgent[]; count: number }>;

  /**
   * 坐席置忙/置闲（setBusy / setIdle）。结果经 `cti.agent` 事件回显。
   */
  setAgentBusy(busy: boolean): Promise<SipCTIActionResult>;

  /** 坐席置闲 */
  setIdle(): Promise<SipCTIActionResult>;

  /** 坐席置忙 */
  setBusy(): Promise<SipCTIActionResult>;

  /** 坐席离席（leave，isLeave=true） */
  leave(): Promise<SipCTIActionResult>;

  /** 坐席复席（resume，isLeave=false，重新进入分配队列） */
  resume(): Promise<SipCTIActionResult>;

  /** 坐席话后整理（setWork，isWork=true）；workSeconds 为整理时长（秒，可选） */
  setWork(workSeconds?: number): Promise<SipCTIActionResult>;

  /**
   * 发起一条 CTI 反向控制指令（与桌面通话面板的按钮一致）。
   * 指令等待 MSP_RESULT 后 resolve；失败 code 为 cti_failed / cti_busy /
   * cti_timeout / cti_not_agent。
   */
  ctiAction(action: string, arg?: string | string[] | Record<string, any>): Promise<SipCTIActionResult>;

  /** 发起咨询（被咨询方 = target） */
  consult(target: string): Promise<SipCTIActionResult>;
  /** 咨询转三方 */
  consultThreeWay(target: string): Promise<SipCTIActionResult>;
  /** 直接转移 */
  transfer(target: string): Promise<SipCTIActionResult>;
  /** 咨询转移 */
  consultTransfer(target: string): Promise<SipCTIActionResult>;
  /** 监听坐席（班长模式） */
  monitor(target: string): Promise<SipCTIActionResult>;
  /** 结束监听（stopMonitor） */
  stopMonitor(): Promise<SipCTIActionResult>;
  /** 代接坐席的振铃来电 */
  takeover(target: string): Promise<SipCTIActionResult>;
  /** 三方通话里强制挂断某一方（仅 owner） */
  exitThreeWay(target: string): Promise<SipCTIActionResult>;
  /** 结束咨询 */
  finishConsult(): Promise<SipCTIActionResult>;
  /** 结束会议（owner） */
  finishConference(): Promise<SipCTIActionResult>;
  /** 监听耳语 */
  whisper(): Promise<SipCTIActionResult>;
  /** 退出监听耳语（恢复静默监听） */
  stopWhisper(): Promise<SipCTIActionResult>;
  /** 监听插话（强插） */
  intervene(): Promise<SipCTIActionResult>;
  /** 监听强拦 */
  intercept(): Promise<SipCTIActionResult>;
  /** 把成员加入会议 */
  addConferenceMembers(members: string | string[]): Promise<SipCTIActionResult>;
  /** 从会议移除成员 */
  removeConferenceMembers(members: string | string[]): Promise<SipCTIActionResult>;

  /** 会议成员会控（owner）：禁音/取消禁音 */
  setConferenceMemberMuted(member: string, muted: boolean): Promise<SipCTIActionResult>;
  /** 会议成员会控（owner）：禁听/取消禁听 */
  setConferenceMemberDeafened(member: string, deafened: boolean): Promise<SipCTIActionResult>;
  /** 会议成员会控（owner）：禁视频/取消禁视频 */
  setConferenceMemberVideoMuted(member: string, muted: boolean): Promise<SipCTIActionResult>;

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

  /** 查询当前通话面板快照（`call.panel` 的负载，见 SipCallPanelState） */
  getCallPanel(): Promise<SipCallPanelState>;

  /** 查询当前坐席状态（`cti.agent` 的负载，见 SipAgentState） */
  getAgentState(): Promise<SipAgentState>;

  /** 查询状态（注册状态、通话列表、版本与平台、更新状态、自动接听开关） */
  getState(): Promise<{
    version: string;
    os: string;
    arch: string;
    autoAnswer: boolean;
    /** 仅托盘无 UI 版（REQ-UI-041）：页面据此自动订阅视频、隐藏窗口相关项 */
    noUI?: boolean;
    /** 实例槽位（0 = 主实例，≥1 = 沙箱实例） */
    instance?: number;
    /** 坐席模式：none / normal / dial_out / monitor（**none 表示不在坐席模式**；
     *  详细的坐席状态走 `cti.agent` 事件） */
    agentMode?: string;
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
      /** 复杂话务状态（咨询/三方/监听/会议，REQ-ACC-026） */
      cti?: SipCTIState;
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

  // ---- 通话记录（REQ-WS-017）----

  /** 分页查询通话记录（新的在前）。remote = 按号码子串筛选；limit 默认 50、上限 200 */
  queryCallLog(filter?: { remote?: string; limit?: number; offset?: number }):
    Promise<{ records: SipCallRecord[]; total: number; hasMore: boolean }>;

  /** 删除若干条通话记录 */
  deleteCallLog(callIds: string[]): Promise<{ deleted: number }>;

  /** 清空全部通话记录（不可撤销） */
  clearCallLog(): Promise<{ deleted: number }>;

  // ---- 通信录增删改查（REQ-WS-018）----

  /**
   * 列出联系人。给了 query 就在软电话侧过滤：支持**音头拼音 / 全拼 / 汉字混合**
   * （"zs"/"zhangsan"/"张三" 都能命中「张三」）、号码与备注子串、号码纯数字写法，
   * 与桌面通信录页同一份实现；结果按拼音排序（空名称最前）。
   */
  listContacts(filter?: { query?: string }):
    Promise<{ count: number; total: number; contacts: SipContactJSON[] }>;

  /** 新增或修改联系人（prevNumber = 改号前的号码）；号码重复以 invalid_params 拒绝 */
  saveContact(contact: SipContactInput, prevNumber?: string):
    Promise<{ contact: SipContactJSON; count: number }>;

  /** 按号码（任意写法）删除联系人 */
  deleteContacts(numbers: string[]): Promise<{ deleted: number; count: number }>;

  /** 批量设置/取消常用 */
  setContactsStarred(numbers: string[], starred: boolean):
    Promise<{ updated: number; count: number }>;

  /** 清空通信录（不可撤销） */
  clearContacts(): Promise<{ deleted: number; count: number }>;

  // ---- 配置读写（REQ-WS-019）----

  /** 配置快照：账户（**不含密码**，只有 passwordSet）、音频、视频、日志、WS、更新、自启 */
  getConfig(): Promise<SipAppConfig>;

  /** 修改 SIP 账户（只改传入字段）。密码只写不读：password 改密码 / clearPassword 清空 */
  setAccount(patch: SipAccountPatch): Promise<SipAccountSnapshot>;

  /** 枚举音频设备。ready=false 表示"还在枚举/不可用"，此时列表为空（不要当成没有设备） */
  listAudioDevices(): Promise<{
    mics: SipAudioDevice[];
    speakers: SipAudioDevice[];
    ready: boolean;
    preprocessSupported: boolean;
  }>;

  /** 修改音频配置（只改传入字段）：设备立即生效，AEC/NS/AGC 从下一通电话生效 */
  setAudioConfig(patch: {
    micId?: string; speakerId?: string; ringSpeakerId?: string;
    aec?: boolean; ns?: boolean; agc?: boolean;
  }): Promise<SipAudioConfig>;

  /** 枚举摄像头 */
  listVideoDevices(): Promise<{
    cameras: SipVideoDevice[];
    supported: boolean;
    cameraSupported: boolean;
  }>;

  /** 修改视频配置（只改传入字段）：enabled 立即生效，摄像头与采集参数下一通生效 */
  setVideoConfig(patch: {
    enabled?: boolean; cameraId?: string; cameraName?: string;
    width?: number; height?: number; fps?: number; bitrateKbps?: number;
  }): Promise<SipVideoConfig>;

  /** 修改日志配置：级别与 sipTrace 立即生效，file/console 重启后生效 */
  setLogConfig(patch: {
    level?: 'debug' | 'info' | 'warn' | 'error'; dir?: string;
    file?: boolean; console?: boolean; sipTrace?: boolean;
  }): Promise<SipLogConfig>;

  /**
   * 修改「网页接入」配置（重启后生效）：监听端口 / 是否允许远程连接（可选，只改传入的）。
   * ⚠️ 允许远程 = 监听 0.0.0.0 且不做来源限制，而网页接口**没有鉴权**。
   */
  setWSPort(port?: number, allowRemote?: boolean):
    Promise<{ port: number; allowRemote: boolean; actualPort: number; restartRequired: boolean }>;

  /** 开/关开机自启（沙箱实例不支持，返回 config_unavailable） */
  setAutoStart(enabled: boolean): Promise<{ enabled: boolean }>;

  /** 重启软电话（先回响应再重启，resolve 后连接随时会断开） */
  restartApp(): Promise<{ restarting: boolean }>;

  // ---- 自动更新（REQ-UPD-020：网页可触发下载/安装，仍须用户点击）----

  /** 更新状态快照（一般用 system.update 事件，这里供初始化查询） */
  updateStatus(): Promise<SipUpdateStatus>;

  /** 开始下载已告知的新版本（进度走 system.update 事件） */
  startUpdate(): Promise<SipUpdateStatus>;

  /** 取消下载 */
  cancelUpdate(): Promise<SipUpdateStatus>;

  /** 安装已下载好的更新并重启（要求 phase === 'ready'） */
  applyUpdate(): Promise<{ phase: string; applying: boolean }>;

  /** 立即检查一次更新站点 */
  checkUpdateNow(): Promise<{
    skipped: boolean; upToDate: boolean; latestVersion: string;
    notified: boolean; reason: string; text: string; currentVersion: string;
  }>;

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
  /** INVITE 的 Call-Info display-name（服务端下发的对端名称，优先于通信录显示；可能为空） */
  peerName?: string;
  /** 本通是服务端推来的咨询腿（Call-Info call-type=consult） */
  consult?: boolean;
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
  /** 复杂话务状态（咨询/三方/监听/会议，REQ-ACC-026）：双方通话时为空/零值 */
  cti?: SipCTIState;
}

/** 在线座席列表的一项（cti.listAgents） */
interface SipAgent {
  /** 座席号（分机号） */
  agent: string;
  /** 显示名 */
  displayName: string;
  /** 技能组（"110:1,122:1"） */
  acd: string;
  /** 坐席模式：normal / dial_out / monitor */
  agentMode: string;
  /** 座席状态：free / ringing / talking / free_offhook / dialing / busy / leave /
   *  work（整理中）；not_ready / miss_phone / offline 的坐席不会出现在列表里 */
  agentStatus: string;
}

/** cti.action / cti.listAgents 的结果 */
interface SipCTIActionResult {
  /** 动作名（与服务端回显一致） */
  action: string;
  /** 指令序号（MSP_ACTION 的 seq） */
  seq: string;
  /** 带返回值的动作的结果（如 listAgents 的原始数组） */
  data?: any;
}

/** 通话的复杂话务状态（协议 §16，MSP_EVENT_* 翻译后的结果） */
interface SipCTIState {
  /** twoParty / consult / threeWay / monitor / conference */
  talkState: string;
  /** 另有一通咨询进行中（本机不是发起方）：隐藏话务入口 */
  consultInProgress?: boolean;
  consulter?: string;
  consulterName?: string;
  consultee?: string;
  consulteeName?: string;
  consultPhase?: string;
  owner?: string;
  ownerName?: string;
  firstParty?: string;
  firstPartyName?: string;
  secondParty?: string;
  secondPartyName?: string;
  thirdParty?: string;
  thirdPartyName?: string;
  monitor?: string;
  monitorName?: string;
  target?: string;
  targetName?: string;
  monitorPhase?: string;
  /** 被监听话务的主被叫（MONITOR_STARTED 携带；CALL_* 双方通话事件复用） */
  callerNo?: string;
  callerName?: string;
  calledNo?: string;
  calledName?: string;
  /** CTI 侧话务 ID（CALL_* 事件，SIP 信令里没有） */
  mspCallId?: string;
  /** 双方通话阶段：dialing / ringing / talking（CALL_RINGING 时显示「振铃中」） */
  callPhase?: string;
  /** 接听坐席（CALL_INCOMING/CONNECTED/FINISHED/FAILED） */
  agent?: string;
  agentName?: string;
  confId?: string;
  members?: Array<{
    telNo: string;
    displayName?: string;
    state: string;
    skillGroup?: string;
  }>;
}

/** 坐席状态（事件 `cti.agent`，REQ-ACC-028）：`agentMode=none` 表示不在坐席模式。 */
interface SipAgentState {
  /** 坐席显示名（AGENT_ONLINE 的 displayName，可为空） */
  displayName: string;
  /** 坐席模式：none / normal / dial_out / monitor */
  agentMode: string;
  /** 是否置忙 */
  isBusy: boolean;
  /** 是否处于话后整理态（isBusy=true 且 isWork=true → 整理中） */
  isWork: boolean;
  /** 是否离席（优先级最高的"不可用"状态） */
  isLeave: boolean;
  /** 话务状态（MSP_HEARTBEAT 上报的 status）：free / dialing / ringing / talking / holding */
  phoneStatus: string;
  /** 坐席状态（由 agentMode + phoneStatus + isBusy/isWork/isLeave 推算，取值同 SipAgent.agentStatus）：
   *  agentMode=none → offline；isLeave → leave；free+isBusy → busy/work，否则 free；
   *  dialing / ringing / talking / holding → 对应值；其它 → unknown */
  agentStatus: string;
}

/** 通话面板快照（事件 `call.panel`，REQ-WS-023 / REQ-SDK-010）。
 *
 * 应用直接基于它构建自己的通话面板，不必解析 `call.*` / `cti.*` 明细：
 * `sdk.on('call.panel', (p) => { ... })`。两个 panel 都为 null 表示没有通话。 */
interface SipCallPanelState {
  /** 通话面板；没有进行中的通话时为 null */
  callPanel: SipCallPanel | null;
  /** 会议面板（会议进行中才有），否则 null */
  conferencePanel: SipConferencePanel | null;
}

/** 面板里的一个参与方：号码 + 名称 + 通信录备注。 */
interface SipPanelParty {
  number: string;
  name?: string;
  /** 通信录备注：**仅当协议没有给名称、回退到通信录时才有**（此时 name 也来自通信录） */
  note?: string;
}

/** 通话面板里的一方（子话务）。字段与 CTI 协议 §16.2 的 `callList` 项一致
 *  （话机未用到的属性也原样保留，应用可以按需使用）；唯一的改写是 `peer`：
 *  协议里是号码字符串（名称另给 `peerName`），这里合并成
 *  `{number,name,note}` 对象。 */
interface SipCallPanelLeg {
  /** 主叫号码（协议原样） */
  callerNo?: string;
  /** 被叫号码（协议原样） */
  calledNo?: string;
  /** 对端（`peer` 号码 + `peerName` 名称 + 通信录备注）；监听话务时 peer 是主叫 */
  peer: SipPanelParty;
  /** 是否为内部分机（协议原样） */
  intercom?: boolean;
  /** 该腿的技能组（外线呼入分配时可能有值） */
  skillGroup?: string;
  /** 服务端给的腿类型（incoming/outgoing/consult） */
  callType?: string;
  /** 子话务状态（协议原属性名 `callStatus`）：incoming / dialing / ringing /
   *  talking / holding（holding = 咨询期间原通话被保持） */
  callStatus: string;
  /** 子话务开始时间（毫秒时间戳，协议原样） */
  startTime?: number;
  /** 振铃时间（毫秒时间戳，协议原样） */
  ringingTime?: number;
  /** 摘机时间（毫秒时间戳，协议原样） */
  offhookTime?: number;
  /** 本端补充（非协议）：仅监听话务，peer=主叫、target=被叫 */
  target?: SipPanelParty;
}

/** 通话面板。字段与 CTI 协议 §16.2 一致（含话机未使用的属性）。 */
interface SipCallPanel {
  callId: string;
  /** 坐席号 */
  agent?: string;
  /** 话务开始时间（毫秒时间戳） */
  startTime: number;
  /** 面板状态文案（本端补充）：拨号中/振铃中/来电/通话中/咨询呼叫中/咨询振铃中/
   *  咨询通话中/三方通话中/监听中/监听耳语中/会议中/通话保持中 */
  title: string;
  /** 面板状态（据此决定显示哪些功能按钮）：dialing / ringing / incoming / talking /
   *  consult-dialing / consult-ringing / consult-talking / three-way / monitoring / whispering */
  state: string;
  /** 服务端面板的通话类型：incoming / outgoing / transfer / monitor */
  callType?: string;
  /** 服务端面板的话务模式：normal / consult / three-way / monitor */
  callMode?: string;
  /** 当前操作的发起方分机（协议原样，`callMode=normal` 时无此字段）：咨询是
   *  咨询方，三方是发起方，监听是班长。**只有 owner 显示该话务的发起方按钮**
   *  （咨询接通后 owner 显示三方/转移；三方的移除按钮只有 owner 显示）。 */
  owner?: string;
  /** 失败原因（仅失败时）：cancel / refuse / takeover / fail */
  failCause?: string;
  callList: SipCallPanelLeg[];
}

/** 会议成员。字段与 CTI 协议 §16.3 的 `memberList` 项一致；`member` 与 `peer`
 *  一样合并成 `{number,name,note}` 对象（协议里是 `member` 号码 + `memberName`）。 */
interface SipConferenceMemberPanel {
  /** 成员（号码 + 名称 + 通信录备注） */
  member: SipPanelParty;
  /** 是否为内部分机 */
  intercom?: boolean;
  /** 是否主持人 */
  owner: boolean;
  /** 成员状态（透传 CTI：dialing/ringing/talking/holding/hangup，会控后缀 muted/deafened/vmuted，可用 "+" 组合） */
  state: string;
  /** 成员加入时间（毫秒时间戳） */
  timestamp?: number;
}

/** 会议面板。字段与 CTI 协议 §16.3 一致。 */
interface SipConferencePanel {
  confId: string;
  /** 主持人号码 */
  owner: string;
  /** 成员列表（主持人固定第一行） */
  memberList: SipConferenceMemberPanel[];
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

/** 一条通话记录（REQ-WS-017）。时间戳为毫秒整数，0 = 未发生（未接通/进行中）。 */
interface SipCallRecord {
  callId: string;
  direction: 'incoming' | 'outgoing';
  remote: string;
  remoteName: string;
  /** 座席显示名（在线座席列表直接呼出时记录；展示时优先于通信录名称） */
  displayName: string;
  startedAt: number;
  /** 0 = 未接通 */
  answeredAt: number;
  /** 0 = 进行中 */
  endedAt: number;
  hasVideo: boolean;
  /** 呼入且未接通（本端拒接/挂断不算未接） */
  missed: boolean;
  reason: string;
  /** 号码命中通信录时的条目（软电话侧按号码规范化解析，口径同桌面通话记录页） */
  contact?: SipContactInfo;
}

/** 新增/修改联系人的入参 */
interface SipContactInput {
  number: string;
  name?: string;
  note?: string;
  starred?: boolean;
}

/** 音频设备（麦克风/扬声器） */
interface SipAudioDevice {
  id: string;
  name: string;
}

/** 摄像头（id 在 Windows 侧每次启动可能变，name 是稳定的兜底标识） */
interface SipVideoDevice {
  id: string;
  name: string;
}

/** 音频配置（含后端状态） */
interface SipAudioConfig {
  micId: string;
  speakerId: string;
  ringSpeakerId: string;
  aec: boolean;
  ns: boolean;
  agc: boolean;
  ready: boolean;
  preprocessSupported: boolean;
}

/** 视频配置（含构建/设备能力） */
interface SipVideoConfig {
  enabled: boolean;
  disabled: boolean;
  cameraId: string;
  cameraName: string;
  width: number;
  height: number;
  fps: number;
  bitrateKbps: number;
  supported: boolean;
  cameraSupported: boolean;
  /** 表示"禁用摄像头"的取值（下拉框的「禁用」项用它） */
  cameraDisabled: string;
}

/** 日志配置 */
interface SipLogConfig {
  level: string;
  dir: string;
  file: boolean;
  console: boolean;
  sipTrace: boolean;
}

/** SIP 账户快照（**不含密码**，只有 passwordSet） */
interface SipAccountSnapshot {
  enabled: boolean;
  server: string;
  port: number;
  expires: number;
  rport: boolean;
  stunEnabled: boolean;
  stunServer: string;
  proxy: string;
  transport: 'udp' | 'tcp' | 'tls';
  tlsVerify: boolean;
  /** 媒体加密四档：off / supported / preferred / required */
  srtp: string;
  domain: string;
  publicIP: string;
  account: string;
  authUser: string;
  displayName: string;
  passwordSet: boolean;
}

/** 账户补丁：只改传入的字段；password 改密码、clearPassword 清空（都不传 = 不改） */
interface SipAccountPatch extends Partial<Omit<SipAccountSnapshot, 'passwordSet'>> {
  password?: string;
  clearPassword?: boolean;
}

/** 配置快照（system.getConfig） */
interface SipAppConfig {
  version: string;
  os: string;
  arch: string;
  account: SipAccountSnapshot;
  audio: SipAudioConfig;
  video: SipVideoConfig;
  log: SipLogConfig;
  ws: {
    /** 配置里的期望端口 */
    port: number;
    /** 当前进程实际监听的端口 */
    actualPort: number;
    /** 是否允许远程连接（监听 0.0.0.0、不拒绝非回环来源）；默认 false */
    allowRemote: boolean;
  };
  update: SipUpdateConfig;
  autoStart: boolean;
}

export = SipPhoneSDK;
