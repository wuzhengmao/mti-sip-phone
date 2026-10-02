#!/usr/bin/env bash
# 允许用 sh(dash) 调用（部分用户习惯 `sh scripts/xxx.sh`）：本脚本使用了 bash
# 专有语法（数组、${var//a/b}），dash 直接跑会在解析到那些行时报
# Syntax error: "(" unexpected。dash 是逐条执行顶层命令的，所以这条守卫会在
# 读到后面的 bash 语法之前先 exec 切回 bash。
if [ -z "${BASH_VERSION:-}" ]; then
    exec bash "$0" "$@"
fi
# 视频编解码静态库构建脚本
#
# 产出三个静态库到 third_party/video/<target>/{include,lib}：
#   - FFmpeg   只做解码（h264/h263/vp8/vp9 原生解码器）+ swscale + h263 编码
#   - libvpx   VP8/VP9 编码（Go 侧 cgo 直接绑定）
#   - OpenH264 H264 编码（Go 侧 cgo 直接绑定）
#
# 关键设计：FFmpeg 只解码，所以 configure 不需要 --enable-libvpx /
# --enable-libopenh264，从而**完全不需要 pkg-config**（WinLibs 工具链里没有它）。
#
# 用法：
#   ./scripts/build-video-libs.sh                    # 只构建本机原生目标
#   ./scripts/build-video-libs.sh windows-amd64      # 指定目标
#   ./scripts/build-video-libs.sh all                # 全部目标（缺工具链的自动跳过）
#   ./scripts/build-video-libs.sh --clean windows-amd64
#
# 注意：不带参数时**只**构建本机原生目标。build.sh 交叉产出的
# windows-amd64 / windows-386 / linux-<对侧arch> 想要带视频，必须先用
# `all`（或逐个指定）生成对应目标的库；没有对应库时那些产物会静默降级为
# 无视频版本（build.sh 只打印一行提示）。
#
# 目标：windows-amd64 / windows-386 / linux-amd64 / linux-arm64
# 产物不入库（.gitignore 已忽略 third_party/video 与 third_party/src）。
set -e

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

FFMPEG_VER="8.1"
VPX_VER="1.15.0"
OPENH264_VER="2.5.0"

SRC_DIR="$ROOT/third_party/src"
OUT_DIR="$ROOT/third_party/video"

CLEAN=0
FORCE=0
TARGETS=""
while [ $# -gt 0 ]; do
    case "$1" in
        --clean) CLEAN=1 ;;
        --force) FORCE=1 ;;
        --help|-h) sed -n '2,20p' "$0"; exit 0 ;;
        -*) echo "未知选项: $1" >&2; exit 2 ;;
        *) TARGETS="$TARGETS $1" ;;
    esac
    shift
done

# ---------- 宿主与工具 ----------

case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) HOST_OS=windows ;;
    Linux)                HOST_OS=linux ;;
    *) echo "不支持的宿主系统：$(uname -s)（仅支持 Linux 与 Windows/git-bash）" >&2; exit 1 ;;
esac

# 当前目录的绝对路径。Windows 宿主下必须给 **D:/... 形式**：git-bash 的
# pwd 返回 /d/... 形式，而 mingw32-make / gcc 是原生 Windows 程序，
# 会把 /d/git 当成「当前盘符根目录下的 d\git」——libvpx/FFmpeg 的
# configure 会把 $(pwd) 写进生成的 makefile，直接用会报
# "No such file or directory: /d/git/.../libs.mk"。
abspath() {
    if [ "$HOST_OS" = windows ]; then pwd -W; else pwd; fi
}

MAKE="$(command -v make || command -v mingw32-make || true)"
if [ -z "$MAKE" ]; then
    echo "找不到 make / mingw32-make" >&2
    echo "  Windows：WinLibs 自带 mingw32-make，确认 mingw64/bin 在 PATH 里" >&2
    exit 1
fi

if command -v nproc >/dev/null 2>&1; then
    JOBS="$(nproc)"
else
    JOBS="${NUMBER_OF_PROCESSORS:-4}"
fi

TARGETS_DEFAULTED=0
if [ -z "$TARGETS" ]; then
    TARGETS_DEFAULTED=1
    case "$HOST_OS" in
        windows) TARGETS="windows-amd64" ;;
        linux)   TARGETS="linux-$(go env GOARCH 2>/dev/null || echo amd64)" ;;
    esac
