package audio

// 音频回调诊断：默认关闭，**零开销**（一个 bool 判断即返回）。
//
// 环境变量 SIP_PHONE_AUDIODUMP=1 打开后，每秒汇总一行「【诊断】音频回调汇总」：
// 回调次数、消费样本数、帧长范围。与媒体层的「播放回调次数/回调请求样本数」
// 对照可区分两类成因：
//   - 两边一致且都低于 50/s × 160：设备侧就少给数据（驱动/设备忙、重采样路径）；
//   - 音频侧偏低而媒体侧请求正常：回调被阻塞（帧长被合并）或播放路径卡住。
//
// 正常值：回调次数 ≈ 50/s、消费样本 ≈ 采样率/s（8 kHz 管线 = 8000、帧长 160；
// 协商到 G.722 的 16 kHz 管线 = 16000、帧长 320）。
// 曾经靠它定位到"日志轮转卡住音频线程"（回调掉到 26/s，见 internal/logx 的
// rotate 注释）。

import (
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"mti-sip-phone/internal/logx"
)

var (
	diagEnabled = os.Getenv("SIP_PHONE_AUDIODUMP") != ""
	diagCalls   atomic.Uint64
	diagFrames  atomic.Uint64
	diagMinF    atomic.Uint64
	diagMaxF    atomic.Uint64

	diagRunning atomic.Bool
)

func diagStat(frames uint32) {
	if !diagEnabled {
		return
	}
	diagCalls.Add(1)
	diagFrames.Add(uint64(frames))
	for {
		old := diagMinF.Load()
		if uint64(frames) >= old || diagMinF.CompareAndSwap(old, uint64(frames)) {
			break
		}
	}
	for {
		old := diagMaxF.Load()
		if uint64(frames) <= old || diagMaxF.CompareAndSwap(old, uint64(frames)) {
			break
		}
	}
	if diagRunning.CompareAndSwap(false, true) {
		diagMinF.Store(^uint64(0))
		go diagLoop()
	}
}

func diagLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		c := diagCalls.Swap(0)
		f := diagFrames.Swap(0)
		if c == 0 && f == 0 {
			diagRunning.Store(false) // 流已停止：退出，避免常驻
			return
		}
		mn, mx := diagMinF.Swap(^uint64(0)), diagMaxF.Swap(0)
		logx.Logger().Info("【诊断】音频回调汇总",
			"回调次数/s", c, "消费样本/s", f,
			"帧长范围", strconv.FormatUint(mn, 10)+"-"+strconv.FormatUint(mx, 10))
	}
}
