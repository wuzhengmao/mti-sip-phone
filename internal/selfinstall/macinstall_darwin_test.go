//go:build darwin

package selfinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mti-sip-phone/internal/macbundle"
	"mti-sip-phone/internal/variant"
)

// TestRelocateInstallsAppBundle macOS 的安装形态：裸二进制 → 固定数据目录下的
// .app 应用包（程序图标与 sip:// 协议都靠它），并从包内可执行文件拉起。
func TestRelocateInstallsAppBundle(t *testing.T) {
	setTargetBase(t)
	noShortcut(t)
	started := fakeStart(t)
	src := writeFakeExe(t, newSrcDir(t), "sip-phone-no-ui-darwin-arm64")
	args := []string{"sip://1001:pwd@10.0.0.1:5060", "-instance", "1"}

	relocated, dst, err := Relocate(src, args)
	if err != nil || !relocated {
		t.Fatalf("应建包并启动：relocated=%v err=%v", relocated, err)
	}
	root, ok := macbundle.RootOf(dst)
	if !ok {
		t.Fatalf("目标不在 .app 包内：%s", dst)
	}
	wantRoot := filepath.Join(targetDirOfTest(t), macbundle.AppName(variant.NoUI))
	if root != wantRoot {
		t.Fatalf("应用包位置 %q，期望 %q", root, wantRoot)
	}
	plist, err := os.ReadFile(macbundle.InfoPlistPath(root))
	if err != nil {
		t.Fatalf("Info.plist 缺失: %v", err)
	}
	if !strings.Contains(string(plist), "<string>sips</string>") {
		t.Fatal("Info.plist 未声明 sips URL scheme")
	}
	if _, err := os.Stat(macbundle.IconPath(root)); err != nil {
		t.Fatalf("应用包图标缺失: %v", err)
	}
	if *started == nil {
		t.Fatal("应从包内可执行文件拉起新进程")
	}
	if (*started).Args[0] != dst {
		t.Fatalf("启动的是 %q，期望 %q", (*started).Args[0], dst)
	}
}

// TestInInstalledAppBundle 已装好的判定：DMG 挂载点与 App Translocation
// 的临时路径都不算（要从那里搬出来）。
func TestInInstalledAppBundle(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/Applications/MTI软电话-无UI版.app/Contents/MacOS/mti-sip-phone-no-ui", true},
		{"/Users/a/Library/Application Support/mti-sip-phone/MTI软电话.app/Contents/MacOS/mti-sip-phone", true},
		{"/Volumes/MTI软电话/MTI软电话-无UI版.app/Contents/MacOS/mti-sip-phone-no-ui", false},
		{"/private/var/folders/x/AppTranslocation/123/d/MTI软电话-无UI版.app/Contents/MacOS/mti-sip-phone-no-ui", false},
		{"/Users/a/Downloads/mti-sip-phone-no-ui-darwin-arm64", false},
	}
	for _, c := range cases {
		if got := inInstalledAppBundle(c.path); got != c.want {
			t.Errorf("inInstalledAppBundle(%q) = %v，期望 %v", c.path, got, c.want)
		}
	}
}
