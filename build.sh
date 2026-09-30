#!/usr/bin/env bash
# mti-sip-phone Linux 构建脚本：本机 Linux GUI 产物 + Windows GUI / 对侧 Linux 架构交叉产物，输出到 dist/
# 依赖：Go 1.23+、系统 gcc（CGO）
#   - 交叉 Windows GUI 需 mingw-w64（amd64/arm64 主机均有包）：
#       Debian/Ubuntu: sudo apt install gcc-mingw-w64-x86-64 gcc-mingw-w64-i686
#       Fedora:        sudo dnf install mingw64-gcc mingw32-gcc
#   - 交叉对侧 Linux 架构（amd64 主机→arm64，arm64 主机→amd64）需对应架构的
#     交叉 gcc + 图形栈开发包，缺啥由脚本检测并打印准备步骤（下方 cross_env_hint）
#   缺失工具链/开发包时该目标跳过，不影响其余产物。
# 交叉 Windows GUI 无需 Windows SDK：glfw 的 C 源码与 mingw 专用头随 go-gl/glfw
# 模块自带，malgo 自带 miniaudio.c，链接目标仅 mingw 自带的系统导入库。
# 注意：交叉出的 Linux 产物链接的是构建机 sysroot 里的 glibc，目标机 glibc 过旧
# 会报 GLIBC_x.y not found，此时改用更旧的构建环境（或 Docker 镜像）交叉。
set -e
cd "$(dirname "$0")"

export GOTOOLCHAIN=local
mkdir -p dist

NATIVE_ARCH="$(go env GOARCH)"

# ---------- 交叉目标探测 ----------

MINGW64_CC="$(command -v x86_64-w64-mingw32-gcc || true)"
MINGW32_CC="$(command -v i686-w64-mingw32-gcc || true)"

# Linux 交叉：目标是「本机架构之外」的那个（amd64 主机→arm64，arm64 主机→amd64）。
# 无 tag 时 glfw 同时编 X11 与 Wayland 后端，go-gl/gl 走 pkg-config: gl，故下面
# 11 个 .pc 必须齐全；查的是目标架构的 pkg-config 目录，避免落到本机架构的 .pc。
case "$NATIVE_ARCH" in
    amd64)
        CROSS_ARCH="arm64"
        CROSS_TRIPLE="aarch64-linux-gnu"
        CROSS_GCC_PKG="gcc-aarch64-linux-gnu"
        CROSS_PC_LIBDIR_DEFAULT="/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig"
        CROSS_APT_NOTE="Ubuntu：主源行加 [arch=amd64]，另加 ports 源 [arch=arm64]（http://ports.ubuntu.com/ubuntu-ports）"
        ;;
    arm64)
        CROSS_ARCH="amd64"
        CROSS_TRIPLE="x86_64-linux-gnu"
        CROSS_GCC_PKG="gcc-x86-64-linux-gnu"
        CROSS_PC_LIBDIR_DEFAULT="/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/share/pkgconfig"
        CROSS_APT_NOTE="Ubuntu：主源行加 [arch=arm64]，另加主源 [arch=amd64]（http://archive.ubuntu.com/ubuntu）"
        ;;
    *)
        CROSS_ARCH=""
        ;;
esac

CROSS_CC="${CROSS_CC:-}"
CROSS_PC_LIBDIR="${CROSS_PKG_CONFIG_LIBDIR:-$CROSS_PC_LIBDIR_DEFAULT}"
CROSS_PC_NEEDED="x11 xrandr xinerama xcursor xi xxf86vm gl wayland-client wayland-cursor wayland-egl xkbcommon"
CROSS_PC_MISSING=""

pc_found_in_libdir() {
    old_ifs="$IFS"
    IFS=:
    for d in $CROSS_PC_LIBDIR; do
        if [ -f "$d/$1.pc" ]; then
            IFS="$old_ifs"
            return 0
        fi
    done
    IFS="$old_ifs"
    return 1
}

if [ -n "$CROSS_ARCH" ]; then
    if [ -z "$CROSS_CC" ]; then
        CROSS_CC="$(command -v "${CROSS_TRIPLE}-gcc" || true)"
    fi
    for pc in $CROSS_PC_NEEDED; do
        pc_found_in_libdir "$pc" || CROSS_PC_MISSING="$CROSS_PC_MISSING ${pc}.pc"
    done
fi

