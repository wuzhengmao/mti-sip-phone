#!/usr/bin/env bash
# 允许用 sh(dash) 调用（部分用户习惯 `sh scripts/xxx.sh`）：本脚本使用了 bash
# 专有语法（数组、${var//a/b}），dash 直接跑会在解析到那些行时报
# Syntax error: "(" unexpected。dash 是逐条执行顶层命令的，所以这条守卫会在
# 读到后面的 bash 语法之前先 exec 切回 bash。
if [ -z "${BASH_VERSION:-}" ]; then
    exec bash "$0" "$@"
fi
# 音频第三方库构建脚本（音频库的统一入口）
#
# 当前产出（都装到 third_party/audio/<target>/{include,lib}）：
#   1. libopus —— Opus 编解码，cgo 直接绑定；
#   2. webrtc-audio-processing 1.3 + abseil —— 音频预处理（AEC 回声消除 /
#      NS 降噪 / AGC 自动增益，REQ-MEDIA-007）。它只有 C++ API，Go 侧走一层
#      极薄的 C++ 桥接；构建要用 meson + cmake + ninja + pkgconf（Windows 的
#      WinLibs 工具链里都有，另需 pip 装 meson），并打三个随仓库携带的补丁
#      （scripts/patches/webrtc-audio-processing-1.3/，来源见文件名注释）。
#
# 以后新增音频库里按同样模式加到这里与同一个目录，构建脚本（build.sh /
# build.bat）只需按新的 .a 追加探测。
#
# libopus 的构建很轻（无需 nasm/pkg-config，几十秒一个目标），交叉编译只需
# 目标平台的 gcc：x86 的 SIMD 走编译器 intrinsics，不依赖外部汇编器。
#
# 用法：
#   ./scripts/build-audio-libs.sh                    # 只构建本机原生目标
#   ./scripts/build-audio-libs.sh windows-amd64      # 指定目标
#   ./scripts/build-audio-libs.sh all                # 全部目标（缺工具链的自动跳过）
#   ./scripts/build-audio-libs.sh --clean windows-amd64
#   ./scripts/build-audio-libs.sh --no-apm all      # 只编 libopus
#   ./scripts/build-audio-libs.sh --no-opus all     # 只编 APM（含 abseil）
#
# 注意：不带参数时**只**构建本机原生目标。build.sh / build.bat 交叉产出的
# windows-amd64 / windows-386 / linux-<对侧arch> 想要带 Opus，必须先用
# `all`（或逐个指定）生成对应目标的库；没有对应库时那些产物会降级为
# **不含 Opus** 的版本（构建脚本只打印一行提示）。
#
# 目标：windows-amd64 / windows-386 / linux-amd64 / linux-arm64
# 产物不入库（.gitignore 已忽略 third_party/audio 与 third_party/src）。
set -e

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

OPUS_VER="1.5.2"

# webrtc-audio-processing（AEC/NS/AGC）与其唯一第三方依赖 abseil-cpp
WAP_VER="1.3"
ABSL_VER="20250814.2"
ABSL_DIR="abseil-cpp-$ABSL_VER"
WAP_DIR="webrtc-audio-processing-v$WAP_VER"

SRC_DIR="$ROOT/third_party/src"
OUT_DIR="$ROOT/third_party/audio"

CLEAN=0
BUILD_OPUS=1
BUILD_APM=1
TARGETS=""
while [ $# -gt 0 ]; do
    case "$1" in
        --clean) CLEAN=1 ;;
        --no-apm) BUILD_APM=0 ;;
        --no-opus) BUILD_OPUS=0 ;;
        --help|-h) sed -n '2,25p' "$0"; exit 0 ;;
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
# pwd 返回 /d/... 形式，而 configure/make 调的是原生 Windows 程序，
# 会把 /d/git 当成「当前盘符根目录下的 d\git」。
abspath() {
    if [ "$HOST_OS" = windows ]; then pwd -W; else pwd; fi
}

MAKE="$(command -v make || command -v mingw32-make || true)"
if [ -z "$MAKE" ]; then
    echo "找不到 make / mingw32-make" >&2
    echo "  Windows：WinLibs 自带 mingw32-make，确认 mingw64/bin 在 PATH 里" >&2
    exit 1
fi
# 传给 configure 的 MAKE 用**裸命令名**：libopus 的 Makefile 递归调用 $(MAKE)，
# Windows 上递归是 CreateProcess 按 **Windows PATH** 查程序名，而 git-bash 里
# `make` 只是个 shell 层的解析（PATH 里实际只有 mingw32-make.exe）——直接用
# `make` 会报 `CreateProcess(NULL, make all-recursive, ...) failed`（实测）。
MAKE_CMD="$(basename "$MAKE")"

