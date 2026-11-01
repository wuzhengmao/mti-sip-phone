package updater

// 更新站点查询（REQ-UPD-017）：软电话自己按周期问站点"有没有新版本"。
//
// 这段逻辑原先在网页端 SDK 里（`checkUpdate` / `_checkURL`），搬到这里之后
// **页面没打开也会检查**。查询结果不直接下载——照旧走 Notify → system.update
// 事件 → 界面右下角提示窗 → 用户点「更新」才下载替换重启（REQ-UPD-009）。
//
// 与 SDK 时期一致的几条口径：
//   - 判定权在站点：只看 `updateAvailable`，软电话这边不再自己比一遍版本
//     （Notify 里那次比较是防降级的兜底）；
//   - **不带 channel**：用站点的应用默认通道；
//   - 查询参数**不要带 os/arch 去 Notify**：站点回的是规范化值（x64/x86），
//     而 Notify 按 runtime.GOARCH 严格比对（amd64/386），传了会被判平台不符；
//   - 同一版本 + 同一 sha256 只告知一次（滚动的周期不会反复弹窗）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

const (
	// checkTimeout 单次查询的超时（与旧 SDK 的 10 秒一致）。
	checkTimeout = 10 * time.Second
	// checkBodyLimit 响应体读取上限（防站点异常返回超大内容）。
	checkBodyLimit = 1 << 20
)

// SetUpdateConfig 下发更新站点配置：空 URL = 不检查（停表）。
// 配置变化时立即重排下一次检查（改完就查一次，不用等一个周期）。
func (u *Updater) SetUpdateConfig(c core.SiteConfigSnapshot) {
	u.checkMu.Lock()
	changed := u.site != c
	u.site = c
	u.lastNotified = "" // 配置（站点/AppID/预览版）变了，允许重新告知同版本
	u.checkMu.Unlock()
	if !changed {
		return
	}
	logx.Logger().Info("更新检查配置已更新",
		"站点", c.URL, "应用", c.AppID, "周期", c.Interval.String(), "接受预览版", c.Prerelease)
	u.kickCheck()
}

// Start 启动轮询（幂等，可重复调用）。ctx 结束时退出。
func (u *Updater) Start(ctx context.Context) {
	u.checkOnce.Do(func() {
		u.checkKick = make(chan struct{}, 1)
		go u.checkLoop(ctx)
	})
}

// kickCheck 非阻塞地催一次检查（通道容量 1 + select default：任何一方都不 close，
// 与 netwatch 的 kick 同一套写法——阻塞发送会把调用方卡死）。
func (u *Updater) kickCheck() {
	if u.checkKick == nil {
		return
	}
	select {
	case u.checkKick <- struct{}{}:
	default:
	}
}

// checkLoop 循环：配置了就按周期查；配置变更/手动检查通过 kick 立即重排。
func (u *Updater) checkLoop(ctx context.Context) {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	first := true // 首次立即查一次（启动时配了站点就不用干等一个周期）
	for {
		u.checkMu.Lock()
		site := u.site
		u.checkMu.Unlock()

		var wait time.Duration
		switch {
		case site.URL == "":
			wait = 24 * time.Hour // 没配站点：不查（哨兵值，等 kick 唤醒）
		case first:
			wait = 0 // 立即
		default:
			wait = site.Interval
		}
		first = false
		if timer == nil {
			timer = time.NewTimer(wait)
		} else {
			timer.Stop()
			timer.Reset(wait)
		}

		select {
		case <-ctx.Done():
			return
		case <-u.checkKick:
			// 配置变了/手动检查：立刻查一次（下面这轮循环计时从头开始）
		case <-timer.C:
		}

		if ctx.Err() != nil {
			return
		}
		if _, err := u.CheckNow(ctx); err != nil {
			// CheckNow 内部已记日志与结果，这里不再重复
			continue
		}
	}
}

// CheckNow 立即查询一次站点并（必要时）告知界面。ctx 为 nil 时用后台 context。
func (u *Updater) CheckNow(ctx context.Context) (core.CheckResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	u.checkMu.Lock()
	site := u.site
	u.checkMu.Unlock()

	if site.URL == "" {
		return core.CheckResult{Skipped: true}, nil
	}

	res, err := u.querySite(ctx, site)
	if err != nil {
		logx.Logger().Warn("检查更新失败", "站点", site.URL, "err", err)
		res.Err = err
		return res, err
	}

	// 已是最新 / 无本平台产物：不打扰用户
	if !res.Notified {
		logx.Logger().Debug("检查更新完成", "站点", site.URL, "最新", res.LatestVersion, "结果", res.Text())
	}
	return res, nil
}

