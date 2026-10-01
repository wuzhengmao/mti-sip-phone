package core

import "errors"

// UpdateInfo 前端告知的新版本信息（WS system.notifyUpdate 的参数）。
type UpdateInfo struct {
	Version string `json:"version"`          // 新版本号，如 1.1.0
	URL     string `json:"url"`              // 下载地址（仅 http/https）
	SHA256  string `json:"sha256,omitempty"` // 可选：sha256 校验值（十六进制，大小写不敏感）
	Notes   string `json:"notes,omitempty"`  // 可选：更新说明
	OS      string `json:"os,omitempty"`     // 可选：前端认定的目标平台，提供时须与自身一致
	Arch    string `json:"arch,omitempty"`   // 可选：前端认定的目标架构，提供时须与自身一致
}

// 更新状态机阶段。
const (
	UpdateIdle        = "idle"        // 无可用更新
	UpdateAvailable   = "available"   // 已收到更新通知，等待用户确认下载
	UpdateDownloading = "downloading" // 正在下载
	UpdateReady       = "ready"       // 下载并校验完成，等待替换重启
	UpdateFailed      = "failed"      // 下载/替换失败（可重试）
)

// UpdateStatus 更新状态快照（供 UI 渲染与 WS 事件推送）。
type UpdateStatus struct {
	Phase       string     `json:"phase"`
	Info        UpdateInfo `json:"info"`
	Host        string     `json:"host,omitempty"`       // 下载地址主机名（界面展示来源）
	Downloaded  int64      `json:"downloaded,omitempty"` // 已下载字节数
	Total       int64      `json:"total,omitempty"`      // 总字节数（0 = 未知）
	Progress    float64    `json:"progress,omitempty"`   // 下载进度 0-100
	WaitingCall bool       `json:"waitingCall,omitempty"` // 已就绪，等通话结束后自动重启
	Error       string     `json:"error,omitempty"`      // 面向用户的中文失败原因
}

// 更新相关错误，供接口层映射为错误码。
var (
	ErrUpdateUnavailable = errors.New("更新能力不可用")
	ErrUpdateNotNewer    = errors.New("版本不高于当前版本")
	ErrUpdateInProgress  = errors.New("更新正在进行中")
	ErrUpdateInvalid     = errors.New("更新参数无效")
)

// Updater 自动更新能力抽象（internal/updater 实现，模式同 Telephony：
// 接口定义在 core，实现包 import core；nil 表示未接入）。
type Updater interface {
	// Notify 接收前端告知的新版本信息：校验后进入 available 状态。
	Notify(info UpdateInfo) error
	// Download 开始下载已告知的新版本（异步，立即返回）。
	Download() error
	// Cancel 取消进行中的下载，或撤销等待中的重启。
	Cancel()
	// Apply 替换当前可执行文件并重启；通话中则等通话结束后执行。
	Apply() error
	// Status 返回当前更新状态快照。
	Status() UpdateStatus
}

// SetUpdater 注入自动更新能力（启动前调用，nil 表示未接入）。
func (a *App) SetUpdater(upd Updater) { a.upd = upd }

// Updater 返回自动更新能力实现（可能为 nil）。
func (a *App) Updater() Updater { return a.upd }

// NotifyUpdate 接收前端告知的新版本（WS system.notifyUpdate）。
func (a *App) NotifyUpdate(info UpdateInfo) error {
	if a.upd == nil {
		return ErrUpdateUnavailable
	}
	return a.upd.Notify(info)
}

// StartUpdate 开始下载（由用户在软电话界面上点击触发，绝不自动下载）。
func (a *App) StartUpdate() error {
	if a.upd == nil {
		return ErrUpdateUnavailable
	}
	return a.upd.Download()
}

// CancelUpdate 取消下载或撤销等待中的重启。
func (a *App) CancelUpdate() {
	if a.upd != nil {
		a.upd.Cancel()
	}
}

// ApplyUpdate 替换可执行文件并重启（通话中挂起等待）。
func (a *App) ApplyUpdate() error {
	if a.upd == nil {
		return ErrUpdateUnavailable
	}
	return a.upd.Apply()
}

// UpdateStatus 返回更新状态快照。
func (a *App) UpdateStatus() UpdateStatus {
	if a.upd == nil {
		return UpdateStatus{Phase: UpdateIdle}
	}
	return a.upd.Status()
}