cross_env_hint() {
    cat <<EOF
      准备 ${CROSS_ARCH} 交叉环境（Debian/Ubuntu 多架构，一次性）：
        sudo dpkg --add-architecture ${CROSS_ARCH}
        Debian：主源已含 amd64 与 arm64，dpkg 之后 apt update 即可
        ${CROSS_APT_NOTE}
        sudo apt update
        sudo apt install ${CROSS_GCC_PKG} \\
          libx11-dev:${CROSS_ARCH} libxrandr-dev:${CROSS_ARCH} libxinerama-dev:${CROSS_ARCH} \\
          libxcursor-dev:${CROSS_ARCH} libxi-dev:${CROSS_ARCH} libxxf86vm-dev:${CROSS_ARCH} \\
          libgl-dev:${CROSS_ARCH} libwayland-dev:${CROSS_ARCH} libxkbcommon-dev:${CROSS_ARCH}
      其他发行版或自建 sysroot：用 CROSS_CC（可含 --sysroot）与
        CROSS_PKG_CONFIG_LIBDIR=<${CROSS_ARCH} 的 pkgconfig 目录> 指定
      或用 Docker 免配置：fyne-cross linux -arch ${CROSS_ARCH} ./cmd/sip-phone
      提示：${CROSS_ARCH} 产物在本机（${NATIVE_ARCH}）跑不起来，验收需对应架构的桌面或 QEMU
EOF
}

# ---------- 构建 ----------

echo "[1/4] 构建本机 GUI 版 sip-phone-linux-${NATIVE_ARCH}（CGO+音频）..."
go build -ldflags "-s -w" -o "dist/sip-phone-linux-${NATIVE_ARCH}" ./cmd/sip-phone

if [ -z "$CROSS_ARCH" ]; then
    echo "[2/4] 跳过 Linux 交叉：本机架构 ${NATIVE_ARCH} 不在支持列表（amd64/arm64）"
elif [ -z "$CROSS_CC" ]; then
    echo "[2/4] 跳过 linux/${CROSS_ARCH}：未找到 ${CROSS_TRIPLE}-gcc"
    cross_env_hint
elif [ -n "$CROSS_PC_MISSING" ]; then
    echo "[2/4] 跳过 linux/${CROSS_ARCH}：${CROSS_ARCH} 图形栈开发包不全，缺${CROSS_PC_MISSING}"
    echo "      pkg-config 查找目录：${CROSS_PC_LIBDIR}"
    cross_env_hint
else
    echo "[2/4] 交叉构建 dist/sip-phone-linux-${CROSS_ARCH}（CGO+音频，CC=${CROSS_CC}）..."
    CGO_ENABLED=1 GOOS=linux GOARCH="$CROSS_ARCH" CC="$CROSS_CC" \
        PKG_CONFIG_LIBDIR="$CROSS_PC_LIBDIR" PKG_CONFIG_PATH= \
        go build -ldflags "-s -w" -o "dist/sip-phone-linux-${CROSS_ARCH}" ./cmd/sip-phone
fi

# Windows amd64 托盘版（参数与 build.bat 的同名目标一致：GUI 子系统无控制台）
if [ -n "$MINGW64_CC" ]; then
    echo "[3/4] 交叉构建 dist/sip-phone-windows-amd64.exe（CGO+音频，CC=${MINGW64_CC}）..."
    CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="$MINGW64_CC" \
        go build -ldflags "-s -w -H=windowsgui" \
        -o dist/sip-phone-windows-amd64.exe ./cmd/sip-phone
else
    echo "[3/4] 跳过 Windows amd64：未找到 x86_64-w64-mingw32-gcc"
    echo "      安装：apt install gcc-mingw-w64-x86-64 / dnf install mingw64-gcc"
fi

# Windows 386 托盘版（-extldflags -static：免 mingw 运行时 DLL，同 build.bat）
if [ -n "$MINGW32_CC" ]; then
    echo "[4/4] 交叉构建 dist/sip-phone-windows-386.exe（CGO+音频，静态，CC=${MINGW32_CC}）..."
    CGO_ENABLED=1 GOOS=windows GOARCH=386 CC="$MINGW32_CC" \
        go build -ldflags "-s -w -H=windowsgui -extldflags -static" \
        -o dist/sip-phone-windows-386.exe ./cmd/sip-phone
else
    echo "[4/4] 跳过 Windows 386：未找到 i686-w64-mingw32-gcc"
    echo "      安装：apt install gcc-mingw-w64-i686 / dnf install mingw32-gcc"
fi

echo "构建完成：dist/"
ls -la dist/
