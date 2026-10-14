package wsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/web"
)

// FindFreeWSPort 探测空闲 WS 端口：从 start 开始，被占用则 +1 递增
// （最多尝试 100 个），返回可用端口。
func FindFreeWSPort(start int, allowRemote bool) int {
	host := "127.0.0.1"
	if allowRemote {
		host = "0.0.0.0" // 按实际要绑的地址探，免得"回环看着空、实际绑不上"
	}
	for i := 0; i < 100; i++ {
		addr := fmt.Sprintf("%s:%d", host, start+i)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = ln.Close()
			return start + i
		}
	}
	return start
}

// Server 本地 WS 服务：仅绑定 127.0.0.1，供本机网页连接。
// 安全性由仅监听回环地址保证（不再使用 token 鉴权，按用户要求）。
//
// 路由：
//   - GET  /                    测试页面
//   - GET  /sdk/*               JS SDK 静态文件
//   - GET  /ws                  WebSocket 端点
//   - GET  /api/contacts.csv    导出通信录 CSV（网页一键下载，REQ-CONTACT-004）
//   - POST /api/contacts/import 导入通信录 CSV（请求体就是 CSV 文本）
type Server struct {
	app   *core.App
	video *videoHub // 网页视频订阅中枢（REQ-VIDEO-009）
	// unreg 「WS 断开自动取消注册」状态机（REQ-WS-020，见 unreg.go）
	unreg *unregGuard
	// enum 设备枚举的单飞 + 短缓存（音频/摄像头，见 handler_config.go）
	enum enumGuard
	// port 当前进程实际监听的端口（启动时可能因占用自动 +1，
	// 与配置里的期望值不一定相同，网页要显示"实际端口"）
	port atomic.Int64
	// allowRemote 允许远程连接（配置项 ws.allowRemote，启动时设定一次）：
	// 为真时监听 0.0.0.0 且**不再拒绝非回环来源**；为假（默认）保持
	// "只监听回环 + 拒绝非回环"的既有安全边界。
	// ⚠️ 放开后没有鉴权，见 config.WSConfig.AllowRemote 的说明。
	allowRemote atomic.Bool
}

// RemoteAllowed 当前是否允许远程连接（配置值，供界面/日志展示）。
func (s *Server) RemoteAllowed() bool { return s.allowRemote.Load() }

// Port 当前 WS 服务实际监听的端口（未启动时为 0）。
func (s *Server) Port() int { return int(s.port.Load()) }

// New 创建服务。视频中枢同时注册为 core.App 的视频帧附加订阅者。
func New(app *core.App) *Server {
	return &Server{app: app, video: newVideoHub(app), unreg: newUnregGuard(app)}
}

// Handler 返回 HTTP 处理器（供测试与内嵌使用）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/sdk/", s.handleStatic)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/contacts.csv", s.handleContactsExport)
	mux.HandleFunc("/api/contacts/import", s.handleContactsImport)
	mux.HandleFunc("/api/contacts", s.handleContactsJSON) // JSON 的 REST 接口（GET 导出 / POST 导入）
	// CORS：允许任意网页来源访问（本机页面可能从任意端口打开）
	return withCORS(mux)
}

// ListenAndServe 启动服务，监听 127.0.0.1:port，阻塞直到 ctx 取消。
func (s *Server) ListenAndServe(ctx context.Context, port int, allowRemote bool) error {
	s.allowRemote.Store(allowRemote)
	host := "127.0.0.1"
	if allowRemote {
		host = "0.0.0.0"
		logx.Logger().Warn("网页接入已允许远程连接：WS 接口**没有鉴权**，"+
			"同网段任何设备都能控制本机软电话（拨号/读通信录/改配置/重启）",
			"port", port)
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("WS 服务监听 %s 失败（端口可能被占用）: %w", addr, err)
	}
	s.port.Store(int64(port))
	logx.Logger().Info("本地 WS 服务已启动", "addr", "http://"+addr, "远程连接", allowRemote)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logx.Logger().Info("WS 服务停止")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// pageFiles 页面/静态资源的白名单：URL 路径 → 内嵌文件（embed.FS 的根是 web/）。
//
// 明确列白名单而不是做目录遍历：内嵌资源里有 .d.ts、构建残留等不该对外的东西，
// 且这样 404 行为可预期。
var pageFiles = map[string]string{
	"/":                     "index.html",
	"/index.html":           "index.html",
	"/history":              "index.html", // hash 路由的别名，便于直接访问/收藏
	"/contacts":             "index.html",
	"/settings":             "index.html",
	"/diag":                 "index.html",
	"/test.html":            "index.html", // 旧测试页的兼容入口（已并入控制台）
	"/css/app.css":          "css/app.css",
	"/js/app.js":            "js/app.js",
	"/js/common.js":         "js/common.js",
	"/js/icons.js":          "js/icons.js",
	"/js/store.js":          "js/store.js",
	"/js/callbar.js":        "js/callbar.js",
	"/js/views/call.js":     "js/views/call.js",
	"/js/views/history.js":  "js/views/history.js",
	"/js/views/contacts.js": "js/views/contacts.js",
	"/js/views/settings.js": "js/views/settings.js",
	"/js/views/diag.js":     "js/views/diag.js",
}

// handleIndex 网页控制台（无需注入 token；仅回环来源）。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	name, ok := pageFiles[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.serveAsset(w, name)
}

// handleStatic JS SDK 静态文件（/sdk/*）。
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/sdk/")
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	s.serveAsset(w, "sdk/"+name)
}

