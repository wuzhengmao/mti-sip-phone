/*!
 * sip-phone-bootstrap
 * SIP 软电话的引导加载器：负责「探测/拉起软电话」→「从软电话动态加载 SDK」，
 * 并在 WS 断线重连后检测版本/构建时间变化、自动重新加载 SDK。
 *
 * 解决的问题：
 *   - 应用只打包这份极小的 bootstrap，真正的 SDK 每次从本机软电话（HTTP）加载，
 *     因此 SDK 升级不再需要重新打包/重新发布应用；
 *   - 软电话更新后，页面无需刷新：WS 重连上后会重新对比版本 + 构建时间，变了就
 *     重新加载 SDK 并重建连接。
 *
 * 用法：
 *   <script src="sip-phone-bootstrap.js"></script>
 *   <script>
 *     // connect() 同步返回一个**稳定的门面**（不是 SDK 实例）：
 *     // 方法/属性始终指向当前实例，热更新换实例时自动切换并重挂事件订阅。
 *     const sdk = SipPhoneBootstrap.connect({
 *       url: 'ws://127.0.0.1:8765/ws',              // 可选，默认 8765
 *       sipUri: 'sip://2999:pwd@10.168.4.40:15060', // 可选：软电话未运行时用它拉起
 *       onClient: (sdk2, meta) => {                 // 可选：首次连接与热更新后各回调一次
 *         console.log(meta.version, meta.buildTime);
 *       },
 *     });
 *     sdk.on('call.incoming', () => sdk.answer());  // 只订阅一次
 *     // 订阅也由门面保管：成功后记住参数，热更新换实例时自动重订
 *     sdk.on('connected', () => sdk.subscribeVideo({ remote: el }));
 *     sdk.ready.then(() => sdk.dial('1001'));       // 首次连接成功（首次失败会 reject，但引导器
 *                                                   // 会继续在后台探测/拉起，之后靠 connected 事件）
 *   </script>
 *
 * 兼容方式：既可作为 <script> 引入（暴露全局 SipPhoneBootstrap），也可作为 CommonJS 模块 require。
 */
