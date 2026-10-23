// 通话记录视图（REQ-CALL-007 / REQ-WS-017）：分页列表、按号码筛选、批量删除、
// 清空、回拨。展示口径与桌面通话记录页一致：
//   - 已接通显示"通话时长"（接通→结束）；
//   - 未接通来电显示"振铃时长"（开始→结束）、呼出显示"拨号时长"；
//   - 未接来电整行标红（missed = 呼入且未接通，本端拒接/挂断不算）。
window.SipApp = window.SipApp || {};
window.SipApp.views = window.SipApp.views || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const sdk = store.sdk;

  const PAGE = 50;
  let records = [];
  let total = 0;
  let hasMore = false;
  let filter = '';
  const selected = new Set(); // 按 callId 存（重新加载/删除后行号会重排）

  function resultText(r) {
    if (r.missed) return '未接';
    if (r.answeredAt) return '已接通';
    if (r.endedAt) return '未接通';
    return '进行中';
  }

  /**
   * 时长拆成两段显示（用户要求分开看）：
   *   waitText 等待时长——未接通时是"振铃/拨号时长"（开始→结束），
   *            接通后是"开始→接通"的振铃/拨号时长；
   *   talkText 通话时长——接通→结束，未接通显示 —。
   */
  function waitText(r) {
    if (!r.startedAt) return '—';
    const till = r.answeredAt || r.endedAt; // 没接通就一直算到结束
    if (!till) return '—';
    return App.fmt.durMs(r.startedAt, till);
  }

  function talkText(r) {
    if (!r.answeredAt || !r.endedAt) return '—';
    return App.fmt.durMs(r.answeredAt, r.endedAt);
  }

  function row(r) {
    const tr = App.dom.el('tr');

    const cb = App.dom.el('input', { type: 'checkbox' });
    cb.checked = selected.has(r.callId);
    cb.addEventListener('change', function () {
      if (cb.checked) selected.add(r.callId); else selected.delete(r.callId);
      syncSelection();
    });
    const tdCb = App.dom.el('td');
    tdCb.appendChild(cb);
    tr.appendChild(tdCb);

    // 方向：呼入/呼出各用一个箭头图标（未接来电标红），视频通话再加一个摄像头图标。
    // 图标路径是常量（见 icons.js），这里可以安全地用 innerHTML。
    const dir = App.dom.el('td', { class: 'dir' });
    const P = App.icons.PATH;
    const incoming = r.direction === 'incoming';
    const arrow = App.dom.el('span', {
      class: 'dir-icon' + (r.missed ? ' missed' : ''),
      title: (incoming ? '呼入' : '呼出') + (r.missed ? '（未接）' : ''),
    });
    arrow.innerHTML = App.icons.svg(incoming ? P.callIn : P.callOut,
      r.missed ? '#dc2626' : '#6b7280', 16);
    dir.appendChild(arrow);
    if (r.hasVideo) {
      const cam = App.dom.el('span', { class: 'dir-icon', title: '视频通话' });
      cam.innerHTML = App.icons.svg(P.videoOn, '#6b7280', 16);
      dir.appendChild(cam);
    }
    tr.appendChild(dir);

    // 对端展示：命中通信录用「号码 名称 - 备注★」，未命中用 SIP 显示名
    // 「显示名（号码）」；号码与名称相同（很常见）时只显示一次。
    // 备注**直接显示**在名称后面（用户要求：不做鼠标悬停提示）。
    const who = App.dom.el('td');
    const nameLine = App.dom.el('div');
    if (r.missed) nameLine.className = 'missed';
    App.display.into(nameLine, r); // 与通话条/通话卡片共用一套「号码 名称 - 备注★」
    who.appendChild(nameLine);
    tr.appendChild(who);

    tr.appendChild(App.dom.el('td', { class: 'num' }, App.fmt.time(r.startedAt)));
    tr.appendChild(App.dom.el('td', { class: 'num' }, waitText(r)));
    tr.appendChild(App.dom.el('td', { class: 'num' }, talkText(r)));

    const res = App.dom.el('td', null, resultText(r));
    if (r.missed) res.className = 'missed';
    if (r.reason && !r.endedAt) res.title = r.reason;
    tr.appendChild(res);

    const acts = App.dom.el('td', { class: 'acts' });
    const call = App.dom.el('button', { class: 'ghost' }, '呼叫');
    call.addEventListener('click', function () {
      // 回拨时带上记录里的座席显示名（没有则照旧走通信录）
      sdk.dial(r.remote, { name: r.displayName }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
    });
    const vcall = App.dom.el('button', { class: 'ghost' }, '视频');
    vcall.addEventListener('click', function () {
      sdk.dialVideo(r.remote, r.displayName).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
    });
    const del = App.dom.el('button', { class: 'red' }, '删除');
    del.addEventListener('click', function () {
      deleteRecords([r.callId]);
    });
    acts.appendChild(call);
    acts.appendChild(vcall);
    acts.appendChild(del);
    tr.appendChild(acts);
    return tr;
  }

  function render() {
    const tbody = $('histList');
    App.dom.clear(tbody);
    records.forEach(function (r) { tbody.appendChild(row(r)); });
    $('histTotal').textContent = total ? '共 ' + total + ' 条（已加载 ' + records.length + '）' : '没有通话记录';
    $('btnHistMore').hidden = !hasMore;
    syncSelection();
  }

  function syncSelection() {
    $('btnHistDelete').disabled = selected.size === 0;
    $('btnHistDelete').textContent = selected.size ? '删除选中（' + selected.size + '）' : '删除选中';
  }

  function load(append) {
    if (!store.isConnected()) return; // 没连上不发请求（连上后 onShow 会重新加载）
    const offset = append ? records.length : 0;
    if (!append) selected.clear();
    return sdk.queryCallLog({ remote: filter, limit: PAGE, offset: offset }).then(function (res) {
      records = append ? records.concat(res.records || []) : (res.records || []);
      total = res.total || 0;
      hasMore = !!res.hasMore;
      render();
    }).catch(function (e) {
      App.ui.toast('读取通话记录失败：' + App.util.errText(e), 'err');
    });
  }

  function deleteRecords(callIds) {
    App.ui.confirm('删除通话记录', '删除 ' + callIds.length + ' 条记录？此操作不可撤销。').then(function (ok) {
      if (!ok) return;
      sdk.deleteCallLog(callIds).then(function (res) {
        App.ui.toast('已删除 ' + res.deleted + ' 条', 'ok');
        load(false);
      }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
    });
  }

  App.views.history = {
    mount: function () {
      $('histFilter').addEventListener('input', App.util.debounce(function () {
        filter = this.value.trim();
        load(false);
      }, 300));
      $('btnHistRefresh').addEventListener('click', function () { load(false); });
      $('btnHistMore').addEventListener('click', function () { load(true); });
      $('btnHistDelete').addEventListener('click', function () {
        deleteRecords(Array.from(selected));
      });
      $('btnHistClear').addEventListener('click', function () {
        App.ui.confirm('清空通话记录', '将删除全部通话记录，此操作不可撤销。').then(function (ok) {
          if (!ok) return;
          sdk.clearCallLog().then(function (res) {
            App.ui.toast('已清空 ' + res.deleted + ' 条', 'ok');
            load(false);
          }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
        });
      });

      // 通话结束会先入库再发事件，所以收到结束事件后重载就能看到新记录
      store.on('callEnded', function () { if (!$('view-history').hidden) load(false); });
      // 其它标签删了记录（calllog.changed）也要跟着刷新
      store.on('calllog', function () { if (!$('view-history').hidden) load(false); });
    },
    onShow: function () { load(false); },
    onHide: function () { /* 保留列表，下次进入重新加载 */ },
  };
})(window.SipApp);
