#!/usr/bin/env bash
# mti-sip-phone Linux 构建脚本：本机 Linux GUI 产物 + Windows GUI / 对侧 Linux 架构交叉产物，输出到 dist/
# 依赖：Go 1.24+、系统 gcc（CGO）
#   - 交叉 Windows GUI 需 mingw-w64（amd64/arm64 主机均有包）：
#       Debian/Ubuntu: sudo apt install gcc-mingw-w64-x86-64 gcc-mingw-w64-i686
#       Fedora:        sudo dnf install mingw64-gcc mingw32-gcc
#   - 交叉 Windows **arm64** GUI 需 llvm-mingw（clang 系）：GCC 系的 mingw-w64
#     没有 aarch64-w64-mingw32 目标，任何 gcc-mingw-w64-* 包都出不了它。
#     解压即用的包在 https://github.com/mstorsjo/llvm-mingw/releases
#     （llvm-mingw-<版本>-ucrt-ubuntu-22.04-x86_64.tar.xz，约 80 MB），
#     解压后把 bin 加进 PATH，或用 LLVM_MINGW=<解压目录> 指定；缺失则跳过该目标。
#     该目标的视频/Opus/音频预处理静态库尚未支持 arm64 → 固定为基础版（G.711/G.722）。
#   - 交叉对侧 Linux 架构（amd64 主机→arm64，arm64 主机→amd64）需对应架构的
#     交叉 gcc + 图形栈开发包，缺啥由脚本检测并打印准备步骤（下方 cross_env_hint）
#   缺失工具链/开发包时该目标跳过，不影响其余产物。
# 交叉 Windows GUI 无需 Windows SDK：glfw 的 C 源码与 mingw 专用头随 go-gl/glfw
# 模块自带，malgo 自带 miniaudio.c，链接目标仅 mingw 自带的系统导入库。
# 视频（REQ-VIDEO-*）：先跑一次 scripts/build-video-libs.sh 生成
#   third_party/video/<target>/{include,lib}（FFmpeg 解码 + libvpx/OpenH264 编码）。
#   存在则自动加 -tags video，缺失则该目标构建为无视频版本（音频不受影响）。
# 音频 Opus：先跑一次 scripts/build-audio-libs.sh 生成
#   third_party/audio/<target>/{include,lib}（libopus）。存在则自动加 -tags audio，
#   缺失则该目标不含 Opus（SDP 不宣告，仍走 PCMA/PCMU），与视频互不影响。
#   两个库都存在时标签为 -tags video,audio。
# 注意：交叉出的 Linux 产物链接的是构建机 sysroot 里的 glibc，目标机 glibc 过旧
# 会报 GLIBC_x.y not found，此时改用更旧的构建环境（或 Docker 镜像）交叉。
# 版本号：脚本产出的一律是「开发版」（版本号带 -rc，取自 internal/core/buildinfo.go
# 的基础版本）；正式发布走 SIP_PHONE_RELEASE=1 ./build.sh（scripts/release.sh 调用）。
set -e
cd "$(dirname "$0")"

export GOTOOLCHAIN=local
mkdir -p dist

# 构建时间注入 core.BuildTime（设置页版本行显示）；ISO 8601，T 分隔避免 ldflags 参数空格问题
BUILD_TS="$(date '+%Y-%m-%dT%H:%M:%S')"
LDFLAGS="-s -w -X mti-sip-phone/internal/core.BuildTime=${BUILD_TS}"

# 版本号：基础版本从 internal/core/buildinfo.go 读取（唯一真相源，脚本只读不改）。
# 开发构建一律带 -rc（非正式版）：正式版发布时数字相同（1.0.5 vs 1.0.5-rc）
# 也要能通知更新——预发布低于同号正式版的比较规则见 internal/updater/version.go。
# 正式构建：SIP_PHONE_RELEASE=1 ./build.sh（发布脚本 scripts/release.sh 会设置）。
VERSION_BASE="$(sed -n 's/^var Version = "\(.*\)"$/\1/p' internal/core/buildinfo.go)"
if [ -z "$VERSION_BASE" ]; then
    echo "无法从 internal/core/buildinfo.go 解析基础版本号" >&2
    exit 1
fi
if [ "${SIP_PHONE_RELEASE:-}" = "1" ]; then
    APP_VERSION="$VERSION_BASE"
