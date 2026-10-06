//go:build apm && cgo

package audio

// 音频预处理（REQ-MEDIA-007）：WebRTC APM 的 cgo 封装。
//
// 为什么单独一个构建标签（而不是并进 audio）：它是一套独立的第三方库
// （webrtc-audio-processing + abseil，见 scripts/build-audio-libs.sh 的 APM
// 目标），有它没它都不影响 Opus/G.722；缺库时构建脚本不加这个标签，程序照常
// 编（只是设置页里三个开关置灰）。
//
// 头文件路径与 -L 路径由构建脚本经 CGO_CFLAGS/CGO_LDFLAGS 传入
// （third_party/audio/<target>/{include,lib}）；下面的 LDFLAGS 只列库名与
// 平台系统库。abseil 的库清单是 pkg-config --libs webrtc-audio-processing-1
// 的输出（abseil 版本变了要重新对一遍；链接器只按需拉取，列全不会变胖）。
//
// ⚠️ 下面的 C 前导块必须保持**纯 ASCII**：cgo 会把紧邻 import "C" 的整段注释
// 当 C 代码送进编译器，里面的非 ASCII 字符/反引号会被当成"非法字符"报一堆
// stray '...' in program（实测踩过）。中文说明一律留在上面的 Go 文档注释里。

/*
#cgo CXXFLAGS: -std=c++17 -O2
#cgo windows LDFLAGS: -lwebrtc-audio-processing-1 -lwinmm -labsl_flags_internal -labsl_flags_marshalling -labsl_flags_reflection -labsl_flags_private_handle_accessor -labsl_flags_commandlineflag -labsl_flags_commandlineflag_internal -labsl_flags_config -labsl_flags_program_name -labsl_raw_hash_set -labsl_cord -labsl_cordz_info -labsl_cord_internal -labsl_cordz_functions -labsl_cordz_handle -labsl_crc_cord_state -labsl_crc32c -labsl_crc_internal -labsl_crc_cpu_detect -labsl_str_format_internal -labsl_hash -labsl_city -labsl_hashtablez_sampler -labsl_exponential_biased -labsl_synchronization -labsl_graphcycles_internal -labsl_kernel_timeout_internal -labsl_stacktrace -labsl_symbolize -ldbghelp -labsl_debugging_internal -labsl_demangle_internal -labsl_demangle_rust -labsl_decode_rust_punycode -labsl_utf8_for_code_point -labsl_malloc_internal -labsl_time -labsl_civil_time -labsl_strings -labsl_strings_internal -labsl_string_view -labsl_int128 -labsl_throw_delegate -labsl_time_zone -labsl_tracing_internal -labsl_base -ladvapi32 -labsl_raw_logging_internal -labsl_log_severity -labsl_spinlock_wait -lwebrtc-audio-coding-1 -lstdc++ -lwinpthread
#cgo linux LDFLAGS: -lwebrtc-audio-processing-1 -labsl_flags_internal -labsl_flags_marshalling -labsl_flags_reflection -labsl_flags_private_handle_accessor -labsl_flags_commandlineflag -labsl_flags_commandlineflag_internal -labsl_flags_config -labsl_flags_program_name -labsl_raw_hash_set -labsl_cord -labsl_cordz_info -labsl_cord_internal -labsl_cordz_functions -labsl_cordz_handle -labsl_crc_cord_state -labsl_crc32c -labsl_crc_internal -labsl_crc_cpu_detect -labsl_str_format_internal -labsl_hash -labsl_city -labsl_hashtablez_sampler -labsl_exponential_biased -labsl_synchronization -labsl_graphcycles_internal -labsl_kernel_timeout_internal -labsl_stacktrace -labsl_symbolize -labsl_debugging_internal -labsl_demangle_internal -labsl_demangle_rust -labsl_decode_rust_punycode -labsl_utf8_for_code_point -labsl_malloc_internal -labsl_time -labsl_civil_time -labsl_strings -labsl_strings_internal -labsl_string_view -labsl_int128 -labsl_throw_delegate -labsl_time_zone -labsl_tracing_internal -labsl_base -labsl_raw_logging_internal -labsl_log_severity -labsl_spinlock_wait -lwebrtc-audio-coding-1 -lstdc++ -lm -lpthread

#include <stdlib.h>

// C bridge lives in apm_bridge.cc (same package, compiled with CXX)
void* apm_create(int rate_hz, int aec, int ns, int agc);
int apm_process(void* handle, short* mic, const short* rev, int n);
void apm_destroy(void* handle);
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"

	"mti-sip-phone/internal/logx"
)

func init() { apmAvailable = true }

// apmProcessor WebRTC APM 的 Go 侧封装：把任意帧长的采集/播放样本切成 10ms 帧
// 送去处理（分帧逻辑见 apm_frames.go）。
type apmProcessor struct {
	h        unsafe.Pointer
	framer   *apmFramer
	failOnce sync.Once
}

// newPreprocessor 按开关创建预处理器；本构建没有 APM（没带 apm 标签）时由
// apm_stub.go 提供同名函数返回 nil。初始化失败也返回 nil（调用方退化为直通，
// 通话不受影响）。
func newPreprocessor(sampleRate int, p Preprocess) preprocessor {
	if !p.Enabled() {
		return nil
	}
	if sampleRate%100 != 0 {
		logx.Logger().Warn("采样率不是 100 的整数倍，无法切 10ms 帧，音频预处理跳过", "rate", sampleRate)
		return nil
	}
	cAEC, cNS, cAGC := 0, 0, 0
	if p.AEC {
		cAEC = 1
	}
	if p.NS {
		cNS = 1
	}
	if p.AGC {
		cAGC = 1
	}
	h := C.apm_create(C.int(sampleRate), C.int(cAEC), C.int(cNS), C.int(cAGC))
	if h == nil {
		logx.Logger().Warn("音频预处理初始化失败，本通通话不做 AEC/NS/AGC")
		return nil
	}
	logx.Logger().Info("音频预处理已启用",
		"采样率", sampleRate, "回声消除", p.AEC, "降噪", p.NS, "自动增益", p.AGC)
	return &apmProcessor{h: h, framer: newAPMFramer(sampleRate/100, maxCallbackFrames)}
}

// Process 就地处理采集样本，返回本次可用样本数（见 preprocessor 接口）。
func (a *apmProcessor) Process(mic, ref []int16) int {
	return a.framer.Filter(mic, ref, func(micFrame, refFrame []int16) {
		if n := C.apm_process(a.h,
			(*C.short)(unsafe.Pointer(&micFrame[0])),
			(*C.short)(unsafe.Pointer(&refFrame[0])),
			C.int(len(micFrame))); n != 0 {
			// 只在异常时报（限频由 logx 的调用点控制不了，这里判断一次即可）：
			// 帧长不对、或 APM 内部处理返回错误码
			a.failOnce.Do(func() {
				logx.Logger().Warn("音频预处理的单帧处理失败，后续帧可能也未生效", "ret", int(n))
			})
		}
	})
}

// Close 释放 APM 实例。
func (a *apmProcessor) Close() {
	if a.h != nil {
		C.apm_destroy(a.h)
		a.h = nil
	}
}

// 编译期确认实现了 preprocessor。
var _ preprocessor = (*apmProcessor)(nil)

// apmBuildInfo 供日志/诊断：桥接版本号（改了桥接语义时递增）
func apmBuildInfo() string { return fmt.Sprintf("webrtc-audio-processing bridge v1") }
