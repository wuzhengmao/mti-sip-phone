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
    const ag = store.state.agent || {};
    return !!ag.online && ag.mode !== 'none';
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
    if (call.state === 'ringing') return call.direction === 'incoming' ? '振铃中' : '呼叫中';
    if (call.state === 'established') return '通话中';
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

  /** 按钮进入"在途"样式：图标换成旋转圆圈、禁用点击；返回恢复函数。 */
  function spinButton(btn) {
    if (!btn) return function () {};
    const old = btn.innerHTML;
    const wasDisabled = btn.disabled;
    btn.disabled = true;
    btn.classList.add('cti-busy');
    const ico = btn.querySelector('.btn-ico');
    if (ico) ico.innerHTML = '<span class="cti-spin"></span>';
    else btn.innerHTML = '<span class="cti-spin"></span>';
    return function () {
      btn.innerHTML = old;
      btn.disabled = wasDisabled;
      btn.classList.remove('cti-busy');
    };
  }

  /**
   * 执行一条 MSP_ACTION：同一时间只允许一条在途（与软电话一致）。
   * 在途期间按钮转圈并禁用，其它动作按钮点击只提示「上一个操作尚未完成」。
   * 返回 Promise（成功 resolve 结果，失败 resolve null，错误已弹提示）。
   */
  function runCTI(btn, fn, okText) {
    if (ctiBusy) {
      App.ui.toast('上一个操作尚未完成');
      return Promise.resolve(null);
    }
    ctiBusy = true;
    const restore = spinButton(btn);
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
      ctiBusy = false;
      restore();
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

  App.callui = {
    norm: norm,
    selfNumber: selfNumber,
    isAgent: isAgent,
    isMonitor: isMonitor,
    status: status,
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
    agentIcon: agentIcon,
  };
})(window.SipApp);
