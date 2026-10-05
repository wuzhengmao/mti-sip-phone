#!/usr/bin/env bash
# 允许用 sh(dash) 调用（部分用户习惯 `sh scripts/xxx.sh`）：本脚本使用了 bash
# 专有语法（数组、${var//a/b}），dash 直接跑会在解析到那些行时报
# Syntax error: "(" unexpected。dash 是逐条执行顶层命令的，所以这条守卫会在
# 读到后面的 bash 语法之前先 exec 切回 bash。
if [ -z "${BASH_VERSION:-}" ]; then
    exec bash "$0" "$@"
fi
# 音频编解码静态库构建脚本（音频第三方库的统一入口）
#
# 当前产出：libopus（Opus 编解码，cgo 直接绑定）到
#   third_party/audio/<target>/{include,lib}
#
# 以后新增音频编码库（如 G.722）按同样模式加到这里与同一个目录，构建脚本
# （build.sh / build.bat）只需按新的 .a 追加探测。
#
# libopus 的构建很轻（无需 nasm/pkg-config，几十秒一个目标），交叉编译只需
# 目标平台的 gcc：x86 的 SIMD 走编译器 intrinsics，不依赖外部汇编器。
#
# 用法：
#   ./scripts/build-audio-libs.sh                    # 只构建本机原生目标
#   ./scripts/build-audio-libs.sh windows-amd64      # 指定目标
#   ./scripts/build-audio-libs.sh all                # 全部目标（缺工具链的自动跳过）
#   ./scripts/build-audio-libs.sh --clean windows-amd64
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

SRC_DIR="$ROOT/third_party/src"
OUT_DIR="$ROOT/third_party/audio"

CLEAN=0
TARGETS=""
while [ $# -gt 0 ]; do
    case "$1" in
        --clean) CLEAN=1 ;;
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

    export CC AR RANLIB STRIP
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

# ---------- 主流程 ----------

echo "宿主：$HOST_OS   目标：$TARGETS"

if [ "$CLEAN" = 1 ]; then
    rm -rf "$OUT_DIR" "$SRC_DIR"
    echo "已清理 third_party/{src,audio}"
fi

fetch_src "opus-$OPUS_VER.tar.gz" "https://downloads.xiph.org/releases/opus/opus-$OPUS_VER.tar.gz" "opus-$OPUS_VER"

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

    # 失败时把目标目录整个删掉：只建了一半的 third_party/audio/<target> 会被
    # build.sh / build.bat 的探测当成「库齐全」，加上 -tags audio 后编译立刻
    # 报缺头文件（fatal error: opus/opus.h），现场与真正的库构建失败对不上号。
    if ! build_opus "$PFX_MK"; then
        echo "  libopus 构建失败" >&2
        FAILED="$FAILED $T"
        rm -rf "$PFX"
        continue
    fi

    echo "  完成：$PFX"
    ls -la "$PFX/lib" | awk '{print "    " $5 "  " $9}' | grep -E '\.a$' || true
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
