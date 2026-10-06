package logx

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// installForTest 安装崩溃记录并做好测试期的清理。
//
// ⚠️ 两次安装之间必须放掉上一次的崩溃 fd：debug.SetCrashOutput 内部 dup 出来的
// 句柄归运行期管（Go 的 os.OpenFile 不带 FILE_SHARE_DELETE），**同一进程内**它
// 一直开着，Windows 下归档要用的改名就会失败——而真实场景（上次的进程已经死了）
// 不存在这个问题，所以测试里显式释放。
func installForTest(t *testing.T, dir, banner string) (string, string) {
	t.Helper()
	debug.SetCrashOutput(nil, debug.CrashOptions{})
	t.Cleanup(func() { debug.SetCrashOutput(nil, debug.CrashOptions{}) })
	path, archived, err := InstallCrashHandler(dir, banner)
	if err != nil {
		t.Fatalf("安装崩溃记录失败: %v", err)
	}
	return path, archived
}

// restoreStderr 收尾：还原 stderr 并关掉可能被接过去的文件句柄（没有控制台
// 的环境下会真的重定向，句柄不关临时目录删不掉）。
func restoreStderr(t *testing.T) {
	t.Helper()
	old := os.Stderr
	t.Cleanup(func() {
		if os.Stderr != old {
			_ = os.Stderr.Close()
		}
		os.Stderr = old
	})
}

// TestCrashHandlerArchivesOnlyRealCrash 只有「除了标题行还有别的内容」才归档：
// 上次正常退出（文件里只有标题行）时不能报"异常退出"，否则每次启动都会误报。
func TestCrashHandlerArchivesOnlyRealCrash(t *testing.T) {
	dir := t.TempDir()
	restoreStderr(t) // 注册在 TempDir 之后：清理时先还原 stderr、再删目录
	crashPath := filepath.Join(dir, crashFileName)

	// 情形一：上次正常退出，只留了标题行 → 不归档，直接清空重来
	old := crashHeader + "2026-10-06 10:00:00 mti-sip-phone 1.0.13 pid=1 实例=0 ===\n"
	if err := os.WriteFile(crashPath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	path, archived := installForTest(t, dir, "第一轮")
	if archived != "" {
		t.Errorf("只有标题行不应归档，却归档到了 %s", archived)
	}
	if got, _ := os.ReadFile(path); strings.Contains(string(got), old) {
		t.Error("上一次的标题行应被清空重来")
	}

	// 情形二：上次留下了崩溃内容 → 归档，且内容原样保留
	body := "panic: 测试用的假崩溃\n\ngoroutine 1 [running]:\nmain.main()\n"
	if err := os.WriteFile(crashPath, []byte(crashHeader+"上一轮 ===\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	path, archived = installForTest(t, dir, "第二轮")
	if archived == "" {
		t.Fatal("有崩溃内容却没有归档")
	}
	got, err := os.ReadFile(archived)
	if err != nil {
		t.Fatalf("读取归档失败: %v", err)
	}
	if !strings.Contains(string(got), "panic: 测试用的假崩溃") {
		t.Errorf("归档内容不完整: %q", got)
	}
	// 本轮的文件是全新的：只有第二轮自己的标题行
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取本轮记录失败: %v", err)
	}
	if strings.Contains(string(cur), "panic: 测试用的假崩溃") {
		t.Error("归档后本轮文件应重新开始，不该带上一轮的内容")
	}
	if !strings.Contains(string(cur), "第二轮") {
		t.Errorf("本轮标题行应已写入: %q", cur)
	}
}

// TestCrashHandlerCapturesRealPanic 端到端验证：**真的 panic 一次**，现场必须
// 落在 crash.log 里（这条是整套机制的地基——`debug.SetCrashOutput` 没生效时，
// 前面的测试照样全绿，而真机上崩溃日志依旧是空的）。
func TestCrashHandlerCapturesRealPanic(t *testing.T) {
	if os.Getenv("LOGX_CRASH_CHILD") == "1" {
		// 子进程：装上崩溃记录后直接 panic（未捕获 → 进程以 exit 2 结束）
		if _, _, err := InstallCrashHandler(os.Getenv("LOGX_CRASH_DIR"), "子进程"); err != nil {
			os.Exit(3)
		}
		panic("测试用的假崩溃")
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestCrashHandlerCapturesRealPanic")
	cmd.Env = append(os.Environ(), "LOGX_CRASH_CHILD=1", "LOGX_CRASH_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("子进程应当 panic 退出，却正常结束了：%s", out)
	}
	data, rerr := os.ReadFile(filepath.Join(dir, crashFileName))
	if rerr != nil {
		t.Fatalf("读取崩溃记录失败: %v", rerr)
	}
	got := string(data)
	if !strings.Contains(got, "测试用的假崩溃") {
		t.Errorf("崩溃记录里没有 panic 内容: %q", got)
	}
	// 栈里应当能看到 goroutine 转储（排查"崩在哪"靠的就是它）
	if !strings.Contains(got, "goroutine") {
		t.Errorf("崩溃记录里没有 goroutine 栈: %q", got)
	}
}

// TestCrashHandlerKeepsArchiveLimit 归档份数有上限（崩溃循环不会把日志目录塞满）。
func TestCrashHandlerKeepsArchiveLimit(t *testing.T) {
	dir := t.TempDir()
	restoreStderr(t) // 注册在 TempDir 之后：清理时先还原 stderr、再删目录
	crashPath := filepath.Join(dir, crashFileName)
	for i := 0; i < crashMaxFiles+3; i++ {
		if err := os.WriteFile(crashPath, []byte("panic: 又崩了\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		installForTest(t, dir, "崩溃循环")
	}
	files, err := filepath.Glob(filepath.Join(dir, "crash-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) > crashMaxFiles-1 {
		t.Errorf("归档份数 %d 超过上限 %d", len(files), crashMaxFiles-1)
	}
}
