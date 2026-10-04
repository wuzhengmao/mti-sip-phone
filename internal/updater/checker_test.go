package updater

// 更新站点查询（REQ-UPD-017）的回归测试：请求地址与参数、结果解析、
// 去重（同一版本只告知一次）、无产物/404/未配置站点的处理。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mti-sip-phone/internal/core"
)

// checkServer 假更新站点：记录收到的查询参数，按需返回 JSON。
type checkServer struct {
	mu     sync.Mutex
	path   string
	query  url.Values
	body   string
	status int
	hits   int
}

func newCheckServer(t *testing.T, body string, status int) (*checkServer, *httptest.Server) {
	t.Helper()
	cs := &checkServer{body: body, status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.path = r.URL.Path
		cs.query = r.URL.Query()
		cs.hits++
		cs.mu.Unlock()
		if cs.status != 0 {
			w.WriteHeader(cs.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cs.body))
	}))
	t.Cleanup(srv.Close)
	return cs, srv
}

func (c *checkServer) snapshot() (path string, query url.Values, hits int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.query, c.hits
}

// newCheckUpdater 构造一个只用于查询的 Updater（注入假站点用的 HTTP 客户端与事件总线）。
func newCheckUpdater(t *testing.T, srv *httptest.Server, bus *core.Bus) *Updater {
	t.Helper()
	u := New(Options{Bus: bus, HTTPClient: srv.Client(), ExePath: t.TempDir() + "/app.exe"})
	return u
}

func site(url string) core.SiteConfigSnapshot {
	return core.SiteConfigSnapshot{URL: url, AppID: "mti-sip-phone", Interval: time.Minute}
}

