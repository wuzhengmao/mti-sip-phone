// 通话视图：拨号盘（含按键音）、最近呼叫、通信录快搜、通话控制、视频订阅、
// 更新提示条。由原测试页（web/test.html）的对应功能改造而来。
window.SipApp = window.SipApp || {};
window.SipApp.views = window.SipApp.views || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const sdk = store.sdk;

  let videoSubscribed = false;

  // ---------------- 拨号 ----------------

  function dial(video) {
    const target = $('dialTarget').value.trim();
    if (!target) { App.ui.toast('请输入号码', 'err'); return; }
    const p = video ? sdk.dialVideo(target) : sdk.dial(target);
    p.catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
  }

  /**
   * 拨号输入框的下拉建议（与桌面版同一套逻辑）：
   *   - 输入为空 → 只列"最近呼叫"（去重最多 5 个，来自 system.state.recentDial）；
   *   - 输入非空 → **同时查通信录**（走服务端 contact.list 的 query，拼音/音头/汉字/
   *     号码/备注都能命中，与通信录页的搜索完全一样）+ 最近呼叫里包含该串的；
   *   - 点某一项就把号码填进输入框（不直接拨号）。
   */
  let suggestTimer = null;

  function renderSuggest(query) {
    const box = $('dialSuggest');
    const q = (query || '').trim();
    // 最近呼叫：空查询全列，有查询则按子串过滤
    const recents = (store.state.recentDial || []).filter(function (n) {
      return !q || n.toLowerCase().indexOf(q.toLowerCase()) >= 0;
    }).slice(0, 5);

    const paint = function (contacts) {
      const box2 = $('dialSuggest');
      App.dom.clear(box2);
      if (!recents.length && !contacts.length) { box2.hidden = true; return; }
      const addGroup = function (title) {
        box2.appendChild(App.dom.el('div', { class: 'grp' }, title));
      };
      const addItem = function (number, name, note, starred) {
        const row = App.dom.el('div', { class: 'item' });
        row.appendChild(App.dom.el('span', { class: 'num' }, number));
        if (name) row.appendChild(App.dom.el('span', { class: 'name' }, name));
        if (note) row.appendChild(App.dom.el('span', { class: 'name' }, '- ' + note));
        if (starred) row.appendChild(App.dom.el('span', { class: 'star-mark ro' }, '★'));
        // mousedown 而不是 click：input 的 blur 会把列表先藏掉
        row.addEventListener('mousedown', function (e) {
          e.preventDefault(); // 别让输入框失焦
          $('dialTarget').value = number;
          App.dom.clear(box2);
          box2.hidden = true;
        });
        box2.appendChild(row);
      };
      if (contacts.length) {
        addGroup('通信录');
        contacts.forEach(function (c) { addItem(c.number, c.name, c.note, c.starred); });
      }
      if (recents.length) {
        addGroup('最近呼叫');
        recents.forEach(function (n) { addItem(n, '', '', false); });
      }
      box2.hidden = false;
    };

    if (!q) { paint([]); return; }
    // 通信录匹配走服务端（网页没有拼音字典），防抖 200ms
    if (suggestTimer) clearTimeout(suggestTimer);
    suggestTimer = setTimeout(function () {
      if (!store.isConnected()) { paint([]); return; }
      sdk.listContacts({ query: q }).then(function (res) {
        paint((res.contacts || []).slice(0, 6));
      }).catch(function () { paint([]); });
    }, 200);
    paint([]); // 先只显示最近呼叫，通信录结果到了再补
  }

  function hideSuggest() { $('dialSuggest').hidden = true; }

  /** 状态快照更新后（如拨完号 recentDial 变了）：下拉开着就按当前输入重画一次。 */
  function refreshSuggestOnState() {
    if (!$('dialSuggest').hidden) renderSuggest($('dialTarget').value);
  }

  // ---------------- 按键音（本地合成，不经过软电话） ----------------

  let audioCtx = null;
  // DTMF 双音频率（行 × 列）
  const TONES = {
    '1': [697, 1209], '2': [697, 1336], '3': [697, 1477],
    '4': [770, 1209], '5': [770, 1336], '6': [770, 1477],
    '7': [852, 1209], '8': [852, 1336], '9': [852, 1477],
    '*': [941, 1209], '0': [941, 1336], '#': [941, 1477],
  };

  function playTone(digit) {
    if (!$('chkKeyTone').checked) return;
    const freq = TONES[digit];
    if (!freq) return;
    try {
      audioCtx = audioCtx || new (window.AudioContext || window.webkitAudioContext)();
      const now = audioCtx.currentTime;
      const gain = audioCtx.createGain();
      gain.gain.setValueAtTime(0.12, now);
      gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.12);
      gain.connect(audioCtx.destination);
      freq.forEach(function (f) {
        const osc = audioCtx.createOscillator();
        osc.type = 'sine';
        osc.frequency.value = f;
        osc.connect(gain);
        osc.start(now);
        osc.stop(now + 0.12);
      });
    } catch (e) { /* 浏览器不允许播放时静默 */ }
  }

  const PAD_KEYS = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '0', '#'];

  function buildPad(container, onKey) {
    App.dom.clear(container);
    PAD_KEYS.forEach(function (k) {
      const btn = App.dom.el('button', { class: 'ghost', text: k }, k);
      btn.addEventListener('click', function () { onKey(k); });
      container.appendChild(btn);
    });
  }

  // ---------------- 通信录快搜 ----------------


  // ---------------- 通话控制 ----------------

  function setIconButton(id, on, pathOn, pathOff) {
    App.icons.buttonState($(id), on, pathOn, pathOff);
  }

  function setSliderValue(el, valEl, value) {
    if (!el || document.activeElement === el) return; // 正在拖动的不覆盖
    el.value = String(value);
    valEl.textContent = value + '%';
  }

  function applyMediaButtons(call) {
    if (!call) return;
    const P = App.icons.PATH;
    setIconButton('btnMic', call.micMuted !== true, P.micOn, P.micOff);
    setIconButton('btnSpeaker', call.speakerMuted !== true, P.speakerOn, P.speakerOff);
    setIconButton('btnVideoToggle', !!(call.video && call.video.enabled), P.videoOn, P.videoOff);
    setSliderValue($('volSpeaker'), $('volSpeakerVal'),
      (call.speakerMuted || !call.speakerVolume) ? 0 : call.speakerVolume);
    setSliderValue($('volMic'), $('volMicVal'),
      (call.micMuted || !call.micGain) ? 0 : call.micGain);
  }

  function showCall(call) {
    $('callArea').style.display = 'block';
    $('noCall').style.display = 'none';

    // 号码 名称 - 备注★（与通话条、通话记录同一套；备注直接显示，不做悬停）
    $('callName').textContent = call.direction === 'incoming' ? '来电：' : '去电：';
    const who = App.dom.el('span');
    App.display.into(who, call);
    $('callName').appendChild(who);
    $('callName').removeAttribute('title');

    const ringing = call.state === 'ringing';
    const incoming = call.direction === 'incoming';
    const established = call.state === 'established';

    $('callHint').textContent = established ? '通话中'
      : (ringing ? (incoming ? '对方正在呼叫您' : '正在呼叫对方...') : call.state);

    $('btnAnswer').style.display = (ringing && incoming) ? '' : 'none';
    $('btnAnswerVideo').style.display = (ringing && incoming && call.hasVideo) ? '' : 'none';
    $('btnReject').style.display = (ringing && incoming) ? '' : 'none';
    $('btnMute').style.display = established ? '' : 'none';
    $('btnMute').textContent = call.muted ? '取消保持' : '保持';
    $('mediaRows').style.display = established ? '' : 'none';
    $('btnVideoToggle').style.display = (established && call.hasVideo) ? '' : 'none';
    $('btnHangup').style.display = (ringing && incoming) ? 'none' : '';
    applyMediaButtons(call);
  }

  function resetCall() {
    $('callArea').style.display = 'none';
    $('noCall').style.display = '';
    $('mediaRows').style.display = 'none';
    $('btnMute').textContent = '保持';
    resetStats();
  }

  // ---------------- 视频订阅 ----------------

  function statText(s) {
    return s.width + '×' + s.height + ' · ' + s.fps + 'fps · ' + s.kbps + 'kbps';
  }

  function resetStats() {
    $('remoteStat').textContent = '—';
    $('localStat').textContent = '—';
  }

  function onStats(channel, s) {
    $(channel === 'local' ? 'localStat' : 'remoteStat').textContent = statText(s);
  }

  function refreshTransformButtons() {
    const tx = sdk.videoTransforms() || {};
    const P = App.icons.PATH;
    const r = tx.remote || {}, l = tx.local || {};
    App.icons.buttonState($('btnRotRemote'), (r.rotation || 0) !== 0, P.rotate, P.rotate);
    App.icons.buttonState($('btnMirRemote'), !!r.mirror, P.mirror, P.mirror);
    App.icons.buttonState($('btnRotLocal'), (l.rotation || 0) !== 0, P.rotate, P.rotate);
    App.icons.buttonState($('btnMirLocal'), !!l.mirror, P.mirror, P.mirror);
  }

  function setTransformButtonsEnabled(on) {
    ['btnRotRemote', 'btnMirRemote', 'btnRotLocal', 'btnMirLocal'].forEach(function (id) {
      $(id).disabled = !on;
    });
  }

  function setVideoSubState(on) {
    videoSubscribed = on;
    refreshSubButton();
    $('videoBadge').textContent = on ? '已订阅' : '未订阅';
    $('videoBadge').className = on ? 'badge ok' : 'badge';
  }

  /**
   * 订阅按钮外观。noUI 下**不允许取消订阅**（订阅成功后按钮变成不可点的
   * 「已自动订阅」），但按钮本身**保留**：万一自动订阅没成功（页面是旧版 JS、
   * 或那次订阅失败），用户还能手动点一下补上——比把按钮藏起来更好救。
   */
  function refreshSubButton() {
    const btn = $('btnSubVideo');
    if (store.state.noUI) {
      btn.hidden = false;
      btn.disabled = videoSubscribed;
      btn.textContent = videoSubscribed ? '已自动订阅' : '订阅视频';
      btn.className = videoSubscribed ? 'ghost' : 'green';
      if (videoSubscribed) btn.title = '仅托盘无 UI 版自动订阅，不能取消（要停请关掉配置页的「启用视频通话」）';
      return;
    }
    btn.hidden = false;
    btn.disabled = false;
    btn.textContent = videoSubscribed ? '取消订阅' : '订阅视频';
    btn.className = videoSubscribed ? 'ghost' : 'green';
  }

  /**
   * 仅托盘无 UI 版（store.state.noUI）没有软电话视频窗口，所以：
   *   - 隐藏「本地画面」与「保留软电话窗口」两个勾选（前者固定为开、后者没有窗口可保留），
   *     连同两处窗口相关提示一起去掉；
   *   - 连上就**自动订阅本地 + 远端**（页面打开即有画面，不用手点）；
   *   - **不允许取消订阅**（订阅按钮隐藏）——要停就关掉配置页的「启用视频通话」。
   */
  let autoSubscribed = false;
  function applyNoUIVideoMode() {
    if (!store.state.noUI) return;
    $('vidLocalRow').hidden = true;
    $('vidKeepWindowRow').hidden = true;
    $('vidKeepWindow').checked = false;
    $('vidWindowHint').hidden = true;
    refreshSubButton();
    if (!autoSubscribed && !videoSubscribed) {
      autoSubscribed = true;
      subscribeVideoNow(true); // noUI 固定订本地 + 远端
    }
  }

  function toggleVideoSubscribe() {
    // noUI：只能订阅（不能取消）
    if (store.state.noUI) {
      if (!videoSubscribed) subscribeVideoNow(true);
      return;
    }
    const fail = function (e) { App.ui.toast('视频订阅失败：' + App.util.errText(e), 'err'); };
    if (videoSubscribed) {
      sdk.unsubscribeVideo().then(function () {
        setVideoSubState(false);
        setTransformButtonsEnabled(false);
        resetStats();
        App.ui.toast('已取消订阅', 'ok');
      }).catch(fail);
      return;
    }
    subscribeVideoNow();
  }

  /**
   * 按当前参数订阅视频（手动点按钮与 noUI 自动订阅共用）。
   * forceLocal = true 时无视「本地画面」勾选、一定订上本地（noUI 用）。
   */
  function subscribeVideoNow(forceLocal) {
    const opts = {
      remote: $('remoteVideo'),
      local: (forceLocal || $('vidLocal').checked) ? { el: $('localVideo') } : null,
      onStats: onStats,
    };
    if ($('vidMaxW').value) opts.maxWidth = parseInt($('vidMaxW').value, 10);
    if ($('vidMaxH').value) opts.maxHeight = parseInt($('vidMaxH').value, 10);
    if ($('vidFps').value) opts.fps = parseInt($('vidFps').value, 10);
    if ($('vidQuality').value) opts.quality = parseInt($('vidQuality').value, 10);
    if ($('vidKeepWindow').checked) opts.keepWindow = true;

    sdk.subscribeVideo(opts).then(function () {
      setVideoSubState(true);
      setTransformButtonsEnabled(true);
      refreshTransformButtons();
      resetStats();
      App.ui.toast('视频已订阅', 'ok');
    }).catch(function (e) {
      autoSubscribed = false; // 失败（如视频总开关关着）：下次连接再试
      if (!App.util.isOfflineErr(e)) App.ui.errToast('视频订阅失败', e);
    });
  }

  function wireTransform(channel, rotBtn, mirBtn) {
    rotBtn.addEventListener('click', function () {
      sdk.rotateVideo(channel).then(refreshTransformButtons)
        .catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
    });
    mirBtn.addEventListener('click', function () {
      sdk.mirrorVideo(channel).then(refreshTransformButtons)
        .catch(function (e) { App.ui.toast(App.util.errText(e), 'err'); });
    });
  }

  // 订阅参数下拉（"自动" = 不传，由软电话按元素尺寸定）
  function fillSelect(id, values, unit) {
    const sel = $(id);
    App.dom.clear(sel);
    sel.appendChild(App.dom.el('option', { value: '' }, '自动'));
    values.forEach(function (v) {
      sel.appendChild(App.dom.el('option', { value: String(v) }, v + (unit || '')));
    });
  }

  // ---------------- 更新提示条 ----------------

  function renderUpdate(st) {
    const card = $('cardUpdate');
    if (!st || !st.phase || st.phase === 'idle') {
      card.hidden = true;
      return;
    }
    card.hidden = false;
    const ver = (st.info && st.info.version) || '?';
    const host = st.host ? '（来自 ' + st.host + '）' : '';
    let text = '';
    switch (st.phase) {
      case 'available': text = '发现新版本 v' + ver + host + '，可下载安装。'; break;
      case 'downloading': text = '正在下载 v' + ver + '：' + (st.progress || 0).toFixed(1) + '%'; break;
      case 'ready': text = 'v' + ver + ' 已下载完成' + (st.waitingCall ? '（通话结束后自动重启）' : '') + '，可安装并重启。'; break;
      case 'failed': text = '更新失败：' + (st.error || '未知原因'); break;
      case 'applying': text = '正在安装 v' + ver + ' 并重启...'; break;
      default: text = '更新状态：' + st.phase + '（v' + ver + '）';
    }
    $('updLine').textContent = text;
    $('btnUpdDownload').hidden = st.phase !== 'available';
    $('btnUpdCancel').hidden = st.phase !== 'downloading';
    $('btnUpdApply').hidden = st.phase !== 'ready';
  }

  // ---------------- 挂载 ----------------

  App.views.call = {
    mount: function () {
      const fail = function (e) { App.ui.toast(App.util.errText(e), 'err'); };
      const call = function () { return store.state.call; };
      const id = function () { return store.callID(); };

      $('btnDial').addEventListener('click', function () { dial(false); });
      $('btnDialVideo').addEventListener('click', function () { dial(true); });
      $('dialTarget').addEventListener('keydown', function (e) {
        if (e.key === 'Enter') dial(false);
      });
      // 输入即查（最近呼叫 + 通信录），结果落在输入框下方的下拉里
      $('dialTarget').addEventListener('input', function () { renderSuggest(this.value); });
      $('dialTarget').addEventListener('focus', function () { renderSuggest(this.value); });
      $('dialTarget').addEventListener('keydown', function (e) {
        if (e.key === 'Escape') hideSuggest();
      });
      // 失焦稍后再收，给下拉项的 mousedown 留出点击时间
      $('dialTarget').addEventListener('blur', function () { setTimeout(hideSuggest, 150); });

      // 拨号盘：未接通时填号码（带按键音），接通后发 DTMF（REQ-CALL-006）
      buildPad($('dialPad'), function (k) {
        playTone(k);
        const c = call();
        if (c && c.state === 'established') {
          sdk.sendDTMF(c.id, k).catch(fail);
        } else {
          $('dialTarget').value += k;
        }
      });

      $('btnAnswer').addEventListener('click', function () { sdk.answer(id()).catch(fail); });
      $('btnAnswerVideo').addEventListener('click', function () { sdk.answerVideo(id()).catch(fail); });
      $('btnReject').addEventListener('click', function () { sdk.reject(id()).catch(fail); });
      $('btnHangup').addEventListener('click', function () { sdk.hangup(id()).catch(fail); });
      $('btnMute').addEventListener('click', function () {
        const c = call();
        sdk.mute(id(), !(c && c.muted)).catch(fail);
      });
      $('btnMic').addEventListener('click', function () {
        const c = call();
        sdk.muteMic(id(), !(c && c.micMuted)).catch(fail);
      });
      $('btnSpeaker').addEventListener('click', function () {
        const c = call();
        sdk.muteSpeaker(id(), !(c && c.speakerMuted)).catch(fail);
      });
      $('btnVideoToggle').addEventListener('click', function () {
        const c = call();
        sdk.setVideo(id(), !(c && c.video && c.video.enabled)).catch(fail);
      });

      // 音量滑块：拖动防抖 120ms 后下发，松手立即生效（0% = 禁用）
      const bindSlider = function (el, valEl, send) {
        let timer = null;
        el.addEventListener('input', function () {
          const v = parseInt(el.value, 10);
          valEl.textContent = v + '%';
          if (timer) clearTimeout(timer);
          timer = setTimeout(function () { send(v); }, 120);
        });
        el.addEventListener('change', function () {
          if (timer) { clearTimeout(timer); timer = null; }
          send(parseInt(el.value, 10));
        });
      };
      bindSlider($('volSpeaker'), $('volSpeakerVal'), function (v) {
        if (store.callID()) sdk.setSpeakerVolume(store.callID(), v).catch(fail);
      });
      bindSlider($('volMic'), $('volMicVal'), function (v) {
        if (store.callID()) sdk.setMicGain(store.callID(), v).catch(fail);
      });

      $('chkAutoAnswer').addEventListener('change', function () {
        sdk.setAutoAnswer(this.checked).catch(fail);
      });

      fillSelect('vidMaxW', [160, 320, 480, 640, 960, 1280], '');
      fillSelect('vidMaxH', [120, 240, 360, 480, 720, 960], '');
      fillSelect('vidFps', [5, 10, 15, 25, 30], ' fps');
      fillSelect('vidQuality', [30, 50, 70, 85, 95], '%');
      $('btnSubVideo').addEventListener('click', toggleVideoSubscribe);
      wireTransform('remote', $('btnRotRemote'), $('btnMirRemote'));
      wireTransform('local', $('btnRotLocal'), $('btnMirLocal'));
      setTransformButtonsEnabled(false);

      $('btnUpdDownload').addEventListener('click', function () {
        sdk.startUpdate().then(function (st) { renderUpdate(st); })
          .catch(function (e) { App.ui.errToast('开始下载失败', e); });
      });
      $('btnUpdCancel').addEventListener('click', function () {
        sdk.cancelUpdate().then(function (st) { renderUpdate(st); })
          .catch(function (e) { App.ui.errToast('取消失败', e); });
      });
      $('btnUpdApply').addEventListener('click', function () {
        App.ui.confirm('安装并重启', '软电话会替换自身程序并重启，通话会被挂断。继续？').then(function (ok) {
          if (!ok) return;
          sdk.applyUpdate().then(function () {
            App.ui.toast('正在安装并重启，页面稍后会断开', 'ok');
          }).catch(function (e) { App.ui.toast('安装失败：' + App.util.errText(e), 'err'); });
        });
      });

      // 事件订阅
      store.on('call', function (c) {
        if (c) showCall(c);
        // 兜底：noUI 下视频一旦协商成功就确保订阅还在（例如订阅此前失败过、
        // 或页面是从旧版本 JS 起来后重连的）
        if (c && c.video && c.video.negotiated) applyNoUIVideoMode();
      });
      store.on('callEnded', resetCall);
      store.on('update', renderUpdate);
      store.on('state', function () {
        refreshSuggestOnState();
        $('chkAutoAnswer').checked = !!store.state.autoAnswer;
        renderUpdate(store.state.update);
        refreshTransformButtons();
        applyNoUIVideoMode();
      });
      store.on('connected', applyNoUIVideoMode);
      // 断线时把视频状态打回"未订阅"：否则界面还显示"已订阅"、实际连接已死，
      // 视频不来、按钮却像能用（用户实测过这种错觉）
      store.on('disconnected', function () {
        videoSubscribed = false;
        autoSubscribed = false;
        setVideoSubState(false);
        setTransformButtonsEnabled(false);
        resetStats();
      });
      store.on('autoAnswer', function (on) { $('chkAutoAnswer').checked = on; });
      sdk.on('video.transform', refreshTransformButtons);

      // mount 只绑事件、不拉数据：那时 WS 还没连上，请求必然失败。
      // 最近呼叫来自状态快照，连上后 app.js 会重跑本页。
      refreshTransformButtons();
    },
    onShow: function () {
      refreshSuggestOnState();
    },
    onHide: function () {
      hideSuggest();
    },
  };
})(window.SipApp);
