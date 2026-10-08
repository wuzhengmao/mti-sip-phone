//go:build windows && cgo

// Windows 摄像头采集：DirectShow（cgo + C++）。
//
// 本文件与同目录的 dshow_windows.{cpp,hpp} 复制自 github.com/pion/mediadevices
// v0.10.0 的 pkg/driver/camera（MIT License, Copyright (c) 2018-2020 Pion），
// 差异只有一处：C++ 侧给 SampleCB / BufferCB 补上 STDMETHODCALLTYPE。
// 上游漏写该调用约定标记，mingw 的 qedit.h 里 ISampleGrabberCB 的回调带
// __stdcall —— x86_64 上被忽略（所以 amd64 一直正常），i686 上是真实冲突、
// 编不过（windows-386 曾因此没有摄像头）。补上后 amd64 与 386 共用这一份实现。
//
// **不再注册到 mediadevices 的 driver 管理器**：管理器把首次发现的设备
// 永久缓存、没有注销 API，应用运行期间摄像头关闭/拔出/插入都反映不到列表里
//（真机缺陷：笔记本摄像头关闭后刷新设备，两边界面都还显示它）。现在由
// `List` 每次实时枚举、`NewDevice` 按设备路径直接构造可打开的驱动，完全
// 绕开管理器——依赖 mediadevices 的 frame/io/prop 包，不依赖它的
// driver/availability 包。
package dshow

// #cgo LDFLAGS: -lstrmiids -lole32 -loleaut32 -lquartz
// #include <dshow.h>
// #include "dshow_windows.hpp"
import "C"

import (
	"fmt"
	"image"
	"io"
	"sync"
	"unsafe"

	"github.com/pion/mediadevices/pkg/frame"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/mediadevices/pkg/prop"
)

var (
	callbacks   = make(map[uintptr]*camera)
	callbacksMu sync.RWMutex
)

type camera struct {
	name string
	// mu protects the fields under as per the mutex hat convention.
	mu     sync.Mutex
	cam    *C.camera
	closed bool
	ch     chan []byte
	done   chan struct{}

	cbuf   unsafe.Pointer // C.malloc'd buffer for DirectShow writes
	bufLen int            // byte length of cbuf
	bufGo  []byte
}

// DeviceInfo 一次实时枚举得到的摄像头。
type DeviceInfo struct {
	// ID 是 DirectShow 的设备路径（getName 返回的 label）——**稳定标识**，
	// 跨进程不变，NewDevice 用它直接打开设备。
	ID string
	// Name 是友好名（getFriendlyName），展示与配置持久化用。
	Name string
}

// Device 一个可打开的摄像头（满足 Open/Close/VideoRecord）。
type Device interface {
	Open() error
	Close() error
	VideoRecord(prop.Media) (video.Reader, error)
}

// List 实时枚举 DirectShow 摄像头：**每次调用都重新枚举**，设备在应用运行
// 期间关闭/拔出/插入都能立即反映（不走 mediadevices 管理器的一次性注册缓存）。
func List() ([]DeviceInfo, error) {
	C.CoInitializeEx(nil, C.COINIT_MULTITHREADED)

	var list C.cameraList
	var errStr *C.char
	if C.listCamera(&list, &errStr) != 0 {
		return nil, fmt.Errorf("failed to list camera: %s", C.GoString(errStr))
	}
	defer C.freeCameraList(&list, &errStr)

	out := []DeviceInfo{}
	for i := 0; i < int(list.num); i++ {
		var id, name string
		if n := C.getName(&list, C.int(i)); n != nil {
			id = C.GoString(n)
		}
		if fn := C.getFriendlyName(&list, C.int(i)); fn != nil {
			name = C.GoString(fn)
		}
		if name == "" {
			name = id
		}
		if id == "" {
			continue
		}
		out = append(out, DeviceInfo{ID: id, Name: name})
	}
	return out, nil
}

// NewDevice 用设备路径（List 返回的 ID）构造一个摄像头驱动。打开是**按路径
// 现开设备**（camera.Open 里 C 侧按 name 查找），不依赖任何注册缓存，所以
// 设备随时可新建、可打开。
func NewDevice(devicePath string) Device {
	return &camera{name: devicePath}
}

func (c *camera) Open() error {
	// COM is per-thread on Windows. Go goroutines can run on any OS thread,
	// so ensure COM is initialized on the current one before making COM calls.
	C.CoInitializeEx(nil, C.COINIT_MULTITHREADED)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cam != nil {
		return fmt.Errorf("camera already open")
	}

	c.ch = make(chan []byte)
	c.done = make(chan struct{})
	c.closed = false
	c.cam = &C.camera{
		name: C.CString(c.name),
	}

	var errStr *C.char
	if C.listResolution(c.cam, &errStr) != 0 {
		C.free(unsafe.Pointer(c.cam.name))
		c.cam = nil
		return fmt.Errorf("failed to open device: %s", C.GoString(errStr))
	}

	return nil
}

