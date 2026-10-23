package wsapi

// 通信录的网页接口（REQ-WS-015）回归测试：导出 CSV、导入 CSV（按号码去重）、
// 大消息不被默认读上限拒掉，以及通话事件里带 contact 对象（REQ-UI-037）。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"mti-sip-phone/internal/core"
)

// fakeContacts 内存版通信录存储（合并语义与真实存储一致：命中只覆盖
// 号码/名称/备注、保留常用标志）。
type fakeContacts struct {
	list []core.Contact
}

func (f *fakeContacts) List() ([]core.Contact, error) {
	return append([]core.Contact(nil), f.list...), nil
}

func (f *fakeContacts) Save(prevNumber string, c core.Contact) error {
	key := core.ContactKey(c.Number)
	for i := range f.list {
		if core.ContactKey(f.list[i].Number) == key {
			f.list[i] = c
			return nil
		}
	}
	f.list = append(f.list, c)
	return nil
}

func (f *fakeContacts) SetStarred(numbers []string, starred bool) (int, error) {
	n := 0
	for _, num := range numbers {
		for i := range f.list {
			if core.ContactKey(f.list[i].Number) == core.ContactKey(num) {
				f.list[i].Starred = starred
				n++
			}
		}
	}
	return n, nil
}

func (f *fakeContacts) Delete(numbers []string) (int, error) { return 0, nil }
func (f *fakeContacts) Clear() (int, error)                  { return 0, nil }

func (f *fakeContacts) Merge(in []core.Contact) (int, int, error) {
	added, updated := 0, 0
	for _, c := range in {
		key := core.ContactKey(c.Number)
		hit := false
		for i := range f.list {
			if core.ContactKey(f.list[i].Number) == key {
				// 与真实存储一致：只覆盖号码/名称/备注，**常用标志取「或」**（只增不减）
				f.list[i].Number, f.list[i].Name, f.list[i].Note = c.Number, c.Name, c.Note
				f.list[i].Starred = f.list[i].Starred || c.Starred
				hit = true
				break
			}
		}
		if hit {
			updated++
		} else {
			f.list = append(f.list, c)
			added++
		}
	}
	return added, updated, nil
}

func (f *fakeContacts) Close() error { return nil }

// TestContactExportImportFlow 导出 → 改动 → 再导入：按号码去重、覆盖名称备注、
// 保留常用标志（REQ-UI-036 的网页路径）。
func TestContactExportImportFlow(t *testing.T) {
	ts, app := newTestServer(t)
	app.SetContacts(&fakeContacts{list: []core.Contact{
		{Number: "2999", Name: "张三", Note: "前台", Starred: true},
		{Number: "2002", Name: "王五"},
	}})
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	// 导出
	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "contact.export", Params: json.RawMessage("{}")})
	resp := waitResponse(t, conn)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("导出应成功，收到: %+v", resp)
	}
	data, _ := resp.Data.(map[string]any)
	csvText, _ := data["csv"].(string)
	if !strings.Contains(csvText, "2999") || !strings.Contains(csvText, "号码") {
		t.Fatalf("导出的 CSV 内容不对：%q", csvText)
	}

	// 导入：已有号码只覆盖名称/备注（常用保留），新号码整条新增
	// 2999 本地常用、CSV 不是 → 保持常用；2901 新号码取 CSV；2002 本地不常用、
	// CSV 是常用 → 变成常用
	importCSV := "号码,名称,备注,常用\n2999,张三丰,新备注,否\n2901,李四,,是\n2002,王五,,是\n"
	params, _ := json.Marshal(map[string]string{"csv": importCSV})
	sendRequest(t, conn, Message{ID: json.RawMessage("2"), Method: "contact.import", Params: params})
	resp = waitResponse(t, conn)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("导入应成功，收到: %+v", resp)
	}
	data, _ = resp.Data.(map[string]any)
	if data["added"] != float64(1) || data["updated"] != float64(2) {
		t.Fatalf("导入计数不对：%+v", data)
	}
	if c, ok := app.LookupContact("2999"); !ok || c.Name != "张三丰" || c.Note != "新备注" || !c.Starred {
		t.Fatalf("已存在条目应覆盖名称/备注并保留常用：%+v ok=%v", c, ok)
	}
	if c, ok := app.LookupContact("2901"); !ok || c.Name != "李四" || !c.Starred {
		t.Fatalf("新号码应整条新增：%+v ok=%v", c, ok)
	}
	if c, ok := app.LookupContact("2002"); !ok || !c.Starred {
		t.Fatalf("本地不常用而 CSV 是常用，应变成常用：%+v ok=%v", c, ok)
	}

	// 参数缺失
	sendRequest(t, conn, Message{ID: json.RawMessage("3"), Method: "contact.import", Params: json.RawMessage("{}")})
	resp = waitResponse(t, conn)
	if resp.OK == nil || *resp.OK || resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("缺 csv 参数应返回 invalid_params，收到: %+v", resp)
	}
}

