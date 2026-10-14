package wsapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"mti-sip-phone/internal/calllog"
	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/contacts"
	"mti-sip-phone/internal/core"
)

// 网页控制台接口的回归测试（REQ-WS-016 ~ REQ-WS-019）：
// 通话记录、通信录增删改查、配置读写、设备枚举、更新触发。

// fakeAudio 音频能力桩：core.AudioControl 的测试实现。
type fakeAudio struct {
	mics       []core.AudioDevice
	speakers   []core.AudioDevice
	preprocess bool
	mic        string
	aec        bool
}

func (f *fakeAudio) Ready() bool { return true }
func (f *fakeAudio) Devices() ([]core.AudioDevice, []core.AudioDevice) {
	return f.mics, f.speakers
}
func (f *fakeAudio) SetMic(id string)                     { f.mic = id }
func (f *fakeAudio) SetSpeaker(string)                    {}
func (f *fakeAudio) SetRingSpeaker(string)                {}
func (f *fakeAudio) SetPreprocess(p core.AudioPreprocess) { f.aec = p.AEC }
func (f *fakeAudio) PreprocessSupported() bool            { return f.preprocess }

// newControlServer 在基础测试服务上再接入通话记录、通信录、音频与重启回调，
// 并返回一条已连上的 WS 连接（与线上路径完全一致：真实 HTTP 服务 + 真实 WS）。
func newControlServer(t *testing.T) (*websocket.Conn, *core.App, *fakeAudio) {
	conn, app, fa, _ := newControlServerURL(t)
	return conn, app, fa
}

// newControlServerURL 同 newControlServer，额外返回测试服务器的 URL
// （断线重连类测试要再连一次）。
func newControlServerURL(t *testing.T) (*websocket.Conn, *core.App, *fakeAudio, string) {
	t.Helper()
	cfg := config.Default()
	cfgPath := filepath.Join(t.TempDir(), "config.json")

	bus := core.NewBus()
	app := core.New(cfg, nil, bus)
	app.SetConfigPath(cfgPath)
	tel := &stubTel{app: app}
	app.SetTel(tel)

	dir := t.TempDir()
	store, err := calllog.Open(filepath.Join(dir, "call-log.db"))
	if err != nil {
		t.Fatalf("打开通话记录库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app.SetCallLog(store)

	cstore, err := contacts.Open(filepath.Join(dir, "contacts.db"))
	if err != nil {
		t.Fatalf("打开通信录库失败: %v", err)
	}
	t.Cleanup(func() { _ = cstore.Close() })
	app.SetContacts(cstore)

	fa := &fakeAudio{
		mics:     []core.AudioDevice{{ID: "mic-1", Name: "麦克风 1"}},
		speakers: []core.AudioDevice{{ID: "spk-1", Name: "扬声器 1"}},
	}
	app.SetAudioControl(fa)
	app.Start(t.Context())

	srv := New(app)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return dialWS(t, ts.URL), app, fa, ts.URL
}

// request 发一条请求并等它的响应（跳过穿插的事件）。
func request(t *testing.T, conn *websocket.Conn, method string, params any) Message {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("参数序列化失败: %v", err)
	}
	sendRequest(t, conn, Message{Method: method, Params: raw})
	return waitResponse(t, conn)
}

// ---------------- 通话记录 ----------------

func TestCallLogQueryDeleteClear(t *testing.T) {
	conn, app, _ := newControlServer(t)

	// 造两条记录：一条已接通、一条未接来电
	log := app.CallLog()
	now := time.Now()
	log.Start(core.CallRecord{CallID: "c1", Direction: "outgoing", Remote: "1001", StartedAt: now})
	log.MarkAnswered("c1", now)
	log.Finish("c1", now.Add(time.Second), "local_hangup", false)
	log.Start(core.CallRecord{CallID: "c2", Direction: "incoming", Remote: "1002", StartedAt: now})
	log.Finish("c2", now.Add(2*time.Second), "canceled", true)

	resp := request(t, conn, MethodCallLogQuery, map[string]any{"limit": 10})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("查询通话记录失败: %+v", resp.Error)
	}
	data := resp.Data.(map[string]any)
	if total := int(data["total"].(float64)); total != 2 {
		t.Fatalf("应有 2 条记录，实际 %d", total)
	}
	recs := data["records"].([]any)
	if len(recs) != 2 {
		t.Fatalf("应返回 2 条，实际 %d", len(recs))
	}
	first := recs[0].(map[string]any)
	if first["callId"] == "" || first["direction"] == "" {
		t.Fatalf("记录字段缺失: %+v", first)
	}
	// 未接通的那条 answeredAt 必须是 0（不是 time.Time 零值序列化出来的东西）
	for _, r := range recs {
		m := r.(map[string]any)
		if m["answeredAt"] == nil {
			t.Fatalf("answeredAt 字段缺失: %+v", m)
		}
		if m["callId"] == "c2" && m["answeredAt"].(float64) != 0 {
			t.Fatalf("未接通的记录 answeredAt 应为 0，实际 %v", m["answeredAt"])
		}
	}

	// 按号码筛选
	resp = request(t, conn, MethodCallLogQuery, map[string]any{"remote": "1002"})
	data = resp.Data.(map[string]any)
	if int(data["total"].(float64)) != 1 {
		t.Fatalf("按号码筛选应得 1 条，实际 %v", data["total"])
	}

	// 删除要求有 callIds
	resp = request(t, conn, MethodCallLogDelete, map[string]any{})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("空 callIds 应返回 invalid_params，实际 %+v", resp)
	}

	// 删除一条 + 广播 calllog.changed
	resp = request(t, conn, MethodCallLogDelete, map[string]any{"callIds": []string{"c1"}})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("删除失败: %+v", resp.Error)
	}
	if n := int(resp.Data.(map[string]any)["deleted"].(float64)); n != 1 {
		t.Fatalf("应删除 1 条，实际 %d", n)
	}

	// 清空
	resp = request(t, conn, MethodCallLogClear, map[string]any{})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("清空失败: %+v", resp.Error)
	}
	resp = request(t, conn, MethodCallLogQuery, map[string]any{})
	if total := int(resp.Data.(map[string]any)["total"].(float64)); total != 0 {
		t.Fatalf("清空后应为 0 条，实际 %d", total)
	}
}

