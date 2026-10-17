// 应用启动：hash 路由（#/call、#/history、#/contacts、#/settings）+ 顶栏状态 +
// 各视图生命周期 + WS 连接。
//
// 为什么是"单页壳 + hash 路由"而不是四个独立页面：视频订阅是**连接级**的
//（连接断开即退订），整页跳转会把订阅丢掉、软电话侧的视频窗口状态跟着抖；
// 而且无 UI 版没有桌面通话面板，切页时必须还能接听/挂断——所以用常驻通话条
//（callbar.js）+ 视图切换，一条 WS 连接贯穿始终。
window.SipApp = window.SipApp || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const views = {
    call: App.views.call,
    history: App.views.history,
    contacts: App.views.contacts,
    settings: App.views.settings,
    diag: App.views.diag,
  };

  let current = '';


  function show(name) {
    if (!views[name]) name = 'call';
    if (current && views[current] && views[current].onHide) views[current].onHide();
    Object.keys(views).forEach(function (k) {
      const section = $('view-' + k);
      if (section) section.hidden = k !== name;
    });
    document.querySelectorAll('.tabs a').forEach(function (a) {
      a.classList.toggle('active', a.getAttribute('data-view') === name);
    });
    const v = views[name];
    if (v && v.onShow) v.onShow();
    current = name;
  }

  function route() {
    const hash = (location.hash || '').replace(/^#\/?/, '');
    show(hash || 'call');
  }

  function refreshTop() {
    const st = store.state;
    const conn = $('connBadge');
    conn.textContent = st.connected ? '已连接' : '未连接';
    conn.className = 'badge ' + (st.connected ? 'ok' : 'err');

    const reg = $('regBadge');
    const map = {
      registered: ['已注册', 'ok'],
      registering: ['注册中', 'warn'],
      failed: ['注册失败', 'err'],
      unregistered: ['未注册', ''],
    };
    const info = map[st.register.state] || ['未注册', ''];
    reg.textContent = info[0];
    reg.className = 'badge ' + info[1];
    // 点击提示（REQ-ACC-024）：注册原因在前，动作提示在后（徽章可点击）
    const actHint = st.register.state === 'registered' ? '点击退出账号'
      : st.register.state === 'registering' ? '点击停止登录' : '点击登录账号';
    reg.title = (st.register.reason ? st.register.reason + ' · ' : '') + actHint;

    const acct = st.register.account || '—';
    $('localAccount').textContent = acct;
    $('localAccount2').value = acct;

    // 版本号由状态快照带来（连上才知道）：顶栏与标签页标题都标上，
    // 排查"页面是不是连上了/跑的是不是新版本"时一眼可见
    if (st.version) {
      $('verShort').textContent = '控制台 ' + st.version;
      document.title = 'MTI 软电话控制台 · ' + st.version;
    }

    const line = $('stateLine');
    line.textContent = st.connected
      ? (st.register.reason ? '注册状态：' + info[0] + '（' + st.register.reason + '）' : '')
      : '未连接到软电话（确认软电话正在运行；无 UI 版请点托盘图标打开本页）';
  }

  /**
   * 顶栏的全局更新徽章：任何页面都能一眼看到"有更新"。
   * 数据与「通话」页那张更新卡片同源（store.state.update / system.update 事件）。
   */
  function refreshUpdateBadge() {
    const el = $('updBadge');
    if (!el) return;
    const st = store.state.update;
    const phase = st && st.phase;
    const ver = (st && st.info && st.info.version) || '';
    let text = '';
    let cls = 'badge warn';
    switch (phase) {
      case 'available':
        text = '发现新版本' + (ver ? ' v' + ver : '');
        break;
      case 'downloading':
        text = '更新下载中 ' + Math.round(st.progress || 0) + '%';
        break;
      case 'ready':
        text = '更新待安装' + (ver ? ' v' + ver : '');
        cls = 'badge ok';
        break;
      case 'failed':
        text = '更新失败';
        cls = 'badge err';
        break;
      default:
        el.hidden = true;
        return;
    }
    el.textContent = text;
    el.className = cls;
    el.hidden = false;
  }

  function init() {
    $('wsUrl').value = location.origin.replace(/^http/, 'ws') + '/ws';
    $('verShort').textContent = '控制台';
    document.querySelectorAll('.tabs a').forEach(function (a) {
      a.addEventListener('click', function () { /* hashchange 负责切换 */ });
    });
    window.addEventListener('hashchange', route);
    // 点徽章：href 负责切到 #/call，再把那张更新卡片滚到视野中间
    $('updBadge').addEventListener('click', function () {
      setTimeout(function () {
        const card = $('cardUpdate');
        if (card && !card.hidden && card.scrollIntoView) {
          card.scrollIntoView({ block: 'center', behavior: 'smooth' });
        }
      }, 80);
    });
    // 顶栏注册状态徽章可点击（REQ-ACC-024，与桌面托盘菜单的
    // 「登录账号 / 退出账号」一致）：已注册/注册中 → 退出账号（发 expires=0、
    // 停注册周期、启用账户=false）；未注册/注册失败 → 登录账号（启用并注册）。
    // 结果经 register.state 事件回显（徽章/托盘/桌面同步）。
    $('regBadge').style.cursor = 'pointer';
    $('regBadge').addEventListener('click', function () {
      if (!store.isConnected()) { return; } // 没连上时不做任何事（SDK 会重连）
      const st = store.state.register.state;
      const unregistering = (st === 'registered' || st === 'registering');
      const p = unregistering ? store.sdk.unregister() : store.sdk.register();
      p.then(function () {
        App.ui.toast(unregistering ? '已退出账号' : '正在登录', 'ok');
      }).catch(function (e) {
        if (!App.util.isOfflineErr(e)) { App.ui.toast(e.message || '操作失败', 'err'); }
      });
    });
    store.on('state', refreshTop);
    store.on('state', refreshUpdateBadge);   // 连上时状态快照里就带着更新状态
    store.on('update', refreshUpdateBadge);  // 之后按 system.update 事件刷新
    store.on('register', refreshTop);
    store.on('connected', refreshTop);
    store.on('disconnected', refreshTop);

    Object.keys(views).forEach(function (k) { if (views[k].mount) views[k].mount(); });
    route();
    refreshTop();
    refreshUpdateBadge();
    App.callbar.bind();
    App.callbar.render(null);

    // 连上（或断线重连）后重新拉一次当前视图的数据：mount/首次 onShow 可能在
    // WS 还没连上时就跑了，那时任何 sdk 请求都会以"尚未连接软电话"失败。
    store.on('connected', function () {
      show(current);
      refreshTop();
    });

    // 连接软电话（无 token 鉴权，仅回环）
    store.connect();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})(window.SipApp);
