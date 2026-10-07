// 配置视图（REQ-WS-016 ~ REQ-WS-019）：SIP 账户 / 音频设备与预处理 / 视频 /
// 日志 / 应用（自启、WS 端口、重启）/ 自动更新，外加"诊断"折叠区
//（版本信息、事件日志、sip:// 链接生成器）。
//
// 一处口径：**密码只写不读**——服务端只回 passwordSet 布尔值，密码框留空表示
// "不修改"，要清空则勾「清除已保存的密码」。
window.SipApp = window.SipApp || {};
window.SipApp.views = window.SipApp.views || {};

(function (App) {
  'use strict';

  const $ = App.dom.$;
  const store = App.store;
  const sdk = store.sdk;

  let cfg = null;      // 最近一次 system.getConfig 的快照
  let devices = { mics: [], speakers: [], ready: false, preprocessSupported: false };
  let cameras = { cameras: [], cameraSupported: false };

  const TRANSPORTS = [
    { value: 'udp', label: 'udp（默认）' },
    { value: 'tcp', label: 'tcp' },
    { value: 'tls', label: 'tls（信令加密，SIPS）' },
  ];
  const SRTP = [
    { value: 'off', label: '关闭' },
    { value: 'supported', label: '支持（默认）' },
    { value: 'preferred', label: '建议' },
    { value: 'required', label: '必要' },
  ];
  const LEVELS = [
    { value: 'debug', label: 'debug' },
    { value: 'info', label: 'info' },
    { value: 'warn', label: 'warn' },
    { value: 'error', label: 'error' },
  ];

  function fillSelect(id, items, current, extra) {
    const sel = $(id);
    App.dom.clear(sel);
    if (extra) sel.appendChild(App.dom.el('option', { value: extra.value }, extra.label));
    items.forEach(function (it) {
      const o = App.dom.el('option', { value: String(it.value) }, it.label);
      if (String(it.value) === String(current)) o.selected = true;
      sel.appendChild(o);
    });
  }

  /** 设备下拉：首项"系统默认"（值空串），可选追加"禁用"。 */
  function fillDeviceSelect(id, list, current, disabledOption) {
    const sel = $(id);
    App.dom.clear(sel);
    sel.appendChild(App.dom.el('option', { value: '' }, '系统默认'));
    list.forEach(function (d) {
      const label = d.name ? d.name : d.id;
      const o = App.dom.el('option', { value: d.id }, label);
      if (String(d.id) === String(current)) o.selected = true;
      sel.appendChild(o);
    });
    if (disabledOption) {
      const o = App.dom.el('option', { value: disabledOption.value }, disabledOption.label);
      if (String(current) === disabledOption.value) o.selected = true;
      sel.appendChild(o);
    }
    // 当前值不在枚举结果里（设备拔掉了/枚举未就绪）：补一项，别把选择悄悄改掉
    if (current && !Array.prototype.some.call(sel.options, function (o) { return o.value === current; })) {
      const o = App.dom.el('option', { value: current }, current + '（当前，未在列表）');
      o.selected = true;
      sel.appendChild(o);
    }
  }

  function applySnapshot(snap) {
    cfg = snap;
    const a = snap.account || {};
    $('accEnabled').checked = !!a.enabled;
    $('accServer').value = a.server || '';
    $('accPort').value = a.port || '';
    $('accAccount').value = a.account || '';
    $('accAuthUser').value = a.authUser || '';
    $('accDisplayName').value = a.displayName || '';
    $('accPassword').value = '';
    $('accPassword').placeholder = a.passwordSet ? '已设置，留空表示不修改' : '未设置';
    $('accClearPassword').checked = false;
    $('accExpires').value = a.expires || 300;
    $('accDomain').value = a.domain || '';
    $('accPublicIP').value = a.publicIP || '';
    $('accProxy').value = a.proxy || '';
    $('accStunServer').value = a.stunServer || '';
    $('accRPort').checked = !!a.rport;
    $('accOptChk').checked = !!a.optchk;
    $('accStunEnabled').checked = !!a.stunEnabled;
    $('accTLSVerify').checked = !!a.tlsVerify;
    fillSelect('accTransport', TRANSPORTS, a.transport || 'udp');
    fillSelect('accSrtp', SRTP, a.srtp || 'supported');
    updateTransportHint();

    const au = snap.audio || {};
    $('audAEC').checked = !!au.aec;
    $('audNS').checked = !!au.ns;
    $('audAGC').checked = !!au.agc;
    const preOk = !!au.preprocessSupported;
    $('audAEC').disabled = !preOk;
    $('audNS').disabled = !preOk;
    $('audAGC').disabled = !preOk;
    $('audHint').textContent = preOk ? '' : '本构建未包含音频预处理（缺静态库），三项不可用。';
    fillDeviceSelect('audMic', devices.mics, au.micId);
    fillDeviceSelect('audSpeaker', devices.speakers, au.speakerId);
    fillDeviceSelect('audRing', devices.speakers, au.ringSpeakerId);

    const v = snap.video || {};
    $('vidEnabled').checked = v.enabled !== false;
    $('vidWidth').value = v.width || 640;
    $('vidHeight').value = v.height || 480;
    $('vidFpsCfg').value = v.fps || 25;
    $('vidBitrate').value = v.bitrateKbps || 512;
    fillDeviceSelect('vidCamera', cameras.cameras, v.cameraId,
      { value: 'disabled', label: '禁用摄像头' });
    $('vidCamera').disabled = !cameras.cameraSupported;
    $('vidHint').textContent = cameras.cameraSupported ? ''
      : (v.supported ? '本平台没有摄像头采集后端，视频只能接不能发。' : '本构建未包含视频。');

    const lg = snap.log || {};
    fillSelect('logLevel', LEVELS, lg.level || 'info');
    $('logDir').value = lg.dir || '';
    $('logFile').checked = !!lg.file;
    $('logSipTrace').checked = !!lg.sipTrace;

    $('appAutoStart').checked = !!snap.autoStart;
    $('wsPort').value = (snap.ws && snap.ws.port) || '';
    const actual = (snap.ws && snap.ws.actualPort) || 0;
    $('wsHint').textContent = actual
      ? '当前进程实际监听：' + actual + '（改端口需重启后生效）'
      : '改端口需重启后生效';

    const u = snap.update || {};
    $('updSite').value = u.url || '';
    $('updAppId').value = u.appId || '';
    $('updInterval').value = u.intervalMinutes || 5;
    $('updPrerelease').checked = !!u.prerelease;

    $('verLine').textContent = '当前版本：' + (snap.version || '?') + '（' + snap.os + '/' + snap.arch + '）';
  }

  function updateTransportHint() {
    const t = $('accTransport').value;
    $('accTLSVerify').disabled = t !== 'tls';
    $('accOptChk').disabled = t !== 'udp';
    $('accHint').textContent = t === 'tls'
      ? 'TLS 只加密信令；通话媒体（声音/视频）是否加密由「媒体加密」决定。'
      : '保存后会立即按新配置重新注册。';
  }

  function loadConfig() {
    if (!store.isConnected()) return Promise.resolve(); // 没连上不发请求（连上后 onShow 会重新加载）
    return sdk.getConfig().then(applySnapshot)
      .catch(function (e) { App.ui.errToast('读取配置失败', e); });
  }

  function loadAudioDevices() {
    if (!store.isConnected()) return Promise.resolve();
    return sdk.listAudioDevices().then(function (d) {
      devices = d;
      if (cfg) applySnapshot(cfg);
      if (!d.ready) $('audHint').textContent = '音频设备仍在枚举中（音频服务可能未就绪），稍后重试。';
    }).catch(function (e) { App.ui.errToast('枚举音频设备失败', e); });
  }

  function loadCameras() {
    if (!store.isConnected()) return Promise.resolve();
    return sdk.listVideoDevices().then(function (d) {
      cameras = d;
      if (cfg) applySnapshot(cfg);
    }).catch(function (e) { App.ui.errToast('枚举摄像头失败', e); });
  }

  function saveAccount() {
    const req = {
      enabled: $('accEnabled').checked,
      server: $('accServer').value.trim(),
      port: parseInt($('accPort').value, 10) || 0,
      account: $('accAccount').value.trim(),
      authUser: $('accAuthUser').value.trim(),
      displayName: $('accDisplayName').value.trim(),
      expires: parseInt($('accExpires').value, 10) || 300,
      domain: $('accDomain').value.trim(),
      publicIP: $('accPublicIP').value.trim(),
      proxy: $('accProxy').value.trim(),
      stunServer: $('accStunServer').value.trim(),
      rport: $('accRPort').checked,
      optchk: $('accOptChk').checked,
      stunEnabled: $('accStunEnabled').checked,
      tlsVerify: $('accTLSVerify').checked,
      transport: $('accTransport').value,
      srtp: $('accSrtp').value,
    };
    if ($('accClearPassword').checked) req.clearPassword = true;
    else if ($('accPassword').value) req.password = $('accPassword').value;

    sdk.setAccount(req).then(function (snap) {
      applySnapshot(Object.assign({}, cfg, { account: snap }));
      App.ui.toast('账户已保存，正在重新注册', 'ok');
    }).catch(function (e) { App.ui.toast('保存失败：' + App.util.errText(e), 'err'); });
  }

  App.views.settings = {
    mount: function () {
      const fail = function (e) { App.ui.toast(App.util.errText(e), 'err'); };

      fillSelect('uriTransport', TRANSPORTS, 'udp');
      $('accTransport').addEventListener('change', updateTransportHint);
      $('btnSaveAccount').addEventListener('click', saveAccount);

      $('btnRefreshAudio').addEventListener('click', function () { loadAudioDevices(); });
      $('btnSaveAudio').addEventListener('click', function () {
        sdk.setAudioConfig({
          micId: $('audMic').value,
          speakerId: $('audSpeaker').value,
          ringSpeakerId: $('audRing').value,
          aec: $('audAEC').checked,
          ns: $('audNS').checked,
          agc: $('audAGC').checked,
        }).then(function () {
          App.ui.toast('音频配置已保存', 'ok');
        }).catch(function (e) { App.ui.errToast('保存失败', e); });
      });

      $('btnRefreshVideo').addEventListener('click', function () { loadCameras(); });
      $('btnSaveVideo').addEventListener('click', function () {
        const cam = $('vidCamera').value;
        const req = {
          enabled: $('vidEnabled').checked,
          cameraId: cam,
          width: parseInt($('vidWidth').value, 10) || 640,
          height: parseInt($('vidHeight').value, 10) || 480,
          fps: parseInt($('vidFpsCfg').value, 10) || 25,
          bitrateKbps: parseInt($('vidBitrate').value, 10) || 512,
        };
        // 显示名一并存下（Windows 的设备 ID 每次启动可能变，名字是兜底匹配）
        const hit = (cameras.cameras || []).filter(function (c) { return c.id === cam; })[0];
        if (hit) req.cameraName = hit.name;
        sdk.setVideoConfig(req).then(function () {
          App.ui.toast('视频配置已保存（摄像头/参数从下一通电话生效）', 'ok');
          loadConfig();
        }).catch(function (e) { App.ui.errToast('保存失败', e); });
      });

      $('btnSaveLog').addEventListener('click', function () {
        sdk.setLogConfig({
          level: $('logLevel').value,
          dir: $('logDir').value.trim(),
          file: $('logFile').checked,
          sipTrace: $('logSipTrace').checked,
        }).then(function () {
          App.ui.toast('日志配置已保存（写文件/控制台需重启）', 'ok');
        }).catch(function (e) { App.ui.errToast('保存失败', e); });
      });

      $('appAutoStart').addEventListener('change', function () {
        const on = this.checked;
        sdk.setAutoStart(on).then(function () {
          App.ui.toast(on ? '已开启开机自启' : '已关闭开机自启', 'ok');
        }).catch(function (e) {
          App.ui.errToast('设置失败', e);
          loadConfig();
        });
      });
      $('btnSaveWS').addEventListener('click', function () {
        const port = parseInt($('wsPort').value, 10);
        if (!port || port < 1 || port > 65535) { App.ui.toast('端口必须在 1–65535', 'err'); return; }
        const allowRemote = $('wsAllowRemote').checked;
        // 从关到开要二次确认（接口无鉴权，开了等于把控制权交给整个网段）
        if (allowRemote && !(cfg && cfg.ws && cfg.ws.allowRemote)) {
          App.ui.confirm('允许远程连接？',
            '网页接口没有鉴权：勾选后监听 0.0.0.0，同网段任何设备都能拨号、读通信录/通话记录、改配置甚至重启本机软电话。确定开启？')
            .then(function (ok) {
              if (!ok) { $('wsAllowRemote').checked = false; return; }
              saveWS(port, true);
            });
          return;
        }
        saveWS(port, allowRemote);
      });

      function saveWS(port, allowRemote) {
        sdk.setWSPort(port, allowRemote).then(function (res) {
          if (res.restartRequired) {
            App.ui.toast('已保存，重启后生效', 'ok');
            App.ui.confirm('重启后生效', '网页接入已改为 端口 ' + res.port +
              (res.allowRemote ? '（允许远程连接）' : '（仅本机）') +
              '，需重启软电话；重启后请用新地址打开控制台。现在重启？').then(function (ok) {
              if (ok) sdk.restartApp().catch(fail);
            });
          } else {
            App.ui.toast('没有变化', 'ok');
          }
          loadConfig();
        }).catch(function (e) { App.ui.errToast('保存失败', e); });
      }
      $('btnRestart').addEventListener('click', function () {
        App.ui.confirm('重启程序', '软电话会退出并立即重启，通话会被挂断。继续？').then(function (ok) {
          if (!ok) return;
          sdk.restartApp().then(function () {
            App.ui.toast('正在重启，页面稍后会断开', 'ok');
          }).catch(fail);
        });
      });

      $('btnWriteUpdCfg').addEventListener('click', function () {
        let interval = parseInt($('updInterval').value, 10);
        if (!interval || interval < 1) interval = 5;
        if (interval > 1440) interval = 1440;
        sdk.setUpdateConfig({
          url: $('updSite').value.trim(),
          appId: $('updAppId').value.trim(),
          intervalMinutes: interval,
          prerelease: $('updPrerelease').checked,
        }).then(function (c) {
          $('updInterval').value = c.intervalMinutes;
          $('updHint').textContent = c.url
            ? '软电话将每 ' + c.intervalMinutes + ' 分钟检查一次 ' + c.url + '（应用 ' + c.appId + '）。'
            : '更新站点为空 = 不自动检查。';
          App.ui.toast('更新配置已保存', 'ok');
        }).catch(function (e) { App.ui.errToast('保存失败', e); });
      });
      $('btnCheckUpdate').addEventListener('click', function () {
        App.ui.toast('正在检查更新…');
        sdk.checkUpdateNow().then(function (res) {
          $('updHint').textContent = '检查结果：' + (res.text || '') +
            (res.latestVersion ? '（最新 ' + res.latestVersion + '）' : '');
          App.ui.toast(res.text || '检查完成', 'ok');
        }).catch(function (e) { App.ui.errToast('检查失败', e); });
      });

      // 音频后端就绪 → 刷新设备列表（无 UI 版没有桌面界面，靠这条事件）
      sdk.on('audio.ready', function () { loadAudioDevices(); });
      store.on('config', function () { if (!$('view-settings').hidden) loadConfig(); });

      // mount 只绑事件：那时 WS 还没连上，发请求必然以"尚未连接软电话"失败
      // （会把右下角刷一屏 toast）。数据在 onShow 里拉。
    },
    onShow: function () {
      loadConfig();
      loadAudioDevices();
      loadCameras();
    },
    onHide: function () { },
  };
})(window.SipApp);
