package wsapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
	"mti-sip-phone/web"
)

// FindFreeWSPort 探测空闲 WS 端口：从 start 开始，被占用则 +1 递增
// （最多尝试 100 个），返回可用端口。
func FindFreeWSPort(start int) int {
	for i := 0; i < 100; i++ {
		addr := fmt.Sprintf("127.0.0.1:%d", start+i)
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
//   - GET /              测试页面
//   - GET /sdk/*         JS SDK 静态文件
//   - GET /ws            WebSocket 端点
type Server struct {
	app   *core.App
	video *videoHub // 网页视频订阅中枢（REQ-VIDEO-009）
}

// New 创建服务。视频中枢同时注册为 core.App 的视频帧附加订阅者。
func New(app *core.App) *Server {
	return &Server{app: app, video: newVideoHub(app)}
}

// Handler 返回 HTTP 处理器（供测试与内嵌使用）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/sdk/", s.handleStatic)
	mux.HandleFunc("/ws", s.handleWS)
	// CORS：允许任意网页来源访问（本机页面可能从任意端口打开）
	return withCORS(mux)
}

// ListenAndServe 启动服务，监听 127.0.0.1:port，阻塞直到 ctx 取消。
func (s *Server) ListenAndServe(ctx context.Context, port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("WS 服务监听 %s 失败（端口可能被占用）: %w", addr, err)
	}
	logx.Logger().Info("本地 WS 服务已启动", "addr", "http://"+addr)

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

// handleIndex 测试页面（无需注入 token）。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	html, err := web.Assets.ReadFile("test.html")
	if err != nil {
		http.Error(w, "页面资源缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(html)
}

// handleStatic JS SDK 静态文件。
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/sdk/")
	if name == "" {
		http.NotFound(w, r)
		return
	}
	data, err := web.Assets.ReadFile("sdk/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// 必须禁缓存：SDK 内嵌在二进制里，升级软电话后浏览器若还用着缓存里的旧
	// SDK，新协议/新行为会"怎么改都不生效"（踩过：取消订阅后画面仍跟着旋转，
	// 其实是旧 SDK）。测试页（handleIndex）同样带 no-cache。
	w.Header().Set("Cache-Control", "no-cache")
	if strings.HasSuffix(name, ".js") {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	} else if strings.HasSuffix(name, ".d.ts") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = w.Write(data)
}

// handleWS WebSocket 连接（无 token 鉴权，仅接受本机回环连接）。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// 仅接受来自本机的连接（服务本身只绑定 127.0.0.1，这里再兜底校验）
	if !isLoopback(r.RemoteAddr) {
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
	logx.Logger().Info("网页已连接", "remote", r.RemoteAddr)
	s.serveConn(r.Context(), conn)
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
