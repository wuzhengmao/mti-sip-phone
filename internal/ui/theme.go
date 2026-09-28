package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// dialTheme 拨号盘自定义主题：更大的按键内边距与文字、更明显的按钮边框。
// 实现完整 fyne.Theme 接口，未覆盖项转发 theme.DefaultTheme()
//（不能嵌入 nil 接口，会导致未覆盖方法调用崩溃）。
type dialTheme struct{}

func (dialTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if name == theme.ColorNameSeparator {
		// Fyne 按钮边框使用 SeparatorColor，加深后按键边框更明显
		return color.NRGBA{R: 0x8a, G: 0x8f, B: 0x98, A: 0xff}
	}
	return theme.DefaultTheme().Color(name, variant)
}

func (dialTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (dialTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (dialTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameInnerPadding:
		return 12 // 主窗体按键内边距（拨号键更高）；弹窗单独切回默认主题
	case theme.SizeNamePadding:
		return 4 // 全局间距：状态行、布局更紧凑
	case theme.SizeNameInputBorder:
		return 3
	}
	// 文字大小保持默认：配置页等界面字体不应被放大
	return theme.DefaultTheme().Size(name)
}
