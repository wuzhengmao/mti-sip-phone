package wsapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/image/draw"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// 网页订阅视频流（REQ-VIDEO-009）。
//
// 链路：媒体层解码出 RGBA → core.VideoSink（多播出口）→ 本中枢按订阅参数
// 缩放 + JPEG 编码 → 以 **WS 二进制帧**推给订阅的连接。
//
// 为什么走二进制而不是 JSON：JPEG 数据经 base64 要塞进 JSON 会多 33% 体积，
// 且要重复解析；二进制帧零编码开销。
//
// 帧格式（固定头 + JPEG 数据）：
//
//	偏移 0:   通道（1 = 远端画面，2 = 本地画面）
//	偏移 1-4: 序号（uint32 大端，逐帧自增，客户端可据此发现丢帧）
//	偏移 5..: JPEG 数据
//
// ⚠️ OnVideoFrame 在**媒体层 goroutine 上同步调用**：无人订阅时必须立即返回，
// 有人订阅时只投递「最新帧槽」——永不阻塞解码循环（编码在独立 goroutine 上）。

// 二进制帧的通道号。
const (
	frameChannelRemote byte = 1 // 远端画面
	frameChannelLocal  byte = 2 // 本地画面
)

// 订阅参数默认值与钳制范围。
// 默认值用户指定：最大宽/高 640、30fps、JPEG 质量 75。
const (
	defaultVideoMaxW    = 640
	defaultVideoMaxH    = 640
	defaultVideoFPS     = 30
	defaultVideoQuality = 75

	minVideoMax     = 64
	maxVideoMax     = 1920
	minVideoFPS     = 1
	maxVideoFPS     = 30
	minVideoQuality = 30
	maxVideoQuality = 95
)

// videoParams 一次订阅的画面参数（钳制后的生效值）。
// 可比较，用作编码分组键。
type videoParams struct {
	MaxW    int `json:"maxWidth"`
	MaxH    int `json:"maxHeight"`
	FPS     int `json:"fps"`
	Quality int `json:"quality"`
}

// defaultVideoParams 默认订阅参数。
func defaultVideoParams() videoParams {
	return videoParams{
		MaxW:    defaultVideoMaxW,
		MaxH:    defaultVideoMaxH,
		FPS:     defaultVideoFPS,
		Quality: defaultVideoQuality,
	}
}

// clamp 用请求参数覆盖默认值并钳制到合法范围（非法值不报错，就近取范围端点）。
func (p videoParams) clamp() videoParams {
	p.MaxW = clampInt(p.MaxW, minVideoMax, maxVideoMax, defaultVideoMaxW)
	p.MaxH = clampInt(p.MaxH, minVideoMax, maxVideoMax, defaultVideoMaxH)
	p.FPS = clampInt(p.FPS, minVideoFPS, maxVideoFPS, defaultVideoFPS)
	p.Quality = clampInt(p.Quality, minVideoQuality, maxVideoQuality, defaultVideoQuality)
	return p
}

// videoParamReq video.subscribe 里**单条通道**的画面参数（字段缺省 = 取默认值）。
type videoParamReq struct {
	MaxWidth  int `json:"maxWidth"`
	MaxHeight int `json:"maxHeight"`
	FPS       int `json:"fps"`
	Quality   int `json:"quality"`
}

// params 转成内部参数（零值交由 clamp 补默认）。
func (r *videoParamReq) params() videoParams {
	return videoParams{MaxW: r.MaxWidth, MaxH: r.MaxHeight, FPS: r.FPS, Quality: r.Quality}
}

