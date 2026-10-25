package core

import (
	"errors"
	"time"
)

// 通话记录（REQ-CALL-007）。
//
// 与 Telephony/Updater 同一模式：core 只定义接口与状态，具体存储在
// internal/calllog（SQLite），由 main 注入。界面经 App 的 QueryCallLog 等方法
// 访问，不直接依赖存储包。
//
// 记录**不在事件总线上采集**：Bus 是容量 64、满即丢的 channel，通话记录
// 不能建立在会丢事件的东西上；开始/接通/结束三个时刻都在 App 的状态更新点
// 直接入队（见 app.go 的 logStart/logAnswered/logFinish）。
//
// 顺序约束：**先写记录（入队）再发布事件**。界面的查询与写入在同一条 FIFO
// 队列上，只有入队发生在发事件之前，"收到 call.terminated 后立刻查库能看到
// 这条记录"才成立。

// CallRecord 一条通话记录。
type CallRecord struct {
	CallID     string
	Direction  string // incoming 呼入 / outgoing 呼出
	Remote     string // 对方号码或 URI
	RemoteName string // 对方显示名
	// DisplayName 呼出时已知的座席显示名（在线座席列表直接呼出带上的
	// displayName）：通话记录展示时**优先于通信录名称**，为空则回退通信录。
	DisplayName string
	StartedAt   time.Time
	AnsweredAt  time.Time // 零值 = 未接通
	EndedAt     time.Time // 零值 = 进行中
	HasVideo    bool
	Missed      bool   // 呼入未接（口径同 missedOnEnd，拒绝/本端挂断不算）
	Reason      string // 结束原因（SIP 层原样透传）
}

// CallLogFilter 通话记录查询条件。
type CallLogFilter struct {
	Remote string // 按号码子串筛选，空 = 全部
	Limit  int    // 每页条数，<=0 用存储层默认值
	Offset int
}

// CallLogPage 一页通话记录。
type CallLogPage struct {
	Records []CallRecord
	Total   int  // 满足筛选条件的总条数
	HasMore bool // 还有下一页
}

// CallLog 通话记录存储能力（internal/calllog 实现）。
type CallLog interface {
	// Start/MarkAnswered/Finish 由电话状态更新点调用，必须非阻塞
	//（SIP 线程路径）。
	Start(r CallRecord)
	MarkAnswered(callID string, at time.Time)
	Finish(callID string, at time.Time, reason string, missed bool)
	// Query/Delete/Clear 由界面调用。
	Query(f CallLogFilter) (CallLogPage, error)
	Delete(callIDs []string) (int, error)
	Clear() (int, error)
	Close() error
}

// ReasonInterrupted 上次运行遗留的"进行中"记录被清扫后的原因：进程崩溃或
// 自更新重启时不会有结束上报（见 internal/calllog 的启动清扫）。
const ReasonInterrupted = "interrupted"

// ErrCallLogUnavailable 通话记录不可用（未接入，或数据库打开失败）。
var ErrCallLogUnavailable = errors.New("通话记录不可用")

// SetCallLog 注入通话记录存储（启动前调用一次；不注入 = 不记录）。
func (a *App) SetCallLog(l CallLog) { a.callLog = l }

// CallLog 返回通话记录存储（未接入时为 nil）。
func (a *App) CallLog() CallLog { return a.callLog }

// QueryCallLog 分页查询通话记录。
func (a *App) QueryCallLog(f CallLogFilter) (CallLogPage, error) {
	if a.callLog == nil {
		return CallLogPage{}, ErrCallLogUnavailable
	}
	return a.callLog.Query(f)
}

// DeleteCallLogRecords 按 call_id 删除若干条记录，返回实际删除条数。
func (a *App) DeleteCallLogRecords(callIDs []string) (int, error) {
	if a.callLog == nil {
		return 0, ErrCallLogUnavailable
	}
	return a.callLog.Delete(callIDs)
}

// ClearCallLog 清空全部通话记录，返回删除条数。
func (a *App) ClearCallLog() (int, error) {
	if a.callLog == nil {
		return 0, ErrCallLogUnavailable
	}
	return a.callLog.Clear()
}

// PublishCallLogChanged 广播"通话记录被删除/清空"（REQ-WS-017）。
//
// 只覆盖删除类变更：新增记录由 call.terminated / call.failed 驱动刷新
// （记录先入库再发事件，所以收到事件时查库一定能查到）。
// 放在 core 里而不是 wsapi：桌面界面与网页都是消费者，事件得是同一份。
func (a *App) PublishCallLogChanged(action string, deleted int) {
	a.bus.Publish("calllog.changed", map[string]any{
		"action":  action,
		"deleted": deleted,
	})
}

// selfCaller 来电主叫是否就是本机分机号（按 NormalizeNumber 比较）。
// CTI 平台在监听/会议等场景会以本机号码作为主叫回呼话机，这类通话不进通话
// 记录；配置里没有分机号（未启用账户）时一律不拦。
func (a *App) selfCaller(remote string) bool {
	a.mu.RLock()
	self := a.cfg.Account.Account
	a.mu.RUnlock()
	if self == "" || remote == "" {
		return false
	}
	return NormalizeNumber(remote) == NormalizeNumber(self)
}

// logStart 记开始（未接入存储时无副作用）。
func (a *App) logStart(r CallRecord) {
	if a.callLog != nil {
		a.callLog.Start(r)
	}
}

// logAnswered 记接通时刻。
func (a *App) logAnswered(callID string) {
	if a.callLog != nil {
		a.callLog.MarkAnswered(callID, time.Now())
	}
}

// logFinish 记结束。重复上报交给存储层按 ended_at=0 过滤（见 calllog 包注释）。
func (a *App) logFinish(callID, reason string, missed bool) {
	if a.callLog != nil {
		a.callLog.Finish(callID, time.Now(), reason, missed)
	}
}