// checkPayload 站点 check 接口的响应（只取用得到的字段）。
type checkPayload struct {
	LatestVersion   string `json:"latestVersion"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Notes           string `json:"notes"`
	Download        *struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"download"`
}

// querySite 发一次查询；命中更高版本时告知界面（updater 进入 available）。
func (u *Updater) querySite(ctx context.Context, site core.SiteConfigSnapshot) (core.CheckResult, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.checkURL(site), nil)
	if err != nil {
		return core.CheckResult{}, err
	}
	req.Header.Set("User-Agent", "mti-sip-phone/"+core.Version)

	resp, err := u.client().Do(req)
	if err != nil {
		return core.CheckResult{}, errors.New(friendlyError(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, checkBodyLimit))
	if err != nil {
		return core.CheckResult{}, errors.New(friendlyError(err))
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return core.CheckResult{}, fmt.Errorf("更新站点没有该应用（HTTP 404）")
		}
		return core.CheckResult{}, fmt.Errorf("更新站点返回 HTTP %d", resp.StatusCode)
	}

	var p checkPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return core.CheckResult{}, fmt.Errorf("更新站点响应解析失败: %w", err)
	}
	res := core.CheckResult{LatestVersion: p.LatestVersion}
	if !p.UpdateAvailable {
		res.UpToDate = true
		res.Reason = "up_to_date"
		return res, nil
	}
	if p.Download == nil || strings.TrimSpace(p.Download.URL) == "" {
		// 该平台暂无产物：站点判"有更新"但没有可下载的东西，不算错误
		res.UpToDate = true
		res.Reason = "no_download"
		return res, nil
	}

	key := p.LatestVersion + "|" + strings.ToLower(p.Download.SHA256)
	u.checkMu.Lock()
	dup := key == u.lastNotified
	u.checkMu.Unlock()
	if dup {
		// 无 UI 版自动升级：上一次下载/安装失败时要允许下一轮检查重新通知，
		// 否则失败一次就永远不会重试（站点内容没变，去重键也相同）。
		if u.opts.AutoUpdate && u.Status().Phase == core.UpdateFailed {
			logx.Logger().Info("上次自动更新失败，重新尝试", "version", p.LatestVersion)
		} else {
			res.Reason = "already_notified"
			return res, nil
		}
	}

	err = u.Notify(core.UpdateInfo{
		Version: p.LatestVersion,
		URL:     p.Download.URL,
		SHA256:  p.Download.SHA256,
		Notes:   p.Notes,
		// 刻意不传 OS/Arch：站点回的是 x64/x86 这类规范化值，而 Notify 按
		// runtime.GOARCH 严格比对（amd64/386），传了会被判平台不符而拒绝。
	})
	switch {
	case err == nil:
		u.checkMu.Lock()
		u.lastNotified = key
		u.checkMu.Unlock()
		res.Notified = true
		logx.Logger().Info("检查到新版本", "版本", p.LatestVersion, "站点", site.URL)
	case isNotNewer(err):
		// 站点说可更新、本地版本比较却更高或相同（例如刚装好）——不算错误
		res.Reason = "not_newer"
	case err != nil:
		res.Reason = "notify_failed"
		res.Err = err
		return res, err
	}
	return res, nil
}

// isNotNewer 是否为"版本不高于当前"（站点说可更新但本地比较不成立，属正常结果）。
func isNotNewer(err error) bool {
	return errors.Is(err, core.ErrUpdateNotNewer)
}

// checkURL 拼查询地址：`{站点}/api/v1/apps/{应用}/check?version=&os=&arch=[&prerelease=true]`。
// **不带 channel**（用站点里应用的默认通道）。
func (u *Updater) checkURL(site core.SiteConfigSnapshot) string {
	q := url.Values{}
	q.Set("version", core.Version)
	q.Set("os", runtime.GOOS)
	q.Set("arch", runtime.GOARCH)
	if site.Prerelease {
		q.Set("prerelease", "true")
	}
	return strings.TrimRight(site.URL, "/") + "/api/v1/apps/" +
		url.PathEscape(site.AppID) + "/check?" + q.Encode()
}