# Windows 宿主下还要给 make 传一个**无空格的 SHELL**：automake 的 libtool 规则是
# `LIBTOOL = $(SHELL) $(top_builddir)/libtool`，mingw32-make 会把 $(SHELL) 原样
# 拼进配方行；而 git-bash 的 sh.exe 在 `C:\Program Files\Git\usr\bin\`——空格把
# 命令行切碎，编译时报 `sh: line 1: C:/Program: No such file or directory`
# （实测；视频那三个库不用 automake，所以从没暴露）。用 8.3 短路径消除空格，
# 命令行赋值 SHELL= 的优先级高于 Makefile 里的 `SHELL = /bin/sh`。
MAKE_SHELL=""
if [ "$HOST_OS" = windows ]; then
    _sh="$(command -v sh || true)"
    if [ -n "$_sh" ] && command -v cygpath >/dev/null 2>&1; then
        MAKE_SHELL="$(cygpath -d -w "$_sh" 2>/dev/null || true)"
    fi
    case "$MAKE_SHELL" in
        *" "*|"")
            MAKE_SHELL=""
            echo "  提示：拿不到 sh.exe 的 8.3 短路径（Windows 目录名很长或 8.3 被禁用），" >&2
            echo "        make 可能因路径含空格而失败（报 C:/Program: No such file or directory）" >&2
            ;;
    esac
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

# 为每个目标设置工具链与 configure 的 --host。
# 结果写入 CC/AR/RANLIB/STRIP 与 HOST_TRIPLE（空 = 本机构建，不传 --host）。
setup_toolchain() {
    local t="$1"
    CC=""; AR=""; RANLIB=""; STRIP=""; HOST_TRIPLE=""

    local mingw64 mingw32 cross64 cross32
    mingw64="$(command -v x86_64-w64-mingw32-gcc || true)"
    mingw32="$(command -v i686-w64-mingw32-gcc || true)"
    cross64="$(command -v aarch64-linux-gnu-gcc || true)"
    cross32="$(command -v x86_64-linux-gnu-gcc || true)"

    case "$t" in
        windows-amd64)
            if [ "$HOST_OS" = windows ]; then
                CC="$(command -v gcc)"
            elif [ -n "$mingw64" ]; then
                CC="$mingw64"
            else
                echo "  缺少 x86_64-w64-mingw32-gcc（apt install gcc-mingw-w64-x86-64）" >&2
                return 1
            fi
            HOST_TRIPLE="x86_64-w64-mingw32"
            ;;
        windows-386)
            if [ -z "$mingw32" ] && [ "$HOST_OS" = windows ]; then
                # Windows 宿主上工具链通常不在 PATH 里。按 build.bat 同样的
                # 两个位置探测：MSYS2 mingw32、WinLibs i686（解压即用）。
                # %USERPROFILE% 是 "C:\Users\xxx" 形式，反斜杠传给 make/sh 会被
                # 当转义吃掉，统一换成正斜杠。
                local up="${USERPROFILE:-$HOME}"
                up="${up//\\//}"
                local cand
                for cand in \
                    "C:/msys64/mingw32/bin/i686-w64-mingw32-gcc.exe" \
                    "$up/tools/winlibs-i686/mingw32/bin/i686-w64-mingw32-gcc.exe" \
                    "$up/tools/winlibs-i686/mingw32/bin/gcc.exe"
                do
                    if [ -f "$cand" ]; then
                        mingw32="$cand"
                        PATH="$(dirname "$cand"):$PATH"
                        export PATH
                        break
                    fi
                done
            fi
            if [ -z "$mingw32" ]; then
                echo "  缺少 i686-w64-mingw32-gcc" >&2
                echo "    Linux:  apt install gcc-mingw-w64-i686" >&2
                echo "    Windows: MSYS2 pacman -S mingw-w64-i686-gcc，" >&2
                echo "             或把 WinLibs i686 解压到 %USERPROFILE%\\tools\\winlibs-i686" >&2
                return 1
            fi
            CC="$mingw32"
            HOST_TRIPLE="i686-w64-mingw32"
            ;;
        linux-amd64)
            if [ "$HOST_OS" = linux ] && [ "$(go env GOARCH 2>/dev/null)" = amd64 ]; then
                CC="$(command -v gcc)"
            elif [ -n "$cross32" ]; then
                CC="$cross32"; HOST_TRIPLE="x86_64-linux-gnu"
            else
                echo "  缺少 x86_64-linux-gnu-gcc（apt install gcc-x86-64-linux-gnu）" >&2
                return 1
            fi
            ;;
        linux-arm64)
            if [ "$HOST_OS" = linux ] && [ "$(go env GOARCH 2>/dev/null)" = arm64 ]; then
                CC="$(command -v gcc)"
            elif [ -n "$cross64" ]; then
                CC="$cross64"; HOST_TRIPLE="aarch64-linux-gnu"
            else
                echo "  缺少 aarch64-linux-gnu-gcc（apt install gcc-aarch64-linux-gnu）" >&2
                return 1
            fi
            ;;
        *) echo "未知目标: $t" >&2; return 1 ;;
    esac

    # 交叉工具链的 ar/ranlib/strip：从 CC 剥掉编译器名得到前缀。
    # Windows 上 CC 可能是 ".../i686-w64-mingw32-gcc.exe"（带盘符与 .exe），
    # 这些工具也带 .exe 后缀，探测要两种写法都试。
    local pfx="$CC"
    pfx="${pfx%.exe}"
    pfx="${pfx%g++}"
    pfx="${pfx%gcc}"
    if [ "$pfx" = "$CC" ] || [ -z "$pfx" ]; then
        pfx=""
    elif ! command -v "${pfx}ar" >/dev/null 2>&1 && ! command -v "${pfx}ar.exe" >/dev/null 2>&1; then
        pfx=""
    fi
    if [ -n "$pfx" ]; then
        AR="$(command -v "${pfx}ar" || command -v "${pfx}ar.exe" || true)"
        RANLIB="$(command -v "${pfx}ranlib" || command -v "${pfx}ranlib.exe" || true)"
        STRIP="$(command -v "${pfx}strip" || command -v "${pfx}strip.exe" || true)"
    fi
    [ -n "$AR" ] || AR="$(command -v ar || true)"
    [ -n "$RANLIB" ] || RANLIB="$(command -v ranlib || true)"
    [ -n "$STRIP" ] || STRIP="$(command -v strip || true)"

    # C++ 编译器（abseil 与 webrtc-audio-processing 都要）：从 CC 换 gcc→g++ 得到，
    # 拿不到就退回 PATH 里的 g++。逐个候选按**真实文件**（-f）判定，
    # Windows 上必须拿到带 .exe 的完整路径（cmake 认死理，给不带 .exe 的会报
    # "is not a full path to an existing compiler tool"）。
    CXX=""
    local cand
    for cand in "${CC%gcc.exe}g++.exe" "${CC%gcc}g++.exe" "${CC%gcc}g++" "${CC%gcc.exe}g++"; do
        if [ -f "$cand" ]; then
            CXX="$cand"
            break
        fi
    done
    [ -n "$CXX" ] || CXX="$(command -v g++ || command -v g++.exe || true)"

    export CC CXX AR RANLIB STRIP

    # 额外编译参数：WebRTC 的 AEC3 在 x86 上**无条件**用 SSE 内建函数
    #（`_mm_storeu_ps` 等），而 i686 基线不带 SSE2，不补参数会报
    # "inlining failed in call to 'always_inline' ... target specific option mismatch"
    #（实测）。SSE2 自 2003 年起的 CPU 都有（Windows 8+ 更是硬性要求），
    # 所以 32 位目标直接开；-mstackrealign 是 ABI 保险：宿主（Go 的 i686 代码、
    # 其他不带 SSE 的库）不保证 16 字节栈对齐，开了它每个函数入口自行对齐。
    ARCH_FLAGS=""
    case "$t" in
        windows-386) ARCH_FLAGS="-msse2 -mstackrealign" ;;
    esac
    export ARCH_FLAGS
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

