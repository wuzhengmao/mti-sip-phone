package tray

import (
	"bytes"
	"image/png"
	"testing"

	"mti-sip-phone/internal/appicon"
)

// 托盘图标必须在**没有界面**的情况下也能生成：光栅化失败会退化成纯色圆点，
// 而图标彻底缺失会让用户连右键菜单都点不到（没有别的退出入口），所以这条要有回归。
func TestLoadIcon(t *testing.T) {
	data := loadIcon(appicon.PhoneGreenSVG(), appicon.ColorGreen)
	if len(data) == 0 {
		t.Fatal("托盘图标为空（托盘会没有图标）")
	}
	// Windows 要 ICO（魔数 00 00 01 00），其余平台要 PNG（89 50 4E 47）
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0x00}) {
		return // ICO
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x89, 0x50, 0x4e, 0x47}) {
		return // PNG
	}
	t.Fatalf("图标既不是 ICO 也不是 PNG（前 4 字节 % x）", data[:min(4, len(data))])
}

// 光栅化要真的画出东西：纯透明图（尺寸对但什么都没画）在托盘上就是"看不见"。
func TestRasterizeDrawsPixels(t *testing.T) {
	img, err := rasterizeSVG(appicon.PhoneRedSVG())
	if err != nil {
		t.Fatalf("SVG 光栅化失败: %v", err)
	}
	if b := img.Bounds(); b.Dx() != iconSize || b.Dy() != iconSize {
		t.Fatalf("图标尺寸应为 %d×%d，实际 %d×%d", iconSize, iconSize, b.Dx(), b.Dy())
	}
	opaque := 0
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				opaque++
			}
		}
	}
	if opaque < 100 {
		t.Fatalf("图标几乎全透明（不透明像素 %d），托盘上会看不见", opaque)
	}
}

// PNG 编码可解码，且像素仍是画出来的那批。
func TestEncodePNG(t *testing.T) {
	data, err := encodePNG(fallbackIcon(appicon.ColorGreen))
	if err != nil {
		t.Fatalf("PNG 编码失败: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PNG 解码失败: %v", err)
	}
	if _, _, _, a := img.At(iconSize/2, iconSize/2).RGBA(); a == 0 {
		t.Fatal("兜底圆点中心应为不透明")
	}
}

// 悬停提示文案：标题 + 附加信息（本机号码），与完整版一致——
// **不带版本号、也不带状态后缀**（注册状态由图标颜色表达，用户要求）。
func TestTooltipText(t *testing.T) {
	state.mu.Lock()
	state.ready = false // 不碰真实 systray
	state.opts = Options{Title: "mti-sip-phone"}
	state.detail = ""
	state.registered = false
	state.signedIn = false
	state.accountItem = nil
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.detail, state.registered, state.signedIn = "", false, false
		state.accountItem = nil
		state.mu.Unlock()
	}()

	// applyTooltip/applyIcon 在未就绪时只更新状态、不调 systray
	SetState("registered", " - 2999")
	applyTooltip()
	applyIcon()

	got := tooltipText()
	want := "mti-sip-phone - 2999"
	if got != want {
		t.Fatalf("提示文案 = %q，期望 %q", got, want)
	}
	// 注册状态变化只换图标颜色，文案不变（与桌面口径一致）
	SetState("unregistered", " - 2999")
	if got := tooltipText(); got != want {
		t.Fatalf("未注册时提示文案 = %q，期望 %q", got, want)
	}
	// 未配置账号时只有应用名
	SetState("unregistered", "")
	if got := tooltipText(); got != "mti-sip-phone" {
		t.Fatalf("未配置账号时提示文案 = %q", got)
	}
}

// 菜单项标签与「已注册侧」判定（REQ-ACC-024）：registered 与 registering
// 都显示「退出账号」（点它=退出），其余显示「登录账号」（点它=登录）。
func TestAccountLabel(t *testing.T) {
	cases := []struct {
		regState string
		want     string
	}{
		{"registered", "退出账号"},
		{"registering", "退出账号"},
		{"unregistered", "登录账号"},
		{"failed", "登录账号"},
	}
	for _, tc := range cases {
		if got := accountLabel(tc.regState == "registered" || tc.regState == "registering"); got != tc.want {
			t.Fatalf("regState=%q 标签 = %q，期望 %q", tc.regState, got, tc.want)
		}
	}
}
