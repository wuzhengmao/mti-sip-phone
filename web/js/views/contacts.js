// 通信录视图（REQ-WS-018）：常用/全部两个页签、搜索、增删改、批量常用/删除、
// 导入导出（CSV 与 JSON，走 HTTP 端点避免 WS 的消息上限）。
// 数据一次拉全量（core 侧本来就是内存快照），过滤与排序在前端做；
// 渲染分批（每批 50 行），上千条也不会一次性建满 DOM。
window.SipApp = window.SipApp || {};
window.SipApp.views = window.SipApp.views || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const sdk = store.sdk;

  const BATCH = 50;
  let all = [];       // 服务端按 query 过滤 + 拼音排序后的结果
  let total = 0;      // 通信录总条数（未过滤）
  let shown = 0;
  let tab = 'starred';
  let keyword = '';
  const selected = new Set(); // 按号码存

  /**
   * 过滤与排序**都在软电话侧做**（`contact.list` 的 query）：拼音/音头搜索要用
   * 拼音字典，网页端没有也不该有第二份实现（桌面通信录页与网页共用
   * internal/contactsearch）。这里只剩"常用"页签的本地过滤，顺序沿用服务端给的
   * （按拼音、空名称最前，与桌面一致）。
   */
  function filtered() {
    if (tab !== 'starred') return all;
    return all.filter(function (c) { return c.starred; });
  }

  function row(c) {
    const tr = App.dom.el('tr');

    const cb = App.dom.el('input', { type: 'checkbox' });
    cb.checked = selected.has(c.number);
    cb.addEventListener('change', function () {
      if (cb.checked) selected.add(c.number); else selected.delete(c.number);
      syncSelection();
    });
    const tdCb = App.dom.el('td');
    tdCb.appendChild(cb);
    tr.appendChild(tdCb);

    const star = App.dom.el('td');
    const starMark = App.dom.el('span', { class: 'star' + (c.starred ? ' on' : ''), title: '设为常用' }, '★');
    starMark.addEventListener('click', function () {
      sdk.setContactsStarred([c.number], !c.starred).then(load)
        .catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
    });
    star.appendChild(starMark);
    tr.appendChild(star);

    tr.appendChild(App.dom.el('td', null, c.number));
    tr.appendChild(App.dom.el('td', null, c.name || ''));
    tr.appendChild(App.dom.el('td', null, c.note || ''));

    const acts = App.dom.el('td', { class: 'acts' });
    const call = App.dom.el('button', { class: 'ghost' }, '呼叫');
    call.addEventListener('click', function () {
      sdk.dial(c.number).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
    });
    const vcall = App.dom.el('button', { class: 'ghost' }, '视频');
    vcall.addEventListener('click', function () {
      sdk.dialVideo(c.number).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
    });
    const edit = App.dom.el('button', { class: 'ghost' }, '编辑');
    edit.addEventListener('click', function () { editContact(c); });
    const del = App.dom.el('button', { class: 'red' }, '删除');
    del.addEventListener('click', function () {
      App.ui.confirm('删除联系人', '删除「' + (c.name || c.number) + '」？').then(function (ok) {
        if (!ok) return;
        sdk.deleteContacts([c.number]).then(load)
          .catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
      });
    });
    acts.appendChild(call);
    acts.appendChild(vcall);
    acts.appendChild(edit);
    acts.appendChild(del);
    tr.appendChild(acts);
    return tr;
  }

  function render(reset) {
    const tbody = $('ctList');
    if (reset) { App.dom.clear(tbody); shown = 0; }
    const list = filtered();
    const next = list.slice(shown, shown + BATCH);
    next.forEach(function (c) { tbody.appendChild(row(c)); });
    shown += next.length;
    $('btnCtMore').hidden = shown >= list.length;
    $('ctCount').textContent = keyword
      ? '匹配 ' + all.length + ' 位（共 ' + total + ' 位，当前显示 ' + Math.min(shown, all.length) + ' 位）'
      : '共 ' + total + ' 位联系人（当前显示 ' + list.length + ' 位）';
    syncSelection();
  }

  function syncSelection() {
    const n = selected.size;
    $('btnCtDelete').disabled = n === 0;
    $('btnCtDelete').textContent = n ? '删除选中（' + n + '）' : '删除选中';
    $('btnCtStar').disabled = n === 0;
    $('btnCtUnstar').disabled = n === 0;
  }

  function load() {
    if (!store.isConnected()) return; // 没连上不发请求（连上后 onShow 会重新加载）
    return sdk.listContacts({ query: keyword }).then(function (res) {
      all = res.contacts || [];
      total = res.total !== undefined ? res.total : all.length;
      selected.clear();
      render(true);
    }).catch(function (e) {
      App.ui.errToast('读取通信录失败', e);
    });
  }

  function editContact(prev) {
    App.ui.form(prev ? '编辑联系人' : '新增联系人', [
      { name: 'number', label: '号码', value: prev ? prev.number : '', placeholder: '必填，如 1001' },
      { name: 'name', label: '名称', value: prev ? prev.name : '' },
      { name: 'note', label: '备注', value: prev ? prev.note : '' },
      { name: 'starred', label: '设为常用', type: 'checkbox', value: prev ? prev.starred : false },
    ]).then(function (v) {
      if (!v) return;
      const req = { number: v.number, name: v.name, note: v.note, starred: v.starred };
      if (prev) req.prevNumber = prev.number;
      sdk.saveContact(req).then(function () {
        App.ui.toast('已保存', 'ok');
        load();
      }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
    });
  }

  function tabButtons() {
    $('btnCtTabStar').className = tab === 'starred' ? '' : 'ghost';
    $('btnCtTabAll').className = tab === 'all' ? '' : 'ghost';
  }

  App.views.contacts = {
    mount: function () {
      $('btnCtTabStar').addEventListener('click', function () { tab = 'starred'; tabButtons(); render(true); });
      $('btnCtTabAll').addEventListener('click', function () { tab = 'all'; tabButtons(); render(true); });
      // 搜索交给软电话（拼音字典在那边），防抖 200ms；结果回来再渲染
      $('ctSearch').addEventListener('input', App.util.debounce(function () {
        keyword = this.value.trim();
        load();
      }, 200));

      $('btnCtAdd').addEventListener('click', function () { editContact(null); });
      $('btnCtMore').addEventListener('click', function () { render(false); });
      $('btnCtStar').addEventListener('click', function () {
        sdk.setContactsStarred(Array.from(selected), true).then(function (res) {
          App.ui.toast('已设为常用 ' + res.updated + ' 条', 'ok');
          load();
        }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
      });
      $('btnCtUnstar').addEventListener('click', function () {
        sdk.setContactsStarred(Array.from(selected), false).then(function (res) {
          App.ui.toast('已取消常用 ' + res.updated + ' 条', 'ok');
          load();
        }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
      });
      $('btnCtDelete').addEventListener('click', function () {
        const nums = Array.from(selected);
        App.ui.confirm('删除联系人', '删除选中的 ' + nums.length + ' 位联系人？').then(function (ok) {
          if (!ok) return;
          sdk.deleteContacts(nums).then(function (res) {
            App.ui.toast('已删除 ' + res.deleted + ' 位', 'ok');
            load();
          }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
        });
      });
      $('btnCtClear').addEventListener('click', function () {
        App.ui.confirm('清空通信录', '将删除全部联系人，此操作不可撤销。').then(function (ok) {
          if (!ok) return;
          sdk.clearContacts().then(function (res) {
            App.ui.toast('已清空 ' + res.deleted + ' 位', 'ok');
            load();
          }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
        });
      });

      // 导入导出：CSV/JSON 都走 HTTP 端点（整份文件不受 WS 消息大小限制）
      $('btnExportContacts').addEventListener('click', function () {
        sdk.downloadContacts().then(function (r) {
          App.ui.toast('已导出 ' + r.count + ' 位联系人', 'ok');
        }).catch(function (e) { App.ui.toast('导出失败：' + App.util.errText(e), 'err'); });
      });
      $('btnImportContacts').addEventListener('click', function () {
        sdk.importContactsFromFile().then(function (r) {
          App.ui.toast('导入完成：新增 ' + r.added + '，更新 ' + r.updated + '，跳过 ' + r.skipped, 'ok');
          load();
        }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
      });
      $('btnExportContactsJSON').addEventListener('click', function () {
        sdk.exportContactsJSON().then(function (text) {
          const blob = new Blob([text], { type: 'application/json' });
          const a = document.createElement('a');
          a.href = URL.createObjectURL(blob);
          a.download = 'contacts.json';
          a.click();
          setTimeout(function () { URL.revokeObjectURL(a.href); }, 1000);
        }).catch(function (e) { App.ui.toast('导出失败：' + App.util.errText(e), 'err'); });
      });
      $('btnImportContactsJSON').addEventListener('click', function () {
        const input = document.createElement('input');
        input.type = 'file';
        input.accept = '.json,application/json';
        input.addEventListener('change', function () {
          const file = input.files && input.files[0];
          if (!file) return;
          const reader = new FileReader();
          reader.onload = function () {
            sdk.importContactsJSON(reader.result).then(function (r) {
              App.ui.toast('导入完成：新增 ' + r.added + '，更新 ' + r.updated + '，跳过 ' + r.skipped, 'ok');
              load();
            }).catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
          };
          reader.readAsText(file, 'utf-8');
        });
        input.click();
      });

      // 其它标签/桌面改了通信录 → 跟着刷新
      store.on('contacts', function () { if (!$('view-contacts').hidden) load(); });

      tabButtons();
    },
    onShow: function () { load(); },
    onHide: function () { App.dom.clear($('ctList')); shown = 0; },
  };
})(window.SipApp);