// serveAsset 输出一份内嵌资源。
//
// 一律 Cache-Control: no-cache：资源内嵌在二进制里，升级软电话后浏览器若还用
// 缓存里的旧版，新协议/新页面会"怎么改都不生效"（踩过：旧 SDK 让取消订阅后画面
// 仍跟着旋转）。**新增静态资源必须走这个函数**，别漏了 no-cache。
func (s *Server) serveAsset(w http.ResponseWriter, name string) {
	data, err := web.Assets.ReadFile(name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", contentTypeOf(name))
	_, _ = w.Write(data)
}

// contentTypeOf 按扩展名给 Content-Type（浏览器对 text/plain 的 <script> 会拒绝执行）。
func contentTypeOf(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".d.ts"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// maxContactsCSVBody 导入请求体上限（与桌面端导入同一个量级）。
const maxContactsCSVBody = 8 << 20

// handleContactsJSON 通信录的 JSON REST 接口（REQ-CONTACT-005）：
//
//	GET  /api/contacts  → { "count": N, "contacts": [ {number,name,note,starred}, ... ] }
//	POST /api/contacts  → 请求体是上面这种对象**或裸数组**，返回 {added,updated,skipped,total}
//
// 给网页用：XHR/fetch 直接收发结构化数据，不用自己解析 CSV（CSV 那套仍然保留，
// 见 /api/contacts.csv 与 /api/contacts/import）。合并语义与 CSV 导入完全一致。
func (s *Server) handleContactsJSON(w http.ResponseWriter, r *http.Request) {
	if !s.remoteAllowed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	switch r.Method {
	case http.MethodGet:
		data, err := s.app.ExportContactsJSON()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(data)

	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxContactsCSVBody))
		if err != nil {
			http.Error(w, "通信录 JSON 读取失败或超过 8MB", http.StatusRequestEntityTooLarge)
			return
		}
		res, err := s.app.ImportContactsJSON(body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		out := map[string]any{
			"added":   res.Added,
			"updated": res.Updated,
			"skipped": res.Skipped,
			"total":   s.app.ContactCount(),
		}
		if err != nil {
			out["error"] = err.Error()
			w.WriteHeader(http.StatusBadRequest) // JSON 语法/格式错误属客户端问题
		}
		_ = json.NewEncoder(w).Encode(out)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleContactsExport 导出通信录 CSV（GET /api/contacts.csv）。
//
// 网页一键导出走这条 **HTTP** 路径而不是 WS：CSV 是一整份文本，走 WS 会受
// 单条消息大小限制（默认 32KB，已放宽到 4MB 也仍是限制），而且浏览器拿到
// 二进制/文本后还要自己造 Blob 再下载；直连 HTTP 可以流式下载、没有大小问题。
func (s *Server) handleContactsExport(w http.ResponseWriter, r *http.Request) {
	if !s.remoteAllowed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	csv, err := s.app.ExportContactsCSV()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	name := "contacts-" + time.Now().Format("20060102") + ".csv"
	// 条数放响应头：网页一键下载是浏览器直接存文件，拿不到正文里的信息，
	// SDK 用它在页面上显示"已导出 N 位联系人"
	w.Header().Set("X-Contact-Count", fmt.Sprint(s.app.ContactCount()))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// 跨域读取响应头需要显式允许（页面由别的端口提供时）
	w.Header().Set("Access-Control-Expose-Headers", "X-Contact-Count")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(csv))
}

// handleContactsImport 导入通信录 CSV（POST /api/contacts/import，请求体即 CSV 文本）。
// 返回 JSON：{added, updated, skipped, total, error?}。
func (s *Server) handleContactsImport(w http.ResponseWriter, r *http.Request) {
	if !s.remoteAllowed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxContactsCSVBody))
	if err != nil {
		http.Error(w, "CSV 内容读取失败或超过 8MB", http.StatusRequestEntityTooLarge)
		return
	}
	res, err := s.app.ImportContactsCSV(string(body))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	data := map[string]any{
		"added":   res.Added,
		"updated": res.Updated,
		"skipped": res.Skipped,
		"total":   s.app.ContactCount(),
	}
	if err != nil {
		data["error"] = err.Error()
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(data)
}

// handleWS WebSocket 连接（无 token 鉴权，仅接受本机回环连接）。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// 仅接受来自本机的连接（服务本身只绑定 127.0.0.1，这里再兜底校验）
	if !s.remoteAllowed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 允许任意来源网页连接（本机任意端口的页面都是合法客户端），安全性由仅回环监听保证
		InsecureSkipVerify: true,
	})
	if err != nil {
		logx.Logger().Debug("WS 握手失败", "err", err)
		return
	}
	// 放开单条消息的读上限（coder/websocket 默认 32768 字节）：通信录导入
	// 是一整份 CSV 文本，稍大一点就会被默认上限拒掉（表现为连接断开、没有
	// 任何业务错误，极难排查）。仅回环连接，4MB 足够且没有滥用面。
	conn.SetReadLimit(4 << 20)
	logx.Logger().Info("网页已连接", "remote", r.RemoteAddr)
	s.serveConn(r.Context(), conn)
}

// remoteAllowed 该请求是否放行：默认只放回环来源；配置里开了"允许远程连接"
// 就一律放行（此时也没有鉴权，安全靠网络边界）。
func (s *Server) remoteAllowed(r *http.Request) bool {
	if s.allowRemote.Load() {
		return true
	}
	return isLoopback(r.RemoteAddr)
}

// isLoopback 判断远程地址是否为本机回环。
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// withCORS 允许跨域访问（页面可能由其他本机端口提供）。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		// Chrome 的 Private Network Access：从公网页面访问本机（私有网络）时，
		// 预检要显式允许，否则请求会被拦掉（本机页面之间不需要）
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
