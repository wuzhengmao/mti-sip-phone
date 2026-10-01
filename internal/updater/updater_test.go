package updater

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mti-sip-phone/internal/core"
)

// fakeExecutable 生成带当前平台可执行文件魔数的假二进制。
func fakeExecutable() []byte {
	if runtime.GOOS == "windows" {
		b := make([]byte, 0x60)
		b[0], b[1] = 'M', 'Z'
		binary.LittleEndian.PutUint32(b[0x3C:], 0x40)
		copy(b[0x40:], "PE\x00\x00")
		return b
	}
	return append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 0x5c)...)
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}

func newTestUpdater(t *testing.T, opts Options) *Updater {
	t.Helper()
	if opts.Bus == nil {
		opts.Bus = core.NewBus()
	}
	u := New(opts)
	u.waitInterval = 20 * time.Millisecond
	return u
}

// waitFor 轮询等待条件成立。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待「%s」超时", what)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestNotifyValidation(t *testing.T) {
	u := newTestUpdater(t, Options{ExePath: filepath.Join(t.TempDir(), "app.exe")})
	cases := []struct {
		name string
		info core.UpdateInfo
	}{
		{"缺版本号", core.UpdateInfo{URL: "http://x/a.exe"}},
		{"缺地址", core.UpdateInfo{Version: "9.9.9"}},
		{"非 http(s)", core.UpdateInfo{Version: "9.9.9", URL: "ftp://x/a.exe"}},
		{"地址无主机", core.UpdateInfo{Version: "9.9.9", URL: "http:///a.exe"}},
		{"平台不符", core.UpdateInfo{Version: "9.9.9", URL: "http://x/a.exe", OS: "linux"}},
		{"架构不符", core.UpdateInfo{Version: "9.9.9", URL: "http://x/a.exe", Arch: "mips"}},
	}
	for _, c := range cases {
		if err := u.Notify(c.info); err == nil {
			t.Errorf("%s：期望报错，实际通过", c.name)
		}
	}
	if got := u.Status().Phase; got != core.UpdateIdle {
		t.Errorf("非法通知后状态应为 idle，实际 %s", got)
	}
}

func TestNotifyNotNewer(t *testing.T) {
	u := newTestUpdater(t, Options{ExePath: filepath.Join(t.TempDir(), "app.exe")})
	for _, v := range []string{core.Version, "1.0.0"} {
		err := u.Notify(core.UpdateInfo{Version: v, URL: "http://x/a.exe"})
		if !errors.Is(err, core.ErrUpdateNotNewer) {
			t.Errorf("版本 %s：期望 ErrUpdateNotNewer，实际 %v", v, err)
		}
	}
	// 不可比较的版本放行（裁决权在前端）
	if err := u.Notify(core.UpdateInfo{Version: "nightly", URL: "http://x/a.exe"}); err != nil {
		t.Errorf("不可比版本应放行，实际 %v", err)
	}
	if got := u.Status().Phase; got != core.UpdateAvailable {
		t.Errorf("状态应为 available，实际 %s", got)
	}
}

