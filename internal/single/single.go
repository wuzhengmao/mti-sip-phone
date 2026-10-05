// Package single 实现单实例运行与第二实例参数转发。
//
// 策略：
//   - 互斥：Windows 命名互斥体 / Linux flock 文件锁（均为平台原语）
//   - 转发：第一实例监听 127.0.0.1 随机端口的 TCP 端口（带 token 鉴权），
//     第二实例将命令行参数（如 sip:// 链接）发给第一实例后退出。
//   - 回退：若第一实例尚未启动 TCP 转发（启动早期），第二实例把参数写入
//     转发文件，第一实例启动完成后读取处理。
package single

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mti-sip-phone/internal/logx"
)

// acquireLock 按平台提供实现，见 mutex_windows.go / mutex_other.go。

// Handler 处理转发来的启动参数（如 sip:// 链接）。
type Handler func(args []string)

// Run 执行单实例逻辑：若是第一实例返回 release 函数（重启前调用以释放锁），
// 并返回 nil；若是第二实例，把参数转发给第一实例后返回 ErrAlreadyRunning。
func Run(lockPath string, args []string) (release func(), err error) {
	release, err = acquireLock(lockPath)
	if err != nil {
		if err == ErrAlreadyRunning {
			// 第二实例：转发参数后退出
			if len(args) > 0 {
				if ferr := Forward(lockPath, args); ferr != nil {
					logx.Logger().Debug("转发参数失败", "err", ferr)
				}
			}
			return nil, err
		}
		return nil, err
	}
	// 第一实例启动前，先尝试读取遗留的转发文件
	return release, handleForwardFile(lockPath+".fwd", nil)
}

// ErrAlreadyRunning 第二实例（已有实例在运行）。
var ErrAlreadyRunning = fmt.Errorf("已有实例在运行")

// lockID 由锁文件路径派生的稳定短标识（Windows 命名互斥体用）。
// 多实例沙箱各槽位的锁路径不同 → 标识不同 → 互不干扰；Windows 路径大小写
// 不敏感，先转小写再哈希。
func lockID(lockPath string) string {
	abs, err := filepath.Abs(lockPath)
	if err != nil {
		abs = lockPath
	}
	sum := sha1.Sum([]byte(strings.ToLower(abs)))
	return hex.EncodeToString(sum[:8])
}

// forwardFile 第二实例与第一实例间的转发文件。
func forwardFile(lockPath string) string { return lockPath + ".fwd" }

// handleForwardFile 读取并处理转发文件中的参数（若 handler 非空）。
func handleForwardFile(path string, handler Handler) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_ = os.Remove(path)
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		return nil // 损坏的转发文件直接忽略
	}
	if handler != nil && len(args) > 0 {
		go handler(args)
	}
	return nil
}

// ServeForward 第一实例启动 TCP 转发端口，接收第二实例的参数。
// 鉴权 token 内部随机生成（与网页接入无关，不暴露给用户配置）。
// 返回端口号与错误；listener 在进程生命周期内保持。
func ServeForward(lockPath string, handler Handler) (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	token := randomToken()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveForwardConn(conn, token, handler)
		}
	}()

	// 把端口号写入信息文件，第二实例据此连接
	info := map[string]any{"port": port, "token": token}
	data, _ := json.Marshal(info)
	infoPath := lockPath + ".info"
	_ = os.WriteFile(infoPath, data, 0o600)
	return port, nil
}

// randomToken 生成内部转发鉴权 token（32 位十六进制随机串）。
func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// serveForwardConn 处理一次转发连接。
func serveForwardConn(conn net.Conn, token string, handler Handler) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// 首行：token；次行：JSON 参数数组
	r := bufio.NewReader(conn)
	gotToken, err := r.ReadString('\n')
	if err != nil || gotToken != token+"\n" {
		return
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(line), &args); err != nil {
		return
	}
	logx.Logger().Info("收到第二实例转发参数", "count", len(args))
	if handler != nil {
		go handler(args)
	}
}

// Forward 第二实例：尝试把参数转发给第一实例。
// 优先走 TCP（信息文件存在时），回退写转发文件。
func Forward(lockPath string, args []string) error {
	// 读第一实例的端口信息
	infoPath := lockPath + ".info"
	if data, err := os.ReadFile(infoPath); err == nil {
		var info struct {
			Port  int    `json:"port"`
			Token string `json:"token"`
		}
		if json.Unmarshal(data, &info) == nil && info.Port > 0 {
			if tryForwardTCP(info.Port, info.Token, args) {
				return nil
			}
		}
	}
	// TCP 不可用：写转发文件（第一实例启动后会读取）
	data, _ := json.Marshal(args)
	_ = os.MkdirAll(filepath.Dir(lockPath), 0o755)
	return os.WriteFile(forwardFile(lockPath), data, 0o600)
}

// tryForwardTCP 通过 TCP 把参数发给第一实例，返回是否成功。
func tryForwardTCP(port int, token string, args []string) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	payload, _ := json.Marshal(args)
	_, err = fmt.Fprintf(conn, "%s\n%s\n", token, payload)
	return err == nil
}
