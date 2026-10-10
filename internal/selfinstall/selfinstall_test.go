package selfinstall

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// setTargetBase 把固定数据目录的基目录指到临时目录：
// Windows 的 TargetDir 走 os.UserConfigDir（APPDATA 环境变量），
// 其它平台走 XDG_DATA_HOME。返回该基目录（目标目录 = 基目录/mti-sip-phone）。
func setTargetBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", base)
	} else {
		t.Setenv("XDG_DATA_HOME", base)
	}
	return base
}

// writeFakeExe 写一个假的"可执行文件"（内容唯一，用于比对复制结果）。
func writeFakeExe(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("fake-exe:"+name), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// noShortcut 测试期关闭桌面快捷方式生成（不往真实桌面写东西）。
func noShortcut(t *testing.T) {
	t.Helper()
	old := createShortcut
	createShortcut = func(string) error { return nil }
	t.Cleanup(func() { createShortcut = old })
}

// fakeStart 测试期拦截进程启动；返回指针，启动发生时指向要启动的命令。
// （闭包里给命名返回值赋值只更新旧栈帧，调用方拿不到，必须经指针。）
func fakeStart(t *testing.T) **exec.Cmd {
	t.Helper()
	old := execStart
	var started *exec.Cmd
	execStart = func(cmd *exec.Cmd) error { started = cmd; return nil }
	t.Cleanup(func() { execStart = old })
	return &started
}

// setTempDir 把"系统临时目录"重定向到 dir（测试自身的临时目录就在系统临时
// 目录下，不重定向的话复制路径永远被跳过规则挡住）。
func setTempDir(t *testing.T, dir string) {
	t.Helper()
	old := tempDir
	tempDir = func() string { return dir }
	t.Cleanup(func() { tempDir = old })
}

// newSrcDir 建一个"不在系统临时目录下"的源目录（重定向后系统临时目录
// 是另一个目录，t.TempDir() 的子目录自然不在其内）。
func newSrcDir(t *testing.T) string {
	setTempDir(t, filepath.Join(t.TempDir(), "fake-temp"))
	return t.TempDir()
}

// TestRelocateSkipsWhenEnvSet 逃生口：SIP_PHONE_NO_RELOCATE 非空 → 原地运行。
func TestRelocateSkipsWhenEnvSet(t *testing.T) {
	setTargetBase(t)
	t.Setenv("SIP_PHONE_NO_RELOCATE", "1")
	exe := writeFakeExe(t, t.TempDir(), "mti-sip-phone.exe")

	relocated, _, err := Relocate(exe, nil)
	if err != nil || relocated {
		t.Fatalf("逃生口应跳过复制：relocated=%v err=%v", relocated, err)
	}
}

// TestRelocateSkipsTempDir go run 的临时 exe（系统临时目录下）不复制。
func TestRelocateSkipsTempDir(t *testing.T) {
	setTargetBase(t)
	srcDir := t.TempDir()
	setTempDir(t, srcDir) // 让 src 恰好"位于系统临时目录下"
	exe := writeFakeExe(t, srcDir, "mti-sip-phone.exe")

	relocated, _, err := Relocate(exe, nil)
	if err != nil || relocated {
		t.Fatalf("临时目录下的 exe 不应复制：relocated=%v err=%v", relocated, err)
	}
}

// TestRelocateSkipsWhenAlreadyInTargetDir 已在固定数据目录 → 无事可做。
func TestRelocateSkipsWhenAlreadyInTargetDir(t *testing.T) {
	base := setTargetBase(t)
	// 重定向临时目录，确保跳过理由不是"临时目录规则"而是"已在固定目录"
	setTempDir(t, filepath.Join(t.TempDir(), "fake-temp"))
	target := filepath.Join(base, "mti-sip-phone")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := writeFakeExe(t, target, "mti-sip-phone.exe")

	relocated, _, err := Relocate(exe, nil)
	if err != nil || relocated {
		t.Fatalf("已在固定目录不应复制：relocated=%v err=%v", relocated, err)
	}
}

// TestRelocateCopiesStartsShortcut 正常路径：复制到固定目录（保留原名、
// 内容一致）、生成快捷方式、按原参数拉起新进程，临时文件被清理。
func TestRelocateCopiesStartsShortcut(t *testing.T) {
	base := setTargetBase(t)
	noShortcut(t)
	started := fakeStart(t)
	src := writeFakeExe(t, newSrcDir(t), "mti-sip-phone.exe")
	args := []string{"sip://1001:pwd@10.0.0.1", "-instance", "2"}

	relocated, dst, err := Relocate(src, args)
	if err != nil || !relocated {
		t.Fatalf("应复制并启动：relocated=%v err=%v", relocated, err)
	}
	want := filepath.Join(base, "mti-sip-phone", "mti-sip-phone.exe")
	if dst != want {
		t.Fatalf("目标应为 %q，实际 %q", want, dst)
	}
	srcData, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dstData, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("固定目录副本不存在: %v", err)
	}
	if !reflect.DeepEqual(dstData, srcData) {
		t.Fatalf("副本内容与源不一致：%q vs %q", dstData, srcData)
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("临时文件应被清理：%v", err)
	}
	if *started == nil {
		t.Fatal("应启动新进程")
	}
	wantArgs := append([]string{dst}, args...)
	if !reflect.DeepEqual((*started).Args, wantArgs) {
		t.Fatalf("启动参数应为 %q，实际 %q", wantArgs, (*started).Args)
	}
}

// TestRelocateOverwritesExistingCopy 目标位置已有同名文件（非运行中）→ 覆盖。
func TestRelocateOverwritesExistingCopy(t *testing.T) {
	base := setTargetBase(t)
	noShortcut(t)
	fakeStart(t)
	src := writeFakeExe(t, newSrcDir(t), "mti-sip-phone.exe")
	target := filepath.Join(base, "mti-sip-phone")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(target, "mti-sip-phone.exe")
	if err := os.WriteFile(old, []byte("old-version"), 0o755); err != nil {
		t.Fatal(err)
	}

	relocated, _, err := Relocate(src, nil)
	if err != nil || !relocated {
		t.Fatalf("应覆盖旧副本并启动：relocated=%v err=%v", relocated, err)
	}
	data, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake-exe:mti-sip-phone.exe" {
		t.Fatalf("旧副本应被新内容覆盖，实际 %q", data)
	}
}

// TestSameDir 目录比较：Windows 大小写不敏感，其余平台敏感。
func TestSameDir(t *testing.T) {
	if !sameDir("/a/b", "/a/b") {
		t.Fatal("相同目录应判等")
	}
	if sameDir("/a/b", "/a/c") {
		t.Fatal("不同目录不应判等")
	}
	if runtime.GOOS == "windows" {
		if !sameDir(`C:\AppData\Mti-Sip-Phone`, `c:\appdata\mti-sip-phone`) {
			t.Fatal("Windows 下大小写不同应判等")
		}
	}
}

// TestUnderDir 路径归属判断。
func TestUnderDir(t *testing.T) {
	if !underDir("/tmp/a/b", "/tmp") {
		t.Fatal("子路径应判在目录内")
	}
	if !underDir("/tmp", "/tmp") {
		t.Fatal("相等应判在目录内")
	}
	if underDir("/tmp2/a", "/tmp") {
		t.Fatal("同前缀不同目录不应判在目录内")
	}
}
