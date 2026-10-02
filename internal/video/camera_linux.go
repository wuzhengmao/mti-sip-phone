//go:build video && cgo && linux

package video

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"path/filepath"
	"sort"

	"github.com/blackjack/webcam"
)

// Linux 摄像头：V4L2，走 github.com/blackjack/webcam（纯 Go，无 cgo）。
//
// 像素格式策略：优先 YUYV（无需解码，直接转 I420）；设备不支持目标尺寸的
// YUYV 时退到 MJPEG（用标准库 image/jpeg 解码）。高分辨率下 YUYV 会吃满
// USB 带宽，MJPEG 更常见。

// V4L2 FourCC（小端打包：低字节是第一个字符）。
func v4l2FourCC(a, b, c, d byte) webcam.PixelFormat {
	return webcam.PixelFormat(uint32(a) | uint32(b)<<8 | uint32(c)<<16 | uint32(d)<<24)
}

var (
	fourccYUYV  = v4l2FourCC('Y', 'U', 'Y', 'V')
	fourccMJPEG = v4l2FourCC('M', 'J', 'P', 'G')
)

type linuxCamera struct {
	cam    *webcam.Webcam
	w, h   int
	fps    int
	mjpeg  bool
	closed bool
}

func listCameras() ([]CameraInfo, error) {
	paths, err := filepath.Glob("/dev/video*")
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]CameraInfo, 0, len(paths))
	for _, p := range paths {
		cam, err := webcam.Open(p)
		if err != nil {
			continue // 非采集设备（如 metadata 节点）或已被占用
		}
		name, nerr := cam.GetName()
		cam.Close()
		if nerr != nil || name == "" {
			name = p
		}
		out = append(out, CameraInfo{ID: p, Name: fmt.Sprintf("%s (%s)", name, p)})
	}
	return out, nil
}

func openCamera(id string, w, h, fps int) (Camera, error) {
	if id == "" {
		cams, err := listCameras()
		if err != nil || len(cams) == 0 {
			return nil, fmt.Errorf("未找到可用摄像头")
		}
		id = cams[0].ID
	}
	cam, err := webcam.Open(id)
	if err != nil {
		return nil, fmt.Errorf("打开摄像头 %s 失败（设备可能被其他程序占用）: %w", id, err)
	}

	useMJpeg := false
	pf, aw, ah, err := cam.SetImageFormat(fourccYUYV, uint32(w), uint32(h))
	if err != nil {
		// YUYV 不支持目标尺寸：退到 MJPEG
		pf2, aw2, ah2, err2 := cam.SetImageFormat(fourccMJPEG, uint32(w), uint32(h))
		if err2 != nil {
			cam.Close()
			return nil, fmt.Errorf("摄像头 %s 不支持 %dx%d 的 YUYV 或 MJPEG 格式", id, w, h)
		}
		pf, aw, ah = pf2, aw2, ah2
		useMJpeg = true
	} else if pf == fourccMJPEG {
		useMJpeg = true
	}

	_ = cam.SetFramerate(float32(fps))
	_ = cam.SetBufferCount(4)
	if err := cam.StartStreaming(); err != nil {
		cam.Close()
		return nil, fmt.Errorf("启动摄像头流失效: %w", err)
	}

	c := &linuxCamera{cam: cam, w: int(aw), h: int(ah), fps: fps, mjpeg: useMJpeg}
	return c, nil
}

func (c *linuxCamera) Format() (w, h, fps int) { return c.w, c.h, c.fps }

func (c *linuxCamera) ReadFrame(ctx context.Context) (*YUV420P, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// 超时用 200ms：既能及时响应 ctx 取消，又不会空转
		err := c.cam.WaitForFrame(200)
		if err != nil {
			if _, ok := err.(*webcam.Timeout); ok {
				continue
			}
			return nil, err
		}
		buf, err := c.cam.ReadFrame()
		if err != nil {
			return nil, err
		}
		if len(buf) == 0 {
			continue
		}
		if c.mjpeg {
			img, err := jpeg.Decode(bytes.NewReader(buf))
			if err != nil {
				continue // 丢掉坏帧
			}
			if yc, ok := img.(*image.YCbCr); ok {
				return ycbcrToI420(yc), nil
			}
			return rgbaToI420FromImage(img), nil
		}
		return yuyvToI420(buf, c.w, c.h), nil
	}
}

func (c *linuxCamera) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	_ = c.cam.StopStreaming()
	return c.cam.Close()
}
