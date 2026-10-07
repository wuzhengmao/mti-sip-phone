package ui

import (
	"testing"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/core"
)

// TestDialSuggestKeepsFocus 拨号建议下拉弹出后**焦点必须还在输入框**上。
//
// 这是真机缺陷的回归（用户实测："输入后弹出下拉，再打字就无效，要关掉下拉才能
// 继续"）：Fyne 把顶层 overlay 当成独立的键盘焦点域（画布有 overlay 时
// focusManager() 返回 overlay 自己的），所以无论 PopUpMenu（Show 里
// canvas.Focus(自己)）还是 widget.PopUp，输入框都会收不到键。现在下拉是
// **内容 Stack 里的图层**，不切焦点域——这条测试钉住这一点：把实现换成
// PopUp/PopUpMenu 会立刻失败。
func TestDialSuggestKeepsFocus(t *testing.T) {
	test.NewApp()

	cfg := config.Default()
	cfg.RecentDial = []string{"2901", "1001"}
	app := &App{core: core.New(cfg, nil, core.NewBus())}
	app.dialSelect = newDialEntry(app)
	app.mainSuggest = newDialSuggestLayer()
	app.wireDialInput()

	w := test.NewWindow(container.NewStack(app.dialSelect, app.mainSuggest))
	defer w.Close()
	w.Canvas().Focus(app.dialSelect)
	if got := w.Canvas().Focused(); got != app.dialSelect {
		t.Fatalf("前置条件不成立：焦点应在输入框，实际 %T", got)
	}

	// 命中"最近呼叫" → 应弹出建议，且焦点不动
	app.refreshDialSuggestions("2901")
	if !app.mainSuggest.shown {
		t.Fatal("有匹配时应弹出建议下拉")
	}
	if got := w.Canvas().Focused(); got != app.dialSelect {
		t.Fatalf("建议下拉弹出后焦点应仍在输入框，实际 %T", got)
	}

	// 继续"打字"（内容刷新）：焦点仍不能被抢走
	app.refreshDialSuggestions("290")
	if got := w.Canvas().Focused(); got != app.dialSelect {
		t.Fatalf("刷新建议后焦点应仍在输入框，实际 %T", got)
	}

	// 输入清空 → 列出"最近呼叫"（获得焦点但没输入时也要能看到最近号码）
	app.refreshDialSuggestions("")
	if !app.mainSuggest.shown {
		t.Fatal("清空后应列出最近呼叫")
	}
	if len(app.mainSuggest.rows.Objects) == 0 {
		t.Fatal("清空后建议里应有最近呼叫")
	}

	// 没有匹配也不该弹
	app.refreshDialSuggestions("zzz-不可能匹配")
	if app.mainSuggest.shown {
		t.Fatal("没有匹配时不应弹出建议下拉")
	}
}

// TestDialSuggestPick 点选一条建议：把号码填进输入框并收起下拉。
//
// 注意顺序：真机上"点下拉里的行"会**先**让输入框失焦（Fyne 的驱动在按下时
// canvas.Unfocus()），**再**在抬起时派发 Tapped——所以这里先 Focus(nil) 再点，
// 复刻真实顺序：失焦只安排"延迟收起"，不能立刻藏掉，否则那一行已经没了、
// 抬起时命中不到，点选永远点不中（用户实测："选不上了"）。
func TestDialSuggestPick(t *testing.T) {
	test.NewApp()

	cfg := config.Default()
	cfg.RecentDial = []string{"2901"}
	app := &App{core: core.New(cfg, nil, core.NewBus())}
	app.dialSelect = newDialEntry(app)
	app.mainSuggest = newDialSuggestLayer()
	app.wireDialInput()

	w := test.NewWindow(container.NewStack(app.dialSelect, app.mainSuggest))
	defer w.Close()
	w.Canvas().Focus(app.dialSelect) // 只有有焦点才会弹（见 dialInputFocused）

	app.refreshDialSuggestions("2901")
	if len(app.mainSuggest.rows.Objects) == 0 {
		t.Fatal("应至少有一行建议")
	}
	btn, ok := app.mainSuggest.rows.Objects[0].(*widget.Button)
	if !ok {
		t.Fatalf("建议行应是按钮，实际 %T", app.mainSuggest.rows.Objects[0])
	}
	// ① 按下那一下：输入框失焦（真机上就是这一步把图层藏掉害得点不中）
	w.Canvas().Focus(nil)
	if !app.mainSuggest.shown {
		t.Fatal("失焦只应安排延迟收起，不能立刻藏（否则点选点不中）")
	}
	if app.hideSuggestTimer == nil {
		t.Fatal("失焦后应有「待收起」的定时器")
	}

	// ② 抬起那一下：行还被点得到 → 填入号码并收起
	btn.OnTapped()
	if got := app.dialSelect.Text; got != "2901" {
		t.Fatalf("点选后输入框应填入号码，实际 %q", got)
	}
	if app.mainSuggest.shown {
		t.Fatal("点选后应收起下拉")
	}
	if app.hideSuggestTimer != nil {
		t.Fatal("点选后应取消「待收起」定时器")
	}
}

// TestDialSuggestNeedsFocus 没有焦点时（点击拨号按键、主窗体敲数字）不该弹下拉，
// 聚焦后才弹，失焦后延迟收起（用户要求）。
func TestDialSuggestNeedsFocus(t *testing.T) {
	test.NewApp()

	cfg := config.Default()
	cfg.RecentDial = []string{"2901", "1001"}
	app := &App{core: core.New(cfg, nil, core.NewBus())}
	app.dialSelect = newDialEntry(app)
	app.mainSuggest = newDialSuggestLayer()
	app.wireDialInput()

	w := test.NewWindow(container.NewStack(app.dialSelect, app.mainSuggest))
	defer w.Close()

	// 模拟"点击拨号按键 / 主窗体按键"：SetText 但**输入框没有焦点** → 不该弹
	app.dialSelect.SetText("2901")
	if app.mainSuggest.shown {
		t.Fatal("输入框没有焦点时不应弹出建议下拉")
	}

	// 聚焦后再改内容 → 才弹
	w.Canvas().Focus(app.dialSelect)
	app.dialSelect.SetText("290")
	if !app.mainSuggest.shown {
		t.Fatal("有焦点时 SetText 应弹出建议下拉")
	}
	// 失去焦点 → 收起
	w.Canvas().Focus(nil)
	app.hideDialSuggest() // 失焦钩子在真机上会走到这里；测试直接调用
	if app.mainSuggest.shown {
		t.Fatal("失去焦点后应收起建议下拉")
	}
}
