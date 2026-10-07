//go:build noui

package variant

// NoUI 当前构建是否为「仅托盘无 UI 版」。
const NoUI = true

// UpdateAppID 本变体默认的更新站点应用 ID。
// 无 UI 版在更新站点上是**独立的应用**（与完整版分开归档、各自一个上传令牌）。
const UpdateAppID = "mti-sip-phone-no-ui"

// OtherUpdateAppID 另一个变体的默认应用 ID：配置文件里若残留该值
// （两个变体共用一份配置），按"未设置"处理并改回本变体的默认值。
// 这条规则对无 UI 版尤其关键：否则它会去查完整版的发布物，把自己
// （os.Executable()）替换成完整版 exe。
const OtherUpdateAppID = "mti-sip-phone"