# ---------- libopus ----------

# 清掉源码树里遗留的目标文件与静态库：四个目标共用同一份源码树，上一轮的
# .o 若残留且时间戳比源码新，make 会跳过重编，于是「32 位库」里装进 64 位
# 目标文件——configure 显示目标正确、编译零报错，只在链接时炸 undefined
# reference（视频库那边踩过两次，同样的防御照搬过来）。
purge_objects() {
    find "$1" -name '*.o' -delete 2>/dev/null || true
    find "$1" -name '*.a' -delete 2>/dev/null || true
    find "$1" -name '*.lo' -delete 2>/dev/null || true
    find "$1" -name '*.la' -delete 2>/dev/null || true
    find "$1" -name '.libs' -type d -exec rm -rf {} + 2>/dev/null || true
}

build_opus() {
    local pfx="$1"
    echo "  [libopus] configure ... (CC=$CC host=${HOST_TRIPLE:-本机})"
    (
        cd "$SRC_DIR/opus-$OPUS_VER"
        purge_objects .
        [ -f Makefile ] && make distclean >/dev/null 2>&1 || true
        local host_arg=""
        [ -n "$HOST_TRIPLE" ] && host_arg="--host=$HOST_TRIPLE"

        # mingw 目标：预定义 _FORTIFY_SOURCE，阻止 libopus 自带的 AX_ADD_FORTIFY_SOURCE
        # 宏再加 -D_FORTIFY_SOURCE=2（该宏先测「是否已定义」，定义了就不加）。
        # 开了 fortify 之后 memcpy/memset 会被换成 __memcpy_chk/__memset_chk——那是
        # glibc 的加固符号，mingw 的 C 运行库不提供，而最终链接由 Go 驱动（不走 gcc
        # 的默认 spec，不会自动带 libssp），在 Debian/Ubuntu 的 mingw-w64 上直接报
        # `undefined reference to __memcpy_chk`（WinLibs 恰好能过，所以只在 Linux
        # 主机交叉 Windows 时暴露）。编解码库损失的加固收益可忽略，换取跨工具链可用。
        case "$HOST_TRIPLE" in
            *mingw*) export CFLAGS="-O2 -D_FORTIFY_SOURCE=0" ;;
        esac
        # --disable-extra-programs：不构建 opus_demo / test_opus_* 等程序，
        # 只要静态库；--disable-doc 跳过文档（需要 doxygen）。
        # --disable-dependency-tracking：automake 的自动依赖跟踪在 configure
        # 阶段要跑一次 make，Windows/git-bash 下必失败（见上面的 MAKE_CMD）；
        # 本脚本每次都清干净重建，不需要它。
        ./configure \
            --prefix="$pfx" \
            $host_arg \
            MAKE="$MAKE_CMD" \
            --disable-shared --enable-static \
            --disable-doc --disable-extra-programs \
            --disable-dependency-tracking
        if [ -n "$MAKE_SHELL" ]; then
            "$MAKE" -j"$JOBS" SHELL="$MAKE_SHELL"
            "$MAKE" install SHELL="$MAKE_SHELL"
        else
            "$MAKE" -j"$JOBS"
            "$MAKE" install
        fi
    )
    # 成功与否只看该有的文件在不在（头文件与静态库）
    [ -f "$pfx/lib/libopus.a" ] || { echo "  未生成 libopus.a" >&2; return 1; }
    [ -f "$pfx/include/opus/opus.h" ] || { echo "  未安装 opus.h" >&2; return 1; }
    return 0
}