fi
if [ "$TARGETS" = " all" ]; then
    TARGETS=" windows-amd64 windows-386 linux-amd64 linux-arm64"
fi

# 为每个目标设置工具链与 FFmpeg configure 参数。
# 结果写入 CC/CXX/AR/NM/RANLIB/STRIP/WINDRES 与 FF_*、VPX_TARGET、OH_OS/OH_ARCH。
setup_toolchain() {
    local t="$1"
    CC=""; CXX=""; AR=""; NM=""; RANLIB=""; STRIP=""; WINDRES=""
    FF_EXTRA=""; VPX_TARGET=""; OH_OS=""; OH_ARCH=""; OH_CROSS=""

    local mingw64 mingw32 cross64 cross32
    mingw64="$(command -v x86_64-w64-mingw32-gcc || true)"
    mingw32="$(command -v i686-w64-mingw32-gcc || true)"
    cross64="$(command -v aarch64-linux-gnu-gcc || true)"
    cross32="$(command -v x86_64-linux-gnu-gcc || true)"

    case "$t" in
        windows-amd64)
            if [ "$HOST_OS" = windows ]; then
                CC="$(command -v gcc)"; CXX="$(command -v g++)"
                FF_EXTRA="--target-os=mingw32 --arch=x86_64"
            elif [ -n "$mingw64" ]; then
                CC="$mingw64"; CXX="${mingw64%gcc}g++"
                FF_EXTRA="--target-os=mingw32 --arch=x86_64 --cross-prefix=x86_64-w64-mingw32-"
            else
                echo "  缺少 x86_64-w64-mingw32-gcc（apt install gcc-mingw-w64-x86-64）" >&2
                return 1
            fi
            VPX_TARGET="x86_64-win64-gcc"; OH_OS="mingw_nt"; OH_ARCH="x86_64"
            ;;
        windows-386)
            if [ -z "$mingw32" ] && [ "$HOST_OS" = windows ]; then
                # Windows 宿主上工具链通常不在 PATH 里。按 build.bat 同样的
                # 两个位置探测：MSYS2 mingw32、WinLibs i686（解压即用）。
                # %USERPROFILE% 是 "C:\Users\xxx" 形式，反斜杠传给 make/sh 会被
                # 当转义吃掉（变成 "C:Usersxxx"），统一换成正斜杠
                local up="${USERPROFILE:-$HOME}"
                up="${up//\\//}"
                local cand
                for cand in \
                    "C:/msys64/mingw32/bin/i686-w64-mingw32-gcc.exe" \
                    "$up/tools/winlibs-i686/mingw32/bin/i686-w64-mingw32-gcc.exe"
                do
                    if [ -f "$cand" ]; then
                        mingw32="$cand"
                        # 该工具链自带的 nasm/yasm/mingw32-make 也要能找到
                        PATH="$(dirname "$cand"):$PATH"
                        export PATH
                        break
                    fi
                done
            fi
            if [ -n "$mingw32" ]; then
                # 去掉 .exe 后缀再换 g++（Windows 上探测到的是完整路径）
                local base="${mingw32%.exe}"
                CC="$mingw32"; CXX="${base%gcc}g++"
                # --disable-x86asm：**32 位目标必须关掉 x86 汇编**。
                # 不关的话 H.264 解码会在 FFmpeg 的 AVX 去块滤波里直接崩：
                #   Thread 1 received signal SIGSEGV
                #   ff_deblock_v_luma_8_avx () ← loop_filter ← decode_slice
                #   ← h264_decode_frame ← avcodec_send_packet
                # （gdb 实测，windows-386 库 + i686-w64-mingw32；amd64 无此问题）
                # 症状是"32 位版视频呼叫一接通就闪退"，而音频/信令全正常。
                # 关掉后解码走 C 回退，速度略慢但 386 本就是兼容目标，可接受。
                FF_EXTRA="--target-os=mingw32 --arch=x86 --cross-prefix=i686-w64-mingw32- --disable-x86asm"
            else
                echo "  缺少 i686-w64-mingw32-gcc" >&2
                echo "    Linux:  apt install gcc-mingw-w64-i686" >&2
                echo "    Windows: MSYS2 pacman -S mingw-w64-i686-gcc，" >&2
                echo "             或把 WinLibs i686 解压到 %USERPROFILE%\\tools\\winlibs-i686" >&2
                return 1
            fi
            VPX_TARGET="x86-win32-gcc"; OH_OS="mingw_nt"; OH_ARCH="i686"
            ;;
        linux-amd64)
            if [ "$HOST_OS" = linux ] && [ "$(go env GOARCH 2>/dev/null)" = amd64 ]; then
                CC="$(command -v gcc)"; CXX="$(command -v g++)"
            elif [ -n "$cross32" ]; then
                CC="$cross32"; CXX="${cross32%gcc}g++"
                # --target-os 同 linux-arm64 分支：有 --cross-prefix 就必须给
                # --arch 与 --target-os（否则 configure 直接 die）
                FF_EXTRA="--target-os=linux --arch=x86_64 --cross-prefix=x86_64-linux-gnu-"
            else
                echo "  缺少 x86_64-linux-gnu-gcc（apt install gcc-x86-64-linux-gnu）" >&2
                return 1
            fi
            VPX_TARGET="x86_64-linux-gcc"; OH_OS="linux"; OH_ARCH="x86_64"
            ;;
        linux-arm64)
            if [ "$HOST_OS" = linux ] && [ "$(go env GOARCH 2>/dev/null)" = arm64 ]; then
                CC="$(command -v gcc)"; CXX="$(command -v g++)"
            elif [ -n "$cross64" ]; then
                CC="$cross64"; CXX="${cross64%gcc}g++"
                # --target-os 不能省：configure 只要见到 --cross-prefix 就要求
                # --arch 与 --target-os **同时**给出，否则直接 die
                # "Must specify target arch (--arch) and OS (--target-os) when
                # cross-compiling"（见 ffmpeg/configure 的 cross_prefix 检查）。
                FF_EXTRA="--target-os=linux --arch=aarch64 --cross-prefix=aarch64-linux-gnu-"
            else
                echo "  缺少 aarch64-linux-gnu-gcc（apt install gcc-aarch64-linux-gnu）" >&2
                return 1
            fi
            VPX_TARGET="arm64-linux-gcc"; OH_OS="linux"; OH_ARCH="aarch64"
            ;;
        *) echo "未知目标: $t" >&2; return 1 ;;
    esac

    # 交叉工具链的 ar/ranlib/strip：从 CC 剥掉编译器名得到前缀。
    # 注意 Windows 上 CC 可能是 ".../i686-w64-mingw32-gcc.exe"（带盘符与 .exe），
    # 且这些工具在后缀 .exe 下，探测要两种写法都试。
    local pfx="$CC"
    pfx="${pfx%.exe}"
    pfx="${pfx%g++}"
    pfx="${pfx%gcc}"
    if [ "$pfx" = "$CC" ] || [ -z "$pfx" ]; then
        pfx="" # 没能剥出前缀（CC 就是 gcc），交给 PATH
    elif ! command -v "${pfx}ar" >/dev/null 2>&1 && ! command -v "${pfx}ar.exe" >/dev/null 2>&1; then
        pfx="" # 前缀下没有 ar，说明这不是交叉工具链
    fi
    if [ -n "$pfx" ]; then
        # Windows 下这些工具带 .exe 后缀，两种写法都试
        AR="$(command -v "${pfx}ar" || command -v "${pfx}ar.exe" || true)"
        NM="$(command -v "${pfx}nm" || command -v "${pfx}nm.exe" || true)"
        RANLIB="$(command -v "${pfx}ranlib" || command -v "${pfx}ranlib.exe" || true)"
        STRIP="$(command -v "${pfx}strip" || command -v "${pfx}strip.exe" || true)"
        WINDRES="$(command -v "${pfx}windres" || command -v "${pfx}windres.exe" || true)"
    fi
    [ -n "$AR" ] || AR="$(command -v ar || true)"
    [ -n "$NM" ] || NM="$(command -v nm || true)"
    [ -n "$RANLIB" ] || RANLIB="$(command -v ranlib || true)"
    [ -n "$STRIP" ] || STRIP="$(command -v strip || true)"
    [ -n "$WINDRES" ] || WINDRES="$(command -v windres || true)"

    # OpenH264 是 C++ 实现，构建必须有与 CC 配套的 C++ 编译器（CXX）。Debian 上
    # 交叉工具链的 g++ 常与 gcc 分属两个包（gcc-aarch64-linux-gnu 不含 g++），
    # 只装 gcc 时要在 openh264 的 make 阶段才失败，错误很难对号入座，这里提前报。
    # （Windows 宿主下 CXX 可能是不带 .exe 的路径，探测要两种写法都试。）
    cxx_ok=0
    if [ -n "$CXX" ] &&
       { command -v "$CXX" >/dev/null 2>&1 || command -v "$CXX.exe" >/dev/null 2>&1; }; then
        cxx_ok=1
    fi
    if [ "$cxx_ok" != 1 ]; then
        echo "  缺少 C++ 编译器（OpenH264 是 C++，需要与 CC=「${CC:-未找到}」配套的 CXX）" >&2
        echo "    Debian/Ubuntu：本机 sudo apt install g++；交叉工具链的 g++ 常是独立包，" >&2
        echo "    如 g++-aarch64-linux-gnu / g++-x86-64-linux-gnu / g++-mingw-w64-x86-64" >&2
        return 1
    fi

    # libvpx 的 configure 只认环境变量（CC/CXX/AR/LD/AS/STRIP），没有 --cc= 之类的选项；
    # FFmpeg 也会优先读环境变量。统一导出，两个库共用。
    export CC CXX AR NM RANLIB STRIP WINDRES
    export LD="$CC"
    # 汇编器：x86 目标用 nasm/yasm（下面按目标再决定）；arm64 用 gcc 内置
    if [ "$VPX_TARGET" = "arm64-linux-gcc" ]; then
        export AS="$CC"
    fi
    return 0
}