// ---------------- 通信录 ----------------

func TestContactsCRUD(t *testing.T) {
	conn, app, _ := newControlServer(t)

	resp := request(t, conn, MethodContactList, map[string]any{})
	if n := int(resp.Data.(map[string]any)["count"].(float64)); n != 0 {
		t.Fatalf("初始通信录应为空，实际 %d", n)
	}

	// 新增
	raw, err := json.Marshal(map[string]any{"number": "1001", "name": "张三", "starred": true})
	if err != nil {
		t.Fatalf("参数序列化失败: %v", err)
	}
	sendRequest(t, conn, Message{Method: MethodContactSave, Params: raw})
	resp, ev := waitResponseAndEvent(t, conn, EvContactChanged)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("保存失败: %+v", resp.Error)
	}
	if ev.Data.(map[string]any)["action"] != "save" {
		t.Fatalf("contact.changed 的 action 应为 save，实际 %+v", ev.Data)
	}

	// 号码重复 → invalid_params
	resp = request(t, conn, MethodContactSave, map[string]any{"number": "1001", "name": "李四"})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("重复号码应返回 invalid_params，实际 %+v", resp)
	}

	// 号码为空 → invalid_params
	resp = request(t, conn, MethodContactSave, map[string]any{"name": "无号码"})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("空号码应返回 invalid_params，实际 %+v", resp)
	}

	// 改号（prevNumber）+ 取消常用
	resp = request(t, conn, MethodContactSave, map[string]any{
		"prevNumber": "1001", "number": "1002", "name": "张三丰", "starred": false,
	})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("改号失败: %+v", resp.Error)
	}
	if c := resp.Data.(map[string]any)["contact"].(map[string]any); c["number"] != "1002" || c["name"] != "张三丰" {
		t.Fatalf("改号后的条目不对: %+v", c)
	}

	// 常用标记
	resp = request(t, conn, MethodContactSetStarred, map[string]any{"numbers": []string{"1002"}, "starred": true})
	if n := int(resp.Data.(map[string]any)["updated"].(float64)); n != 1 {
		t.Fatalf("应更新 1 条，实际 %d", n)
	}
	if c, ok := app.LookupContact("1002"); !ok || !c.Starred {
		t.Fatalf("常用标记未生效: %+v", c)
	}

	// 删除
	resp = request(t, conn, MethodContactDelete, map[string]any{"numbers": []string{"1002"}})
	if n := int(resp.Data.(map[string]any)["deleted"].(float64)); n != 1 {
		t.Fatalf("应删除 1 条，实际 %d", n)
	}

	// 清空（先再插一条）
	request(t, conn, MethodContactSave, map[string]any{"number": "2001", "name": "王五"})
	resp = request(t, conn, MethodContactClear, map[string]any{})
	if n := int(resp.Data.(map[string]any)["deleted"].(float64)); n != 1 {
		t.Fatalf("应清空 1 条，实际 %d", n)
	}
}

