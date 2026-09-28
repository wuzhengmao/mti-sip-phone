package media

import "testing"

// TestPlayRingBasic 环形缓冲基本读写：写多少读多少，跨环形边界连续。
func TestPlayRingBasic(t *testing.T) {
	r := newPlayRing(1)
	samples := make([]int16, 8000)
	for i := range samples {
		samples[i] = int16(i)
	}
	r.Write(samples) // 恰好写满 1 秒容量

	buf := make([]int16, 8000)
	if n := r.Read(buf); n != 8000 {
		t.Fatalf("应读到 8000 样本，实际 %d", n)
	}
	for i := range buf {
		if buf[i] != int16(i) {
			t.Fatalf("样本 %d 应为 %d，实际 %d", i, i, buf[i])
		}
	}
}

// TestPlayRingOverflow 缓冲满后写新数据：最旧数据被丢弃，读到的总是最新。
func TestPlayRingOverflow(t *testing.T) {
	r := newPlayRing(1) // 容量 8000 样本
	first := make([]int16, 8000)
	for i := range first {
		first[i] = 1
	}
	r.Write(first)
	r.Write([]int16{2, 3, 4, 5}) // 溢出 4 个：丢弃最旧 4 个样本

	buf := make([]int16, 8000)
	if n := r.Read(buf); n != 8000 {
		t.Fatalf("应读到 8000 样本，实际 %d", n)
	}
	// 前 7996 个应为 1，末尾应为 2,3,4,5
	if buf[0] != 1 || buf[7995] != 1 {
		t.Fatal("溢出后保留的数据应是最新的旧数据")
	}
	for i, v := range buf[7996:] {
		if v != int16(i+2) {
			t.Fatalf("尾部新数据 %d 应为 %d，实际 %d", i, i+2, v)
		}
	}
}

// TestPlayRingReadLessThanAvailable 分次读取：数据不丢不重。
func TestPlayRingReadLessThanAvailable(t *testing.T) {
	r := newPlayRing(1)
	samples := make([]int16, 1000)
	for i := range samples {
		samples[i] = int16(i)
	}
	r.Write(samples)

	buf := make([]int16, 300)
	total := 0
	for total < 1000 {
		n := r.Read(buf)
		if n == 0 {
			t.Fatal("不应提前读空")
		}
		for i, v := range buf[:n] {
			if v != int16(total+i) {
				t.Fatalf("样本 %d 应为 %d，实际 %d", total+i, total+i, v)
			}
		}
		total += n
	}
	if n := r.Read(buf); n != 0 {
		t.Fatalf("读空后应返回 0，实际 %d", n)
	}
}

// TestPlayRingReset Reset 后缓冲清空。
func TestPlayRingReset(t *testing.T) {
	r := newPlayRing(1)
	r.Write(make([]int16, 160))
	r.Reset()
	buf := make([]int16, 160)
	if n := r.Read(buf); n != 0 {
		t.Fatalf("Reset 后应读空，实际 %d", n)
	}
}
