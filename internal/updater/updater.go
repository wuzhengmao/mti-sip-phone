// Package updater 实现自动更新：接收业务前端经 WS 告知的新版本信息，
// 由用户在界面上确认后下载（可选 sha256 与可执行文件魔数校验）、替换
// 当前可执行文件并重启。软电话自身不查询更新服务器——"有没有更高版本"
// 由前端应用裁决（网页端 SDK 定时查询更新站点，见 web/sdk），本包只在
// 版本可比时拒绝降级：预发布低于同号正式版（开发构建版本号带 -rc，
// 正式版发布时数字相同也必须能通知更新，规则见 version.go）。
//
// 状态变化经 core.Bus 发布 "system.update" 事件（UI 渲染、WS 转发给网页）。
// 锁序纪律：本包持 u.mu 时不得调用 Options.IsBusy（它会取 core.App 的锁）；
// core.App.State() 也在进 a.mu 前先取本包快照——两把锁不得互相等待。
package updater

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// Options 构造参数（由 cmd/sip-phone 注入，测试可替换）。
type Options struct {
	Bus        *core.Bus    // 事件发布（system.update）；可为 nil
	ExePath    string       // 当前可执行文件路径；空则取 os.Executable()
	Restart    func() error // 替换成功后调用：请求进程重启（由 cmd 实现）
	IsBusy     func() bool  // 是否有进行中的通话（nil = 不等待）
	HTTPClient *http.Client // 测试注入；nil 用默认客户端
	Timeout    time.Duration
}

// Updater 自动更新状态机。
type Updater struct {
	opts    Options
	exePath string

	mu          sync.Mutex
	phase       string
	info        core.UpdateInfo
	host        string
	downloaded  int64
	total       int64
	progress    float64
	waitingCall bool
	applying    bool
	errMsg      string

	cancel   context.CancelFunc // 取消进行中的下载
	stopWait chan struct{}      // 通知"等待通话结束"的轮询退出

	waitInterval time.Duration // 等待通话结束的轮询间隔（测试可缩短）

	lastPublish  time.Time
	lastProgress float64
}

// New 创建 Updater。
func New(opts Options) *Updater {
	exe := opts.ExePath
	if exe == "" {
		if p, err := os.Executable(); err == nil {
			exe = p
		}
	}
	return &Updater{opts: opts, exePath: exe, phase: core.UpdateIdle, waitInterval: 2 * time.Second}
}

// Notify 接收前端告知的新版本：校验参数与版本后进入 available 状态。
// 返回 core.ErrUpdateNotNewer 表示版本不高于当前（前端应视为正常结果）。
func (u *Updater) Notify(info core.UpdateInfo) error {
	info.Version = strings.TrimSpace(info.Version)
	info.URL = strings.TrimSpace(info.URL)
	info.SHA256 = strings.ToLower(strings.TrimSpace(info.SHA256))
	info.OS = strings.TrimSpace(info.OS)
	info.Arch = strings.TrimSpace(info.Arch)

	if info.Version == "" || info.URL == "" {
		return fmt.Errorf("%w：版本号与下载地址不能为空", core.ErrUpdateInvalid)
	}
	parsed, err := url.Parse(info.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%w：下载地址无效（仅支持 http/https）", core.ErrUpdateInvalid)
	}
	if info.OS != "" && info.OS != runtime.GOOS {
		return fmt.Errorf("%w：目标平台 %s 与当前 %s 不符", core.ErrUpdateInvalid, info.OS, runtime.GOOS)
	}
	if info.Arch != "" && info.Arch != runtime.GOARCH {
		return fmt.Errorf("%w：目标架构 %s 与当前 %s 不符", core.ErrUpdateInvalid, info.Arch, runtime.GOARCH)
	}
	// 强制推送（联调/回滚用）跳过版本高低检查；平台一致性等其余校验照旧。
	if !info.Force {
		if cmp, ok := compareVersions(info.Version, core.Version); ok && cmp <= 0 {
			return fmt.Errorf("%w：当前 %s，收到 %s", core.ErrUpdateNotNewer, core.Version, info.Version)
		}
	}

	u.mu.Lock()
	switch u.phase {
	case core.UpdateDownloading, core.UpdateReady:
		if u.info.Version == info.Version {
			u.mu.Unlock()
			logx.Logger().Debug("重复的更新通知，已忽略", "version", info.Version)
			return nil
		}
		u.mu.Unlock()
		return core.ErrUpdateInProgress
	}
	u.phase = core.UpdateAvailable
	u.info = info
	u.host = parsed.Hostname()
	u.downloaded, u.total, u.progress = 0, 0, 0
	u.progress, u.lastProgress = 0, 0
	u.waitingCall = false
	u.errMsg = ""
	st := u.statusLocked()
	u.mu.Unlock()

	logx.Logger().Info("收到新版本通知", "version", info.Version, "host", st.Host,
		"current", core.Version, "强制推送", info.Force)
	u.publish(st)
	return nil
}

