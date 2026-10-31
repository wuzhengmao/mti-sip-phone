// 应用共享状态：SDK 单例 + 连接/注册/通话状态 + 事件扇出。
//
// 为什么要集中一层：控制台是"单页壳 + 多视图"（见 app.js），一条 WS 连接、
// 一个 SDK 实例贯穿所有视图。各视图只订阅自己关心的事件，不各自 new SDK——
// 视频订阅是**连接级**的（断开即退订），多条连接会让画面在切视图时闪断。
//
// SDK 经**引导器**（SipPhoneBootstrap，见 docs/sdk-bootstrap.md）从软电话动态加载。
// 引导器返回一个**稳定的门面**（不是 SDK 实例）：界面始终用 `store.sdk`，热更新换实例时
// 由门面内部切换并自动重挂事件订阅，业务代码不必感知热更新。
window.SipApp = window.SipApp || {};

(function (App) {
  'use strict';

  const listeners = {};      // eventName -> [fn]
  const state = {
    connected: false,
    register: { state: 'unregistered', account: '', reason: '' },
    call: null,              // 当前通话（core.Call 快照，含 video/audio 字段）
    // 通话面板快照（call.panel，REQ-WS-023）：页面据此直接渲染面板，
    // {callPanel,conferencePanel}；无通话时两个面板为 null（坐席状态走 cti.agent）。
    panel: null,
    version: '',
    os: '',
    arch: '',
    noUI: false, // 仅托盘无 UI 版（core.State.noUI）：网页据此自动订阅视频、隐藏窗口相关项
    instance: 0, // 实例槽位（0 = 主实例）：诊断页拼 sip:// 拉起链接要用
    updatePrimarySite: '', // 官方更新站点（system.state 下发，配置页「使用主站点」按钮用，REQ-UPD-023）
    recentDial: [],
    update: null,            // 更新状态（system.update / system.state.update）
    autoAnswer: false,
    contactCount: 0,
    // 坐席模式（REQ-ACC-028 / REQ-ACC-034）：system.state 快照 + cti.agent 事件维护
    agent: { online: false, displayName: '', mode: 'none', busy: false, isWork: false, leave: false, phoneStatus: '', agentStatus: 'unknown' },
  };

  // SDK 门面：引导器 connect() 返回的**稳定对象**（不是 SDK 实例）。事件只订阅一次，
  // 热更新换实例时门面内部自动切到新实例、并自动重挂这些订阅——界面代码零改动，
  // 也不会接触到 SDK 实例本身。
  const sdk = SipPhoneBootstrap.connect({
    url: 'ws://' + location.host + '/ws',
    onClient: function (client, meta) {
      console.log('[控制台] SDK 已就绪 v' + meta.version + ' build=' + (meta.buildTime || '—'));
    },
  });

  /** on 订阅事件（'*' 收全部业务事件）。 */
  function on(name, fn) {
    (listeners[name] = listeners[name] || []).push(fn);
    return function () { off(name, fn); };
  }

  function off(name, fn) {
    const arr = listeners[name];
    if (!arr) return;
    const i = arr.indexOf(fn);
    if (i >= 0) arr.splice(i, 1);
  }

  function emit(name, data) {
    (listeners[name] || []).forEach(function (fn) {
      try { fn(data); } catch (e) { console.error('事件处理失败', name, e); }
    });
    (listeners['*'] || []).forEach(function (fn) {
      try { fn({ event: name, data: data }); } catch (e) { console.error('事件处理失败', name, e); }
    });
  }

  /** 通话是否处于"已接通"（决定计时与媒体控件显隐）。 */
  function callActive() { return !!(state.call && state.call.state === 'established'); }

  /** 当前通话 id（没有则为空串）。 */
  function callID() { return state.call ? state.call.id : ''; }

  /** 设置当前通话并通知界面。**只认进行中的状态**（ringing/established）：
   *  软电话的 calls 快照保留已结束的通话（terminated），重连时的 system.state
   *  或迟到的 call.audio/call.video 快照若照单全收，通话卡片会停在"呼叫中"
   *  （用户实测：话机已空闲、心跳 free，网页还挂着卡片）。 */
  function setCall(call) {
    const active = !!call && (call.state === 'ringing' || call.state === 'established');
    state.call = active ? call : null;
    emit('call', state.call);
  }

  // 从配置派生出 sip:// 拉起地址交给引导器：软电话退出后断线重连时用它重新拉起。
  // 密码拿不到（配置快照只回 passwordSet），故不带密码——软电话自身配置里已有密码，
  // 拉起只会重新启用账户。instance 取本进程槽位（0 = 主实例）。
  function syncLaunchUri(instance) {
    sdk.getConfig().then(function (cfg) {
      const a = cfg && cfg.account;
      if (!a || !a.server || !a.account) return;
      const host = a.server + (a.port ? ':' + a.port : '');
      const uri = (a.transport === 'tls' ? 'sips://' : 'sip://') + a.account + '@' + host;
      const params = [];
      if (a.transport === 'tcp') params.push('transport=tcp');
      if (instance > 0) params.push('instance=' + instance);
      SipPhoneBootstrap.setLaunchUri(uri + (params.length ? '?' + params.join('&') : ''));
    }).catch(function () {
      // 拿不到配置就跳过：断线重连仍能探测，只是无法自动拉起
    });
  }

  // ---- SDK 事件 → 状态更新 + 扇出 ----
  // 只订阅一次：门面（见文件头）保证 SDK 热更新换实例后把这些订阅自动重挂到新实例上。
  function wire() {

    sdk.on('connected', function () { state.connected = true; emit('connected'); });
    sdk.on('disconnected', function () {
      state.connected = false;
      // 断线后留着旧的通话/面板会显示成"还在这通电话里"：直接清掉，等重连
      // 快照（system.state + call.panel）重新下发。
      state.call = null;
      state.panel = null;
      emit('call', null);
      emit('panel', null);
      emit('disconnected');
    });

    // 通话面板快照（REQ-WS-023）：应用直接基于它构建自己的通话面板。
    sdk.on('call.panel', function (data) {
      state.panel = data || null;
      emit('panel', state.panel);
    });

    sdk.on('system.state', function (data) {
      // 连接建立时的状态快照：一次性补齐所有状态（不用轮询 getState）
      if (data) {
        if (data.register) state.register = data.register;
        if (data.version) state.version = data.version;
        if (data.os) state.os = data.os;     // 编译目标平台/架构（诊断页与版本行用）
        state.noUI = !!data.noUI;            // 仅托盘无 UI 版（REQ-UI-041）
        state.instance = data.instance || 0;  // 实例槽位（漏了这个，诊断页永远是 0）
        if (data.updatePrimarySite) state.updatePrimarySite = data.updatePrimarySite; // 官方更新站点（REQ-UPD-023）
        if (data.arch) state.arch = data.arch;
        if (data.autoAnswer !== undefined) state.autoAnswer = data.autoAnswer;
        if (data.update) state.update = data.update;
        if (data.recentDial) state.recentDial = data.recentDial;
        // 坐席状态：system.state 里只剩 agentMode（none = 不在线），完整状态由随后
        // 的 cti.agent 事件补发（软电话在 system.state 之后、call.panel 之前推）。
        const agentMode = data.agentMode || 'none';
        state.agent = {
          online: agentMode !== 'none',
          displayName: '',
          mode: agentMode,
          busy: false,
          isWork: false,
          leave: false,
          phoneStatus: '',
          agentStatus: 'unknown',
        };
        const calls = data.calls || [];
        // 软电话的 calls 保留历史（已结束的通话也在里面）：只取进行中的那条，
        // 否则重连后页面会把上一通已结束的电话当成当前通话显示。
        const active = calls.filter(function (c) {
          return c && (c.state === 'ringing' || c.state === 'established');
        });
        state.call = active.length ? active[active.length - 1] : null;
      }
      // 每次连上（含断线重连 / SDK 热更新换实例）都按**最新配置**刷新拉起地址：
      // 改过账号/传输后，自动拉起才会用新地址（原来只在首次连上同步一次，会过期）。
      syncLaunchUri( (data && data.instance) || 0);
      emit('state', state);
      emit('agent', state.agent);
      emit('call', state.call);
    });

    sdk.on('register.state', function (data) {
      state.register = {
        state: data.state || 'unregistered',
        account: data.account || '',
        reason: data.reason || '',
      };
      emit('register', state.register);
    });

    ['call.incoming', 'call.dialing', 'call.ringing', 'call.established',
      'call.audio', 'call.video'].forEach(function (name) {
        sdk.on(name, function (data) {
          // 迟到的事件可能带"已结束"的快照：setCall 会过滤掉，卡片随之收起。
          if (data && data.call) setCall(data.call);
        });
      });

    // call.muted 的负载是 {callId, muted}（不带 call 快照）：把它并回 state.call，
    // 界面才能把「保持」切换成「取消保持」。
    sdk.on('call.muted', function (data) {
      if (state.call && data && typeof data.muted === 'boolean') {
        state.call = Object.assign({}, state.call, { muted: data.muted });
      }
      emit('call', state.call);
    });

    ['call.terminated', 'call.failed'].forEach(function (name) {
      sdk.on(name, function (data) {
        // 结束事件：保留最后一份快照给界面显示原因，随后清空。
        // 注意 last 不能是 const——原来写 `const last = ...; last || (last = {})`
        // 在没有当前通话时会抛 TypeError（赋值给常量），整个结束事件就丢了。
        let last = state.call ? Object.assign({}, state.call) : null;
        if (data && data.call) last = Object.assign(last || {}, data.call);
        state.call = null;
        emit('call', null);
        emit('callEnded', { event: name, last: last, data: data || {} });
      });
    });

    sdk.on('system.autoAnswer', function (data) {
      state.autoAnswer = !!(data && data.enabled);
      emit('autoAnswer', state.autoAnswer);
    });

    sdk.on('system.update', function (data) {
      state.update = data;
      emit('update', data);
    });

    sdk.on('system.updateConfig', function () { emit('updateConfig'); });
    sdk.on('system.config', function (data) { emit('config', data); });
    sdk.on('contact.changed', function (data) {
      state.contactCount = data && data.total !== undefined ? data.total : state.contactCount;
      emit('contacts', data);
    });
    sdk.on('calllog.changed', function (data) { emit('calllog', data); });
    sdk.on('video.transform', function (data) { emit('transform', data); });
    sdk.on('video.web', function (data) { emit('videoWeb', data); });

    // ---- CTI（REQ-ACC-026 / REQ-ACC-028 / REQ-ACC-034）----
    // 坐席模式/置忙：顶栏徽章、在线座席页据此刷新。
    sdk.on('cti.agent', function (data) {
      const mode = (data && data.agentMode) || 'none';
      state.agent = {
        online: mode !== 'none',
        displayName: (data && data.displayName) || '',
        mode: mode,
        busy: !!(data && data.isBusy),
        isWork: !!(data && data.isWork),
        leave: !!(data && data.isLeave),
        phoneStatus: (data && data.phoneStatus) || '',
        agentStatus: (data && data.agentStatus) || 'unknown',
      };
      emit('agent', state.agent);
    });
    // 话务状态（双方通话 CALL_* / 咨询/三方/监听/会议）：负载带完整通话快照，
    // 直接并回 state.call（同样只认进行中的通话）
    sdk.on('cti.state', function (data) {
      if (data && data.call) setCall(data.call);
      emit('cti', state.call ? state.call.cti || null : null);
    });
    // 指令结果（桌面的按钮与网页可能同时发起）：页面可据此弹提示
    sdk.on('cti.result', function (data) { emit('ctiResult', data); });

    // ---- 视图直接订阅过的 SDK 事件（走 store 扇出，热更新后不丢）----
    // 全部事件（诊断页事件日志用）：透传成 sdkEvent，payload 为 {event, data}
    sdk.on('*', function (m) { emit('sdkEvent', m); });
    // 音频后端就绪（无 UI 版靠这条刷新设备列表）
    sdk.on('audio.ready', function (data) { emit('audio.ready', data); });
    // CTI 话务失败（CALL_FAILED/CONSULT_FAILED，软电话已翻成中文提示）
    sdk.on('cti.failed', function (data) { emit('cti.failed', data); });
  }

  // 首次连接失败（软电话没在跑且没给 sipUri）时，ready 会 reject：只记日志，
  // 引导器仍会在后续重连流程里继续探测/拉起。
  sdk.ready.catch(function (e) { console.error('[store] bootstrap 初始连接失败', e); });

  // 事件接线只做一次（门面负责在新实例上重挂）
  wire();

  App.store = {
    sdk: sdk,
    state: state,
    on: on,
    off: off,
    emit: emit,
    callActive: callActive,
    callID: callID,
    /** 已连接？（视图的加载入口用它挡住"还没连上就发请求"——那会以"尚未连接软电话"失败） */
    isConnected: function () { return state.connected; },
    // 便捷包装：界面代码里 sdk.xxx 直接经 store 走，统一在一处（热更新后仍指向当前实例）
    request: function () { return sdk; },
  };
})(window.SipApp);
