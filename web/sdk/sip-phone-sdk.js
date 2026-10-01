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
     *         call.failed / call.muted / register.state / system.state /
     *         system.update（自动更新状态：phase/progress/error 等）/ connected / disconnected
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
     * @returns {Promise<{callId: string}>}
     */
    dial(target) {
      return this._request('call.dial', { target: target });
    }

    /**
     * 接听。
     * @param {string} callId
     * @returns {Promise<{callId: string}>}
     */
    answer(callId) {
      return this._request('call.answer', { callId: callId });
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
     * 静音切换。
     * @param {string} callId
     * @param {boolean} muted
     * @returns {Promise<{callId: string}>}
     */
    mute(callId, muted) {
      return this._request('call.mute', { callId: callId, muted: !!muted });
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

    /** 内部：处理服务端消息 */
    _onMessage(raw) {
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
