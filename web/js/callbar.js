// 常驻通话条：任何视图都能看到当前话务并操作。
//
// 多方话务按"每一方一个块"展示（咨询/三方/监听）：
//   - 每方一个块，块右侧是这一方的图形操作按钮（结束咨询/强制挂断/结束监听…）；
//   - 会议只显示**主持人**的块，鼠标移上去浮现完整成员列表；
//   - 全局操作（接听/拒接/保持/挂断、拨号盘/咨询/转移/会议、监听三件套…）
//     放在右侧，按钮显隐与桌面通话面板一致。
// 来电振铃只有接听/视频接听/拒接——**不显示挂断**（与拒接同义）。
window.SipApp = window.SipApp || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const sdk = store.sdk;

  let timerHandle = null;
  let establishedAt = 0;
  // 会议成员列表的展开状态：由鼠标进入/离开控制（点按钮不关闭——
  // 操作会触发重画，用这个标志在重画后保持展开）。
  let confMembersOpen = false;

  function stopTimer() {
    if (timerHandle) { clearInterval(timerHandle); timerHandle = null; }
    $('barTimer').textContent = '';
  }

  function startTimer() {
    if (timerHandle) return;
    establishedAt = Date.now();
    const tick = function () {
      $('barTimer').textContent = App.fmt.dur((Date.now() - establishedAt) / 1000);
    };
    tick();
    timerHandle = setInterval(tick, 1000);
  }

  /** 通话各方（会议只留主持人块 + 悬停成员列表）。数据来自 call.panel 快照。 */
  function renderParties(call) {
    const box = $('barParties');
    App.dom.clear(box);
    const conf = App.callui.panelConf();

    if (conf) {
      // 协议 §16.3：conf.owner 是主持人号码，成员是 member:{number,name,note} 对象
      const hostNo = conf.owner || '';
      const hostMember = (conf.memberList || []).find(function (m) {
        return App.callui.norm((m.member || {}).number) === App.callui.norm(hostNo);
      }) || {};
      const hostName = (hostMember.member || {}).name || '';
      const block = App.dom.el('div', { class: 'bar-party' });
      block.appendChild(App.dom.el('span', { class: 'who' },
        hostNo + (hostName ? ' ' + hostName : '')));
      // 悬停展开的成员列表：与通话卡片里的会议列表同一套行（主持人第一行、
      // 左侧移除/主持人图标、右侧会控）
      const list = App.dom.el('div', { class: 'bar-members' });
      const rows = App.callui.displayRows(call);
      rows.forEach(function (p) { list.appendChild(App.callui.partyRowEl(p)); });
      if (!rows.length) list.appendChild(App.dom.el('div', null, '暂无成员'));
      // 展开/收起只由鼠标是否停在方块（含列表自身）上决定：
      // 点列表里的会控按钮会触发重画，这里按标志保持展开，鼠标离开才收起。
      list.style.display = confMembersOpen ? 'block' : 'none';
      block.addEventListener('mouseenter', function () {
        confMembersOpen = true;
        list.style.display = 'block';
      });
      block.addEventListener('mouseleave', function () {
        confMembersOpen = false;
        list.style.display = 'none';
      });
      // 主持人块左侧也带主持人图标
      const hostRow = rows.find(function (p) { return p.host; });
      if (hostRow && (hostRow.lead || []).length) {
        const lead = App.dom.el('span', { class: 'lead' });
        hostRow.lead.forEach(function (a) { lead.appendChild(App.callui.actionEl(a)); });
        block.insertBefore(lead, block.firstChild);
      }
      block.appendChild(list);
      box.appendChild(block);
      return;
    }

    App.callui.displayRows(call).forEach(function (p) {
      const block = App.dom.el('div', { class: 'bar-party' });
      const who = App.dom.el('span', { class: 'who' });
      App.callui.fillPartyText(who, p, '#e5e7eb');
      if (p.tag) who.appendChild(App.dom.el('span', { class: 'tag skill-tag' }, p.tag));
      block.appendChild(who);
      block.appendChild(App.callui.partyActions(p));
      box.appendChild(block);
    });
  }

  /** 右侧全局操作按钮：全部来自面板快照（call.panel）的 panelControls。 */
  function renderActions(call) {
    const box = $('barActions');
    App.dom.clear(box);
    const fail = function (e) { App.ui.toast(App.util.errText(e), 'err'); };

    App.callui.panelControls(call).forEach(function (sp) {
      // 拨号盘（发送 DTMF）是唯一的图形按钮
      if (sp.id === 'btnDtmfPad') {
        const b = App.dom.el('button', { class: 'mini-icon dialpad-btn', title: '拨号盘（发送 DTMF）' });
        b.innerHTML = App.icons.svg(App.icons.PATH.dialpad, '#ffffff', 16);
        b.addEventListener('click', function (e) { App.dialpad.openDtmf(e.clientX, e.clientY); });
        box.appendChild(b);
        return;
      }
      const b = App.dom.el('button', { class: sp.cls || '' }, sp.label);
      if (sp.icon) App.callui.decorateButton(b, sp.icon);
      b.addEventListener('click', function (ev) {
        if (sp.cti) {
          App.callui.runCTI(b, sp.run, undefined, sp.id);
          return;
        }
        const p = sp.run(ev);
        if (p && p.catch) p.catch(fail);
      });
      box.appendChild(b);
      // 该动作仍在途（面板在点击后重画过、按钮已重建）：把转圈贴到新按钮
      App.callui.attachPending(sp.id, b);
    });
  }

  function render(call) {
    const bar = $('callBar');
    if (!call) {
      bar.hidden = true;
      stopTimer();
      return;
    }
    bar.hidden = false;
    bar.classList.toggle('ringing', call.state === 'ringing' && call.direction === 'incoming');
    renderParties(call);
    renderActions(call);
    if (call.state === 'established') startTimer(); else stopTimer();
  }

  function bind() {
    // CTI 指令结束：操作按钮按当前面板重新渲染（转圈不能恢复点击前的快照）
    App.callui.onRender(function () { renderActions(store.state.call); });
    store.on('call', render);
    // 面板快照变化（call.panel）：各方与操作按钮都以它为准
    store.on('panel', function () { render(store.state.call); });
    store.on('agent', function () { if (store.state.call) render(store.state.call); });
    render(null);
  }

  App.callbar = { bind: bind, render: render };
})(window.SipApp);
