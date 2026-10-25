// 拨号对话框（呼叫 / 咨询 / 转移 / 会议添加成员共用）+ 迷你 DTMF 拨号盘。
//
// 两个页签：
//   拨号盘   —— 输入号码/名称（带通信录与最近呼叫建议）、号码盘、下方动作按钮；
//   在线座席 —— 仅坐席模式显示，座席列表的操作按钮随座席状态与当前模式变化
//               （与桌面"在线座席页"同一套规则）。
// 点遮罩空白处自动关闭。
window.SipApp = window.SipApp || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const sdk = store.sdk;
  const PAD_KEYS = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '0', '#'];

  let mode = 'call';          // call / consult / transfer / conference
  let agents = [];
  let agentsLoading = false;
  let keyword = '';
  let suggestTimer = null;
  let openAt = null;          // 当前弹层（用于"点外面关闭"判断）
  let outsideFn = null;

  /** 把弹层摆到鼠标点击位置（越界时贴边），没有坐标时居中。 */
  function place(el, x, y) {
    el.hidden = false;
    const pad = 8;
    const r = el.getBoundingClientRect();
    let left = (typeof x === 'number') ? x : (window.innerWidth - r.width) / 2;
    let top = (typeof y === 'number') ? y : (window.innerHeight - r.height) / 2;
    left = Math.min(Math.max(pad, left), Math.max(pad, window.innerWidth - r.width - pad));
    top = Math.min(Math.max(pad, top), Math.max(pad, window.innerHeight - r.height - pad));
    el.style.left = left + 'px';
    el.style.top = top + 'px';
  }

  /** 打开弹层后：点外面 / Esc 关闭；数字与退格键仍可输入（不聚焦输入框也能用）。 */
  function attachOutside(el, onClose, onKey) {
    detachOutside();
    openAt = el;
    outsideFn = function (e) {
      if (!el.contains(e.target) && !(e.target.closest && e.target.closest('.dial-trigger'))) {
        onClose();
      }
    };
    setTimeout(function () { document.addEventListener('mousedown', outsideFn, true); }, 0);
    el.__keyFn = function (e) {
      if (e.key === 'Escape') { onClose(); return; }
      if (onKey) onKey(e);
    };
    document.addEventListener('keydown', el.__keyFn, true);
  }

  function detachOutside() {
    if (outsideFn) document.removeEventListener('mousedown', outsideFn, true);
    if (openAt && openAt.__keyFn) document.removeEventListener('keydown', openAt.__keyFn, true);
    outsideFn = null;
    openAt = null;
  }

  // ---------------- 通信录/最近呼叫建议 ----------------

  function renderSuggest(query) {
    const box = $('dialSuggest');
    const q = String(query || '').trim();
    const recents = (store.state.recentDial || []).filter(function (n) {
      return !q || n.toLowerCase().indexOf(q.toLowerCase()) >= 0;
    }).slice(0, 5);

    const paint = function (contacts) {
      App.dom.clear(box);
      if (!recents.length && !contacts.length) { box.hidden = true; return; }
      const addGroup = function (title) { box.appendChild(App.dom.el('div', { class: 'grp' }, title)); };
      const addItem = function (number, name, note, starred) {
        const row = App.dom.el('div', { class: 'item' });
        row.appendChild(App.dom.el('span', { class: 'num' }, number));
        if (name) row.appendChild(App.dom.el('span', { class: 'name' }, name));
        if (note) row.appendChild(App.dom.el('span', { class: 'name' }, '- ' + note));
        if (starred) row.appendChild(App.dom.el('span', { class: 'star-mark ro' }, '★'));
        row.addEventListener('mousedown', function (e) {
          e.preventDefault();
          $('dialTarget').value = number;
          App.dom.clear(box);
          box.hidden = true;
        });
        box.appendChild(row);
      };
      if (contacts.length) {
        addGroup('通信录');
        contacts.forEach(function (c) { addItem(c.number, c.name, c.note, c.starred); });
      }
      if (recents.length) {
        addGroup('最近呼叫');
        recents.forEach(function (n) { addItem(n, '', '', false); });
      }
      box.hidden = false;
    };

    if (!q) { paint([]); return; }
    if (suggestTimer) clearTimeout(suggestTimer);
    suggestTimer = setTimeout(function () {
      if (!store.isConnected()) { paint([]); return; }
      sdk.listContacts({ query: q }).then(function (res) {
        paint((res.contacts || []).slice(0, 6));
      }).catch(function () { paint([]); });
    }, 200);
    paint([]);
  }

  function hideSuggest() { $('dialSuggest').hidden = true; }

  // ---------------- 动作（按模式） ----------------

  function target() { return String($('dialTarget').value || '').trim(); }

  function close() {
    detachOutside();
    hideSuggest();
    $('dialDialog').hidden = true;
  }

  /** 回车 = 点第一个动作：呼叫类直接发；CTI 类走统一的在途处理。 */
  function pressPrimary() {
    if (mode === 'call') {
      const p = runPrimary();
      if (p && p.then) {
        p.then(function () { close(); })
          .catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
      }
      return;
    }
    const ok = mode === 'consult' ? '已发起咨询'
      : mode === 'transfer' ? '已发起直接转移' : '已邀请加入会议';
    submit(function () { return runPrimary(); }, ok);
  }

  /** 主/次动作返回 Promise（呼叫类不走 CTI 在途；CTI 由 runCTI 统一包）。 */
  function runPrimary(number, name) {
    const no = number || target();
    if (!no) { App.ui.toast('请输入号码', 'err'); return null; }
    switch (mode) {
      case 'consult': return sdk.consult(no);
      case 'transfer': return sdk.transfer(no);
      case 'conference': return sdk.addConferenceMembers([no]);
      default: return sdk.dial(no, { name: name });
    }
  }

  function runSecondary(number, name) {
    const no = number || target();
    if (!no) { App.ui.toast('请输入号码', 'err'); return null; }
    switch (mode) {
      case 'consult': return sdk.consultThreeWay(no);
      case 'transfer': return sdk.consultTransfer(no);
      default: return sdk.dialVideo(no, name);
    }
  }

  /** CTI 动作统一入口：转圈 + 在途互斥，成功后关闭对话框。 */
  function submit(fn, okText) {
    App.callui.runCTI(null, fn, okText).then(function (res) {
      if (res !== null) close();
    });
  }

  function submitWithButton(btn, fn, okText) {
    App.callui.runCTI(btn, fn, okText).then(function (res) {
      if (res !== null) close();
    });
  }

  /** 重建弹框底部的动作按钮（随模式变化；带图标，CTI 动作显示在途转圈）。 */
  function buildActions() {
    const box = $('dialActions');
    App.dom.clear(box);
    const add = function (label, cls, fn, icon, cti, okText) {
      const b = App.dom.el('button', { class: cls || '' }, label);
      if (icon) App.callui.decorateButton(b, icon);
      b.addEventListener('click', function () {
        if (cti) {
          submitWithButton(b, fn, okText);
        } else {
          // 呼叫/视频呼叫：成功后关闭对话框（之前忘了 close，点击后框一直留着）
          const p = fn();
          if (p && p.then) {
            p.then(function () { close(); })
              .catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
          }
        }
      });
      box.appendChild(b);
    };
    if (mode === 'consult') {
      add('咨询', '', function () { return runPrimary(); }, { img: '/img/consult.png' }, true, '已发起咨询');
      add('三方通话', 'green', function () { return runSecondary(); }, { img: '/img/threeway.png' }, true, '已发起三方通话');
    } else if (mode === 'transfer') {
      add('直接转移', '', function () { return runPrimary(); }, { img: '/img/transfer.png' }, true, '已发起直接转移');
      add('咨询转移', 'green', function () { return runSecondary(); }, { img: '/img/consult-transfer.png' }, true, '已发起咨询转移');
    } else if (mode === 'conference') {
      add('加入会议', 'green', function () { return runPrimary(); }, { img: '/img/add-member.png' }, true, '已邀请加入会议');
    } else {
      add('呼叫', '', function () { return runPrimary(); }, { img: '/img/phone-out.png' }, false);
      add('视频呼叫', 'green', function () { return runSecondary(); }, { img: '/img/video-out.png' }, false);
    }
  }

  // ---------------- 在线座席页签 ----------------

  function statusText(s) {
    const map = {
      free: '空闲', dialing: '呼叫中', ringing: '振铃中', talking: '通话中',
      free_offhook: '摘机', busy: '置忙', leave: '离席', not_ready: '示忙',
      miss_phone: '话机未连接', offline: '离线',
    };
    return map[String(s || '').toLowerCase()] || (s || '未知');
  }

  function isFree(a) { return String(a.agentStatus || '').toLowerCase() === 'free'; }

  /** 每个座席行右侧的操作按钮（与桌面在线座席页同一套规则）。 */
  function agentActions(a) {
    const out = [];
    // 图形按钮用桌面 UI 的同一份 PNG（白图标 + 蓝/红底，与软电话面板一致）。
    // CTI 动作走 runCTI（转圈 + 在途互斥），成功后关闭对话框。
    const add = function (icon, title, fn, okText, isCall) {
      const b = App.dom.el('button', { class: 'cti-icon', title: title });
      b.appendChild(App.dom.el('img', { src: '/img/' + icon + '.png', alt: '' }));
      b.addEventListener('click', function () {
        if (isCall) {
          // 普通拨号/视频呼叫不是 MSP_ACTION：直接发，结果用 toast 提示
          const p = fn();
          if (p && p.then) {
            p.then(function () { close(); })
              .catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
          }
          return;
        }
        submitWithButton(b, fn, okText);
      });
      out.push(b);
    };
    const free = isFree(a);
    if (mode === 'consult') {
      if (!free) return out;
      add('consult', '咨询', function () { return sdk.consult(a.agent); }, '已发起咨询');
      add('threeway', '三方通话', function () { return sdk.consultThreeWay(a.agent); }, '已发起三方通话');
      return out;
    }
    if (mode === 'transfer') {
      if (!free) return out;
      add('transfer', '直接转移', function () { return sdk.transfer(a.agent); }, '已发起直接转移');
      add('consult-transfer', '咨询转移', function () { return sdk.consultTransfer(a.agent); }, '已发起咨询转移');
      return out;
    }
    if (mode === 'conference') {
      if (!free) return out;
      add('add-member', '加入会议', function () { return sdk.addConferenceMembers([a.agent]); }, '已邀请加入会议');
      return out;
    }
    // 直接模式：空闲=呼叫/视频；通话中=监听（班长）；振铃中=代接
    const st = String(a.agentStatus || '').toLowerCase();
    if (free) {
      add('phone-out', '呼叫', function () { return sdk.dial(a.agent, { name: a.displayName }); }, '', true);
      add('video-out', '视频呼叫', function () { return sdk.dialVideo(a.agent, a.displayName); }, '', true);
    } else if (st === 'talking' && App.callui.isMonitor()) {
      add('monitor', '监听', function () { return sdk.monitor(a.agent); }, '已发起监听');
    } else if (st === 'ringing') {
      add('takeover', '代接', function () { return sdk.takeover(a.agent); }, '已发起代接');
    }
    return out;
  }

  /** 技能组 tag：只显示第一个，多个时再加一个 more（悬停看其余）。 */
  function skillTags(acd) {
    const line = App.dom.el('div', { class: 'agent-skills' });
    const groups = String(acd || '').split(',').map(function (g) { return g.trim(); })
      .filter(function (g) { return !!g; });
    if (!groups.length) return line;
    line.appendChild(App.dom.el('span', { class: 'tag' }, groups[0]));
    if (groups.length > 1) {
      line.appendChild(App.dom.el('span', { class: 'tag more', title: groups.slice(1).join(', ') }, 'more'));
    }
    return line;
  }

  function renderAgents() {
    const box = $('dialAgentsList');
    App.dom.clear(box);
    if (agentsLoading) {
      box.appendChild(App.dom.el('p', { class: 'state-line' }, '正在获取座席列表…'));
      return;
    }
    const self = App.callui.selfNumber();
    const q = keyword.toLowerCase();
    const list = agents.filter(function (a) {
      if (self && App.callui.norm(a.agent) === self) return false;
      if (!q) return true;
      return String(a.agent || '').toLowerCase().indexOf(q) >= 0 ||
        String(a.displayName || '').toLowerCase().indexOf(q) >= 0 ||
        String(a.acd || '').toLowerCase().indexOf(q) >= 0;
    });
    if (!list.length) {
      box.appendChild(App.dom.el('p', { class: 'state-line' }, '暂无在线座席'));
      return;
    }
    list.forEach(function (a) {
      const row = App.dom.el('div', { class: 'dial-agent-row' });
      row.appendChild(App.callui.agentIcon(a.agentStatus));
      const who = App.dom.el('div', { class: 'who' });
      who.appendChild(App.dom.el('div', { class: 'no' }, a.agent || ''));
      who.appendChild(App.dom.el('div', { class: 'nm' }, a.displayName || ''));
      row.appendChild(who);
      // 右上=状态，右下=技能组（与 UI 在线座席行一致）
      const info = App.dom.el('div', { class: 'agent-info' });
      info.appendChild(App.dom.el('div', { class: 'agent-status' }, statusText(a.agentStatus)));
      info.appendChild(skillTags(a.acd));
      row.appendChild(info);
      const acts = App.dom.el('span', { class: 'acts' });
      agentActions(a).forEach(function (b) { acts.appendChild(b); });
      row.appendChild(acts);
      box.appendChild(row);
    });
  }

  function loadAgents() {
    if (!store.isConnected() || !App.callui.isAgent()) {
      agents = [];
      agentsLoading = false;
      renderAgents();
      return;
    }
    if (agentsLoading) return;
    agentsLoading = true;
    renderAgents();
    sdk.listAgents().then(function (res) {
      agents = res.agents || [];
      agentsLoading = false;
      renderAgents();
    }).catch(function (e) {
      agentsLoading = false;
      renderAgents();
      if (e && e.code === 'cti_busy') { setTimeout(loadAgents, 600); return; }
      if (!App.util.isOfflineErr(e)) App.ui.toast('获取在线座席失败：' + App.util.errText(e), 'err');
    });
  }

  // ---------------- 页签 ----------------

  function setTab(name) {
    const agentTab = $('dialTabAgents');
    if (name === 'agents' && agentTab.hidden) name = 'pad';
    $('dialTabPad').classList.toggle('active', name === 'pad');
    agentTab.classList.toggle('active', name === 'agents');
    $('dialPanePad').hidden = name !== 'pad';
    $('dialPaneAgents').hidden = name !== 'agents';
    if (name === 'agents') loadAgents();
    else hideSuggest();
  }

  /** 对话框内的键盘输入：不聚焦输入框也能直接输入数字/退格。
   *  焦点在输入框里时交给浏览器原生处理（避免重复追加）。 */
  function dialogKey(e) {
    const t = e.target;
    if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT')) return;
    if (e.key === 'Backspace') {
      e.preventDefault();
      const v = $('dialTarget').value;
      $('dialTarget').value = v.slice(0, -1);
      return;
    }
    if (e.key === 'Enter') { e.preventDefault(); pressPrimary(); return; }
    if (/^[0-9*#]$/.test(e.key)) {
      e.preventDefault();
      $('dialTarget').value += e.key;
    }
  }

  /** 打开拨号对话框。mode: call / consult / transfer / conference。
   *  x/y 为鼠标点击位置（弹层跟随，不居中、无遮罩）。 */
  function open(m, x, y) {
    mode = m || 'call';
    $('dialTarget').value = '';
    hideSuggest();
    buildActions();
    const agentTab = $('dialTabAgents');
    agentTab.hidden = !App.callui.isAgent();
    $('dialAgentsSearch').value = '';
    keyword = '';
    agents = [];
    closeDtmf();
    setTab('pad');
    place($('dialDialog'), x, y);
    attachOutside($('dialDialog'), close, dialogKey);
  }

  // ---------------- 迷你 DTMF 拨号盘 ----------------

  function openDtmf(x, y) {
    if (!store.callID()) return;
    close();
    place($('dtmfDialog'), x, y);
    attachOutside($('dtmfDialog'), closeDtmf, null);
  }

  function closeDtmf() {
    detachOutside();
    $('dtmfDialog').hidden = true;
  }

  // ---------------- 挂载 ----------------

  function bind() {
    $('dialClose').innerHTML = App.icons.svg(App.icons.PATH.close, '#334155', 14);
    $('dialClose').addEventListener('click', close);

    $('dialTabPad').addEventListener('click', function () { setTab('pad'); });
    $('dialTabAgents').addEventListener('click', function () { setTab('agents'); });
    $('dialAgentsRefresh').addEventListener('click', loadAgents);
    $('dialAgentsSearch').addEventListener('input', App.util.debounce(function () {
      keyword = this.value.trim();
      renderAgents();
    }, 200));

    // 号码盘：只填号码（对话框里不需要按键音；接通后发 DTMF 用迷你盘）
    PAD_KEYS.forEach(function (k) {
      const b = App.dom.el('button', { class: 'ghost' }, k);
      b.addEventListener('click', function () { $('dialTarget').value += k; });
      $('dialPad').appendChild(b);

      const d = App.dom.el('button', { class: 'ghost' }, k);
      d.addEventListener('click', function () {
        if (store.callID()) sdk.sendDTMF(store.callID(), k).catch(function (e) {
          App.ui.toast(App.util.errText(e), 'err');
        });
      });
      $('dtmfPad').appendChild(d);
    });

    $('dialTarget').addEventListener('input', function () { renderSuggest(this.value); });
    $('dialTarget').addEventListener('focus', function () { renderSuggest(this.value); });
    $('dialTarget').addEventListener('keydown', function (e) {
      if (e.key === 'Enter') { e.preventDefault(); pressPrimary(); }
      if (e.key === 'Escape') { hideSuggest(); close(); }
    });
    $('dialTarget').addEventListener('blur', function () { setTimeout(hideSuggest, 150); });
  }

  App.dialpad = { bind: bind, open: open, close: close, openDtmf: openDtmf, closeDtmf: closeDtmf, mode: function () { return mode; } };
})(window.SipApp);