# ---------- webrtc-audio-processing（AEC/NS/AGC，REQ-MEDIA-007）----------
#
# 依赖链：abseil-cpp（官方 GitHub releases）→ webrtc-audio-processing（freedesktop
# GitLab，v1.3，打三个随仓库携带的补丁）。两者都是 **out-of-tree** 构建
#（abseil 用 cmake+ninja，webrtc 用 meson+ninja），四个目标共用一份源码树也没有
# 残留目标文件的问题，不需要 opus 那样的 purge。
#
# 工具要求：cmake / ninja / pkgconf + meson。WinLibs 工具链自带前三样，meson 用
# `pip install meson` 装（Windows 上通常不在 PATH，所以统一走
# `python -m mesonbuild.mesonmain`）。WinLibs 里 pkg-config 叫 pkgconf.exe，而
# meson 按名字找 pkg-config，故这里造一个同名转发壳。

WAP_PATCH_DIR="$ROOT/scripts/patches/webrtc-audio-processing-$WAP_VER"

have_tool() { command -v "$1" >/dev/null 2>&1; }

# 交给**原生 Windows 程序**的路径要转成 C:/... 形式。
# git-bash 只对命令行参数做路径转换，**环境变量里的不管**——meson（原生 Python）
# 从 CC/CXX 环境变量取编译器时会拿到 `/c/Users/.../gcc`，一执行就是
# "[WinError 2] 系统找不到指定的文件"（实测踩到），所以这里显式转换。
# 补 .exe：git-bash 的 command -v 给的名字不带后缀，而 cmake/meson 要求
# "存在的文件路径"。已经带 .exe 的原样返回。
with_exe() {
    local p="$1"
    if [ -f "$p.exe" ]; then
        echo "$p.exe"
    else
        echo "$p"
    fi
}

to_native_path() {
    local p="$1"
    if [ -z "$p" ]; then
        echo ""
        return 0
    fi
    if [ "$HOST_OS" = windows ] && command -v cygpath >/dev/null 2>&1; then
        cygpath -m "$p"
    else
        echo "$p"
    fi
}

# 定位 meson：PATH 里的 meson，或 python -m mesonbuild.mesonmain。
MESON=""
detect_meson() {
    [ -n "$MESON" ] && return 0
    if have_tool meson; then
        MESON="meson"
        return 0
    fi
    local py
    for py in python python3 py; do
        if have_tool "$py" && "$py" -c "import mesonbuild.mesonmain" >/dev/null 2>&1; then
            MESON="$py -m mesonbuild.mesonmain"
            return 0
        fi
    done
    return 1
}

# 定位 pkg-config（把 pkgconf 转发成 pkg-config 的名字，供 meson 查找）。
PKGCONF_DIR=""
detect_pkgconfig() {
    have_tool pkg-config && return 0
    if have_tool pkgconf; then
        PKGCONF_DIR="$SRC_DIR/pkgconfig-shim"
        mkdir -p "$PKGCONF_DIR"
        local name="pkg-config"
        [ "$HOST_OS" = windows ] && name="pkg-config.exe"
        cp -f "$(command -v pkgconf)" "$PKGCONF_DIR/$name" 2>/dev/null || true
        [ -f "$PKGCONF_DIR/$name" ] && return 0
    fi
    return 1
}

