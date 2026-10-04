/**
 * mti-sip-phone 网页端 JS SDK
 *
 * 通过本地 WebSocket 连接 SIP 软电话，实现对软电话的控制（拨号/接听/挂断/拒接/静音）
 * 与事件监听（来电振铃、通话建立、挂断、注册状态变化等）。
 *
 * 用法：
 *   const sdk = new SipPhoneSDK();   // 本地 WS 无鉴权参数
 *   sdk.on('call.incoming', (data) => console.log('来电', data.call.remote));
 *   await sdk.connect();
 *   await sdk.dial('1001');
 *
 * 自动更新（REQ-UPD-017/018）：**查询由软电话自己做**——这里只是把更新配置
 * 写进软电话（构造参数或 setUpdateConfig），软电话据此定时查站点、有更新弹提示
 * （下载与重启仍须用户在软电话界面上确认）：
 *   const sdk = new SipPhoneSDK({ updateUrl: 'http://update.example.com' }); // 连上即写入
 *   await sdk.setUpdateConfig({ url: '', });                                // 运行期改/清空
 * 另外保留**强制推送**通道（不受版本与预览版约束，测试页「推送」按钮用）：
 *   await sdk.notifyUpdate({ version: '1.2.3', url: '...', sha256: '...' });
 *
 * 通信录（REQ-SDK-006）：网页端不做增删改查，只提供一键导入/导出；
 * 一键路径走**本地 HTTP 接口**（`GET /api/contacts.csv` 与
 * `POST /api/contacts/import`），没有 WS 的单条消息大小限制；
 * 通话对象（call.contact）里带通信录信息，页面可直接显示名称/备注/常用：
 *   await sdk.downloadContacts();       // 一键导出（浏览器下载 CSV，HTTP）
 *   await sdk.importContactsFromFile(); // 一键导入（选 CSV 文件后合并，HTTP）
 *   await sdk.exportContactsJSON();     // 导出 JSON（REST GET  /api/contacts）
 *   await sdk.importContactsJSON(list); // 导入 JSON（REST POST /api/contacts，收数组或整个对象）
 *
 * 视频（REQ-VIDEO-009）：
 *   await sdk.dialVideo('1001');                     // 视频呼叫
 *   await sdk.subscribeVideo({                       // 订阅并把画面渲染到 <video>
 *     remote: document.getElementById('remoteVideo'),  // 必传
 *     local:  document.getElementById('localVideo')    // 可选（不传 = 不订阅本地回显）
 *   });
 *
 * 协议详见 docs/ws-protocol.md。
 */
