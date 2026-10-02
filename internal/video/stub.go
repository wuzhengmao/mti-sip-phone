//go:build !video || !cgo

// 未启用视频的构建（缺少 -tags video 或 FFmpeg 静态库）。
// 所有能力查询返回 false、所有构造函数返回 ErrUnsupported，
// 上层据此隐藏视频入口、SDP 不宣告 m=video——音频功能完全不受影响。
//
// 注意：即便带 video 标签，Go 工具链在没有 cgo 时也会退到这里
//（见 video_cgo.go 的 `video && cgo` 标签组合）。
package video

const supported = false

func encoderAvailable(string) bool { return false }

func newEncoder(string, int, int, int, int) (Encoder, error) { return nil, ErrUnsupported }

func newDecoder(string) (Decoder, error) { return nil, ErrUnsupported }

func scaleYUV(*YUV420P, int, int) (*YUV420P, error) { return nil, ErrUnsupported }

func toRGBA(*YUV420P) (*Frame, error) { return nil, ErrUnsupported }

func listCameras() ([]CameraInfo, error) { return nil, ErrUnsupported }

func openCamera(string, int, int, int) (Camera, error) { return nil, ErrUnsupported }
