package wsapi

import (
	"encoding/json"
	"fmt"
	"sort"

	"mti-sip-phone/internal/contactsearch"
	"mti-sip-phone/internal/core"
)

// 通信录的网页增删改查（REQ-WS-018）。
//
// 原 REQ-WS-015 写的是"网页不做增删改查"（只给 CSV 导入导出），1.0.15 起放开：
// 仅托盘无 UI 版没有桌面界面，通信录只能从网页维护。写入统一走
// core.App 的 mutateContacts（"写存储 + 重建内存快照"整体串行），
// 完成后由 core 广播 contact.changed，多标签与桌面列表据此刷新。

// contactJSON 联系人的线上结构（与 GET /api/contacts 的字段一致）。
type contactJSON struct {
	Number  string `json:"number"`
	Name    string `json:"name"`
	Note    string `json:"note"`
	Starred bool   `json:"starred"`
}

func toContactJSON(c core.Contact) contactJSON {
	return contactJSON{Number: c.Number, Name: c.Name, Note: c.Note, Starred: c.Starred}
}

// dispatchContacts 处理 contact.list / save / delete / setStarred / clear。
func (s *Server) dispatchContacts(msg Message) (Message, bool) {
	switch msg.Method {
	case MethodContactList:
		// 可选 query：**在服务端过滤**——拼音/音头匹配用 internal/contactsearch，
		// 与桌面通信录页是同一份实现（网页端没有拼音字典，也没必要再抄一遍算法）。
		var p struct {
			Query string `json:"query"`
		}
		if len(msg.Params) > 0 {
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("参数格式错误")), true
			}
		}
		list := s.app.Contacts()
		// 排序口径同桌面：名称按拼音（空名称排最前），同音再按规范化号码，结果稳定
		type entry struct {
			c   core.Contact
			idx contactsearch.Index
		}
		all := make([]entry, 0, len(list))
		for _, c := range list {
			all = append(all, entry{c: c, idx: contactsearch.NewIndex(c.Name)})
		}
		sort.SliceStable(all, func(i, j int) bool {
			ki, kj := all[i].idx.SortKey(), all[j].idx.SortKey()
			if ki != kj {
				return ki < kj
			}
			return core.ContactKey(all[i].c.Number) < core.ContactKey(all[j].c.Number)
		})
		out := make([]contactJSON, 0, len(all))
		for _, e := range all {
			if !contactsearch.Match(e.idx, e.c.Number, e.c.Note, p.Query) {
				continue
			}
			out = append(out, toContactJSON(e.c))
		}
		return okMessage(msg.ID, map[string]any{
			"count":    len(out), // 本次返回的条数（已按 query 过滤）
			"total":    len(all), // 通信录总条数（未过滤）
			"contacts": out,
		}), true

	case MethodContactSave:
		var p struct {
			PrevNumber string `json:"prevNumber"` // 修改时的原号码（改号才需要）
			Number     string `json:"number"`
			Name       string `json:"name"`
			Note       string `json:"note"`
			Starred    *bool  `json:"starred"` // 缺省 = 保持原状（新增时 = 非常用）
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || p.Number == "" {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 number")), true
		}
		c := core.Contact{Number: p.Number, Name: p.Name, Note: p.Note}
		if p.Starred != nil {
			c.Starred = *p.Starred
		} else if p.PrevNumber != "" {
			// 没传常用标志但改了号码：沿用原条目的常用状态（网页表单不一定带这一项）
			if prev, ok := s.app.LookupContact(p.PrevNumber); ok {
				c.Starred = prev.Starred
			}
		}
		if err := s.app.SaveContact(p.PrevNumber, c); err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		// 回读生效后的条目（号码会被规范化，名称/备注会被截断）
		saved, _ := s.app.LookupContact(c.Number)
		return okMessage(msg.ID, map[string]any{
			"contact": toContactJSON(saved),
			"count":   s.app.ContactCount(),
		}), true

	case MethodContactDelete:
		var p struct {
			Numbers []string `json:"numbers"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || len(p.Numbers) == 0 {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 numbers")), true
		}
		n, err := s.app.DeleteContacts(p.Numbers)
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, map[string]any{
			"deleted": n,
			"count":   s.app.ContactCount(),
		}), true

	case MethodContactSetStarred:
		var p struct {
			Numbers []string `json:"numbers"`
			Starred bool     `json:"starred"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || len(p.Numbers) == 0 {
			return errMessage(msg.ID, CodeInvalidParams, fmt.Errorf("缺少参数 numbers")), true
		}
		n, err := s.app.SetContactsStarred(p.Numbers, p.Starred)
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, map[string]any{
			"updated": n,
			"count":   s.app.ContactCount(),
		}), true

	case MethodContactClear:
		n, err := s.app.ClearContacts()
		if err != nil {
			return errMessage(msg.ID, codeOf(err), err), true
		}
		return okMessage(msg.ID, map[string]any{"deleted": n, "count": s.app.ContactCount()}), true
	}
	return Message{}, false
}
