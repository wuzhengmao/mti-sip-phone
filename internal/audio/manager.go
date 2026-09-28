// Package audio 管理音频设备：枚举、麦克风/扬声器选择、与媒体引擎对接。
//
// 后端实现按构建标签切换：
//   - 默认（无标签）：malgo（miniaudio，全双工 + 设备枚举，需 CGO）
//   - -tags noaudio：空后端（无音频设备，供纯 Go 构建与测试）
package audio

import (
	"sync"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/internal/media"
)

// Manager 音频设备管理：全局单例，一次仅支持一路通话的音频流。
type Manager struct {
	mu            sync.Mutex
	micID         string // 当前选择的麦克风 ID，空 = 系统默认
	speakerID     string // 当前选择的扬声器 ID，空 = 系统默认
	ringSpeakerID string // 振铃音专用扬声器 ID，空 = 系统默认
	engine    *media.Engine
	backend   Backend
	running   bool
}

// NewManager 创建音频管理器并初始化后端。
func NewManager() (*Manager, error) {
	backend, err := newBackend()
	if err != nil {
		return nil, err
	}
	return &Manager{backend: backend}, nil
}

// SetMic 选择麦克风。空 ID 表示系统默认设备。
func (m *Manager) SetMic(deviceID string) error {
	m.mu.Lock()
	m.micID = deviceID
	restart := m.running
	m.mu.Unlock()
	logx.Logger().Info("切换麦克风", "device", deviceID)
	if restart {
		return m.restart()
	}
	return nil
}

// SetSpeaker 选择扬声器。空 ID 表示系统默认设备。
func (m *Manager) SetSpeaker(deviceID string) error {
	m.mu.Lock()
	m.speakerID = deviceID
	restart := m.running
	m.mu.Unlock()
	logx.Logger().Info("切换扬声器", "device", deviceID)
	if restart {
		return m.restart()
	}
	return nil
}

// Devices 返回麦克风与扬声器列表。
func (m *Manager) Devices() ([]core.AudioDevice, []core.AudioDevice) {
	return m.backend.Devices()
}

// AttachEngine 通话建立时对接媒体引擎：启动全双工音频流。
func (m *Manager) AttachEngine(engine *media.Engine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.engine = engine
	if err := m.startLocked(); err != nil {
		logx.Logger().Error("启动音频流失败", "err", err)
	}
}

// DetachEngine 通话结束时停止音频流。
func (m *Manager) DetachEngine() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	m.engine = nil
}

// restart 设备切换时重建音频流。
func (m *Manager) restart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return nil
	}
	m.stopLocked()
	return m.startLocked()
}

func (m *Manager) startLocked() error {
	if m.running || m.engine == nil {
		return nil
	}
	if err := m.backend.Start(m.micID, m.speakerID, m.engine); err != nil {
		return err
	}
	m.running = true
	logx.Logger().Info("音频流已启动", "mic", m.micID, "speaker", m.speakerID)
	return nil
}

func (m *Manager) stopLocked() {
	if !m.running {
		return
	}
	m.backend.Stop()
	m.running = false
	logx.Logger().Debug("音频流已停止")
}

// SetRingSpeaker 设置振铃音专用扬声器。
func (m *Manager) SetRingSpeaker(deviceID string) {
	m.mu.Lock()
	m.ringSpeakerID = deviceID
	m.mu.Unlock()
	logx.Logger().Info("振铃设备已切换", "device", deviceID)
}

// PlayRing 播放振铃音（循环），通话事件驱动；使用振铃专用扬声器（若已配置）。
func (m *Manager) PlayRing() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.backend.PlayTone(ringtone, true, m.ringSpeakerID); err != nil {
		logx.Logger().Warn("播放振铃音失败", "err", err)
	}
}

// PlayRingback 已移除：回铃音由 SIP 服务器下发（183 早期媒体），
// 本地通过媒体引擎接收并播放，不再本地生成。

// PlayHangup 播放挂断提示音（一次）。
func (m *Manager) PlayHangup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.backend.PlayTone(hangup, false, m.ringSpeakerID); err != nil {
		logx.Logger().Warn("播放挂断音失败", "err", err)
	}
}

// StopRing 停止提示音（振铃结束/通话建立时）。
func (m *Manager) StopRing() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backend.StopTone()
}

// Close 释放后端资源。
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	m.backend.Close()
}
