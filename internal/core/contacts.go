package core

// 通信录（REQ-UI-032 ~ REQ-UI-037）。
//
// 与通话记录（calllog.go）同一模式：core 只定义数据、接口与内存缓存，具体存储
// 在 internal/contacts（SQLite），由 main 注入。
//
// 两处与通话记录**刻意不同**：
//
//  1. **读路径走内存缓存**。通话记录是"点开页面才查库"，而通信录要被逐行查询
//     （通话记录列表每行、通话面板、视频窗口标题、WS 事件负载），因此启动时
//     载入全量快照到内存，界面与 WS 的查找是 O(1) 且永不阻塞；每次写操作成功后
//     由本文件统一重建快照（存储层是单 worker 串行执行，写完即读是一致的）。
//  2. **主键是"规范化号码"**（NormalizeNumber）。同一个人的号码在 SIP 报文里
//     可能写成 `2999`、`sip:2999@10.168.4.40:5060`、`+86 138-0000-0000`
//     等形态，去重与匹配必须先归一。

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"

	"mti-sip-phone/internal/logx"
)

// Contact 一条联系人记录。
type Contact struct {
	Number    string    `json:"number"`  // 号码（原样保存：展示与拨号都用它）
	Name      string    `json:"name"`    // 显示名称
	Note      string    `json:"note"`    // 备注
	Starred   bool      `json:"starred"` // 常用联系人（列表里带 ★）
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// maxContactFieldLen 号码/名称/备注的最大长度（按 rune）。这些字段可由网页经
// WS 导入，属不可信输入，落库前截断。
const maxContactFieldLen = 256

// ContactImportResult 一次导入的结果。
type ContactImportResult struct {
	Added   int `json:"added"`   // 新增条数
	Updated int `json:"updated"` // 覆盖条数（只覆盖号码/名称/备注）
	Skipped int `json:"skipped"` // 跳过的行数（缺号码或整行无法解析）
}

// ContactStore 通信录存储能力（internal/contacts 实现）。
//
// Merge（导入合并）时**常用标志只增不减**：已存在的条目只覆盖号码/名称/备注，
// 常用标志取本地与导入的"或"——本地已标常用就保持，本地没标而导入标了就变常用。
//
// Save 的 prevNumber 非空表示"修改原号码为 prevNumber 的那条"，新号码若已被
// 其它条目占用则返回 ErrContactNumberExists（整条不落库）——判断放在存储层，
// 与写入同一事务，避免"先查后写"的竞态。
type ContactStore interface {
	List() ([]Contact, error)
	Save(prevNumber string, c Contact) error
	SetStarred(numbers []string, starred bool) (int, error)
	Delete(numbers []string) (int, error)
	Clear() (int, error)
	Merge(list []Contact) (added, updated int, err error)
	Close() error
}

// 通信录相关错误。
var (
	// ErrContactsUnavailable 通信录不可用（未接入，或数据库打开失败）。
	ErrContactsUnavailable = errors.New("通信录不可用")
	// ErrContactNumberExists 该号码已存在（新增或改号撞上别的条目）。
	ErrContactNumberExists = errors.New("该号码已在通信录中")
	// ErrContactNumberEmpty 号码为空（规范化后没有任何有效字符）。
	ErrContactNumberEmpty = errors.New("号码不能为空")
)

// NormalizeNumber 号码规范化：通信录的去重键与匹配键。
//
// 处理 SIP 报文里出现过的各种形态：
//
//	2999                          → 2999
//	sip:2999@10.168.4.40:5060     → 2999
//	张三 <sip:2999@host>           → 2999
//	+86 138-0000-0000             → +8613800000000
//	(0512)6888-8888               → 051268888888
//	2901;user=phone               → 2901
//
// placeholderDisplayNames SIP 信令里常见的"占位"显示名：FreeSWITCH 等网关把
// 出局腿 From 的 display-name 固定写成 "Outbound Call"，它不是真正的名称，
// 显示时应回退到号码/通信录名称（否则通话记录/通话面板会出现
// 「91381003 Outbound Call」这种怪名字，用户实测）。
var placeholderDisplayNames = map[string]bool{
	"outbound call":  true,
	"outboundcall":   true,
	"outbound":       true,
	"inbound call":   true,
	"inboundcall":    true,
	"unknown":        true,
	"anonymous":      true,
	"anonymous call": true,
}

// CleanDisplayName 过滤 SIP 信令里的占位显示名：命中占位（大小写、空格不敏感）
// 返回空串，调用方回退到号码/通信录名称；否则原样返回（去首尾空白）。
func CleanDisplayName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return ""
	}
	if placeholderDisplayNames[strings.ToLower(strings.Join(strings.Fields(n), " "))] {
		return ""
	}
	return n
}

