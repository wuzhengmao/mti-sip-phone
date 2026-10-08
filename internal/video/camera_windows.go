//go:build video && cgo && windows

package video

import (
	"context"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/pion/mediadevices/pkg/frame"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/mediadevices/pkg/prop"

	"mti-sip-phone/internal/logx"
	// DirectShow 摄像头驱动：仓库内的一份实现（从 mediadevices 的
	// pkg/driver/camera 复制，补上了上游漏写的 STDMETHODCALLTYPE 调用约定）
	// ——**amd64 与 386 共用**。不能改回上游的 camera 包：它在 i686 上编不过，
	// windows-386 会再次丢摄像头。枚举与打开走 dshow.List / dshow.NewDevice
	//（每次实时枚举，不经过 mediadevices 管理器的一次性注册缓存）。
	"mti-sip-phone/internal/video/dshow"
)

// Windows 摄像头：DirectShow（cgo + C++，链接 mingw 自带的
// strmiids/ole32/oleaut32/quartz）。
//
// ⚠️ 上游 mediadevices 的 camera 驱动在 i686 上编不过（回调漏写
// STDMETHODCALLTYPE，与 qedit.h 的 __stdcall 冲突），所以本仓库内保留了一份
// 修好的副本（internal/video/dshow），两个架构都走它。
//
// 枚举与打开都走 dshow.List / dshow.NewDevice——**每次实时枚举**，应用运行
// 期间摄像头关闭/拔出/插入都会反映到设备列表（不走 mediadevices 管理器
// 的一次性注册缓存）。设备 ID 现在是 DirectShow 设备路径（稳定标识）；
// 配置里仍存设备**名称**，打开时按 ID → 名称逐级回退（PickCamera）。

// 包级串行化：DirectShow 同一设备不能并发打开，且上一路 Close 未完成时
// 立刻开下一路会失败。用一把锁把 Open/Close 串起来。
var cameraOpenMu sync.Mutex

type windowsCamera struct {
	drv      dshow.Device
	reader   video.Reader
	w, h     int
	fps      int
	closed   bool
	lastRead time.Time
}

// cameraSupported Windows（cgo）有 DirectShow 采集后端，amd64 / 386 皆可。
func cameraSupported() bool { return true }

func listCameras() ([]CameraInfo, error) {
	list, err := dshow.List()
	if err != nil {
		return nil, err
	}
	out := make([]CameraInfo, 0, len(list))
	for _, d := range list {
		out = append(out, CameraInfo{ID: d.ID, Name: d.Name})
	}
	return out, nil
}

func openCamera(id string, w, h, fps int) (Camera, error) {
	cameraOpenMu.Lock()
	defer cameraOpenMu.Unlock()

	// 每次实时枚举：配置里存的旧 ID（历史随机 UUID）或名称都可能对不上，
	// 按「ID（设备路径）精确匹配 → 名称匹配 → 第一个设备」回退。
	list, err := dshow.List()
	if err != nil {
		return nil, fmt.Errorf("枚举摄像头失败: %w", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("未找到可用摄像头")
	}
	target := list[0].ID
	for _, d := range list {
		if d.ID == id || d.Name == id {
			target = d.ID
			break
		}
	}
	drv := dshow.NewDevice(target)

	// 依次尝试像素格式：NV12（原生 4:2:0，无需色度重采样）→ YUY2（兼容性最好）。
	//
	// ⚠️ 必须逐个试：DirectShow 侧用 ISampleGrabber::SetMediaType 固定格式，
	// 摄像头不支持该格式时**整个采集链路直接失败**。很多 USB 摄像头只提供
	// YUY2/MJPEG，固定用 NV12 会一台都打不开——表现为「本地回显和发送都没有，
	// 但远端视频正常」（接收链路与摄像头无关）。
	var lastErr error
	for _, ff := range []frame.Format{frame.FormatNV12, frame.FormatYUY2} {
		if err := drv.Open(); err != nil {
			lastErr = fmt.Errorf("打开设备失败（可能被其他程序占用）: %w", err)
			continue
		}
		reader, err := drv.VideoRecord(prop.Media{
			Video: prop.Video{
				Width:       w,
				Height:      h,
				FrameRate:   float32(fps),
				FrameFormat: ff,
			},
		})
		if err != nil {
			lastErr = fmt.Errorf("以 %s 格式启动采集失败: %w", ff, err)
			_ = drv.Close()
			continue
		}
		logx.Logger().Info("摄像头已打开", "id", id, "格式", string(ff), "请求", fmt.Sprintf("%dx%d@%d", w, h, fps))
		return &windowsCamera{drv: drv, reader: reader, w: w, h: h, fps: fps}, nil
	}
	return nil, fmt.Errorf("启动摄像头采集失败（已依次尝试 NV12 / YUY2）: %w", lastErr)
}

func (c *windowsCamera) Format() (w, h, fps int) { return c.w, c.h, c.fps }

func (c *windowsCamera) ReadFrame(ctx context.Context) (*YUV420P, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	img, release, err := c.reader.Read()
	if err != nil {
		// DirectShow 在设备拔出等情况下会持续报错，避免忙等
		time.Sleep(50 * time.Millisecond)
		return nil, err
	}
	if release != nil {
		defer release()
	}
	if yc, ok := img.(*image.YCbCr); ok {
		return ycbcrToI420(yc), nil
	}
	if rgba, ok := img.(*image.RGBA); ok {
		b := rgba.Bounds()
		return rgbaToI420(rgba.Pix[rgba.PixOffset(b.Min.X, b.Min.Y):], rgba.Stride, b.Dx(), b.Dy()), nil
	}
	return rgbaToI420FromImage(img), nil
}

func (c *windowsCamera) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	cameraOpenMu.Lock()
	defer cameraOpenMu.Unlock()
	return c.drv.Close()
}