// clampInt v 为 0（未指定）时取默认值，否则钳制到 [lo, hi]。
func clampInt(v, lo, hi, def int) int {
	if v <= 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// streamKey 编码分组键：通道 + 参数相同的订阅者共用一次缩放/编码。
type streamKey struct {
	local  bool
	params videoParams
}

// videoSub 一个连接的视频订阅（字段只在订阅登记时写入，之后由编码 goroutine 只读）。
type videoSub struct {
	c            *wsConn
	wantLocal    bool
	localParams  videoParams
	wantRemote   bool
	remoteParams videoParams
	seq          uint32 // 逐帧自增（只由编码 goroutine 修改）
}

// frameMsg 待编码的一帧。
type frameMsg struct {
	local bool
	img   *image.RGBA
}

// videoHub 网页视频推送中枢：注册为 core.VideoSink 的附加订阅者。
type videoHub struct {
	app *core.App

	// frameCh 最新帧槽：容量 1，编码跟不上时挤掉旧帧（视频允许丢帧）。
	frameCh chan frameMsg

	startOnce sync.Once // 编码 goroutine 懒启动（无订阅者时零开销）
	nextID    atomic.Uint64

	mu   sync.RWMutex
	subs map[uint64]*videoSub

	// 以下字段**只由编码 goroutine 读写**，无需加锁。
	lastSent map[streamKey]time.Time
}

// newVideoHub 创建中枢并注册到 core.App 的视频帧出口。
func newVideoHub(app *core.App) *videoHub {
	h := &videoHub{
		app:      app,
		frameCh:  make(chan frameMsg, 1),
		subs:     make(map[uint64]*videoSub),
		lastSent: make(map[streamKey]time.Time),
	}
	app.AddVideoSink(h)
	return h
}

// OnVideoFrame core.VideoSink 实现（媒体层 goroutine 同步调用）。
func (h *videoHub) OnVideoFrame(_ string, local bool, img *image.RGBA) {
	if img == nil || !h.hasSubscriber(local) {
		return // 无人订阅本通道：立即返回（绝大多数帧走这条路径）
	}
	m := frameMsg{local: local, img: img}
	select {
	case h.frameCh <- m:
	default:
		// 编码跟不上：把待编码的旧帧挤掉，永远只编码最新一帧。
		select {
		case <-h.frameCh:
		default:
		}
		select {
		case h.frameCh <- m:
		default:
		}
	}
}

// hasSubscriber 是否有连接订阅了该通道。
func (h *videoHub) hasSubscriber(local bool) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.subs {
		if (local && s.wantLocal) || (!local && s.wantRemote) {
			return true
		}
	}
	return false
}

// Subscribe 登记（或更新）一个连接的视频订阅，返回两条通道的生效参数。
//
// **订阅必定包含远端画面**（用户明确要求）：远端恒为订阅，`local` 只表示
// 要不要本地画面。否则会出现"只订阅本地 → 桌面窗口被隐藏、远端画面却没人看"
// 的糊涂状态。两条通道各自带参数（本地回显通常要小一号）。
func (h *videoHub) Subscribe(c *wsConn, local bool, localP, remoteP videoParams, keepWindow bool) (videoParams, videoParams) {
	remoteP = remoteP.clamp()
	localP = localP.clamp()
	h.mu.Lock()
	h.subs[c.id] = &videoSub{
		c: c, wantLocal: local, localParams: localP,
		wantRemote: true, remoteParams: remoteP,
	}
	h.mu.Unlock()

	// 通知核心层：网页在看画面 → 软电话默认隐藏视频窗口（REQ-VIDEO-009）；
	// 订阅方要求 keepWindow 时保持窗口显示（REQ-VIDEO-009 修订）
	h.app.SetWebVideoViewer(c.id, local, true, keepWindow)
	h.startOnce.Do(func() { go h.loop() })
	logx.Logger().Info("网页订阅视频", "id", c.id, "local", local,
		"remote", fmt.Sprintf("%dx%d@%dfps q%d", remoteP.MaxW, remoteP.MaxH, remoteP.FPS, remoteP.Quality),
		"localParams", fmt.Sprintf("%dx%d@%dfps q%d", localP.MaxW, localP.MaxH, localP.FPS, localP.Quality))
	return localP, remoteP
}

// Unsubscribe 取消该连接的全部视频订阅。
func (h *videoHub) Unsubscribe(c *wsConn) {
	h.mu.Lock()
	_, existed := h.subs[c.id]
	delete(h.subs, c.id)
	h.mu.Unlock()
	if existed {
		h.app.RemoveWebVideoViewer(c.id)
		logx.Logger().Info("网页取消订阅视频", "id", c.id)
	}
}