else
    APP_VERSION="${VERSION_BASE}-rc"
fi
LDFLAGS="$LDFLAGS -X mti-sip-phone/internal/core.Version=${APP_VERSION}"
echo "版本：${APP_VERSION}$([ "${SIP_PHONE_RELEASE:-}" = "1" ] && echo "（正式版）" || echo "（开发版，-rc）")"

NATIVE_ARCH="$(go env GOARCH)"

# ---------- 交叉目标探测 ----------

MINGW64_CC="$(command -v x86_64-w64-mingw32-gcc || true)"
MINGW32_CC="$(command -v i686-w64-mingw32-gcc || true)"

# 交叉 Windows 时还必须显式给 **CXX**（与 CC 同前缀的 g++）：
#   - -tags video 时 mediadevices 的摄像头是 C++ 源（camera_windows.cpp），
#     cgo 编 C++ 走 CXX；
#   - 只要包里含 C++ 源，**链接驱动也用 CXX**（cmd/go/internal/work/gc.go）。
# 不设 CXX 时 Go 的默认值是裸 "g++"（cfg.DefaultCXX 对交叉目标不做映射），
# 也就是**宿主的 g++**——症状是拿 Linux 的 g++ 去编 Windows 源码：
# <iostream>/<unistd.h> 都在、唯独 <dshow.h> 找不到（实测），极具误导性。
# 另外带视频时链接要的 -lstdc++ 也由 g++ 包提供（Debian 上 g++ 与 gcc 分属两个包）。
# （Windows/git-bash 下 command -v 可能返回带 .exe 的路径，先剥掉后缀再换 g++）
MINGW64_CXX=""
if [ -n "$MINGW64_CC" ]; then _c="${MINGW64_CC%.exe}"; MINGW64_CXX="${_c%gcc}g++"; fi
MINGW32_CXX=""
if [ -n "$MINGW32_CC" ]; then _c="${MINGW32_CC%.exe}"; MINGW32_CXX="${_c%gcc}g++"; fi

# Windows arm64 用 **llvm-mingw**（clang 系）而非 GCC：mingw-w64 没有
# aarch64-w64-mingw32 目标。查找顺序：PATH → LLVM_MINGW=<解压目录> → 常见位置。
# LLVM_MINGW 显式给出时不再回落到默认位置（便于"故意指错来验证跳过分支"）。
ARM64_CC="$(command -v aarch64-w64-mingw32-clang || true)"
if [ -z "$ARM64_CC" ]; then
    if [ -n "${LLVM_MINGW:-}" ]; then
        # 显式指定时只看它：指错就跳过该目标（提示里会打印这个路径），不回落
        if [ -x "$LLVM_MINGW/bin/aarch64-w64-mingw32-clang" ]; then
            ARM64_CC="$LLVM_MINGW/bin/aarch64-w64-mingw32-clang"
        fi
    else
        for _d in "$HOME/tools/llvm-mingw" /opt/llvm-mingw /usr/local/llvm-mingw; do
            if [ -x "$_d/bin/aarch64-w64-mingw32-clang" ]; then
                ARM64_CC="$_d/bin/aarch64-w64-mingw32-clang"
                break
            fi
        done
    fi
fi
# CXX 是同名 + "++"（clang++）；Windows/git-bash 宿主下可执行文件带 .exe，保留后缀
ARM64_CXX=""
if [ -n "$ARM64_CC" ]; then
    case "$ARM64_CC" in
        *.exe) ARM64_CXX="${ARM64_CC%.exe}++.exe" ;;
        *)     ARM64_CXX="${ARM64_CC}++" ;;
    esac