# ---------- 源码下载 ----------

fetch_src() {
    mkdir -p "$SRC_DIR"
    local name="$1" url="$2" dir="$3"
    if [ -d "$SRC_DIR/$dir" ] && [ ! -f "$SRC_DIR/$dir/.stamp" ]; then
        echo "  源码已存在：$dir"
    fi
    if [ ! -d "$SRC_DIR/$dir" ]; then
        echo "  下载 $name ..."
        local arc="$SRC_DIR/$name"
        curl -fsSL --retry 3 -m 900 -o "$arc" "$url"
        tar -xf "$arc" -C "$SRC_DIR"
        rm -f "$arc"
    fi
    touch "$SRC_DIR/$dir/.stamp"
}

# ---------- 各库构建 ----------

# 清掉源码树里遗留的目标文件与静态库。
#
# **为什么必须显式删**：四个目标共用同一份源码树，切换目标时只靠
# `make clean` 并不保险——上一轮的目标文件若残留且时间戳比源码新，
# make 会认为无需重编直接跳过，于是「32 位库」里装进 64 位目标文件。
# 症状极具迷惑性：configure 显示 target 正确、编译无报错，只有链接时
# 报一堆 undefined reference（符号修饰/位宽不同）。
purge_objects() {
    find "$1" -name '*.o' -delete 2>/dev/null || true
    find "$1" -name '*.a' -delete 2>/dev/null || true
    find "$1" -name '*.d' -delete 2>/dev/null || true
    # libvpx 的 *_rtcd.h 由 rtcd.pl 按目标架构生成，函数名表随架构变化
    # （如 fwd_txfm_ssse3_x86_64.asm 只在 x86_64 下存在）。不删就会沿用
    # 上一目标的表，链接时报一堆 _ssse3 未定义——本次 windows-386 就踩了这个。
    find "$1" -name '*_rtcd.h' -delete 2>/dev/null || true
}

