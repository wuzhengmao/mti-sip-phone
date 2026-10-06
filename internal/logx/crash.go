package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// 崩溃现场记录：把 Go 运行期的致命输出落盘，解决「进程凭空消失、日志里
// 一句线索都没有」的排查困境。
//
// 为什么需要它：正式构建带 `-H=windowsgui`（Windows GUI 子系统，**没有控制台**），
// 而未捕获的 panic、运行期致命错误（并发 map 读写、栈溢出）、cgo 里的非法内存
// 访问，都由 Go 运行期打印到 **stderr** 后 `exit(2)`——GUI 子系统下 stderr 无处可去，
// 消息直接蒸发；Windows 事件日志也不会留下记录（那是进程**自己**正常退出的，
// 不是被异常终止，所以既没有 Application Error 也没有 WER 报告），于是表现为
// 「软件突然消失，日志停在半截」。
//
// `debug.SetCrashOutput`（Go 1.24+）会把这份输出**另写一份**到指定文件；
// 另外 Fyne/GLFW 这类库是直接往标准 stderr 写错误的，进程没有控制台时这里
// 也把 os.Stderr 接到文件上（有控制台时不动，免得把控制台输出抢走）。
const (
	// crashFileName 崩溃现场文件名（与主日志同目录）。
	crashFileName = "crash.log"
	// stderrFileName 无控制台时 stderr 的兜底去处。
	stderrFileName = "stderr.log"
	// stderrMaxSize 兜底文件超过它就在下次启动时清空（只收错误文本，正常长不大）。
	stderrMaxSize = 1 << 20
	// crashHeader 启动标题行前缀，用来认出「这行不是崩溃内容」。
	crashHeader = "=== "
	// crashMaxFiles 崩溃归档最多保留几份（含当前，与日志归档口径一致）。
	crashMaxFiles = 10
)

// InstallCrashHandler 安装崩溃现场记录，返回崩溃记录文件路径。
//
// banner 是一行「本进程是谁」的说明（版本/平台/实例槽位等），写在记录文件
// 开头——一个文件里的标题行代表一次运行，对不上号时能立刻分辨是哪一次。
//
// 若上次运行留下的记录里**有崩溃内容**（不只是标题行），会先归档为
// crash-<时间>.log 并把路径作为 archived 返回（调用方据此在日志里提示用户）；
// 只有标题行（上次正常退出）则直接清空重来。
//
// 失败不致命：调用方降级为「崩溃时没有现场可查」即可，不要阻断启动。
func InstallCrashHandler(dir, banner string) (path, archived string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("创建日志目录失败: %w", err)
	}
	path = filepath.Join(dir, crashFileName)
	archived, keepOld := archivePreviousCrash(path, dir)

	flags := os.O_CREATE | os.O_WRONLY
	if keepOld {
		flags |= os.O_APPEND // 归档失败（文件被占用）：留着旧内容继续追加
	} else {
		flags |= os.O_TRUNC // 上次正常退出（或已归档）：重新开始记
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return "", archived, fmt.Errorf("打开崩溃记录文件失败: %w", err)
	}
	// SetCrashOutput 内部会 dup 这份 fd（dup 出来的那个归运行期管、进程退出才关），
	// 所以写完标题我们就可以放掉自己的句柄——否则文件会一直被占着（Windows 上
	// 连删除都做不到）。
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		_ = f.Close()
		return "", archived, fmt.Errorf("安装崩溃输出失败: %w", err)
	}
	_, werr := fmt.Fprintf(f, "%s%s %s ===\n", crashHeader,
		time.Now().Format("2006-01-02 15:04:05"), banner)
	_ = f.Close()
	if werr != nil {
		return path, archived, fmt.Errorf("写入崩溃记录标题失败: %w", werr)
	}
	redirectStderr(dir)
	return path, archived, nil
}

// archivePreviousCrash 归档上次运行的崩溃记录。
// 返回归档文件路径（无需归档或归档失败时为空），以及是否保留原文件内容
// （归档失败时保留：宁可在同一个文件里接着记，也不能把现场删掉）。
func archivePreviousCrash(path, dir string) (archived string, keepOld bool) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		return "", true // 读不出来（占用等）：别动它
	}
	if !hasCrashContent(data) {
		return "", false // 只有标题行 = 上次正常退出，清空重来
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", true
	}
	archived = filepath.Join(dir, "crash-"+fi.ModTime().Format("20060102-150405")+".log")
	if err := os.Rename(path, archived); err != nil {
		return "", true
	}
	pruneCrashArchives(dir)
	return archived, false
}

// hasCrashContent 记录里除了标题行与空行之外还有没有别的内容。
func hasCrashContent(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, crashHeader) {
			continue
		}
		return true
	}
	return false
}

// pruneCrashArchives 只保留最新的 crashMaxFiles-1 份归档（当前文件另外算）。
func pruneCrashArchives(dir string) {
	files, err := filepath.Glob(filepath.Join(dir, "crash-*.log"))
	if err != nil || len(files) <= crashMaxFiles-1 {
		return
	}
	sort.Strings(files) // 文件名含时间戳，字典序即时间序
	for _, f := range files[:len(files)-(crashMaxFiles-1)] {
		_ = os.Remove(f)
	}
}

// redirectStderr stderr **没有去处**时（GUI 子系统从资源管理器启动那种）把它
// 接到文件上；有控制台、被重定向到管道/文件的一律不动——那是别人要的输出。
//
// 只接 Go 侧的标准错误流；Go 运行期的崩溃输出由 SetCrashOutput 直接写 fd，
// 不经过 os.Stderr，两者互不重复。
func redirectStderr(dir string) {
	if stderrUsable() {
		return
	}
	if stderrFile != nil {
		_ = stderrFile.Close() // 重复安装（测试）时先放掉上一次的句柄
		stderrFile = nil
	}
	target := filepath.Join(dir, stderrFileName)
	if fi, err := os.Stat(target); err == nil && fi.Size() > stderrMaxSize {
		_ = os.Truncate(target, 0) // 只收错误文本，正常长不大；异常膨胀就清掉重来
	}
	sf, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return // 接不上就算了，崩溃现场记录仍然有效
	}
	os.Stderr = sf
	stderrFile = sf
}

// stderrFile 我们接过去的 stderr 文件（进程存活期间保持打开；重复安装时先关旧的）。
var stderrFile *os.File
