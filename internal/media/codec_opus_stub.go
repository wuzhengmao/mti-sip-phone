//go:build !audio || !cgo

package media

// 未启用音频扩展编解码库的构建（缺 -tags audio，或 CGO 关闭）：本端不支持 Opus，
// SDP offer 里也不会出现它。构建脚本探测到 third_party/audio/<目标>/lib/libopus.a
// 齐全时会自动加 -tags audio（见 scripts/build-audio-lib.*）。

// optionalCodecs 可选编解码器列表（本构建为空）。
var optionalCodecs []Codec

// opusInitErr 本构建 Opus 实例创建失败的原因（无 Opus 时恒为 nil）。
var opusInitErr error
