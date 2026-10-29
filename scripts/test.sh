#!/usr/bin/env bash
# 固定路径的测试运行器。
#
# 为什么需要它：`go test` 每次都把测试二进制放进**新建的临时目录**
#（Windows 上是 %TEMP%\go-build<随机>\b001\<包名>.test.exe）。测试要监听
# UDP/TCP 端口，而 Windows 防火墙把每一个"没见过的程序路径"都当成新程序，
# 于是每次跑测试都弹一次「是否允许访问网络」，必须手点。
#
# 本脚本把测试二进制统一构建到仓库内**固定路径** .testbin/<包名>.test[.exe]
# 再直接执行：防火墙只需放行一次，之后所有运行都复用同一条规则（路径不变）。
#
# 用法：
#   ./scripts/test.sh                        # 跑默认的全部测试包
#   ./scripts/test.sh -p ./internal/media/   # 只跑指定包（可重复 -p）
#   ./scripts/test.sh -p ./internal/sip/ -- -run TestIncomingOverTLS -v
#   ./scripts/test.sh -- -count=1 -timeout 5m
#
# `--` 之后的参数用 **go test 的写法**（-run/-v/-count/-timeout…），脚本会翻成
# 测试二进制认识的 -test.xxx 形式；不写 `--` 时默认带 -count=1
#（固定路径运行时没有 go 的缓存层，这里只为与 go test 口径一致）。
set -e
cd "$(dirname "$0")/.."
ROOT="$(pwd)"

BIN_DIR="$ROOT/.testbin"
EXE="$(go env GOEXE)"
mkdir -p "$BIN_DIR"

# ---------- CGO 工具链（音频/界面的测试需要 gcc）----------
# 优先 WinLibs（build.bat 同款探测），其次 PATH 里的 gcc。
if [ -z "${CC:-}" ]; then
    _local="${LOCALAPPDATA:-$USERPROFILE/AppData/Local}"
    command -v cygpath >/dev/null 2>&1 && _local="$(cygpath -u "$_local")"
    for d in "$_local"/Microsoft/WinGet/Packages/BrechtSanders.WinLibs.POSIX.UCRT_*/mingw64/bin/gcc.exe; do
        if [ -x "$d" ]; then
            export CC="$d"
            break
        fi
    done
fi
if [ -z "${CC:-}" ] && command -v gcc >/dev/null 2>&1; then
    export CC="$(command -v gcc)"
fi
export CGO_ENABLED="${CGO_ENABLED:-1}"
[ -n "${CC:-}" ] && echo "CGO 工具链：$CC" || echo "警告：未找到 gcc，需要 CGO 的包会编译失败"

# ---------- 参数解析 ----------
PKGS=()
PASSTHRU=()
SEEN_DASHES=0
while [ $# -gt 0 ]; do
    case "$1" in
        --) SEEN_DASHES=1; shift ;;
        -p) PKGS+=("$2"); shift 2 ;;
        *)
            if [ "$SEEN_DASHES" = 0 ]; then
                echo "未知参数：$1（测试参数请放在 -- 之后）" >&2
                exit 2
            fi
            PASSTHRU+=("$1"); shift ;;
    esac
done
if [ ${#PKGS[@]} -eq 0 ]; then
    # 默认：本仓库全部可测包（与 CLAUDE.md 的测试命令一致）
    PKGS=(./internal/media/ ./internal/logx/ ./internal/config/ ./internal/calllog/ \
          ./internal/contacts/ ./internal/wsapi/ ./internal/sip/ ./internal/updater/ ./internal/ui/)
fi
if [ ${#PASSTHRU[@]} -eq 0 ]; then
    PASSTHRU=(-count=1)
fi

# go test 的参数 → 测试二进制的参数：-run X → -test.run X（二进制只认 -test. 前缀）。
# 逐个别名列出而不是统一加前缀：go test 的包级参数（-tags/-gcflags 等）不该转发。
TEST_FLAGS="run|v|count|timeout|short|bench|benchtime|failfast|parallel|cpu|coverprofile|covermode|skip|shuffle|json|list|outputdir"
ARGS=()
for a in "${PASSTHRU[@]}"; do
    name="${a%%=*}"
    case "$name" in
        -test.*) ARGS+=("$a") ;;
        -*) case "|$TEST_FLAGS|" in
                *"|${name#-}|"*) ARGS+=("-test.${a#-}") ;;
                *) ARGS+=("$a") ;;
            esac ;;
        *) ARGS+=("$a") ;;
    esac
done
PASSTHRU=("${ARGS[@]}")

# ---------- 构建并运行 ----------
FAILED=()
for pkg in "${PKGS[@]}"; do
    name="$(echo "$pkg" | sed 's#^\./##; s#/$##; s#/#_#g')"
    bin="$BIN_DIR/$name.test$EXE"
    if ! go test -c -o "$bin" "$pkg"; then
        echo "构建测试二进制失败：$pkg" >&2
        FAILED+=("$pkg")
        continue
    fi
    echo "---- $pkg ----"
    # 测试二进制的**工作目录必须是包目录**（testdata 等相对路径依赖它，
    # go test 也是这么跑的）
    if ! ( cd "$pkg" && "$bin" "${PASSTHRU[@]}" ); then
        FAILED+=("$pkg")
    fi
done

rm -rf "$BIN_DIR"

if [ ${#FAILED[@]} -gt 0 ]; then
    echo "失败：${FAILED[*]}" >&2
    exit 1
fi
echo "全部通过"
