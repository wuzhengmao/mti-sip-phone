// 网页控制台公共工具（无框架依赖）。
//
// 约定：所有插到页面里的**数据**一律用 textContent / 属性赋值，绝不拼 innerHTML
//（通信录的号码/名称/备注、通话记录里的显示名都来自用户输入或导入的 CSV/JSON，
// 拼字符串等于给自己开 XSS）。图标是代码里的固定 SVG 常量，才允许 innerHTML。
window.SipApp = window.SipApp || {};

(function (App) {
  'use strict';

  /** $ 单元素查询（页面里 id 唯一，够用）。 */
  function $(id) { return document.getElementById(id); }

  /** 创建元素：el('button', {class:'red', onclick:fn}, '文字')。 */
  function el(tag, attrs, text) {
    const node = document.createElement(tag);
    if (attrs) {
      for (const k in attrs) {
        const v = attrs[k];
        if (v === null || v === undefined) continue;
        if (k === 'class') node.className = v;
        else if (k === 'text') node.textContent = v;
        else if (k.slice(0, 2) === 'on') node.addEventListener(k.slice(2), v);
        else if (k === 'dataset') { for (const d in v) node.dataset[d] = v[d]; }
        else node.setAttribute(k, v);
      }
    }
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  /** 清空子节点。 */
  function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }

  /** toast 短提示：kind = '' | 'ok' | 'err'。 */
  function toast(msg, kind) {
    const box = $('toast');
    if (!box) return;
    const item = el('div', { class: 'item ' + (kind || '') }, msg);
    box.appendChild(item);
    setTimeout(function () { item.remove(); }, kind === 'err' ? 6000 : 3000);
  }

  /** 确认框（Promise<boolean>）：文案与按钮沿用桌面口径「确定/取消」。 */
  function confirmDialog(title, message) {
    return new Promise(function (resolve) {
      const acts = el('div', { class: 'modal-acts' });
      const mask = el('div', { class: 'modal-mask' }, null);
      const modal = el('div', { class: 'modal' });
      modal.appendChild(el('h3', null, title));
      if (message) modal.appendChild(el('p', { class: 'hint-text' }, message));
      const okBtn = el('button', { class: 'red', text: '确定' }, '确定');
      const cancelBtn = el('button', { class: 'ghost', text: '取消' }, '取消');
      okBtn.addEventListener('click', function () { close(true); });
      cancelBtn.addEventListener('click', function () { close(false); });
      acts.appendChild(cancelBtn);
      acts.appendChild(okBtn);
      modal.appendChild(acts);
      mask.appendChild(modal);
      document.body.appendChild(mask);
      function close(v) {
        mask.remove();
        resolve(v);
      }
    });
  }

  /**
   * 表单弹窗：fields = [{name, label, value, type, placeholder, options, required}]
   * 返回 Promise<对象|null>（取消为 null）。
   */
  function formDialog(title, fields) {
    return new Promise(function (resolve) {
      const modal = el('div', { class: 'modal' });
      modal.appendChild(el('h3', null, title));
      const inputs = {};
      fields.forEach(function (f) {
        const wrap = el('div', { class: 'field' });
        let input;
        if (f.type === 'checkbox') {
          // 勾选项做成**一个整体**：勾选框和文字在同一个 <label> 里（原来文字在上、
          // 勾选框单独一行，看着像两个字段）
          const lbl = el('label', { class: 'chk' });
          input = el('input', { type: 'checkbox' });
          input.checked = !!f.value;
          lbl.appendChild(input);
          lbl.appendChild(document.createTextNode(' ' + f.label));
          wrap.appendChild(lbl);
        } else {
          wrap.appendChild(el('label', null, f.label));
          if (f.type === 'select') {
            input = el('select');
            (f.options || []).forEach(function (o) {
              const opt = el('option', { value: o.value }, o.label);
              if (o.value === f.value) opt.selected = true;
              input.appendChild(opt);
            });
          } else {
            input = el('input', { type: f.type || 'text', placeholder: f.placeholder || '' });
            input.value = f.value === undefined || f.value === null ? '' : f.value;
          }
          wrap.appendChild(input);
        }
        if (f.hint) wrap.appendChild(el('div', { class: 'hint-text' }, f.hint));
        modal.appendChild(wrap);
        inputs[f.name] = { el: input, def: f };
      });
      const acts = el('div', { class: 'modal-acts' });
      const okBtn = el('button', { text: '确定' }, '确定');
      const cancelBtn = el('button', { class: 'ghost', text: '取消' }, '取消');
      acts.appendChild(cancelBtn);
      acts.appendChild(okBtn);
      modal.appendChild(acts);
      const mask = el('div', { class: 'modal-mask' });
      mask.appendChild(modal);
      document.body.appendChild(mask);

      function close(v) { mask.remove(); resolve(v); }
      cancelBtn.addEventListener('click', function () { close(null); });
      okBtn.addEventListener('click', function () {
        const out = {};
        for (const name in inputs) {
          const it = inputs[name];
          if (it.el.type === 'checkbox') out[name] = it.el.checked;
          else out[name] = it.el.value;
        }
        close(out);
      });
      const first = modal.querySelector('input,select');
      if (first) first.focus();
    });
  }

  /** 时间格式化：毫秒 → YYYY-MM-DD HH:MM:SS（0 = 未发生，返回 '—'）。 */
  function fmtTime(ms) {
    if (!ms) return '—';
    const d = new Date(ms);
    const p = function (n) { return n < 10 ? '0' + n : '' + n; };
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' +
      p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
  }

  /** 秒数 → mm:ss / h:mm:ss。 */
  function fmtDur(sec) {
    if (sec === null || sec === undefined || sec < 0 || !isFinite(sec)) return '—';
    sec = Math.floor(sec);
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    const p = function (n) { return n < 10 ? '0' + n : '' + n; };
    return h > 0 ? h + ':' + p(m) + ':' + p(s) : p(m) + ':' + p(s);
  }

  /** 毫秒差 → 时长文案（同 fmtDur 的秒）。 */
  function fmtDurMs(fromMs, toMs) {
    if (!fromMs || !toMs) return '—';
    return fmtDur((toMs - fromMs) / 1000);
  }

  /** 防抖。 */
  function debounce(fn, ms) {
    let timer = null;
    return function () {
      const args = arguments, self = this;
      clearTimeout(timer);
      timer = setTimeout(function () { fn.apply(self, args); }, ms || 300);
    };
  }

  /** localStorage 封装（带命名空间前缀，读写失败静默）。 */
  const store = {
    get: function (k, def) {
      try {
        const v = localStorage.getItem('mti-sip-phone.' + k);
        return v === null ? def : JSON.parse(v);
      } catch (e) { return def; }
    },
    set: function (k, v) {
      try { localStorage.setItem('mti-sip-phone.' + k, JSON.stringify(v)); } catch (e) { /* 忽略 */ }
    },
  };

  /** 错误文案：优先用 SDK 抛出的 message（服务端错误码已映射成人话）。 */
  function errText(err) {
    if (!err) return '未知错误';
    if (typeof err === 'string') return err;
    return err.message || String(err);
  }

  /**
   * 离线类错误："还没连上软电话" / "连接已断开"。
   *
   * 页面刚打开时各视图的第一次加载请求必然撞上它（WS 握手还没完成），
   * 软电话没运行时更是每次重连都撞一次——这些**不该弹 toast**，否则一刷新
   * 就是右下角一屏"尚未连接软电话"（用户实测）。SDK 自己会重连，连上后
   * 各视图会重新加载（见 app.js 的 connected 钩子）。
   */
  function isOfflineErr(err) {
    const m = errText(err);
    return m.indexOf('尚未连接软电话') >= 0 || m.indexOf('连接已断开') >= 0;
  }

  /** 请求失败的统一处理：离线类只写控制台，其余弹 toast。 */
  function errToast(prefix, err) {
    if (isOfflineErr(err)) {
      console.warn(prefix + '：' + errText(err));
      return;
    }
    toast(prefix + '：' + errText(err), 'err');
  }

  /**
   * 对端展示的统一口径（网页各处都用它，别再各写一套）：
   *   名称优先级：Call-Info 的 display-name（peerName）> 通信录 > SIP 显示名；
   *   命中通信录 → 「号码 名称 - 备注★」（备注**直接跟在名称后面**，不用悬停提示——用户要求）；
   *   未命中     → 「显示名（号码）」，名称与号码相同（很常见）时只显示一次。
   * 与桌面通信录/通话记录的展示口径一致（见 internal/ui/ui_history.go 的 historyDisplay）。
   */
  function peerText(o) {
    const remote = (o && o.remote) || '';
    // peerName = INVITE 的 Call-Info display-name（REQ-ACC-029）：优先于通信录名称；
    // displayName = 通话记录里保存的座席显示名（座席列表直接呼出时记录）：
    // 同样优先于通信录名称（用户要求）。
    const peerName = (o && o.peerName) || '';
    const displayName = (o && o.displayName) || '';
    const name0 = peerName || displayName || (o && o.remoteName) || '';
    const c = o && o.contact;
    if (c) {
      let label = peerName || displayName || c.name || name0;
      const note = c.note || '';
      if (note) label = label ? label + ' - ' + note : note;
      const starred = !!c.starred;
      if (!label || label === remote) return { text: remote || label || '—', starred: starred };
      if (!remote) return { text: label, starred: starred };
      return { text: remote + ' ' + label, starred: starred };
    }
    if (name0 && name0 !== remote) {
      return { text: remote ? name0 + '（' + remote + '）' : name0, starred: false };
    }
    return { text: remote || name0 || '—', starred: false };
  }

  /** 把上面那套展示写进一个元素（文本 + 可选的金色 ★，★ 是只读标记）。 */
  function peerInto(el, o) {
    const d = peerText(o);
    clear(el);
    el.appendChild(document.createTextNode(d.text));
    if (d.starred) el.appendChild(el_(d));
  }
  function el_(d) {
    const star = document.createElement('span');
    star.className = 'star-mark ro';
    star.textContent = '★';
    return star;
  }

  App.dom = { $: $, el: el, clear: clear };
  App.display = { peer: peerText, into: peerInto };
  App.ui = { toast: toast, confirm: confirmDialog, form: formDialog, errToast: errToast };
  App.fmt = { time: fmtTime, dur: fmtDur, durMs: fmtDurMs };
  App.util = { debounce: debounce, store: store, errText: errText, isOfflineErr: isOfflineErr };
})(window.SipApp);
