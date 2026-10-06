package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestChkOptDefaultOn 服务端心跳检测默认开启（REQ-ACC-020）：
// Load 是先取 Default() 再反序列化覆盖，所以旧配置文件里没有 optchk 字段时
// 必须仍为 true（升级上来的用户不用手动勾选）。
func TestChkOptDefaultOn(t *testing.T) {
	if !Default().Account.ChkOpt {
		t.Fatal("默认配置应启用服务端心跳检测")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"version":1,"account":{"server":"10.0.0.1","account":"1001"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if !cfg.Account.ChkOpt {
		t.Fatal("旧配置缺少 optchk 字段时应按默认开启")
	}

	off := `{"version":1,"account":{"server":"10.0.0.1","account":"1001","optchk":false}}`
	if err := os.WriteFile(path, []byte(off), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Account.ChkOpt {
		t.Fatal("配置里显式关闭 optchk 时不应被默认值覆盖")
	}
}

// TestInstanceDir 多实例沙箱目录（REQ-NFR-007）：
// 槽位 0 = 原目录（**位置不变**，向后兼容）；槽位 ≥1 = <原目录>/instances/<N>/ 且被创建。
func TestInstanceDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("APPDATA", base)         // Windows：os.UserConfigDir 读 APPDATA
	t.Setenv("XDG_CONFIG_HOME", base) // Linux

	root, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := InstanceDir(0); err != nil || got != root {
		t.Fatalf("槽位 0 应返回原目录 %q，实际 %q err=%v", root, got, err)
	}
	if got, err := InstanceDir(-1); err != nil || got != root {
		t.Fatalf("负槽位应按主实例处理，实际 %q err=%v", got, err)
	}
	dir, err := InstanceDir(2)
	if err != nil {
		t.Fatalf("创建沙箱目录失败: %v", err)
	}
	if want := filepath.Join(root, "instances", "2"); dir != want {
		t.Fatalf("槽位 2 目录应为 %q，实际 %q", want, dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("槽位目录应已创建: %v", err)
	}
}

// TestVideoEnabledDefaultOn 视频通话总开关默认启用（REQ-VIDEO-014）：
// 旧配置文件里没有 video.disabled 字段时必须仍是启用状态（升级上来的用户
// 不会被"静默关掉视频"）；显式写了 disabled=true 才关闭。
func TestVideoEnabledDefaultOn(t *testing.T) {
	if !Default().Video.Enabled() {
		t.Fatal("默认配置应启用视频通话")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"version":1,"account":{"server":"10.0.0.1","account":"1001"},"video":{"cameraId":"","width":640,"height":480}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if !cfg.Video.Enabled() {
		t.Fatal("旧配置缺少 disabled 字段时应按默认启用视频通话")
	}
	if !cfg.Video.CameraUsable() {
		t.Fatal("视频启用时摄像头应可用")
	}

	off := `{"version":1,"video":{"disabled":true}}`
	if err := os.WriteFile(path, []byte(off), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Video.Enabled() {
		t.Fatal("配置里显式关闭视频通话时不应被默认值覆盖")
	}
	if cfg.Video.CameraUsable() {
		t.Fatal("视频关闭时摄像头应算不可用")
	}
}

// TestTransportNormalize 传输协议规范化（REQ-ACC-006）：大小写/空白容错，
// 空值与非法值回退 udp（旧配置没有 tls，行为与从前一致）；
// tls 的服务端默认端口是 5061，其余 5060。
func TestTransportNormalize(t *testing.T) {
	cases := map[string]string{
		"":      "udp",
		"UDP":   "udp",
		"tcp":   "tcp",
		" TCP ": "tcp",
		"TLS":   "tls",
		"tls":   "tls",
		"sips":  "udp", // 非法值回退
		"quic":  "udp",
	}
	for in, want := range cases {
		if got := NormalizeTransport(in); got != want {
			t.Fatalf("NormalizeTransport(%q) = %q，期望 %q", in, got, want)
		}
	}
	if p := DefaultPortForTransport("tls"); p != 5061 {
		t.Fatalf("tls 默认端口应为 5061，实际 %d", p)
	}
	if p := DefaultPortForTransport("tcp"); p != 5060 {
		t.Fatalf("tcp 默认端口应为 5060，实际 %d", p)
	}
}

// TestSRTPNormalize 媒体加密策略的规范化（REQ-MEDIA-009）：
// 空值/非法值一律回退默认「支持」（旧配置没有该字段）。
func TestSRTPNormalize(t *testing.T) {
	cases := map[string]string{
		"":           SRTPSupported,
		"off":        SRTPOff,
		"OFF":        SRTPOff,
		" supported": SRTPSupported,
		"preferred":  SRTPPreferred,
		" Required":  SRTPRequired,
		"yes":        SRTPSupported, // 非法值回退
		"none":       SRTPSupported,
	}
	for in, want := range cases {
		if got := NormalizeSRTP(in); got != want {
			t.Fatalf("NormalizeSRTP(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 默认配置即为「支持」
	if got := NormalizeSRTP(Default().Account.SRTP); got != SRTPSupported {
		t.Fatalf("默认应为「支持」，实际 %q", got)
	}
}

// TestDomainNormalize SIP 域名规范化：去前缀/user@/端口（REQ-ACC-022）。
func TestDomainNormalize(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"  sip.mti-sh.cn  ":         "sip.mti-sh.cn",
		"sip:sip.mti-sh.cn":         "sip.mti-sh.cn",
		"sips:sip.mti-sh.cn":        "sip.mti-sh.cn",
		"sip:alice@sip.mti-sh.cn":   "sip.mti-sh.cn",
		"sip.mti-sh.cn:5060":        "sip.mti-sh.cn",
		"sip://sip.mti-sh.cn:5061":  "sip.mti-sh.cn",
		"sip:1001@example.com:5060": "example.com",
	}
	for in, want := range cases {
		if got := NormalizeDomain(in); got != want {
			t.Errorf("NormalizeDomain(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestPublicIPNormalize 声明 IP 只接受 IP 字面量（SDP 的 c= 行必须写 IP）。
func TestPublicIPNormalize(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"  10.168.5.10  ":  "10.168.5.10",
		"192.168.3.15":     "192.168.3.15",
		"[::1]":            "::1",
		"example.com":      "", // 域名不行（SDP 会非法）
		"10.168.5.10:5060": "",
		"999.1.1.1":        "",
		"sip:10.168.5.10":  "",
	}
	for in, want := range cases {
		if got := NormalizePublicIP(in); got != want {
			t.Errorf("NormalizePublicIP(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestUIConfigWindowFieldsCompat 窗口位置记忆与视频窗口状态的兼容性
// （REQ-UI-040 / REQ-VIDEO-015）：旧配置没有这些字段时全部落零——移动标志
// false 表示"从没移动过，按默认位置摆放"，置顶 false，旋转 0；显式写入的
// 值 Save→Load 往返保真（副屏的负坐标也要保住）。
func TestUIConfigWindowFieldsCompat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"version":1,"account":{"server":"10.0.0.1","account":"1001"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.UI.CallPanel.Moved || cfg.UI.VideoWindow.Moved {
		t.Fatal("旧配置缺字段时应视为「从未移动过」（Moved=false）")
	}
	if cfg.UI.VideoTopmost || cfg.UI.LocalPreviewRotation != 0 || cfg.UI.LocalPreviewMirror {
		t.Fatal("旧配置缺字段时置顶应为 false、本地旋转/镜像应为零值")
	}

	cfg.UI.CallPanel = WindowPlacement{Moved: true, X: 1200, Y: 800}
	cfg.UI.VideoWindow = WindowPlacement{Moved: true, X: -300, Y: 40} // 左侧副屏：负坐标
	cfg.UI.VideoTopmost = true
	cfg.UI.LocalPreviewRotation = 270
	cfg.UI.LocalPreviewMirror = true
	if err := cfg.Save(path); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if got.UI.CallPanel != (WindowPlacement{Moved: true, X: 1200, Y: 800}) {
		t.Errorf("通话面板位置往返不一致: %+v", got.UI.CallPanel)
	}
	if got.UI.VideoWindow != (WindowPlacement{Moved: true, X: -300, Y: 40}) {
		t.Errorf("视频窗口位置往返不一致: %+v", got.UI.VideoWindow)
	}
	if !got.UI.VideoTopmost || got.UI.LocalPreviewRotation != 270 || !got.UI.LocalPreviewMirror {
		t.Errorf("置顶/本地旋转镜像往返不一致: topmost=%v rot=%d mirror=%v",
			got.UI.VideoTopmost, got.UI.LocalPreviewRotation, got.UI.LocalPreviewMirror)
	}
}

// TestNormalizeVideoRotation 旋转角规范化：取 360 的模，非 90 的整倍数归 0
// （手改配置写坏时不留半吊子角度）。
func TestNormalizeVideoRotation(t *testing.T) {
	cases := map[int]int{
		0: 0, 90: 90, 180: 180, 270: 270,
		360: 0, 450: 90, 720: 0,
		-90: 270, -450: 270,
		45: 0, 91: 0, 999: 0, -1: 0,
	}
	for in, want := range cases {
		if got := NormalizeVideoRotation(in); got != want {
			t.Errorf("NormalizeVideoRotation(%d) = %d，期望 %d", in, got, want)
		}
	}

	// Load 也要规范化（旧配置/手改配置里的非法角度不该进内存）
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"version":1,"account":{"server":"10.0.0.1","account":"1001"},"ui":{"localPreviewRotation":450}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.UI.LocalPreviewRotation != 90 {
		t.Errorf("加载时未规范化旋转角: %d，期望 90", cfg.UI.LocalPreviewRotation)
	}
}
