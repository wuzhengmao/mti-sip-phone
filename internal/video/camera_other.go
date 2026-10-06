//go:build video && cgo && !linux && !windows

package video

import "fmt"

// 不支持摄像头的平台组合（如 darwin 等）。
// 编译期保留视频编解码能力，但采集不可用——上层会因此在
// 「摄像头」下拉里看到禁用项、且不提供视频接听按钮。

// cameraSupported 这些平台组合没有采集后端（darwin 未实现）：
// 编解码可用，但不采集。Windows 的两种架构都有（camera_windows.go）。
func cameraSupported() bool { return false }

func listCameras() ([]CameraInfo, error) {
	return nil, fmt.Errorf("当前平台不支持摄像头采集: %w", ErrUnsupported)
}

func openCamera(string, int, int, int) (Camera, error) {
	return nil, fmt.Errorf("当前平台不支持摄像头采集: %w", ErrUnsupported)
}
