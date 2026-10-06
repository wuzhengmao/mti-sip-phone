//go:build apm && cgo

// 音频预处理（REQ-MEDIA-007）的 C++ 桥接。
//
// webrtc-audio-processing 1.x **只有 C++ API**，cgo 不能直接调，所以这里放一层
// 极薄的 extern "C" 包装：建实例（按开关配 AEC/NS/AGC）、按 10ms 帧处理
//（先 ProcessReverseStream 送播放参考，再 ProcessStream 处理采集）、销毁。
//
// 编译与链接由 cgo 负责：本文件与 apm.go 同包，cgo 会用 CXX（g++）编它、
// 并在链接时走 C++ 驱动；头文件路径与库路径由构建脚本经 CGO_CFLAGS/CGO_LDFLAGS
// 传入（third_party/audio/<target>/{include,lib}），库名写在 apm.go 的
// #cgo LDFLAGS 里。

#include <cstdint>
#include <cstring>
#include <vector>

#include "modules/audio_processing/include/audio_processing.h"

namespace {

struct ApmHandle {
  rtc::scoped_refptr<webrtc::AudioProcessing> apm;
  int rate_hz = 0;
  std::vector<int16_t> out16;  // 处理缓冲（采集）
  std::vector<int16_t> rev16;  // 处理缓冲（参考，先放参考再原地处理）
};

}  // namespace

// apm_create 建实例并配好开关。返回 NULL 表示初始化失败（调用方退化为直通）。
extern "C" void* apm_create(int rate_hz, int aec, int ns, int agc) {
  ApmHandle* h = new ApmHandle();
  h->rate_hz = rate_hz;
  h->out16.assign(rate_hz / 100, 0);
  h->rev16.assign(rate_hz / 100, 0);

  webrtc::AudioProcessingBuilder builder;
  h->apm = builder.Create();
  if (!h->apm) {
    delete h;
    return nullptr;
  }
  webrtc::AudioProcessing::Config cfg;
  cfg.echo_canceller.enabled = (aec != 0);
  // ⚠️ 刻意**不开** mobile_mode：实测 webrtc-audio-processing 1.3 里
  // mobile_mode 与 NS/AGC 同时开启时，ProcessStream 会直接返回
  // -11（kStreamParameterNotSetError）——8k/16k 都一样，纯桌面档则所有组合正常
  //（见本文件同目录的说明与提交信息）。默认的桌面档多花一点 CPU，但能工作。
  cfg.noise_suppression.enabled = (ns != 0);
  cfg.gain_controller1.enabled = (agc != 0);
  h->apm->ApplyConfig(cfg);
  if (h->apm->Initialize() != webrtc::AudioProcessing::kNoError) {
    delete h;
    return nullptr;
  }
  return h;
}

// apm_process 处理一帧：n 必须是 10ms 的样本数（采样率/100），否则返回 -1。
// mic 就地改写，rev（参考）只读。用 int16 接口：帧本来就是 16 位整数，
// 少一层 float 转换，也避开 float 路径上的怪癖。
extern "C" int apm_process(void* handle, short* mic, const short* rev, int n) {
  if (handle == nullptr) {
    return -1;
  }
  ApmHandle* h = static_cast<ApmHandle*>(handle);
  if (n != h->rate_hz / 100) {
    return -1;
  }
  webrtc::StreamConfig sc(h->rate_hz, 1);
  h->rev16.assign(rev, rev + n);
  h->out16.assign(mic, mic + n);
  int rr = h->apm->ProcessReverseStream(h->rev16.data(), sc, sc, h->rev16.data());
  int rs = h->apm->ProcessStream(h->out16.data(), sc, sc, h->out16.data());
  if (rr != 0 || rs != 0) {
    return rr != 0 ? rr : rs;  // 把 APM 的错误码带出去（-11 = 流参数没设上）
  }
  for (int i = 0; i < n; i++) {
    mic[i] = h->out16[i];
  }
  return 0;
}

extern "C" void apm_destroy(void* handle) {
  delete static_cast<ApmHandle*>(handle);
}
