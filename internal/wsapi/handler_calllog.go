package wsapi

import (
	"encoding/json"
	"fmt"
	"time"

	"mti-sip-phone/internal/core"
)

// 通话记录的网页接口（REQ-WS-017）。
//
// 桌面版通话记录页的数据源就是 core.App 的 QueryCallLog / DeleteCallLogRecords
// / ClearCallLog，这里只做 JSON 编解码与参数钳制（口径与桌面一致：按号码子串
// 筛选、分页、未接标红等展示口径由前端按同一套字段算）。

const (
	// callLogDefaultLimit / callLogMaxLimit 每页条数（不传用默认，超出钳到上限）。
	// 桌面版每次加载 50 条，网页沿用同一量级。
	callLogDefaultLimit = 50
	callLogMaxLimit     = 200
)

// callRecordJSON 通话记录的线上结构。
//
// 时间戳用**毫秒整数**（库里存的就是毫秒，界面自行格式化）；未接通/进行中的
// 时刻用 0 表示，**不要**把 time.Time 的零值序列化成 0001-01-01（前端会算出
// 荒唐的时长）。
type callRecordJSON struct {
	CallID     string `json:"callId"`
	Direction  string `json:"direction"` // incoming / outgoing
	Remote     string `json:"remote"`
	RemoteName string `json:"remoteName"`
	// DisplayName 座席显示名（在线座席列表直接呼出时记录）：页面展示名称时
	// **优先于通信录名称**，为空再回退 contact / remoteName。
	DisplayName string `json:"displayName"`
	StartedAt   int64  `json:"startedAt"`  // 毫秒
	AnsweredAt  int64  `json:"answeredAt"` // 0 = 未接通
	EndedAt     int64  `json:"endedAt"`    // 0 = 进行中
	HasVideo    bool   `json:"hasVideo"`
	Missed      bool   `json:"missed"`
	Reason      string `json:"reason"`
	// Contact 号码命中通信录时的条目（口径同桌面通话记录页：号码规范化后查
	// core 的内存快照）。页面据此显示"号码 + 名称 + ★"，并拿 note 做悬停提示。
	Contact *core.ContactInfo `json:"contact,omitempty"`
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// toCallRecordJSON 把一条记录转成线上结构；app 用于解析通信录名称
// （nil 时跳过解析，测试里可以直接传 nil）。
func toCallRecordJSON(app *core.App, r core.CallRecord) callRecordJSON {
	out := toCallRecordJSONPlain(r)
	if app != nil && r.Remote != "" {
		if c, ok := app.LookupContact(r.Remote); ok {
			out.Contact = &core.ContactInfo{Name: c.Name, Note: c.Note, Starred: c.Starred}
		}
	}
	return out
}

func toCallRecordJSONPlain(r core.CallRecord) callRecordJSON {
	return callRecordJSON{
		CallID:      r.CallID,
		Direction:   r.Direction,
		Remote:      r.Remote,
		RemoteName:  r.RemoteName,
		DisplayName: r.DisplayName,
		StartedAt:   millis(r.StartedAt),
		AnsweredAt:  millis(r.AnsweredAt),
		EndedAt:     millis(r.EndedAt),
		HasVideo:    r.HasVideo,
		Missed:      r.Missed,
		Reason:      r.Reason,
	}
}

// dispatchCallLog 处理 calllog.* 方法；ok = false 表示不是本组方法。
func (s *Server) dispatchCallLog(msg Message) (Message, bool) {
	switch msg.Method {
	case MethodCallLogQuery:
		var p struct {
			Remote *string `json:"remote"`
			Limit  *int    `json:"limit"`
			Offset *int    `json:"offset"`
		}
		if len(msg.Params) > 0 {
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误")), true
			}
		}
		f := core.CallLogFilter{Limit: callLogDefaultLimit}
		if p.Remote != nil {
			f.Remote = *p.Remote
		}
		if p.Limit != nil {
			f.Limit = *p.Limit
			if f.Limit <= 0 {
				f.Limit = callLogDefaultLimit
			}
			if f.Limit > callLogMaxLimit {
				f.Limit = callLogMaxLimit
			}
		}
		if p.Offset != nil && *p.Offset > 0 {
			f.Offset = *p.Offset
		}
		page, err := s.app.QueryCallLog(f)
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		records := make([]callRecordJSON, 0, len(page.Records))
		for _, r := range page.Records {
			records = append(records, toCallRecordJSON(s.app, r))
		}
		return okMessage(msg.ID, map[string]any{
			"records": records,
			"total":   page.Total,
			"hasMore": page.HasMore,
		}), true

	case MethodCallLogDelete:
		var p struct {
			CallIDs []string `json:"callIds"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || len(p.CallIDs) == 0 {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 callIds")), true
		}
		n, err := s.app.DeleteCallLogRecords(p.CallIDs)
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		s.app.PublishCallLogChanged("delete", n)
		return okMessage(msg.ID, map[string]any{"deleted": n}), true

	case MethodCallLogClear:
		n, err := s.app.ClearCallLog()
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		s.app.PublishCallLogChanged("clear", n)
		return okMessage(msg.ID, map[string]any{"deleted": n}), true
	}
	return Message{}, false
}