(function (root, factory) {
  if (typeof module === 'object' && module.exports) {
    module.exports = factory(root);
  } else {
    root.SipPhoneBootstrap = factory(root);
  }
})(typeof self !== 'undefined' ? self : this, function (root) {
  'use strict';

  var DEFAULT_HTTP_PORT = 8765;
  var PORT_PROBE_MAX = 10;        // 最多试探 10 个端口
  var PORT_PROBE_TIMEOUT = 500;   // 单个端口探测超时（毫秒）
  var SCAN_STEP_INTERVAL = 50;    // 端口扫描相邻两探的间隔（死端口拒绝连接极快，加间隔避免 10 个请求瞬间刷出）
  var RECONNECT_BASE_INTERVAL = 1000;  // 断线重连退避的起始间隔（毫秒）
  var RECONNECT_MAX_INTERVAL = 30000;  // 断线重连退避的上限（参考 SDK 的 maxReconnectInterval）
  // 应用主动重启/安装更新时，软电话会用这个关闭码关闭 WS（见 wsapi.CloseCodeRestart）；
  // 浏览器只在 1000 与 3000–4999 时暴露 CloseEvent.reason，所以用 4409 而不是 1001。
  var RESTART_CLOSE_CODE = 4409;
  var RESTART_RECONNECT_BASE = 500;     // 重启期间原端口重连的起始间隔
  var RESTART_RECONNECT_MAX = 5000;     // 重启期间原端口重连的间隔上限
  var RESTART_WAIT_MAX = 120000;        // 等多久还没回来就回落到常规流程（扫描/拉起）
  var LAUNCH_WAIT_ATTEMPTS = 30;  // 拉起后最多等 30 × 500ms ≈ 15 秒
  var LAUNCH_WAIT_INTERVAL = 500;
  // 自动拉起（无用户手势）用的短超时：Chrome 会直接拒绝，没必要干等 15 秒再提示用户
  var AUTO_LAUNCH_WAIT_ATTEMPTS = 8; // 8 × 500ms = 4 秒

  // ---- 基础工具 ----

  function delay(ms) {
    return new Promise(function (resolve) { setTimeout(resolve, ms); });
  }

  // 下面几个地址工具都走 `new URL` 解析，而不是字符串截断：IPv6（`ws://[::1]:8765`）
  // 里的冒号会让 `/:(\d+)/` 之类的正则误判成端口；wss:// 也要映射成 https://
  // （与 SDK 的 httpBase() 同一口径）。解析失败才退回字符串处理。

  // ws(s)://host:port/path -> http(s)://host:port
  function httpBaseFromWsUrl(wsUrl) {
    try {
      var u = new URL(wsUrl);
      return (u.protocol === 'wss:' ? 'https:' : 'http:') + '//' + u.host;
    } catch (e) {
      return String(wsUrl || '').replace(/^ws(s?):\/\//, 'http$1://').replace(/\/.*$/, '');
    }
  }

  // 从 wsUrl 解析端口（没有显式端口时用默认 8765——本应用始终带端口）。
  function portOf(wsUrl) {
    try {
      var p = new URL(wsUrl).port;
      return p ? parseInt(p, 10) : DEFAULT_HTTP_PORT;
    } catch (e) {
      var m = /:(\d+)/.exec(wsUrl || '');
      return m ? parseInt(m[1], 10) : DEFAULT_HTTP_PORT;
    }
  }

  // 取主机名（保留 IPv6 的方括号），扫描/找空闲端口时与 httpBase 保持同一台机器。
  function hostOf(httpBase) {
    try { return new URL(httpBase).hostname; } catch (e) { return '127.0.0.1'; }
  }

  function httpAt(host, port) {
    return 'http://' + host + ':' + port;
  }

  // 用指定端口重建 httpBase。
  function withPort(httpBase, port) {
    try {
      var u = new URL(httpBase);
      u.port = String(port);
      return u.protocol + '//' + u.host;
    } catch (e) {
      if (/:\d+\/?$/.test(httpBase)) return httpBase.replace(/:\d+(\/?)$/, ':' + port + '$1');
      return httpBase.replace(/\/$/, '') + ':' + port;
    }
  }

  // 用指定端口重建 wsUrl（保持 host 与路径）。
  function wsUrlWithPort(wsUrl, port) {
    try {
      var u = new URL(wsUrl);
      u.port = String(port);
      return u.toString();
    } catch (e) {
      if (/:\d+/.test(wsUrl)) return wsUrl.replace(/:\d+/, ':' + port);
      return wsUrl.replace(/(\/\/[^/]+)/, '$1:' + port);
    }
  }

  // 探测一个地址是否为软电话：
  //   - phone：能拿到 /api/status 且含 version
  //   - occupied：端口有服务，但不是软电话
  //   - free：连接被拒（或超时），端口空闲
  // 用 AbortController 限时，避免浏览器对「拒绝连接」的 fetch 偶发长时间挂起拖慢扫描。
  function probePhone(httpBase) {
    var ctrl = (typeof AbortController !== 'undefined') ? new AbortController() : null;
    var timer = ctrl ? setTimeout(function () { ctrl.abort(); }, PORT_PROBE_TIMEOUT) : null;
    return fetch(httpBase + '/api/status', { cache: 'no-store', signal: ctrl ? ctrl.signal : undefined })
      .then(function (r) {
        if (timer) clearTimeout(timer);
        if (!r.ok) return { kind: 'occupied' };
        return r.json().then(function (j) {
          if (j && j.version !== undefined) {
            return { kind: 'phone', version: j.version, buildTime: j.buildTime || '' };
          }
          return { kind: 'occupied' };
        });
      })
      .catch(function () {
        if (timer) clearTimeout(timer);
        return { kind: 'free' };
      });
  }

  // 从 startPort 往上扫描：找到已运行的软电话直接返回；否则返回 null。
  // 10 个都试探不到就放弃（软电话的 WS 端口默认 8765，占用会自动 +1，沙箱实例 +10×槽位）。
  // host 取自 httpBase——远程接入（允许远程连接）时也要扫同一台机器，不能固定回环地址。
  function scanPorts(startPort, host) {
    host = host || '127.0.0.1';
    function step(i) {
      if (i >= PORT_PROBE_MAX) return Promise.resolve(null);
      var port = startPort + i;
      if (port > 65535) return Promise.resolve(null);
      var base = httpAt(host, port);
      return probePhone(base).then(function (res) {
        if (res.kind === 'phone') {
          return { httpBase: base, version: res.version, buildTime: res.buildTime };
        }
        // 死端口会立刻拒绝连接，加个小间隔让 10 个探测看起来是逐个进行的
        return delay(SCAN_STEP_INTERVAL).then(function () { return step(i + 1); });
      });
    }
    return step(0);
  }

  // 从 startPort 往上找第一个空闲端口（拉起时经 ?listen= 让软电话绑它）。
  // 找不到就回退起始端口（软电话自己会再 +1，bootstrap 的扫描能兜住）。
  function findFreePort(startPort, host) {
    host = host || '127.0.0.1';
    function step(i) {
      if (i >= PORT_PROBE_MAX) return Promise.resolve(startPort);
      var port = startPort + i;
      if (port > 65535) return Promise.resolve(startPort);
      return probePhone(httpAt(host, port)).then(function (res) {
        if (res.kind === 'free') return port;
        return delay(SCAN_STEP_INTERVAL).then(function () { return step(i + 1); });
      });
    }
    return step(0);
  }

  // 构造真正用于拉起的 sip:// 地址：
  //   1. 把 userinfo 里的账号/密码挪到查询参数（account=&password=）——浏览器会
  //      拦截 URL 里带 user:pass@ 的子资源（"embedded credentials are blocked"）；
  //   2. 追加 listen=<port>，让软电话绑到探测出来的空闲端口（端口确定，不必再扫）。
  // 账号/密码没有时对应参数省略。解析失败就原样返回（至少 listen 仍尽量追加）。
  function buildLaunchUri(sipUri, port) {
    var u;
    try {
      u = new URL(sipUri);
    } catch (e) {
      return port ? sipUri + (sipUri.indexOf('?') >= 0 ? '&' : '?') + 'listen=' + port : sipUri;
    }
    var user = u.username || '';
    var pass = u.password || '';
    var params = [];
    // 保留原查询参数（transport/instance/updatesite…），再补 account/password/listen
    u.searchParams.forEach(function (v, k) { params.push([k, v]); });
    function has(k) {
      return params.some(function (p) { return p[0] === k; });
    }
    if (user && !has('account')) params.push(['account', user]);
    if (pass && !has('password')) params.push(['password', pass]);
    if (port && !has('listen')) params.push(['listen', String(port)]);

    // 重建：scheme://host:port?query（丢弃 userinfo）
    var out = u.protocol + '//' + u.hostname + (u.port ? ':' + u.port : '');
    var qs = params.map(function (p) {
      return encodeURIComponent(p[0]) + '=' + encodeURIComponent(p[1]);
    }).join('&');
    return qs ? out + '?' + qs : out;
  }

  // 最近的 sip:// 拉起地址（供「用户手势」兜底时重试）。
  var lastLaunchUri = null;

  // 当前要用的拉起地址：connect 时从 options.sipUri 取，也可经 setLaunchUri 动态更新
  //（应用在拿到配置后告知；断线重连用它拉起软电话）。
  var currentSipUri = null;

  // 当前是否有用户手势激活（Chrome 115+ 提供 navigator.userActivation）。
  // 返回 true/false；浏览器不支持时返回 null（未知）。
  // 外部协议拉起要求 transient activation，isActive=false 时拉起必定被浏览器拒绝，
  // 据此可以**确定判断**是否会被拦截，而不是靠「等了多久还没起来」去猜。
  function hasUserActivation() {
    try {
      var ua = navigator && navigator.userActivation;
      if (ua && typeof ua.isActive === 'boolean') return ua.isActive;
    } catch (e) { /* 不支持则返回 null */ }
    return null;
  }

  // 日志里隐藏拉起地址中的密码（userinfo 与 ?password= 两处都算）：控制台日志
  // 常被截图/粘贴进问题单，别把密码一起带出去（软电话侧日志同样做了脱敏）。
  function maskUri(uri) {
    return String(uri)
      .replace(/(sips?:\/\/[^:/?#@]+:)[^@/?#]+@/i, '$1***@')
      .replace(/([?&]password=)[^&]*/i, '$1***');
  }

  // 经隐藏 iframe 触发 sip:// 协议拉起：iframe 立即移除，不影响页面。
  function triggerLaunch(uri) {
    var iframe = document.createElement('iframe');
    iframe.style.display = 'none';
    iframe.src = uri;
    document.body.appendChild(iframe);
    setTimeout(function () { if (iframe.parentNode) iframe.parentNode.removeChild(iframe); }, 1000);
  }

  // 经隐藏 iframe 触发 sip:// 协议拉起软电话。
  // 注意：Chrome 要求外部协议拉起必须由**用户手势**触发（约 5 秒激活窗口），
  // 定时器/断线回调里的自动拉起会被拒绝；且 iframe 子资源若 URL 里带
  // user:pass@host 形式的凭据，Chrome 也会直接拦截（控制台报 "Subresource requests
  // whose URLs contain embedded credentials are blocked"）。因此失败时要经
  // onLaunchBlocked 让应用提供一个按钮、由用户点击再拉起。
  function launchPhone(sipUri) {
    lastLaunchUri = sipUri;
    // 先打一条日志，方便判定到底有没有真的去拉起（被拦与否看浏览器后续告警）
    if (typeof console !== 'undefined') console.log('[bootstrap] 尝试拉起软电话：' + maskUri(sipUri));
    triggerLaunch(sipUri);
    return lastLaunchUri;
  }

  // launchFromGesture 在**用户点击事件处理函数里**调用，重新拉起最近一次的 sip:// 地址。
  // 浏览器会对没有用户手势的拉起直接拒绝，所以自动拉起失败后必须走这条路径。
  function launchFromGesture() {
    if (!lastLaunchUri) return false;
    triggerLaunch(lastLaunchUri);
    return true;
  }

  // 等待软电话起来（默认最多 15 秒；自动拉起路径用更短的超时以便及时提示用户）。
  // 软电话重启后仍绑它自己配置的端口（即 httpBase），所以只探这一个已知端口即可，
  // 不再扫 10 个端口。
  function waitForPhone(httpBase, maxAttempts) {
    var limit = maxAttempts || LAUNCH_WAIT_ATTEMPTS;
    var attempts = 0;
    function poll() {
      return probePhone(httpBase).then(function (res) {
        if (res.kind === 'phone') {
          return { httpBase: httpBase, version: res.version, buildTime: res.buildTime };
        }
        if (++attempts >= limit) throw new Error('等待软电话启动超时');
        return delay(LAUNCH_WAIT_INTERVAL).then(poll);
      });
    }
    return poll();
  }

  // 加载软电话的 SDK 脚本（缓存戳 = version|buildTime）。
  function loadScript(url) {
    return new Promise(function (resolve, reject) {
      var s = document.createElement('script');
      s.src = url;
      s.async = true;
      s.onload = function () { resolve(); };
      s.onerror = function () { reject(new Error('加载 SDK 失败：' + url)); };
      document.head.appendChild(s);
    });
  }

  function loadSDK(httpBase, fingerprint) {
    return loadScript(httpBase + '/sdk/sip-phone-sdk.js?v=' + encodeURIComponent(fingerprint));
  }

  // ---- 对外 API ----

  // 内部实现：确保软电话在 httpBase（或附近端口）运行，返回 { httpBase, version, buildTime }。
  // url 只用于确定扫描起始端口；拉起用 sipUri（可空，空则无法拉起只报错）。
  // onBlocked 在「拉起后超时仍未起来」时回调——reason 按当时的用户手势判定。
  function _ensurePhone(wsUrl, httpBase, sipUri, onBlocked) {
    var startPort = portOf(wsUrl);
    var host = hostOf(httpBase);
    // 1. 先直接试当前 httpBase（软电话可能已在该端口运行）
    return probePhone(httpBase).then(function (res) {
      if (res.kind === 'phone') {
        return { httpBase: httpBase, version: res.version, buildTime: res.buildTime };
      }
      // 2. 不可达：扫描端口，找已运行的软电话（同一台机器上的相邻端口）
      return scanPorts(startPort, host).then(function (found) {
        if (found) return found;
        // 3. 没找到软电话：**总是尝试拉起**（不管有没有用户手势——无手势时浏览器
        //    会静默拒绝，靠下面的超时判定）。
        //    先探测一个空闲端口经 ?listen= 告诉软电话绑它，拉起后只等这个已知端口，
        //    不再扫端口；账号/密码挪进查询参数，避开浏览器的 user:pass@ 拦截。
        if (!sipUri) throw new Error('软电话未运行且未提供 sipUri，无法拉起');
        return findFreePort(startPort, host).then(function (port) {
          var base = withPort(httpBase, port);
          var uri = buildLaunchUri(sipUri, port);
          lastLaunchUri = uri;
          launchPhone(uri);
          return waitForPhone(base, AUTO_LAUNCH_WAIT_ATTEMPTS).then(function (found) {
            return found;
          }, function (e) {
            var reason = hasUserActivation() === false ? 'no_user_gesture' : 'launch_failed';
            if (typeof onBlocked === 'function') onBlocked({ uri: uri, httpBase: base, reason: reason });
            throw e;
          });
        });
      });
    });
  }

  // 确保软电话运行，返回 { httpBase, version, buildTime }。
  function ensurePhone(options) {
    options = options || {};
    var wsUrl = options.url || 'ws://127.0.0.1:' + DEFAULT_HTTP_PORT + '/ws';
    var httpBase = (options.httpUrl || httpBaseFromWsUrl(wsUrl)).replace(/\/+$/, '');
    return _ensurePhone(wsUrl, httpBase, options.sipUri, options.onLaunchBlocked);
  }

  // 加载 SDK：确保软电话 → 从软电话加载 SDK → 返回 client 类 + 指纹。
  function load(options) {
    return ensurePhone(options).then(function (meta) {
      var fingerprint = meta.version + '|' + meta.buildTime;
      return loadSDK(meta.httpBase, fingerprint).then(function () {
        return {
          client: root.SipPhoneSDK,
          version: meta.version,
          buildTime: meta.buildTime,
          httpBase: meta.httpBase
        };
      });
    });
  }

  // 完整生命周期：确保软电话 → 加载 SDK → 建连；断线由 bootstrap 接管重连，
  // 连上后比对 version|buildTime 热更新 SDK。
  //
  // **返回一个稳定的门面（facade）**，而不是 SDK 实例：调用方拿到它以后**永远用它**
  // （`sdk.on(...)` / `sdk.dial(...)` / `sdk.connected` …），热更新换实例时门面内部自动
  // 指向新实例，业务代码无需改写，也**不会接触到 SDK 实例本身**。
  //   - 事件订阅登记在门面里，每换一个新实例自动重挂（调用方只订阅一次）；
  //   - `facade.ready` 是首次连接成功的 Promise（首次失败会 reject，但引导器随后仍会
  //     在后台探测/拉起，连上时照常触发 connected 事件）；
  //   - `facade.close()` / `destroy()` 停止整个引导（断开并**不再**重连/拉起）。
  // `onClient(facade, meta)` 可选：首次连接与每次热更新后回调，用于打日志等。
  function connect(options) {
    options = options || {};
    var onClient = options.onClient;
    if (options.sipUri) currentSipUri = options.sipUri;
    var wsUrl = options.url || 'ws://127.0.0.1:' + DEFAULT_HTTP_PORT + '/ws';
    var httpBase = (options.httpUrl || httpBaseFromWsUrl(wsUrl)).replace(/\/+$/, '');
    var currentWsUrl = wsUrl; // 软电话实际监听端口对应的 wsUrl（初始/拉起后都会更新）
    var fingerprint = null;
    var currentClient = null;
    var reloading = false;
    var reconnecting = false;
    var stopped = false;      // facade.close()/destroy() 之后不再重连/拉起
    var restartPending = false; // 应用正在主动重启：只做原端口重连，不扫描/不拉起
    var clientGen = 0;        // 客户端代数：区分「我们主动销毁旧实例」与「真实断线」
    var listeners = {};       // 事件订阅登记表（event -> [fn]），换实例时重挂
    // 视频订阅同样登记在门面上：换实例（热更新）后自动按原参数重新订阅，
    // 调用方不必自己做（同一实例的重连由 SDK 内部自己恢复）。
    var videoOn = false;
    var videoSubOpts = null;
    var videoSubInstance = null;
    var log = options.logger || (typeof console !== 'undefined' ? function (m) { console.log('[bootstrap] ' + m); } : function () {});

    // ---- 门面 ----
    var facade = null;
    var readyResolve, readyReject;
    var readySettled = false;
    var ready = new Promise(function (resolve, reject) {
      readyResolve = function (v) { readySettled = true; resolve(v); };
      readyReject = function (e) { readySettled = true; reject(e); };
    });
    // 没人 await 时不要把拒绝当成未处理的异常（错误仍可通过 ready 拿到）
    ready.catch(function () {});

    // 门面自己提供的成员：实例还没加载出来时它们也是可用的
    // （否则 `'ready' in sdk` 会误判成 false）。
    var FACADE_MEMBERS = {
      ready: 1, on: 1, off: 1, connect: 1, close: 1, destroy: 1,
      subscribeVideo: 1, unsubscribeVideo: 1, videoSubscribed: 1
    };
    // 赋给门面的属性（如 `sdk.unregisterOnDisconnect = true`）：先记下来，**每个**新实例
    // 建好后都套用一次——既支持"实例还没加载就赋值"，也让热更新换实例后设置不丢。
    var overrides = {};

    function addListener(ev, fn) {
      (listeners[ev] = listeners[ev] || []).push(fn);
      if (currentClient) currentClient.on(ev, fn);
    }

    function removeListener(ev, fn) {
      var arr = listeners[ev];
      if (arr) {
        var i = arr.indexOf(fn);
        if (i >= 0) arr.splice(i, 1);
      }
      if (currentClient) currentClient.off(ev, fn);
    }

    // 把登记过的事件挂到（新）实例上
    function attachListeners(inst) {
      Object.keys(listeners).forEach(function (ev) {
        listeners[ev].forEach(function (fn) { inst.on(ev, fn); });
      });
    }

    function destroyClient() {
      if (!currentClient) return;
      try {
        if (typeof currentClient.destroy === 'function') currentClient.destroy();
        else currentClient.close();
      } catch (e) { /* 忽略 */ }
      currentClient = null;
    }

    facade = new Proxy({}, {
      get: function (_, prop) {
        if (prop === 'ready') return ready;
        if (prop === 'on') {
          return function (ev, fn) {
            addListener(String(ev), fn);
            return facade;
          };
        }
        if (prop === 'off') {
          return function (ev, fn) {
            removeListener(String(ev), fn);
            return facade;
          };
        }
        if (prop === 'connect') {
          // 连接由引导器托管；保留这个方法是让引导器模式与直接用 SDK 的代码形状一致
          return function () {
            if (currentClient) return currentClient.connect();
            // 首次还没加载出来：等首次连接即可（ready 失败会照样 reject）
            return ready.then(function () {});
          };
        }
        if (prop === 'close' || prop === 'destroy') {
          return function () {
            stopped = true;
            destroyClient();
            // 还没连上就被关闭：把挂起的 ready 结束掉，别让 `await sdk.ready` 永远等下去
            if (!readySettled) readyReject(new Error('引导器已关闭'));
          };
        }
        if (prop === 'videoSubscribed') return videoOn;
        if (prop === 'subscribeVideo') {
          return function (opts) {
            var inst = currentClient;
            if (!inst) return Promise.reject(new Error('尚未连接软电话'));
            var p = inst.subscribeVideo(opts);
            // 成功后登记参数：热更新换实例时据此自动重订
            p.then(function () {
              videoOn = true;
              videoSubOpts = opts;
              videoSubInstance = inst;
            }, function () { /* 失败不登记，等调用方重试 */ });
            return p;
          };
        }
        if (prop === 'unsubscribeVideo') {
          return function () {
            videoOn = false;
            videoSubOpts = null;
            videoSubInstance = null;
            return currentClient ? currentClient.unsubscribeVideo() : Promise.resolve();
          };
        }
        var inst = currentClient;
        if (!inst) return undefined;
        var v = inst[prop];
        return typeof v === 'function' ? v.bind(inst) : v;
      },
      set: function (_, prop, value) {
        if (typeof prop === 'symbol') return false;
        overrides[prop] = value; // 记下来：换实例后还要再套用一次
        if (currentClient) currentClient[prop] = value;
        return true;
      },
      has: function (_, prop) {
        if (FACADE_MEMBERS[prop]) return true;
        if (currentClient) return prop in currentClient;
        return prop in overrides;
      },
    });

    function clientOptions() {
      var o = {};
      for (var k in options) {
        // bootstrap 专属键不透传给 SDK；sipUri 也由 bootstrap 负责拉起，不交给 SDK
        if (k === 'url' || k === 'httpUrl' || k === 'sipUri' || k === 'onClient' || k === 'onLaunchBlocked' || k === 'logger') continue;
        o[k] = options[k];
      }
      o.url = currentWsUrl;
      // 断线重连交给 bootstrap（探测/扫描/拉起），SDK 不再自己重试——否则会一直
      // 对着已退出的软电话死重连，bootstrap 的拉起反而插不上手。
      o.autoReconnect = false;
      return o;
    }

    function makeClient() {
      var gen = ++clientGen; // 先换代号：旧实例即便在销毁过程中还有 disconnected 也一律忽略
      destroyClient(); // 旧实例：destroy 会清事件/未请求/心跳/视频渲染器并停 track
      var Client = root.SipPhoneSDK;
      if (!Client) throw new Error('SDK 未加载：sip-phone-sdk.js 未注入 SipPhoneSDK');
      currentClient = new Client(clientOptions());
      // 门面上设置过的属性补到新实例（首次加载与每次热更新都套用，设置不丢）。
      // `url` 例外：它由引导器按实际端口维护，重放会让新实例连到旧端口。
      Object.keys(overrides).forEach(function (k) {
        if (k === 'url') return;
        currentClient[k] = overrides[k];
      });
      // 先把调用方登记过的订阅挂上（换实例后每个新实例都要重挂）
      attachListeners(currentClient);
      // 每次（重）连上后检查 SDK 是否有更新；断开后把控制交回 bootstrap
      currentClient.on('connected', function () {
        if (gen !== clientGen) return;
        restartPending = false; // 连上了说明重启已完成
        if (!stopped) maybeReload();
      });
      // 热更新换实例后恢复视频订阅（同一实例的重连由 SDK 自己恢复，见 restoreVideo）
      currentClient.on('connected', function () { if (gen === clientGen && !stopped) restoreVideo(); });
      currentClient.on('disconnected', function (ev) {
        if (gen === clientGen && !stopped) onDisconnected(ev);
      });
      var p = currentClient.connect();
      if (p && typeof p.catch === 'function') p.catch(function () { /* 失败由 disconnected 分支接管 */ });
      return currentClient;
    }

    // 加载 SDK 脚本并创建实例（首次连接与「首次失败后的后台重试」共用）。
    // 返回 true 表示实例已建好；false 表示期间被 close() 了。
    function loadAndCreate(meta) {
      if (stopped) return Promise.resolve(false); // 已 close()：连脚本都不必再加载
      httpBase = meta.httpBase;
      currentWsUrl = wsUrlWithPort(wsUrl, portOf(httpBase));
      fingerprint = meta.version + '|' + meta.buildTime;
      return loadSDK(httpBase, fingerprint).then(function () {
        if (stopped) return false;
        log('已加载 SDK v' + meta.version + ' build=' + (meta.buildTime || '—') + '（' + httpBase + '）');
        makeClient();
        if (typeof onClient === 'function') onClient(facade, { version: meta.version, buildTime: meta.buildTime });
        return true;
      });
    }

    // 视频订阅是连接级的：换了实例（热更新）后旧实例的订阅与渲染器都没了，
    // 这里按登记过的参数在新实例上重新订阅；同一实例的断线重连由 SDK 内部恢复。
    function restoreVideo() {
      if (!videoOn || !videoSubOpts) return;
      if (videoSubInstance === currentClient) return;
      var inst = currentClient;
      inst.subscribeVideo(videoSubOpts).then(function () {
        videoSubInstance = inst;
        log('已恢复视频订阅');
      }, function (e) {
        log('恢复视频订阅失败：' + (e && e.message ? e.message : e));
      });
    }

    function maybeReload() {
      if (reloading || stopped) return;
      reloading = true;
      probePhone(httpBase).then(function (res) {
        if (res.kind !== 'phone') { reloading = false; return; }
        var fp = res.version + '|' + res.buildTime;
        if (fp === fingerprint) {
          log('已连接软电话 v' + res.version + ' build=' + (res.buildTime || '—') + '，SDK 无变化');
          reloading = false;
          return;
        }
        log('检测到新构建（' + fingerprint + ' → ' + fp + '），重新加载 SDK');
        return loadSDK(httpBase, fp).then(function () {
          if (stopped) { reloading = false; return; } // 加载期间被 close()：不再建实例
          fingerprint = fp;
          makeClient();
          log('SDK 已热更新：v' + res.version + ' build=' + (res.buildTime || '—'));
          if (typeof onClient === 'function') onClient(facade, { version: res.version, buildTime: res.buildTime });
          reloading = false;
        });
      }).catch(function () { reloading = false; });
    }

    // 断线分流：关闭码是「重启」时只做原端口重连（应用自己会按同样的端口起来，
    // 不需要扫描/拉起，也不会被浏览器的用户手势限制拦住）；其余按常规流程处理。
    function onDisconnected(ev) {
      if (ev && ev.code === RESTART_CLOSE_CODE) {
        restartPending = true;
        log('应用正在重启（' + (ev.reason || 'restart') + '），只做原端口重连');
        reconnectSamePort();
        return;
      }
      if (restartPending) { reconnectSamePort(); return; } // 重启等待期间的连接失败，继续等
      scheduleReconnect();
    }

    // 重启期间：只在**原端口**上轮询 /api/status（不扫描、不拉起），起来后重连；
    // 超过 RESTART_WAIT_MAX 还没回来就回落到常规流程（可能换了端口/需要拉起）。
    function reconnectSamePort() {
      if (reconnecting || stopped) return;
      reconnecting = true;
      var delayMs = RESTART_RECONNECT_BASE;
      var deadline = Date.now() + RESTART_WAIT_MAX;
      var occupied = 0; // 连续几次「端口有服务但不是软电话」：多半已被挤到下一个端口

      function attempt() {
        if (stopped) { reconnecting = false; return; }
        return probePhone(httpBase).then(function (res) {
          if (res.kind === 'phone') {
            currentWsUrl = wsUrlWithPort(wsUrl, portOf(httpBase));
            if (currentClient) {
              currentClient.url = currentWsUrl;
              var p = currentClient.connect();
              if (p && typeof p.catch === 'function') p.catch(function () { /* 交给 disconnected */ });
            }
            reconnecting = false;
            return;
          }
          // 原端口被别的服务占了：重启后的软电话已经绑到 +1 端口，别死等
          if (res.kind === 'occupied' && ++occupied >= 3) {
            restartPending = false;
            reconnecting = false;
            log('原端口已被其他服务占用，回到常规流程（探测/扫描/拉起）');
            scheduleReconnect();
            return;
          }
          if (res.kind !== 'occupied') occupied = 0;
          if (Date.now() > deadline) {
            restartPending = false;
            reconnecting = false;
            log('等待重启超时，回到常规流程（探测/扫描/拉起）');
            scheduleReconnect();
            return;
          }
          var wait = delayMs;
          delayMs = Math.min(delayMs * 2, RESTART_RECONNECT_MAX);
          return delay(wait).then(attempt);
        });
      }

      attempt();
    }

    // 断线后把控制交回 bootstrap：循环「探测 / 扫描 / 拉起」，失败按退避重试。
    // 每轮走 _ensurePhone（探测 → 扫描 → 拉起 → 等待），软电话回来后重连；
    // 一轮失败（拉起超时）则退避后重试，不再固定间隔反复刷。
    function scheduleReconnect() {
      if (reconnecting || stopped) return;
      reconnecting = true;
      var delayMs = RECONNECT_BASE_INTERVAL;

      function attempt() {
        if (stopped) { reconnecting = false; return Promise.resolve(); }
        // 扫描起点用**当前**的 ws 地址（端口可能已被上一次拉起改过），httpBase 也一样
        return _ensurePhone(currentWsUrl, httpBase, currentSipUri, options.onLaunchBlocked).then(function (meta) {
          httpBase = meta.httpBase;
          currentWsUrl = wsUrlWithPort(wsUrl, portOf(httpBase));
          log('软电话已就绪（' + httpBase + '），重连并检查 SDK 构建');
          if (!currentClient) {
            // 首次加载就没成功（SDK 脚本还没注入）：先把加载与建实例补上，
            // 新实例的 connected 事件会继续走指纹比对/视频恢复。
            return loadAndCreate(meta).then(function () { reconnecting = false; });
          }
          currentClient.url = currentWsUrl;
          var p = currentClient.connect();
          if (p && typeof p.catch === 'function') p.catch(function () { /* 交给 disconnected */ });
          reconnecting = false;
        }).catch(function (e) {
          log('断线重连未成功（' + (e && e.message ? e.message : e) + '），' + delayMs + 'ms 后重试');
          var wait = delayMs;
          delayMs = Math.min(delayMs * 2, RECONNECT_MAX_INTERVAL);
          return delay(wait).then(attempt);
        });
      }

      attempt();
    }

    _ensurePhone(wsUrl, httpBase, currentSipUri, options.onLaunchBlocked).then(function (meta) {
      if (stopped) return; // 加载期间已被 close()：不再建实例
      return loadAndCreate(meta).then(function (created) {
        if (created) readyResolve(facade);
      });
    }).catch(function (e) {
      // 首次连接失败常见于「软电话没在跑 + 自动拉起被浏览器拦（无用户手势）」。
      // ready 只 reject 一次（应用据此出「重新拉起」按钮），但引导器**不放弃**：
      // 转入后台重连循环——用户手动起软电话、或点按钮拉起后都能自动连上。
      log('初始连接失败：' + (e && e.message ? e.message : e) + '，转入后台重试（探测/扫描/拉起）');
      readyReject(e);
      if (!stopped) scheduleReconnect();
    });

    return facade;
  }

  // 动态设置/更新拉起地址：应用可能要到连上后（拿到配置）才知道 sip:// 地址，
  // 之后断线重连就按它拉起软电话。返回当前生效的拉起地址（null 表示未设置）。
  function setLaunchUri(uri) {
    currentSipUri = uri || null;
    return currentSipUri;
  }

  return {
    ensurePhone: ensurePhone,
    load: load,
    connect: connect,
    setLaunchUri: setLaunchUri,
    // 必须在**用户点击事件处理函数里**调用：浏览器要求外部协议拉起需用户手势，
    // 自动拉起被拒绝后由应用出按钮、点击时调用本方法重新拉起最近一次的 sip:// 地址。
    launchFromGesture: launchFromGesture
  };
});