// 无法规范化（空串/只有分隔符）返回空串。
func NormalizeNumber(remote string) string {
	s := strings.TrimSpace(remote)
	if s == "" {
		return ""
	}
	// 显示名 <URI> 形式：只取尖括号里的部分（没闭合就取到结尾）
	if i := strings.IndexByte(s, '<'); i >= 0 {
		s = s[i+1:]
		if j := strings.IndexByte(s, '>'); j >= 0 {
			s = s[:j]
		}
	}
	// sip:/sips: 前缀（大小写不敏感）
	if lower := strings.ToLower(s); strings.HasPrefix(lower, "sips:") {
		s = s[len("sips:"):]
	} else if strings.HasPrefix(lower, "sip:") {
		s = s[len("sip:"):]
	}
	// URI 参数（;user=phone）与主机部分（@host）
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '\r', '\n', '-', '(', ')', '[', ']', '.', '_', '/':
			continue // 常见分隔符：直接丢掉
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// ContactKey 号码 → 通信录主键（NormalizeNumber 的别名，语义更清楚）。
func ContactKey(number string) string { return NormalizeNumber(number) }

// SetContacts 注入通信录存储（启动前调用一次），并立刻载入内存快照。
func (a *App) SetContacts(s ContactStore) {
	a.contacts = s
	if s == nil {
		return
	}
	if err := a.ReloadContacts(); err != nil {
		logx.Logger().Warn("载入通信录失败（本次运行通信录为空）", "err", err)
	}
}

// Contacts 返回全量快照（副本，按规范化号码排序，结果稳定）。
// 拼音排序是界面层的事，这里只保证顺序确定。
func (a *App) Contacts() []Contact {
	a.contactMu.RLock()
	defer a.contactMu.RUnlock()
	out := make([]Contact, len(a.contactList))
	copy(out, a.contactList)
	return out
}

// ContactCount 通信录条数。
func (a *App) ContactCount() int {
	a.contactMu.RLock()
	defer a.contactMu.RUnlock()
	return len(a.contactList)
}

// LookupContact 按号码（任意形态）查联系人。命中第二个返回值为 true。
func (a *App) LookupContact(remote string) (Contact, bool) {
	key := NormalizeNumber(remote)
	if key == "" {
		return Contact{}, false
	}
	a.contactMu.RLock()
	defer a.contactMu.RUnlock()
	c, ok := a.contactMap[key]
	return c, ok
}

// ReloadContacts 重新载入全量快照（启动时与每次写操作后调用）。
func (a *App) ReloadContacts() error {
	if a.contacts == nil {
		return ErrContactsUnavailable
	}
	list, err := a.contacts.List()
	if err != nil {
		return err
	}
	// 逐条再截断一次：CSV 导入/网页写入的文本长度不在存储层的信任范围内
	for i := range list {
		list[i].Number = truncateContact(list[i].Number)
		list[i].Name = truncateContact(list[i].Name)
		list[i].Note = truncateContact(list[i].Note)
	}
	sort.Slice(list, func(i, j int) bool {
		return NormalizeNumber(list[i].Number) < NormalizeNumber(list[j].Number)
	})
	m := make(map[string]Contact, len(list))
	for _, c := range list {
		if key := NormalizeNumber(c.Number); key != "" {
			m[key] = c
		}
	}
	a.contactMu.Lock()
	a.contactMap = m
	a.contactList = list
	a.contactMu.Unlock()
	return nil
}

// SaveContact 保存联系人：prevNumber 为空 = 新增；非空 = 修改（号码可以改，
// 传修改前的号码）。号码重复返回 ErrContactNumberExists。
func (a *App) SaveContact(prevNumber string, c Contact) error {
	c.Number = truncateContact(c.Number)
	c.Name = truncateContact(c.Name)
	c.Note = truncateContact(c.Note)
	if NormalizeNumber(c.Number) == "" {
		return ErrContactNumberEmpty
	}
	return a.mutateContacts("save", func() error { return a.contacts.Save(prevNumber, c) })
}

// DeleteContacts 按号码（任意形态）删除若干条，返回实际删除条数。
func (a *App) DeleteContacts(numbers []string) (int, error) {
	n := 0
	err := a.mutateContacts("delete", func() error {
		var err error
		n, err = a.contacts.Delete(contactKeys(numbers))
		return err
	})
	return n, err
}

// SetContactsStarred 批量设置/取消常用标志，返回受影响条数。
func (a *App) SetContactsStarred(numbers []string, starred bool) (int, error) {
	n := 0
	err := a.mutateContacts("star", func() error {
		var err error
		n, err = a.contacts.SetStarred(contactKeys(numbers), starred)
		return err
	})
	return n, err
}

// ClearContacts 清空通信录，返回删除条数。
func (a *App) ClearContacts() (int, error) {
	n := 0
	err := a.mutateContacts("clear", func() error {
		var err error
		n, err = a.contacts.Clear()
		return err
	})
	return n, err
}

// mutateContacts 串行执行"写存储 + 重建内存快照"。
//
// 两步必须整体串行（contactWriteMu）：写入方可能并发（界面线程 + 多个 WS 连接），
// 若 A 写完后 B 再写、A 的快照却后于 B 完成，就会把旧数据当成最新快照发布出去。
// 存储层是单 worker 串行执行 SQL，read-your-writes 由它保证。
func (a *App) mutateContacts(action string, fn func() error) error {
	if a.contacts == nil {
		return ErrContactsUnavailable
	}
	a.contactWriteMu.Lock()
	defer a.contactWriteMu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	if err := a.ReloadContacts(); err != nil {
		return err
	}
	// 通信录已变更（动因 action）：广播给所有订阅者。
	// 放在这里而不是各个写方法里，是为了让**所有**写入路径都只维护一处——
	// 桌面界面（新增/编辑/删除/清空）、网页控制台（contact.* 方法）、
	// CSV/JSON 导入都会经过这里，多标签页与桌面列表据此刷新。
	a.bus.Publish("contact.changed", map[string]any{
		"action": action,
		"total":  a.ContactCount(),
	})
	return nil
}

// ExportContactsCSV 导出全部联系人为 CSV 文本（表头 + 号码/名称/备注/常用）。
func (a *App) ExportContactsCSV() (string, error) {
	if a.contacts == nil {
		return "", ErrContactsUnavailable
	}
	return MarshalContactsCSV(a.Contacts())
}

// ImportContactsCSV 导入 CSV 文本：按号码去重，已存在的只覆盖号码/名称/备注，
// **常用标志只增不减**（本地已标就保持，本地没标而导入标了则变常用），不存在
// 的整条新增。整行无法解析或没有号码的计入 Skipped。
func (a *App) ImportContactsCSV(text string) (ContactImportResult, error) {
	list, skipped := ParseContactsCSV(text)
	res := ContactImportResult{Skipped: skipped}
	err := a.mutateContacts("import", func() error {
		added, updated, err := a.contacts.Merge(list)
		res.Added, res.Updated = added, updated
		return err
	})
	if err != nil {
		return res, err
	}
	logx.Logger().Info("导入通信录",
		"added", res.Added, "updated", res.Updated, "skipped", res.Skipped)
	return res, nil
}

// contactKeys 号码列表 → 去空、去重的规范化键列表。
func contactKeys(numbers []string) []string {
	keys := make([]string, 0, len(numbers))
	seen := make(map[string]bool, len(numbers))
	for _, n := range numbers {
		k := NormalizeNumber(n)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	return keys
}

// truncateContact 按 rune 截断不可信文本。
func truncateContact(s string) string {
	r := []rune(s)
	if len(r) <= maxContactFieldLen {
		return s
	}
	return string(r[:maxContactFieldLen])
}

// ContactInfo 通话事件与状态快照里携带的联系人信息（REQ-UI-037 的网页侧）：
// 网页据此在号码旁显示名称与 ★、悬停显示备注，无需另查通信录。
type ContactInfo struct {
	Name    string `json:"name"`
	Note    string `json:"note"`
	Starred bool   `json:"starred"`
}