# 交叉目标：为 meson 生成 cross file；为 cmake 准备 system/编译器参数。
# 结果写入 CROSS_FILE（meson）与 CMAKE_CROSS_ARGS（数组，cmake 用）。
#
# 判断口径是**目标 OS 与宿主 OS 是否相同**，而不是 HOST_TRIPLE 是否为空：
# windows-amd64 在 Windows 宿主机上、linux-amd64 在 Linux 宿主机上都是本机构建，
# 它们的 HOST_TRIPLE 同样非空（交叉工具链那段统一填的），若照着写 cross file，
# meson 会把本来"能直接跑"的编译器也按交叉的方式去找，连 /c/... 这种 MSYS 路径
# 都原样拿去执行（实测报 [WinError 2]）。
CROSS_FILE=""
CMAKE_CROSS_ARGS=()
# APM_SKIP_REASON 非空 = 该目标跳过 APM（附原因），主循环据此给出提示
APM_SKIP_REASON=""
setup_cross() {
    local t="$1"
    APM_SKIP_REASON=""
    CROSS_FILE=""
    CMAKE_CROSS_ARGS=()
    [ -z "$HOST_TRIPLE" ] && return 0

    # 真·本机构建（OS 与架构都与本机一致）才不写 cross file：
    # "同 OS 换架构"（Linux amd64 宿主编 arm64、Windows 宿主编 386）仍属交叉，
    # 不写的话 cmake/meson 会按本机处理，给目标代码带上宿主的架构假设。
    local host_arch t_os t_arch
    host_arch="$(go env GOARCH 2>/dev/null || echo unknown)"
    case "$t" in
        windows-amd64) t_os="windows"; t_arch="amd64" ;;
        windows-386)   t_os="windows"; t_arch="386" ;;
        linux-amd64)   t_os="linux";   t_arch="amd64" ;;
        linux-arm64)   t_os="linux";   t_arch="arm64" ;;
        *) return 0 ;;
    esac
    [ "$t_os" = "$HOST_OS" ] && [ "$t_arch" = "$host_arch" ] && return 0

    local sys cpu

    case "$HOST_TRIPLE" in
        x86_64-w64-mingw32) sys="windows"; cpu="x86_64" ;;
        i686-w64-mingw32)   sys="windows"; cpu="x86" ;;
        x86_64-linux-gnu)   sys="linux";   cpu="x86_64" ;;
        aarch64-linux-gnu)  sys="linux";   cpu="aarch64" ;;
        *) return 0 ;;
    esac

    local cc cxx
    cc="$(to_native_path "$(with_exe "$CC")")"
    cxx="$(to_native_path "$(with_exe "$CXX")")"
    # 线程模型：交叉构建里 CMake 的 FindThreads 探测不到（它要跑测试程序），
    # 于是既不给线程参数、又让 abseil 选到用 std::mutex 的实现——mingw 的两种
    # 变体要求还不一样（实测：Linux 交叉 windows-amd64 时 abseil 报
    # "error: 'mutex' is not a member of 'std'"）：
    #   - posix 变体：C++ 标准库的 mutex/thread 需要 -pthread 才可见；
    #   - win32 变体：根本没有 std::mutex，必须让 abseil 走 Win32 线程实现
    #     （CMAKE_USE_WIN32_THREADS_INIT）。
    # 所以先问编译器自己是哪种模型，再对症下参数。
    CMAKE_CROSS_ARGS=(
        "-DCMAKE_SYSTEM_NAME=$([ "$sys" = windows ] && echo Windows || echo Linux)"
        "-DCMAKE_C_COMPILER=$cc"
        "-DCMAKE_CXX_COMPILER=$cxx"
    )
    if [ "$sys" = windows ]; then
        local tm
        tm="$("$CXX" -v 2>&1 | grep -i 'thread model' | sed 's/.*: *//' | tr -d '
