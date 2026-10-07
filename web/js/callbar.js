// 常驻通话条：来电/通话时出现在**任何视图**上方，保证切到"通话记录/通信录/配置"
// 页面时仍然能接听、挂断（无 UI 版没有桌面通话面板兜底）。
//
// 通话状态口径（core.CallState）：ringing（呼入与呼出振铃共用）/ established。
// 是呼入还是呼出看 direction；计时从"通话条第一次看到 established"开始。
window.SipApp = window.SipApp || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const sdk = store.sdk;

  let timerHandle = null;
  let establishedAt = 0;

  function stopTimer() {
    if (timerHandle) { clearInterval(timerHandle); timerHandle = null; }
  }

  function startTimer() {
    stopTimer();
    establishedAt = Date.now();
    const tick = function () {
      $('barTimer').textContent = App.fmt.dur((Date.now() - establishedAt) / 1000);
    };
    tick();
    timerHandle = setInterval(tick, 1000);
  }

  /** 按当前通话刷新通话条（call = null 时隐藏）。 */
  function render(call) {
    const bar = $('callBar');
    if (!call) {
      bar.hidden = true;
      stopTimer();
      return;
    }
    bar.hidden = false;

    const incoming = call.direction === 'incoming';
    const ringing = call.state === 'ringing';
    const established = call.state === 'established';
    bar.classList.toggle('ringing', ringing && incoming);

    // 与通话记录/通话卡片同一套展示：号码 名称 - 备注★
    App.display.into($('barName'), call);
    $('barState').textContent = ringing ? (incoming ? '来电' : '呼叫中') : (established ? '通话中' : call.state);

    // 来电振铃：接听/视频接听/拒接；呼出振铃：只能挂断（=取消）
    $('barAnswer').hidden = !(ringing && incoming);
    $('barAnswerVideo').hidden = !(ringing && incoming && call.hasVideo);
    $('barReject').hidden = !(ringing && incoming);
    $('barHangup').hidden = false;
    $('barMute').hidden = !established;
    $('barMute').textContent = call.muted ? '取消保持' : '保持';

    if (established) {
      if (!timerHandle) startTimer();
    } else {
      stopTimer();
      $('barTimer').textContent = '';
    }
  }

  function bind() {
    const id = function () { return store.callID(); };
    const fail = function (e) { App.ui.toast(App.util.errText(e), 'err'); };

    $('barAnswer').addEventListener('click', function () { sdk.answer(id()).catch(fail); });
    $('barAnswerVideo').addEventListener('click', function () { sdk.answerVideo(id()).catch(fail); });
    $('barReject').addEventListener('click', function () { sdk.reject(id(), 486).catch(fail); });
    $('barHangup').addEventListener('click', function () { sdk.hangup(id()).catch(fail); });
    $('barMute').addEventListener('click', function () {
      const call = store.state.call;
      sdk.mute(id(), !(call && call.muted)).catch(fail);
    });

    store.on('call', render);
  }

  App.callbar = { bind: bind, render: render };
})(window.SipApp);