// TestCheckNotifiesNewVersion 查到新版本 → 走 Notify（进入 available、发 system.update）。
func TestCheckNotifiesNewVersion(t *testing.T) {
	body := `{"latestVersion":"9.9.9","updateAvailable":true,
		"notes":"### 新增","download":{"url":"http://example.com/a.exe","sha256":"ABC123"}}`
	cs, srv := newCheckServer(t, body, 0)
	bus := core.NewBus()
	sub := bus.Subscribe()
	u := newCheckUpdater(t, srv, bus)
	u.SetUpdateConfig(site(srv.URL))

	res, err := u.CheckNow(context.Background())
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !res.Notified || res.LatestVersion != "9.9.9" {
		t.Fatalf("应告知新版本 9.9.9，实际 %+v", res)
	}

	// 请求地址与参数：version/os/arch 必带；prerelease 未开不带
	path, q, hits := cs.snapshot()
	if hits != 1 {
		t.Fatalf("应只请求一次，实际 %d", hits)
	}
	if path != "/api/v1/apps/mti-sip-phone/check" {
		t.Fatalf("请求路径不对: %s", path)
	}
	if got := q.Get("version"); got != core.Version {
		t.Fatalf("version 参数应为 %s，实际 %s", core.Version, got)
	}
	if got := q.Get("os"); got != runtime.GOOS {
		t.Fatalf("os 参数应为 %s，实际 %s", runtime.GOOS, got)
	}
	if got := q.Get("arch"); got != runtime.GOARCH {
		t.Fatalf("arch 参数应为 %s，实际 %s", runtime.GOARCH, got)
	}
	if q.Has("prerelease") || q.Has("channel") {
		t.Fatalf("默认不应带 prerelease/channel: %v", q)
	}

	// 状态机进入 available，并向总线发了 system.update
	if st := u.Status(); st.Phase != core.UpdateAvailable || st.Info.Version != "9.9.9" {
		t.Fatalf("更新状态应为 available/9.9.9，实际 %+v", st)
	}
	select {
	case ev := <-sub:
		if ev.Name != "system.update" {
			t.Fatalf("期望 system.update 事件，实际 %s", ev.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 system.update 事件")
	}
}

// TestCheckDedupAndPrerelease 同一版本只告知一次；开启预览版时带 prerelease=true。
func TestCheckDedupAndPrerelease(t *testing.T) {
	body := `{"latestVersion":"9.9.9","updateAvailable":true,
		"download":{"url":"http://example.com/a.exe","sha256":"abc"}}`
	cs, srv := newCheckServer(t, body, 0)
	u := newCheckUpdater(t, srv, nil)
	cfg := site(srv.URL)
	cfg.Prerelease = true
	u.SetUpdateConfig(cfg)

	if _, err := u.CheckNow(context.Background()); err != nil {
		t.Fatalf("首次查询失败: %v", err)
	}
	_, q, _ := cs.snapshot()
	if q.Get("prerelease") != "true" {
		t.Fatalf("开启预览版时应带 prerelease=true，实际 %v", q)
	}

	// 第二次：同版本 + 同 sha256 → 不再告知（避免每 5 分钟重复弹窗）
	res, err := u.CheckNow(context.Background())
	if err != nil {
		t.Fatalf("二次查询失败: %v", err)
	}
	if res.Notified || res.Reason != "already_notified" {
		t.Fatalf("同版本应只告知一次，实际 %+v", res)
	}
}

// TestCheckNoDownload 站点说有更新但本平台没有产物 → 不报错、不告知。
func TestCheckNoDownload(t *testing.T) {
	cs, srv := newCheckServer(t, `{"latestVersion":"9.9.9","updateAvailable":true,"download":null}`, 0)
	_ = cs
	u := newCheckUpdater(t, srv, nil)
	u.SetUpdateConfig(site(srv.URL))

	res, err := u.CheckNow(context.Background())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if res.Notified || res.Reason != "no_download" {
		t.Fatalf("应视为暂无产物，实际 %+v", res)
	}
	if st := u.Status(); st.Phase != core.UpdateIdle {
		t.Fatalf("不应进入更新状态，实际 %s", st.Phase)
	}
}

// TestCheckUpToDate 站点说已是最新 → up_to_date。
func TestCheckUpToDate(t *testing.T) {
	_, srv := newCheckServer(t, `{"latestVersion":"1.0.9","updateAvailable":false}`, 0)
	u := newCheckUpdater(t, srv, nil)
	u.SetUpdateConfig(site(srv.URL))

	res, err := u.CheckNow(context.Background())
	if err != nil || !res.UpToDate {
		t.Fatalf("应为已是最新，实际 %+v err=%v", res, err)
	}
}

// TestCheckErrors 站点 404 / 响应体非法 → 返回错误（不改变更新状态）。
func TestCheckErrors(t *testing.T) {
	_, srv := newCheckServer(t, "", http.StatusNotFound)
	u := newCheckUpdater(t, srv, nil)
	u.SetUpdateConfig(site(srv.URL))
	if _, err := u.CheckNow(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "404") {
		t.Fatalf("404 应报错，实际 %v", err)
	}

	_, srv2 := newCheckServer(t, "not json", 0)
	u2 := newCheckUpdater(t, srv2, nil)
	u2.SetUpdateConfig(site(srv2.URL))
	if _, err := u2.CheckNow(context.Background()); err == nil {
		t.Fatal("响应体非法应报错")
	}
}

// TestCheckSkippedWithoutSite 未配置站点 → 不查、不报错。
func TestCheckSkippedWithoutSite(t *testing.T) {
	_, srv := newCheckServer(t, `{}`, 0)
	u := newCheckUpdater(t, srv, nil)

	res, err := u.CheckNow(context.Background())
	if err != nil {
		t.Fatalf("未配置站点不应报错: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("应标记为未检查，实际 %+v", res)
	}
}

// TestStartChecksImmediately 启动轮询后应立刻查一次（不必等一个周期），
// 且 Stop（ctx 取消）后不再查。
// TestStartChecksImmediatelyLongInterval 启动后应立即查一次——**不能等一个周期**
//（周期默认 5 分钟，等一个周期意味着"刚填完站点后五分钟内什么都没发生"）。
func TestStartChecksImmediatelyLongInterval(t *testing.T) {
	cs, srv := newCheckServer(t, `{"latestVersion":"1.0.9","updateAvailable":false}`, 0)
	u := newCheckUpdater(t, srv, nil)
	cfg := site(srv.URL)
	cfg.Interval = time.Hour // 长周期：只有"立即查"才可能在测试里命中
	u.SetUpdateConfig(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.Start(ctx)

	deadline := time.After(2 * time.Second)
	for {
		if _, _, hits := cs.snapshot(); hits >= 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("启动后未立即查询（要等一个周期才查是 bug）")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestStartChecksImmediately(t *testing.T) {
	cs, srv := newCheckServer(t, `{"latestVersion":"1.0.9","updateAvailable":false}`, 0)
	u := newCheckUpdater(t, srv, nil)
	cfg := site(srv.URL)
	cfg.Interval = 50 * time.Millisecond
	u.SetUpdateConfig(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	u.Start(ctx)

	deadline := time.After(2 * time.Second)
	for {
		if _, _, hits := cs.snapshot(); hits >= 2 {
			break // 立即一次 + 至少一个周期
		}
		select {
		case <-deadline:
			_, _, hits := cs.snapshot()
			t.Fatalf("轮询未生效（hits=%d）", hits)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	time.Sleep(150 * time.Millisecond) // 让在途请求收尾
	_, _, before := cs.snapshot()
	time.Sleep(200 * time.Millisecond)
	if _, _, after := cs.snapshot(); after != before {
		t.Fatalf("ctx 取消后不应再查：%d → %d", before, after)
	}
}

// TestCheckURLShape 地址拼装：末尾斜杠、应用 ID 转义、不带 channel。
func TestCheckURLShape(t *testing.T) {
	u := New(Options{})
	got := u.checkURL(core.SiteConfigSnapshot{URL: "http://x.example/", AppID: "my app"})
	want := "http://x.example/api/v1/apps/my%20app/check?arch=" + runtime.GOARCH +
		"&os=" + runtime.GOOS + "&version=" + core.Version
	if got != want {
		t.Fatalf("地址拼装不对:\n得到 %s\n期望 %s", got, want)
	}
	_ = json.Marshal // 保持 json 导入（响应解析在 querySite 里）
}
