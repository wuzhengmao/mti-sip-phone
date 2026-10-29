package macbundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRootOf 从 exe 路径解析 .app 包根（自安装要靠它判断"已在包内"）。
func TestRootOf(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"/Applications/MTI软电话.app/Contents/MacOS/mti-sip-phone", "/Applications/MTI软电话.app", true},
		{"/Applications/MTI软电话.app", "/Applications/MTI软电话.app", true},
		{"/Volumes/MTI/SIP.app/Contents/MacOS/x", "/Volumes/MTI/SIP.app", true},
		{"/Users/a/Library/Application Support/mti-sip-phone/MTI软电话-无UI版.app/Contents/MacOS/mti-sip-phone-no-ui",
			"/Users/a/Library/Application Support/mti-sip-phone/MTI软电话-无UI版.app", true},
		{"/Users/a/Downloads/mti-sip-phone-no-ui-darwin-arm64", "", false},
		{"/Users/a/foo.app.txt/bar", "", false},
	}
	for _, c := range cases {
		got, ok := RootOf(filepath.FromSlash(c.in))
		want := filepath.FromSlash(c.want)
		if ok != c.wantOK || (c.wantOK && got != want) || (!c.wantOK && got != "") {
			t.Errorf("RootOf(%q) = (%q,%v)，期望 (%q,%v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// TestEnsureCreatesBundle 建包：可执行文件、Info.plist、图标都要在，
// 且 plist 里必须有 sip/sips 两个 URL scheme（否则协议注册无从谈起）。
func TestEnsureCreatesBundle(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sip-phone-no-ui-darwin-arm64")
	if err := os.WriteFile(src, []byte("fake-exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, AppName(true))

	if err := Ensure(root, src, true); err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	exe := ExecutablePath(root, true)
	data, err := os.ReadFile(exe)
	if err != nil || string(data) != "fake-exe" {
		t.Fatalf("包内可执行文件不对: %q err=%v", data, err)
	}
	plist, err := os.ReadFile(InfoPlistPath(root))
	if err != nil {
		t.Fatalf("Info.plist 缺失: %v", err)
	}
	for _, want := range []string{"<string>sip</string>", "<string>sips</string>",
		"<string>mti-sip-phone-no-ui</string>", "<true/>", "CFBundleIconFile"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("Info.plist 缺少 %q", want)
		}
	}
	icon, err := os.ReadFile(IconPath(root))
	if err != nil || len(icon) < 8 || string(icon[0:4]) != "icns" {
		t.Fatalf("图标不是 ICNS: len=%d err=%v", len(icon), err)
	}

	// 幂等：再跑一次不应报错，内容保持不变
	before, _ := os.ReadFile(InfoPlistPath(root))
	if err := Ensure(root, src, true); err != nil {
		t.Fatalf("重复 Ensure 失败: %v", err)
	}
	after, _ := os.ReadFile(InfoPlistPath(root))
	if string(before) != string(after) {
		t.Fatal("重复 Ensure 改写了 Info.plist")
	}
}

// TestAppNameDistinct 两个变体的应用包名必须不同（否则互相覆盖）。
func TestAppNameDistinct(t *testing.T) {
	full, noUI := AppName(false), AppName(true)
	if full == noUI {
		t.Fatalf("两个变体的应用包同名: %s", full)
	}
	if BundleID(false) == BundleID(true) {
		t.Fatal("两个变体的 bundle 标识相同")
	}
	if ExecutableName(false) == ExecutableName(true) {
		t.Fatal("两个变体的包内可执行文件同名")
	}
}