// loop 编码循环（单 goroutine）：从最新帧槽取帧 → 按参数分组编码 → 广播。
func (h *videoHub) loop() {
	for m := range h.frameCh { // 永不关闭：进程生命周期内长驻
		h.sendFrame(m)
	}
}

// sendFrame 把一帧编码后发给所有订阅连接（同参数只编码一次）。
func (h *videoHub) sendFrame(m frameMsg) {
	now := time.Now()

	// 快照订阅并按参数分组（发送在锁外做，慢客户端不会卡住订阅登记）
	h.mu.RLock()
	groups := make(map[videoParams][]*videoSub, 2)
	for _, s := range h.subs {
		var p videoParams
		if m.local {
			if !s.wantLocal {
				continue
			}
			p = s.localParams // 本地回显与远端画面各用各自的参数
		} else {
			if !s.wantRemote {
				continue
			}
			p = s.remoteParams
		}
		groups[p] = append(groups[p], s)
	}
	h.mu.RUnlock()
	if len(groups) == 0 {
		return
	}

	for p, subs := range groups {
		key := streamKey{local: m.local, params: p} // 同参数只编码一次
		if last, ok := h.lastSent[key]; ok && now.Sub(last) < time.Second/time.Duration(p.FPS) {
			continue // 限流：本组还没到下一帧时间
		}
		jpg, w, hgt, err := encodeFrame(m.img, p)
		if err != nil {
			logx.Logger().Warn("网页视频帧编码失败", "err", err)
			continue
		}
		h.lastSent[key] = now
		channel := frameChannelRemote
		if m.local {
			channel = frameChannelLocal
		}
		for _, s := range subs {
			s.seq++
			s.send(frameBytes(channel, s.seq, jpg))
		}
		logx.Logger().Debug("网页视频帧已推送", "local", m.local,
			"size", w, "x", hgt, "bytes", len(jpg), "conns", len(subs))
	}
}

// send 写一帧二进制消息。抢不到写锁（上一次写未完成/控制消息在写）就丢这一帧——
// 慢客户端既不拖住编码循环，也不阻塞控制消息；连接真正断开由读循环/心跳兜底。
func (s *videoSub) send(buf []byte) {
	c := s.c
	if !c.wmu.TryLock() {
		return
	}
	defer c.wmu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageBinary, buf); err != nil {
		logx.Logger().Debug("视频帧发送失败", "id", c.id, "err", err)
	}
}

// activeCall 当前进行中的通话（无则 nil），供订阅响应里带上视频状态快照。
func activeCall(app *core.App) *core.Call {
	st := app.State()
	for i := range st.Calls {
		if st.Calls[i].State != core.CallTerminated {
			return &st.Calls[i]
		}
	}
	return nil
}

// frameBytes 组装二进制帧：[通道][序号大端 4B][JPEG]。
func frameBytes(channel byte, seq uint32, jpg []byte) []byte {
	buf := make([]byte, 5+len(jpg))
	buf[0] = channel
	binary.BigEndian.PutUint32(buf[1:5], seq)
	copy(buf[5:], jpg)
	return buf
}

// encodeFrame 按订阅参数缩放（保持宽高比，双线性）并编码为 JPEG。
// 不修改原帧；仅在超过上限时缩放。
func encodeFrame(img *image.RGBA, p videoParams) ([]byte, int, int, error) {
	src := image.Image(img)
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w > p.MaxW || h > p.MaxH {
		k := float64(p.MaxW) / float64(w)
		if kh := float64(p.MaxH) / float64(h); kh < k {
			k = kh
		}
		nw, nh := int(float64(w)*k), int(float64(h)*k)
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		draw.ApproxBiLinear.Scale(dst, dst.Rect, src, img.Rect, draw.Src, nil)
		src, w, h = dst, nw, nh
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: p.Quality}); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), w, h, nil
}