fi

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
CROSS_CXX="${CROSS_CXX:-}"
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
    # 交叉的 C++ 编译器，它同时是**链接驱动**：只要构建里有任何一个包含 C++ 源，
    # Go 就用 CXX 去链接（cmd/go 的 gc.go），而 cfg.DefaultCXX 对交叉目标返回的
    # 是裸 "g++"——**宿主架构**的 g++。症状：宿主的 g++ 拿 aarch64 目标文件交给
    # 自己的 ld，报 "Relocations in generic ELF (EM: 183)" / "file in wrong
    # format"，错误里唯一的人话只有 /usr/bin/g++ 那一行，极具误导性（实测）。
    # 目前仓库里唯一的 C++ 源是 internal/audio/apm_bridge.cc（-tags apm）；
    # 带视频时虽无 C++ 源，但链接要 -lstdc++（OpenH264 是 C++ 实现），目标架构的
    # libstdc++ 同样由 g++ 包提供，所以这两种情况都要求交叉 g++。
    if [ -z "$CROSS_CXX" ]; then
        _cxx="${CROSS_CC%.exe}"
        case "$_cxx" in
            *gcc)
                if [ -x "${_cxx%gcc}g++" ]; then
                    CROSS_CXX="${_cxx%gcc}g++"
                fi
                ;;
        esac
    fi
    if [ -z "$CROSS_CXX" ]; then
        CROSS_CXX="$(command -v "${CROSS_TRIPLE}-g++" || true)"
    fi
    # 提示里用的 g++ 包名（Debian/Ubuntu 与交叉 gcc 同名同源）
    case "$CROSS_GCC_PKG" in
        gcc-*) CROSS_GXX_PKG="g++-${CROSS_GCC_PKG#gcc-}" ;;
        *)     CROSS_GXX_PKG="g++" ;;
    esac
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

# ---------- 第三方编解码库探测（视频 / 音频） ----------
#
# third_party/video/<target> 由 scripts/build-video-libs.sh 生成（FFmpeg 解码 +
# libvpx/OpenH264 编码三件套）；third_party/audio/<target> 由
# scripts/build-audio-libs.sh 生成（libopus 音频编码）。两者**互相独立**：
# 齐全则加对应标签（video / audio），缺失则该目标降级关闭该功能，其余功能
# 完全不受影响（沿用本脚本「缺失即跳过并提示」的一贯做法）。
#
# 判定必须逐个检查链接所需的 .a 文件，不能只看 lib 目录是否存在：库构建中途失败
# （或中断）会留下空壳目录，只看目录会把它当成可用，加上标签后编译立刻报缺头
# 文件（视频实测 fatal error: wels/codec_api.h；音频则是 opus/opus.h）。
# 注意：这里用普通字符串拼接而非 bash 数组——脚本要能用 sh(dash) 直接执行，
# 数组是 bash 专有语法，dash 会报 Syntax error: "(" unexpected。
BUILD_TAGS=""
VIDEO_ON=""
AUDIO_ON=""
# FEATURES 面向用户的特性摘要（如 "CGO+音频+视频+Opus"），CODEC_NOTES 是探测
# 过程中攒下的提示行（在目标行之后打印，否则提示会跑到目标行前面去）
FEATURES=""
CODEC_NOTES=""
# internal/video/ffmpeg.go 的 #cgo LDFLAGS 依赖这 5 个静态库（改那边记得同步这里）
VIDEO_LIBS="libavcodec.a libswscale.a libavutil.a libvpx.a libopenh264.a"
# internal/media/codec_opus.go 的 #cgo LDFLAGS 依赖 libopus（改那边记得同步这里）
AUDIO_LIBS="libopus.a"
# internal/audio/apm.go 的 #cgo LDFLAGS 依赖 webrtc-audio-processing + abseil
#（音频预处理 AEC/NS/AGC，REQ-MEDIA-007；改那边记得同步这里）
APM_LIBS="libwebrtc-audio-processing-1.a libwebrtc-audio-coding-1.a"

# 工程根目录的绝对路径。Windows（git-bash）宿主下必须用 **D:/... 形式**：
# go build 只会把 CGO_CFLAGS/CGO_LDFLAGS 里**第一个** MSYS 形式（/d/git/...）
# 的路径转成 Windows 形式，后面的原样传给原生 gcc → 找不到头文件（实测：
# video+opus 同时启用时报 opus/opus.h 缺失，单独启用却正常）。与
# scripts/build-video-libs.sh 的 abspath 同理。build.bat 用 %CD% 本来就是
# Windows 形式，不受影响。
case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) ABS_ROOT="$(pwd -W 2>/dev/null || pwd)" ;;
    *)                    ABS_ROOT="$(pwd)" ;;
esac

# 追加一个标签与一组 -I/-L（多个库共存时不能覆盖彼此）
add_cgo_dir() {
    BUILD_TAGS="${BUILD_TAGS:+$BUILD_TAGS,}$2"
    export CGO_CFLAGS="${CGO_CFLAGS:+$CGO_CFLAGS }-I$ABS_ROOT/$1/include"
    export CGO_LDFLAGS="${CGO_LDFLAGS:+$CGO_LDFLAGS }-L$ABS_ROOT/$1/lib"
}

