//go:build video && cgo && windows

package video

import (
	"context"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/pion/mediadevices/pkg/driver"
	"github.com/pion/mediadevices/pkg/frame"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/mediadevices/pkg/prop"

	// 副作用导入：把 DirectShow 摄像头驱动注册进 driver 管理器。
	// 这是仓库内的一份实现（从 mediadevices 的 pkg/driver/camera 复制，
	// 补上了上游漏写的 STDMETHODCALLTYPE 调用约定）——**amd64 与 386 共用**。
	// 不能改回上游的 camera 包：它在 i686 上编不过，windows-386 会再次丢摄像头。
	_ "mti-sip-phone/internal/video/dshow"

	"mti-sip-phone/internal/logx"
)

// Windows 摄像头：DirectShow（cgo + C++，链接 mingw 自带的
// strmiids/ole32/oleaut32/quartz）。
//
// ⚠️ 上游 mediadevices 的 camera 驱动在 i686 上编不过（回调漏写
// STDMETHODCALLTYPE，与 qedit.h 的 __stdcall 冲突），所以本仓库内保留了一份
// 修好的副本（internal/video/dshow），两个架构都走它。
//
// ⚠️ 设备 ID 不稳定：mediadevices 的驱动 ID 由包装层生成，**每次进程启动都会变**。
// 所以配置里存的是设备**名称**，打开时按 ID → 名称逐级回退（PickCamera）。

// 包级串行化：DirectShow 同一设备不能并发打开，且上一路 Close 未完成时
// 立刻开下一路会失败。用一把锁把 Open/Close 串起来。
var cameraOpenMu sync.Mutex

type windowsCamera struct {
	drv      driver.Driver
	reader   video.Reader
	w, h     int
	fps      int
	closed   bool
	lastRead time.Time
}

// cameraSupported Windows（cgo）有 DirectShow 采集后端，amd64 / 386 皆可。
func cameraSupported() bool { return true }

func listCameras() ([]CameraInfo, error) {
	var out []CameraInfo
	for _, d := range driver.GetManager().Query(func(d driver.Driver) bool {
		return d.Info().DeviceType == driver.Camera
	}) {
		name := d.Info().Name
		if name == "" {
			name = d.Info().Label
		}
		if name == "" {
			name = d.ID()
		}
		out = append(out, CameraInfo{ID: d.ID(), Name: name})
	}
	return out, nil
}

func openCamera(id string, w, h, fps int) (Camera, error) {
	cameraOpenMu.Lock()
	defer cameraOpenMu.Unlock()

	drivers := driver.GetManager().Query(func(d driver.Driver) bool {
		return d.Info().DeviceType == driver.Camera
	})
	if len(drivers) == 0 {
		return nil, fmt.Errorf("未找到可用摄像头")
	}

	// 按 ID 精确匹配（ID 每次启动可能变，故失败时回退到第一个设备）
	drv := drivers[0]
	for _, d := range drivers {
		if d.ID() == id {
			drv = d
			break
		}
	}

	vr, ok := drv.(driver.VideoRecorder)
	if !ok {
		return nil, fmt.Errorf("摄像头驱动不支持视频采集")
	}

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
		reader, err := vr.VideoRecord(prop.Media{
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