')"
        case "$tm" in
            win32)
                # win32 线程模型的 mingw **没有 std::mutex**（libstdc++ 里被
                # _GLIBCXX_HAS_GTHREADS 挡掉），而 abseil 的 synchronization 与
                # cctz 都要用它——不是加参数能绕过的，只能换 posix 变体。
                # 提示的包名/命令要跟着**目标三元组**走（windows-386 是 i686，
                # windows-amd64 才是 x86-64；写死一套会误导——用户实测踩过）
                local apt_arch="$HOST_TRIPLE" # x86_64-w64-mingw32 / i686-w64-mingw32
                local pkg_arch
                case "$HOST_TRIPLE" in
                    i686-w64-mingw32) pkg_arch="i686" ;;
                    *)                pkg_arch="x86-64" ;;
                esac
                echo "  交叉线程模型：win32 —— abseil 需要 std::mutex，此模型编不过" >&2
                echo "  请改用 posix 变体后重跑（Debian/Ubuntu）：" >&2
                echo "    sudo apt install g++-mingw-w64-$pkg_arch-posix gcc-mingw-w64-$pkg_arch-posix" >&2
                echo "    sudo update-alternatives --set $apt_arch-g++ /usr/bin/$apt_arch-g++-posix" >&2
                echo "    sudo update-alternatives --set $apt_arch-gcc /usr/bin/$apt_arch-gcc-posix" >&2
                APM_SKIP_REASON="交叉工具链是 win32 线程模型（需 posix 变体）"
                return 0
                ;;
            *)
                echo "  交叉线程模型：${tm:-未知}（按 posix 补 -pthread）"
                CMAKE_CROSS_ARGS+=(
                    "-DTHREADS_PREFER_PTHREAD_FLAG=ON"
                    "-DCMAKE_HAVE_THREADS_LIBRARY=1"
                    "-DCMAKE_USE_PTHREADS_INIT=1"
                    "-DCMAKE_THREAD_LIBS_INIT=-pthread"
                    "-DCMAKE_CXX_FLAGS=-pthread"
                )
                ;;
        esac
    else
        CMAKE_CROSS_ARGS+=(
            "-DTHREADS_PREFER_PTHREAD_FLAG=ON"
            "-DCMAKE_HAVE_THREADS_LIBRARY=1"
            "-DCMAKE_USE_PTHREADS_INIT=1"
            "-DCMAKE_THREAD_LIBS_INIT=-pthread"
            "-DCMAKE_CXX_FLAGS=-pthread"
        )
    fi

    CROSS_FILE="$SRC_DIR/cross-$HOST_TRIPLE.ini"
    # 转换后的工具路径**先赋值再拼接**：`echo "... $(f "$(g "$X")")"` 这种
    # 双引号里套两层命令替换的写法 bash 解析不了（实测报
    # "syntax error near unexpected token `}'"，且错误指向几十行之后的收尾大括号，
    # 极难定位）。
    local ar_n="" ranlib_n="" strip_n=""
    [ -n "$AR" ] && ar_n="$(to_native_path "$(with_exe "$AR")")"
    [ -n "$RANLIB" ] && ranlib_n="$(to_native_path "$(with_exe "$RANLIB")")"
    [ -n "$STRIP" ] && strip_n="$(to_native_path "$(with_exe "$STRIP")")"
    {
        echo "[binaries]"
        echo "c = '$cc'"
        echo "cpp = '$cxx'"
        [ -n "$ar_n" ] && echo "ar = '$ar_n'"
        [ -n "$ranlib_n" ] && echo "ranlib = '$ranlib_n'"
        [ -n "$strip_n" ] && echo "strip = '$strip_n'"
        # 交叉构建里 meson **只用 cross file 里列的** pkg-config / cmake：
        # 不写就报 "Found pkg-config: NO / Found CMake: NO"，即便 PATH 里明明有
        #（实测 i686 目标卡在这）。abseil 的 .pc 是纯文本、本机工具读得了，
        # 所以这里指的就是本机那两个程序。
        local pkgbin=""
        if [ -n "$PKGCONF_DIR" ]; then
            pkgbin="$PKGCONF_DIR/pkg-config"
            [ "$HOST_OS" = windows ] && pkgbin="$PKGCONF_DIR/pkg-config.exe"
        else
            pkgbin="$(command -v pkg-config || command -v pkgconf || true)"
        fi
        if [ -n "$pkgbin" ]; then
            # 两个键名都写：老版 meson（Ubuntu 24.04 自带 1.3.x）只认 pkgconfig，
            # 新版（1.12）只认 pkg-config（同值时允许并存）。只写其一时，交叉
            # 目标会报 "Found Pkg-config: NO" → 去找 wrap → 最后
            # "Automatic wrap-based subproject downloading is disabled"（实测）。
            pkgbin="$(to_native_path "$pkgbin")"
            echo "pkgconfig = '$pkgbin'"
            echo "pkg-config = '$pkgbin'"
        fi
        local cmakebin
        cmakebin="$(command -v cmake || true)"
        [ -n "$cmakebin" ] && echo "cmake = '$(to_native_path "$cmakebin")'"
        echo
        echo "[host_machine]"
        echo "system = '$sys'"
        echo "cpu_family = '$cpu'"
        echo "cpu = '$cpu'"
        echo "endian = 'little'"
    } >"$CROSS_FILE"
    return 0
}

