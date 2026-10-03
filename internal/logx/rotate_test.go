package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 轮转回归测试：文件超过上限后必须**被切断并继续轮转**。
//
// 历史缺陷（Windows 专有）：gzipFile 里 `return os.Remove(src)` 在
// `defer in.Close()` 之前执行，自己的读句柄未关就删文件必然失败（Go 的
// os.OpenFile 不带 FILE_SHARE_DELETE）——归档"压完却删不掉"，文件尺寸一直
// 大于上限，于是**每次写日志都再轮转一次**：把整个（越来越大的）文件重新
// gzip 一遍并持着写锁，SIP/界面/媒体线程全被卡住（实测 494ms 级停顿）。
// 本测试直接盯住这个症状：连续写入远超上限的数据后，当前文件必须始终
// 小于上限，归档数必须有限。
func TestRotateTruncatesCurrentFile(t *testing.T) {
	dir := t.TempDir()
	w, err := newRotatingFile(dir)
	if err != nil {
		t.Fatalf("创建日志文件失败: %v", err)
	}
	defer w.Close()

	chunk := make([]byte, 256*1024)
	for i := range chunk {
		chunk[i] = 'x'
	}

	// 写入约 25MB（上限 10MB）：正常实现会产生 2~3 份归档、当前文件回到
	// 上限以下；缺陷实现则让当前文件涨到 25MB 以上，且每次写都重压一遍。
	written := 0
	for written < 25*1024*1024 {
		n, err := w.Write(chunk)
		if err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		written += n
	}

	// 归档是后台 goroutine 做的：轮询等它把临时文件压缩完（超时即失败）
	countLogFiles := func() (gz, rotating int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return 0, 0
		}
		for _, e := range entries {
			name := e.Name()
			switch {
			case strings.HasSuffix(name, ".gz"):
				gz++
			case strings.HasSuffix(name, ".rotating"):
				rotating++
			}
		}
		return gz, rotating
	}
	deadline := time.Now().Add(20 * time.Second)
	gz, rotating := 0, 0
	for time.Now().Before(deadline) {
		gz, rotating = countLogFiles()
		if gz > 0 && rotating == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	info, err := os.Stat(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("当前日志文件不存在: %v", err)
	}
	if info.Size() >= maxFileSize {
		t.Fatalf("轮转没有切断当前文件：size=%d ≥ 上限 %d（每次写都会重压整个文件）", info.Size(), maxFileSize)
	}

	// 归档数有限（最多 maxFiles-1 份 .gz，且不应残留 .rotating 临时文件）
	if gz == 0 {
		t.Error("没有生成任何归档")
	}
	if gz > maxFiles-1 {
		t.Errorf("归档数超出上限：%d > %d", gz, maxFiles-1)
	}
	if rotating != 0 {
		t.Errorf("残留未归档的临时文件 %d 个（归档失败没清理）", rotating)
	}
}

// 改名被占用时的退路：应清空当前文件（而不是永远超限、每次写都重压）。
// 用把目录里的当前文件额外打开一个句柄来模拟"别的程序占着日志"——
// 但 Go 自己的句柄同样阻止改名，所以这里恰好能测退路分支。
func TestRotateFallbackTruncates(t *testing.T) {
	dir := t.TempDir()
	w, err := newRotatingFile(dir)
	if err != nil {
		t.Fatalf("创建日志文件失败: %v", err)
	}
	defer w.Close()

	path := filepath.Join(dir, logFileName)
	held, err := os.Open(path) // 额外句柄：改名会失败
	if err != nil {
		t.Fatalf("打开日志文件失败: %v", err)
	}
	defer held.Close()

	w.mu.Lock()
	w.size = maxFileSize // 假装已经写满
	w.mu.Unlock()

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	w.mu.Lock()
	size := w.size
	w.mu.Unlock()
	if size >= maxFileSize {
		t.Fatalf("退路没有把尺寸压回上限以下：size=%d", size)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("当前日志文件不存在: %v", err)
	}
	if fi.Size() >= maxFileSize {
		t.Fatalf("退路没有清空当前文件：size=%d", fi.Size())
	}
}
