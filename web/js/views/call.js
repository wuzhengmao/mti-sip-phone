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

  /** 按话务状态显示/隐藏全局操作按钮（规则与桌面通话面板一致）。 */
  function renderActions(call) {
    const st = call.cti || {};
    const ringing = call.state === 'ringing';
    const incoming = call.direction === 'incoming';
    const established = call.state === 'established';
    const talk = st.talkState || 'twoParty';
    const agent = App.callui.isAgent();
    const two = App.callui.twoPartyEstablished(call);
    const consultTalking = talk === 'consult' && st.consultPhase === 'talking';
    const monitor = talk === 'monitor';
    const owner = st.owner && App.callui.norm(st.owner) === App.callui.selfNumber();

    // 来电振铃：接听/视频接听/拒接；**不显示挂断**（挂断与拒接同义）
    show('btnAnswer', ringing && incoming);
    show('btnAnswerVideo', ringing && incoming && call.hasVideo);
    show('btnReject', ringing && incoming);
    show('btnHangup', !(ringing && incoming));
    $('btnHangup').textContent = talk === 'conference' ? '退出会议'
      : talk === 'threeWay' ? '退出三方'
        : talk === 'monitor' ? '结束监听' : '挂断';
    $('btnHangup').title = talk === 'conference' ? '退出会议'
      : talk === 'threeWay' ? '退出三方通话'
        : talk === 'monitor' ? '结束监听' : '挂断';
    // 图标随动作切换（与 UI 一致）：退出多方用"离开话务"图标，挂断/结束监听用方块
    App.callui.decorateButton($('btnHangup'),
      (talk === 'conference' || talk === 'threeWay')
        ? { img: '/img/exit-conference.png' } : { svg: 'stop' });
    show('btnMute', established && talk === 'twoParty');
    $('btnMute').textContent = call.muted ? '取消保持' : '保持';
    // textContent 会清掉子节点：图标必须在这之后补（否则保持按钮没图标）
    App.callui.decorateButton($('btnMute'), { svg: call.muted ? 'replay' : 'pause' });
    show('btnVideoToggle', established && call.hasVideo);
    show('btnDtmfPad', established);

    // 两方已接通（坐席模式）：咨询 / 转移 / 会议（添加成员）
    show('btnConsult', agent && two);
    show('btnTransfer', agent && two);
    show('btnConference', agent && two);
    // 咨询已接通：三方 / 咨询转移
    show('btnThreeWay', agent && consultTalking);
    show('btnConsultTransferBtn', agent && consultTalking);
    // 监听
    show('btnWhisper', monitor && st.monitorPhase !== 'whisper');
    show('btnIntervene', monitor && st.monitorPhase !== 'whisper');
    show('btnIntercept', monitor && st.monitorPhase !== 'whisper');
    // 会议（owner）
    show('btnAddMember', talk === 'conference' && !!owner);
    show('btnFinishConference', talk === 'conference' && !!owner);
  }

  function renderParties(call) {
    const box = $('callParties');
    App.dom.clear(box);
    // 每方一行（会议行带左侧的移除/主持人图标），与话务条悬停成员列表同一套 DOM
    App.callui.parties(call).forEach(function (p) {
      box.appendChild(App.callui.partyRowEl(p));
    });
  }

  function showCall(call) {
    $('callArea').style.display = 'block';
    $('noCall').style.display = 'none';
    $('idleDialRow').style.display = 'none';

    $('callStatus').textContent = App.callui.status(call);
    renderParties(call);
    renderActions(call);

    const established = call.state === 'established';
    $('mediaRows').style.display = established ? '' : 'none';
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

  // ---------------- 挂载 ----------------

  App.views.call = {
    mount: function () {
      const fail = function (e) { App.ui.toast(App.util.errText(e), 'err'); };
      const call = function () { return store.state.call; };
      const id = function () { return store.callID(); };

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
        App.callui.runCTI($('btnConference'), function () { return sdk.addConferenceMembers([]); });
      });
      $('btnAddMember').addEventListener('click', function (e) {
        App.dialpad.open('conference', e.clientX, e.clientY);
      });

      // MSP_ACTION 类按钮：统一 runCTI（转圈 + 在途期间不可再点）
      const ctiBtn = function (id, fn) {
        $(id).addEventListener('click', function () { App.callui.runCTI($(id), fn); });
      };
      // 咨询接通后的三方/咨询转移（不带 target：沿用当前咨询）
      ctiBtn('btnThreeWay', function () { return sdk.consultThreeWay(); });
      ctiBtn('btnConsultTransferBtn', function () { return sdk.consultTransfer(); });
      // 监听
      ctiBtn('btnWhisper', function () { return sdk.whisper(); });
      ctiBtn('btnIntervene', function () { return sdk.intervene(); });
      ctiBtn('btnIntercept', function () { return sdk.intercept(); });
      // 会议
      ctiBtn('btnFinishConference', function () { return sdk.finishConference(); });

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

      store.on('call', function (c) {
        if (c) showCall(c);
        if (c && c.video && c.video.negotiated) applyNoUIVideoMode();
      });
      store.on('callEnded', resetCall);
      store.on('update', renderUpdate);
      store.on('state', function () {
        $('chkAutoAnswer').checked = !!store.state.autoAnswer;
        renderUpdate(store.state.update);
        refreshTransformButtons();
        applyNoUIVideoMode();
        if (store.state.call) showCall(store.state.call);
      });
      store.on('agent', function () {
        if (store.state.call) renderActions(store.state.call);
      });
      store.on('connected', applyNoUIVideoMode);
      store.on('disconnected', function () {
        videoSubscribed = false;
        autoSubscribed = false;
        setVideoSubState(false);
        setTransformButtonsEnabled(false);
        resetStats();
      });
      store.on('autoAnswer', function (on) { $('chkAutoAnswer').checked = on; });
      sdk.on('video.transform', refreshTransformButtons);

      refreshTransformButtons();
      resetCall();
    },
    onShow: function () {
      if (store.state.call) showCall(store.state.call);
    },
    onHide: function () { App.dialpad.close(); },
  };
})(window.SipApp);