build_abseil() {
    local pfx="$1" bdir="$2"
    echo "  [abseil $ABSL_VER] cmake 配置并编译 ..."
    rm -rf "$bdir"
    # cmake 对编译器要求"存在的完整文件路径"：Windows 上补 .exe（git-bash 的
    # command -v 给的是不带后缀的形式，autotools 能用、cmake 不认）。
    local cc cxx
    cc="$(to_native_path "$(with_exe "$CC")")"
    cxx="$(to_native_path "$(with_exe "$CXX")")"
    # 输出收进日志：失败时打尾巴，免得"失败但没有任何报错"（实测 Linux 交叉目标
    # 就这样丢过一次错误）。
    local clog="$bdir.cmake.log"
    cmake -G Ninja -S "$SRC_DIR/$ABSL_DIR" -B "$bdir"         -DCMAKE_BUILD_TYPE=Release         -DCMAKE_INSTALL_PREFIX="$pfx"         -DBUILD_SHARED_LIBS=OFF         -DABSL_ENABLE_INSTALL=ON         -DABSL_PROPAGATE_CXX_STD=ON         -DCMAKE_CXX_STANDARD=17         -DABSL_BUILD_TESTING=OFF         -DABSL_USE_GOOGLETEST_HEAD=OFF         -DCMAKE_C_COMPILER="$cc"         -DCMAKE_CXX_COMPILER="$cxx"         "${CMAKE_CROSS_ARGS[@]}" >"$clog" 2>&1 || { abseil_fail "$clog"; return 1; }
    cmake --build "$bdir" --target install >>"$clog" 2>&1 || { abseil_fail "$clog"; return 1; }
    [ -f "$pfx/lib/libabsl_base.a" ] || {
        echo "  未生成 libabsl_base.a（完整日志：$clog）" >&2
        tail -n 15 "$clog" >&2 || true
        return 1
    }
    return 0
}

# abseil_fail 打印 cmake 日志尾巴（错误定位用）。
abseil_fail() {
    echo "  cmake 失败，末尾日志（完整日志：$1）：" >&2
    tail -n 20 "$1" >&2 || true
}

build_webrtc_apm() {
    local pfx="$1" bdir="$2"
    echo "  [webrtc-audio-processing $WAP_VER] meson 配置并编译 ..."
    rm -rf "$bdir"
    local meson_args=""
    [ -n "$CROSS_FILE" ] && meson_args="--cross-file=$CROSS_FILE"

    local path_backup="$PATH"
    # PATH 用 git-bash 自己的形式（POSIX 路径 + ':' 分隔）：启动原生程序时
    # MSYS 会自动整段转成 Windows 形式；手动拼成 'D:/x;C:/y' 反而会被它当成
    # 一个畸形条目，转发壳等于没加进去（实测报 "Did not find pkg-config
    # by name 'pkg-config'"）。
    [ -n "$PKGCONF_DIR" ] && PATH="$PKGCONF_DIR:$PATH"

    # meson 从**环境变量**取编译器，而 git-bash 不转换环境变量里的路径：
    # 必须给 Windows 形式的完整路径（见 to_native_path）。
    local cc cxx
    cc="$(to_native_path "$(with_exe "$CC")")"
    cxx="$(to_native_path "$(with_exe "$CXX")")"

    # abseil 是通过 pkg-config 找到的（.pc 由 abseil 的 cmake install 生成）。
    # 路径分隔符跟着平台走：Windows 的 pkgconf 认 ';'，其余平台认 ':'。
    local pcs=";"
    [ "$HOST_OS" = windows ] || pcs=":"
    # meson 的报错走 stdout，别整个吞掉：失败时打末尾若干行出来定位
    local mlog="$bdir.meson.log"
    meson_fail() {
        echo "  meson 失败，末尾日志（完整日志：$mlog）：" >&2
        tail -n 15 "$mlog" >&2 || true
        PATH="$path_backup"
        return 1
    }

    PKG_CONFIG_PATH="$(to_native_path "$pfx/lib/pkgconfig")${PKG_CONFIG_PATH:+$pcs$PKG_CONFIG_PATH}" \
    CC="$cc" CXX="$cxx" \
    $MESON setup "$bdir" "$SRC_DIR/$WAP_DIR" \
        --prefix="$pfx" --libdir=lib --includedir=include \
        --wrap-mode=nodownload --auto-features=enabled \
        --buildtype=plain -Ddefault_library=static \
        -Dc_args="-O2 ${ARCH_FLAGS}" -Dcpp_args="-O2 ${ARCH_FLAGS}" \
        $meson_args >"$mlog" 2>&1 || meson_fail
    $MESON compile -C "$bdir" >>"$mlog" 2>&1 || meson_fail
    $MESON install -C "$bdir" >>"$mlog" 2>&1 || meson_fail
    PATH="$path_backup"

    [ -f "$pfx/lib/libwebrtc-audio-processing-1.a" ] || {
        echo "  未生成 libwebrtc-audio-processing-1.a" >&2
        return 1
    }
    return 0
}

