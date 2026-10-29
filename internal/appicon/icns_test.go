package appicon

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestICNSStructure ICNS 头部与块长度必须自洽（macOS 对格式很严格，
// 长度写错会整个图标不显示）。
func TestICNSStructure(t *testing.T) {
	data, err := ICNS(PhoneGreenPNG())
	if err != nil {
		t.Fatalf("生成 ICNS 失败: %v", err)
	}
	if len(data) < 8 {
		t.Fatalf("ICNS 过短: %d", len(data))
	}
	if string(data[0:4]) != "icns" {
		t.Fatalf("魔数不对: %q", data[0:4])
	}
	total := binary.BigEndian.Uint32(data[4:8])
	if int(total) != len(data) {
		t.Fatalf("总长度字段 %d 与实际 %d 不一致", total, len(data))
	}
	var got []string
	for off := 8; off < len(data); {
		if off+8 > len(data) {
			t.Fatalf("块头越界: off=%d len=%d", off, len(data))
		}
		kind := string(data[off : off+4])
		clen := int(binary.BigEndian.Uint32(data[off+4 : off+8]))
		if clen < 8 || off+clen > len(data) {
			t.Fatalf("块 %s 长度非法: %d（off=%d len=%d）", kind, clen, off, len(data))
		}
		if !bytes.HasPrefix(data[off+8:off+clen], []byte("\x89PNG")) {
			t.Fatalf("块 %s 的数据不是 PNG", kind)
		}
		got = append(got, kind)
		off += clen
	}
	want := []string{"icp4", "icp5", "icp6", "ic07", "ic08", "ic09"}
	if len(got) != len(want) {
		t.Fatalf("块数量 %d，期望 %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 块是 %s，期望 %s", i, got[i], want[i])
		}
	}
}