build_libvpx() {
    local pfx="$1"
    echo "  [libvpx] configure ... (CC=$CC AR=$AR AS=${AS:-默认})"
    (
        cd "$SRC_DIR/libvpx-$VPX_VER"
        local here; here="$(abspath)"
        purge_objects .
        [ -f Makefile ] && "$MAKE" clean >/dev/null 2>&1 || true
        local cfg=(--target="$VPX_TARGET" --prefix="$pfx"
                   --disable-shared --enable-static
                   --disable-examples --disable-tools --disable-docs --disable-unit-tests)
        # 仅 x86 目标需要外部汇编器；arm64 走 gcc 内置汇编。
        #
        # x86 目标是**硬性要求** nasm/yasm，不能降级：libvpx 1.15 的 configure
        # 只认 --as={yasm|nasm|auto}，没有 --disable-asm（实测报
        # Unknown option "--disable-asm"）；且 x86 下 .asm 源文件是必编的，
        # 汇编器缺失时 build/make/configure.sh 直接 die
        # "Neither yasm nor nasm have been found."。
        # 所以这里只能缺啥报错，并把安装办法打出来。
        case "$VPX_TARGET" in
            *win64-gcc|*win32-gcc|x86_64-linux-gcc)
                if command -v nasm >/dev/null 2>&1; then
                    cfg+=(--as=nasm)
                elif command -v yasm >/dev/null 2>&1; then
                    cfg+=(--as=yasm)
                else
                    echo "  错误：libvpx 的 x86 目标必须用 nasm 或 yasm 汇编（libvpx 无 --disable-asm 选项）" >&2
                    echo "    Debian/Ubuntu:  sudo apt install nasm" >&2
                    echo "    Fedora/RHEL:    sudo dnf install nasm" >&2
                    echo "    Windows 宿主:   MSYS2 pacman -S nasm，或把 nasm.exe 放进 PATH" >&2
                    return 1
                fi
                ;;
        esac
        # CC/CXX/AR/LD 由 setup_toolchain 导出到环境。
        # 必须用 Windows 形式的绝对路径调用 configure（见 abspath 注释）。
        "$here/configure" "${cfg[@]}"
        "$MAKE" -j"$JOBS" >/dev/null
        "$MAKE" install >/dev/null
    )
}

