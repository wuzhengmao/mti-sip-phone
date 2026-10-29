package appicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"

	"golang.org/x/image/draw"
)

// ICNS 把 PNG 图标编码成 macOS 的 .icns（PNG 内嵌格式，10.7+）。
//
// 与 Windows 的 ICO 一样，这里不让自安装去 shell 出 sips/iconutil——纯 Go
// 生成的好处是：可在任何平台上单测（自安装的 macOS 分支因此能被 CI 覆盖），
// 也不依赖 Xcode 工具。icns 的结构是
//
//	"icns" | 总长度(4B 大端) | 若干块（4B 类型 + 4B 块长 + PNG 数据）
//
// 块类型按尺寸固定（icp4=16、icp5=32、icp6=64、ic07=128、ic08=256、ic09=512）。
// 源图是 256×256，512 档靠放大，macOS 会优先取最接近的档位，观感足够。
func ICNS(srcPNG []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(srcPNG))
	if err != nil {
		return nil, err
	}
	sizes := []struct {
		kind string
		px   int
	}{
		{"icp4", 16},
		{"icp5", 32},
		{"icp6", 64},
		{"ic07", 128},
		{"ic08", 256},
		{"ic09", 512},
	}
	var body bytes.Buffer
	for _, s := range sizes {
		data, err := resizePNG(img, s.px)
		if err != nil {
			return nil, err
		}
		var hdr [8]byte
		copy(hdr[0:4], s.kind)
		binary.BigEndian.PutUint32(hdr[4:8], uint32(8+len(data)))
		body.Write(hdr[:])
		body.Write(data)
	}
	var out bytes.Buffer
	out.WriteString("icns")
	_ = binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

// resizePNG 把图缩放到 px×px 并编码成 PNG（CatmullRom，与托盘图标同一算法）。
func resizePNG(src image.Image, px int) ([]byte, error) {
	dst := image.NewRGBA(image.Rect(0, 0, px, px))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
