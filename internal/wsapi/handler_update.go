package wsapi

import (
	"context"
	"fmt"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 自动更新的网页接口（REQ-UPD-020）。
//
// 原口径（REQ-UPD-009）是"下载必须由用户在**软电话界面**上点击，WS 仅能告知"。
// 仅托盘无 UI 版没有软电话界面，网页就是唯一的界面，所以 1.0.15 起放开：
// 网页可以触发下载/取消/安装并重启，仍然**必须由用户点击**（网页上的按钮），
// 不做静默自动安装。桌面版的提示窗与确认流程不变。

// dispatchUpdate 处理 update.* 与 system.checkUpdateNow。
func (s *Server) dispatchUpdate(ctx context.Context, msg Message) (Message, bool) {
	switch msg.Method {
	case MethodUpdateStatus:
		return okMessage(msg.ID, s.app.UpdateStatus()), true

	case MethodUpdateStart:
		if err := s.app.StartUpdate(); err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, s.app.UpdateStatus()), true

	case MethodUpdateCancel:
		s.app.CancelUpdate()
		return okMessage(msg.ID, s.app.UpdateStatus()), true

	case MethodUpdateApply:
		// 前置检查：没下载好就点"安装"要明确报错，而不是回个 ok 却什么都没做
		//（apply 是异步的，回包时已经没法改口了）
		st := s.app.UpdateStatus()
		if st.Phase != core.UpdateReady {
			return errMessage(msg.ID, CodeInvalidParams,
				fmt.Errorf("更新尚未下载完成（当前状态 %s）", st.Phase)), true
		}
		// **先回响应再安装**：安装会替换 exe 并重启进程，先动手的话这个响应
		// 发不出去，网页只看到连接断开。
		go func() {
			time.Sleep(applyDelay)
			if err := s.app.ApplyUpdate(); err != nil {
				logx.Logger().Warn("网页触发的更新安装失败", "err", err)
			}
		}()
		return okMessage(msg.ID, map[string]any{"phase": st.Phase, "applying": true}), true

	case MethodCheckUpdateNow:
		// 立即检查一次更新站点（不等到下一个周期）。带超时：站点不可达时
		// 不能让这个请求挂着不放。
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		res, err := s.app.CheckUpdateNow(checkCtx)
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, map[string]any{
			"skipped":        res.Skipped,
			"upToDate":       res.UpToDate,
			"latestVersion":  res.LatestVersion,
			"notified":       res.Notified,
			"reason":         res.Reason,
			"text":           res.Text(),
			"currentVersion": core.Version,
		}), true
	}
	return Message{}, false
}
