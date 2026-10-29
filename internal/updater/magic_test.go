package updater

import "testing"

// TestIsMachOMagic macOS 自更新的下载校验：只认 Mach-O 魔数（含 universal），
// 挡掉 HTML 错误页/压缩包。
func TestIsMachOMagic(t *testing.T) {
	ok := [][]byte{
		{0xCF, 0xFA, 0xED, 0xFE, 0x07, 0, 0, 0}, // 64 位小端（Go 产出的 arm64/amd64 就是它）
		{0xCE, 0xFA, 0xED, 0xFE},                // 32 位小端
		{0xFE, 0xED, 0xFA, 0xCE},                // 大端 32 位
		{0xFE, 0xED, 0xFA, 0xCF},                // 大端 64 位
		{0xCA, 0xFE, 0xBA, 0xBE, 0, 0, 0, 2},    // universal（lipo 合并）
		{0xCA, 0xFE, 0xBA, 0xBF},
	}
	for _, h := range ok {
		if !isMachO(h) {
			t.Errorf("%x 应判为 Mach-O", h)
		}
	}
	bad := [][]byte{
		[]byte("<html>"),
		[]byte{0x7f, 'E', 'L', 'F'},
		[]byte("MZ"),
		{0x1f, 0x8b}, // gzip
		{},
		{0xCA},
	}
	for _, h := range bad {
		if isMachO(h) {
			t.Errorf("%x 不应判为 Mach-O", h)
		}
	}
}