# codec_note 攒一条提示（在目标构建行之后统一打印）
codec_note() {
    CODEC_NOTES="${CODEC_NOTES}      $1
"
}

# 探测某个库目录：$1=目录 $2=标签 $3=库文件列表 $4=构建脚本提示
# 齐全则加标签并注入路径（返回 0），否则记下提示（返回 1）
codec_libs_ok() {
    local dir="$1" tag="$2" libs="$3" script="$4"
    local missing="" lib
    for lib in $libs; do
        [ -f "$dir/lib/$lib" ] || missing="$missing $lib"
    done
    if [ -z "$missing" ]; then
        add_cgo_dir "$dir" "$tag"
        return 0
    fi
    if [ -d "$dir" ]; then
        codec_note "提示：$dir 不完整（缺${missing}），本次构建不含该功能"
        codec_note "      重跑 scripts/$script 可补齐"
    else
        codec_note "提示：未找到 $dir，本次构建不含该功能（跑 scripts/$script 可生成）"
    fi
    return 1
}

video_setup() {
    # $1 = 目标名（如 linux-amd64 / windows-amd64）
    if codec_libs_ok "third_party/video/$1" video "$VIDEO_LIBS" "build-video-libs.sh"; then
        VIDEO_ON=1
    fi
}

audio_setup() {
    # $1 = 目标名；音频扩展为独立标签，与视频互不影响
    if codec_libs_ok "third_party/audio/$1" audio "$AUDIO_LIBS" "build-audio-libs.sh"; then
        AUDIO_ON=1
    fi
}

apm_setup() {
    # $1 = 目标名；音频预处理（AEC/NS/AGC）是独立标签：库在同一个目录里，
    # 有 opus 没 APM（或反过来）都允许（两者分别降级）
    if codec_libs_ok "third_party/audio/$1" apm "$APM_LIBS" "build-audio-libs.sh"; then
        # 头文件装在 include/webrtc-audio-processing-1/ 下（meson 的版本后缀），
        # 源码里按 "modules/audio_processing/..." 引入，所以要再加这一层。
        # ⚠️ C++ 源码（apm_bridge.cc）读的是 CGO_CXXFLAGS，**不是** CGO_CFLAGS，
        # 两处都要给，否则 C 编译能找到头、C++ 编译找不到（实测踩过）。
        export CGO_CFLAGS="$CGO_CFLAGS -I$ABS_ROOT/third_party/audio/$1/include -I$ABS_ROOT/third_party/audio/$1/include/webrtc-audio-processing-1"
        export CGO_CXXFLAGS="$CGO_CXXFLAGS -I$ABS_ROOT/third_party/audio/$1/include -I$ABS_ROOT/third_party/audio/$1/include/webrtc-audio-processing-1"
        APM_ON=1
    fi
}

# 每个目标构建前重置标签与 CGO 路径，再分别探测视频与音频库
codec_setup() {
    BUILD_TAGS=""
    VIDEO_ON=""
    AUDIO_ON=""
    APM_ON=""
    CODEC_NOTES=""
    export CGO_CFLAGS=""
    export CGO_CXXFLAGS=""
    export CGO_LDFLAGS=""
    video_setup "$1"
    audio_setup "$1"
    apm_setup "$1"
    # 特性摘要（不列具体编码格式）：音频是基础（malgo 设备 + G.711，Opus 也是
    # 音频的一部分），视频与 Opus 由第三方静态库带来。四种组合直接写清楚，
    # 缺什么在这里标出，具体怎么补见后面的提示行。
    FEATURES="CGO+音频，无视频、无Opus"
    if [ -n "$VIDEO_ON" ]; then
        FEATURES="CGO+音频+视频，无Opus"
    fi
    if [ -n "$AUDIO_ON" ]; then
        FEATURES="CGO+音频，无视频"
    fi
    if [ -n "$VIDEO_ON" ] && [ -n "$AUDIO_ON" ]; then
        FEATURES="CGO+音频+视频"
    fi
    # 音频预处理（AEC/NS/AGC）单独标出：它是可独立降级的一项
    if [ -n "$APM_ON" ]; then
        FEATURES="$FEATURES+AEC"
    fi
}

