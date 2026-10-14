package wsapi

import (
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 网页配置页的接口（REQ-WS-016 起）。
//
// 口径统一：
//   - **只改传入的字段**（可选字段一律指针，nil = 不改），与 system.setUpdateConfig 同款；
//   - 校验失败返回 invalid_params，**不落盘也不改内存**（在 core 的 Patch* 里做）；
//   - 成功即"改内存 → 落盘 → 广播 system.config"，其余网页/桌面据此同步。
//
// 设备枚举（audio.devices / video.devices）是**异步回包**的：枚举可能阻塞数秒，
// 不能在分发里同步做。写路径有 wmu 串行化、SDK 按 id 配对响应，所以异步与乱序
// 回复都是安全的（唯一要求：别把"会终止进程"的动作放在回包之前）。

// dispatchConfig 处理配置与设备类方法（ok = false 表示不是本组方法）。
//
// 需要 c（连接）是因为设备枚举走异步回包。
func (s *Server) dispatchConfig(c *wsConn, msg Message) (Message, bool) {
	switch msg.Method {
	case MethodGetConfig:
		snap := s.app.ConfigSnapshot()
		return okMessage(msg.ID, map[string]any{
			"version": core.Version,
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
			"account": snap.Account,
			"audio":   snap.Audio,
			"video":   snap.Video,
			"log":     snap.Log,
			"ws": map[string]any{
				"port": snap.WS.Port, "actualPort": s.Port(),
				// 允许远程连接（ws.allowRemote）：界面据此显示勾选状态与风险提示
				"allowRemote": snap.WS.AllowRemote,
			},
			"update":    snap.Update,
			"autoStart": snap.AutoStart,
		}), true

	case MethodSetAccount:
		// 账户是**整份覆盖**语义（与桌面设置页一致）：网页先 config.get 读出
		// account、改几个字段再整份回传。password 缺省 = 不改密码，
		// clearPassword = 显式清空（两者二选一，clearPassword 优先）。
		var p struct {
			Enabled       *bool   `json:"enabled"`
			Server        *string `json:"server"`
			Port          *int    `json:"port"`
			Expires       *int    `json:"expires"`
			RPort         *bool   `json:"rport"`
			ChkOpt        *bool   `json:"optchk"`
			STUNEnabled   *bool   `json:"stunEnabled"`
			STUNServer    *string `json:"stunServer"`
			Proxy         *string `json:"proxy"`
			Transport     *string `json:"transport"`
			TLSVerify     *bool   `json:"tlsVerify"`
			SRTP          *string `json:"srtp"`
			Domain        *string `json:"domain"`
			PublicIP      *string `json:"publicIP"`
			Account       *string `json:"account"`
			AuthUser      *string `json:"authUser"`
			DisplayName   *string `json:"displayName"`
			Password      *string `json:"password"`
			ClearPassword bool    `json:"clearPassword"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误")), true
		}
		snap, err := s.app.PatchAccount(core.AccountPatch{
			Enabled: p.Enabled, Server: p.Server, Port: p.Port, Expires: p.Expires,
			RPort: p.RPort, ChkOpt: p.ChkOpt, STUNEnabled: p.STUNEnabled,
			STUNServer: p.STUNServer, Proxy: p.Proxy, Transport: p.Transport,
			TLSVerify: p.TLSVerify, SRTP: p.SRTP, Domain: p.Domain, PublicIP: p.PublicIP,
			Account: p.Account, AuthUser: p.AuthUser, DisplayName: p.DisplayName,
			Password: p.Password, ClearPassword: p.ClearPassword,
		})
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, snap), true

	case MethodSetAudioConfig:
		var p struct {
			MicID         *string `json:"micId"`
			SpeakerID     *string `json:"speakerId"`
			RingSpeakerID *string `json:"ringSpeakerId"`
			AEC           *bool   `json:"aec"`
			NS            *bool   `json:"ns"`
			AGC           *bool   `json:"agc"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误")), true
		}
		snap, err := s.app.PatchAudio(core.AudioPatch{
			MicID: p.MicID, SpeakerID: p.SpeakerID, RingSpeakerID: p.RingSpeakerID,
			AEC: p.AEC, NS: p.NS, AGC: p.AGC,
		})
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, snap), true

	case MethodSetVideoConfig:
		var p struct {
			Enabled     *bool   `json:"enabled"` // 总开关（网页读起来直观，等于 !disabled）
			Disabled    *bool   `json:"disabled"`
			CameraID    *string `json:"cameraId"`
			CameraName  *string `json:"cameraName"`
			Width       *int    `json:"width"`
			Height      *int    `json:"height"`
			FPS         *int    `json:"fps"`
			BitrateKbps *int    `json:"bitrateKbps"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误")), true
		}
		disabled := p.Disabled
		if p.Enabled != nil {
			off := !*p.Enabled
			disabled = &off
		}
		snap, err := s.app.PatchVideo(core.VideoPatch{
			Disabled: disabled, CameraID: p.CameraID, CameraName: p.CameraName,
			Width: p.Width, Height: p.Height, FPS: p.FPS, BitrateKbps: p.BitrateKbps,
		})
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, snap), true

	case MethodSetLogConfig:
		var p struct {
			Level    *string `json:"level"`
			Dir      *string `json:"dir"`
			File     *bool   `json:"file"`
			Console  *bool   `json:"console"`
			SIPTrace *bool   `json:"sipTrace"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误")), true
		}
		cfg, err := s.app.PatchLog(core.LogPatch{
			Level: p.Level, Dir: p.Dir, File: p.File, Console: p.Console, SIPTrace: p.SIPTrace,
		})
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, cfg), true

	case MethodSetWSPort:
		// 字段都可选：只传 port 就只改端口，只传 allowRemote 就只改开关。
		// allowRemote = 允许远程连接（监听 0.0.0.0 且不拒绝非回环来源）——
		// ⚠️ WS 接口没有鉴权，放开后同网段任何设备都能控制本机软电话。
		var p struct {
			Port        *int  `json:"port"`
			AllowRemote *bool `json:"allowRemote"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || (p.Port == nil && p.AllowRemote == nil) {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 port 或 allowRemote")), true
		}
		changed, err := s.app.PatchWS(core.WSPatch{Port: p.Port, AllowRemote: p.AllowRemote})
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		snap := s.app.ConfigSnapshot()
		return okMessage(msg.ID, map[string]any{
			"port":            snap.WS.Port,
			"allowRemote":     snap.WS.AllowRemote,
			"actualPort":      s.Port(), // 当前进程实际在监听的端口
			"restartRequired": changed,  // 改了就提示"重启后生效"
		}), true

	case MethodSetAutoStart:
		var p struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || p.Enabled == nil {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 enabled")), true
		}
		on, err := s.app.SetAutoStart(*p.Enabled)
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, map[string]any{"enabled": on}), true

	case MethodSetUnregisterOnDisconnect:
		// REQ-WS-020：WS 断开自动取消注册——应用级开关，默认关、不持久化
		// （SDK 的 unregisterOnDisconnect 参数每次连上后下发，最后一次设置生效）。
		// 行为见 unregGuard：所有连接断开后延迟确认执行；通话中排队、
		// 重连即取消排队并恢复注册。
		var p struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || p.Enabled == nil {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 enabled")), true
		}
		s.unreg.setOn(*p.Enabled)
		return okMessage(msg.ID, map[string]any{"enabled": *p.Enabled}), true

	case MethodRestart:
		// **先回响应再重启**：重启会取消 ctx、关掉 WS 服务，先重启的话这个响应
		// 就发不出去，网页只看到"连接断开"、不知道到底成没成。
		go func() {
			time.Sleep(applyDelay) // 等响应写出去
			if err := s.app.Restart(); err != nil {
				logx.Logger().Warn("网页触发的重启失败", "err", err)
			}
		}()
		return okMessage(msg.ID, map[string]any{"restarting": true}), true

	case MethodAudioDevices:
		s.enum.devices(s, c, msg.ID)
		return Message{}, true

	case MethodVideoDevices:
		s.enum.cameras(s, c, msg.ID)
		return Message{}, true
	}
	return Message{}, false
}

// applyDelay 触发"会终止本进程"的动作（重启/安装更新）前，等响应写出去的间隔。
const applyDelay = 150 * time.Millisecond

// enumGuard 设备枚举的单飞 + 短缓存 + 超时：
//
//   - 单飞/缓存：连点"刷新设备"、多标签同时打开配置页时只跑一次枚举，
//     紧接着的第二次请求直接拿缓存（2 秒）；
//   - 超时：设备服务半天不响应时先回一份空列表 + ready=false（页面显示"枚举中"），
//     枚举结果稍后进缓存，下次请求就能拿到——不让页面干等十几秒。
type enumGuard struct {
	mu     sync.Mutex
	at     time.Time
	mics   []core.AudioDevice
	speaks []core.AudioDevice
	cams   []core.VideoDevice
}

const (
	enumCacheTTL = 2 * time.Second
	enumTimeout  = 3 * time.Second
)

// devices 异步枚举音频设备并回包。
func (g *enumGuard) devices(s *Server, c *wsConn, id json.RawMessage) {
	g.mu.Lock()
	cached := time.Since(g.at) < enumCacheTTL
	mics, speaks := g.mics, g.speaks
	g.mu.Unlock()
	if cached {
		s.reply(c, okMessage(id, audioPayload(mics, speaks, s.app)))
		return
	}

	go func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			m, sp := s.app.AudioDevices()
			g.mu.Lock()
			g.mics, g.speaks, g.at = m, sp, time.Now()
			g.mu.Unlock()
		}()
		select {
		case <-done:
		case <-time.After(enumTimeout):
			// 超时：先回"还在枚举"（ready=false、空列表），结果稍后进缓存
			s.reply(c, okMessage(id, audioPayload(nil, nil, s.app)))
			return
		}
		g.mu.Lock()
		mics, speaks := g.mics, g.speaks
		g.mu.Unlock()
		s.reply(c, okMessage(id, audioPayload(mics, speaks, s.app)))
	}()
}

// audioPayload 组装音频设备响应。
// ready=false 时前端显示"设备枚举中/不可用"，而不是把空列表当成"没有设备"。
func audioPayload(mics, speaks []core.AudioDevice, app *core.App) map[string]any {
	if mics == nil {
		mics = []core.AudioDevice{}
	}
	if speaks == nil {
		speaks = []core.AudioDevice{}
	}
	return map[string]any{
		"mics":                mics,
		"speakers":            speaks,
		"ready":               app.AudioReady(),
		"preprocessSupported": app.AudioPreprocessSupported(),
	}
}

// cameras 异步枚举摄像头并回包（ListCameras 同样可能耗时）。
// 不设超时：摄像头枚举失败会直接返回空列表，不会长时间挂住。
func (g *enumGuard) cameras(s *Server, c *wsConn, id json.RawMessage) {
	g.mu.Lock()
	cached := time.Since(g.at) < enumCacheTTL && g.cams != nil
	cams := g.cams
	g.mu.Unlock()
	if cached {
		s.reply(c, okMessage(id, videoPayload(cams, s.app)))
		return
	}
	go func() {
		list := s.app.VideoDevices()
		g.mu.Lock()
		g.cams = list
		g.mu.Unlock()
		s.reply(c, okMessage(id, videoPayload(list, s.app)))
	}()
}

func videoPayload(cams []core.VideoDevice, app *core.App) map[string]any {
	if cams == nil {
		cams = []core.VideoDevice{}
	}
	return map[string]any{
		"cameras":         cams,
		"supported":       app.VideoSupported(),
		"cameraSupported": app.CameraSupported(),
	}
}
