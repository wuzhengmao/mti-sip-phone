// Package multiinstance 实现同一台机器上多个软电话实例（主实例 + 沙箱槽位）之间的
// 轻量协作：目前用于「一个实例安装更新后，通知其它实例在空闲时一起重启」。
//
// 为什么用共享标记文件而不是进程间通信：
//   - 多实例是**各自独立的进程**（各槽位独立数据目录与单实例锁），但共用同一个
//     可执行文件与同一个主配置目录；写一个 JSON 标记到主配置目录最省事，
//     不需要端口扫描、监听 socket 或平台专有 IPC；
//   - 更新的替换动作发生在**可执行文件**上（所有实例共用），所以"新版本已就位"
//     这件事本身就是共享状态；
//   - 其它实例按固定间隔轮询标记（一次几 KB 的读），发现"自己跑的还是旧镜像"
//     后，等**本实例没有进行中的通话**再重启。
//
// 标记的判定口径（见 staleFor）：槽位来源、写入时间（必须晚于本进程启动）、
// 可执行文件指纹（sha256）三者结合——**重启后的新进程不会再被同一条标记触发**，
// 否则会无限重启。
package multiinstance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"mti-sip-phone/internal/config"
	"mti-sip-phone/internal/logx"
)

// MarkerName 共享标记文件名（放在主配置目录下，所有槽位共用）。
const MarkerName = "restart-all.json"

// Marker 是「新版本已安装，其它实例请空闲后重启」的共享标记。
type Marker struct {
	Version    string `json:"version"`          // 新版本号
	Sha256     string `json:"sha256,omitempty"` // 新可执行文件指纹（算不出时为空，退回版本比较）
	SourceSlot int    `json:"sourceSlot"`       // 写入方槽位（该实例自己会重启，不算"其它实例"）
	UpdatedAt  int64  `json:"updatedAt"`        // 写入时间（毫秒，Unix）
}

// MarkerPath 返回共享标记文件路径（主配置目录下）。
func MarkerPath() (string, error) {
	base, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, MarkerName), nil
}

// RequestRestart 在可执行文件替换成功后调用：写入共享标记，通知其它实例空闲后重启。
// 指纹计算失败时仍写标记（其它实例退回按版本号判断）。
func RequestRestart(exePath string, slot int, version string) error {
	fp, err := FileSHA256(exePath)
	if err != nil {
		logx.Logger().Warn("计算新可执行文件指纹失败，其它实例将按版本号判断", "err", err)
		fp = ""
	}
	return WriteMarker(Marker{
		Version:    version,
		Sha256:     fp,
		SourceSlot: slot,
		UpdatedAt:  time.Now().UnixMilli(),
	})
}

// WriteMarker 原子写入标记（先写临时文件再改名；Windows 上目标存在时改名会失败，
// 先删再试一次——轮询方读到"文件不存在"只是这一轮跳过）。
func WriteMarker(m Marker) error {
	path, err := MarkerPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadMarker 读取标记；文件不存在返回 os.ErrNotExist。
func ReadMarker() (Marker, error) {
	var m Marker
	path, err := MarkerPath()
	if err != nil {
		return m, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("解析重启标记失败: %w", err)
	}
	return m, nil
}

// FileSHA256 计算文件指纹（启动时算一次，用来判断"当前运行的镜像"是否已被替换）。
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WatchOptions 轮询参数。
type WatchOptions struct {
	OwnSlot        int           // 本实例槽位
	OwnFingerprint string        // 本进程**启动时**可执行文件的 sha256（空 = 不按指纹判断）
	OwnVersion     string        // 本进程版本（指纹为空时退回比较）
	StartedAt      time.Time     // 本进程启动时刻：早于它的标记忽略（重启后的新进程不会被旧标记再触发）
	Interval       time.Duration // 轮询间隔，默认 2 秒
	IsBusy         func() bool   // 是否有进行中的通话（true 时等待）
	OnStale        func(Marker)  // 确认需要重启（且本实例空闲）时回调；调用方负责真正重启
}

// Watch 轮询共享标记：发现"别的实例装了新版本"且本实例空闲时调用一次 OnStale。
// ctx 取消即退出。
func Watch(ctx context.Context, opts WatchOptions) {
	interval := opts.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logged := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		m, err := ReadMarker()
		if err != nil {
			if !os.IsNotExist(err) {
				logx.Logger().Debug("读取实例重启标记失败", "err", err)
			}
			logged = false
			continue
		}
		if !staleFor(m, opts) {
			logged = false
			continue
		}
		busy := opts.IsBusy != nil && opts.IsBusy()
		if !logged {
			logx.Logger().Info("检测到其它实例已安装新版本，本实例空闲后一并重启",
				"version", m.Version, "sourceSlot", m.SourceSlot, "busy", busy)
			logged = true
		}
		if busy {
			continue
		}
		if opts.OnStale != nil {
			opts.OnStale(m)
		}
		return // 调用方会请求重启；本实例退出后不必继续轮询
	}
}

// staleFor 判断标记是否要求本实例重启：
//   - 写入方就是本槽位：自己会重启，忽略；
//   - 标记早于本进程启动：说明本进程启动时新版本就已就位，忽略（否则重启后会无限循环）；
//   - 指纹相同：本进程跑的就是新文件，忽略；
//   - 没有指纹时退回版本号比较。
func staleFor(m Marker, opts WatchOptions) bool {
	if m.SourceSlot == opts.OwnSlot {
		return false
	}
	if m.UpdatedAt > 0 && m.UpdatedAt < opts.StartedAt.UnixMilli() {
		return false
	}
	if m.Sha256 != "" {
		if opts.OwnFingerprint == "" {
			return true
		}
		return m.Sha256 != opts.OwnFingerprint
	}
	if m.Version != "" && opts.OwnVersion != "" {
		return m.Version != opts.OwnVersion
	}
	return true
}