# 组装 -tags 参数（标签用逗号分隔，避免把多个标签拆成多个参数）
tags_args() {
    if [ -n "$BUILD_TAGS" ]; then
        echo "-tags $BUILD_TAGS"
    fi
}

# ---------- 构建 ----------

# 构建变体（见 internal/variant 与 cmd/sip-phone/gui_noui.go）：
#   full = 完整版（Fyne 桌面界面 + 系统托盘）
#   noui = 仅托盘无 UI 版（-tags noui：只起系统托盘，不创建任何窗口，所有操作
#          走网页控制台）。它**不链接 fyne/GLFW**，所以 Linux 侧也不需要
#          X11/Wayland/GL 的 .pc 与开发包（systray 的 Linux 实现是纯 Go DBus）。
# 每个目标按变体各出一份产物：
#   dist/sip-phone-<os>-<arch>[.exe]          （full，名字与历史一致）
#   dist/sip-phone-no-ui-<os>-<arch>[.exe]    （noui）
variant_setup() {
    case "$1" in
    noui)
        VAR_INFIX="no-ui-"
        VAR_TAGS_EXTRA="noui"
        VAR_LABEL="no-ui"
        VAR_FEAT="，仅托盘无 UI"
        ;;
    *)
        VAR_INFIX=""
        VAR_TAGS_EXTRA=""
        VAR_LABEL="full"
        VAR_FEAT=""
        ;;
    esac
}

# apply_variant 把变体标签追加到 codec_setup 探测出的标签后面，并把变体写进
# 特性摘要（必须在对应目标的 codec_setup 之后调用）。
apply_variant() {
    if [ -z "$VAR_TAGS_EXTRA" ]; then
        return 0
    fi
    if [ -n "$BUILD_TAGS" ]; then
        BUILD_TAGS="$BUILD_TAGS,$VAR_TAGS_EXTRA"
    else
        BUILD_TAGS="$VAR_TAGS_EXTRA"
    fi
    FEATURES="${FEATURES}${VAR_FEAT}"
}

# out_name 目标产物名：dist/sip-phone-<变体前缀><os>-<arch><后缀>
out_name() {
    printf 'dist/sip-phone-%s%s-%s%s' "$VAR_INFIX" "$1" "$2" "$3"
}

# step_label 统一的进度行（共 10 步：5 个目标 × 2 个变体，跳过也算一步）。
STEP=0
step_label() {
    STEP=$((STEP + 1))
    echo "[$STEP/10] $*"
}

# ---- 各目标（每个函数按当前变体出一份产物）----

build_linux_native() {
    codec_setup "linux-${NATIVE_ARCH}"
    apply_variant
    step_label "构建 $VAR_LABEL 本机版 $(out_name linux "${NATIVE_ARCH}" "")（${FEATURES}）..."
    printf '%s' "$CODEC_NOTES"
    go build $(tags_args) -ldflags "$LDFLAGS" -o "$(out_name linux "${NATIVE_ARCH}" "")" ./cmd/sip-phone
}