// TestContactUnavailable 未接入通信录存储时返回 contacts_unavailable
// （而不是笼统的 internal）。
func TestContactUnavailable(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "contact.export", Params: json.RawMessage("{}")})
	resp := waitResponse(t, conn)
	if resp.OK == nil || *resp.OK || resp.Error == nil || resp.Error.Code != CodeContactsFailed {
		t.Fatalf("应返回 contacts_unavailable，收到: %+v", resp)
	}
}

// TestContactImportLargePayload 导入大 CSV 必须能过——coder/websocket 默认单条
// 消息读上限 32768 字节，服务端已放开到 4MB（否则这里会以"连接断开"的形式失败）。
func TestContactImportLargePayload(t *testing.T) {
	ts, app := newTestServer(t)
	app.SetContacts(&fakeContacts{})
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	var b strings.Builder
	b.WriteString("号码,名称,备注,常用\n")
	const rows = 3000 // 每行约 30 字节 → 合计约 90KB，远超默认读上限
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "1380000%04d,姓名%d,备注文字长长的占位内容,否\n", i, i)
	}
	params, _ := json.Marshal(map[string]string{"csv": b.String()})
	sendRequest(t, conn, Message{ID: json.RawMessage("1"), Method: "contact.import", Params: params})
	resp := waitResponse(t, conn)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("大 CSV 导入应成功（读上限已放开），收到: %+v", resp)
	}
	data, _ := resp.Data.(map[string]any)
	if data["added"] != float64(rows) {
		t.Fatalf("应导入 %d 条，实际 %+v", rows, data)
	}
	if app.ContactCount() != rows {
		t.Fatalf("通信录条数应为 %d，实际 %d", rows, app.ContactCount())
	}
}

// TestCallEventCarriesContact 通话事件与状态快照里带 contact 对象（网页显示名称/★/备注）。
func TestCallEventCarriesContact(t *testing.T) {
	ts, app := newTestServer(t)
	app.SetContacts(&fakeContacts{list: []core.Contact{
		{Number: "2999", Name: "张三", Note: "前台", Starred: true},
		{Number: "2002", Name: "王五"},
	}})
	conn := dialWS(t, ts.URL)
	waitEvent(t, conn, "system.state")

	app.OnIncoming("c1", core.IncomingCall{Remote: "sip:2999@10.168.4.40"})
	ev := waitEvent(t, conn, "call.incoming")
	data, _ := ev.Data.(map[string]any)
	call, _ := data["call"].(map[string]any)
	contact, _ := call["contact"].(map[string]any)
	if contact == nil {
		t.Fatalf("通话事件应带 contact 对象：%+v", data)
	}
	if contact["name"] != "张三" || contact["note"] != "前台" || contact["starred"] != true {
		t.Fatalf("contact 内容不对：%+v", contact)
	}

	// 未命中通信录的通话不带 contact 字段（omitempty）
	app.OnIncoming("c2", core.IncomingCall{Remote: "2901"})
	ev = waitEvent(t, conn, "call.incoming")
	data, _ = ev.Data.(map[string]any)
	call, _ = data["call"].(map[string]any)
	if _, ok := call["contact"]; ok {
		t.Fatalf("未命中通信录不该带 contact：%+v", call)
	}
}

