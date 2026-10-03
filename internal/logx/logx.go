// Package logx 提供分级日志：写入本地文件（按大小滚动、归档压缩）与
// 可选的控制台输出。
//
// 用法：
//
//	logx.Init(level, dir, true, false) // 文件开、控制台关
//	logx.Logger().Info("注册成功", "account", "1001")
//	logx.TraceSIP("收到", msg)         // 记录 SIP 信令（DEBUG 级，自动脱敏）
//	logx.SetLevel("debug")             // 运行中调整级别
package logx

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxFileSize = 10 * 1024 * 1024 // 单个日志文件最大 10MB
	maxFiles    = 10               // 最多保留 10 份（当前 + 9 份 gzip 归档）
	logFileName = "sip-phone.log"
)

var (
	mu     sync.Mutex
	logger *slog.Logger
	file   *rotatingFile
	level  = new(slog.LevelVar) // 运行中可调级别
	// traceOut SIP 信令输出目标：与文件/控制台开关一致，原始文本逐行写出
	traceOut io.Writer
)

// Init 初始化日志：level 为 debug/info/warn/error；dir 为空时使用 <配置目录>/logs。
// toFile 控制是否写文件；toConsole 控制是否输出标准错误。
// 惯例：写文件时不输出控制台（避免重复），关闭文件时才输出控制台。
func Init(lvl, dir string, toFile, toConsole bool) error {
	mu.Lock()
	defer mu.Unlock()

	var l slog.Level
	switch strings.ToLower(lvl) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	level.Set(l)

	var writers []io.Writer
	if toFile {
		f, err := newRotatingFile(dir)
		if err != nil {
			return fmt.Errorf("初始化日志文件失败: %w", err)
		}
		file = f
		writers = append(writers, f)
	}
	if toConsole {
		writers = append(writers, os.Stderr)
	}
	if len(writers) == 0 {
		// 既不写文件也不写控制台：丢弃日志
		writers = append(writers, io.Discard)
	}

	w := io.MultiWriter(writers...)
	logger = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
	}))
	// 信令输出目标：写文件时进文件，否则进控制台（与日志输出开关一致）
	if toFile && file != nil {
		traceOut = file
	} else if toConsole {
		traceOut = os.Stderr
	} else {
		traceOut = io.Discard
	}
	logger.Info("日志初始化完成",
		"level", l.String(),
		"toFile", toFile,
		"toConsole", toConsole,
		"file", logPath(dir))
	return nil
}

// SetLevel 运行中调整日志级别。
// 注意：不能在持锁状态下调用 Logger()（内部会再次加锁导致死锁），
// 这里先释放锁再记录。
func SetLevel(lvl string) {
	mu.Lock()
	switch strings.ToLower(lvl) {
	case "debug":
		level.Set(slog.LevelDebug)
	case "warn":
		level.Set(slog.LevelWarn)
	case "error":
		level.Set(slog.LevelError)
	default:
		level.Set(slog.LevelInfo)
	}
	mu.Unlock()
	Logger().Info("日志级别已调整", "level", strings.ToLower(lvl))
}

// Logger 返回全局 logger；Init 之前调用返回默认 logger。
func Logger() *slog.Logger {
	mu.Lock()
	defer mu.Unlock()
	if logger == nil {
		return slog.Default()
	}
	return logger
}

// Close 关闭日志文件。
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
		file = nil
	}
}

// logPath 计算日志文件路径（与 newRotatingFile 相同规则，供日志输出）。
func logPath(dir string) string {
	if dir == "" {
		cd, err := os.UserConfigDir()
		if err == nil {
			dir = filepath.Join(cd, "mti-sip-phone", "logs")
		}
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, logFileName)
}

// ---------------------------------------------------------------------------
// SIP 信令与敏感信息脱敏
// ---------------------------------------------------------------------------

