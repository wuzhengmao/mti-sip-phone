//go:build !noui

package variant

// NoUI 当前构建是否为「仅托盘无 UI 版」。
const NoUI = false

// UpdateAppID 本变体默认的更新站点应用 ID。
const UpdateAppID = "mti-sip-phone"

// OtherUpdateAppID 另一个变体的默认应用 ID：配置文件里若残留该值
// （两个变体共用一份配置），按"未设置"处理并改回本变体的默认值。
const OtherUpdateAppID = "mti-sip-phone-no-ui"