// TestContactListSearch 通信录搜索（REQ-WS-018）：拼音/音头/汉字的混合匹配在
// **服务端**做（internal/contactsearch，与桌面通信录页同一份实现），
// 结果按拼音排序。
func TestContactListSearch(t *testing.T) {
	conn, _, _ := newControlServer(t)

	// 造几条：中文名（可拼音搜）、带备注、纯号码
	for _, c := range []map[string]any{
		{"number": "1001", "name": "张三", "note": "前台"},
		{"number": "1002", "name": "李四"},
		{"number": "+86 138-0000-0000", "name": "Alice"},
	} {
		resp := request(t, conn, MethodContactSave, c)
		if resp.OK == nil || !*resp.OK {
			t.Fatalf("保存 %v 失败: %+v", c, resp.Error)
		}
	}

	cases := []struct {
		query string
		want  []string // 期望命中的号码（顺序敏感）
	}{
		{"", []string{"+86 138-0000-0000", "1002", "1001"}}, // 空查询 = 全部，按拼音：alice < lisi < zhangsan
		{"zs", []string{"1001"}},                            // 音头
		{"zhangsan", []string{"1001"}},                      // 全拼
		{"张三", []string{"1001"}},                            // 汉字
		{"张san", []string{"1001"}},                          // 混合
		{"ls", []string{"1002"}},                            // 音头
		{"138", []string{"+86 138-0000-0000"}},              // 号码子串
		{"前台", []string{"1001"}},                            // 备注
		{"alice", []string{"+86 138-0000-0000"}},            // ASCII 名称
		{"nobody", nil},                                     // 不命中
	}
	for _, c := range cases {
		params := map[string]any{}
		if c.query != "" {
			params["query"] = c.query
		}
		resp := request(t, conn, MethodContactList, params)
		if resp.OK == nil || !*resp.OK {
			t.Fatalf("contact.list(%q) 失败: %+v", c.query, resp.Error)
		}
		data := resp.Data.(map[string]any)
		if total := int(data["total"].(float64)); total != 3 {
			t.Fatalf("total（未过滤总数）应为 3，实际 %d", total)
		}
		got := []string{}
		for _, it := range data["contacts"].([]any) {
			got = append(got, it.(map[string]any)["number"].(string))
		}
		if len(got) != len(c.want) {
			t.Fatalf("query=%q 命中 %v，期望 %v", c.query, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("query=%q 结果顺序 %v，期望 %v", c.query, got, c.want)
			}
		}
	}
}

// ---------------- 配置 ----------------

