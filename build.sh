#!/usr/bin/env bash
# mti-sip-phone Linux 构建脚本
# 依赖：Go 1.23+、系统 gcc（CGO）
set -e
cd "$(dirname "$0")"

export GOTOOLCHAIN=local

echo "[1/2] 构建 sip-phone（Linux 无控制台窗口概念，直接构建）..."
go build -ldflags "-s -w" -o bin/sip-phone ./cmd/sip-phone

echo "[2/2] 构建纯 Go 版（-tags noaudio，无音频后端，交叉编译用）..."
go build -tags noaudio -ldflags "-s -w" -o bin/sip-phone-noaudio ./cmd/sip-phone

echo "构建完成：bin/sip-phone、bin/sip-phone-noaudio"