build_linux_cross() {
    if [ -z "$CROSS_ARCH" ]; then
        step_label "跳过 $VAR_LABEL Linux 交叉：本机架构 ${NATIVE_ARCH} 不在支持列表（amd64/arm64）"
        return 0
    fi
    if [ -z "$CROSS_CC" ]; then
        step_label "跳过 $VAR_LABEL linux/${CROSS_ARCH}：未找到 ${CROSS_TRIPLE}-gcc"
        cross_env_hint
        return 0
    fi
    # 图形栈开发包只对完整版是必需的：仅托盘无 UI 版不链接 fyne，也就没有
    # X11/Wayland/GL 的 pkg-config 依赖（交叉 gcc/CXX 仍然要——音频是 CGO）。
    if [ -z "$VAR_TAGS_EXTRA" ] && [ -n "$CROSS_PC_MISSING" ]; then
        step_label "跳过 $VAR_LABEL linux/${CROSS_ARCH}：${CROSS_ARCH} 图形栈开发包不全，缺${CROSS_PC_MISSING}"
        echo "      pkg-config 查找目录：${CROSS_PC_LIBDIR}"
        cross_env_hint
        return 0
    fi
    codec_setup "linux-${CROSS_ARCH}"
    apply_variant
    if [ -z "$CROSS_CXX" ] && { [ -n "$VIDEO_ON" ] || [ -n "$APM_ON" ]; }; then
        step_label "跳过 $VAR_LABEL linux/${CROSS_ARCH}：带视频或音频预处理需要 ${CROSS_TRIPLE}-g++"
        printf '%s' "$CODEC_NOTES"
        echo "      安装：apt install ${CROSS_GXX_PKG}（或 dnf/其他发行版的对应交叉 g++）"
        echo "      原因：包里有 C++ 源（internal/audio/apm_bridge.cc）时 Go 用 CXX 当"
        echo "            链接驱动，没有交叉 g++ 就退回**宿主的** g++，链接必然失败"
        echo "            （报 Relocations in generic ELF (EM: 183) / file in wrong format）；"
        echo "            带视频时链接要的 -lstdc++ 也由该包提供"
        echo "      若不需要视频/预处理：删掉 third_party/video/linux-${CROSS_ARCH} 与"
        echo "            third_party/audio/linux-${CROSS_ARCH} 即可出基础版"
        echo "      自建工具链：用 CROSS_CC / CROSS_CXX 显式指定"
        return 0
    fi
    step_label "交叉构建 $VAR_LABEL $(out_name linux "${CROSS_ARCH}" "")（${FEATURES}，CC=${CROSS_CC} CXX=${CROSS_CXX}）..."
    printf '%s' "$CODEC_NOTES"
    CGO_ENABLED=1 GOOS=linux GOARCH="$CROSS_ARCH" CC="$CROSS_CC" CXX="$CROSS_CXX" \
        PKG_CONFIG_LIBDIR="$CROSS_PC_LIBDIR" PKG_CONFIG_PATH= \
        go build $(tags_args) -ldflags "$LDFLAGS" -o "$(out_name linux "${CROSS_ARCH}" "")" ./cmd/sip-phone
}

# Windows amd64 托盘版（参数与 build.bat 的同名目标一致：GUI 子系统无控制台）
# -extldflags -static：把 mingw 运行时（libwinpthread/libstdc++/libgcc）链进
# exe。不带这个参数时产物依赖这几个 DLL，而它们**不是 Windows 自带的**，
# 拷到没装 MinGW 的机器上会起不来（"libwinpthread-1.dll not found"）。
# Windows 386 目标一直是这么做的（见下）。
build_windows_amd64() {
    if [ -z "$MINGW64_CC" ]; then
        step_label "跳过 $VAR_LABEL Windows amd64：未找到 x86_64-w64-mingw32-gcc"
        echo "      安装：apt install gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64 / dnf install mingw64-gcc mingw64-gcc-c++"
        echo "            （g++ 用于 -tags video：摄像头是 C++ 源，链接也需它提供 libstdc++）"
        return 0
    fi
    codec_setup "windows-amd64"
    apply_variant
    if [ -n "$VIDEO_ON" ] && [ ! -x "$MINGW64_CXX" ]; then
        step_label "跳过 $VAR_LABEL Windows amd64：带视频需要 ${MINGW64_CXX}（g++ 与 gcc 是两个包）"
        echo "      安装：apt install g++-mingw-w64-x86-64 / dnf install mingw64-gcc-c++"
        echo "      若不需要视频：删掉 third_party/video/windows-amd64 即可出无视频版本"
        return 0
    fi
    step_label "交叉构建 $VAR_LABEL $(out_name windows amd64 .exe)（${FEATURES}，静态，CC=${MINGW64_CC} CXX=${MINGW64_CXX}）..."
    printf '%s' "$CODEC_NOTES"
    CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC="$MINGW64_CC" CXX="$MINGW64_CXX" \
        go build $(tags_args) -ldflags "$LDFLAGS -H=windowsgui -extldflags -static" \
        -o "$(out_name windows amd64 .exe)" ./cmd/sip-phone
}