# 打三个补丁（幂等：已打过的用 -N 跳过）。补丁来自上游 MR38 与 MSYS2 的
# mingw64/ucrt64 打包（见 scripts/patches/ 下的文件名注释）。
apply_wap_patches() {
    local d="$SRC_DIR/$WAP_DIR"
    [ -d "$d" ] || return 1
    local f
    for f in "$WAP_PATCH_DIR"/*.patch; do
        [ -f "$f" ] || continue
        ( cd "$d" && patch -p1 -N -s -i "$f" >/dev/null 2>&1 ) || true
    done
    # 打了没有：GCC14 那个补丁会改 meson.build，抽一行来核对
    grep -q "cpp_std" "$d/meson.build" || true
    return 0
}

# ---------- 主流程 ----------

echo "宿主：$HOST_OS   目标：$TARGETS"

if [ "$CLEAN" = 1 ]; then
    rm -rf "$OUT_DIR" "$SRC_DIR"
    echo "已清理 third_party/{src,audio}"
fi

fetch_src "opus-$OPUS_VER.tar.gz" "https://downloads.xiph.org/releases/opus/opus-$OPUS_VER.tar.gz" "opus-$OPUS_VER"

if [ "$BUILD_APM" = 1 ]; then
    fetch_src "abseil-cpp-$ABSL_VER.tar.gz"         "https://github.com/abseil/abseil-cpp/archive/refs/tags/$ABSL_VER.tar.gz"         "$ABSL_DIR"
    fetch_src "webrtc-audio-processing-v$WAP_VER.tar.gz"         "https://gitlab.freedesktop.org/pulseaudio/webrtc-audio-processing/-/archive/v$WAP_VER/webrtc-audio-processing-v$WAP_VER.tar.gz"         "$WAP_DIR"
    apply_wap_patches
fi

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
    # 传给 configure 的前缀：Windows 宿主下必须是 D:/... 形式
    PFX_MK="$(cd "$PFX" && abspath)"

    # 失败时删掉该库自己的产物（静态库 + 它的头文件目录）：只建了一半的库会被
    # build.sh / build.bat 的探测当成「库齐全」，加上 -tags 后编译立刻报缺头文件
    #（fatal error: opus/opus.h），现场与真正的库构建失败对不上号。
    # 两个库各自独立降级：opus 失败不影响 APM，反之亦然。
    if [ "$BUILD_OPUS" = 1 ]; then
        if ! build_opus "$PFX_MK"; then
            echo "  libopus 构建失败（该目标降级为无 Opus）" >&2
            rm -f "$PFX/lib/libopus.a"
            rm -rf "$PFX/include/opus"
            FAILED="$FAILED $T(opus)"
        fi
    fi

    if [ "$BUILD_APM" = 1 ]; then
        if ! detect_meson; then
            echo "  跳过 APM：找不到 meson（Windows/Linux 都可 pip install meson）" >&2
            FAILED="$FAILED $T(apm:无meson)"
        elif ! detect_pkgconfig; then
            echo "  跳过 APM：找不到 pkg-config / pkgconf" >&2
            FAILED="$FAILED $T(apm:无pkgconf)"
        elif ! have_tool cmake || ! have_tool ninja; then
            echo "  跳过 APM：缺 cmake / ninja" >&2
            FAILED="$FAILED $T(apm:缺cmake/ninja)"
        else
            setup_cross "$T"
            if [ -n "$APM_SKIP_REASON" ]; then
                FAILED="$FAILED $T(apm:$APM_SKIP_REASON)"
            elif build_abseil "$PFX_MK" "$SRC_DIR/build-abseil-$T" &&
                build_webrtc_apm "$PFX_MK" "$SRC_DIR/build-apm-$T"; then
                :
            else
                echo "  APM 构建失败（该目标降级为无 AEC/NS/AGC）" >&2
                rm -f "$PFX/lib/libwebrtc-audio-processing-1.a"                       "$PFX/lib/libwebrtc-audio-coding-1.a"                       "$PFX"/lib/libabsl_*.a
                rm -rf "$PFX/include/webrtc-audio-processing-1"
                FAILED="$FAILED $T(apm)"
            fi
        fi
    fi

    echo "  完成：$PFX"
    ls -la "$PFX/lib" 2>/dev/null | awk '{print "    " $5 "  " $9}' | grep -E '(libopus|libwebrtc|libabsl_base)\.a$' || true
    BUILT="$BUILT $T"
done

echo
echo "构建完成：${BUILT:- （无）}"
if [ -n "$FAILED" ]; then
    echo "跳过（缺工具链或构建失败）：$FAILED"
fi
if [ "$TARGETS_DEFAULTED" = 1 ]; then
    echo
    echo "提示：本次只构建了本机原生目标（${TARGETS# }）。build.sh / build.bat 交叉产出的"
    echo "      windows-amd64 / windows-386 / linux-<对侧arch> 想带 Opus，须先执行："
    echo "      sh scripts/build-audio-libs.sh all"
fi
echo
echo "下一步：build.bat / build.sh 会自动探测 third_party/audio/<target> 并加上 -tags audio。"
echo "      APM（AEC/NS/AGC）的探测看 lib/libwebrtc-audio-processing-1.a（另行接）。"