build_openh264() {
    local pfx="$1"
    echo "  [openh264] make ..."
    (
        cd "$SRC_DIR/openh264-$OPENH264_VER"
        purge_objects .
        "$MAKE" clean >/dev/null 2>&1 || true
        "$MAKE" -j"$JOBS" \
            OS="$OH_OS" ARCH="$OH_ARCH" \
            PREFIX="$pfx" INCLUDE_PREFIX="$pfx/include" LIBDIR_NAME=lib \
            CC="$CC" CXX="$CXX" AR="$AR" \
            BUILDTYPE=Release DEBUGSYMBOLS=False \
            install-static >/dev/null
    )
}

build_ffmpeg() {
    local pfx="$1"
    echo "  [ffmpeg] configure ..."
    (
        cd "$SRC_DIR/ffmpeg-$FFMPEG_VER"
        local here; here="$(abspath)"
        # FFmpeg 的 configure 会在 $TMPDIR 里建临时文件并尝试执行（编译探针）。
        # Windows 宿主下 TMPDIR 若继承自 bash 会是 /d/... 形式，被 MSYS 路径转换
        # 吃掉斜杠变成 D:cachetemp —— 必须显式指向 Windows 形式的目录。
        local tmpdir="$SRC_DIR/.fftmp"
        mkdir -p "$tmpdir"
        export TMPDIR="$(cd "$tmpdir" && abspath)"
        purge_objects .
        "$MAKE" distclean >/dev/null 2>&1 || true
        # --disable-everything 关掉全部组件，所以后三项与每个 decoder/parser 都要显式列出。
        # 只留解码器 + h263 编码器 + swscale/avcodec/avutil，不启用任何外部库，因此不
        # 依赖 pkg-config（WinLibs 工具链里没有它）。
        # --disable-autodetect：autodetect 类外部库**不受 --disable-everything 约束**，
        # 会按构建机装了哪些开发包自动启用——踩过的是 libdrm（Linux 宿主装了 libdrm-dev
        # 就把 hwcontext_drm.o 编进 libavutil.a，链接报 undefined reference to
        # `drmGetVersion'；Windows 宿主没有 pkg-config 所以从不触发）。同类还有
        # zlib/bzlib/lzma/xlib/vaapi/vdpau/vulkan/cuda/nvdec 等，交叉 mingw/arm64 时
        # 宿主 pkg-config 尤其容易误判。我们只用软件解码，全关不影响任何启用的组件；
        # 线程（pthreads/w32threads）不在 AUTODETECT 之列，不受影响。
        "$here/configure" \
            --prefix="$pfx" \
            $FF_EXTRA \
            --enable-static --disable-shared \
            --disable-programs --disable-doc --disable-network \
            --disable-autodetect \
            --disable-everything \
            --enable-avcodec --enable-avutil --enable-swscale \
            --enable-decoder=h264,h263,h263p,h263i,vp8,vp9 \
            --enable-encoder=h263,h263p \
            --enable-parser=h264,h263,vp8,vp9,mpeg4video \
            --enable-protocol=file \
            $( [ -n "$CC" ] && echo --cc="$CC" ) \
            $( [ -n "$CXX" ] && echo --cxx="$CXX" ) \
            $( [ -n "$AR" ] && echo --ar="$AR" ) \
            $( [ -n "$RANLIB" ] && echo --ranlib="$RANLIB" ) \
            $( [ -n "$STRIP" ] && echo --strip="$STRIP" ) \
            $( [ -n "$NM" ] && echo --nm="$NM" ) \
            $( [ -n "$WINDRES" ] && echo --windres="$WINDRES" )
        "$MAKE" -j"$JOBS" >/dev/null
        "$MAKE" install >/dev/null
    )
}

