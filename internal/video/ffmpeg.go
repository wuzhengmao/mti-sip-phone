//go:build video && cgo

package video

/*
#cgo LDFLAGS: -lavcodec -lswscale -lavutil -lvpx -lopenh264
#cgo windows LDFLAGS: -lmingw32 -lws2_32 -lbcrypt -lsecur32 -luser32 -ladvapi32 -lole32 -lstdc++
#cgo linux LDFLAGS: -lm -lpthread -ldl

// 视频编解码 cgo 绑定。
//
// 分工（见 scripts/build-video-libs.sh）：
//   - 解码：FFmpeg 原生解码器（h264/h263/vp8/vp9 全覆盖，无需外部库）
//   - 编码：H264 用 OpenH264、VP8/VP9 用 libvpx、H263 用 FFmpeg 原生编码器
//   - 色彩转换与缩放：libswscale
//
// 头文件与库路径由构建脚本通过 CGO_CFLAGS / CGO_LDFLAGS 传入
// （third_party/video/<target>/{include,lib}）。
//
// 所有跨 cgo 边界的数据都**拷贝**：C 侧缓冲的生命周期由本文件管理，
// Go 侧不持有任何 C 指针，规避 cgo 指针规则与 GC 的坑。
//
// 库搜索路径（-L）由构建脚本通过 CGO_LDFLAGS 传入（third_party/video/<target>/lib），
// 头文件路径由 CGO_CFLAGS 传入；下面的 #cgo 只声明库名本身。
// 链接顺序：被依赖者靠后（GNU ld 单遍解析），故 avcodec → swscale → avutil。
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
#include <libavutil/frame.h>
#include <libavutil/imgutils.h>
#include <libswscale/swscale.h>

#include <wels/codec_api.h>

#include <vpx/vpx_encoder.h>
#include <vpx/vp8cx.h>

// H263-1998 在 FFmpeg 里对应 AV_CODEC_ID_H263（RFC 4629 的负载格式由
// 我们自己在 Go 侧处理，编解码器本身用标准 H.263）。
enum sp_codec_id {
    SP_CODEC_H264 = 0,
    SP_CODEC_H263 = 1,
    SP_CODEC_VP8  = 2,
    SP_CODEC_VP9  = 3,
};

enum sp_enc_kind {
    SP_ENC_H264_OPENH264 = 0,
    SP_ENC_VP8           = 1,
    SP_ENC_VP9           = 2,
    SP_ENC_H263_FFMPEG   = 3,
};

static enum AVCodecID sp_av_codec_id(int id) {
    switch (id) {
    case SP_CODEC_H264: return AV_CODEC_ID_H264;
    case SP_CODEC_H263: return AV_CODEC_ID_H263;
    case SP_CODEC_VP8:  return AV_CODEC_ID_VP8;
    case SP_CODEC_VP9:  return AV_CODEC_ID_VP9;
    }
    return AV_CODEC_ID_NONE;
}

// ---------------------------------------------------------------- 解码

typedef struct sp_decoder {
    AVCodecContext *ctx;
    AVFrame        *frame;
    AVPacket       *pkt;
    uint8_t        *inbuf;      // 复用输入缓冲（含 FFmpeg 要求的尾部填充）
    int             incap;
    int             haveFrame;
    int             isKey;
} sp_decoder;

static sp_decoder *sp_dec_create(int codec_id) {
    const AVCodec *codec = avcodec_find_decoder(sp_av_codec_id(codec_id));
    if (!codec) return NULL;
    sp_decoder *d = (sp_decoder *)calloc(1, sizeof(sp_decoder));
    if (!d) return NULL;
    d->ctx = avcodec_alloc_context3(codec);
    d->frame = av_frame_alloc();
    d->pkt = av_packet_alloc();
    if (!d->ctx || !d->frame || !d->pkt) goto fail;
    // 低延迟：不做多帧重排（视频通话不需要 B 帧重排带来的额外延迟）
    d->ctx->flags |= AV_CODEC_FLAG_LOW_DELAY;
    d->ctx->thread_count = 1;
    if (avcodec_open2(d->ctx, codec, NULL) < 0) goto fail;
    return d;
fail:
    if (d->pkt) av_packet_free(&d->pkt);
    if (d->frame) av_frame_free(&d->frame);
    if (d->ctx) avcodec_free_context(&d->ctx);
    free(d);
    return NULL;
}

static void sp_dec_destroy(sp_decoder *d) {
    if (!d) return;
    if (d->pkt) av_packet_free(&d->pkt);
    if (d->frame) av_frame_free(&d->frame);
    if (d->ctx) avcodec_free_context(&d->ctx);
    if (d->inbuf) av_freep(&d->inbuf);
    free(d);
}

static void sp_dec_reset(sp_decoder *d) {
    if (!d) return;
    avcodec_flush_buffers(d->ctx);
    d->haveFrame = 0;
}

// 返回 1=解出一帧，0=无输出帧（参数集未齐等），-1=错误
static int sp_dec_decode(sp_decoder *d, const uint8_t *data, int len) {
    if (!d || len <= 0) return 0;
    // FFmpeg 的比特流读取器要求数据尾部有 AV_INPUT_BUFFER_PADDING_SIZE
    // 个零字节，否则可能越界读——必须自己补。
    int need = len + AV_INPUT_BUFFER_PADDING_SIZE;
    if (d->incap < need) {
        av_freep(&d->inbuf);
        d->inbuf = (uint8_t *)av_malloc(need);
        if (!d->inbuf) { d->incap = 0; return -1; }
        d->incap = need;
    }
    memcpy(d->inbuf, data, len);
    memset(d->inbuf + len, 0, AV_INPUT_BUFFER_PADDING_SIZE);
    d->pkt->data = d->inbuf;
    d->pkt->size = len;

    int ret = avcodec_send_packet(d->ctx, d->pkt);
    if (ret < 0 && ret != AVERROR(EAGAIN)) return -1;

    av_frame_unref(d->frame);
    ret = avcodec_receive_frame(d->ctx, d->frame);
    if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) return 0;
    if (ret < 0) return -1;
    d->haveFrame = 1;
    // FFmpeg 7+ 移除了 AVFrame.key_frame，改用 flags 里的 AV_FRAME_FLAG_KEY
    d->isKey = ((d->frame->flags & AV_FRAME_FLAG_KEY) != 0) ||
               (d->frame->pict_type == AV_PICTURE_TYPE_I);
    return 1;
}

static int sp_dec_width(sp_decoder *d)  { return d->frame->width; }
static int sp_dec_height(sp_decoder *d) { return d->frame->height; }
static int sp_dec_is_key(sp_decoder *d) { return d->isKey; }

// 把解码出的帧转成 RGBA 写入 dst（stride 为字节数），返回 0 成功。
static int sp_dec_convert_rgba(sp_decoder *d, uint8_t *dst, int stride) {
    if (!d || !d->haveFrame) return -1;
    struct SwsContext *sws = sws_getContext(
        d->frame->width, d->frame->height, (enum AVPixelFormat)d->frame->format,
        d->frame->width, d->frame->height, AV_PIX_FMT_RGBA,
        SWS_BILINEAR, NULL, NULL, NULL);
    if (!sws) return -1;
    uint8_t *dstData[4] = { dst, NULL, NULL, NULL };
    int dstLines[4] = { stride, 0, 0, 0 };
    sws_scale(sws, (const uint8_t *const *)d->frame->data, d->frame->linesize,
              0, d->frame->height, dstData, dstLines);
    sws_freeContext(sws);
    d->haveFrame = 0;
    return 0;
}

// ---------------------------------------------------------------- 编码

typedef struct sp_encoder {
    int kind;
    int w, h, fps, bitrate;

    // 输出缓冲（Annex-B / 裸帧），由本结构持有
    uint8_t *out;
    int      outCap;
    int      outSize;
    int      outKey;

    // H264 (OpenH264)
    // ⚠️ OpenH264 的 C 接口是个「vtable 指针的指针」：
    //   typedef const ISVCEncoderVtbl* ISVCEncoder;
    //   int WelsCreateSVCEncoder(ISVCEncoder** ppEncoder);
    // 所以调用形式是 (*svc)->Method(svc, ...)：先解引用拿到 vtable，
    // 再把 svc 自身作为显式 self 传进去。（C++ 下 ISVCEncoder 是类，
    // 没有这层间接，Pion 的 bridge.cpp 因此写不出这个坑。）
    ISVCEncoder *svc;

    // VP8/VP9 (libvpx)
    vpx_codec_ctx_t   vpx;
    int               vpxInit;
    vpx_codec_iface_t *vpxIface;
    int64_t           vpxPts;

    // H263 (FFmpeg)
    AVCodecContext *ff;
    AVFrame        *ffFrame;
    AVPacket       *ffPkt;
    int64_t         ffPts;
} sp_encoder;

static void sp_enc_destroy(sp_encoder *e);

static void sp_enc_set_out(sp_encoder *e, const uint8_t *data, int size, int key) {
    if (size > e->outCap) {
        e->out = (uint8_t *)realloc(e->out, size);
        e->outCap = size;
    }
    memcpy(e->out, data, size);
    e->outSize = size;
    e->outKey = key;
}

static sp_encoder *sp_enc_create(int kind, int w, int h, int fps, int bitrate) {
    if (w <= 0 || h <= 0 || fps <= 0) return NULL;
    sp_encoder *e = (sp_encoder *)calloc(1, sizeof(sp_encoder));
    if (!e) return NULL;
    e->kind = kind; e->w = w; e->h = h; e->fps = fps; e->bitrate = bitrate;

    switch (kind) {
    case SP_ENC_H264_OPENH264: {
        if (WelsCreateSVCEncoder(&e->svc) != 0 || !e->svc) goto fail;
        // 用 InitializeExt 而非 Initialize：SEncParamBase 不带 profile 字段，
        // 会让编码器落到 UNSPECIFIC profile 并打警告；显式设成 PRO_BASELINE
        // 与 SDP 里宣告的 profile-level-id=42e01f（Constrained Baseline）一致。
        SEncParamExt p;
        memset(&p, 0, sizeof(p));
        if ((*e->svc)->GetDefaultParams(e->svc, &p) != 0) goto fail;
        p.iUsageType = CAMERA_VIDEO_REAL_TIME;
        p.iPicWidth = w;
        p.iPicHeight = h;
        p.iTargetBitrate = bitrate;
        p.iMaxBitrate = bitrate * 2;
        p.iRCMode = RC_BITRATE_MODE;
        p.fMaxFrameRate = (float)fps;
        // 注意 profile/level 在**空间层**上，不在 SEncParamExt 顶层
        p.sSpatialLayers[0].uiProfileIdc = PRO_BASELINE;
        p.uiIntraPeriod = (unsigned int)(fps * 2); // 2 秒一个 IDR
        p.iMultipleThreadIdc = 2;
        p.bEnableFrameSkip = true;
        // 单层编码：把空间层 0 的参数补齐（GetDefaultParams 给的默认值不可靠）
        p.sSpatialLayers[0].iVideoWidth = w;
        p.sSpatialLayers[0].iVideoHeight = h;
        p.sSpatialLayers[0].fFrameRate = (float)fps;
        p.sSpatialLayers[0].iSpatialBitrate = bitrate;
        p.sSpatialLayers[0].iMaxSpatialBitrate = bitrate * 2;
        p.iSpatialLayerNum = 1;
        p.uiMaxNalSize = 1200; // 配合 RTP 分片，避免单个 NAL 过大
        if ((*e->svc)->InitializeExt(e->svc, &p) != 0) goto fail;
        EVideoFormatType vfmt = videoFormatI420;
        (*e->svc)->SetOption(e->svc, ENCODER_OPTION_DATAFORMAT, &vfmt);
        break;
    }
    case SP_ENC_VP8:
    case SP_ENC_VP9: {
        e->vpxIface = (kind == SP_ENC_VP8) ? vpx_codec_vp8_cx() : vpx_codec_vp9_cx();
        vpx_codec_enc_cfg_t cfg;
        if (vpx_codec_enc_config_default(e->vpxIface, &cfg, 0) != VPX_CODEC_OK) goto fail;
        cfg.g_w = w; cfg.g_h = h;
        cfg.g_timebase.num = 1; cfg.g_timebase.den = fps;
        cfg.rc_target_bitrate = (unsigned int)(bitrate / 1000); // kbps
        cfg.rc_end_usage = VPX_CBR;
        cfg.rc_min_quantizer = 4;
        cfg.rc_max_quantizer = 56;
        cfg.g_error_resilient = VPX_ERROR_RESILIENT_DEFAULT;
        cfg.g_lag_in_frames = 0;      // 实时：不做前向缓冲
        cfg.g_threads = 2;
        if (vpx_codec_enc_init(&e->vpx, e->vpxIface, &cfg, 0) != VPX_CODEC_OK) goto fail;
        e->vpxInit = 1;
        // VP8E_SET_CPUUSED 对 VP8/VP9 编码器都有效（libvpx 沿用同一套控制名）
        vpx_codec_control(&e->vpx, VP8E_SET_CPUUSED, 8); // 最快档（实时优先）
        break;
    }
    case SP_ENC_H263_FFMPEG: {
        const AVCodec *codec = avcodec_find_encoder(AV_CODEC_ID_H263P);
        if (!codec) codec = avcodec_find_encoder(AV_CODEC_ID_H263);
        if (!codec) goto fail;
        e->ff = avcodec_alloc_context3(codec);
        e->ffFrame = av_frame_alloc();
        e->ffPkt = av_packet_alloc();
        if (!e->ff || !e->ffFrame || !e->ffPkt) goto fail;
        e->ff->bit_rate = bitrate;
        e->ff->width = w; e->ff->height = h;
        e->ff->time_base.num = 1; e->ff->time_base.den = fps;
        e->ff->framerate.num = fps; e->ff->framerate.den = 1;
        e->ff->gop_size = fps * 2;
        e->ff->max_b_frames = 0;
        e->ff->pix_fmt = AV_PIX_FMT_YUV420P;
        e->ffFrame->format = AV_PIX_FMT_YUV420P;
        e->ffFrame->width = w;
        e->ffFrame->height = h;
        if (av_frame_get_buffer(e->ffFrame, 0) < 0) goto fail;
        if (avcodec_open2(e->ff, codec, NULL) < 0) goto fail;
        break;
    }
    default:
        goto fail;
    }
    return e;
fail:
    sp_enc_destroy(e);
    return NULL;
}

static void sp_enc_destroy(sp_encoder *e) {
    if (!e) return;
    if (e->svc) { (*e->svc)->Uninitialize(e->svc); WelsDestroySVCEncoder(e->svc); e->svc = NULL; }
    if (e->vpxInit) { vpx_codec_destroy(&e->vpx); e->vpxInit = 0; }
    if (e->ffPkt) av_packet_free(&e->ffPkt);
    if (e->ffFrame) av_frame_free(&e->ffFrame);
    if (e->ff) avcodec_free_context(&e->ff);
    free(e->out);
    free(e);
}

// 编码一帧 I420；返回 0 成功（可能 outSize==0，表示本次无输出），
// -1 一般错误，-2 输入帧尺寸小于编码器要求。
//
// inW/inH 是**输入帧的真实尺寸**。三个编码器都会按 e->w × e->h 去读 y/u/v，
// 输入比它小就是越界读——桌面程序崩溃的代价远大于丢一帧，所以这里硬拦。
// （正常情况下调用方已保证尺寸一致，这道校验是兜底。）
static int sp_enc_encode(sp_encoder *e, const uint8_t *y, const uint8_t *u, const uint8_t *v,
                         int sy, int su, int sv, int forceKey, int inW, int inH) {
    if (!e) return -1;
    if (inW < e->w || inH < e->h) return -2;
    e->outSize = 0;
    e->outKey = 0;

    switch (e->kind) {
    case SP_ENC_H264_OPENH264: {
        if (forceKey) (*e->svc)->ForceIntraFrame(e->svc, true);
        SSourcePicture pic;
        memset(&pic, 0, sizeof(pic));
        pic.iPicWidth = e->w; pic.iPicHeight = e->h;
        pic.iColorFormat = videoFormatI420;
        pic.iStride[0] = sy; pic.iStride[1] = pic.iStride[2] = su;
        pic.pData[0] = (unsigned char *)y;
        pic.pData[1] = (unsigned char *)u;
        pic.pData[2] = (unsigned char *)v;
        SFrameBSInfo info;
        memset(&info, 0, sizeof(info));
        if ((*e->svc)->EncodeFrame(e->svc, &pic, &info) != 0) return -1;
        if (info.eFrameType == videoFrameTypeSkip) return 0;
        int total = 0;
        for (int layer = 0; layer < info.iLayerNum; layer++) {
            for (int i = 0; i < info.sLayerInfo[layer].iNalCount; i++) {
                total += info.sLayerInfo[layer].pNalLengthInByte[i];
            }
        }
        if (total <= 0) return 0;
        if (total > e->outCap) { e->out = (uint8_t *)realloc(e->out, total); e->outCap = total; }
        int off = 0;
        for (int layer = 0; layer < info.iLayerNum; layer++) {
            int layerLen = 0;
            for (int i = 0; i < info.sLayerInfo[layer].iNalCount; i++)
                layerLen += info.sLayerInfo[layer].pNalLengthInByte[i];
            memcpy(e->out + off, info.sLayerInfo[layer].pBsBuf, (size_t)layerLen);
            off += layerLen;
        }
        e->outSize = total;
        e->outKey = (info.eFrameType == videoFrameTypeIDR);
        return 0;
    }
    case SP_ENC_VP8:
    case SP_ENC_VP9: {
        // 直接填充 vpx_image_t（等价于 vpx_img_wrap，但允许三个平面各自独立）
        vpx_image_t img;
        memset(&img, 0, sizeof(img));
        img.fmt = VPX_IMG_FMT_I420;
        img.w = e->w; img.h = e->h;
        img.d_w = e->w; img.d_h = e->h;
        img.x_chroma_shift = 1; img.y_chroma_shift = 1; // I420：色度 2x2 下采样
        img.planes[VPX_PLANE_Y] = (unsigned char *)y;
        img.planes[VPX_PLANE_U] = (unsigned char *)u;
        img.planes[VPX_PLANE_V] = (unsigned char *)v;
        img.stride[VPX_PLANE_Y] = sy;
        img.stride[VPX_PLANE_U] = su;
        img.stride[VPX_PLANE_V] = sv;
        vpx_enc_frame_flags_t flags = forceKey ? VPX_EFLAG_FORCE_KF : 0;
        vpx_codec_err_t rc = vpx_codec_encode(&e->vpx, &img, (vpx_codec_pts_t)e->vpxPts,
                                              1, flags, VPX_DL_REALTIME);
        e->vpxPts++;
        if (rc != VPX_CODEC_OK) return -1;
        vpx_codec_iter_t it = NULL;
        const vpx_codec_cx_pkt_t *pkt;
        while ((pkt = vpx_codec_get_cx_data(&e->vpx, &it)) != NULL) {
            if (pkt->kind != VPX_CODEC_CX_FRAME_PKT) continue;
            sp_enc_set_out(e, (const uint8_t *)pkt->data.frame.buf,
                           (int)pkt->data.frame.sz,
                           (pkt->data.frame.flags & VPX_FRAME_IS_KEY) != 0);
            return 0;
        }
        return 0;
    }
    case SP_ENC_H263_FFMPEG: {
        if (av_frame_make_writable(e->ffFrame) < 0) return -1;
        // 逐行拷贝，处理源步长与编码器帧步长不一致的情况
        for (int r = 0; r < e->h; r++)
            memcpy(e->ffFrame->data[0] + r * e->ffFrame->linesize[0], y + r * sy, e->w);
        for (int r = 0; r < (e->h + 1) / 2; r++)
            memcpy(e->ffFrame->data[1] + r * e->ffFrame->linesize[1], u + r * su, (e->w + 1) / 2);
        for (int r = 0; r < (e->h + 1) / 2; r++)
            memcpy(e->ffFrame->data[2] + r * e->ffFrame->linesize[2], v + r * sv, (e->w + 1) / 2);
        e->ffFrame->pts = e->ffPts++;
        if (forceKey) e->ffFrame->pict_type = AV_PICTURE_TYPE_I;
        else e->ffFrame->pict_type = AV_PICTURE_TYPE_NONE;

        int ret = avcodec_send_frame(e->ff, e->ffFrame);
        if (ret < 0) return -1;
        av_packet_unref(e->ffPkt);
        ret = avcodec_receive_packet(e->ff, e->ffPkt);
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) return 0;
        if (ret < 0) return -1;
        sp_enc_set_out(e, e->ffPkt->data, e->ffPkt->size,
                       (e->ffPkt->flags & AV_PKT_FLAG_KEY) != 0);
        return 0;
    }
    }
    return -1;
}

static const uint8_t *sp_enc_data(sp_encoder *e) { return e->out; }
static int sp_enc_size(sp_encoder *e) { return e->outSize; }
static int sp_enc_is_key(sp_encoder *e) { return e->outKey; }

// ---------------------------------------------------------------- 缩放/转换

// I420 → I420 缩放（sws_scale）。源与目标步长必须为「宽」的紧致排列。
static int sp_scale_i420(const uint8_t *sy, const uint8_t *su, const uint8_t *sv,
                         int sw, int sh,
                         uint8_t *dy, uint8_t *du, uint8_t *dv, int dw, int dh) {
    struct SwsContext *sws = sws_getContext(sw, sh, AV_PIX_FMT_YUV420P,
                                            dw, dh, AV_PIX_FMT_YUV420P,
                                            SWS_BILINEAR, NULL, NULL, NULL);
    if (!sws) return -1;
    const uint8_t *srcData[4] = { sy, su, sv, NULL };
    int srcLines[4] = { sw, sw / 2, sw / 2, 0 };
    uint8_t *dstData[4] = { dy, du, dv, NULL };
    int dstLines[4] = { dw, dw / 2, dw / 2, 0 };
    int rc = sws_scale(sws, srcData, srcLines, 0, sh, dstData, dstLines);
    sws_freeContext(sws);
    return rc < 0 ? -1 : 0;
}

// I420 定尺寸转 RGBA（dst 步长 = dw*4）
static int sp_i420_to_rgba(const uint8_t *sy, const uint8_t *su, const uint8_t *sv,
                           int w, int h, uint8_t *dst, int dstStride) {
    struct SwsContext *sws = sws_getContext(w, h, AV_PIX_FMT_YUV420P,
                                            w, h, AV_PIX_FMT_RGBA,
                                            SWS_BILINEAR, NULL, NULL, NULL);
    if (!sws) return -1;
    const uint8_t *srcData[4] = { sy, su, sv, NULL };
    int srcLines[4] = { w, w / 2, w / 2, 0 };
    uint8_t *dstData[4] = { dst, NULL, NULL, NULL };
    int dstLines[4] = { dstStride, 0, 0, 0 };
    int rc = sws_scale(sws, srcData, srcLines, 0, h, dstData, dstLines);
    sws_freeContext(sws);
    return rc < 0 ? -1 : 0;
}

// 初始化：压掉第三方库的默认日志——GUI 子系统没有控制台，
// 默认级别（FFmpeg 的 info、OpenH264 的 warning）会往 stderr 打大量
// 无用信息（等于丢弃），还会拖慢采集循环。
static void sp_video_init(void) {
    av_log_set_level(AV_LOG_ERROR);
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"mti-sip-phone/internal/logx"
)

const supported = true

func init() { C.sp_video_init() }

// --- 编解码器标识（与 C 侧枚举一一对应）---

func cCodecID(name string) (C.int, error) {
	switch name {
	case CodecH264:
		return C.SP_CODEC_H264, nil
	case CodecH263:
		return C.SP_CODEC_H263, nil
	case CodecVP8:
		return C.SP_CODEC_VP8, nil
	case CodecVP9:
		return C.SP_CODEC_VP9, nil
	}
	return 0, fmt.Errorf("未知视频编码: %s", name)
}

func cEncoderKind(name string) (C.int, error) {
	switch name {
	case CodecH264:
		return C.SP_ENC_H264_OPENH264, nil
	case CodecVP8:
		return C.SP_ENC_VP8, nil
	case CodecVP9:
		return C.SP_ENC_VP9, nil
	case CodecH263:
		return C.SP_ENC_H263_FFMPEG, nil
	}
	return 0, fmt.Errorf("未知视频编码: %s", name)
}

func encoderAvailable(name string) bool {
	kind, err := cEncoderKind(name)
	if err != nil {
		return false
	}
	// 用 1x1 试探能否真正创建（库缺失时这里就会失败）
	w, h := 176, 144
	e := C.sp_enc_create(kind, C.int(w), C.int(h), C.int(25), C.int(256000))
	if e == nil {
		return false
	}
	C.sp_enc_destroy(e)
	return true
}

// --- 编码器 ---

type ffmpegEncoder struct {
	e *C.sp_encoder
}

func newEncoder(name string, w, h, fps, bitrateBps int) (Encoder, error) {
	kind, err := cEncoderKind(name)
	if err != nil {
		return nil, err
	}
	if w%2 != 0 {
		w--
	}
	if h%2 != 0 {
		h--
	}
	e := C.sp_enc_create(kind, C.int(w), C.int(h), C.int(fps), C.int(bitrateBps))
	if e == nil {
		return nil, fmt.Errorf("创建 %s 编码器失败（%dx%d）", name, w, h)
	}
	logx.Logger().Debug("视频编码器已创建", "codec", name, "w", w, "h", h, "fps", fps, "bitrate", bitrateBps)
	return &ffmpegEncoder{e: e}, nil
}

func (enc *ffmpegEncoder) Encode(f *YUV420P, forceKey bool) ([]byte, bool, error) {
	if enc.e == nil {
		return nil, false, ErrUnsupported
	}
	if f.W%2 != 0 || f.H%2 != 0 {
		return nil, false, fmt.Errorf("I420 尺寸必须为偶数: %dx%d", f.W, f.H)
	}
	ck := C.int(0)
	if forceKey {
		ck = 1
	}
	rc := C.sp_enc_encode(enc.e,
		(*C.uint8_t)(unsafe.Pointer(&f.Y[0])),
		(*C.uint8_t)(unsafe.Pointer(&f.U[0])),
		(*C.uint8_t)(unsafe.Pointer(&f.V[0])),
		C.int(f.StrideY), C.int(f.StrideU), C.int(f.StrideV), ck,
		C.int(f.W), C.int(f.H))
	switch rc {
	case 0:
		// 继续
	case -2:
		// 输入帧比编码器要求的小：绝不硬编（会越界读），直接报错让上层重建编码器
		return nil, false, fmt.Errorf("视频帧尺寸 %dx%d 小于编码器要求，已拒绝编码", f.W, f.H)
	default:
		return nil, false, fmt.Errorf("视频编码失败")
	}
	n := int(C.sp_enc_size(enc.e))
	if n <= 0 {
		return nil, false, nil
	}
	out := C.GoBytes(unsafe.Pointer(C.sp_enc_data(enc.e)), C.int(n))
	return out, C.sp_enc_is_key(enc.e) != 0, nil
}

func (enc *ffmpegEncoder) SetBitrate(int) error { return nil } // 静态参数，暂不支持动态调整

func (enc *ffmpegEncoder) Close() {
	if enc.e != nil {
		C.sp_enc_destroy(enc.e)
		enc.e = nil
	}
}

// --- 解码器 ---

type ffmpegDecoder struct {
	d *C.sp_decoder
}

func newDecoder(name string) (Decoder, error) {
	id, err := cCodecID(name)
	if err != nil {
		return nil, err
	}
	d := C.sp_dec_create(id)
	if d == nil {
		return nil, fmt.Errorf("创建 %s 解码器失败", name)
	}
	return &ffmpegDecoder{d: d}, nil
}

func (dec *ffmpegDecoder) Decode(au []byte) (*Frame, error) {
	if dec.d == nil || len(au) == 0 {
		return nil, nil
	}
	rc := C.sp_dec_decode(dec.d, (*C.uint8_t)(unsafe.Pointer(&au[0])), C.int(len(au)))
	switch {
	case rc < 0:
		return nil, fmt.Errorf("视频解码失败")
	case rc == 0:
		return nil, nil // 参数集未齐 / 需要更多数据
	}
	w := int(C.sp_dec_width(dec.d))
	h := int(C.sp_dec_height(dec.d))
	if w <= 0 || h <= 0 {
		return nil, nil
	}
	f := &Frame{W: w, H: h, Stride: w * 4, Pix: make([]byte, w*4*h)}
	if C.sp_dec_convert_rgba(dec.d, (*C.uint8_t)(unsafe.Pointer(&f.Pix[0])), C.int(f.Stride)) != 0 {
		return nil, fmt.Errorf("视频帧色彩转换失败")
	}
	return f, nil
}

func (dec *ffmpegDecoder) Reset() {
	if dec.d != nil {
		C.sp_dec_reset(dec.d)
	}
}

func (dec *ffmpegDecoder) Close() {
	if dec.d != nil {
		C.sp_dec_destroy(dec.d)
		dec.d = nil
	}
}

// --- 缩放/转换 ---

func scaleYUV(src *YUV420P, w, h int) (*YUV420P, error) {
	if src == nil || w <= 0 || h <= 0 {
		return nil, fmt.Errorf("无效的缩放参数")
	}
	if w%2 != 0 {
		w--
	}
	if h%2 != 0 {
		h--
	}
	dst := NewYUV420P(w, h)
	rc := C.sp_scale_i420(
		(*C.uint8_t)(unsafe.Pointer(&src.Y[0])),
		(*C.uint8_t)(unsafe.Pointer(&src.U[0])),
		(*C.uint8_t)(unsafe.Pointer(&src.V[0])),
		C.int(src.W), C.int(src.H),
		(*C.uint8_t)(unsafe.Pointer(&dst.Y[0])),
		(*C.uint8_t)(unsafe.Pointer(&dst.U[0])),
		(*C.uint8_t)(unsafe.Pointer(&dst.V[0])),
		C.int(w), C.int(h))
	if rc != 0 {
		return nil, fmt.Errorf("视频缩放失败")
	}
	return dst, nil
}

func toRGBA(src *YUV420P) (*Frame, error) {
	if src == nil || len(src.Y) == 0 {
		return nil, fmt.Errorf("无效的帧数据")
	}
	f := &Frame{W: src.W, H: src.H, Stride: src.W * 4, Pix: make([]byte, src.W*4*src.H)}
	rc := C.sp_i420_to_rgba(
		(*C.uint8_t)(unsafe.Pointer(&src.Y[0])),
		(*C.uint8_t)(unsafe.Pointer(&src.U[0])),
		(*C.uint8_t)(unsafe.Pointer(&src.V[0])),
		C.int(src.W), C.int(src.H),
		(*C.uint8_t)(unsafe.Pointer(&f.Pix[0])), C.int(f.Stride))
	if rc != 0 {
		return nil, fmt.Errorf("YUV→RGBA 转换失败")
	}
	return f, nil
}