func TestConfigGetAndSet(t *testing.T) {
	conn, app, fa := newControlServer(t)

	// 读快照：默认值 + 密码未设置 + 音频能力状态
	resp := request(t, conn, MethodGetConfig, map[string]any{})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("读取配置失败: %+v", resp.Error)
	}
	snap := resp.Data.(map[string]any)
	acc := snap["account"].(map[string]any)
	if acc["passwordSet"] != false {
		t.Fatalf("初始不应有密码: %+v", acc)
	}
	if _, leaked := acc["password"]; leaked {
		t.Fatal("配置快照不能下发密码")
	}
	if snap["autoStart"] != true {
		t.Fatalf("默认应开启自启: %+v", snap["autoStart"])
	}
	audio := snap["audio"].(map[string]any)
	if audio["ready"] != true || audio["preprocessSupported"] != false {
		t.Fatalf("音频能力状态不对: %+v", audio)
	}

	// 账户：非法传输方式 → invalid_params，且不改内存
	resp = request(t, conn, MethodSetAccount, map[string]any{"transport": "sctp"})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("非法传输方式应返回 invalid_params，实际 %+v", resp)
	}
	// 声明 IP 必须是 IP 字面量
	resp = request(t, conn, MethodSetAccount, map[string]any{"publicIP": "sip.example.com"})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("非法声明 IP 应返回 invalid_params，实际 %+v", resp)
	}
	// 正常写入
	resp = request(t, conn, MethodSetAccount, map[string]any{
		"server": "10.168.4.40", "account": "2999", "password": "secret",
		"transport": "tls", "srtp": "required", "domain": "sip.mti-sh.cn",
	})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("写账户失败: %+v", resp.Error)
	}
	got := resp.Data.(map[string]any)
	if got["transport"] != "tls" || got["srtp"] != "required" || got["domain"] != "sip.mti-sh.cn" {
		t.Fatalf("账户字段未生效: %+v", got)
	}
	if got["passwordSet"] != true {
		t.Fatalf("写入密码后 passwordSet 应为 true: %+v", got)
	}

	// 音频：设备选择 + 预处理
	resp = request(t, conn, MethodSetAudioConfig, map[string]any{"micId": "mic-1", "aec": false, "agc": true})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("写音频配置失败: %+v", resp.Error)
	}
	if fa.mic != "mic-1" || fa.aec != false {
		t.Fatalf("音频配置未下发: mic=%q aec=%v", fa.mic, fa.aec)
	}
	if c := app.ConfigSnapshot().Audio; !c.AGC || c.AEC {
		t.Fatalf("音频配置未落内存: %+v", c)
	}

	// 视频：非法帧率 → invalid_params；正常写入生效
	resp = request(t, conn, MethodSetVideoConfig, map[string]any{"fps": 999})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("非法帧率应返回 invalid_params，实际 %+v", resp)
	}
	resp = request(t, conn, MethodSetVideoConfig, map[string]any{"enabled": false, "width": 1280})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("写视频配置失败: %+v", resp.Error)
	}
	if v := app.ConfigSnapshot().Video; v.Enabled || v.Width != 1280 {
		t.Fatalf("视频配置未生效: %+v", v)
	}

	// 日志：级别立即生效，非法级别被拒
	resp = request(t, conn, MethodSetLogConfig, map[string]any{"level": "nope"})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("非法日志级别应返回 invalid_params，实际 %+v", resp)
	}
	resp = request(t, conn, MethodSetLogConfig, map[string]any{"level": "debug", "sipTrace": true})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("写日志配置失败: %+v", resp.Error)
	}
	if lv := app.ConfigSnapshot().Log.Level; lv != "debug" {
		t.Fatalf("日志级别未生效: %q", lv)
	}

	// WS 端口：越界被拒；正常写返回 restartRequired
	resp = request(t, conn, MethodSetWSPort, map[string]any{"port": 70000})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("非法端口应返回 invalid_params，实际 %+v", resp)
	}
	resp = request(t, conn, MethodSetWSPort, map[string]any{"port": 8801})
	gotWS := resp.Data.(map[string]any)
	if gotWS["restartRequired"] != true || int(app.ConfigSnapshot().WS.Port) != 8801 {
		t.Fatalf("WS 端口未生效: %+v", gotWS)
	}

	// 未接入自启控制能力时返回 config_unavailable（测试服务没注入）
	resp = request(t, conn, MethodSetAutoStart, map[string]any{"enabled": false})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeConfigUnavailable {
		t.Fatalf("未接入自启时应返回 config_unavailable，实际 %+v", resp)
	}
}

// TestRestartReturnsBeforeRestarting 重启：先回响应、后异步触发（否则响应发不出去）。
func TestRestartReturnsBeforeRestarting(t *testing.T) {
	conn, app, _ := newControlServer(t)

	restarted := make(chan struct{}, 1)
	app.SetRestartFunc(func() error {
		restarted <- struct{}{}
		return nil
	})

	resp := request(t, conn, MethodRestart, map[string]any{})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("重启请求应成功: %+v", resp.Error)
	}
	if resp.Data.(map[string]any)["restarting"] != true {
		t.Fatalf("响应应带 restarting: %+v", resp.Data)
	}
	select {
	case <-restarted:
	case <-time.After(3 * time.Second):
		t.Fatal("重启回调未被触发")
	}
}

// ---------------- 设备枚举 ----------------

func TestAudioAndVideoDevices(t *testing.T) {
	conn, _, _ := newControlServer(t)

	resp := request(t, conn, MethodAudioDevices, map[string]any{})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("枚举音频设备失败: %+v", resp.Error)
	}
	data := resp.Data.(map[string]any)
	mics := data["mics"].([]any)
	if len(mics) != 1 || mics[0].(map[string]any)["id"] != "mic-1" {
		t.Fatalf("麦克风列表不对: %+v", mics)
	}
	if data["ready"] != true {
		t.Fatalf("ready 应为 true: %+v", data)
	}
	// 第二次请求走缓存，仍返回同一份
	resp2 := request(t, conn, MethodAudioDevices, map[string]any{})
	if got := len(resp2.Data.(map[string]any)["mics"].([]any)); got != 1 {
		t.Fatalf("缓存命中后应仍返回 1 个麦克风，实际 %d", got)
	}

	resp = request(t, conn, MethodVideoDevices, map[string]any{})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("枚举摄像头失败: %+v", resp.Error)
	}
	if cams := resp.Data.(map[string]any)["cameras"].([]any); len(cams) != 0 {
		t.Fatalf("桩没有摄像头，应返回空列表: %+v", cams)
	}
}