// Download 开始下载（异步，立即返回）。
func (u *Updater) Download() error {
	u.mu.Lock()
	if u.phase == core.UpdateDownloading {
		u.mu.Unlock()
		return core.ErrUpdateInProgress
	}
	if u.phase != core.UpdateAvailable && u.phase != core.UpdateFailed {
		u.mu.Unlock()
		return fmt.Errorf("当前没有可下载的更新")
	}
	info := u.info
	timeout := u.opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	u.cancel = cancel
	u.phase = core.UpdateDownloading
	u.downloaded, u.total, u.progress = 0, 0, 0
	u.lastProgress, u.lastPublish = 0, time.Time{}
	u.waitingCall = false
	u.errMsg = ""
	st := u.statusLocked()
	u.mu.Unlock()

	logx.Logger().Info("开始下载更新", "version", info.Version, "url", info.URL)
	u.publish(st)
	go u.runDownload(ctx, info)
	return nil
}

// Cancel 取消进行中的下载（回到可重试状态），或撤销等待中的重启。
func (u *Updater) Cancel() {
	u.mu.Lock()
	cancel := u.cancel
	wasWaiting := u.waitingCall
	u.waitingCall = false
	if u.stopWait != nil {
		close(u.stopWait)
		u.stopWait = nil
	}
	u.mu.Unlock()

	if cancel != nil {
		cancel() // 下载 goroutine 会自行清理并回到 available
	}
	if wasWaiting {
		logx.Logger().Info("用户取消等待重启，更新保持就绪")
		u.publishStatus()
	}
}

// Apply 替换可执行文件并重启；有通话进行中时挂起等待，通话结束后自动执行。
func (u *Updater) Apply() error {
	u.mu.Lock()
	if u.phase != core.UpdateReady {
		u.mu.Unlock()
		return fmt.Errorf("更新尚未下载完成")
	}
	u.mu.Unlock()

	// IsBusy 取 core.App 的锁：必须在释放 u.mu 之后调用（锁序纪律）
	if u.opts.IsBusy != nil && u.opts.IsBusy() {
		u.mu.Lock()
		if u.waitingCall || u.applying {
			u.mu.Unlock()
			return nil
		}
		u.waitingCall = true
		stop := make(chan struct{})
		u.stopWait = stop
		st := u.statusLocked()
		u.mu.Unlock()

		logx.Logger().Info("下载完成，等待通话结束后重启安装")
		u.publish(st)
		go u.waitIdleThenApply(stop)
		return nil
	}
	return u.applyNow()
}

// waitIdleThenApply 通话结束后自动应用更新（每 2 秒轮询一次，Cancel 可中止）。
func (u *Updater) waitIdleThenApply(stop chan struct{}) {
	interval := u.waitInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if u.opts.IsBusy != nil && u.opts.IsBusy() {
				continue
			}
			u.mu.Lock()
			if u.phase != core.UpdateReady || !u.waitingCall {
				u.mu.Unlock()
				return
			}
			u.waitingCall = false
			u.stopWait = nil
			u.mu.Unlock()
			u.publishStatus()
			if err := u.applyNow(); err != nil {
				logx.Logger().Error("自动重启安装失败", "err", err)
			}
			return
		}
	}
}

// applyNow 执行替换与重启（调用方须确保已通过 IsBusy 检查或承担后果）。
func (u *Updater) applyNow() error {
	u.mu.Lock()
	if u.applying {
		u.mu.Unlock()
		return nil
	}
	u.applying = true
	info := u.info
	u.mu.Unlock()

	if err := u.replaceExe(); err != nil {
		u.fail(friendlyError(err))
		return err
	}
	logx.Logger().Info("更新已应用，正在重启", "version", info.Version)
	if u.opts.Restart != nil {
		if err := u.opts.Restart(); err != nil {
			u.fail("重启失败：" + err.Error())
			return err
		}
	}
	return nil
}

// fail 进入失败状态并推送事件。
func (u *Updater) fail(msg string) {
	u.mu.Lock()
	u.applying = false
	u.phase = core.UpdateFailed
	u.errMsg = msg
	u.waitingCall = false
	st := u.statusLocked()
	u.mu.Unlock()
	logx.Logger().Warn("更新失败", "err", msg)
	u.publish(st)
}

// Status 返回当前更新状态快照。
func (u *Updater) Status() core.UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.statusLocked()
}

// statusLocked 组装状态快照（须持 u.mu）。
func (u *Updater) statusLocked() core.UpdateStatus {
	return core.UpdateStatus{
		Phase:       u.phase,
		Info:        u.info,
		Host:        u.host,
		Downloaded:  u.downloaded,
		Total:       u.total,
		Progress:    u.progress,
		WaitingCall: u.waitingCall,
		Error:       u.errMsg,
	}
}

// publishStatus 取快照并发布事件（不持锁调用 publish 之前的状态读取）。
func (u *Updater) publishStatus() {
	u.mu.Lock()
	st := u.statusLocked()
	u.mu.Unlock()
	u.publish(st)
}

// publish 发布 system.update 事件（总线事件会自动转发给所有 WS 客户端）。
func (u *Updater) publish(st core.UpdateStatus) {
	if u.opts.Bus != nil {
		u.opts.Bus.Publish("system.update", st)
	}
}
