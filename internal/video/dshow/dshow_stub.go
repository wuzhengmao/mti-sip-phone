//go:build !windows || !cgo

// 非 Windows 或无 cgo 的构建没有 DirectShow 摄像头后端。
// 实现在 dshow_windows.go（Windows + cgo），来源与改动见 dshow_windows.hpp 头部注释。
package dshow