// ---------------- 更新 ----------------

func TestUpdateMethods(t *testing.T) {
	conn, _, _ := newControlServer(t)

	resp := request(t, conn, MethodUpdateStatus, map[string]any{})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("查询更新状态失败: %+v", resp.Error)
	}
	if phase := resp.Data.(map[string]any)["phase"]; phase != core.UpdateIdle {
		t.Fatalf("初始更新状态应为 idle，实际 %v", phase)
	}

	// 没有待安装的更新时点"安装"必须报错（异步动作回包后没法改口）
	resp = request(t, conn, MethodUpdateApply, map[string]any{})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("无待安装更新时应返回 invalid_params，实际 %+v", resp)
	}

	// 没有待下载的更新时开始下载：core 返回错误（不应 panic 或挂住）
	resp = request(t, conn, MethodUpdateStart, map[string]any{})
	if resp.OK != nil && *resp.OK {
		t.Fatalf("无待下载更新时 start 不应成功: %+v", resp.Data)
	}
	// 取消是幂等的
	resp = request(t, conn, MethodUpdateCancel, map[string]any{})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("取消更新应成功: %+v", resp.Error)
	}
}

// ---------------- 通话内小缺口 ----------------

func TestSetVideoPreviewUnknownCall(t *testing.T) {
	conn, _, _ := newControlServer(t)

	resp := request(t, conn, MethodSetVideoPreview, map[string]any{"callId": "nope", "enabled": true})
	if resp.OK == nil || *resp.OK {
		t.Fatalf("未知通话应返回错误: %+v", resp)
	}
	if resp.Error.Code != CodeCallNotFound && resp.Error.Code != CodeVideoUnsupported {
		t.Fatalf("错误码不对: %+v", resp.Error)
	}
}

// TestRemoteAccessGate 网页接入的远程开关（ws.allowRemote）：
// 默认只放行回环来源；置 true 后非回环来源也放行（对应监听 0.0.0.0）。
func TestRemoteAccessGate(t *testing.T) {
	_, app, _ := newControlServer(t)
	srv := New(app)

	// 默认（未开启）：非回环来源一律 403
	req := httptest.NewRequest(http.MethodGet, "/api/contacts", nil)
	req.RemoteAddr = "10.1.2.3:4567"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("未开启远程时非回环来源应 403，实际 %d", w.Code)
	}
	// 回环来源照常放行
	req.RemoteAddr = "127.0.0.1:4567"
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("回环来源应 200，实际 %d", w.Code)
	}

	// 开启后：非回环来源放行
	srv.allowRemote.Store(true)
	req.RemoteAddr = "10.1.2.3:4567"
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("开启远程后非回环来源应 200，实际 %d", w.Code)
	}
}

// TestPatchWS 网页改「网页接入」配置：端口与允许远程都可选，返回 restartRequired。
func TestPatchWS(t *testing.T) {
	conn, app, _ := newControlServer(t)

	resp := request(t, conn, MethodSetWSPort, map[string]any{"port": 8801})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("改端口失败: %+v", resp.Error)
	}
	got := resp.Data.(map[string]any)
	if got["restartRequired"] != true || got["allowRemote"] != false {
		t.Fatalf("返回不对: %+v", got)
	}
	// 只改开关（不传端口）
	resp = request(t, conn, MethodSetWSPort, map[string]any{"allowRemote": true})
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("改远程开关失败: %+v", resp.Error)
	}
	got = resp.Data.(map[string]any)
	if got["allowRemote"] != true || int(app.ConfigSnapshot().WS.Port) != 8801 {
		t.Fatalf("开关或端口不对: %+v", got)
	}
	// 两项都没变 → restartRequired = false
	resp = request(t, conn, MethodSetWSPort, map[string]any{"port": 8801, "allowRemote": true})
	if resp.Data.(map[string]any)["restartRequired"] != false {
		t.Fatalf("没变时不应要求重启: %+v", resp.Data)
	}
	// 非法端口
	resp = request(t, conn, MethodSetWSPort, map[string]any{"port": 70000})
	if resp.OK == nil || *resp.OK || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("非法端口应 invalid_params: %+v", resp)
	}
}
