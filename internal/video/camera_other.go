//go:build video && cgo && !linux && !(windows && amd64)

package video

import "fmt"

// 不支持摄像头的平台组合（如 Windows 386、darwin 等）。
// 编译期保留视频编解码能力，但采集不可用——上层会因此在
// 「摄像头」下拉里看到禁用项、且不提供视频接听按钮。

func listCameras() ([]CameraInfo, error) {
	return nil, fmt.Errorf("当前平台不支持摄像头采集: %w", ErrUnsupported)
}

func openCamera(string, int, int, int) (Camera, error) {
	return nil, fmt.Errorf("当前平台不支持摄像头采集: %w", ErrUnsupported)
}
