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
 * 视频（REQ-VIDEO-009）：
 *   await sdk.dialVideo('1001');                     // 视频呼叫
 *   await sdk.subscribeVideo({local: true, remote: true});   // 订阅视频画面
 *   const r = sdk.createVideoRenderer(document.getElementById('v'));
 *   sdk.on('video.frame', (f) => { if (f.channel === 'remote') r.render(f.blob); });
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
     */
    constructor(options) {
      options = options || {};
      this.url = options.url || DEFAULT_WS_URL();
      this.schemaUri = options.schemaUri || '';
      this.autoReconnect = options.autoReconnect !== false;
      this.reconnectDelay = options.reconnectDelay || 3000;

      this._seq = 0;                 // 请求 ID 自增
      this._pending = new Map();     // id -> { resolve, reject }
      this._handlers = new Map();    // event -> Set<fn>
      this._ws = null;
      this._closed = false;          // 用户主动关闭，不再重连
      this._connected = false;
      this._heartbeat = null;        // 心跳定时器（保持连接不被服务端读超时断开）
      this._everConnected = false;   // 是否曾成功连接过
      this._launchAttempted = false; // 是否已尝试过通过协议拉起软电话（仅一次）
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
     *         connected / disconnected
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
     * 查询软电话当前状态（注册状态、通话列表等）。
     * 返回值含 version / os / arch（前端据此到业务平台查询对应架构的
     * 最新版本，见 notifyUpdate）。
     * @returns {Promise<Object>}
     */
    getState() {
      return this._request('system.getState', {});
    }

    /**
     * 告知软电话有新版本可用（自动更新）。软电话只提示用户，
     * 由用户在软电话界面上确认后才会下载、替换并重启。
     * @param {{version: string, url: string, sha256?: string, notes?: string,
     *          os?: string, arch?: string}} info 新版本信息；
     *        version 必须高于软电话当前版本，否则返回 update_not_newer
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
        arch: info.arch
      });
    }


    /**
     * 订阅视频画面（REQ-VIDEO-009）。订阅期间软电话**不再显示视频窗口**，
     * 画面以 JPEG 帧经 WS 二进制消息推来，见 video.frame 事件。
     *
     * **订阅必定包含远端画面**：软电话强制订阅远端（`remote` 传 false 也会被忽略），
     * `local` 只表示要不要本地画面。
     *
     * 参数越界会被软电话钳制到合法范围（返回值里是生效值）。
     * @param {{local?: boolean, remote?: boolean, maxWidth?: number, maxHeight?: number,
     *          fps?: number, quality?: number}} [options]
     *        local 默认 true；maxWidth/maxHeight 默认 640×640，
     *        fps 默认 30（上限 30），quality 默认 75
     * @returns {Promise<{local: boolean, remote: boolean, maxWidth: number, maxHeight: number,
     *                    fps: number, quality: number, call: Object|null}>}
     */
    subscribeVideo(options) {
      options = options || {};
      const params = {
        // 远端画面恒订阅（软电话强制，见下），本地画面可选、默认订阅
        local: options.local !== undefined ? !!options.local : true,
        remote: true
      };
      if (options.maxWidth) { params.maxWidth = options.maxWidth; }
      if (options.maxHeight) { params.maxHeight = options.maxHeight; }
      if (options.fps) { params.fps = options.fps; }
      if (options.quality) { params.quality = options.quality; }
      return this._request('video.subscribe', params);
    }

    /**
     * 取消本连接的视频订阅（软电话恢复显示视频窗口）。
     * @returns {Promise<{local: boolean, remote: boolean}>}
     */
    unsubscribeVideo() {
      return this._request('video.unsubscribe', {});
    }

    /**
     * 创建视频渲染器：把 video.frame 的 JPEG 帧画进 canvas，再以
     * canvas.captureStream() 作为 MediaStream 喂给页面上的 h5 <video>。
     *
     *   const renderer = sdk.createVideoRenderer(document.getElementById('remoteVideo'));
     *   sdk.on('video.frame', (f) => { if (f.channel === 'remote') renderer.render(f.blob); });
     *
     * @param {HTMLVideoElement} videoEl 目标 <video>（自动设置 srcObject 并播放）
     * @param {{fps?: number, onStats?: Function}} [options]
     *        fps 画布流的目标帧率（默认 30）；onStats 每秒回调 {fps, kbps, width, height}
     * @returns {VideoRenderer}
     */
    createVideoRenderer(videoEl, options) {
      return new VideoRenderer(videoEl, options);
    }

    /** 内部：发送请求并等待响应 */
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
    }
  }

  /**
   * VideoRenderer 视频渲染器：把 JPEG 帧画进 canvas，再由 canvas.captureStream()
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
      this.width = 0;     // 最近一帧的宽
      this.height = 0;
      this.frames = 0;    // 累计帧数

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
        if (this.canvas.width !== bmp.width || this.canvas.height !== bmp.height) {
          this.canvas.width = bmp.width;
          this.canvas.height = bmp.height;
          this.width = bmp.width;
          this.height = bmp.height;
        }
        this.ctx.drawImage(bmp, 0, 0);
        if (bmp.close) { bmp.close(); }
        this._tick(blob.size);
        this._busy = false;
      }).catch(() => {
        this._busy = false;
      });
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
