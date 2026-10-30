// 多方通话的公共渲染逻辑（通话卡片与常驻话务条共用）：
//   - 状态文案（呼叫中/振铃中/通话中/咨询呼叫中/咨询通话中/三方通话中/监听中/会议中）
//   - 通话各方列表（每方带自己的操作按钮，逻辑与桌面通话面板一致）
//   - 图形按钮构造（与桌面面板同一套图标的 web 版本）
window.SipApp = window.SipApp || {};

(function (App) {
  'use strict';

  const store = App.store;
  const sdk = store.sdk;

  /** 号码规范化（与软电话 core.NormalizeNumber 同口径的轻量版）。 */
  function norm(v) {
    return String(v || '')
      .replace(/^sips?:/i, '')
      .replace(/[<>；;]/g, '')
      .replace(/\?.*$/, '')
      .replace(/@.*$/, '')
      .replace(/[\s\-()\[\]_./]/g, '')
      .toLowerCase();
  }

  function selfNumber() { return norm(store.state.register && store.state.register.account); }

  function isAgent() {
    // online 由 store 从 agentMode 派生（agentMode !== 'none'），无需再判 mode
    return !!store.state.agent.online;
  }

  function isMonitor() { return (store.state.agent || {}).mode === 'monitor'; }

  /** 通话状态文案（用户定的口径）。 */
  function status(call) {
    if (!call) return '';
    const st = call.cti || {};
    switch (st.talkState) {
      case 'consult':
        if (st.consultPhase === 'talking') return '咨询通话中';
        if (st.consultPhase === 'ringing') return '咨询振铃中';
        return '咨询呼叫中';
      case 'threeWay':
        return '三方通话中';
      case 'monitor':
        return st.monitorPhase === 'whisper' ? '监听耳语中' : '监听中';
      case 'conference':
        return '会议中';
    }
    // CTI 的 CALL_* 事件（协议 §16.2）带来 SIP 信令里没有的阶段：
    // CALL_RINGING 时显示"振铃中"（服务端外呼不一定有 180），已接通优先。
    if (call.state === 'ringing') {
      if (call.direction === 'incoming' || st.callPhase === 'ringing') return '振铃中';
      return '呼叫中';
    }
    if (call.state === 'established') return '通话中';
    if (st.callPhase === 'ringing') return '振铃中';
    if (st.callPhase === 'talking') return '通话中';
    return '呼叫中';
  }

  /** 两方通话且已接通（坐席话务入口只在此时显示）。 */
  function twoPartyEstablished(call) {
    if (!call || call.state !== 'established') return false;
    const st = call.cti || {};
    return (!st.talkState || st.talkState === 'twoParty') && !st.consultInProgress && !call.consult;
  }

  function memberHas(state, flag) {
    return String(state || '').split('+').map(function (s) { return s.trim(); }).indexOf(flag) >= 0;
  }

  /** 某一方的显示名：协议名称 > 通话对象的名称/通信录（仅对端）> 空。 */
  function partyName(call, no) {
    const st = (call && call.cti) || {};
    const pairs = [
      [st.firstParty, st.firstPartyName], [st.secondParty, st.secondPartyName],
      [st.thirdParty, st.thirdPartyName], [st.owner, st.ownerName],
      [st.consulter, st.consulterName], [st.consultee, st.consulteeName],
      [st.monitor, st.monitorName], [st.target, st.targetName],
      [st.callerNo, st.callerName], [st.calledNo, st.calledName],
    ];
    for (let i = 0; i < pairs.length; i++) {
      if (pairs[i][0] && pairs[i][1] && norm(pairs[i][0]) === norm(no)) return pairs[i][1];
    }
    if (call && norm(call.remote) === norm(no)) {
      if (call.peerName) return call.peerName;
      if (call.remoteName) return call.remoteName;
      if (call.contact && call.contact.name) return call.contact.name;
    }
    return '';
  }

  /**
   * 通话各方（按话务状态）：每方 { no, name, actions: [{icon,title,danger,onClick}] }。
   * 自己的行：三方/监听/咨询里不显示自己（与桌面面板一致）；会议显示全部成员。
   */
  function parties(call) {
    const out = [];
    if (!call) return out;
    const st = call.cti || {};
    const self = selfNumber();
    const push = function (no, name, actions, includeSelf, note) {
      if (!no) return;
      if (!includeSelf && norm(no) === self) return;
      if (out.some(function (p) { return norm(p.no) === norm(no); })) return;
      out.push({ no: no, name: name || partyName(call, no), actions: actions || [], note: note || '' });
    };

    switch (st.talkState) {
      case 'consult': {
        push(call.remote, '', []);
        const finish = {
          icon: 'stop', title: '结束咨询', danger: true,
          run: function () { return sdk.finishConsult(); },
        };
        const phase = st.consultPhase === 'talking' ? '通话中'
          : st.consultPhase === 'ringing' ? '振铃中' : '拨号中';
        push(st.consultee, st.consulteeName, [finish], false, phase);
        break;
      }
      case 'threeWay': {
        const owner = norm(st.owner) === self;
        const force = function (no) {
          return [{
            icon: 'removeCircle', title: '强制挂断', danger: true,
            run: function () { return sdk.exitThreeWay(no); },
          }];
        };
        [st.firstParty, st.secondParty, st.thirdParty].forEach(function (no) {
          if (no) push(no, '', owner ? force(no) : []);
        });
        break;
      }
      case 'monitor': {
        // 与 UI 一致：**一行**显示主被叫（caller → called），不显示自己；
        // 结束监听统一走底部的挂断按钮（文案改为「结束监听」）。
        // 有 callerNo 时名称只取 callerName（不能回退到 monitorName——
        // 那会把监听座席自己的名字显示成主叫，用户实测）。
        let leftNo = st.callerNo;
        let leftName = st.callerName;
        if (!leftNo) {
          leftNo = st.monitor;
          leftName = st.monitorName;
        }
        let rightNo = st.target;
        let rightName = st.targetName;
        if (st.callerNo && norm(st.callerNo) === norm(st.target)) {
          rightNo = st.calledNo;
          rightName = st.calledName;
        }
        out.push({
          no: leftNo, name: leftName, rightNo: rightNo, rightName: rightName,
          arrow: true, actions: [],
        });
        break;
      }
      case 'conference': {
        const owner = norm(st.owner) === self;
        const hostNo = st.owner || '';
        // 主持人永远第一行（其余保持服务端顺序）
        const list = (st.members || []).slice().sort(function (a, b) {
          return norm(a.telNo) === norm(hostNo) ? -1 : norm(b.telNo) === norm(hostNo) ? 1 : 0;
        });
        const hasVideo = !!(call.video && call.video.negotiated);
        list.forEach(function (m) {
          const isSelf = norm(m.telNo) === self;
          const isHost = norm(m.telNo) === norm(hostNo);
          const dialing = memberHas(m.state, 'dialing');
          // 左侧：移除成员（owner，黄色，最左）或主持人图标
          const lead = [];
          if (isHost) {
            lead.push({ image: '/img/host.png', title: '主持人' });
          } else if (owner && !isSelf && !dialing) {
            lead.push({ icon: 'removeCircle', title: '移除成员', warn: true,
              run: function () { return sdk.removeConferenceMembers([m.telNo]); } });
          }
          const acts = [];
          if (dialing) {
            if (owner) {
              acts.push({ icon: 'stop', title: '取消呼叫', danger: true,
                run: function () { return sdk.removeConferenceMembers([m.telNo]); } });
            }
          } else {
            // 顺序与 UI 一致：禁视频 / 禁音 / 禁听；非视频会议不显示禁视频
            const muted = memberHas(m.state, 'muted');
            const deaf = memberHas(m.state, 'deafened');
            const vmuted = memberHas(m.state, 'vmuted');
            if (hasVideo) {
              acts.push({
                icon: vmuted ? 'videoOff' : 'videoOn', title: vmuted ? '取消禁视频' : '禁视频',
                danger: vmuted, readOnly: !owner,
                run: owner ? function () { return sdk.setConferenceMemberVideoMuted(m.telNo, !vmuted); } : null,
              });
            }
            acts.push({
              icon: muted ? 'micOff' : 'micOn', title: muted ? '取消禁音' : '禁音',
              danger: muted, readOnly: !owner,
              run: owner ? function () { return sdk.setConferenceMemberMuted(m.telNo, !muted); } : null,
            });
            acts.push({
              icon: deaf ? 'speakerOff' : 'speakerOn', title: deaf ? '取消禁听' : '禁听',
              danger: deaf, readOnly: !owner,
              run: owner ? function () { return sdk.setConferenceMemberDeafened(m.telNo, !deaf); } : null,
            });
          }
          out.push({
            no: m.telNo, name: m.displayName, actions: acts, lead: lead,
            reserveLead: true, host: isHost, note: '',
          });
        });
        break;
      }
      default:
        push(call.remote, '');
    }
    return out;
  }

  /** 一方的展示文本（号码 + 名称）。 */
  function partyText(p) {
    if (p.name && norm(p.name) !== norm(p.no)) return p.no + ' ' + p.name;
    return p.no;
  }

  /** 图形按钮（与桌面面板同一套图标）：kind = normal（蓝）/ danger（红）/ warn（黄）。 */
  function iconButton(icon, title, onClick, kind, readOnly) {
    const path = App.icons.PATH[icon] || App.icons.PATH.phone;
    const b = App.dom.el('button', {
      class: 'mini-icon ' + (kind === 'danger' ? 'danger' : kind === 'warn' ? 'warn' : 'normal'),
      title: title || '',
    });
    b.innerHTML = App.icons.svg(path, kind === 'danger' ? '#dc2626' : kind === 'warn' ? '#d97706' : '#2563eb', 16);
    if (onClick) b.addEventListener('click', onClick);
    if (readOnly) { b.disabled = true; b.style.cursor = 'default'; }
    return b;
  }

  // ---- MSP_ACTION 的在途反馈（与 UI 一致：图标换成转圈，期间不能再次点击） ----

  let ctiBusy = false;

  /** 给文字按钮加图标（PNG 白图标或内联 SVG，颜色与按钮文字一致 = 白色）。 */
  function decorateButton(btn, spec) {
    if (!btn || !spec) return;
    const old = btn.querySelector('.btn-ico');
    if (old) old.remove();
    btn.classList.add('has-ico');
    const ico = App.dom.el('span', { class: 'btn-ico' });
    if (spec.img) {
      ico.appendChild(App.dom.el('img', { src: spec.img, alt: '' }));
    } else {
      // currentColor：颜色跟着按钮文字走（用户要求图标与文字同色）
      ico.innerHTML = App.icons.svg(App.icons.PATH[spec.svg] || App.icons.PATH.phone, 'currentColor', 16);
    }
    btn.insertBefore(ico, btn.firstChild);
  }

  // 在途动作：id 用于"面板重画后把转圈贴到新按钮上"（话务条每次面板变化都会
  // 整条重建，旧按钮已被丢弃）；btns 收集所有被贴上转圈的按钮（通话卡片与
  // 话务条可能各有一个），结束时统一恢复。结束时**不恢复点击前的快照**，
  // 而是按当前面板重新渲染（在途期间面板可能已经变化，例如耳语→监听，
  // 恢复旧快照会把「监听」又冲回「耳语」）。
  let ctiPending = null;
  const renderHooks = [];

  /** 注册"在途结束后按当前面板重新渲染按钮"的回调（通话卡片/话务条各一份）。 */
  function onRender(fn) {
    if (typeof fn === 'function') renderHooks.push(fn);
  }

  /** 某个动作是否在途（面板重画后据此给新按钮补上转圈）。 */
  function isPending(id) {
    return !!(id && ctiPending && ctiPending.id === id);
  }

  /** 给按钮贴"在途"样式：图标换成旋转圆圈、禁用点击。 */
  function spinButton(btn) {
    if (!btn) return;
    btn.disabled = true;
    btn.classList.add('cti-busy');
    const ico = btn.querySelector('.btn-ico');
    if (ico) ico.innerHTML = '<span class="cti-spin"></span>';
    else btn.innerHTML = '<span class="cti-spin"></span>';
  }

  /** 面板重画后调用：该动作仍在途时把新按钮接管为在途按钮并重新转圈。 */
  function attachPending(id, btn) {
    if (!isPending(id) || !btn) return;
    if (ctiPending.btns.indexOf(btn) < 0) ctiPending.btns.push(btn);
    spinButton(btn);
  }

  /**
   * 执行一条 MSP_ACTION：同一时间只允许一条在途（与软电话一致）。
   * 在途期间按钮转圈并禁用，其它动作按钮点击只提示「上一个操作尚未完成」。
   * `id` 是按钮对应的 panelControls id（可选）：面板在途期间重画时，用它把
   * 转圈补到新按钮上。
   * 返回 Promise（成功 resolve 结果，失败 resolve null，错误已弹提示）。
   */
  function runCTI(btn, fn, okText, id) {
    if (ctiBusy) {
      App.ui.toast('上一个操作尚未完成');
      return Promise.resolve(null);
    }
    ctiBusy = true;
    ctiPending = { id: id || null, btns: btn ? [btn] : [] };
    spinButton(btn);
    let p;
    try {
      p = Promise.resolve(fn());
    } catch (e) {
      p = Promise.reject(e);
    }
    return p.then(function (v) {
      if (okText) App.ui.toast(okText, 'ok');
      return v === undefined ? true : v;
    }).catch(function (e) {
      if (!App.util.isOfflineErr(e)) App.ui.toast(App.util.errText(e), 'err');
      return null;
    }).then(function (v) {
      const btns = (ctiPending && ctiPending.btns) || [];
      ctiBusy = false;
      ctiPending = null;
      btns.forEach(function (b) {
        b.disabled = false;
        b.classList.remove('cti-busy');
      });
      // 按当前面板重新渲染按钮文案/图标（不能用点击前的快照恢复）。
      renderHooks.forEach(function (fn) {
        try { fn(); } catch (e) { console.error(e); }
      });
      return v;
    });
  }

  /** 主持人图标（PNG，与 UI 同一份），悬停提示「主持人」。 */
  function hostIcon(title) {
    const b = App.dom.el('span', { class: 'host-icon', title: title || '主持人' });
    b.appendChild(App.dom.el('img', { src: '/img/host.png', alt: '' }));
    return b;
  }

  /** 渲染一个按钮/只读图标（party 的 lead / actions 统一入口）。
   *  可点击的动作统一走 runCTI：转圈 + 在途互斥（同一时间只允许一条 MSP_ACTION）。 */
  function actionEl(a) {
    if (a.image) return hostIcon(a.title);
    const kind = a.danger ? 'danger' : a.warn ? 'warn' : 'normal';
    const b = iconButton(a.icon, a.title, null, kind, !a.run);
    if (a.run) b.addEventListener('click', function () { runCTI(b, a.run); });
    return b;
  }

  /** 座席状态图标（与桌面 UI 的座位图标同一份，按状态着色）：绿/黄/红。 */
  function agentIcon(status) {
    const v = String(status || '').toLowerCase();
    const color = v === 'free' ? 'green'
      : (v === 'dialing' || v === 'ringing' || v === 'talking') ? 'yellow' : 'red';
    return App.dom.el('img', {
      class: 'agent-status-icon', alt: '', src: '/img/agent-seat-' + color + '.png',
    });
  }

  /** 为某一方填充操作按钮（**图形按钮**，图标与桌面 UI 一致；无按钮时返回空容器）。 */
  function partyActions(p) {
    const box = App.dom.el('span', { class: 'acts' });
    (p.actions || []).forEach(function (a) { box.appendChild(actionEl(a)); });
    return box;
  }

  /** 一方完整的行 DOM（左侧 lead=[移除/主持人图标]、中间号码名称、右侧操作）。
   *  会议列表与话务条的成员悬停列表共用，保证两处显示一致。 */
  function partyRowEl(p) {
    const row = App.dom.el('div', { class: 'party-row' });
    // 会议行**始终**保留首列（固定宽度），没有按钮/图标的行也占位：
    // 这样移除按钮、主持人图标与各行号码左缘都对齐（用户要求）。
    if (p.reserveLead || (p.lead || []).length) {
      const lead = App.dom.el('span', { class: 'lead lead-slot' });
      (p.lead || []).forEach(function (a) { lead.appendChild(actionEl(a)); });
      row.appendChild(lead);
    }
    const who = App.dom.el('div', { class: 'who' });
    fillPartyText(who, p, '#6b7280');
    if (p.name && p.name !== p.no) who.title = partyText(p);
    // 技能组小标签：紧跟在号码/名称后面（CALL_RINGING 的 skillGroup）
    if (p.tag) who.appendChild(App.dom.el('span', { class: 'tag skill-tag' }, p.tag));
    if (p.note) who.appendChild(App.dom.el('span', { class: 'nm' }, ' · ' + p.note));
    row.appendChild(who);
    row.appendChild(partyActions(p));
    return row;
  }

  /** 把某一方的展示写进元素；监听是「主叫 → 被叫」一行（arrow=true）。 */
  function fillPartyText(el, p, color) {
    App.dom.clear(el);
    const left = partyText({ no: p.no, name: p.name });
    if (!p.arrow) { el.textContent = left; return; }
    el.appendChild(document.createTextNode(left));
    const arrow = App.dom.el('span', { class: 'party-arrow' });
    arrow.innerHTML = App.icons.svg(App.icons.PATH.arrow, color || '#6b7280', 14);
    el.appendChild(arrow);
    el.appendChild(document.createTextNode(partyText({ no: p.rightNo, name: p.rightName })));
  }

  // ---------------- 通话面板事件（call.panel，REQ-WS-023） ----------------
  //
  // 软电话把面板状态整理成 CallPanelState 推送：{callPanel, conferencePanel}。
  // 页面直接用这份快照渲染，不再自己解析 call.cti 的明细。

  /** 本机是否是咨询发起方。协议 §16.2 用面板的 `owner` 指定发起方（咨询方）；
   *  老服务端没有 owner 时才回退旧口径（callMode=consult 且 callList 里有
   *  callType=consult 的腿）。被咨询方的面板 callMode 也是 consult——按普通通话
   *  显示，不出现咨询行/咨询按钮。 */
  function isConsultingPanel(cp) {
    if (!cp || String(cp.callMode || '').toLowerCase() !== 'consult') return false;
    if (cp.owner) return norm(cp.owner) === selfNumber();
    return (cp.callList || []).some(function (l) {
      return String(l.callType || '').toLowerCase() === 'consult';
    });
  }

  function panel() { return store.state.panel || null; }

  /** 是否使用服务端下发的面板：新协议里 callId 是 CTI 侧的话务 ID（与本地 SIP
   *  call.id 不同），**不能**拿它和本地 call.id 比较——面板事件本身就是当前话务的
   *  权威快照，直接采用。 */
  function panelMatchesCall(p) { return !!p; }

  function panelCall() {
    const p = panel();
    return p && p.callPanel && panelMatchesCall(p) ? p.callPanel : null;
  }
  function panelConf() {
    const p = panel();
    return p && p.conferencePanel && panelMatchesCall(p) ? p.conferencePanel : null;
  }

  /** 面板标题（拨号中/振铃中/咨询呼叫中/…）；没有面板时回退旧口径。 */
  function displayStatus(call) {
    const cp = panelCall();
    return cp && cp.title ? cp.title : status(call);
  }

  /** 三方通话的发起方是否本机：优先用面板的 owner（协议 §16.2），
   *  老服务端没有 owner 时回退通话快照里的 owner。 */
  function threeWayOwner(call) {
    const cp = panelCall();
    if (cp && cp.owner) return norm(cp.owner) === selfNumber();
    const st = (call && call.cti) || {};
    return !!st.owner && norm(st.owner) === selfNumber();
  }

  function legStateText(st) {
    switch (st) {
      case 'incoming': return '来电';
      case 'dialing': return '拨号中';
      case 'ringing': return '振铃中';
      case 'talking': return '通话中';
      case 'holding': return '保持中';
    }
    return '';
  }

  /** 会议成员行（与 callPanel 的行共用 partyRowEl 结构）。
   *  字段按协议 §16.3：`{confId, owner, memberList:[{member:{number,name,note},
   *  intercom, owner, state, timestamp}]}`（member 与 peer 同为对象）。 */
  function conferenceRows(conf, call) {
    const hostNo = conf.owner || '';
    const owner = !!hostNo && norm(hostNo) === selfNumber();
    const hasVideo = !!(call && call.video && call.video.negotiated);
    return (conf.memberList || []).map(function (m) {
      const member = m.member || {};
      const no = member.number || '';
      const isSelf = norm(no) === selfNumber();
      const isHost = !!m.owner || norm(no) === norm(hostNo);
      const dialing = memberHas(m.state, 'dialing');
      const lead = [];
      if (isHost) {
        lead.push({ image: '/img/host.png', title: '主持人' });
      } else if (owner && !isSelf && !dialing) {
        lead.push({
          icon: 'removeCircle', title: '移除成员', warn: true,
          run: function () { return sdk.removeConferenceMembers([no]); },
        });
      }
      const acts = [];
      if (dialing) {
        if (owner) {
          acts.push({
            icon: 'stop', title: '取消呼叫', danger: true,
            run: function () { return sdk.removeConferenceMembers([no]); },
          });
        }
      } else {
        // 顺序与 UI 一致：禁视频 / 禁音 / 禁听；非视频会议不显示禁视频
        const muted = memberHas(m.state, 'muted');
        const deaf = memberHas(m.state, 'deafened');
        const vmuted = memberHas(m.state, 'vmuted');
        if (hasVideo) {
          acts.push({
            icon: vmuted ? 'videoOff' : 'videoOn', title: vmuted ? '取消禁视频' : '禁视频',
            danger: vmuted, readOnly: !owner,
            run: owner ? function () { return sdk.setConferenceMemberVideoMuted(no, !vmuted); } : null,
          });
        }
        acts.push({
          icon: muted ? 'micOff' : 'micOn', title: muted ? '取消禁音' : '禁音',
          danger: muted, readOnly: !owner,
          run: owner ? function () { return sdk.setConferenceMemberMuted(no, !muted); } : null,
        });
        acts.push({
          icon: deaf ? 'speakerOff' : 'speakerOn', title: deaf ? '取消禁听' : '禁听',
          danger: deaf, readOnly: !owner,
          run: owner ? function () { return sdk.setConferenceMemberDeafened(no, !deaf); } : null,
        });
      }
      return {
        no: no, name: member.name || '', note: member.note || '',
        actions: acts, lead: lead, reserveLead: true, host: isHost,
      };
    });
  }

  /** 面板里的通话各方（callList / 会议成员）→ partyRowEl 可用的行。 */
  function panelRows(call) {
    const conf = panelConf();
    if (conf) return conferenceRows(conf, call);
    const cp = panelCall();
    if (!cp) return [];
    const list = cp.callList || [];
    const st = cp.state;
    if (st === 'monitoring' || st === 'whispering') {
      // 一行显示"主叫 → 被叫"（与 UI 一致）
      const leg = list[0];
      if (!leg) return [];
      return [{
        no: leg.peer.number, name: leg.peer.name, note: leg.peer.note,
        rightNo: leg.target ? leg.target.number : '',
        rightName: leg.target ? leg.target.name : '',
        arrow: true, actions: [],
      }];
    }
    // 只有**咨询发起方**（owner=本机）才显示咨询行/结束咨询；被咨询方的面板
    // 按普通通话显示。
    const consulting = isConsultingPanel(cp);
    const owner = threeWayOwner(call);
    return list.map(function (leg, i) {
      const actions = [];
      // 咨询：被咨询方那行（第二行）带「结束咨询」（尚未接通用「取消咨询」）
      if (consulting && i > 0) {
        actions.push({
          icon: 'stop', title: st === 'consult-talking' ? '结束咨询' : '取消咨询', danger: true,
          run: function () { return sdk.finishConsult(); },
        });
      }
      // 三方：只有发起方（owner）能强制挂断某一方
      if (st === 'three-way' && owner) {
        actions.push({
          icon: 'removeCircle', title: '强制挂断', danger: true,
          run: function () { return sdk.exitThreeWay(leg.peer.number); },
        });
      }
      const note = leg.peer.note || (consulting && i > 0 ? legStateText(leg.callStatus) : '');
      return {
        no: leg.peer.number, name: leg.peer.name, note: note, actions: actions,
        // 技能组标签（协议 §16.2 的 callList[].skillGroup）：对端号码右侧的小 tag
        tag: i === 0 && leg.skillGroup ? leg.skillGroup : '',
      };
    });
  }

  /** 面板行优先；没有面板快照时回退旧口径（兼容旧版本软电话）。 */
  function displayRows(call) {
    if (panelCall() || panelConf()) return panelRows(call);
    return parties(call);
  }

  /**
   * 面板全局操作（按 callPanel.state / conferencePanel 决定显示哪些按钮）。
   * 返回 [{id, label, cls, icon, cti, run}]：id 对应通话卡片里的静态按钮，
   * 话务条据此动态建按钮。run(ev) 返回 Promise；cti=true 表示要转圈互斥。
   */
  function panelControls(call) {
    const cp = panelCall();
    const conf = panelConf();
    const st = cp ? cp.state : '';
    const agent = isAgent();
    const owner = !!(conf && conf.owner && norm(conf.owner) === selfNumber());
    const out = [];
    const add = function (bid, label, cls, icon, cti, run) {
      out.push({ id: bid, label: label, cls: cls || '', icon: icon || null, cti: !!cti, run: run });
    };

    // 来电振铃：以**本地 SIP 通话**为准（服务端面板的 state 可能是 ringing /
    // incoming，被咨询方的咨询腿就是 ringing）——桌面版同样按本地振铃显示接听。
    const incomingRinging = !!call && call.state === 'ringing' && call.direction === 'incoming';
    if (incomingRinging || st === 'incoming') {
      add('btnAnswer', '接听', 'green', { img: '/img/phone-out.png' }, false,
        function () { return sdk.answer(); });
      if (call && call.hasVideo) {
        add('btnAnswerVideo', '视频接听', 'green', { img: '/img/video-out.png' }, false,
          function () { return sdk.answer({ video: true }); });
      }
      add('btnReject', '拒接', 'red', { svg: 'stop' }, false,
        function () { return sdk.hangup(); });
      return out;
    }
    // 会议时服务端只给 conferencePanel（callPanel 为 null），不能因此不显示按钮
    if (!cp && !conf) return out;

    const talking = st === 'talking' || st === 'consult-talking' || st === 'three-way' ||
      st === 'monitoring' || st === 'whispering';
    // 拨号盘（发送 DTMF）：已接通的各方话务与会议
    if (talking || conf) add('btnDtmfPad', '', 'icon-btn', null, false, null);
    const callMode = String((cp && cp.callMode) || 'normal').toLowerCase();
    const consulting = isConsultingPanel(cp);
    // 两方已接通、且是**普通话务**（callMode=normal）：咨询 / 转移 / 会议。
    // 被咨询方的面板 callMode 也是 consult，不能出现这组按钮（用户口径）。
    if (agent && !conf && st === 'talking' && callMode === 'normal') {
      add('btnConsult', '咨询', '', { img: '/img/consult.png' }, false,
        function (ev) { App.dialpad.open('consult', ev.clientX, ev.clientY); });
      add('btnTransfer', '转移', '', { img: '/img/transfer.png' }, false,
        function (ev) { App.dialpad.open('transfer', ev.clientX, ev.clientY); });
      add('btnConference', '会议', '', { img: '/img/conference.png' }, true,
        function () { return sdk.addConferenceMembers([]); });
    }
    // 咨询已接通、且本机是**咨询发起方**（面板 owner=本机）：三方 / 咨询转移
    //（结束咨询在各方行上）。非发起方（被咨询方）不显示咨询相关控制。
    if (agent && consulting && st === 'consult-talking') {
      add('btnThreeWay', '三方通话', '', { img: '/img/threeway.png' }, true,
        function () { return sdk.consultThreeWay(); });
      add('btnConsultTransferBtn', '咨询转移', '', { img: '/img/consult-transfer.png' }, true,
        function () { return sdk.consultTransfer(); });
    }
    // 监听：耳语态同一个按钮改为「监听」（stopWhisper 回到静默监听）；
    // 插话/强拦两种状态都保留（用户要求）。
    if (st === 'monitoring' || st === 'whispering') {
      if (st === 'whispering') {
        add('btnWhisper', '监听', '', { img: '/img/monitor.png' }, true,
          function () { return sdk.stopWhisper(); });
      } else {
        add('btnWhisper', '耳语', '', { img: '/img/whisper.png' }, true,
          function () { return sdk.whisper(); });
      }
      add('btnIntervene', '插话', '', { img: '/img/intervene.png' }, true,
        function () { return sdk.intervene(); });
      add('btnIntercept', '强拦', '', { img: '/img/intercept.png' }, true,
        function () { return sdk.intercept(); });
    }
    // 会议（主持人）：添加成员 / 结束会议
    if (conf && owner) {
      add('btnAddMember', '添加成员', '', { img: '/img/add-member.png' }, false,
        function (ev) { App.dialpad.open('conference', ev.clientX, ev.clientY); });
      add('btnFinishConference', '结束会议', 'red', { img: '/img/end-conference.png' }, true,
        function () { return sdk.finishConference(); });
    }
    // 保持（仅两方通话）
    if (st === 'talking' && !conf) {
      const muted = !!(call && call.muted);
      add('btnMute', muted ? '取消保持' : '保持', 'yellow',
        { svg: muted ? 'replay' : 'pause' }, false,
        function () { return muted ? sdk.unhold() : sdk.hold(); });
    }
    // 挂断（按状态换文案/图标）。**咨询发起方不显示挂断**——与桌面一致：
    // 退出咨询走各方行上的「结束咨询/取消咨询」。
    if (!consulting) {
      const label = conf ? '退出会议' : st === 'three-way' ? '退出三方'
        : (st === 'monitoring' || st === 'whispering') ? '结束监听' : '挂断';
      const icon = (conf || st === 'three-way')
        ? { img: '/img/exit-conference.png' } : { svg: 'stop' };
      add('btnHangup', label, 'red', icon, false, function () { return sdk.hangup(); });
    }
    return out;
  }

  App.callui = {
    norm: norm,
    selfNumber: selfNumber,
    isAgent: isAgent,
    isMonitor: isMonitor,
    status: status,
    displayStatus: displayStatus,
    panel: panel,
    panelCall: panelCall,
    panelConf: panelConf,
    panelRows: panelRows,
    displayRows: displayRows,
    panelControls: panelControls,
    memberHas: memberHas,
    twoPartyEstablished: twoPartyEstablished,
    partyName: partyName,
    parties: parties,
    partyText: partyText,
    fillPartyText: fillPartyText,
    iconButton: iconButton,
    partyActions: partyActions,
    partyRowEl: partyRowEl,
    actionEl: actionEl,
    decorateButton: decorateButton,
    runCTI: runCTI,
    isCTIBusy: function () { return ctiBusy; },
    onRender: onRender,
    isPending: isPending,
    attachPending: attachPending,
    agentIcon: agentIcon,
  };
})(window.SipApp);