(function (global) {
  'use strict';

  const DEFAULT_WS_URL = function () {
    // 页面由软电话提供（http://127.0.0.1:8765/）时同源连接；
    // 以 file:// 打开时回退默认地址。
    if (global.location && /^https?:$/.test(global.location.protocol)) {
      return 'ws://' + global.location.host + '/ws';
    }
    return 'ws://127.0.0.1:8765/ws';
  };

  class SipPhoneSDK {
    /**
     * @param {Object} options
     * @param {string} [options.url]        软电话 WS 地址，默认 ws://127.0.0.1:8765/ws
     * @param {string} [options.schemaUri]  拉起软电话的系统协议 URI（如 sip://账号:密码@服务器）。
     *                                      设置后：首次 WS 连接失败时自动通过该 URI 拉起软电话，
     *                                      之后失败不再自动拉起（每页会话仅一次）
     * @param {boolean} [options.autoReconnect] 断线自动重连，默认 true
     * @param {number} [options.reconnectDelay] 重连间隔毫秒，默认 3000
     * @param {string} [options.updateUrl]  更新站点根地址（REQ-UPD-018）。
     *                                      给了就连上软电话后写入其自动更新配置：软电话据此
     *                                      自己定时查询（不传 = 不改软电话配置）
     * @param {string} [options.updateAppId] 更新站点上的应用 ID，默认 'mti-sip-phone'
     * @param {number} [options.updateIntervalMinutes] 检查周期（分钟，1–1440，默认 5）
     * @param {boolean} [options.updatePrerelease] 是否接受预览版，默认 false
     */
    constructor(options) {
      options = options || {};
      this.url = options.url || DEFAULT_WS_URL();
      this.schemaUri = options.schemaUri || '';
      this.autoReconnect = options.autoReconnect !== false;
      this.reconnectDelay = options.reconnectDelay || 3000;

      // 更新配置（REQ-UPD-018）：给了才在连上后写入软电话（undefined = 不改）
      this._updateConfigWanted = null;
      const upd = {};
      if (options.updateUrl !== undefined) { upd.url = String(options.updateUrl || '').trim(); }
      if (options.updateAppId !== undefined) { upd.appId = String(options.updateAppId || '').trim(); }
      if (options.updateIntervalMinutes !== undefined) { upd.intervalMinutes = Math.round(options.updateIntervalMinutes); }
      if (options.updatePrerelease !== undefined) { upd.prerelease = !!options.updatePrerelease; }
      if (Object.keys(upd).length > 0) { this._updateConfigWanted = upd; }

      this._seq = 0;                 // 请求 ID 自增
      this._pending = new Map();     // id -> { resolve, reject }
      this._handlers = new Map();    // event -> Set<fn>
      this._renderers = {};          // channel -> VideoRenderer（订阅视频时绑定）
      this._videoRouted = false;     // 帧路由与生命周期清理是否已安装
      this._videoSubActive = false;  // 当前是否处于订阅状态（未订阅不套用旋转/宽高）
      this._subOpts = null;          // 最近一次 video.subscribe 的参数（断线重连后自动恢复）
      // 旋转/镜像的**权威状态在软电话侧**（桌面视频窗口与网页共用一份）：
      // 这里只是本地镜像，订阅/退订/断线重连都不丢
      this._videoTx = { remote: { rotation: 0, mirror: false }, local: { rotation: 0, mirror: false } };
      this._ws = null;
      this._closed = false;          // 用户主动关闭，不再重连
      this._connected = false;
      this._heartbeat = null;        // 心跳定时器（保持连接不被服务端读超时断开）
      this._everConnected = false;   // 是否曾成功连接过
      this._launchAttempted = false; // 是否已尝试过通过协议拉起软电话（仅一次）

      // 旋转/镜像状态：连上（含断线重连）时从状态快照恢复，变化时由事件同步
      this.on('system.state', (st) => {
        if (st && st.video) { this._applyTransformState(st.video); }
      });
      this.on('video.transform', (st) => this._applyTransformState(st));
      // 断线重连后自动恢复订阅（订阅是连接级的，新连接默认没有订阅）
      this.on('connected', () => this._restoreVideoSubscription());
      // 业务前端在构造参数里给了更新配置 → 连上后写入软电话（REQ-UPD-018）
      this.on('connected', () => this._applyUpdateConfigOnConnect());
    }

    /** 是否已连接 */
    get connected() {
      return this._connected;
    }

    /**
     * 建立连接。返回 Promise，连接建立后 resolve。
     * @returns {Promise<void>}
     */
    connect() {
      if (this._ws) {
        return Promise.resolve();
      }
      this._closed = false;
      return new Promise((resolve, reject) => {
        let ws;
        try {
          ws = new WebSocket(this.url);
        } catch (e) {
          reject(e);
          return;
        }
        this._ws = ws;
        // 视频帧是二进制消息：用 arraybuffer 接收（Blob 还要异步转换一次）
        ws.binaryType = 'arraybuffer';

        ws.onopen = () => {
          this._connected = true;
          this._everConnected = true;
          this._startHeartbeat();
          this._emit('connected', {});
          resolve();
        };
        ws.onerror = () => {
          // 具体错误信息在 onclose 中统一处理
        };
        ws.onmessage = (ev) => this._onMessage(ev.data);
        ws.onclose = () => {
          this._stopHeartbeat();
          this._connected = false;
          this._ws = null;
          // 未完成的请求全部失败
          this._pending.forEach((p) => p.reject(new Error('连接已断开')));
          this._pending.clear();
          this._emit('disconnected', {});
          // 首次连接失败（从未成功连接过）且配置了 schemaUri：
          // 自动通过系统协议拉起软电话（仅此一次）
          if (!this._closed && !this._everConnected && !this._launchAttempted && this.schemaUri) {
            this._launchAttempted = true;
            launchSchemaUri(this.schemaUri);
          }
          if (!this._closed && this.autoReconnect) {
            setTimeout(() => this.connect().catch(() => {}), this.reconnectDelay);
          }
        };
      });
    }

    /** 主动断开连接（不再自动重连） */
    close() {
      this._closed = true;
      if (this._ws) {
        this._ws.close();
        this._ws = null;
      }
      this._connected = false;
    }

    /**
     * 订阅事件。
     * 事件名：call.incoming / call.ringing / call.established / call.terminated /
     *         call.failed / call.muted / call.audio（麦克风/扬声器开关）/
     *         call.dialing / call.video / video.web /
     *         register.state / system.state /
     *         system.update（自动更新状态：phase/progress/error 等）/
     *         video.frame（订阅视频后逐帧推送，data 为 {channel, seq, blob, size}）/
     *         system.updateConfig（自动更新配置变化）/
     *         connected / disconnected
     * 注：SDK 本地事件（connected / disconnected）不会派发给 '*' 订阅者。
     * @param {string} event 事件名，'*' 订阅全部
     * @param {Function} fn 回调 (data) => {}
     * @returns {this}
     */
    on(event, fn) {
      if (!this._handlers.has(event)) {
        this._handlers.set(event, new Set());
      }
      this._handlers.get(event).add(fn);
      return this;
    }

    /** 取消订阅 */
    off(event, fn) {
      const set = this._handlers.get(event);
      if (set) {
        set.delete(fn);
        if (set.size === 0) {
          this._handlers.delete(event);
        }
      }
      return this;
    }

    /**
     * 拨号。
     * @param {string} target 号码或 SIP URI
     * @param {{video?: boolean}} [options] video=true 发起视频呼叫
     * @returns {Promise<{callId: string}>}
     */
    dial(target, options) {
      options = options || {};
      return this._request('call.dial', { target: target, video: !!options.video });
    }

    /**
     * 视频呼叫（dial 的便捷写法）。
     * @param {string} target 号码或 SIP URI
     * @returns {Promise<{callId: string}>}
     */
    dialVideo(target) {
      return this.dial(target, { video: true });
    }

    /**
     * 接听。
     * @param {string} callId
     * @param {{video?: boolean}} [options] video=true 视频接听（打开摄像头双向视频）
     * @returns {Promise<{callId: string}>}
     */
    answer(callId, options) {
      options = options || {};
      return this._request('call.answer', { callId: callId, video: !!options.video });
    }

    /**
     * 视频接听（answer 的便捷写法）。
     * @param {string} callId
     * @returns {Promise<{callId: string}>}
     */
    answerVideo(callId) {
      return this.answer(callId, { video: true });
    }

    /**
     * 挂断。
     * @param {string} callId
     * @returns {Promise<{callId: string}>}
     */
    hangup(callId) {
      return this._request('call.hangup', { callId: callId });
    }

    /**
     * 拒接。
     * @param {string} callId
     * @param {number} [code] SIP 状态码，默认 486（忙）
     * @returns {Promise<{callId: string}>}
     */
    reject(callId, code) {
      return this._request('call.reject', { callId: callId, code: code || 486 });
    }

    /**
     * 保持（静音）切换。
     * @param {string} callId
     * @param {boolean} muted
     * @returns {Promise<{callId: string}>}
     */
    mute(callId, muted) {
      return this._request('call.mute', { callId: callId, muted: !!muted });
    }

    /**
     * 麦克风开关（纯本地动作，不发 SIP 信令；状态变化推 call.audio 事件）。
     * @param {string} callId
     * @param {boolean} muted true = 静音麦克风（停止发送本地音频）
     * @returns {Promise<{callId: string}>}
     */
    muteMic(callId, muted) {
      return this._request('call.muteMic', { callId: callId, muted: !!muted });
    }

    /**
     * 扬声器开关（纯本地动作，继续接收）。
     * @param {string} callId
     * @param {boolean} muted true = 静音扬声器（停止本地播放）
     * @returns {Promise<{callId: string}>}
     */
    muteSpeaker(callId, muted) {
      return this._request('call.muteSpeaker', { callId: callId, muted: !!muted });
    }

    /**
     * 麦克风增益（REQ-MEDIA-006，纯本地动作，不发 SIP 信令）。
     * 0 = 禁用（等同把麦克风开关关掉），大于 0 时同时解除禁用。
     * 状态变化会推 call.audio 事件（滚轮连调时事件会被合并，见软电话侧说明）。
     * @param {string} callId
     * @param {number} gainPercent 0–200 的百分比整数
     * @returns {Promise<{callId: string}>}
     */
    setMicGain(callId, gainPercent) {
      return this._request('call.setMicGain', { callId: callId, gain: Math.round(gainPercent) });
    }

    /**
     * 扬声器音量（REQ-MEDIA-006，纯本地动作）。
     * 0 = 禁用（等同把扬声器开关关掉），大于 0 时同时解除禁用。
     * @param {string} callId
     * @param {number} volumePercent 0–100 的百分比整数
     * @returns {Promise<{callId: string}>}
     */
    setSpeakerVolume(callId, volumePercent) {
      return this._request('call.setSpeakerVolume', { callId: callId, speakerVolume: Math.round(volumePercent) });
    }

    /**
     * 发送 DTMF（RFC 4733 telephone-event，通话接通后有效，REQ-CALL-006）。
     * @param {string} callId
     * @param {string} digit 单个字符：0-9 * # A-D
     * @returns {Promise<{callId: string}>}
     */
    sendDTMF(callId, digit) {
      return this._request('call.dtmf', { callId: callId, digit: String(digit) });
    }

    /**
     * 视频总开关（纯本地动作，不发 SIP 信令，REQ-VIDEO-008）：
     * 关闭 = 不显示远端视频、不推送本地视频、视频窗口关闭；状态见 call.video 事件。
     * @param {string} callId
     * @param {boolean} enabled
     * @returns {Promise<{callId: string}>}
     */
    setVideo(callId, enabled) {
      return this._request('call.setVideo', { callId: callId, enabled: !!enabled });
    }

    /**
     * 自动接听开关（与软电话界面上的「自动接听」按钮共用一份状态）：
     * 开启后来电自动接听；视频呼叫按视频接听处理（打开摄像头双向视频，
     * 摄像头不可用时只收不发）。
     * 变更后软电话广播 system.autoAnswer 事件，桌面按钮与网页据此同步。
     * @param {boolean} enabled
     * @returns {Promise<{enabled: boolean}>}
     */
    setAutoAnswer(enabled) {
      return this._request('system.setAutoAnswer', { enabled: !!enabled });
    }

    /**
     * 查询软电话当前状态（注册状态、通话列表等）。
     * 返回值含 version / os / arch（前端据此到业务平台查询对应架构的
     * 最新版本，见 notifyUpdate）与 autoAnswer（自动接听开关当前状态）。
     * @returns {Promise<Object>}
     */
    getState() {
      return this._request('system.getState', {});
    }

    /**
     * 导出通信录为 CSV 文本（表头「号码,名称,备注,常用」，带 UTF-8 BOM，
     * Excel 打开不乱码，REQ-WS-015）。
     * 网页端**不做**通信录的增删改查，只提供一键导入/导出。
     * @returns {Promise<{csv: string, count: number}>}
     */
    exportContacts() {
      return this._request('contact.export', {});
    }

    /**
     * 一键导出：取回 CSV 后由浏览器存成文件（默认 contacts-YYYYMMDD.csv）。
     * **走 HTTP 接口**（`GET /api/contacts.csv`）而不是 WS：CSV 是整份文本，
     * 走 HTTP 没有单条消息大小限制，也能拿到明确的错误。依赖 DOM/Blob。
     * @param {string} [filename] 另存的文件名
     * @returns {Promise<{count: number, filename: string}>}
     */
    async downloadContacts(filename) {
      const name = filename ||
        ('contacts-' + new Date().toISOString().slice(0, 10).replace(/-/g, '') + '.csv');
      if (typeof document === 'undefined' || typeof URL === 'undefined' || !URL.createObjectURL) {
        const err = new Error('当前环境不支持浏览器下载');
        err.code = 'unsupported';
        throw err;
      }
      const res = await fetch(this.httpBase() + '/api/contacts.csv', { cache: 'no-store' });
      if (!res.ok) {
        const err = new Error('导出失败（HTTP ' + res.status + '）');
        err.code = 'http_error';
        throw err;
      }
      const blob = await res.blob();
      const count = Number(res.headers.get('X-Contact-Count') || 0);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = name;
      a.style.display = 'none';
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      setTimeout(() => URL.revokeObjectURL(url), 5000);
      return { count: count, filename: name };
    }

    /**
     * 导入通信录 CSV：按号码去重——已存在的条目只覆盖号码/名称/备注，
     * **常用标志只增不减**（本地已标就保持、本地没标而 CSV 标了就变常用）；
     * 不存在的整条新增（常用标志取 CSV 里的值）。
     * @param {string} csv CSV 文本（可带或不带表头）
     * @returns {Promise<{added: number, updated: number, skipped: number, total: number}>}
     */
    importContacts(csv) {
      return this._request('contact.import', { csv: String(csv == null ? '' : csv) });
    }

    /**
     * 导出通信录为 **JSON**（REST：`GET /api/contacts`），返回
     * `{count, contacts: [{number, name, note, starred}, ...]}`。
     * 与 CSV 版（exportContacts / downloadContacts）的区别：这是给程序用的
     * 结构化数据，网页可以直接 XHR 取来渲染，不用自己解析 CSV。
     * @returns {Promise<{count: number, contacts: Array<{number: string, name: string,
     *                    note: string, starred: boolean}>}>}
     */
    async exportContactsJSON() {
      const res = await fetch(this.httpBase() + '/api/contacts', { cache: 'no-store' });
      if (!res.ok) {
        const err = new Error('导出失败（HTTP ' + res.status + '）');
        err.code = 'http_error';
        throw err;
      }
      return res.json();
    }

    /**
     * 导入通信录 **JSON**（REST：`POST /api/contacts`）。请求体可以是
     * `[{number,name,note,starred}, ...]` 裸数组，也可以是 exportContactsJSON()
     * 返回的整个对象（导出→改→导入可直接回灌）。
     * 合并语义与 CSV 导入一致：按号码去重，只覆盖号码/名称/备注，
     * **常用标志只增不减**（本地已标就保持、本地没标而数据标了就变常用）。
     * @param {Array|Object} contacts 联系人数组，或 {contacts: [...]} 对象
     * @returns {Promise<{added: number, updated: number, skipped: number, total: number}>}
     */
    async importContactsJSON(contacts) {
      const body = typeof contacts === 'string' ? contacts : JSON.stringify(contacts);
      const res = await fetch(this.httpBase() + '/api/contacts', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json; charset=utf-8' },
        body: body
      });
      let data = null;
      try {
        data = await res.json();
      } catch (e) {
        data = null;
      }
      if (!res.ok || !data) {
        const msg = (data && data.error) || ('导入失败（HTTP ' + res.status + '）');
        const err = new Error(msg);
        err.code = 'http_error';
        throw err;
      }
      return data;
    }

    /**
     * 一键导入：弹系统文件选择框选 CSV，读出来交给软电话合并。
     * **走 HTTP 接口**（POST /api/contacts/import，请求体即 CSV 文本）而不是 WS：
     * 没有单条消息大小限制。依赖 DOM（浏览器环境接口）；用户取消时以
     * code='cancelled' 拒绝。
     * @returns {Promise<{added: number, updated: number, skipped: number,
     *                    total: number, filename: string}>}
     */
    importContactsFromFile() {
      return new Promise((resolve, reject) => {
        if (typeof document === 'undefined') {
          const err = new Error('当前环境不支持文件选择');
          err.code = 'unsupported';
          reject(err);
          return;
        }
        const input = document.createElement('input');
        input.type = 'file';
        input.accept = '.csv,text/csv';
        input.style.display = 'none';
        let done = false;
        const finish = (fn, arg) => {
          if (done) return;
          done = true;
          if (input.parentNode) input.parentNode.removeChild(input);
          fn(arg);
        };
        input.onchange = () => {
          const file = input.files && input.files[0];
          if (!file) {
            finish(reject, Object.assign(new Error('未选择文件'), { code: 'cancelled' }));
            return;
          }
          this.importContactsFile(file)
            .then((res) => finish(resolve, Object.assign({ filename: file.name }, res)))
            .catch((e) => finish(reject, e));
        };
        // 取消没有事件：窗口回到前台时若还没选到文件，按"已取消"收尾
        // （选过文件时 input.files 非空，不会误判）
        if (global.addEventListener) {
          global.addEventListener('focus', () => {
            setTimeout(() => {
              if (!done && (!input.files || !input.files.length)) {
                finish(reject, Object.assign(new Error('已取消'), { code: 'cancelled' }));
              }
            }, 500);
          }, { once: true });
        }
        document.body.appendChild(input);
        input.click();
      });
    }

    /**
     * 按 File 对象导入通信录（HTTP 直传，不经过 WS）。
     * @param {File|Blob} file
     * @returns {Promise<{added: number, updated: number, skipped: number, total: number}>}
     */
    async importContactsFile(file) {
      const res = await fetch(this.httpBase() + '/api/contacts/import', {
        method: 'POST',
        headers: { 'Content-Type': 'text/csv; charset=utf-8' },
        body: file
      });
      let data = null;
      try {
        data = await res.json();
      } catch (e) {
        data = null;
      }
      if (!res.ok || !data) {
        const msg = (data && data.error) || ('导入失败（HTTP ' + res.status + '）');
        const err = new Error(msg);
        err.code = 'http_error';
        throw err;
      }
      return data;
    }

    /**
     * **强制推送**更新：把指定版本告知软电话，软电话提示用户、由用户确认后更新。
     *
     * 这是 SDK 唯一的更新相关能力——**不受版本高低与预览版约束**（内部恒带
     * force）。「有没有新版本」由**软电话自己**按配置的周期查询更新站点
     * （配置见 setUpdateConfig）；网页若要自己查版本，请自行请求站点接口。
     *
     * @param {{version: string, url: string, sha256?: string, notes?: string,
     *          os?: string, arch?: string}} info 新版本信息
     * @returns {Promise<{phase: string, version: string, currentVersion: string}>}
     */
    notifyUpdate(info) {
      info = info || {};
      if (!info.version || !info.url) {
        return Promise.reject(new Error('notifyUpdate 需要 version 与 url'));
      }
      return this._request('system.notifyUpdate', {
        version: info.version,
        url: info.url,
        sha256: info.sha256,
        notes: info.notes,
        os: info.os,
        arch: info.arch,
        force: true // 本方法就是强制推送通道
      });
    }

    /**
     * 写入软电话的**自动更新配置**（REQ-UPD-018）：软电话据此自己定时查询更新
     * 站点（配了站点才会查），查到新版本弹提示窗。字段都是可选的，只改传入的。
     *
     * 构造参数里给了 `updateUrl` / `updateAppId` / `updateIntervalMinutes` /
     * `updatePrerelease` 时，连上软电话后会自动写入一次（业务前端统一管理策略）。
     *
     * @param {{url?: string, appId?: string, intervalMinutes?: number,
     *          prerelease?: boolean}} cfg url 空 = 不自动检查；周期 1–1440 分钟
     * @returns {Promise<{url: string, appId: string, intervalMinutes: number,
     *          prerelease: boolean}>} 生效后的完整配置
     */
    setUpdateConfig(cfg) {
      cfg = cfg || {};
      const params = {};
      if (cfg.url !== undefined) { params.url = String(cfg.url || '').trim(); }
      if (cfg.appId !== undefined) { params.appId = String(cfg.appId || '').trim(); }
      if (cfg.intervalMinutes !== undefined) { params.intervalMinutes = Math.round(cfg.intervalMinutes); }
      if (cfg.prerelease !== undefined) { params.prerelease = !!cfg.prerelease; }
      this._updateConfigWanted = Object.assign({}, this._updateConfigWanted, params);
      if (!this._connected) {
        return Promise.reject(new Error('尚未连接软电话'));
      }
      return this._request('system.setUpdateConfig', params);
    }

    /** _applyUpdateConfigOnConnect 连上后把构造参数里的更新配置写入软电话。 */
    _applyUpdateConfigOnConnect() {
      if (!this._updateConfigWanted) { return; }
      this._request('system.setUpdateConfig', this._updateConfigWanted).catch(() => {
        // 写入失败不影响其它功能（页面可从 system.state.updateConfig 看到实际值）
      });
    }

    /**
     * 订阅视频画面（REQ-VIDEO-009）。**SDK 自己把画面渲染到传入的 <video>**，
     * 页面不必处理 video.frame、也不用建渲染器。
     *
     *   await sdk.subscribeVideo({
     *     remote: document.getElementById('remoteVideo'),  // 必传
     *     local:  document.getElementById('localVideo'),   // 可选：不传 = 不订阅本地回显
     *     fps: 30, quality: 75,                            // 可选：两路共用的参数
     *     onStats: (channel, stats) => { ... }             // 可选：每秒回调一次
     *   });
     *
     * 每条通道也可以写成**带参数的对象**，两路各要各的（本地回显通常小一号）：
     *   remote: { el: remoteVideo, maxWidth: 640, maxHeight: 480, fps: 30, quality: 75 }
     * maxWidth/maxHeight 不传时直接取 <video> 元素当前的显示尺寸。
     *
     * **订阅必定包含远端画面**（软电话强制）；订阅期间软电话不再显示视频窗口，
     * 关闭视频/挂断/退订时 SDK 会把画面清成黑底。
     * 参数越界会被软电话钳制到合法范围（返回值里是生效值）。
     *
     * @param {{remote: (HTMLVideoElement|Object), local?: (HTMLVideoElement|Object),
     *          maxWidth?: number, maxHeight?: number, fps?: number, quality?: number,
     *          onStats?: function}} options
     * @returns {Promise<{remote: Object, local?: Object, call: Object|null}>}
     */
    subscribeVideo(options) {
      options = options || {};
      const remote = pickChannel(options.remote);
      if (!remote) {
        return Promise.reject(new Error('subscribeVideo 需要 remote 视频元素'));
      }
      const local = pickChannel(options.local);
      const params = { remote: channelParams(remote, options) };
      if (local) {
        params.local = channelParams(local, options);
      }

      this._subOpts = params; // 记住订阅参数：断线重连后自动恢复
      return this._request('video.subscribe', params).then((res) => {
        this._ensureVideoRouting();
        // 订阅时以软电话的当前状态为准（转/镜像可能是桌面那边设的）：
        // 先记录，再绑定渲染器（绑定即套用当前状态），最后标记"订阅中"
        this._applyTransformState(res.transforms);
        this._bindVideoChannel('remote', remote, options);
        if (local) {
          this._bindVideoChannel('local', local, options);
        }
        this._videoSubActive = true;
        return this._decorateSubscription(res);
      });
    }

    /**
     * 画面旋转（每次顺时针 +90°，90°/270° 时宽高互换，<video> 的框跟着变）。
     * 状态存在软电话侧：桌面视频窗口的按钮会同步变，断线/退订都不丢。
     * @param {string} [channel] 'remote'（默认）或 'local'
     * @returns {Promise<{remote: Object, local: Object}>} 旋转后的完整状态
     */
    rotateVideo(channel) {
      return this._request('video.setTransform', { channel: channel || 'remote', rotate: true })
        .then((st) => this._applyTransformState(st));
    }

    /**
     * 画面水平镜像开关（本地回显常用作自拍镜像）。状态同 rotateVideo。
     * @param {string} [channel] 'remote'（默认）或 'local'
     * @returns {Promise<{remote: Object, local: Object}>} 切换后的完整状态
     */
    mirrorVideo(channel) {
      const ch = channel || 'remote';
      const cur = (this._videoTx && this._videoTx[ch]) || { mirror: false };
      return this._request('video.setTransform', { channel: ch, mirror: !cur.mirror })
        .then((st) => this._applyTransformState(st));
    }

    /**
     * 当前已知的旋转/镜像状态（远端 + 本地回显）。
     * @returns {{remote: {rotation: number, mirror: boolean}, local: {rotation: number, mirror: boolean}}}
     */
    videoTransforms() {
      return this._videoTx;
    }

    /** 内部：给订阅响应挂上 rotate/mirror/clear 控制方法 */
    _decorateSubscription(res) {
      const sdk = this;
      res.rotate = function (channel) { return sdk.rotateVideo(channel || 'remote'); };
      res.mirror = function (channel) { return sdk.mirrorVideo(channel || 'remote'); };
      res.clear = function () { sdk.clearVideo(); };
      res.transforms = function () { return sdk.videoTransforms(); };
      return res;
    }

    /** 内部：状态同步到本地（渲染器 + 供页面查询） */
    _applyTransformState(st) {
      if (!st) {
        return;
      }
      const zero = { rotation: 0, mirror: false };
      this._videoTx = { remote: st.remote || zero, local: st.local || zero };
      // **未订阅不改画面/宽高**（用户要求）：桌面那边旋转视频窗口时，网页上
      // 那两个 <video> 的尺寸不该跟着变；订阅时才按当前状态套用。
      if (!this._videoSubActive) {
        return this._videoTx;
      }
      Object.keys(this._renderers).forEach((ch) => {
        const t = this._videoTx[ch];
        if (t) {
          this._renderers[ch].setTransform(t.rotation, t.mirror);
        }
      });
      return this._videoTx;
    }

    /** 内部：断线重连后恢复订阅（画面元素不变，只重新登记） */
    _restoreVideoSubscription() {
      if (!this._subOpts) {
        return;
      }
      this._request('video.subscribe', this._subOpts)
        .then((res) => { this._decorateSubscription(res); })
        .catch(() => {});
    }

    /**
     * 取消本连接的视频订阅（软电话恢复显示视频窗口），画面清成黑底。
     * @returns {Promise<{remote: Object, local?: Object}>}
     */
    unsubscribeVideo() {
      // 主动退订要**忘掉**订阅参数：否则断线重连时会把它偷偷恢复回来，
      // 用户看到的现象是"取消了订阅，软电话的视频窗口却一直不回来"。
      this._subOpts = null;
      this._videoSubActive = false;
      return this._request('video.unsubscribe', {}).then((res) => {
        this.clearVideo();
        // 取消订阅后不再受旋转/镜像影响：<video> 恢复默认宽高比（用户要求）
        Object.keys(this._renderers).forEach((ch) => {
          this._renderers[ch].resetAspect();
        });
        return res;
      });
    }

    /**
     * 清空已订阅的画面（黑底）。关闭视频、挂断、退订时 SDK 会自动调用，
     * 页面一般不用自己调。
     */
    clearVideo() {
      Object.keys(this._renderers).forEach((ch) => {
        this._renderers[ch].clear();
      });
    }

    /** 内部：发送请求并等待响应 */
    /**
     * 内部：由 WS 地址推导 HTTP 基址。
     *
     * **不能直接取 `new URL(ws://…).origin`**：对非 http(s) 协议，origin 的
     * 序列化会把 scheme 原样带上（得到 "ws://127.0.0.1:8765"），拿它去 fetch
     * 会直接报 "Failed to fetch"（fetch 不支持 ws 协议）。必须换成 http/https。
     */
    httpBase() {
      try {
        const u = new URL(this.url);
        return (u.protocol === 'wss:' ? 'https://' : 'http://') + u.host;
      } catch (e) {
        return 'http://127.0.0.1:8765';
      }
    }

    _request(method, params) {
      if (!this._connected) {
        return Promise.reject(new Error('尚未连接软电话'));
      }
      const id = ++this._seq;
      return new Promise((resolve, reject) => {
        this._pending.set(id, { resolve: resolve, reject: reject });
        try {
          this._ws.send(JSON.stringify({ id: id, method: method, params: params }));
        } catch (e) {
          this._pending.delete(id);
          reject(e);
        }
      });
    }

    /** 内部：处理服务端消息（文本 JSON 或二进制视频帧） */
    _onMessage(raw) {
      if (raw instanceof ArrayBuffer) {
        this._onBinary(raw);
        return;
      }
      if (typeof Blob !== 'undefined' && raw instanceof Blob) {
        raw.arrayBuffer().then((buf) => this._onBinary(buf)).catch(() => {});
        return;
      }
      let msg;
      try {
        msg = JSON.parse(raw);
      } catch (e) {
        return;
      }
      if (msg.type === 'event') {
        this._emit(msg.event, msg.data || {});
        this._emit('*', { event: msg.event, data: msg.data || {} });
        return;
      }
      // 响应：按 id 关联到未完成请求
      if (msg.id !== undefined && msg.id !== null) {
        const p = this._pending.get(msg.id);
        if (p) {
          this._pending.delete(msg.id);
          if (msg.ok) {
            p.resolve(msg.data);
          } else {
            const err = new Error((msg.error && msg.error.message) || '未知错误');
            err.code = msg.error && msg.error.code;
            p.reject(err);
          }
        }
      }
    }

    /**
     * 内部：解析二进制视频帧并派发 video.frame 事件。
     * 帧格式：[1字节 通道（1=远端 2=本地）][4字节序号 大端][JPEG 数据]
     */
    _onBinary(buf) {
      if (!buf || buf.byteLength < 5) {
        return;
      }
      const view = new DataView(buf);
      const data = {
        channel: view.getUint8(0) === 2 ? 'local' : 'remote',
        seq: view.getUint32(1),
        blob: new Blob([new Uint8Array(buf, 5)], { type: 'image/jpeg' })
      };
      data.size = data.blob.size;
      this._emit('video.frame', data);
      this._emit('*', { event: 'video.frame', data: data });
    }

    /** 内部：帧路由 + 生命周期清理只装一次 */
    _ensureVideoRouting() {
      if (this._videoRouted) {
        return;
      }
      this._videoRouted = true;
      // 订阅的通道自动渲染（video.frame 事件仍会派发，供需要自定义显示的页面用）
      this.on('video.frame', (f) => {
        const r = this._renderers[f.channel];
        if (r) { r.render(f.blob); }
      });
      // 关闭视频 / 挂断 / 呼叫失败：画面清成黑底，别定格在最后一帧
      this.on('call.video', (data) => {
        const v = data && data.call && data.call.video;
        if (v && !v.enabled) { this.clearVideo(); }
      });
      this.on('call.terminated', () => this.clearVideo());
      this.on('call.failed', () => this.clearVideo());
    }

    /** 内部：把一条通道绑定到 <video> 元素（含 onStats 回调） */
    _bindVideoChannel(channel, co, top) {
      const onStats = co.onStats || top.onStats || null;
      const old = this._renderers[channel];
      if (old && old.el !== co.el) {
        old.destroy();
      }
      const r = new VideoRenderer(co.el, {
        fps: co.fps || top.fps,
        onStats: onStats ? (stats) => onStats(channel, stats) : null
      });
      // 旋转/镜像状态不随（重新）订阅丢失：新渲染器套用当前状态
      const t = this._videoTx && this._videoTx[channel];
      if (t) {
        r.setTransform(t.rotation, t.mirror);
      }
      this._renderers[channel] = r;
    }


    /** 内部：启动心跳（每 25 秒发一条空消息，重置服务端读超时） */
    _startHeartbeat() {
      this._stopHeartbeat();
      this._heartbeat = setInterval(() => {
        if (this._ws && this._ws.readyState === WebSocket.OPEN) {
          try {
            this._ws.send('{}');
          } catch (e) {
            // 连接异常由 onclose 统一处理
          }
        }
      }, 25000);
    }

    /** 内部：停止心跳 */
    _stopHeartbeat() {
      if (this._heartbeat) {
        clearInterval(this._heartbeat);
        this._heartbeat = null;
      }
    }

    /** 内部：派发事件 */
    _emit(event, data) {
      const set = this._handlers.get(event);
      if (set) {
        set.forEach((fn) => {
          try {
            fn(data);
          } catch (e) {
            console.error('[sip-phone-sdk] 事件回调异常', event, e);
          }
        });
      }
    }  }

  /**
   * pickChannel 归一化一条视频通道：允许直接传 <video> 元素，或传
   * { el, maxWidth, maxHeight, fps, quality, onStats }。无效时返回 null。
   */
  function pickChannel(ch) {
    if (!ch) {
      return null;
    }
    if (ch.nodeType) {
      return { el: ch }; // 直接给了 <video> 元素
    }
    if (typeof ch === 'object' && ch.el && ch.el.nodeType) {
      return ch;
    }
    return null;
  }

  /**
   * channelParams 算出某条通道要发给软电话的参数：
   * 通道自己的值 → 顶层默认值 → <video> 元素当前显示尺寸。
   * 都没有的字段就不传，由软电话取默认（640×640 / 30fps / 质量 75）。
   */
  function channelParams(co, top) {
    const num = function (v) { return (typeof v === 'number' && v > 0) ? v : 0; };
    const el = co.el || {};
    const p = {};
    const w = num(co.maxWidth) || num(top.maxWidth) || num(el.clientWidth) || num(el.getAttribute && el.getAttribute('width'));
    const h = num(co.maxHeight) || num(top.maxHeight) || num(el.clientHeight) || num(el.getAttribute && el.getAttribute('height'));
    if (w) { p.maxWidth = w; }
    if (h) { p.maxHeight = h; }
    const fps = num(co.fps) || num(top.fps);
    if (fps) { p.fps = fps; }
    const q = num(co.quality) || num(top.quality);
    if (q) { p.quality = q; }
    return p;
  }

  /**
   * VideoRenderer 视频渲染器（SDK 内部使用）：把 JPEG 帧画进 canvas，再由 canvas.captureStream()
   * 产出 MediaStream 喂给 h5 <video>。
   *
   * 为什么不用 blob URL + <img>/MSE：需求要求用 h5 video 显示；而 MediaSource
   * 不支持 MJPEG，逐帧 blob URL 又要反复创建/回收。canvas 画布流既满足 <video>
   * 播放，每帧也只是往画布上贴一张图。
   *
   * 渲染跟不上时**直接丢帧**（同一时刻只允许一帧在解码），不排队——
   * 与软电话侧的丢帧策略一致，延迟不累积。
   */
  class VideoRenderer {
    constructor(videoEl, options) {
      options = options || {};
      this.el = videoEl;
      this.canvas = document.createElement('canvas');
      this.canvas.width = 2;
      this.canvas.height = 2;
      this.ctx = this.canvas.getContext('2d');
      this.onStats = options.onStats || null;

      this.fps = 0;       // 每秒帧数（统计值）
      this.kbps = 0;      // 每秒码率（统计值）
      this.width = 0;     // 当前画面的宽（含旋转：90°/270° 时是源图的高）
      this.height = 0;
      this.frames = 0;    // 累计帧数

      this.rotation = 0;  // 旋转角度（0/90/180/270，顺时针）
      this.mirror = false; // 水平镜像
      this.srcW = 0;      // 最近一帧的原始宽高（算旋转后的框用）
      this.srcH = 0;

      this._busy = false; // 有帧正在解码/绘制
      this._n = 0;        // 统计窗口内的帧数
      this._bytes = 0;    // 统计窗口内的字节数
      this._at = 0;       // 统计窗口起点

      if (this.canvas.captureStream) {
        this.stream = this.canvas.captureStream(options.fps || 30);
        if (videoEl) {
          videoEl.srcObject = this.stream;
          videoEl.muted = true; // 画面无音轨；静音避免自动播放被策略拦截
          const p = videoEl.play();
          if (p && p.catch) { p.catch(() => {}); }
        }
      }
    }

    /**
     * 渲染一帧（来自 video.frame 事件的 blob）。
     * @param {Blob} blob image/jpeg
     */
    render(blob) {
      if (this._busy || !blob || !this.ctx) {
        return; // 上一帧还没画完：丢这一帧
      }
      this._busy = true;
      createImageBitmap(blob).then((bmp) => {
        const rot = ((this.rotation % 360) + 360) % 360;
        const swap = (rot === 90 || rot === 270);
        const w = swap ? bmp.height : bmp.width;
        const h = swap ? bmp.width : bmp.height;
        this.srcW = bmp.width;
        this.srcH = bmp.height;
        if (this.canvas.width !== w || this.canvas.height !== h) {
          this.canvas.width = w;   // 旋转 90°/270° 时宽高互换
          this.canvas.height = h;
          this.width = w;
          this.height = h;
          this._applyAspect();
        }
        const ctx = this.ctx;
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.fillStyle = '#000';
        ctx.fillRect(0, 0, w, h); // 旋转后四角不留上一帧的残影
        ctx.translate(w / 2, h / 2);
        ctx.rotate(rot * Math.PI / 180);
        if (this.mirror) {
          ctx.scale(-1, 1);       // 先镜像再旋转（与桌面端一致）
        }
        ctx.drawImage(bmp, -bmp.width / 2, -bmp.height / 2);
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        if (bmp.close) { bmp.close(); }
        this._tick(blob.size);
        this._busy = false;
      }).catch(() => {
        this._busy = false;
      });
    }

    /** 设定旋转/镜像（状态由软电话下发，见 SDK 的 _applyTransformState）。 */
    setTransform(rotation, mirror) {
      this.rotation = ((Number(rotation) || 0) % 360 + 360) % 360;
      this.mirror = !!mirror;
      this._applyAspect();
    }

    /** 恢复 <video> 的默认宽高比（取消订阅时调用）。 */
    resetAspect() {
      if (this.el && this.el.style) {
        this.el.style.aspectRatio = '';
      }
    }

    /** 按旋转后的宽高比调整 <video> 的显示框（CSS aspect-ratio 覆盖默认值）。 */
    _applyAspect() {
      if (!this.el || !this.el.style || !this.srcW || !this.srcH) {
        return;
      }
      const swap = (this.rotation === 90 || this.rotation === 270);
      const w = swap ? this.srcH : this.srcW;
      const h = swap ? this.srcW : this.srcH;
      this.el.style.aspectRatio = w + ' / ' + h;
    }

    /**
     * 清空画面（关闭视频/挂断/取消订阅时调用）：填成黑底并清零统计。
     * 不这么做的话，视频停掉以后 <video> 会一直定格在最后一帧上。
     */
    clear() {
      this._busy = false;
      this._n = 0;
      this._bytes = 0;
      this._at = 0;
      this.fps = 0;
      this.kbps = 0;
      this.frames = 0;
      if (!this.ctx) { return; }
      this.ctx.fillStyle = '#000';
      this.ctx.fillRect(0, 0, this.canvas.width, this.canvas.height);
    }

    /** 释放画布流（页面切换/通话结束时调用）。 */
    destroy() {
      if (this.stream) {
        this.stream.getTracks().forEach((t) => t.stop());
        this.stream = null;
      }
      if (this.el) {
        this.el.srcObject = null;
      }
    }

    /** 内部：统计 fps 与码率，每秒回调一次 onStats */
    _tick(bytes) {
      this.frames++;
      this._n++;
      this._bytes += bytes || 0;
      const now = (typeof performance !== 'undefined' && performance.now) ? performance.now() : Date.now();
      if (!this._at) {
        this._at = now;
        return;
      }
      const dt = now - this._at;
      if (dt >= 1000) {
        this.fps = Math.round(this._n * 1000 / dt);
        this.kbps = Math.round(this._bytes * 8 / dt); // 字节/毫秒 = kbit/s
        this._n = 0;
        this._bytes = 0;
        this._at = now;
        if (this.onStats) {
          this.onStats({ fps: this.fps, kbps: this.kbps, width: this.width, height: this.height });
        }
      }
    }
  }

  /**
   * 通过隐藏 iframe 触发系统协议 URI（拉起软电话），不导航当前页面。
   * 浏览器会弹出协议确认提示（首次）。
   */
  function launchSchemaUri(uri) {
    try {
      var iframe = document.createElement('iframe');
      iframe.style.display = 'none';
      iframe.src = uri;
      document.body.appendChild(iframe);
      setTimeout(function () {
        if (iframe.parentNode) {
          iframe.parentNode.removeChild(iframe);
        }
      }, 3000);
    } catch (e) {
      // 拉起失败不影响 SDK 自身逻辑
    }
  }

  global.SipPhoneSDK = SipPhoneSDK;
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = SipPhoneSDK;
  }
})(typeof window !== 'undefined' ? window : globalThis);
