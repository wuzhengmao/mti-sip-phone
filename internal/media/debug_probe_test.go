package media

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// 探针的并发契约：写样本（音频回调线程）与关闭（通话结束线程）是并发路径，
// 向已关闭的通道发送会 panic。这里并发写、中途 close，再重复 close，配合
// -race 跑（`go test -race ./internal/media/`）。
func TestWavDumpConcurrentWriteClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "play-1.wav")
	d, err := newWavDump(path)
	if err != nil {
		t.Fatalf("创建 WAV 失败: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				d.write(make([]int16, 160))
			}
		}()
	}
	time.Sleep(5 * time.Millisecond) // 让写方先跑起来
	d.close()
	d.close() // 幂等：重复关闭不应 panic
	wg.Wait()

	// 头部必须回填了真实长度，否则播放器读不出内容
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 WAV 失败: %v", err)
	}
	if len(data) < 44 {
		t.Fatalf("WAV 太短: %d 字节", len(data))
	}
	riff := binary.LittleEndian.Uint32(data[4:8])
	dataLen := binary.LittleEndian.Uint32(data[40:44])
	if int(riff) != len(data)-8 {
		t.Errorf("RIFF 大小未回填: %d，实际 %d", riff, len(data)-8)
	}
	if int(dataLen) != len(data)-44 {
		t.Errorf("data 大小未回填: %d，实际 %d", dataLen, len(data)-44)
	}
}
