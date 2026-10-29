package updater

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// maxDownloadSize 下载体积上限：防御误填的超大文件（正常产物 ~30MB）。
const maxDownloadSize = 256 << 20

// runDownload 下载协程：完成后置 ready 并自动进入重启流程（有通话则等待）。
func (u *Updater) runDownload(ctx context.Context, info core.UpdateInfo) {
	err := u.download(ctx, info)

	u.mu.Lock()
	if u.cancel != nil {
		u.cancel = nil
	}
	switch {
	case err == nil:
		u.phase = core.UpdateReady
		u.progress = 100
		u.errMsg = ""
	case errors.Is(err, context.Canceled):
		// 用户取消：回到可重试状态，不算失败
		u.phase = core.UpdateAvailable
		u.downloaded, u.progress = 0, 0
		u.errMsg = ""
	default:
		u.phase = core.UpdateFailed
		u.errMsg = friendlyError(err)
	}
	st := u.statusLocked()
	u.mu.Unlock()

	if err != nil {
		if errors.Is(err, context.Canceled) {
			logx.Logger().Info("更新下载已取消")
		} else {
			logx.Logger().Warn("更新下载失败", "err", err)
		}
		u.publish(st)
		return
	}
	logx.Logger().Info("更新下载完成", "version", info.Version, "bytes", st.Total)
	u.publish(st)

	// 用户点击「更新」即授权了"下载→重启"整条链路：下载完成后自动重启，
	// 通话中则由 Apply 挂起等待（并发/重复调用由 Apply 自身去重）
	if err := u.Apply(); err != nil {
		logx.Logger().Error("重启安装失败", "err", err)
	}
}

// download 下载并校验更新文件：写 <exe>.new.tmp → 校验 → 改名 <exe>.new。
func (u *Updater) download(ctx context.Context, info core.UpdateInfo) (err error) {
	dir := filepath.Dir(u.exePath)
	// 前置可写预检：程序目录不可读时立即失败，避免下载到一半才报错
	if err = checkWritable(dir); err != nil {
		return err
	}

	tmp := u.exePath + newSuffix + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return fmt.Errorf("下载地址无效: %w", err)
	}
	req.Header.Set("User-Agent", "mti-sip-phone/"+core.Version)
	resp, err := u.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("服务器返回 %s", resp.Status)
	}
	if resp.ContentLength > maxDownloadSize {
		return fmt.Errorf("文件大小 %d MB 超过上限", resp.ContentLength>>20)
	}
	u.mu.Lock()
	u.total = resp.ContentLength
	u.mu.Unlock()

	hasher := sha256.New()
	buf := make([]byte, 64<<10)
	var downloaded int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)
			if downloaded > maxDownloadSize {
				return fmt.Errorf("文件超过 %d MB 上限", maxDownloadSize>>20)
			}
			hasher.Write(buf[:n])
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("写入文件失败（磁盘空间不足？）: %w", werr)
			}
			u.setProgress(downloaded)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// 注意返回 rerr 而非命名返回值 err——err 此时为 nil，
			// 写错会把"网络中断"当成下载成功（单测已覆盖）
			return rerr
		}
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	if downloaded == 0 {
		return errors.New("下载内容为空")
	}
	if info.SHA256 != "" {
		sum := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(sum, info.SHA256) {
			return errors.New("下载内容校验失败（sha256 不匹配），请重试")
		}
	}
	if err = checkExecutableMagic(tmp); err != nil {
		return err
	}
	// 同目录改名：与 exe 同卷，rename 为原子操作
	if err = os.Rename(tmp, u.exePath+newSuffix); err != nil {
		return fmt.Errorf("保存更新文件失败: %w", err)
	}
	return nil
}

// client 返回 HTTP 客户端（总超时由 ctx 控制，故客户端不设 Timeout）。
func (u *Updater) client() *http.Client {
	if u.opts.HTTPClient != nil {
		return u.opts.HTTPClient
	}
	return &http.Client{}
}

