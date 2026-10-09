# mti-sip-phone 交叉编译镜像：一个容器产出全部 10 个产物
#   完整版 × 5 平台 + 仅托盘无 UI 版（noui）× 5 平台
#   linux-amd64 / linux-arm64 / windows-amd64 / windows-386 / windows-arm64
#
# ── 为什么基础镜像是 Debian 10（glibc 2.28）──
#   信创环境实测的 glibc 底线：
#     统信 UOS 专业版（V20 各版本）      glibc 2.28（基于 Debian 10，官方明确不能单独升级）
#     银河麒麟 V10 SP1 桌面（x64/arm64） glibc 2.31
#     （龙芯/申威平台的麒麟为 2.28；deepin 23+ 是 2.38）
#   基线取两者更低者 = 2.28 —— 在 2.28 编译的产物在 glibc ≥ 2.28 的系统上都能跑
#   （glibc 严格向后兼容；注意第三方静态库也必须在**本镜像内**编译，见 README）。
#
# ── 为什么 llvm-mingw 钉在 20230320（而不是最新版）──
#   llvm-mingw 官方 Linux 包按构建宿主划分：≤20230320 是 ubuntu-18.04 包（宿主
#   glibc ≥ 2.27，Debian 10 能跑）；20230427~20250402 是 20.04 包（要 ≥2.31）；
#   20250417 起是 22.04 包（要 ≥2.35）。要在 glibc 2.28 的镜像里出 windows-arm64，
#   只能用 20230320（clang 16）这一档。
#
# ── 与 Windows 产物无关 ──
#   交叉编译到 Windows 时宿主 glibc 不参与，Windows 产物的门槛仍由 Go 工具链决定
#   （Go 1.22 起要求 Windows 10+），换基线不影响它们。
#
# 构建与用法见同目录 README.md；宿主侧一键脚本是 docker/build.sh。

FROM debian:10

ENV DEBIAN_FRONTEND=noninteractive TZ=Asia/Shanghai

# ── Debian 10 已 EOL：源指向 archive，并关掉 Release 有效期检查 ──
# main 用华为云镜像（首包 0.19s，官方 archive.debian.org 在国外、拉大包慢）；
# **security 源必须留、而且只有官方归档有**——buster 的 libc6 最终版是 +deb10u4，
# 而 main 里停在 +deb10u1；基础镜像 debian:10 自带的 libc6 是 +deb10u3，缺了
# security 源就凑不齐依赖（apt 不许降级）→ libc6-dev 装不上、整个构建 exit 100
# （实测踩了一天）。华为云没有 debian-security 路径（404），所以那行走官方。
# 基础镜像没有 ca-certificates，所以用 http——apt 的完整性由包内 GPG 签名保证。
RUN printf 'deb http://mirrors.huaweicloud.com/debian-archive/debian buster main contrib non-free\ndeb http://archive.debian.org/debian-security buster/updates main contrib non-free\n' \
      > /etc/apt/sources.list \
 && echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/99no-check-valid-until \
 && echo 'Acquire::Retries "3";' > /etc/apt/apt.conf.d/99retries

# ── arm64 的图形栈开发包（完整版 linux-arm64 交叉编译 fyne 需要目标架构的 .pc）──
# Debian 的 arm64 包与 amd64 在同一个 archive 仓库里，不需要额外的 ports 源。
RUN dpkg --add-architecture arm64