// 需要脱敏的字段：认证头、密码参数、sip URI 中的密码
var secretPatterns = []*regexp.Regexp{
	// Authorization / Proxy-Authorization 整行
	regexp.MustCompile(`(?mi)^(\s*)(Proxy-)?Authorization\s*:.*$`),
	// password=xxx 参数
	regexp.MustCompile(`(?i)(password\s*=\s*)[^\s,;>"]+`),
	// sip:user:pass@host 形式 URI 中的密码
	regexp.MustCompile(`(?i)(sips?:[^@\s;>"]*:)[^@\s;>"]+(@)`),
}

// MaskSecrets 对 SIP 消息中的凭据做脱敏，用于日志输出。
func MaskSecrets(s string) string {
	out := s
	out = secretPatterns[0].ReplaceAllString(out, `${1}${2}Authorization: ***脱敏***`)
	out = secretPatterns[1].ReplaceAllString(out, `${1}***脱敏***`)
	out = secretPatterns[2].ReplaceAllString(out, `${1}***脱敏***${2}`)
	return out
}

// TraceSIP 记录一条 SIP 信令（自动脱敏），direction 如 "收到"/"发出"。
// 原始报文逐行输出（不做转义、不包在结构化字段中），格式：
//
//	2026-09-27 13:45:22.604 [SIP 收到]
//	SIP/2.0 200 OK
//	Via: SIP/2.0/UDP ...
//
// 信令记录开关（REQ-LOG-008）由 sip 包控制 tracer 注册；本函数写入
// 独立输出目标，与全局日志级别解耦。
func TraceSIP(direction, msg string) {
	mu.Lock()
	out := traceOut
	mu.Unlock()
	if out == nil {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	_, _ = fmt.Fprintf(out, "%s [SIP %s]\n%s\n\n", ts, direction, MaskSecrets(msg))
}

// ---------------------------------------------------------------------------
// 滚动文件（归档 gzip 压缩）
// ---------------------------------------------------------------------------

// rotatingFile 按大小滚动的日志文件：
//   - 超过 maxFileSize 时轮转：当前文件压缩为 .1.gz，旧归档依次后移（.1.gz→.2.gz），
//     删除最旧归档；保留当前文件 + maxFiles-1 份 gzip 归档（REQ-LOG-007）。
type rotatingFile struct {
	path string
	mu   sync.Mutex
	f    *os.File
	size int64
}

func newRotatingFile(dir string) (*rotatingFile, error) {
	if dir == "" {
		cd, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(cd, "mti-sip-phone", "logs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w := &rotatingFile{path: filepath.Join(dir, logFileName)}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFile) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f = f
	w.size = info.Size()
	return nil
}

// archiveName 第 i 份归档文件名（.1.gz 最新归档，.2.gz 次之）。
func archiveName(base string, i int) string {
	return fmt.Sprintf("%s.%d.gz", base, i)
}

// openTruncate 以清空方式重开当前日志文件（轮转的退路，见 rotate）。
func (w *rotatingFile) openTruncate() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	w.size = 0
	return nil
}

// rotate 轮转：把当前文件改名"切走"（改名即完成截断），压缩与归档序列
// 移位交给后台 goroutine —— 不持锁做重活。
//
// 为什么不能用"直接 gzip + 删除原文件"（历史缺陷，Windows 专有）：
//   - 删除/改名要求**所有**句柄都以 FILE_SHARE_DELETE 打开，而 Go 的
//     os.OpenFile 只给 FILE_SHARE_READ|FILE_SHARE_WRITE（syscall_windows.go
//     的 sharemode）。原实现 `return os.Remove(src)` 在 `defer in.Close()`
//     之前执行，自己的读句柄还没关 → 删除**必然失败** → 归档"压完却删不掉"：
//     文件不截断、尺寸一直大于上限 → **之后每次写日志都再轮转一次**（把整个
//     文件重新 gzip 一遍），且全程持着 w.mu。实测该缺陷让 SIP/界面/媒体线程
//     被卡住数百毫秒（原始日志里同一个 recvLoop 迭代内相邻两行日志相差
//     494ms），媒体开始时更是把 RTP 接收循环卡住——包在 socket 里堆积后成批
//     涌入、播放缓冲超深截断，"第一声回铃音断续"由此而来；日志本身也永远
//     轮不掉（实测停在 42MB 且每 10 秒重压一次）。
//   - 压缩大文件要几百毫秒到数秒，放在写路径里同样会卡住所有写日志的调用方。
//
// 退路：改名失败（例如日志被别的程序以无 FILE_SHARE_DELETE 的方式打开）时
// 退化为清空当前文件（O_TRUNC）；连清空都不行则按"不再轮转"降级——绝不回到
// "每次写都重新压缩整个文件"的恶性循环。
func (w *rotatingFile) rotate() error {
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			return err
		}
		w.f = nil
	}
	// 临时名带时间戳：连续轮转时各自的归档互不覆盖
	tmp := fmt.Sprintf("%s.%d.rotating", w.path, time.Now().UnixNano())
	if err := os.Rename(w.path, tmp); err != nil {
		if terr := w.openTruncate(); terr != nil {
			warnf("日志轮转失败，降级为持续追加（不再轮转）: 改名 %v / 清空 %v", err, terr)
			w.size = 0
			return w.open()
		}
		return nil
	}
	if err := w.open(); err != nil {
		return err
	}
	enqueueArchive(archiveJob{tmp: tmp, base: w.path})
	return nil
}