func TestNotifyIdempotentAndEvents(t *testing.T) {
	bus := core.NewBus()
	sub := bus.Subscribe()
	u := newTestUpdater(t, Options{ExePath: filepath.Join(t.TempDir(), "app.exe"), Bus: bus})

	info := core.UpdateInfo{Version: "9.9.9", URL: "http://dl.example.com/a.exe"}
	for i := 0; i < 2; i++ {
		if err := u.Notify(info); err != nil {
			t.Fatalf("Notify #%d: %v", i, err)
		}
	}
	st := u.Status()
	if st.Phase != core.UpdateAvailable || st.Host != "dl.example.com" {
		t.Errorf("状态异常：%+v", st)
	}

	// 至少收到一次 system.update 事件
	select {
	case ev := <-sub:
		if ev.Name != "system.update" {
			t.Errorf("事件名应为 system.update，实际 %s", ev.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 system.update 事件")
	}
}

func TestDownloadReplaceAndRestart(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	writeFile(t, exe, []byte("OLD-BINARY"))

	newBin := fakeExecutable()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newBin)
	}))
	defer srv.Close()

	restarted := make(chan struct{}, 1)
	u := newTestUpdater(t, Options{
		ExePath: exe,
		Restart: func() error { restarted <- struct{}{}; return nil },
	})
	info := core.UpdateInfo{Version: "9.9.9", URL: srv.URL + "/app.exe", SHA256: sha256Hex(newBin)}
	if err := u.Notify(info); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := u.Download(); err != nil {
		t.Fatalf("Download: %v", err)
	}

	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatalf("等待重启回调超时：%+v", u.Status())
	}

	got, err := os.ReadFile(exe)
	if err != nil || !bytes.Equal(got, newBin) {
		t.Errorf("可执行文件未被替换（err=%v）", err)
	}
	if _, err := os.Stat(exe + oldSuffix); err != nil {
		t.Errorf("旧文件备份不存在: %v", err)
	}
	if _, err := os.Stat(exe + newSuffix); !os.IsNotExist(err) {
		t.Errorf("残留 .new 文件未清理")
	}
}

func TestDownloadFailures(t *testing.T) {
	newBin := fakeExecutable()
	cases := []struct {
		name    string
		handler http.HandlerFunc
		info    func(srvURL string) core.UpdateInfo
		wantMsg string
	}{
		{
			name:    "HTTP 404",
			handler: func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
			info:    func(u string) core.UpdateInfo { return core.UpdateInfo{Version: "9.9.9", URL: u} },
			wantMsg: "404",
		},
		{
			name:    "sha256 不匹配",
			handler: func(w http.ResponseWriter, r *http.Request) { w.Write(newBin) },
			info: func(u string) core.UpdateInfo {
				return core.UpdateInfo{Version: "9.9.9", URL: u, SHA256: sha256Hex([]byte("other"))}
			},
			wantMsg: "校验失败",
		},
		{
			name:    "内容不是可执行文件",
			handler: func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>error</html>")) },
			info:    func(u string) core.UpdateInfo { return core.UpdateInfo{Version: "9.9.9", URL: u} },
			wantMsg: "不是有效",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "app.exe")
			writeFile(t, exe, []byte("OLD"))
			srv := httptest.NewServer(c.handler)
			defer srv.Close()

			u := newTestUpdater(t, Options{ExePath: exe, Restart: func() error { return nil }})
			if err := u.Notify(c.info(srv.URL + "/app.exe")); err != nil {
				t.Fatalf("Notify: %v", err)
			}
			if err := u.Download(); err != nil {
				t.Fatalf("Download: %v", err)
			}
			waitFor(t, "进入失败状态", func() bool { return u.Status().Phase == core.UpdateFailed })

			st := u.Status()
			if !bytes.Contains([]byte(st.Error), []byte(c.wantMsg)) {
				t.Errorf("失败原因应包含 %q，实际 %q", c.wantMsg, st.Error)
			}
			if _, err := os.Stat(exe + newSuffix); !os.IsNotExist(err) {
				t.Errorf("失败后不应留下 .new 文件")
			}
			if _, err := os.Stat(exe + newSuffix + tmpSuffix); !os.IsNotExist(err) {
				t.Errorf("失败后不应留下 .new.tmp 文件")
			}
			// 旧文件必须完好
			if got, _ := os.ReadFile(exe); !bytes.Equal(got, []byte("OLD")) {
				t.Errorf("失败后旧文件被改动")
			}
		})
	}
}

