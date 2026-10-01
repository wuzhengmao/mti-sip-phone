package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strings"
)

// 密码加密方案：
//   - 密钥由本机标识派生（Windows 取注册表 MachineGuid，Linux 取 /etc/machine-id），
//     AES-256-GCM 加密后以 base64 落盘。
//   - 该方案保证配置文件被单独拷走时密码不可还原，但无法防御本机恶意进程。
//     后续版本可升级为 DPAPI / 系统钥匙串（P1）。

// machineID 获取本机唯一标识。
func machineID() string {
	switch runtime.GOOS {
	case "windows":
		if id := windowsMachineID(); id != "" {
			return id
		}
	case "linux":
		if id := linuxMachineID(); id != "" {
			return id
		}
	}
	// 兜底：用户名 + 主机名
	u, err := user.Current()
	if err != nil {
		return "unknown"
	}
	host, _ := os.Hostname()
	return u.Username + "@" + host
}

// windowsMachineID 的实现在 machineid_windows.go（构建标签 windows）：
// 注册表读取仅 Windows 可用，直接导入 x/sys/windows/registry 会破坏
// Linux 交叉编译。

func linuxMachineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		b, err := os.ReadFile(p)
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// secretKey 由本机标识派生 32 字节密钥。
func secretKey() []byte {
	sum := sha256.Sum256([]byte("mti-sip-phone:secret-v1:" + machineID()))
	return sum[:]
}

// encryptPassword 加密明文密码，返回 base64(iv+密文)。
func encryptPassword(plain string) (string, error) {
	block, err := aes.NewCipher(secretKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	ct := gcm.Seal(iv, iv, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// decryptPassword 解密 base64(iv+密文)，返回明文密码。
func decryptPassword(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("密文格式错误: %w", err)
	}
	block, err := aes.NewCipher(secretKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("密文长度非法")
	}
	iv, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, iv, ct, nil)
	if err != nil {
		return "", fmt.Errorf("解密失败（可能配置文件来自其他机器）")
	}
	return string(plain), nil
}
