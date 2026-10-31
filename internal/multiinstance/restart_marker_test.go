package multiinstance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setConfigDir 把主配置目录重定向到临时目录（与 config.ConfigDir 的平台口径一致）。
func setConfigDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("APPDATA", base)         // Windows
	t.Setenv("XDG_CONFIG_HOME", base) // Linux
	t.Setenv("HOME", base)            // macOS
	return filepath.Join(base, "mti-sip-phone")
}

// TestMarkerRoundTrip 标记写入/读取；文件不存在时返回 os.ErrNotExist。
func TestMarkerRoundTrip(t *testing.T) {
	setConfigDir(t)

	if _, err := ReadMarker(); !os.IsNotExist(err) {
		t.Fatalf("标记不存在时应返回 os.ErrNotExist，实际 %v", err)
	}

	want := Marker{Version: "1.1.0", Sha256: "abc123", SourceSlot: 2, UpdatedAt: 1761990000000}
	if err := WriteMarker(want); err != nil {
		t.Fatalf("写入标记失败: %v", err)
	}
	got, err := ReadMarker()
	if err != nil {
		t.Fatalf("读取标记失败: %v", err)
	}
	if got != want {
		t.Fatalf("标记内容不一致: got %+v want %+v", got, want)
	}

	// 覆盖写（Windows 上目标已存在时的删除+改名分支）
	want.Version = "1.0.23"
	if err := WriteMarker(want); err != nil {
		t.Fatalf("覆盖写入标记失败: %v", err)
	}
	if got, _ = ReadMarker(); got.Version != "1.0.23" {
		t.Fatalf("覆盖后版本应为 1.0.23，实际 %q", got.Version)
	}
}

// TestFileSHA256 指纹用于判断"当前运行的镜像是否已被替换"。
func TestFileSHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.bin")
	if err := os.WriteFile(p, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := FileSHA256(p)
	if err != nil {
		t.Fatalf("计算指纹失败: %v", err)
	}
	if want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
		t.Fatalf("sha256 = %s，期望 %s", got, want)
	}
}

// TestStaleFor 判定口径：自己写的、早于启动的、指纹相同的标记都不触发。
func TestStaleFor(t *testing.T) {
	start := time.UnixMilli(1761990000000)
	opts := WatchOptions{OwnSlot: 0, OwnFingerprint: "old", OwnVersion: "1.0.21", StartedAt: start}

	cases := []struct {
		name string
		m    Marker
		want bool
	}{
		{"别的实例的新标记", Marker{Version: "1.1.0", Sha256: "new", SourceSlot: 1, UpdatedAt: start.UnixMilli() + 1}, true},
		{"自己写的标记", Marker{Version: "1.1.0", Sha256: "new", SourceSlot: 0, UpdatedAt: start.UnixMilli() + 1}, false},
		{"早于本进程启动", Marker{Version: "1.1.0", Sha256: "new", SourceSlot: 1, UpdatedAt: start.UnixMilli() - 1}, false},
		{"指纹相同（已是新版本）", Marker{Version: "1.1.0", Sha256: "old", SourceSlot: 1, UpdatedAt: start.UnixMilli() + 1}, false},
		{"无指纹时版本相同", Marker{Version: "1.0.21", SourceSlot: 1, UpdatedAt: start.UnixMilli() + 1}, false},
		{"无指纹时版本不同", Marker{Version: "1.1.0", SourceSlot: 1, UpdatedAt: start.UnixMilli() + 1}, true},
	}
	for _, c := range cases {
		if got := staleFor(c.m, opts); got != c.want {
			t.Errorf("%s: staleFor = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestWatchWaitsForIdle 有通话时不动，通话结束后才回调（回调后 Watch 返回）。
func TestWatchWaitsForIdle(t *testing.T) {
	setConfigDir(t)
	start := time.Now()

	busy := true
	fired := make(chan Marker, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Watch(ctx, WatchOptions{
		OwnSlot:        0,
		OwnFingerprint: "old",
		OwnVersion:     "1.0.21",
		StartedAt:      start,
		Interval:       10 * time.Millisecond,
		IsBusy:         func() bool { return busy },
		OnStale:        func(m Marker) { fired <- m },
	})

	if err := WriteMarker(Marker{Version: "1.1.0", Sha256: "new", SourceSlot: 1, UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("写入标记失败: %v", err)
	}

	select {
	case <-fired:
		t.Fatal("通话进行中不应重启")
	case <-time.After(120 * time.Millisecond):
	}

	busy = false
	select {
	case m := <-fired:
		if m.Version != "1.1.0" {
			t.Fatalf("回调版本应为 1.1.0，实际 %q", m.Version)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("空闲后应回调 OnStale")
	}
}

// TestWatchIgnoresOwnMarker 自己槽位写入的标记不触发重启（该实例自己会重启）。
func TestWatchIgnoresOwnMarker(t *testing.T) {
	setConfigDir(t)
	start := time.Now()

	fired := make(chan Marker, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Watch(ctx, WatchOptions{
		OwnSlot:        1,
		OwnFingerprint: "old",
		OwnVersion:     "1.0.21",
		StartedAt:      start,
		Interval:       10 * time.Millisecond,
		IsBusy:         func() bool { return false },
		OnStale:        func(m Marker) { fired <- m },
	})

	if err := WriteMarker(Marker{Version: "1.1.0", Sha256: "new", SourceSlot: 1, UpdatedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("写入标记失败: %v", err)
	}
	select {
	case <-fired:
		t.Fatal("自己槽位的标记不应触发重启")
	case <-time.After(120 * time.Millisecond):
	}
}
