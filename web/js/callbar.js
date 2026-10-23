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

  /** 通话各方（会议只留主持人块 + 悬停成员列表）。 */
  function renderParties(call) {
    const box = $('barParties');
    App.dom.clear(box);
    const st = call.cti || {};

    if (st.talkState === 'conference') {
      const members = st.members || [];
      const hostNo = st.owner || App.callui.selfNumber();
      const host = members.find(function (m) {
        return App.callui.norm(m.telNo) === App.callui.norm(hostNo);
      });
      const block = App.dom.el('div', { class: 'bar-party' });
      block.appendChild(App.dom.el('span', { class: 'who' },
        host ? host.telNo + (host.displayName ? ' ' + host.displayName : '') : hostNo));
      // 悬停展开的成员列表：与通话卡片里的会议列表同一套行（主持人第一行、
      // 左侧移除/主持人图标、右侧会控）
      const list = App.dom.el('div', { class: 'bar-members' });
      const parties = App.callui.parties(call);
      parties.forEach(function (p) { list.appendChild(App.callui.partyRowEl(p)); });
      if (!parties.length) list.appendChild(App.dom.el('div', null, '暂无成员'));
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
      const hostParty = parties.find(function (p) { return p.host; });
      if (hostParty && (hostParty.lead || []).length) {
        const lead = App.dom.el('span', { class: 'lead' });
        hostParty.lead.forEach(function (a) { lead.appendChild(App.callui.actionEl(a)); });
        block.insertBefore(lead, block.firstChild);
      }
      block.appendChild(list);
      box.appendChild(block);
      return;
    }

    App.callui.parties(call).forEach(function (p) {
      const block = App.dom.el('div', { class: 'bar-party' });
      const who = App.dom.el('span', { class: 'who' });
      App.callui.fillPartyText(who, p, '#e5e7eb');
      block.appendChild(who);
      block.appendChild(App.callui.partyActions(p));
      box.appendChild(block);
    });
  }

  /** 右侧全局操作按钮（每次重建；显隐规则同桌面通话面板）。 */
  function renderActions(call) {
    const box = $('barActions');
    App.dom.clear(box);
    const st = call.cti || {};
    const ringing = call.state === 'ringing';
    const incoming = call.direction === 'incoming';
    const established = call.state === 'established';
    const talk = st.talkState || 'twoParty';
    const agent = App.callui.isAgent();
    const two = App.callui.twoPartyEstablished(call);
    const owner = st.owner && App.callui.norm(st.owner) === App.callui.selfNumber();
    const fail = function (e) { App.ui.toast(App.util.errText(e), 'err'); };
    const add = function (el) { box.appendChild(el); };
    // 文字按钮：都带图标（与 UI 同一套、颜色随文字）；useCTI=true 时走统一的
    // 在途处理（转圈 + 不可重复点击）。
    const mk = function (label, cls, fn, icon, useCTI) {
      const b = App.dom.el('button', { class: cls || '' }, label);
      if (icon) App.callui.decorateButton(b, icon);
      b.addEventListener('click', function () {
        if (useCTI) App.callui.runCTI(b, fn);
        else fn();
      });
      add(b);
      return b;
    };
    const text = function (label, cls, fn) { return mk(label, cls, fn, null, false); };

    // 来电振铃：接听/视频接听/拒接（没有挂断）
    if (ringing && incoming) {
      mk('接听', 'green', function () { sdk.answer(store.callID()).catch(fail); }, { img: '/img/phone-out.png' }, false);
      if (call.hasVideo) {
        mk('视频接听', 'green', function () { sdk.answerVideo(store.callID()).catch(fail); }, { img: '/img/video-out.png' }, false);
      }
      mk('拒接', 'red', function () { sdk.reject(store.callID()).catch(fail); }, { svg: 'stop' }, false);
      return;
    }

    // 通话中：拨号盘（DTMF，唯一的图形按钮），其后是文字操作按钮
    if (established) {
      const b = App.dom.el('button', { class: 'mini-icon dialpad-btn', title: '拨号盘（发送 DTMF）' });
      b.innerHTML = App.icons.svg(App.icons.PATH.dialpad, '#ffffff', 16);
      b.addEventListener('click', function (e) { App.dialpad.openDtmf(e.clientX, e.clientY); });
      add(b);
    }
    if (agent && two) {
      mk('咨询', '', function (e) { App.dialpad.open('consult', e.clientX, e.clientY); }, { img: '/img/consult.png' }, false);
      mk('转移', '', function (e) { App.dialpad.open('transfer', e.clientX, e.clientY); }, { img: '/img/transfer.png' }, false);
      mk('会议', '', function () { return sdk.addConferenceMembers([]); }, { img: '/img/conference.png' }, true);
    }
    if (talk === 'consult' && st.consultPhase === 'talking') {
      mk('三方通话', '', function () { return sdk.consultThreeWay(); }, { img: '/img/threeway.png' }, true);
      mk('咨询转移', '', function () { return sdk.consultTransfer(); }, { img: '/img/consult-transfer.png' }, true);
    }
    if (talk === 'monitor') {
      if (st.monitorPhase !== 'whisper') {
        mk('耳语', '', function () { return sdk.whisper(); }, { img: '/img/whisper.png' }, true);
        mk('插话', '', function () { return sdk.intervene(); }, { img: '/img/intervene.png' }, true);
        mk('强拦', '', function () { return sdk.intercept(); }, { img: '/img/intercept.png' }, true);
      }
    }
    if (talk === 'conference' && owner) {
      mk('添加成员', '', function (e) { App.dialpad.open('conference', e.clientX, e.clientY); }, { img: '/img/add-member.png' }, false);
      mk('结束会议', 'red', function () { return sdk.finishConference(); }, { img: '/img/end-conference.png' }, true);
    }

    if (established && talk === 'twoParty') {
      mk(call.muted ? '取消保持' : '保持', 'yellow', function () {
        sdk.mute(store.callID(), !call.muted).catch(fail);
      }, { svg: call.muted ? 'replay' : 'pause' }, false);
    }
    mk(talk === 'conference' ? '退出会议' : talk === 'threeWay' ? '退出三方'
      : talk === 'monitor' ? '结束监听' : '挂断',
      'red', function () { sdk.hangup(store.callID()).catch(fail); },
      (talk === 'conference' || talk === 'threeWay')
        ? { img: '/img/exit-conference.png' } : { svg: 'stop' }, false);
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
    store.on('call', render);
    store.on('agent', function () { if (store.state.call) render(store.state.call); });
    render(null);
  }

  App.callbar = { bind: bind, render: render };
})(window.SipApp);
