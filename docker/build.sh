#!/usr/bin/env bash
# 在容器里交叉编译 mti-sip-phone 的全部 5 个产物
#   linux-amd64 / linux-arm64 / windows-amd64 / windows-386 / windows-arm64
#
# 用法（在 WSL / Linux 里执行；位置随意，脚本自己找仓库根）：
#   docker/build.sh                 # 构建镜像（如缺）+ 编译全部 5 个产物
#   docker/build.sh --target windows-amd64   # **只编 Windows amd64（测试用，快）**
#   docker/build.sh --libs          # 只编第三方静态库（视频 / Opus+AEC，很慢，幂等）
#   docker/build.sh --build-image   # 只（重新）构建镜像
#   docker/build.sh --shell         # 进容器开一个交互 shell（排查用）
#   docker/build.sh --go 1.25.3     # 指定 Go 版本重建镜像
#
# 持久化：
#   dist/ 与 third_party/   在宿主仓库里（产物与第三方静态库都留在仓库目录）
#   Go 模块缓存 / 构建缓存   Docker 命名卷 mti-sip-phone-gomod / -gocache（重复构建快很多）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="wuzm219/msp-builder"   # Docker Hub 上的构建镜像（Debian 10 / glibc 2.28，见 docker/README.md）
GO_VERSION="1.25.0"

# MSYS/git-bash 会把"看起来像 Unix 路径"的参数自动转成 Windows 形式——
# 实测 `-w /src` 被转成 `C:/Program Files/Git/src`，容器直接起不来。
# 容器内的路径必须原样传给 CLI；宿主侧需要 Windows 形式的路径下面已用 cygpath
# 显式转换，不依赖自动转换。Linux 上这个变量无副作用。
export MSYS_NO_PATHCONV=1

MODE=build
FORCE_IMAGE=0
EXTRA_ARGS=""
while [ $# -gt 0 ]; do
    case "$1" in
        --libs)        MODE=libs ;;
        --build-image) MODE=image ;;
        --shell)       MODE=shell ;;
        --go)          GO_VERSION="${2:?--go 需要一个版本号}"; FORCE_IMAGE=1; shift ;;
        --target)      EXTRA_ARGS="$EXTRA_ARGS --target ${2:?--target 需要一个目标名}"; shift ;;
        *) echo "未知参数：$1" >&2; exit 2 ;;
    esac
    shift
done

# 容器 CLI 探测：**默认 docker 优先**。wslc（WSL 3.0.1 的容器命令）实测有多个
# 子命令卡死/状态坏死的 bug——ps/list/image inspect 无响应、session terminate 后
# ERROR_INVALID_STATE、状态文件锁连 `wsl --shutdown` 都释放不掉（2026-10-09 踩过），
# 暂时不用；要用时 `CONTAINER_CLI=wslc` 显式指定。
# 两者在 build/run/-v/-w/-e/-u/--rm 上完全一致。
CLI="${CONTAINER_CLI:-}"
if [ -z "$CLI" ]; then
    if command -v docker >/dev/null 2>&1; then
        CLI=docker
    elif command -v wslc >/dev/null 2>&1; then
        CLI=wslc
    fi
fi
if [ -z "$CLI" ]; then
    echo "找不到容器命令：docker（Docker Desktop）或 wslc（WSL 3.0+）都没有" >&2
    exit 1
fi
"$CLI" info >/dev/null 2>&1 || { echo "$CLI 引擎不可用（WSL 会话 / docker daemon 起了吗？）" >&2; exit 1; }

# 挂载源路径：在 git-bash（MSYS）里跑的两种情况——Docker Desktop 的 CLI 和
# wslc——**都是 Windows 程序、都认 Windows 路径**（`/d/git/...` 会报 not found），
# 所以 MSYS 下一律转成 Windows 形式；在 WSL/Linux 里跑（docker 自己的 CLI）时
# 保持 Linux 路径。显式转换，不依赖 shell 的自动路径转换。
MOUNT_SRC="$ROOT"
BUILD_CTX="$ROOT/docker"
case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
        MOUNT_SRC="$(cygpath -w "$ROOT")"
        BUILD_CTX="$(cygpath -w "$BUILD_CTX")"
        ;;
esac

# 本地已有镜像就直接用，跳过 rebuild——`docker build` 必须先联网解析 FROM 基础
# 镜像元数据（要访问 Docker Hub），网络不通时会在编译步骤之前就失败（本地镜像
# 其实完好）。只有显式 --build-image / --go 或镜像不存在时才 rebuild。
# ⚠️ 跳过依赖 `image inspect`，但它在 wslc 下会卡死（60 秒无响应），所以这个
# "跳过"只在 docker CLI 下生效；wslc 仍走原来的每次 build。
if [ "$MODE" != image ] && [ "$FORCE_IMAGE" != 1 ] && [ "$CLI" = docker ] \
    && docker image inspect "$IMAGE" >/dev/null 2>&1; then
    echo "== 使用本地镜像 $IMAGE（跳过 rebuild；--build-image / --go 可强制重建）=="
else
    echo "== 校验/构建镜像 $IMAGE（命中缓存时几秒；CLI=$CLI）=="
    "$CLI" build --progress=plain --build-arg "GO_VERSION=$GO_VERSION" -t "$IMAGE" "$BUILD_CTX"
fi
if [ "$MODE" = image ]; then
    echo "镜像就绪：$IMAGE"
    exit 0
fi

# 容器内执行；结束后把 dist/ 与 third_party/ 的属主改回宿主用户
# （容器里是 root，不改的话宿主侧这些文件都是 root 所有，删改都别扭）
run_in_container() {
    "$CLI" run --rm \
        -v "$MOUNT_SRC":/src \
        -v mti-sip-phone-gomod:/go/pkg/mod \
        -v mti-sip-phone-gocache:/root/.cache/go-build \
        -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
        -e SIP_PHONE_GLFW_X11=1 \
        -e SIP_PHONE_RELEASE \
        -w /src \
        "$IMAGE" \
        bash -c "$1; ec=\$?; chown -R \$HOST_UID:\$HOST_GID /src/dist /src/third_party 2>/dev/null || true; exit \$ec"
}

case "$MODE" in
    shell)
        exec "$CLI" run --rm -it \
            -v "$MOUNT_SRC":/src \
            -v mti-sip-phone-gomod:/go/pkg/mod \
            -v mti-sip-phone-gocache:/root/.cache/go-build \
            -e SIP_PHONE_GLFW_X11=1 \
            -e SIP_PHONE_RELEASE \
            -w /src "$IMAGE" bash
        ;;
    libs)
        echo "== 编第三方静态库（视频 / Opus+AEC）：几十分钟到数小时，幂等可重跑 =="
        run_in_container "./scripts/build-video-libs.sh all && ./scripts/build-audio-libs.sh all"
        echo "== 完成。接着跑 docker/build.sh 出 5 个产物 =="
        ;;
    build)
        run_in_container "./build.sh $EXTRA_ARGS"
        echo "== 产物在 $ROOT/dist/ =="
        ;;
esac
