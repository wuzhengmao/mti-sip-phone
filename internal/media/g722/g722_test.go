package g722

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 参考向量（testdata/*.hex）由 FFmpeg 的 G.722 参考实现一次性生成，
// **不是**用本包生成的——它是"我们的实现与标准一致"的硬证据。
//
// 生成方法（一次性工具，不进仓库）：
//  1. 取 FFmpeg 源码树，用最小配置编出参考实现：
//     ./configure --disable-everything --enable-encoder=adpcm_g722 \
//       --enable-decoder=adpcm_g722 --disable-programs --disable-doc \
//       --disable-network --disable-autodetect --disable-x86asm --cc=gcc
//     （Windows 宿主必须先 export TMPDIR=<Windows 形式绝对路径>，
//      否则 configure 的临时文件路径会被路径转换吃掉）
//  2. 用一个小 C 程序调 avcodec 的 g722 编/解码器（trellis 保持默认 0 =
//     标准编码器），输入是确定性生成的 12 帧（静音 / 低频 / 高频 / 噪声 /
//     幅度扫掠 / 满幅交替），输出 in_pcm.hex、ref_enc.hex、ref_dec.hex。
//
// 断言：**编码逐字节一致、解码逐样本一致**——两端都是 ITU-T G.722 的
// 位精确实现，任何一处表值/移位写错都会在这里暴露（而且不会因为"两端
// 错得一样"而假通过：向量来自独立实现）。

// readVectors 读三份十六进制向量（去掉换行与空白）。
func readVectors(t *testing.T) (pcm []int16, stream []byte, decoded []int16) {
	t.Helper()
	readHex := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		b, err := hex.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		return b
	}
	pcmBytes := readHex("in_pcm.hex")
	stream = readHex("ref_enc.hex")
	decBytes := readHex("ref_dec.hex")

	pcm = make([]int16, len(pcmBytes)/2)
	for i := range pcm {
		pcm[i] = int16(uint16(pcmBytes[2*i]) | uint16(pcmBytes[2*i+1])<<8)
	}
	decoded = make([]int16, len(decBytes)/2)
	for i := range decoded {
		decoded[i] = int16(uint16(decBytes[2*i]) | uint16(decBytes[2*i+1])<<8)
	}
	return pcm, stream, decoded
}

// TestEncoderGolden 编码器输出与参考实现逐字节一致。
func TestEncoderGolden(t *testing.T) {
	pcm, want, _ := readVectors(t)
	if len(pcm)%FrameSamples != 0 {
		t.Fatalf("向量长度不是帧长整数倍：%d", len(pcm))
	}
	enc := NewEncoder()
	got := make([]byte, 0, len(pcm)/2)
	out := make([]byte, FrameBytes)
	for off := 0; off < len(pcm); off += FrameSamples {
		enc.Encode(pcm[off:off+FrameSamples], out)
		got = append(got, out...)
	}
	if len(got) != len(want) {
		t.Fatalf("编码长度不符：得到 %d 字节，期望 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 字节不符（第 %d 帧第 %d 字节）：得到 %02x，期望 %02x",
				i, i/FrameBytes, i%FrameBytes, got[i], want[i])
		}
	}
}

// TestDecoderGolden 解码器输出与参考实现逐样本一致。
func TestDecoderGolden(t *testing.T) {
	_, stream, want := readVectors(t)
	if len(stream)%FrameBytes != 0 {
		t.Fatalf("码流长度不是帧长整数倍：%d", len(stream))
	}
	dec := NewDecoder()
	got := make([]int16, 0, len(stream)*2)
	out := make([]int16, FrameSamples)
	for off := 0; off < len(stream); off += FrameBytes {
		dec.Decode(stream[off:off+FrameBytes], out)
		got = append(got, out...)
	}
	if len(got) != len(want) {
		t.Fatalf("解码样本数不符：得到 %d，期望 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个样本不符：得到 %d，期望 %d", i, got[i], want[i])
		}
	}
}

// TestRoundTripGolden 同一路向量走完整往返：编码→解码，样本数一致、
// 能量相当（G.722 有损且有 22 样本时延，不逐样本比对）。
func TestRoundTripGolden(t *testing.T) {
	pcm, _, refDec := readVectors(t)
	enc := NewEncoder()
	dec := NewDecoder()
	encOut := make([]byte, FrameBytes)
	decOut := make([]int16, FrameSamples)
	got := make([]int16, 0, len(pcm))
	for off := 0; off < len(pcm); off += FrameSamples {
		enc.Encode(pcm[off:off+FrameSamples], encOut)
		dec.Decode(encOut, decOut)
		got = append(got, decOut...)
	}
	var sumSrc, sumGot, sumRef float64
	for i := range got {
		sumGot += float64(got[i]) * float64(got[i])
		sumRef += float64(refDec[i]) * float64(refDec[i])
		if i < len(pcm) {
			sumSrc += float64(pcm[i]) * float64(pcm[i])
		}
	}
	if sumRef == 0 || sumGot == 0 {
		t.Fatal("往返输出为空")
	}
	// 与参考解码结果的能量比应≈1（同一条码流），与原信号的能量比在合理区间
	if r := sumGot / sumRef; r < 0.99 || r > 1.01 {
		t.Fatalf("与参考解码结果能量比异常：%.4f", r)
	}
	if r := sumGot / sumSrc; r < 0.5 || r > 1.5 {
		t.Fatalf("与原信号能量比异常：%.4f", r)
	}
}

// TestResetToInitial 复位后与新建实例的初始状态一致（第一个字节即可区分）。
func TestResetToInitial(t *testing.T) {
	pcm := make([]int16, FrameSamples)
	for i := range pcm {
		pcm[i] = int16((i*131)%30000 - 15000)
	}
	a := NewEncoder()
	first := make([]byte, FrameBytes)
	a.Encode(pcm, first)
	// 再编几帧弄脏状态后复位
	junk := make([]byte, FrameBytes)
	for i := 0; i < 3; i++ {
		a.Encode(pcm, junk)
	}
	a.Reset()
	again := make([]byte, FrameBytes)
	a.Encode(pcm, again)
	if string(first) != string(again) {
		t.Fatal("Reset 后同一输入的编码结果应与新建实例一致")
	}

	b := NewDecoder()
	want := make([]int16, FrameSamples)
	b.Decode(first, want) // 新建实例的基准输出
	scratch := make([]int16, FrameSamples)
	for i := 0; i < 3; i++ {
		b.Decode(again, scratch) // 弄脏状态
	}
	b.Reset()
	got := make([]int16, FrameSamples)
	b.Decode(first, got)
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("Reset 后解码结果不一致（第 %d 个样本）", i)
		}
	}
}