# ---------- 主流程 ----------

echo "宿主：$HOST_OS   目标：$TARGETS"

if [ "$CLEAN" = 1 ]; then
    rm -rf "$OUT_DIR" "$SRC_DIR"
    echo "已清理 third_party/{src,video}"
fi

# 源码下载（一次，各目标共用）
fetch_src "ffmpeg-$FFMPEG_VER.tar.xz" "https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VER.tar.xz" "ffmpeg-$FFMPEG_VER"
fetch_src "libvpx-$VPX_VER.tar.gz"    "https://github.com/webmproject/libvpx/archive/refs/tags/v$VPX_VER.tar.gz" "libvpx-$VPX_VER"
fetch_src "openh264-$OPENH264_VER.tar.gz" "https://github.com/cisco/openh264/archive/refs/tags/v$OPENH264_VER.tar.gz" "openh264-$OPENH264_VER"

FAILED=""
BUILT=""
for T in $TARGETS; do
    echo
    echo "=== 构建 $T ==="
    if ! setup_toolchain "$T"; then
        FAILED="$FAILED $T"
        continue
    fi
    PFX="$OUT_DIR/$T"
    rm -rf "$PFX"
    mkdir -p "$PFX/lib" "$PFX/include"
    # 传给各库 configure/make 的前缀：Windows 宿主下必须是 D:/... 形式
    PFX_MK="$(cd "$PFX" && abspath)"

    # 已构建过的库跳过（迭代调试时省时间）；--force 强制重建
    built_or_run() {
        local lib="$1"; shift
        if [ "$FORCE" != 1 ] && [ -f "$PFX/lib/$lib" ]; then
            echo "  跳过 $lib（已存在；--force 可重建）"
            return 0
        fi
        "$@"
    }
    # 失败时把目标目录整个删掉：只建了一半的 third_party/video/<target> 会被
    # build.sh / build.bat 的探测当成「库齐全」，加上 -tags video 后编译立刻
    # 报缺头文件（如 wels/codec_api.h），现场与真正的库构建失败对不上号。
    if ! built_or_run libvpx.a    build_libvpx "$PFX_MK";   then echo "  libvpx 构建失败";   FAILED="$FAILED $T"; rm -rf "$PFX"; continue; fi
    if ! built_or_run libopenh264.a build_openh264 "$PFX_MK"; then echo "  openh264 构建失败"; FAILED="$FAILED $T"; rm -rf "$PFX"; continue; fi
    if ! built_or_run libavcodec.a build_ffmpeg "$PFX_MK";   then echo "  ffmpeg 构建失败";   FAILED="$FAILED $T"; rm -rf "$PFX"; continue; fi

    # 合并三个库的头文件，便于 cgo 用单一的 -I
    for inc in vpx openh264 wels libavcodec libavutil libswscale; do
        [ -d "$PFX/include/$inc" ] || { [ -d "$PFX/../$inc" ] && cp -r "$PFX/../$inc" "$PFX/include/" 2>/dev/null; }
    done

    echo "  完成：$PFX"
    ls -la "$PFX/lib" | awk '{print "    " $5 "  " $9}' | grep -E '\.a$' || true
    BUILT="$BUILT $T"
done

echo
echo "构建完成：${BUILT:- （无）}"
[ -n "$FAILED" ] && echo "跳过（缺工具链）：$FAILED"
if [ "$TARGETS_DEFAULTED" = 1 ]; then
    echo
    echo "提示：本次只构建了本机原生目标（${TARGETS# }）。build.sh 交叉产出的"
    echo "      windows-amd64 / windows-386 / linux-<对侧arch> 想带视频，须先执行："
    echo "      sh scripts/build-video-libs.sh all"
fi
echo
echo "下一步：build.bat / build.sh 会自动探测 third_party/video/<target> 并加上 -tags video。"
