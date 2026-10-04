package core

// 通信录 CSV 编解码（REQ-UI-036）。
//
// 放在 core 而不是 internal/contacts：存储包要 import core（实现 ContactStore），
// core 再 import 存储包就成环了；而 CSV 是"通信录数据"的一部分，
// 桌面（文件对话框）与网页（WS `contact.export`/`contact.import`）两条路径
// 都必须走同一份实现，格式才不会漂移。
//
// 格式（表头固定，列序固定）：
//
//	号码,名称,备注,常用
//	2999,张三,前台,是
//
// - 导出**带 UTF-8 BOM**：中文 Windows 上 Excel 双击打开不带 BOM 的 UTF-8 CSV
//   会把中文显示成乱码（用户要求"导出成标准的 csv 格式"且要能用表格软件打开）。
// - 导入**容忍**：BOM、缺表头（首行就是数据）、列数不足/多余、空行、
//   单双引号不规范、常用列写 是/否/1/0/true/false/yes/no。

import (
	"encoding/csv"
	"errors"
	"io"
	"strings"
)

// contactCSVHeader 导出表头（导入时按它识别首行是否为表头，并据此映射列）。
var contactCSVHeader = []string{"号码", "名称", "备注", "常用"}

// csvBOM UTF-8 BOM（导出时写、导入时剥）。
// 必须写成转义：源码里直接放 BOM 字符会被 Go 编译器判为
// "invalid BOM in the middle of the file"。
var csvBOM = string(rune(0xFEFF))

// MarshalContactsCSV 导通信录为 CSV 文本（带表头与 BOM）。
func MarshalContactsCSV(list []Contact) (string, error) {
	var b strings.Builder
	b.WriteString(csvBOM)
	w := csv.NewWriter(&b)
	if err := w.Write(contactCSVHeader); err != nil {
		return "", err
	}
	for _, c := range list {
		star := "否"
		if c.Starred {
			star = "是"
		}
		if err := w.Write([]string{c.Number, c.Name, c.Note, star}); err != nil {
			return "", err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// ParseContactsCSV 解析 CSV 文本，返回解析出的联系人（**未去重**，去重与合并
// 由存储层的 Merge 按规范化号码完成）与跳过的行数（没有号码，或整行无法解析）。
//
// 首行第一列是「号码」/「number」/「phone」就按表头映射列（允许列序不同），
// 否则整份文件按列序（号码, 名称, 备注, 常用）解析——用户手写的 CSV 常常没有表头。
func ParseContactsCSV(text string) (list []Contact, skipped int) {
	text = strings.TrimPrefix(text, csvBOM)
	text = strings.TrimPrefix(text, csvBOM) // 双保险：个别工具会写两遍
	if strings.TrimSpace(text) == "" {
		return nil, 0
	}
	r := csv.NewReader(strings.NewReader(text))
	// 容忍列数不齐与不规范引号：手改过的 CSV 很常见，不该整份拒收
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	col := map[string]int{"number": 0, "name": 1, "note": 2, "starred": 3}
	first := true
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// csv.Reader 遇到解析错误会跳过该行并继续，这里按"跳过一行"计数
			skipped++
			continue
		}
		if first {
			first = false
			if m, ok := contactHeaderColumns(rec); ok {
				col = m
				continue // 表头行不产生数据
			}
		}
		field := func(key string) string {
			i, ok := col[key]
			if !ok || i < 0 || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		number := field("number")
		if NormalizeNumber(number) == "" {
			skipped++
			continue
		}
		list = append(list, Contact{
			Number:  number,
			Name:    field("name"),
			Note:    field("note"),
			Starred: parseCSVBool(field("starred")),
		})
	}
	return list, skipped
}

// contactHeaderColumns 判断首行是否为表头并给出列映射。
//
// 判定规则：**只要有任一单元格是已知表头名**就当表头（不要求「号码」必须在
// 第一列）——列序可以不同，多余列忽略。数据行的号码/名称/备注几乎不可能
// 恰好叫「号码」「name」这些名字，误判概率可以接受；反过来漏判（用户改了列序）
// 的后果更糟：整行被当成数据，号码列会解析错。
func contactHeaderColumns(rec []string) (map[string]int, bool) {
	col := map[string]int{"number": 0, "name": 1, "note": 2, "starred": 3}
	found := false
	for i, h := range rec {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "号码", "电话", "电话号码", "number", "phone", "tel":
			col["number"] = i
			found = true
		case "名称", "名字", "姓名", "显示名", "name":
			col["name"] = i
			found = true
		case "备注", "note", "remark", "comment":
			col["note"] = i
			found = true
		case "常用", "常用联系人", "star", "starred", "favorite":
			col["starred"] = i
			found = true
		}
	}
	return col, found
}

// parseCSVBool 解析"常用"列：是/否/1/0/true/false/yes/no/y/n（大小写不敏感）。
func parseCSVBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "是", "1", "true", "yes", "y", "t", "√", "✓":
		return true
	}
	return false
}
