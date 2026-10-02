//go:build video && cgo && windows && amd64

package video

import (
	"context"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/pion/mediadevices/pkg/driver"
	_ "github.com/pion/mediadevices/pkg/driver/camera" // 副作用注册 DirectShow 摄像头驱动
	"github.com/pion/mediadevices/pkg/frame"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/mediadevices/pkg/prop"

	"mti-sip-phone/internal/logx"
)

// Windows 摄像头：DirectShow，走 github.com/pion/mediadevices 的 camera 驱动
// （cgo + C++，链接 mingw 自带的 strmiids/ole32/oleaut32/quartz）。
//
// ⚠️ **仅限 amd64，这是上游的编译期限制，不是我们偷懒**：mediadevices 的
// camera_windows.hpp 声明的回调 `HRESULT SampleCB(...)` 漏了 STDMETHODCALLTYPE，
// 而 mingw 的 qedit.h 里 `ISampleGrabberCB::SampleCB` 带 `__stdcall`。
// x86_64 上 __stdcall 被忽略所以能编；**i686 上是真实的调用约定冲突**，报
// "conflicting type attributes specified for virtual HRESULT ..."。
// 想强行编（如 -D__stdcall=）会让整份 COM 调用变成 cdecl，运行期必崩，不要试。
// 因此 Windows 386 目标：视频编解码可用，但摄像头不可用（走 camera_other.go）。
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