# ── 全部构建依赖（amd64 与 arm64 分两层装，别合并）──
# buster 的部分 -dev 包（mesa 那串）**不是 Multi-Arch: same**，与 amd64 的同名包
# 混在一条 install 里会让 apt 的依赖解析整体失败——连 build-essential 都报
# "Depends: libc6-dev but it is not going to be installed"（实测，整个构建 exit 100）。
# 注意 buster 的 GL 开发包叫 libgl1-mesa-dev（libgl-dev 是 bullseye 后才拆出来的）。
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git xz-utils file make \
      build-essential pkg-config \
      nasm cmake ninja-build meson python3 python3-pip \
      autoconf automake libtool m4 \
      gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64 \
      gcc-mingw-w64-i686 g++-mingw-w64-i686 \
      gcc-aarch64-linux-gnu g++-aarch64-linux-gnu \
      libx11-dev libxrandr-dev libxinerama-dev libxcursor-dev libxi-dev \
      libxxf86vm-dev libgl1-mesa-dev libegl1-mesa-dev libwayland-dev \
      libxkbcommon-dev libxkbcommon-x11-dev \
 && rm -rf /var/lib/apt/lists/*

# arm64 图形栈开发包（完整版 linux-arm64 交叉编 fyne 需要目标架构的 .pc 与 .so）
RUN apt-get update && apt-get install -y --no-install-recommends \
      libx11-dev:arm64 libxrandr-dev:arm64 libxinerama-dev:arm64 libxcursor-dev:arm64 \
      libxi-dev:arm64 libxxf86vm-dev:arm64 libgl1-mesa-dev:arm64 libegl1-mesa-dev:arm64 \
      libwayland-dev:arm64 libxkbcommon-dev:arm64 libxkbcommon-x11-dev:arm64 \
 && rm -rf /var/lib/apt/lists/*

# buster 自带的 meson 0.49 / cmake 3.13 太老（webrtc-audio-processing 与 abseil 要新版）。
# 两个坑：① buster 的 pip 18 读不懂新版 pyproject.toml（装 cmake 直接 TomlError 崩）；
# ② Python 是 3.7，pip 最高只能升到 21.3.1（22+ 要求 3.8）。所以：meson 是纯 Python 包，
# 升级 pip 后装；**cmake 干脆不用 pip**，走 Kitware 官方预编译二进制（不碰 Python）；
# ninja 用上面 apt 装的 ninja-build。
RUN python3 -m pip install --no-cache-dir --upgrade "pip<22" setuptools wheel \
 && pip3 install --no-cache-dir "meson>=1.0,<1.4"

ARG CMAKE_VERSION=3.27.9
RUN curl -fsSL -o /tmp/cmake.tgz \
      "https://github.com/Kitware/CMake/releases/download/v${CMAKE_VERSION}/cmake-${CMAKE_VERSION}-linux-x86_64.tar.gz" \
 && mkdir -p /opt/cmake && tar -xzf /tmp/cmake.tgz -C /opt/cmake --strip-components=1 \
 && rm /tmp/cmake.tgz

# ── nasm 2.16（源码编到 /usr/local，优先于 apt 的 2.14）──
# buster 的 nasm 2.14 编不了 FFmpeg 的 x86 汇编：libavcodec/x86/vp8dsp.asm 报
# `WIN64_SPILL_XMM ... symbol not defined before use`（nasm 2.15 才修的老缺陷），
# 只影响两个 amd64 目标——386 走 --disable-x86asm、arm64 本就没有 x86 汇编。
ARG NASM_VERSION=2.16.03
RUN curl -fsSL -o /tmp/nasm.tar.xz \
      "https://www.nasm.us/pub/nasm/releasebuilds/${NASM_VERSION}/nasm-${NASM_VERSION}.tar.xz" \
 && tar -xJf /tmp/nasm.tar.xz -C /tmp \
 && cd "/tmp/nasm-${NASM_VERSION}" \
 && ./configure --prefix=/usr/local >/dev/null \
 && make -j"$(nproc)" >/dev/null \
 && make install >/dev/null \
 && rm -rf /tmp/nasm* \
 && nasm -v

# ── Go（官方 tarball；buster 源里的 1.11 差得太远）──
# 升级 Go 只改这一个 ARG（也可用 build.sh --go <版本> 覆盖）。
ARG GO_VERSION=1.25.0
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
      | tar -C /usr/local -xz

# ── llvm-mingw（windows-arm64 的交叉工具链，GCC 系 mingw 没有 aarch64 目标）──
# 必须停在 20230320：它是最后一个提供 ubuntu-18.04 包的版本（宿主 glibc 要求 2.27，
# Debian 10 的 2.28 满足）。更新的版本要求宿主 ≥2.31/≥2.35，在本镜像里跑不起来。
ARG LLVM_MINGW_VERSION=20230320
RUN curl -fsSL -o /tmp/llvm-mingw.tar.xz \
      "https://github.com/mstorsjo/llvm-mingw/releases/download/${LLVM_MINGW_VERSION}/llvm-mingw-${LLVM_MINGW_VERSION}-ucrt-ubuntu-18.04-x86_64.tar.xz" \
 && mkdir -p /opt/llvm-mingw \
 && tar -xJf /tmp/llvm-mingw.tar.xz -C /opt/llvm-mingw --strip-components=1 \
 && rm /tmp/llvm-mingw.tar.xz

# /opt/llvm-mingw 本来就在 build.sh 的探测默认位置里，进 PATH 只是为了保险；
# /opt/cmake/bin 放最前，压过 apt 装的老 cmake 3.13
ENV PATH=/usr/local/go/bin:/opt/cmake/bin:/usr/local/bin:/opt/llvm-mingw/bin:$PATH \
    LLVM_MINGW=/opt/llvm-mingw \
    GOMODCACHE=/go/pkg/mod \
    GOCACHE=/root/.cache/go-build

# ── 自检：任何一件工具链缺失就让镜像构建失败（好过编译到一半才发现）──
# 必须用 bash：Debian 的 /bin/sh 是 dash，**不支持 `set -o pipefail`**
# （直接报 Illegal option 退 2，看起来像自检失败、其实是 shell 不认这个选项）。
SHELL ["/bin/bash", "-c"]
RUN set -euxo pipefail; \
    go version; \
    gcc --version; \
    x86_64-w64-mingw32-gcc --version; \
    i686-w64-mingw32-gcc --version; \
    aarch64-linux-gnu-gcc --version; \
    aarch64-linux-gnu-g++ --version; \
    aarch64-w64-mingw32-clang --version; \
    nasm -v; \
    pkg-config --version; \
    meson --version; \
    cmake --version; \
    pkg-config --exists x11 && echo "pkgconfig amd64 x11 ok"; \
    PKG_CONFIG_LIBDIR=/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig pkg-config --exists x11 \
      && echo "pkgconfig arm64 x11 ok"; \
    ldd --version

WORKDIR /src
