// gen-appicon 生成 exe 文件图标 assets/app-icon.ico：把 internal/appicon 的
// 绿色话机 PNG（256×256，iconfont 原始渲染）CatmullRom 缩到多尺寸位图，
// 打包成多条目 PNG-ICO（Vista+ 支持，本项目门槛 Windows 10+）。
//
// 用法（仓库根目录）：go run ./tools/gen-appicon
//
// 下一步生成 .syso 资源（一次性，go-winres 纯 Go 工具，不写进 go.mod）：
//
//	go run github.com/tc-hib/go-winres@latest make \
//	  --in cmd/sip-phone/winres.json --arch amd64,386,arm64 --out cmd/sip-phone/rsrc
//
// 三个 rsrc_windows_*.syso 提交进仓库后，Go 对 Windows 目标自动链接它们，
// 所有 Windows 产物（完整版/无 UI 版/控制台版）都带图标，构建脚本零改动；
// 非 Windows 构建不受影响。
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"

	"mti-sip-phone/internal/appicon"
)

// sizes 图标尺寸档位（像素）。256 是 Windows 10+ 大图标显示的标准尺寸。
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	out := flag.String("out", filepath.Join("assets", "app-icon.ico"), "输出 ico 文件路径")
	flag.Parse()
	if err := run(*out); err != nil {
		log.Fatal(err)
	}
	fmt.Println("已生成", *out)
}

func run(out string) error {
	src, err := png.Decode(bytes.NewReader(appicon.PhoneGreenPNG()))
	if err != nil {
		return fmt.Errorf("解码话机 PNG 失败: %w", err)
	}
	sb := src.Bounds()

	var imgs []image.Image
	for _, s := range sizes {
		if s == sb.Dx() && s == sb.Dy() {
			imgs = append(imgs, src)
			continue
		}
		dst := image.NewRGBA(image.Rect(0, 0, s, s))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, draw.Over, nil)
		imgs = append(imgs, dst)
	}

	var buf bytes.Buffer
	if err := writeICO(&buf, imgs); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}

// writeICO 写多条目 ICO 容器（每条目为 PNG 数据；宽/高字节 0 表示 256）。
func writeICO(w *bytes.Buffer, imgs []image.Image) error {
	offset := 6 + 16*len(imgs)

	// ICONDIR 头：reserved + type(1=图标) + count
	writeU16(w, 0)
	writeU16(w, 1)
	writeU16(w, uint16(len(imgs)))

	// 各尺寸先编码成 PNG，再写目录条目
	var pngs [][]byte
	for _, img := range imgs {
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			return err
		}
		pngs = append(pngs, b.Bytes())
	}
	for i, img := range imgs {
		sz := img.Bounds().Dx()
		wb, hb := byte(sz), byte(sz)
		if sz >= 256 {
			wb, hb = 0, 0
		}
		w.WriteByte(wb)                   // 宽
		w.WriteByte(hb)                   // 高
		w.WriteByte(0)                    // 调色板色数（0 = 不适用）
		w.WriteByte(0)                    // 保留
		writeU16(w, 1)                    // 颜色平面数
		writeU16(w, 32)                   // 位深
		writeU32(w, uint32(len(pngs[i]))) // 数据大小
		writeU32(w, uint32(offset))       // 数据偏移
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		w.Write(p)
	}
	return nil
}

func writeU16(w *bytes.Buffer, v uint16) { _ = binary.Write(w, binary.LittleEndian, v) }
func writeU32(w *bytes.Buffer, v uint32) { _ = binary.Write(w, binary.LittleEndian, v) }
