package core

import (
	"os"
	"runtime"
	"strings"
)

// Version 应用版本号（构建时可通过 -ldflags 覆盖）。
var Version = "1.0.5"

// BuildTime 构建时间，由构建脚本通过 -ldflags -X 注入，格式 2006-01-02T15:04:05
// （用 T 分隔避免 ldflags 参数里出现空格）；手动 go build / go run 未注入时为空，
// 展示时回退为可执行文件修改时间。
var BuildTime = ""

// BuildTimeText 返回展示用构建时间（未注入时取可执行文件修改时间，再失败返回「未知」）。
func BuildTimeText() string {
	if BuildTime != "" {
		return strings.Replace(BuildTime, "T", " ", 1)
	}
	if exe, err := os.Executable(); err == nil {
		if fi, err := os.Stat(exe); err == nil {
			return fi.ModTime().Format("2006-01-02 15:04:05")
		}
	}
	return "未知"
}

// Platform 返回编译目标平台与架构，如 windows/amd64。
func Platform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
