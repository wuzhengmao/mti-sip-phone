//go:build !apm || !cgo

package audio

// 本构建没有编进音频预处理（REQ-MEDIA-007）：没有 `apm` 标签（构建脚本没找到
// third_party/audio/<target>/lib/libwebrtc-audio-processing-1.a）或没有 cgo。
// 界面里的三个开关会置灰，通话路径完全不变。
//
// 生成库：sh scripts/build-audio-libs.sh（Windows 用 scripts\build-audio-libs.bat）。

// newPreprocessor 没有 APM 时恒返回 nil（调用方按"直通"处理）。
func newPreprocessor(sampleRate int, p Preprocess) preprocessor { return nil }

func init() { apmAvailable = false }