func TestCancelDownload(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	writeFile(t, exe, []byte("OLD"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		w.Write(make([]byte, 4096))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done() // 挂住直到客户端取消
	}))
	defer srv.Close()

	u := newTestUpdater(t, Options{ExePath: exe, Restart: func() error { return nil }})
	if err := u.Notify(core.UpdateInfo{Version: "9.9.9", URL: srv.URL + "/app.exe"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := u.Download(); err != nil {
		t.Fatalf("Download: %v", err)
	}
	waitFor(t, "开始下载", func() bool {
		st := u.Status()
		return st.Phase == core.UpdateDownloading && st.Downloaded > 0
	})

	u.Cancel()
	waitFor(t, "回到可下载状态", func() bool { return u.Status().Phase == core.UpdateAvailable })
	if _, err := os.Stat(exe + newSuffix + tmpSuffix); !os.IsNotExist(err) {
		t.Errorf("取消后应清理临时文件")
	}
}

func TestApplyWaitsForCallEnd(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	writeFile(t, exe, []byte("OLD"))

	newBin := fakeExecutable()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newBin)
	}))
	defer srv.Close()

	var busy atomic.Bool
	busy.Store(true)
	restarted := make(chan struct{}, 1)
	u := newTestUpdater(t, Options{
		ExePath: exe,
		IsBusy:  func() bool { return busy.Load() },
		Restart: func() error { restarted <- struct{}{}; return nil },
	})
	if err := u.Notify(core.UpdateInfo{Version: "9.9.9", URL: srv.URL + "/app.exe"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := u.Download(); err != nil {
		t.Fatalf("Download: %v", err)
	}
	waitFor(t, "进入等待通话结束", func() bool {
		st := u.Status()
		return st.Phase == core.UpdateReady && st.WaitingCall
	})
	// 通话中不应替换
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, []byte("OLD")) {
		t.Fatalf("通话中不应替换可执行文件")
	}

	busy.Store(false) // 通话结束
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatalf("通话结束后未自动重启：%+v", u.Status())
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, newBin) {
		t.Errorf("通话结束后未替换可执行文件")
	}
}

// TestReplaceRollback 注入"新文件就位失败"，验证回滚后旧文件完好。
func TestReplaceRollback(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	writeFile(t, exe, []byte("OLD"))
	writeFile(t, exe+newSuffix, []byte("NEW"))

	// 注入：".new → exe" 这一步改名恒失败（含重试），其余放行
	orig := renameFile
	renameFile = func(oldPath, newPath string) error {
		if strings.HasSuffix(oldPath, newSuffix) {
			return errors.New("注入的改名失败")
		}
		return orig(oldPath, newPath)
	}
	defer func() { renameFile = orig }()

	u := newTestUpdater(t, Options{ExePath: exe})
	if err := u.replaceExe(); err == nil {
		t.Fatal("期望替换失败")
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, []byte("OLD")) {
		t.Errorf("回滚后旧文件应完好，实际 %q", got)
	}
	if _, err := os.Stat(exe + oldSuffix); !os.IsNotExist(err) {
		t.Errorf("回滚后不应残留 .old 备份")
	}
	// 新文件仍在（保留可重试）
	if _, err := os.Stat(exe + newSuffix); err != nil {
		t.Errorf("失败后 .new 应保留以便重试: %v", err)
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	writeFile(t, exe, []byte("APP"))
	for _, suffix := range []string{oldSuffix, newSuffix, newSuffix + tmpSuffix} {
		writeFile(t, exe+suffix, []byte("stale"))
	}

	u := newTestUpdater(t, Options{ExePath: exe})
	u.Cleanup()
	for _, suffix := range []string{oldSuffix, newSuffix, newSuffix + tmpSuffix} {
		if _, err := os.Stat(exe + suffix); !os.IsNotExist(err) {
			t.Errorf("残留文件 %s 未被清理", suffix)
		}
	}
	if _, err := os.Stat(exe); err != nil {
		t.Errorf("主可执行文件被误删: %v", err)
	}
}

func TestDownloadWithoutNotify(t *testing.T) {
	u := newTestUpdater(t, Options{ExePath: filepath.Join(t.TempDir(), "app.exe")})
	if err := u.Download(); err == nil {
		t.Error("未告知新版本时下载应报错")
	}
	if err := u.Apply(); err == nil {
		t.Error("未下载完成时应用更新应报错")
	}
	if got := u.Status().Phase; got != core.UpdateIdle {
		t.Errorf("状态应为 idle，实际 %s", got)
	}
}