//export imageCallback
func imageCallback(cam uintptr) {
	callbacksMu.RLock()
	cb, ok := callbacks[uintptr(unsafe.Pointer(cam))]
	if !ok {
		callbacksMu.RUnlock()
		return
	}
	copy(cb.bufGo, unsafe.Slice((*byte)(cb.cbuf), cb.bufLen))
	callbacksMu.RUnlock()

	select {
	case cb.ch <- cb.bufGo:
	case <-cb.done:
	}
}

func (c *camera) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cam := c.cam
	c.cam = nil
	cbuf := c.cbuf
	c.cbuf = nil
	done := c.done
	ch := c.ch
	c.mu.Unlock()

	// Remove from callbacks map so no new imageCallback calls find this cam
	callbacksMu.Lock()
	delete(callbacks, uintptr(unsafe.Pointer(cam)))
	callbacksMu.Unlock()

	if done != nil {
		close(done)
	}

	if cam != nil {
		C.free(unsafe.Pointer(cam.name))
		C.freeCamera(cam)
	}

	C.free(cbuf)

	if ch != nil {
		close(ch)
	}
	return nil
}

func (c *camera) VideoRecord(p prop.Media) (video.Reader, error) {
	C.CoInitializeEx(nil, C.COINIT_MULTITHREADED)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cam == nil {
		return nil, fmt.Errorf("camera not open")
	}

	nPix := p.Width * p.Height
	bufSize := nPix * 2
	c.cbuf = C.malloc(C.size_t(bufSize))
	if c.cbuf == nil {
		return nil, fmt.Errorf("failed to allocate frame buffer")
	}
	c.bufLen = bufSize
	c.bufGo = make([]byte, bufSize)
	c.cam.width = C.int(p.Width)
	c.cam.height = C.int(p.Height)

	switch p.FrameFormat {
	case frame.FormatNV12:
		c.cam.fcc = fourccNV12
	default:
		c.cam.fcc = fourccYUY2
	}

	c.cam.buf = C.size_t(uintptr(c.cbuf))

	var errStr *C.char
	if C.openCamera(c.cam, &errStr) != 0 {
		C.free(c.cbuf)
		c.cbuf = nil
		return nil, fmt.Errorf("failed to open device: %s", C.GoString(errStr))
	}

	callbacksMu.Lock()
	callbacks[uintptr(unsafe.Pointer(c.cam))] = c
	callbacksMu.Unlock()

	img := &image.YCbCr{}

	r := video.ReaderFunc(func() (image.Image, func(), error) {
		b, ok := <-c.ch
		if !ok {
			return nil, func() {}, io.EOF
		}

		if p.FrameFormat == frame.FormatNV12 {
			// I420: Y plane (nPix) + U plane (nPix/4) + V plane (nPix/4)
			img.Y = b[:nPix]
			img.Cb = b[nPix : nPix+nPix/4]
			img.Cr = b[nPix+nPix/4 : nPix+nPix/2]
			img.YStride = p.Width
			img.CStride = p.Width / 2
			img.SubsampleRatio = image.YCbCrSubsampleRatio420
		} else { // YUY2
			// I422: Y plane (nPix) + Cb plane (nPix/2) + Cr plane (nPix/2)
			img.Y = b[:nPix]
			img.Cb = b[nPix : nPix+nPix/2]
			img.Cr = b[nPix+nPix/2 : nPix*2]
			img.YStride = p.Width
			img.CStride = p.Width / 2
			img.SubsampleRatio = image.YCbCrSubsampleRatio422
		}
		img.Rect = image.Rect(0, 0, p.Width, p.Height)
		return img, func() {}, nil
	})
	return r, nil
}

func (c *camera) Properties() []prop.Media {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cam == nil {
		return nil
	}
	properties := []prop.Media{}
	for i := 0; i < int(c.cam.numProps); i++ {
		p := C.getProp(c.cam, C.int(i))
		var fmt frame.Format
		switch p.fcc {
		case fourccYUY2:
			fmt = frame.FormatYUY2
		case fourccNV12:
			fmt = frame.FormatNV12
		default:
			continue
		}
		properties = append(properties, prop.Media{
			Video: prop.Video{
				Width:       int(p.width),
				Height:      int(p.height),
				FrameFormat: fmt,
			},
		})
	}
	return properties
}

const (
	fourccYUY2 = 0x32595559
	fourccNV12 = 0x3231564E
)