// TestContactsHTTPEndpoints 通信录的 HTTP 一键导入导出（REQ-CONTACT-004）：
// 网页端一键路径走 HTTP，没有 WS 的单条消息大小限制。
func TestContactsHTTPEndpoints(t *testing.T) {
	ts, app := newTestServer(t)
	app.SetContacts(&fakeContacts{list: []core.Contact{
		{Number: "2999", Name: "张三", Note: "前台", Starred: true},
	}})

	// 导出
	resp, err := http.Get(ts.URL + "/api/contacts.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type 应为 text/csv，实际 %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, ".csv") {
		t.Fatalf("应带下载文件名，实际 %q", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "2999") || !strings.HasPrefix(string(body), string(rune(0xFEFF))+"号码") {
		t.Fatalf("导出的 CSV 不对：%q", string(body))
	}
	if n := resp.Header.Get("X-Contact-Count"); n != "1" {
		t.Fatalf("X-Contact-Count 应为 1，实际 %q", n)
	}

	// 导入（超 32KB，验证 HTTP 路径不受 WS 单条消息上限影响）
	var big strings.Builder
	big.WriteString("号码,名称,备注,常用\n")
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&big, "1390000%04d,姓名%d,备注占位内容占位内容,否\n", i, i)
	}
	resp2, err := http.Post(ts.URL+"/api/contacts/import", "text/csv", strings.NewReader(big.String()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("导入应 200，实际 %d", resp2.StatusCode)
	}
	var res map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res["added"] != float64(3000) {
		t.Fatalf("应新增 3000 条，实际 %+v", res)
	}
	if app.ContactCount() != 3001 {
		t.Fatalf("总数应为 3001，实际 %d", app.ContactCount())
	}

	// 方法不对：405
	resp3, err := http.Get(ts.URL + "/api/contacts/import")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET 导入接口应 405，实际 %d", resp3.StatusCode)
	}
}

// TestContactsJSONREST 通信录的 JSON REST 接口（REQ-CONTACT-005）：
// GET /api/contacts 导出、POST /api/contacts 导入（接受裸数组与带外壳的对象）。
func TestContactsJSONREST(t *testing.T) {
	ts, app := newTestServer(t)
	app.SetContacts(&fakeContacts{list: []core.Contact{
		{Number: "2999", Name: "张三", Note: "前台", Starred: true},
	}})

	// 导出
	resp, err := http.Get(ts.URL + "/api/contacts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type 应为 application/json，实际 %q", ct)
	}
	var doc struct {
		Count    int `json:"count"`
		Contacts []struct {
			Number  string `json:"number"`
			Name    string `json:"name"`
			Note    string `json:"note"`
			Starred bool   `json:"starred"`
		} `json:"contacts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Count != 1 || len(doc.Contacts) != 1 || doc.Contacts[0].Name != "张三" || !doc.Contacts[0].Starred {
		t.Fatalf("导出的 JSON 不对：%+v", doc)
	}

	// 导入：裸数组（含一个新号码 + 一条没有号码的脏数据）
	body := `[{"number":"2901","name":"李四","note":"二楼","starred":true},{"name":"没有号码"}]`
	resp2, err := http.Post(ts.URL+"/api/contacts", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("导入应 200，实际 %d", resp2.StatusCode)
	}
	var res map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res["added"] != float64(1) || res["skipped"] != float64(1) || res["total"] != float64(2) {
		t.Fatalf("导入结果不对：%+v", res)
	}
	if c, ok := app.LookupContact("2901"); !ok || c.Name != "李四" || !c.Starred {
		t.Fatalf("新号码应导入：%+v ok=%v", c, ok)
	}

	// 导入：带外壳的对象（导出→导入可直接回灌），且常用只增不减
	doc2 := `{"count":1,"contacts":[{"number":"2999","name":"张三丰","note":"改过","starred":false}]}`
	resp3, err := http.Post(ts.URL+"/api/contacts", "application/json", strings.NewReader(doc2))
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if c, ok := app.LookupContact("2999"); !ok || c.Name != "张三丰" || !c.Starred {
		t.Fatalf("回灌应覆盖名称/备注并保留常用：%+v ok=%v", c, ok)
	}

	// 语法错误 → 400
	resp4, err := http.Post(ts.URL+"/api/contacts", "application/json", strings.NewReader("{不是 JSON"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400，实际 %d", resp4.StatusCode)
	}

	// 方法不允许
	req5, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/contacts", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp5, err := http.DefaultClient.Do(req5)
	if err != nil {
		t.Fatal(err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE 应 405，实际 %d", resp5.StatusCode)
	}
}
