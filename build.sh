#!/usr/bin/env bash
# mti-sip-phone Linux 构建脚本：本机 GUI 产物 + 无界面交叉产物，输出到 dist/
# 依赖：Go 1.23+、系统 gcc（CGO）
# 交叉编译目标（windows/amd64、windows/386、linux/arm64）为 HEADLESS 构建
#（-tags "headless noaudio"，纯 Go、无界面无音频）：fyne 的 OpenGL 绑定
# 依赖 CGO，跨平台 GUI 产物需目标平台的 gcc 工具链、请在各平台本机构建。
set -e
cd "$(dirname "$0")"

export GOTOOLCHAIN=local
mkdir -p dist

NATIVE_ARCH="$(go env GOARCH)"

echo "[1/4] 构建本机 GUI 版 sip-phone-linux-${NATIVE_ARCH}（CGO+音频）..."
go build -ldflags "-s -w" -o "dist/sip-phone-linux-${NATIVE_ARCH}" ./cmd/sip-phone

echo "[2/4] 构建 Windows amd64 无界面版（纯 Go）..."
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -tags "headless noaudio" -ldflags "-s -w" \
  -o dist/sip-phone-windows-amd64-headless.exe ./cmd/sip-phone

echo "[3/4] 构建 Windows 386 无界面版（纯 Go）..."
CGO_ENABLED=0 GOOS=windows GOARCH=386 \
  go build -tags "headless noaudio" -ldflags "-s -w" \
  -o dist/sip-phone-windows-386-headless.exe ./cmd/sip-phone

echo "[4/4] 构建 Linux arm64 无界面版（纯 Go；arm64 本机时与 [1] 相同）..."
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -tags "headless noaudio" -ldflags "-s -w" \
  -o dist/sip-phone-linux-arm64-headless ./cmd/sip-phone

echo "构建完成：dist/"
ls -la dist/
