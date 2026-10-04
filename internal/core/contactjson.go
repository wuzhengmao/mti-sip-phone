package core

// 通信录 JSON 编解码（REQ-CONTACT-005）。
//
// 与 CSV（contactcsv.go）并列：CSV 给人看/给表格软件用（桌面端的导入导出、
// 网页的一键下载），JSON 给程序用——网页可以直接 XHR 收发结构化数据，
// 不用自己解析 CSV。
//
// 形状：
//
//	导出：{ "count": 2, "contacts": [ {"number":"2999","name":"张三","note":"前台","starred":true}, ... ] }
//	导入：接受上面这种"带外壳"的对象，**也接受裸数组** [ {...}, ... ]（便于调用方直接
//	      把数组 POST 上来；导出→导入可以直接回灌）。
//
// 字段与 core.Contact 的 json tag 一致，但**不含 updatedAt**（那是本地元数据）。

import (
	"encoding/json"
	"fmt"

	"mti-sip-phone/internal/logx"
)

// contactJSON 单个联系人的 JSON 形状（导入导出共用）。
type contactJSON struct {
	Number  string `json:"number"`
	Name    string `json:"name"`
	Note    string `json:"note"`
	Starred bool   `json:"starred"`
}

// contactsJSONDoc 导出的外层文档。
type contactsJSONDoc struct {
	Count    int           `json:"count"`
	Contacts []contactJSON `json:"contacts"`
}

// MarshalContactsJSON 导出为 JSON 文档（带 count 与 contacts）。
func MarshalContactsJSON(list []Contact) ([]byte, error) {
	doc := contactsJSONDoc{Count: len(list), Contacts: make([]contactJSON, 0, len(list))}
	for _, c := range list {
		doc.Contacts = append(doc.Contacts, contactJSON{
			Number:  c.Number,
			Name:    c.Name,
			Note:    c.Note,
			Starred: c.Starred,
		})
	}
	return json.Marshal(doc)
}

// ParseContactsJSON 解析 JSON（带外壳的对象或裸数组均可）。
// 没有号码的条目计入 skipped；JSON 语法错误返回 error（整份不生效）。
func ParseContactsJSON(data []byte) (list []Contact, skipped int, err error) {
	items, err := decodeContactJSONItems(data)
	if err != nil {
		return nil, 0, err
	}
	for _, it := range items {
		if NormalizeNumber(it.Number) == "" {
			skipped++
			continue
		}
		list = append(list, Contact{
			Number:  it.Number,
			Name:    it.Name,
			Note:    it.Note,
			Starred: it.Starred,
		})
	}
	return list, skipped, nil
}

// decodeContactJSONItems 兼容两种形状：{"contacts":[...]} 与 [...]
func decodeContactJSONItems(data []byte) ([]contactJSON, error) {
	trimmed := skipSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	switch trimmed[0] {
	case '[': // 裸数组
		var items []contactJSON
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, fmt.Errorf("通信录 JSON 解析失败: %w", err)
		}
		return items, nil
	case '{': // 带外壳
		var doc contactsJSONDoc
		if err := json.Unmarshal(trimmed, &doc); err != nil {
			return nil, fmt.Errorf("通信录 JSON 解析失败: %w", err)
		}
		return doc.Contacts, nil
	default:
		return nil, fmt.Errorf("通信录 JSON 格式不对：应为数组或带 contacts 字段的对象")
	}
}

// skipSpace 跳过前导空白（判断首个非空字符是 [ 还是 {）。
func skipSpace(b []byte) []byte {
	for i, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return b[i:]
	}
	return nil
}

// ExportContactsJSON 导出全部联系人为 JSON 文档。
func (a *App) ExportContactsJSON() ([]byte, error) {
	if a.contacts == nil {
		return nil, ErrContactsUnavailable
	}
	return MarshalContactsJSON(a.Contacts())
}

// ImportContactsJSON 导入 JSON：语义与 CSV 导入完全一致（按号码去重、只覆盖
// 号码/名称/备注、**常用标志只增不减**、没有的整条新增）。
func (a *App) ImportContactsJSON(data []byte) (ContactImportResult, error) {
	list, skipped, err := ParseContactsJSON(data)
	if err != nil {
		return ContactImportResult{}, err
	}
	res := ContactImportResult{Skipped: skipped}
	if err := a.mutateContacts(func() error {
		added, updated, err := a.contacts.Merge(list)
		res.Added, res.Updated = added, updated
		return err
	}); err != nil {
		return res, err
	}
	logx.Logger().Info("导入通信录（JSON）",
		"added", res.Added, "updated", res.Updated, "skipped", res.Skipped)
	return res, nil
}
