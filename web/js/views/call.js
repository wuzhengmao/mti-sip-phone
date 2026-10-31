// 通话视图：多方通话面板（咨询/三方/监听/会议）、拨号对话框入口、视频订阅、
// 更新提示条。
//
// 通话控制卡片第一行 = 状态 + 计时；第二行起 = 通话各方（每方按钮在该行右侧）；
// 最后一行 = 全局操作（拨号盘/咨询/转移/会议/保持/挂断…）。按钮显隐参考桌面
// 通话面板（REQ-ACC-026）。
window.SipApp = window.SipApp || {};
window.SipApp.views = window.SipApp.views || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const sdk = store.sdk;

  // 实际订阅状态（只用于按钮/统计的显示；订阅的跨重连/跨热更新恢复由引导器门面负责，
  // 见 docs/sdk-bootstrap.md 与 web/sdk/sip-phone-bootstrap.js 的 restoreVideo）。
  let videoSubscribed = false;
  let timerHandle = null;
  let establishedAt = 0;

  // ---------------- 计时 ----------------

  function stopTimer() {
    if (timerHandle) { clearInterval(timerHandle); timerHandle = null; }
    $('callTimer').textContent = '';
  }

  function startTimer() {
    if (timerHandle) return; // 已在计时
    establishedAt = Date.now();
    const tick = function () {
      $('callTimer').textContent = App.fmt.dur((Date.now() - establishedAt) / 1000);
    };
    tick();
    timerHandle = setInterval(tick, 1000);
  }

  // ---------------- 通话控制 ----------------

  function setIconButton(id, on, pathOn, pathOff) {
    App.icons.buttonState($(id), on, pathOn, pathOff);
  }

  function setSliderValue(el, valEl, value) {
    if (!el || document.activeElement === el) return;
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

  function show(id, on) { $(id).style.display = on ? '' : 'none'; }

  /** 通话卡片里的全部操作按钮（显隐由面板事件的 state 决定）。 */
  const ACTION_BUTTONS = [
    'btnDtmfPad', 'btnConsult', 'btnTransfer', 'btnConference', 'btnThreeWay',
    'btnConsultTransferBtn', 'btnWhisper', 'btnIntervene', 'btnIntercept',
    'btnAddMember', 'btnFinishConference', 'btnAnswer', 'btnAnswerVideo',
    'btnReject', 'btnMute', 'btnHangup',
  ];

  /** 按面板快照（call.panel）决定显示哪些操作按钮、以及挂断/保持的文案与图标。 */
  function renderActions(call) {
    const byId = {};
    App.callui.panelControls(call).forEach(function (sp) { byId[sp.id] = sp; });
    ACTION_BUTTONS.forEach(function (id) { show(id, !!byId[id]); });
    Object.keys(byId).forEach(function (id) {
      const sp = byId[id];
      const btn = $(id);
      if (!btn) return;
      if (sp.label) {
        btn.textContent = sp.label; // 会清掉子节点：图标必须在设置文字之后再补
        btn.title = sp.label;
      }
      if (id !== 'btnDtmfPad' && sp.icon) App.callui.decorateButton(btn, sp.icon);
      // 该动作仍在途（面板在点击后重画过）：把转圈贴回这个静态按钮
      App.callui.attachPending(id, btn);
    });
  }

  function renderParties(call) {
    const box = $('callParties');
    App.dom.clear(box);
    // 面板行（callList / 会议成员）优先，与话务条同一套 DOM
    App.callui.displayRows(call).forEach(function (p) {
      box.appendChild(App.callui.partyRowEl(p));
    });
  }

  function showCall(call) {
    $('callArea').style.display = 'block';
    $('noCall').style.display = 'none';
    $('idleDialRow').style.display = 'none';

    // 标题优先用面板快照的 title（拨号中/振铃中/咨询呼叫中…）
    $('callStatus').textContent = App.callui.displayStatus(call);
    renderParties(call);
    renderActions(call);

    const established = call.state === 'established';
    $('mediaRows').style.display = established ? '' : 'none';
    show('btnVideoToggle', established && call.hasVideo);
    if (established) startTimer(); else stopTimer();
    applyMediaButtons(call);
  }

  function resetCall() {
    $('callArea').style.display = 'none';
    $('noCall').style.display = '';
    $('idleDialRow').style.display = '';
    $('mediaRows').style.display = 'none';
    $('callStatus').textContent = '';
    App.dom.clear($('callParties'));
    stopTimer();
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
    // SDK 经引导器异步加载，mount 早期可能还没就绪：没拿到实例就按默认（未旋转/未镜像）显示
    const tx = (sdk.videoTransforms && sdk.videoTransforms()) || {};
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
      subscribeVideoNow(true);
    }
  }

  function toggleVideoSubscribe() {
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
      autoSubscribed = false;
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

  // ---------------- 通话结束提示 ----------------

  /** 通话结束：**SIP 呼叫失败**（call.failed）把原因提示出来，与桌面版
   *  showCallFailed 一致（如「486 Busy Here」）；正常挂断不提示。
   *  CTI 接管的话务（收到过 CALL_RINGING，事件带 ctiManaged）不显示 SIP 原因——
   *  CTI 随后会下发 CALL_FAILED，由 cti.failed 给出统一的中文消息，
   *  否则「603 …」和「呼叫未接通」会同时出现（用户实测）。 */
  function noticeCallEnded(info) {
    if (!info || info.event !== 'call.failed') return;
    if (info.data && info.data.ctiManaged) return;
    const reason = (info.data && info.data.reason) || '';
    App.ui.toast('呼叫失败：' + (reason || '未知原因'), 'err');
  }

  // ---------------- 挂载 ----------------

  App.views.call = {
    mount: function () {
      const fail = function (e) { App.ui.toast(App.util.errText(e), 'err'); };
      const call = function () { return store.state.call; };

      // 拨号入口：弹出拨号对话框（拨号盘 / 在线座席），位置跟随鼠标点击处
      $('btnOpenDial').addEventListener('click', function (e) {
        App.dialpad.open('call', e.clientX, e.clientY);
      });
      $('btnDtmfPad').addEventListener('click', function (e) {
        App.dialpad.openDtmf(e.clientX, e.clientY);
      });

      // 两方通话入口：咨询/转移/会议（打开对话框，按钮按模式调整）
      $('btnConsult').addEventListener('click', function (e) {
        App.dialpad.open('consult', e.clientX, e.clientY);
      });
      $('btnTransfer').addEventListener('click', function (e) {
        App.dialpad.open('transfer', e.clientX, e.clientY);
      });
      // 会议：与桌面 UI 一致——直接把当前两方通话转成会议（空成员），
      // 进入会议模式后才显示「添加成员」（点它才打开拨号对话框）。
      $('btnConference').addEventListener('click', function () {
        App.callui.runCTI($('btnConference'), function () { return sdk.addConferenceMembers([]); }, undefined, 'btnConference');
      });
      $('btnAddMember').addEventListener('click', function (e) {
        App.dialpad.open('conference', e.clientX, e.clientY);
      });

      // MSP_ACTION 类按钮：统一 runCTI（转圈 + 在途期间不可再点）
      const ctiBtn = function (id, fn) {
        $(id).addEventListener('click', function () {
          App.callui.runCTI($(id), fn, undefined, id);
        });
      };
      // 咨询接通后的三方/咨询转移（不带 target：沿用当前咨询）
      ctiBtn('btnThreeWay', function () { return sdk.consultThreeWay(); });
      ctiBtn('btnConsultTransferBtn', function () { return sdk.consultTransfer(); });
      // 监听
      // 耳语态同一个按钮是「监听」：调用 stopWhisper 回到静默监听
      ctiBtn('btnWhisper', function () {
        const cp = App.callui.panelCall();
        if (cp && cp.state === 'whispering') return sdk.stopWhisper();
        return sdk.whisper();
      });
      ctiBtn('btnIntervene', function () { return sdk.intervene(); });
      ctiBtn('btnIntercept', function () { return sdk.intercept(); });
      // 会议
      ctiBtn('btnFinishConference', function () { return sdk.finishConference(); });

      $('btnAnswer').addEventListener('click', function () { sdk.answer().catch(fail); });
      $('btnAnswerVideo').addEventListener('click', function () { sdk.answer({ video: true }).catch(fail); });
      $('btnReject').addEventListener('click', function () { sdk.hangup().catch(fail); });
      $('btnHangup').addEventListener('click', function () { sdk.hangup().catch(fail); });
      $('btnMute').addEventListener('click', function () {
        const c = call();
        (c && c.muted ? sdk.unhold() : sdk.hold()).catch(fail);
      });
      $('btnMic').addEventListener('click', function () {
        const c = call();
        sdk.muteMic(!(c && c.micMuted)).catch(fail);
      });
      $('btnSpeaker').addEventListener('click', function () {
        const c = call();
        sdk.muteSpeaker(!(c && c.speakerMuted)).catch(fail);
      });
      $('btnVideoToggle').addEventListener('click', function () {
        const c = call();
        sdk.setVideo(!(c && c.video && c.video.enabled)).catch(fail);
      });

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
        sdk.setSpeakerVolume(v).catch(fail);
      });
      bindSlider($('volMic'), $('volMicVal'), function (v) {
        sdk.setMicGain(v).catch(fail);
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

      // 图形按钮（拨号盘）与文字按钮的图标（与 UI 同一套图标、颜色随文字=白色）
      $('btnDtmfPad').innerHTML = App.icons.svg(App.icons.PATH.dialpad, '#ffffff', 16);
      const dec = App.callui.decorateButton;
      dec($('btnOpenDial'), { img: '/img/phone-out.png' });
      dec($('btnConsult'), { img: '/img/consult.png' });
      dec($('btnTransfer'), { img: '/img/transfer.png' });
      dec($('btnConference'), { img: '/img/conference.png' });
      dec($('btnThreeWay'), { img: '/img/threeway.png' });
      dec($('btnConsultTransferBtn'), { img: '/img/consult-transfer.png' });
      dec($('btnWhisper'), { img: '/img/whisper.png' });
      dec($('btnIntervene'), { img: '/img/intervene.png' });
      dec($('btnIntercept'), { img: '/img/intercept.png' });
      dec($('btnAddMember'), { img: '/img/add-member.png' });
      dec($('btnFinishConference'), { img: '/img/end-conference.png' });
      dec($('btnAnswer'), { img: '/img/phone-out.png' });
      dec($('btnAnswerVideo'), { img: '/img/video-out.png' });
      dec($('btnReject'), { svg: 'stop' });
      dec($('btnHangup'), { svg: 'stop' });
      dec($('btnMute'), { svg: 'pause' });

      // CTI 指令结束：按钮按当前面板重新渲染（在途期间面板可能已变化，
      // 不能恢复点击前的文案/图标）
      App.callui.onRender(function () {
        if (store.state.call) renderActions(store.state.call);
      });

      store.on('call', function (c) {
        // null = 没有进行中的通话（含重连快照里只剩已结束通话）：收起卡片，
        // 否则上一通电话的"呼叫中"会一直挂在那里
        if (c) showCall(c); else resetCall();
        if (c && c.video && c.video.negotiated) applyNoUIVideoMode();
      });
      store.on('callEnded', function (info) {
        // 视频订阅是**连接级**的（与有没有通话无关）：通话结束**不**清除订阅，
        // 下一通视频电话的画面会自动进来（用户口径：打完一通视频电话不应变成未订阅）。
        resetCall();
        noticeCallEnded(info);
      });
      // 面板快照变化（call.panel）：标题/各方/按钮都以它为准
      store.on('panel', function () {
        if (store.state.call) showCall(store.state.call); else resetCall();
      });
      store.on('update', renderUpdate);
      store.on('state', function () {
        $('chkAutoAnswer').checked = !!store.state.autoAnswer;
        renderUpdate(store.state.update);
        refreshTransformButtons();
        applyNoUIVideoMode();
        if (store.state.call) showCall(store.state.call); else resetCall();
      });
      store.on('agent', function () {
        if (store.state.call) renderActions(store.state.call);
      });
      // 重连（含 SDK 热更新换了新实例）后：**订阅本身由引导器门面自动恢复**，
      // 这里只把按钮/统计同步成实际状态（门面暴露 videoSubscribed）。
      store.on('connected', function () {
        applyNoUIVideoMode();
        if (sdk.videoSubscribed) {
          setVideoSubState(true);
          setTransformButtonsEnabled(true);
          refreshTransformButtons();
          resetStats();
        }
      });
      store.on('disconnected', function () {
        // 断线时按钮先回到"未订阅"；重连后由门面恢复订阅、connected 里再同步回来
        videoSubscribed = false;
        autoSubscribed = false;
        setVideoSubState(false);
        setTransformButtonsEnabled(false);
        resetStats();
      });
      store.on('autoAnswer', function (on) { $('chkAutoAnswer').checked = on; });
      store.on('transform', refreshTransformButtons);
      // CTI 话务失败（协议 §16.2/§16.3）：CALL_FAILED 的 refuse/cancel/takeover
      // 与 CONSULT_FAILED 的 refuse/cancel——软电话已把原因翻成中文，直接提示。
      store.on('cti.failed', function (data) {
        if (!data || !data.message) return;
        App.ui.toast(data.message, data.kind === 'call' ? 'err' : '');
      });

      refreshTransformButtons();
      resetCall();
    },
    onShow: function () {
      if (store.state.call) showCall(store.state.call);
    },
    onHide: function () { App.dialpad.close(); },
  };
})(window.SipApp);