// setProgress 更新下载进度并节流发布事件（≥200ms 或 ≥1% 才推一次）。
func (u *Updater) setProgress(downloaded int64) {
	u.mu.Lock()
	u.downloaded = downloaded
	if u.total > 0 {
		u.progress = float64(downloaded) * 100 / float64(u.total)
	}
	now := time.Now()
	publish := now.Sub(u.lastPublish) >= 200*time.Millisecond ||
		(u.total > 0 && u.progress-u.lastProgress >= 1)
	if publish {
		u.lastPublish = now
		u.lastProgress = u.progress
	}
	st := u.statusLocked()
	u.mu.Unlock()
	if publish {
		u.publish(st)
	}
}

// checkWritable 探测目录是否可写（建一个临时文件再删）。
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".mti-update-probe-*")
	if err != nil {
		return fmt.Errorf("程序目录不可写，请以管理员身份运行或手动更新")
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// checkExecutableMagic 校验下载内容是否为当前平台的可执行文件
// （防止把 HTML 错误页/压缩包当成程序替换上去）。
func checkExecutableMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("读取下载文件失败: %w", err)
	}
	defer f.Close()

	head := make([]byte, 64)
	n, _ := io.ReadFull(f, head)
	head = head[:n]

	switch runtime.GOOS {
	case "windows":
		if n < 2 || head[0] != 'M' || head[1] != 'Z' {
			return errors.New("下载内容不是有效的程序文件")
		}
		// 进一步确认 PE 签名（e_lfanew 位于 0x3C）
		if n < 0x40 {
			return errors.New("下载内容不是有效的程序文件")
		}
		off := binary.LittleEndian.Uint32(head[0x3C:0x40])
		fi, serr := f.Stat()
		if serr != nil || int64(off)+4 > fi.Size() {
			return errors.New("下载内容不是有效的程序文件")
		}
		if _, serr := f.Seek(int64(off), io.SeekStart); serr != nil {
			return fmt.Errorf("读取下载文件失败: %w", serr)
		}
		sig := make([]byte, 4)
		if _, serr := io.ReadFull(f, sig); serr != nil ||
			sig[0] != 'P' || sig[1] != 'E' || sig[2] != 0 || sig[3] != 0 {
			return errors.New("下载内容不是有效的程序文件")
		}
	case "linux":
		if n < 4 || head[0] != 0x7f || head[1] != 'E' || head[2] != 'L' || head[3] != 'F' {
			return errors.New("下载内容不是有效的程序文件")
		}
	case "darwin":
		// macOS 没有"可执行扩展名"，只能靠 Mach-O 魔数（含 universal/fat）
		if !isMachO(head) {
			return errors.New("下载内容不是有效的程序文件")
		}
	}
	return nil
}

// isMachO 判断前 4 字节是否为 Mach-O 魔数（单架构或 universal/fat）。
// 小端文件在磁盘上是 CE FA ED FE / CF FA ED FE；universal 是 CA FE BA BE。
func isMachO(head []byte) bool {
	if len(head) < 4 {
		return false
	}
	switch binary.BigEndian.Uint32(head[:4]) {
	case 0xFEEDFACE, 0xFEEDFACF, // 大端 Mach-O
		0xCEFAEDFE, 0xCFFAEDFE, // 小端 Mach-O
		0xCAFEBABE, 0xCAFEBABF: // universal / fat
		return true
	}
	return false
}

// friendlyError 把底层错误转成面向用户的中文提示（尽量简短，
// 详细原因由日志记录；界面空间有限，不放 Go 原始错误文本）。
func friendlyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "下载超时，请重试"
	}
	var uerr interface{ Timeout() bool }
	if errors.As(err, &uerr) && uerr.Timeout() {
		return "连接更新服务器超时，请重试"
	}
	// 网络层错误（连接被拒绝/不可达/DNS 等）：统一给简短提示
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return "无法连接更新服务器，请重试"
	}
	msg := err.Error()
	// 明确的下载/校验类错误原样展示（本身已是中文且简短）
	switch {
	case strings.Contains(msg, "下载内容不是有效"),
		strings.Contains(msg, "校验失败"),
		strings.Contains(msg, "程序目录不可写"),
		strings.Contains(msg, "服务器返回"):
		return msg
	}
	if len(msg) > 40 {
		msg = msg[:40] + "…"
	}
	return "下载失败：" + msg
}
