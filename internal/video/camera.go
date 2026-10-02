package video

import "context"

// CameraInfo 一个摄像头设备。
//
// 注意 ID 的不稳定性：Windows 侧（pion/mediadevices）的驱动 ID 由包装层
// 生成，**每次进程启动都可能变化**。配置里持久化的是设备**名称**，
// 打开时按「ID 精确匹配 → 名称精确匹配 → 默认设备」逐级回退（见 PickCamera）。
type CameraInfo struct {
	ID   string
	Name string
}

// Camera 已打开的摄像头（单 goroutine 使用）。
type Camera interface {
	// Format 实际生效的分辨率与帧率（可能不等于请求值，设备会就近选择）。
	Format() (w, h, fps int)
	// ReadFrame 阻塞读取一帧并转换为 I420。ctx 取消或读超时返回错误。
	ReadFrame(ctx context.Context) (*YUV420P, error)
	Close() error
}

// ListCameras 枚举摄像头。可能耗时（V4L2 逐个探测 / DirectShow 枚举），
// 调用方必须放进后台 goroutine（与 audio.Manager.Devices 的约定一致）。
func ListCameras() ([]CameraInfo, error) { return listCameras() }

// OpenCamera 打开指定摄像头；id 为空时取第一个可用设备。
func OpenCamera(id string, w, h, fps int) (Camera, error) {
	return openCamera(id, w, h, fps)
}

// PickCamera 按 ID → 名称的顺序在枚举结果中挑选设备。
// 返回空字符串表示用系统默认设备（第一个）。
// 设备 ID 不稳定（每次启动可能变），所以名称是更可靠的持久化标识。
func PickCamera(cams []CameraInfo, wantID, wantName string) (id string) {
	if wantID != "" {
		for _, c := range cams {
			if c.ID == wantID {
				return c.ID
			}
		}
	}
	if wantName != "" {
		for _, c := range cams {
			if c.Name == wantName {
				return c.ID
			}
		}
	}
	return ""
}
