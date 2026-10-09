// 诊断视图（#/diag）：运行信息 + 事件日志 + sip:// 拉起链接生成器。
//
// 这一页原来塞在配置页底部（日志还折叠在「诊断」里），看日志要翻到页面最下面、
// 生成拉起链接还得先展开折叠加滚动——都不方便，索性独立成一页（用户要求）。
window.SipApp = window.SipApp || {};
window.SipApp.views = window.SipApp.views || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const sdk = store.sdk;

  const MAX_LOG = 200; // 日志最多保留的条数（长时间开着也不会把页面拖慢）

  // 当前配置里的更新站点（REQ-UPD-022）：生成拉起链接时自动附加 updatesite 参数。
  // fillURIFromConfig 里取配置快照缓存，设置页（或其它网页）保存新站点后
  // 经 store 的 updateConfig 事件即时刷新。
  let lastUpdateSite = '';

  const TRANSPORTS = [
    { value: 'udp', label: 'udp（默认）' },
    { value: 'tcp', label: 'tcp' },
    { value: 'tls', label: 'tls（信令加密，SIPS）' },
  ];

  /** 日志区追加一行（kind 用于将来上色，现在统一一种样式）。 */
  function addLog(tag, text) {
    const box = $('log');
    if (!box) return;
    const line = document.createElement('div');
    line.textContent = '[' + new Date().toLocaleTimeString() + '] ' + tag + ': ' + text;
    box.appendChild(line);
    while (box.childElementCount > MAX_LOG) box.removeChild(box.firstChild);
    box.scrollTop = box.scrollHeight;
  }

  /** 运行信息行：版本/平台/本机号码/实际监听端口。 */
  function refreshInfo() {
    const st = store.state;
    const parts = [];
    if (st.version) parts.push('版本 ' + st.version + ' · ' + (st.os || '?') + '/' + (st.arch || '?'));
    parts.push('本机号码 ' + (st.register.account || '未配置'));
    parts.push('WS ' + location.port);
    if (st.register.reason) parts.push('注册原因 ' + st.register.reason);
    $('diagInfo').textContent = parts.join(' · ');
  }

  App.views.diag = {
    mount: function () {
      const $t = $('uriTransport');
      App.dom.clear($t);
      TRANSPORTS.forEach(function (t) {
        $t.appendChild(App.dom.el('option', { value: t.value }, t.label));
      });

      // 事件日志：默认不回显视频帧（30fps 会把日志刷爆）
      sdk.on('*', function (m) {
        if (m.event === 'video.frame' && !$('chkLogFrames').checked) return;
        addLog(m.event, JSON.stringify(m.data));
      });
      // 连接状态也记一行，排查"页面到底连上没有"最直接
      store.on('connected', function () { addLog('连接', '已连接软电话'); });
      store.on('disconnected', function () { addLog('连接', '连接已断开（SDK 会自动重连）'); });
      $('btnClearLog').addEventListener('click', function () { $('log').textContent = ''; });

      // sip:// 拉起链接生成器（与原测试页同一套口径）：
      //   - 端口缺省按传输取（TLS → 5061、其余 5060），写了端口就用写的；
      //   - TLS 用 sips:// 前缀，tcp 用 ?transport=tcp；
      //   - 槽位 > 0 加 ?instance=N（该实例在跑就转发参数，没跑就用它的独立数据目录启动）。
      $('btnGenUri').addEventListener('click', generateURI);

      // 连上（或重连）后按**当前配置**自动填好各项并生成一条链接：
      // 程序关掉后直接点这一条就能用同样的参数把它拉起来（用户要求）。
      store.on('connected', fillURIFromConfig);
      // 更新配置变化（如设置页保存了新站点）→ 刷新缓存的更新站点并重新生成链接
      store.on('updateConfig', fillURIFromConfig);
      fillURIFromConfig();
    },
    onShow: function () { refreshInfo(); fillURIFromConfig(); },
    onHide: function () { },
  };

  /**
   * 按当前配置回填拉起参数并生成链接。
   *
   * 密码**拿不到**（配置快照只回 passwordSet，绝不下发明文），所以密码框留空、
   * 生成的链接里不带密码；要用带密码的链接时手填一次再点「生成拉起链接」。
   * 实例槽位取 store.state.instance（本进程的槽位）——不带上它，链接拉起的
   * 会是主实例而不是当前这个沙箱实例。
   */
  function fillURIFromConfig() {
    if (!store.isConnected()) return;
    sdk.getConfig().then(function (snap) {
      const a = snap.account || {};
      if (a.server) {
        $('uriServer').value = a.server + (a.port ? ':' + a.port : '');
      }
      if (a.account) $('uriAccount').value = a.account;
      if (a.transport) $('uriTransport').value = a.transport;
      $('uriInstance').value = store.state.instance || 0;
      // 更新站点（REQ-UPD-022）：配置里配了就自动附加到生成链接
      lastUpdateSite = String((snap.update && snap.update.url) || '').trim();
      if (a.passwordSet) {
        addLog('拉起链接', '已按当前配置生成（密码不下发，需要带密码时请手填一次再点「生成拉起链接」）');
      }
      generateURI();
    }).catch(function (e) { App.ui.errToast('读取配置失败', e); });
  }

  /**
   * 用当前的输入框内容生成链接并显示。
   * 密码为空时链接里**不带** `:密码@`（`sip://账号@服务器`，解析出来就是空密码）。
   */
  function generateURI() {
    const server = $('uriServer').value.trim();
    const account = $('uriAccount').value.trim();
    const password = $('uriPassword').value;
    const transport = $('uriTransport').value;
    if (!server || !account) { App.ui.toast('请输入服务器与账号', 'err'); return; }
    let host = server;
    if (host.indexOf(':') < 0) host += (transport === 'tls' ? ':5061' : ':5060');
    let uri = (transport === 'tls' ? 'sips://' : 'sip://') + account +
      (password ? ':' + encodeURIComponent(password) : '') + '@' + host;
    const params = [];
    if (transport === 'tcp') params.push('transport=tcp');
    const slot = parseInt($('uriInstance').value, 10);
    if (slot > 0) {
      if (slot > 99) { App.ui.toast('实例槽位范围 0–99', 'err'); return; }
      params.push('instance=' + slot);
    }
    // 更新站点按当前配置自动附加（REQ-UPD-022）；百分号编码保证 URL 里的
    // :/&? 不破坏参数结构（服务端 Query().Get 自动解码）。
    if (lastUpdateSite) params.push('updatesite=' + encodeURIComponent(lastUpdateSite));
    if (params.length) uri += '?' + params.join('&');

    const line = $('uriLine');
    App.dom.clear(line);
    line.appendChild(App.dom.el('div', null,
      '点击下面的链接（浏览器会提示打开软电话），也可以复制发到别的机器：'));
    line.appendChild(App.dom.el('a', { href: uri }, uri));
    line.style.display = '';
    addLog('生成拉起链接', uri.replace(/:[^:@/]*@/, ':***@')); // 日志里不落明文密码
  }
})(window.SipApp);