// archiveJob 一次归档任务：把 tmp 压缩为 <base>.1.gz，并把既有归档序列后移。
type archiveJob struct {
	tmp  string
	base string
}

var (
	archiveOnce sync.Once
	archiveCh   chan archiveJob
)

// enqueueArchive 投递归档任务（非阻塞）：队列满则丢弃该归档，不能拖住写日志。
func enqueueArchive(job archiveJob) {
	archiveOnce.Do(func() {
		archiveCh = make(chan archiveJob, 8)
		go archiveWorker()
	})
	select {
	case archiveCh <- job:
	default:
		warnf("日志归档队列已满，丢弃一次归档: %s", job.tmp)
		_ = os.Remove(job.tmp)
	}
}

// archiveWorker 串行归档（单独 goroutine）：保证 .1.gz 始终是最新一份。
func archiveWorker() {
	for job := range archiveCh {
		// 删除最旧归档
		_ = os.Remove(archiveName(job.base, maxFiles-1))
		// 旧归档依次后移（从旧到新）
		for i := maxFiles - 2; i >= 1; i-- {
			_ = os.Rename(archiveName(job.base, i), archiveName(job.base, i+1))
		}
		if err := gzipFile(job.tmp, archiveName(job.base, 1)); err != nil {
			warnf("日志归档失败: %v", err)
			_ = os.Remove(job.tmp)
		}
	}
}

// warnf 轮转/归档失败告警：直接写 stderr（此刻不能用 Logger——可能正持着
// 日志锁；GUI 子系统无控制台时也只是一次无害的写失败）。
func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "logx: "+format+"\n", args...)
}

// gzipFile 将 src 压缩为 dst.gz 并删除 src。
func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		in.Close()
		return err
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		in.Close()
		os.Remove(dst)
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		in.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		in.Close()
		os.Remove(dst)
		return err
	}
	// 先关自己的句柄再删（Windows 上句柄未关——且共享位不含 FILE_SHARE_DELETE
	// ——删除必然失败，此处顺序错了就是历史缺陷的根源）
	if err := in.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// Write 实现 io.Writer：写入前检查是否需要滚动。
func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size+int64(len(p)) > maxFileSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	if w.f == nil { // 轮转退路可能仍没拿到句柄（见 rotate）
		return 0, os.ErrClosed
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close 关闭文件。
func (w *rotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