# Windows 386 托盘版（-extldflags -static：免 mingw 运行时 DLL，同 build.bat）
build_windows_386() {
    if [ -z "$MINGW32_CC" ]; then
        step_label "跳过 $VAR_LABEL Windows 386：未找到 i686-w64-mingw32-gcc"
        echo "      安装：apt install gcc-mingw-w64-i686 g++-mingw-w64-i686 / dnf install mingw32-gcc mingw32-gcc-c++"
        echo "            （386 带视频时链接仍需 g++ 提供的 libstdc++；摄像头 C++ 源也要 g++）"
        return 0
    fi
    codec_setup "windows-386"
    apply_variant
    if [ -n "$VIDEO_ON" ] && [ ! -x "$MINGW32_CXX" ]; then
        step_label "跳过 $VAR_LABEL Windows 386：带视频需要 ${MINGW32_CXX}（链接 -lstdc++ 由 g++ 包提供）"
        echo "      安装：apt install g++-mingw-w64-i686 / dnf install mingw32-gcc-c++"
        echo "      若不需要视频：删掉 third_party/video/windows-386 即可出无视频版本"
        return 0
    fi
    step_label "交叉构建 $VAR_LABEL $(out_name windows 386 .exe)（${FEATURES}，静态，CC=${MINGW32_CC}）..."
    printf '%s' "$CODEC_NOTES"
    CGO_ENABLED=1 GOOS=windows GOARCH=386 CC="$MINGW32_CC" CXX="$MINGW32_CXX" \
        go build $(tags_args) -ldflags "$LDFLAGS -H=windowsgui -extldflags -static" \
        -o "$(out_name windows 386 .exe)" ./cmd/sip-phone
}

# Windows arm64 托盘版。工具链是 llvm-mingw（clang），**不是** GCC 系：
# mingw-w64 没有 aarch64-w64-mingw32 目标（见文件头的依赖说明）。
# 这里刻意不走 codec_setup：视频/Opus/AEC 的静态库还没有 windows-arm64 版本，
# 探测只会打印对该目标无效的提示（"跑 build-video-libs.sh 生成"），所以固定
# 基础版；CGO 路径必须清空，避免沿用上一个目标注入的 -I/-L。
build_windows_arm64() {
    if [ -z "$ARM64_CC" ]; then
        step_label "跳过 $VAR_LABEL Windows arm64：未找到 llvm-mingw 的 aarch64-w64-mingw32-clang"
        echo "      安装（x86_64 宿主，解压即用、约 80 MB）：从"
        echo "        https://github.com/mstorsjo/llvm-mingw/releases 取"
        echo "        llvm-mingw-<版本>-ucrt-ubuntu-22.04-x86_64.tar.xz，然后："
        echo "          sudo tar -xf llvm-mingw-<版本>-ucrt-ubuntu-22.04-x86_64.tar.xz -C /opt"
        echo "          export PATH=/opt/llvm-mingw-<版本>-ucrt-ubuntu-22.04-x86_64/bin:\$PATH"
        echo "        （arm64 宿主把 ubuntu-22.04-x86_64 换成 ubuntu-22.04-aarch64）"
        echo "        或用 LLVM_MINGW=<解压目录> 指定（目录下应有 bin/aarch64-w64-mingw32-clang）"
        echo "      说明：GCC 系 mingw-w64 没有 aarch64-w64-mingw32 目标，必须用 llvm-mingw；"
        echo "            该产物为基础版（G.711/G.722，无视频/Opus/AEC，静态库暂无 arm64 版）"
        return 0
    fi
    BUILD_TAGS=""
    if [ -n "$VAR_TAGS_EXTRA" ]; then
        BUILD_TAGS="$VAR_TAGS_EXTRA"
    fi
    export CGO_CFLAGS=""
    export CGO_CXXFLAGS=""
    export CGO_LDFLAGS=""
    step_label "交叉构建 $VAR_LABEL $(out_name windows arm64 .exe)（CGO+音频，无视频/无Opus/无AEC）..."
    echo "      工具链：llvm-mingw，CC=${ARM64_CC}"
    echo "      说明：尚无 arm64 版的视频/Opus/AEC 静态库，此处为基础版（G.711/G.722）"
    CGO_ENABLED=1 GOOS=windows GOARCH=arm64 CC="$ARM64_CC" CXX="$ARM64_CXX" \
        go build $(tags_args) -ldflags "$LDFLAGS -H=windowsgui" \
        -o "$(out_name windows arm64 .exe)" ./cmd/sip-phone
}

# ---- 按变体跑一遍全部目标 ----

variant_setup full
build_linux_native
build_linux_cross
build_windows_amd64
build_windows_386
build_windows_arm64

variant_setup noui
build_linux_native
build_linux_cross
build_windows_amd64
build_windows_386
build_windows_arm64

echo "构建完成：dist/（版本 ${APP_VERSION}）"
ls -la dist/
