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

  /**
   * 坐席状态（用户口径）：离席优先级最高，其次整理中（isBusy && isWork），再置忙，
   * 否则空闲。返回 {text, action, cls}：action 为点击徽章时发送的 CTI 动作
   *（空闲→setBusy，离席→resume，置忙/整理→setIdle）。
   */
  function agentStatusOf(ag) {
    if (ag.leave) return { text: '离席', action: 'resume', cls: 'warn' };
    if (ag.busy && ag.isWork) return { text: '整理中', action: 'setIdle', cls: 'info' };
    if (ag.busy) return { text: '置忙', action: 'setIdle', cls: 'warn' };
    return { text: '空闲', action: 'setBusy', cls: 'ok' };
  }

  /**
   * 顶栏坐席徽章（REQ-ACC-028 / REQ-ACC-034）：坐席模式下显示模式 + 空闲/置忙/
   * 整理中/离席，点击切换（与桌面顶栏的座席按钮一致）；非坐席模式隐藏。
   * 在线座席列表只在拨号对话框的「在线座席」页签里（导航栏不再单独入口）。
   */
  function refreshAgent() {
    const ag = store.state.agent || { online: false };
    const badge = $('agentBadge');
    if (!badge) return;
    // 未连接话机时不显示坐席状态：此时留的是断线前的快照，点了也发不出去。
    if (!store.isConnected() || !ag.online) {
      badge.hidden = true;
      return;
    }
    const modeText = { normal: '普通', dial_out: '外呼', monitor: '班长' }[ag.mode] || '';
    const st = agentStatusOf(ag);
    badge.textContent = '坐席' + (modeText ? '·' + modeText : '') + '·' + st.text;
    badge.className = 'badge ' + st.cls;
    const actText = st.action === 'resume' ? '复席' : (st.action === 'setIdle' ? '置闲' : '置忙');
    badge.title = '坐席' + (ag.displayName ? '：' + ag.displayName : '') +
      '（' + st.text + '，点击' + actText + '）';
    badge.hidden = false;
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
    el.title = phase === 'available' ? '点击定位到更新卡片并开始下载'
      : phase === 'ready' ? '点击定位到更新卡片并安装'
        : '点击查看更新进度';
  }

  /** 更新卡片的短暂高亮：即使卡片已经在视野里，点击徽章也有可见反馈。 */
  function flashCard(card) {
    if (!card) return;
    card.classList.remove('flash');
    void card.offsetWidth; // 重新触发动画
    card.classList.add('flash');
    setTimeout(function () { card.classList.remove('flash'); }, 2600);
  }

  function init() {
    $('wsUrl').value = location.origin.replace(/^http/, 'ws') + '/ws';
    $('verShort').textContent = '控制台';
    document.querySelectorAll('.tabs a').forEach(function (a) {
      a.addEventListener('click', function () { /* hashchange 负责切换 */ });
    });
    window.addEventListener('hashchange', route);
    // 点徽章：切到通话页 → 滚到更新卡片 → 高亮；「发现新版本」时直接开始下载
    //（这次点击就是明确的用户意图，下载仍可在卡片上取消；安装/重启仍需再点一次）。
    $('updBadge').addEventListener('click', function (ev) {
      ev.preventDefault();
      if (location.hash !== '#/call') location.hash = '#/call';
      setTimeout(function () {
        const card = $('cardUpdate');
        if (!card || card.hidden) {
          App.ui.toast('暂未获取到更新详情，请稍后重试');
          return;
        }
        if (card.scrollIntoView) card.scrollIntoView({ block: 'center', behavior: 'smooth' });
        flashCard(card);
        const st = store.state.update;
        if (st && st.phase === 'available') {
          const ver = (st.info && st.info.version) || '';
          store.sdk.startUpdate().then(function () {
            App.ui.toast('开始下载更新' + (ver ? ' v' + ver : ''), 'ok');
          }).catch(function (e) {
            if (!App.util.isOfflineErr(e)) App.ui.errToast('开始下载失败', e);
          });
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
    store.on('state', refreshAgent);         // 坐席状态也在快照里
    store.on('agent', refreshAgent);
    store.on('update', refreshUpdateBadge);  // 之后按 system.update 事件刷新
    store.on('register', refreshTop);
    store.on('connected', refreshTop);
    store.on('disconnected', refreshTop);
    store.on('disconnected', refreshAgent);  // 断线后收起坐席状态徽章

    // 坐席徽章点击 = 置忙/置闲（与桌面顶栏的座席按钮同一动作；
    // 生效状态由 cti.agent 事件回显，不在本地乐观更新）
    $('agentBadge').style.cursor = 'pointer';
    $('agentBadge').addEventListener('click', function () {
      if (!store.isConnected()) return;
      const ag = store.state.agent || {};
      if (!ag.online) return;
      const st = agentStatusOf(ag);
      store.sdk.ctiAction(st.action).then(function () {
        App.ui.toast(st.action === 'setBusy' ? '已置忙'
          : st.action === 'resume' ? '已复席' : '已置闲', 'ok');
        refreshAgent();
      }).catch(function (e) {
        if (!App.util.isOfflineErr(e)) { App.ui.toast(App.util.errText(e), 'err'); }
      });
    });

    Object.keys(views).forEach(function (k) { if (views[k].mount) views[k].mount(); });
    route();
    refreshTop();
    refreshAgent();
    refreshUpdateBadge();
    App.dialpad.bind();
    App.callbar.bind();
    App.callbar.render(null);

    // 连上（或断线重连）后重新拉一次当前视图的数据：mount/首次 onShow 可能在
    // WS 还没连上时就跑了，那时任何 sdk 请求都会以"尚未连接软电话"失败。
    store.on('connected', function () {
      show(current);
      refreshTop();
    });

    // 连接由 store 里的引导器（SipPhoneBootstrap）自动发起，无需这里再手动 connect
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})(window.SipApp);
