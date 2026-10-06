package audio

// 音频预处理（REQ-MEDIA-007）：回声消除（AEC）、降噪（NS）、自动增益（AGC）。
//
// 实现在 WebRTC 的音频处理模块（webrtc-audio-processing，见
// scripts/build-audio-libs.sh 的 APM 目标与 internal/audio/apm_bridge.cc）；
// **只有带 `apm` 构建标签**（构建脚本探测到 third_party/audio/<target>/lib/
// libwebrtc-audio-processing-1.a 时自动加）才编进去，没有标签时本文件照常工作，
// 只是 newPreprocessor 恒返回 nil、界面里的开关置灰不可点。

// Preprocess 预处理的三个开关（来自配置 audio.aec/ns/agc）。
type Preprocess struct {
	AEC bool // 回声消除：用"正在播放的样本"当参考，消掉麦克风里的扬声器回声
	NS  bool // 降噪：压稳态底噪
	AGC bool // 自动增益：把音量拉到目标电平
}

// Enabled 是否有任何一项开启（全关就不必建 APM 实例）。
func (p Preprocess) Enabled() bool { return p.AEC || p.NS || p.AGC }

// preprocessor 预处理器：接住**同一拍**的采集样本与播放样本（任意帧长），
// 就地处理采集样本。播放样本是 AEC 的参考信号，必须与采集严格同拍——
// 这正是 malgo 全双工回调天然提供的东西（见 backend_malgo.go）。
type preprocessor interface {
	// Process 就地处理 mic，返回本次可用样本数（不足 10ms 的尾巴留在内部）。
	// ref 只读，长度与 mic 相同。
	Process(mic, ref []int16) int
	// Close 释放底层资源。
	Close()
}

// PreprocessSupported 本构建是否编进了音频预处理（构建脚本按 APM 静态库是否
// 存在决定加不加 `apm` 标签）。界面据此决定三个开关是否可点。
func PreprocessSupported() bool { return apmAvailable }

// apmAvailable 由 apm.go（带标签）或 apm_stub.go（不带）赋值。
var apmAvailable bool
