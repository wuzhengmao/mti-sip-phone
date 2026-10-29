#!/usr/bin/env bash
# 允许用 sh(dash) 调用：本脚本用了 bash 专有语法（数组），dash 会在解析到那些
# 行时报 Syntax error。dash 是逐条执行顶层命令的，这条守卫会在读到后面的
# bash 语法之前先 exec 切回 bash。
if [ -z "${BASH_VERSION:-}" ]; then
    exec bash "$0" "$@"
fi
# 一键发布脚本：编译 → 按更新站点《应用发布规范 v1》打包 → 上传到更新站点
#
# 用法（Windows 走 git-bash，或直接 scripts\release.bat）：
#   ./scripts/release.sh                  # 正式版：版本取自 buildinfo.go（如 1.0.5）
#   ./scripts/release.sh --pre            # 预发布/开发版：版本 1.0.5-rc（站点标记为预发布）
#   ./scripts/release.sh --pre --dry-run  # 只编译打包，不上传（产物留在 dist/release/）
#   ./scripts/release.sh --no-build       # 用 dist/ 里已有的产物打包（跳过新鲜度校验）
#   ./scripts/release.sh --only no-ui     # 只发「仅托盘无 UI 版」（默认两个应用都发）
#   ./scripts/release.sh --title "体验优化版" --mandatory
#   ./scripts/release.sh --darwin-ci      # 同时出 macOS 产物：触发 GitHub Actions 编 darwin，
#                                         # 与本地容器编译并行，跑完自动下载回来一起发布
#
# 配置：release/release.conf（站点地址 / 应用 ID / 上传令牌 / 可选站点证书），
#       同名环境变量可覆盖（UPDATE_SITE / APP_ID / UPLOAD_TOKEN / CA_CERT /
#       INSECURE_TLS）。站点启用 TLS 时 UPDATE_SITE 必须直接写 https 地址
#       （HTTP 端口会 301 跳转，而 POST 跟随 301 会退化成 GET）。
#       --darwin-ci 另需 GH_TOKEN（GitHub PAT，能触发/读取 Actions）与可选
#       GH_REPO（默认 wuzhengmao/mti-sip-phone）、GH_REF（默认当前分支）。
#
# 产物打包内容（站点规范）：
#   mti-sip-phone-<平台>-<架构>[.exe]          发布产物（**不含版本号**：下载地址跨版本不变）
#   release.json                              版本元数据（artifacts 显式声明平台/架构/校验和）
#   CHANGELOG.md                              从根 changelog.md 抽取该版本段落
#   SHA256SUMS                                校验和清单
# 应用级元数据走站点接口，不直接动归档目录：
#   release/app.json  --PUT-->  /api/v1/apps/<app>/metadata     （整份替换）
#   release/icon.svg  --PUT-->  /api/v1/apps/<app>/icon         （文件名由站点按内容决定）
#
# 注意：
#   - 编译统一走**容器**（docker/build.sh）：一次产出全部 10 个产物（完整版 +
#     无 UI 版 × 5 平台），基线固定 Debian 10 / glibc 2.28（信创兼容下限）；
#   - 构建产物的 mtime 必须晚于本次构建开始，否则视为旧产物跳过（防把上次的二进制当新版本发出去）；
#   - 版本号只读不写，升版仍需人工改 internal/core/buildinfo.go。
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
# 配置文件：默认完整版；--no-ui 时（参数解析后）切到 release-no-ui.conf。
# 两个变体在更新站点上是**两个独立应用**（各自的归档目录与上传令牌）。
CONF="$ROOT/release/release.conf"

usage() {
    cat <<'EOF'
用法（Windows 走 git-bash，或直接 scripts\release.bat）：
  ./scripts/release.sh                  # 正式版：版本取自 buildinfo.go（如 1.0.5）
  ./scripts/release.sh --pre            # 预发布/开发版：版本 1.0.5-rc（站点标记为预发布）
  ./scripts/release.sh --pre --dry-run  # 只编译打包，不上传（产物留在 dist/release/）
  ./scripts/release.sh --no-build       # 用 dist/ 里已有的产物打包（跳过新鲜度校验）
  ./scripts/release.sh --only no-ui     # 只发「仅托盘无 UI 版」（默认：完整版 + 无 UI 版一起发）
  ./scripts/release.sh --title "体验优化版" --mandatory
  ./scripts/release.sh --darwin-ci      # 同时经 GitHub Actions 编 macOS 产物并一起发布
选项：--pre / --dry-run / --no-build / --only <full|no-ui> / -v <版本> / --title <标题> / --channel <通道> / --mandatory
      --darwin-ci                       需要 GH_TOKEN（GitHub PAT）；可选 GH_REPO / GH_REF
配置：**两个应用各一份**——release/release.conf（完整版 mti-sip-phone）与
      release/release-no-ui.conf（无 UI 版 mti-sip-phone-no-ui），各自一份上传令牌；
      两份都读同名的环境变量覆盖：
      UPDATE_SITE / APP_ID / UPLOAD_TOKEN / CA_CERT / INSECURE_TLS，
      同名环境变量可覆盖，例如对着本地站点试跑：
      UPDATE_SITE=https://localhost:8443 CA_CERT=certs/server.crt ./scripts/release.sh ...）
EOF
}

# ---------- 参数 ----------

PRE=0
DRY=0
DO_BUILD=1
VERSION=""
TITLE=""
CHANNEL=""
MANDATORY=0
ONLY="" # 只发某个应用（full / no-ui）；留空 = 两个都发
DARWIN_CI=0 # 1 = 触发 GitHub Actions 编 macOS 产物（--darwin-ci）

while [ $# -gt 0 ]; do
    case "$1" in
        --pre) PRE=1 ;;
        --dry-run) DRY=1 ;;
        --no-build) DO_BUILD=0 ;;
        --only) ONLY="${2:-}"; shift ;;
        --no-ui) ONLY="no-ui" ;; # 等价于 --only no-ui（向后兼容）
        -v|--version) VERSION="${2:-}"; shift ;;
        --title) TITLE="${2:-}"; shift ;;
        --channel) CHANNEL="${2:-}"; shift ;;
        --mandatory) MANDATORY=1 ;;
        --darwin-ci) DARWIN_CI=1 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
    esac
    shift
done

# ---------- 配置 ----------

# conf_get 读配置项：release/release.conf 里的 KEY=VALUE，同名环境变量优先。
conf_get() {
    local key="$1" line val="" env_val=""
    if [ -f "$CONF" ]; then
        while IFS= read -r line || [ -n "$line" ]; do
            line="${line%$'\r'}"
            case "$line" in ''|'#'*) continue ;; esac
            case "$line" in "$key="*) val="${line#"$key="}" ;; esac
        done < "$CONF"
    fi
    env_val="$(printenv "$key" 2>/dev/null || true)"
    if [ -n "$env_val" ]; then
        printf '%s' "$env_val"
    else
        printf '%s' "$val"
    fi
}

# load_app_conf 按应用切换配置与元数据：两个应用在站点上是**两个 app**
#（各自的 APP_ID / 上传令牌 / 归档目录），所以每一项都要按变体重读一遍。
load_app_conf() {
    case "$1" in
    no-ui)
        APP_NAME="仅托盘无 UI 版"
        CONF="$ROOT/release/release-no-ui.conf"
        APP_JSON="$ROOT/release/app-no-ui.json"
        ;;
    *)
        APP_NAME="完整版"
        CONF="$ROOT/release/release.conf"
        APP_JSON="$ROOT/release/app.json"
        ;;
    esac

    UPDATE_SITE="$(conf_get UPDATE_SITE)"
    APP_ID="$(conf_get APP_ID)"
    UPLOAD_TOKEN="$(conf_get UPLOAD_TOKEN)"
    CA_CERT="$(conf_get CA_CERT)"
    INSECURE_TLS="$(conf_get INSECURE_TLS)"
    UPDATE_SITE="${UPDATE_SITE%/}" # 去结尾斜杠

    if [ -z "$UPDATE_SITE" ] || [ -z "$APP_ID" ] || [ -z "$UPLOAD_TOKEN" ]; then
        echo "配置不完整：请检查 $CONF 里的 UPDATE_SITE / APP_ID / UPLOAD_TOKEN" >&2
        exit 1
    fi

    # 站点证书：优先用 CA_CERT 校验（自签名证书以自身为 CA，--cacert 即可），
    # 没配就交给 curl 的默认信任链；INSECURE_TLS=1 才跳过校验（调试用）。
    CURL_TLS=()
    if [ "$INSECURE_TLS" = "1" ]; then
        CURL_TLS+=(-k)
    elif [ -n "$CA_CERT" ]; then
        if [ -f "$CA_CERT" ]; then
            CURL_TLS+=(--cacert "$(cygpath -m "$CA_CERT" 2>/dev/null || echo "$CA_CERT")")
        else
            echo "配置的 CA_CERT 不存在（$CA_CERT），本次不校验证书" >&2
            CURL_TLS+=(-k)
        fi
    fi
}

case "$ONLY" in
    ""|full|no-ui) ;;
    *) echo "未知的 --only 取值：$ONLY（应为 full 或 no-ui）" >&2; exit 2 ;;
esac

# ---------- 版本 ----------

# 注意行尾：Windows 检出下 buildinfo.go 是 CRLF，sed 的 "$" 锚点会被行尾的
# \r 挡住（表现为"解析不到版本号"），所以末尾用 .* 吸收 \r（build.bat 的
# findstr 没有这个问题）。
VERSION_BASE="$(sed -n 's/^var Version = "\([^"]*\)".*$/\1/p' "$ROOT/internal/core/buildinfo.go")"
if [ -z "$VERSION_BASE" ]; then
    echo "无法从 internal/core/buildinfo.go 解析基础版本号" >&2
    exit 1
fi
if [ -z "$VERSION" ]; then
    if [ "$PRE" = 1 ]; then
        VERSION="${VERSION_BASE}-rc"
    else
        VERSION="$VERSION_BASE"
    fi
fi
VERSION="${VERSION#v}"
# 站点对版本目录的要求：^v?\d+(\.\d+)*([-_+][0-9A-Za-z.\-+]*)?$。
# 同时兜住 --version 传入的怪值（版本号会进路径，不能让 ../ 之类混进来）。
if ! grep -Eq '^[0-9]+(\.[0-9]+)*([-_+][0-9A-Za-z.+-]*)?$' <<<"$VERSION"; then
    echo "版本号不合法：$VERSION（形如 1.2.3 / 1.2.3-rc / 2026.10.03）" >&2
    exit 1
fi
case "$VERSION" in
    *-*|*_*) PRERELEASE=true ;;   # 版本号含 - 或 _ → 站点判定为预发布
    *) PRERELEASE=false ;;
esac
TITLE_GIVEN="$TITLE" # 命令行给了 --title 就两个应用共用，否则各自 <APP_ID> <版本>
TITLE=""

# ---------- macOS 产物（GitHub Actions，--darwin-ci） ----------
# macOS 只能在 macOS 上编译：本地容器在编 Windows/Linux 的 10 个产物时，同时
# 触发 GitHub Actions 编 darwin 两个架构，跑完把 artifact 下载回 dist/：
#   裸二进制（自动更新下载的目标）→ dist/sip-phone[-no-ui]-darwin-<arch>
#   universal DMG（首次安装包）    → dist/macos/，另存稳定副本 dist/macos-dmg-<变体>.dmg
# dispatch 放在容器编译之前、collect 放在之后，两边并行，总时长取较慢的一边。
GH_REPO="${GH_REPO:-wuzhengmao/mti-sip-phone}"
GH_WORKFLOW="${GH_WORKFLOW:-build.yml}"
GH_REF="${GH_REF:-}"
GH_API="https://api.github.com"
GH_BODY="$(mktemp)"
CI_WORKDIR=""
cleanup_gh() {
    rm -f "$GH_BODY"
    [ -n "$CI_WORKDIR" ] && rm -rf "$CI_WORKDIR"
}
trap cleanup_gh EXIT

if [ "$DARWIN_CI" = 1 ] && [ -z "${GH_TOKEN:-}" ]; then
    echo "--darwin-ci 需要环境变量 GH_TOKEN（GitHub PAT，需 Actions 读写权限）" >&2
    echo "  export GH_TOKEN=<token>            # 可选 GH_REPO / GH_REF 覆盖默认仓库与分支" >&2
    exit 1
fi

# gh_api <METHOD> <PATH> [JSON]：响应体写到 $GH_BODY，HTTP 状态码在 $GH_CODE。
# （不用命令替换是为了让调用方还能读到状态码与响应体。）
gh_api() {
    local method="$1" path="$2" body="${3:-}" code
    if [ -n "$body" ]; then
        code="$(curl -sS --max-time 60 -o "$GH_BODY" -w '%{http_code}' -X "$method" \
            -H "Authorization: Bearer $GH_TOKEN" \
            -H "Accept: application/vnd.github+json" \
            -H "Content-Type: application/json" \
            -d "$body" "$GH_API$path")" || code=000
    else
        code="$(curl -sS --max-time 60 -o "$GH_BODY" -w '%{http_code}' -X "$method" \
            -H "Authorization: Bearer $GH_TOKEN" \
            -H "Accept: application/vnd.github+json" \
            "$GH_API$path")" || code=000
    fi
    GH_CODE="$code"
}

# dispatch_darwin_ci 触发 workflow_dispatch：正式版传 release=true（构建不带 -rc）。
dispatch_darwin_ci() {
    local ref="$GH_REF" release_input
    if [ -z "$ref" ]; then
        ref="$(git rev-parse --abbrev-ref HEAD)"
    fi
    CI_SHA="$(git rev-parse HEAD)"
    CI_REF="$ref"
    if [ "$PRERELEASE" = true ]; then release_input=false; else release_input=true; fi
    if ! git diff --quiet || ! git diff --cached --quiet; then
        echo "提示：工作区有未提交改动——GitHub Actions 编的是已提交的 HEAD（$CI_SHA）。" >&2
    fi
    echo "触发 GitHub Actions：$GH_REPO / $GH_WORKFLOW @ $ref（正式版=$release_input）…"
    gh_api POST "/repos/$GH_REPO/actions/workflows/$GH_WORKFLOW/dispatches" \
        "{\"ref\":\"$ref\",\"inputs\":{\"release\":\"$release_input\"}}"
    if [ "$GH_CODE" != 204 ]; then
        echo "触发失败（HTTP $GH_CODE）：" >&2
        cat "$GH_BODY" >&2
        echo >&2
        echo "检查：GH_TOKEN 是否有 Actions 写权限；分支 $ref 是否已推送到 $GH_REPO；" >&2
        echo "      工作流文件是否在该分支上（默认 $GH_WORKFLOW）。" >&2
        exit 1
    fi
}

# extract_zip 解压 artifact（git-bash 不一定有 unzip，逐个回退）。
extract_zip() {
    local zip="$1" dir="$2"
    mkdir -p "$dir"
    if command -v unzip >/dev/null 2>&1; then
        unzip -q -o "$zip" -d "$dir"
    elif command -v python3 >/dev/null 2>&1; then
        python3 -m zipfile -e "$zip" "$dir"
    elif command -v powershell.exe >/dev/null 2>&1; then
        powershell.exe -NoProfile -Command \
            "Expand-Archive -Force -LiteralPath '$(cygpath -w "$zip" 2>/dev/null || echo "$zip")' -DestinationPath '$(cygpath -w "$dir" 2>/dev/null || echo "$dir")'"
    else
        echo "解压 artifact 需要 unzip / python3 / powershell 之一" >&2
        return 1
    fi
}

# collect_darwin_ci 等 Actions 跑完，把裸二进制与 DMG 收进 dist/。
collect_darwin_ci() {
    local i run_id="" run_sha="" run_status="" conclusion="" aid zip bins dmg full_dmg no_dmg f
    CI_WORKDIR="$(mktemp -d)"
    echo "等待 GitHub Actions 编译 macOS（最多 40 分钟）…"
    for i in $(seq 1 120); do
        gh_api GET "/repos/$GH_REPO/actions/runs?event=workflow_dispatch&branch=$CI_REF&per_page=5"
        run_id=""
        run_sha=""
        if [ "$GH_CODE" = 200 ]; then
            run_id="$(sed -n 's/^ *"id": \([0-9]\+\),[[:space:]]*$/\1/p' "$GH_BODY" | head -1)"
            run_sha="$(sed -n 's/^ *"head_sha": "\([0-9a-f]\{40\}\)",[[:space:]]*$/\1/p' "$GH_BODY" | head -1)"
        fi
        if [ -n "$run_id" ] && [ "$run_sha" = "$CI_SHA" ]; then
            break
        fi
        sleep 10
    done
    if [ -z "$run_id" ] || [ "$run_sha" != "$CI_SHA" ]; then
        echo "没找到 $CI_SHA 的 GitHub Actions 运行（检查 $GH_REPO 的 Actions 页面）" >&2
        exit 1
    fi
    echo "运行 ID：$run_id（$CI_SHA）"

    for i in $(seq 1 240); do
        gh_api GET "/repos/$GH_REPO/actions/runs/$run_id"
        run_status="$(sed -n 's/^ *"status": "\([a-z_]*\)",[[:space:]]*$/\1/p' "$GH_BODY" | head -1)"
        if [ "$run_status" = completed ]; then
            conclusion="$(sed -n 's/^ *"conclusion": "\([a-z_]*\)",[[:space:]]*$/\1/p' "$GH_BODY" | head -1)"
            break
        fi
        sleep 10
    done
    if [ "$run_status" != completed ] || [ "$conclusion" != success ]; then
        echo "GitHub Actions 未成功（status=$run_status conclusion=${conclusion:-无}）" >&2
        echo "  查看：https://github.com/$GH_REPO/actions/runs/$run_id" >&2
        exit 1
    fi
    echo "Actions 完成（success），下载产物…"

    gh_api GET "/repos/$GH_REPO/actions/runs/$run_id/artifacts"
    if [ "$GH_CODE" != 200 ]; then
        echo "读取 artifact 列表失败（HTTP $GH_CODE）" >&2
        cat "$GH_BODY" >&2
        exit 1
    fi
    # artifact 对象里 id 在 name 之前（workflow_run.id 在后），"记住最近 id、遇到
    # name 时配对"即可；只取我们认识的两个名字。
    aid="$(awk '/"id": [0-9]+,?$/ { id=$2; gsub(/,/,"",id) }
                /"name": "darwin-binaries"/ { print id; exit }' "$GH_BODY")"
    bins="$(awk '/"id": [0-9]+,?$/ { id=$2; gsub(/,/,"",id) }
                  /"name": "macos-dmg"/ { print id; exit }' "$GH_BODY")"
    if [ -z "$aid" ] || [ -z "$bins" ]; then
        echo "artifact 列表里缺 darwin-binaries 或 macos-dmg（工作流是否更新过？）" >&2
        exit 1
    fi
    zip="$CI_WORKDIR/bins.zip"
    curl -sSL --max-time 300 -H "Authorization: Bearer $GH_TOKEN" \
        -o "$zip" "$GH_API/repos/$GH_REPO/actions/artifacts/$aid/zip" ||
        { echo "下载 darwin-binaries 失败" >&2; exit 1; }
    extract_zip "$zip" "$CI_WORKDIR/bins" || exit 1
    zip="$CI_WORKDIR/dmg.zip"
    curl -sSL --max-time 300 -H "Authorization: Bearer $GH_TOKEN" \
        -o "$zip" "$GH_API/repos/$GH_REPO/actions/artifacts/$bins/zip" ||
        { echo "下载 macos-dmg 失败" >&2; exit 1; }
    extract_zip "$zip" "$CI_WORKDIR/dmg" || exit 1

    for f in sip-phone-darwin-amd64 sip-phone-darwin-arm64 \
             sip-phone-no-ui-darwin-amd64 sip-phone-no-ui-darwin-arm64; do
        [ -f "$CI_WORKDIR/bins/$f" ] || { echo "Actions 产物缺少 $f" >&2; exit 1; }
        cp -f "$CI_WORKDIR/bins/$f" "$ROOT/dist/$f"
        chmod 755 "$ROOT/dist/$f"
    done
    mkdir -p "$ROOT/dist/macos"
    cp -f "$CI_WORKDIR/dmg/"*.dmg "$ROOT/dist/macos/"
    full_dmg="$(ls "$ROOT/dist/macos/"MTI-SIP-Phone-full-*.dmg 2>/dev/null | head -1)"
    no_dmg="$(ls "$ROOT/dist/macos/"MTI-SIP-Phone-no-ui-*.dmg 2>/dev/null | head -1)"
    [ -n "$full_dmg" ] || { echo "Actions 产物缺少完整版 DMG" >&2; exit 1; }
    [ -n "$no_dmg" ] || { echo "Actions 产物缺少无 UI 版 DMG" >&2; exit 1; }
    cp -f "$full_dmg" "$ROOT/dist/macos-dmg-full.dmg"
    cp -f "$no_dmg" "$ROOT/dist/macos-dmg-no-ui.dmg"
    echo "已收进 dist/：darwin 裸二进制 ×4、DMG ×2（已另存 dist/macos-dmg-{full,no-ui}.dmg）"
}

# ---------- 编译 ----------

case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) HOST=windows ;;
    Linux) HOST=linux ;;
    *) HOST=other ;;
esac
if [ "$HOST" = other ]; then
    echo "不支持的宿主平台：$(uname -s)（Windows 用 git-bash，Linux 直接跑）" >&2
    exit 1
fi

BUILD_START="$(date +%s)"

# macOS 的 CI 先触发，和下面的容器编译并行跑
if [ "$DARWIN_CI" = 1 ]; then
    dispatch_darwin_ci
fi

if [ "$DO_BUILD" = 1 ]; then
    # 统一走**容器编译**（docker/build.sh）：一次产出全部 10 个产物（完整版 + 无 UI 版
    # × 5 平台），构建基线固定 Debian 10 / glibc 2.28（信创系统 UOS/麒麟 的兼容下限，
    # 见 docker/README.md）。Windows / Linux 宿主同一条路——不再按平台分
    # build.bat / build.sh（那两条老路留给没装容器的机器手工用）。
    # 正式版把 SIP_PHONE_RELEASE=1 透传进容器（docker/build.sh 用 -e 带入）。
    if [ "$PRERELEASE" = true ]; then
        echo "编译（开发版，版本带 -rc）：docker/build.sh（容器）"
        bash "$ROOT/docker/build.sh"
    else
        echo "编译（正式版，SIP_PHONE_RELEASE=1）：docker/build.sh（容器）"
        SIP_PHONE_RELEASE=1 bash "$ROOT/docker/build.sh"
    fi
else
    echo "跳过编译（--no-build）：直接使用 dist/ 里已有的产物"
fi

if [ "$DARWIN_CI" = 1 ]; then
    collect_darwin_ci
fi

# publish_app 发布**一个**应用（完整版或仅托盘无 UI 版）：
# 读自己的配置 → 收集自己的产物 → 打包 → 上传 → 核对。
publish_app() {
VARIANT="$1"
load_app_conf "$VARIANT"
TITLE="${TITLE_GIVEN:-${APP_ID} ${VERSION}}"
echo
echo "======== 发布 $APP_NAME（应用 $APP_ID）========"
echo "配置：$CONF"

# ---------- 收集产物 ----------
# 源文件 → 发布文件名 <APP_ID>-<平台>-<架构>[.exe]（固定不含版本号：同一平台的
# 下载地址跨版本不变，用户与脚本收藏的链接不会失效；版本靠版本目录区分，
# 站点文档也允许这种命名——文件名里有平台/架构词即可，何况 release.json 还显式声明了）。
# 只列托盘 GUI 产物：sip-phone-console.exe 是同平台第二个候选，不发布。
# windows-arm64 是**基础版**（无视频/Opus/音频预处理：这三套静态库还没有 arm64 版本，
# 见 build.bat / build.sh 的说明），标题里写明，免得 ARM64 用户以为是完整功能版；
# 该产物需要 llvm-mingw 工具链，没装时构建脚本会跳过它，这里按"缺失"处理（不影响其余）。
# 变体：完整版 = dist/sip-phone-*；仅托盘无 UI 版 = dist/sip-phone-no-ui-*（--only no-ui）。
if [ "$VARIANT" = "no-ui" ]; then
    ART_SRC=(
        "dist/sip-phone-no-ui-windows-amd64.exe"
        "dist/sip-phone-no-ui-windows-386.exe"
        "dist/sip-phone-no-ui-windows-arm64.exe"
        "dist/sip-phone-no-ui-linux-amd64"
        "dist/sip-phone-no-ui-linux-arm64"
        "dist/sip-phone-no-ui-darwin-amd64"
        "dist/sip-phone-no-ui-darwin-arm64"
        "dist/macos-dmg-no-ui.dmg"
    )
    ART_TITLE=(
        "Windows 64 位（免安装，仅托盘）"
        "Windows 32 位（免安装，仅托盘）"
        "Windows ARM64（免安装，仅托盘，无视频/Opus/AEC）"
        "Linux 64 位（仅托盘）"
        "Linux ARM64（仅托盘）"
        "macOS Intel（仅托盘）"
        "macOS Apple Silicon（仅托盘）"
        "macOS 安装包（Universal DMG，仅托盘）"
    )
else
    ART_SRC=(
        "dist/sip-phone-windows-amd64.exe"
        "dist/sip-phone-windows-386.exe"
        "dist/sip-phone-windows-arm64.exe"
        "dist/sip-phone-linux-amd64"
        "dist/sip-phone-linux-arm64"
        "dist/sip-phone-darwin-amd64"
        "dist/sip-phone-darwin-arm64"
        "dist/macos-dmg-full.dmg"
    )
    ART_TITLE=(
        "Windows 64 位（免安装）"
        "Windows 32 位（免安装）"
        "Windows ARM64（免安装，无视频/Opus/AEC）"
        "Linux 64 位"
        "Linux ARM64"
        "macOS Intel"
        "macOS Apple Silicon"
        "macOS 安装包（Universal DMG）"
    )
fi
# macOS 三项：两个架构的裸二进制（自动更新的下载目标，arch 用 x64/arm64）
# + universal DMG（首次安装包，arch=universal，kind=installer）。
# 裸二进制**不做 universal**：自动更新按 GOARCH 下载对应架构，单架构文件更小。
ART_OS=(windows windows windows linux linux macos macos macos)
ART_ARCH=(x64 x86 arm64 x64 arm64 x64 arm64 universal)
ART_KIND=(portable portable portable portable portable portable portable installer)

STAGE="$ROOT/dist/release/$APP_ID/$VERSION"
rm -rf "$STAGE"
mkdir -p "$STAGE"

FILES=()   # 发布文件名（相对版本目录）
HASHES=()  # 对应 sha256
SIZES=()   # 对应字节数
MISSING=()

for i in "${!ART_SRC[@]}"; do
    src="$ROOT/${ART_SRC[$i]}"
    if [ ! -f "$src" ]; then
        MISSING+=("${ART_SRC[$i]}")
        continue
    fi
    # 新鲜度：产物必须在本次构建开始之后写出，否则就是上次构建留下的旧二进制
    if [ "$DO_BUILD" = 1 ]; then
        mtime="$(stat -c %Y "$src" 2>/dev/null || echo 0)"
        if [ "$mtime" -lt "$BUILD_START" ]; then
            echo "跳过旧产物（mtime 早于本次构建，未重新生成）：${ART_SRC[$i]}"
            MISSING+=("${ART_SRC[$i]}（旧产物）")
            continue
        fi
    fi
    name="${APP_ID}-${ART_OS[$i]}-${ART_ARCH[$i]}"
    case "$src" in
        *.exe) name="$name.exe" ;;
        *.dmg) name="$name.dmg" ;;
    esac
    cp -f "$src" "$STAGE/$name"
    FILES+=("$name")
    HASHES+=("$(sha256sum "$STAGE/$name" | awk '{print $1}')")
    SIZES+=("$(stat -c %s "$STAGE/$name")")
done

if [ "${#FILES[@]}" -eq 0 ]; then
    echo "dist/ 里没有可发布的产物，发布中止" >&2
    exit 1
fi

echo "收集到 ${#FILES[@]} 个产物："
for i in "${!FILES[@]}"; do
    echo "  ${FILES[$i]}  ($(( SIZES[i] / 1048576 )) MB)"
done
if [ "${#MISSING[@]}" -gt 0 ]; then
    echo "以下产物未包含（缺失或未重新生成）："
    for m in "${MISSING[@]}"; do echo "  $m"; done
    echo "提示：Windows 只能产出 Windows 产物；Linux 产物需在 Linux 机器上跑 build.sh 后再发布。"
    echo "      macOS 产物只能在 macOS 上编：加 --darwin-ci，由 GitHub Actions 编好后自动下载回来。"
    if printf '%s\n' "${MISSING[@]}" | grep -q "windows-arm64"; then
        echo "      Windows ARM64 需要 llvm-mingw 工具链（缺了就跳过该目标）："
        echo "        Windows 解压到 %USERPROFILE%\\tools\\llvm-mingw，"
        echo "        Linux 用 LLVM_MINGW=<解压目录> 指定，详见 build.bat / build.sh 的提示。"
    fi
fi

# ---------- 版本元数据 ----------

# json_escape：转义 JSON 字符串里的反斜杠与双引号，并把换行压成空格
# （只用于单行的 title / channel；更新说明一律走 CHANGELOG.md，不拼进 JSON）
json_escape() {
    printf '%s' "$1" | tr '\n' ' ' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# 更新说明：优先取版本同名段落；预发布版（如 1.0.5-rc）回落到底版本段（1.0.5，
# 开发期该段落注明「待发布」）。都没有则不写 CHANGELOG.md，站点用 release.json 的 notes。
extract_changelog() {
    [ -f "$ROOT/changelog.md" ] || return 0
    awk -v ver="$1" '
        !found && index($0, "## [" ver "]") == 1 { found = 1; next }
        found && substr($0, 1, 3) == "## " { exit }
        found {
            if (!started && $0 ~ /^[[:space:]]*$/) next
            started = 1
            lines[++n] = $0
            if ($0 ~ /[^[:space:]]/) last = n
        }
        END { for (i = 1; i <= last; i++) print lines[i] }
    ' "$ROOT/changelog.md"
}

NOTES_FROM=""
CHANGELOG_TEXT="$(extract_changelog "$VERSION")"
if [ -n "$CHANGELOG_TEXT" ]; then
    NOTES_FROM="$VERSION"
else
    CHANGELOG_TEXT="$(extract_changelog "$VERSION_BASE")"
    if [ -n "$CHANGELOG_TEXT" ]; then
        NOTES_FROM="$VERSION_BASE"
    fi
fi
if [ -n "$CHANGELOG_TEXT" ]; then
    printf '%s\n' "$CHANGELOG_TEXT" > "$STAGE/CHANGELOG.md"
    echo "更新说明：取自 changelog.md 的 [$NOTES_FROM] 段"
else
    echo "提示：changelog.md 里没有 [$VERSION] / [$VERSION_BASE] 段，发布物不含 CHANGELOG.md"
fi

{
    printf '{\n'
    printf '  "channel": "%s",\n' "$(json_escape "${CHANNEL:-stable}")"
    printf '  "title": "%s",\n' "$(json_escape "$TITLE")"
    printf '  "publishedAt": "%s",\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf '  "prerelease": %s,\n' "$PRERELEASE"
    printf '  "mandatory": %s,\n' "$([ "$MANDATORY" = 1 ] && echo true || echo false)"
    printf '  "artifacts": [\n'
    for i in "${!FILES[@]}"; do
        sep=","
        if [ "$i" -eq $(( ${#FILES[@]} - 1 )) ]; then
            sep=""
        fi
        printf '    {"file": "%s", "name": "%s", "kind": "%s", "os": "%s", "arch": "%s", "sha256": "%s", "size": %s}%s\n' \
            "$(json_escape "${FILES[$i]}")" "$(json_escape "${ART_TITLE[$i]}")" "${ART_KIND[$i]}" \
            "${ART_OS[$i]}" "${ART_ARCH[$i]}" "${HASHES[$i]}" "${SIZES[$i]}" "$sep"
    done
    printf '  ]\n'
    printf '}\n'
} > "$STAGE/release.json"

# SHA256SUMS：只列产物（站点规范：<哈希>  <文件名>）
: > "$STAGE/SHA256SUMS"
for i in "${!FILES[@]}"; do
    printf '%s  %s\n' "${HASHES[$i]}" "${FILES[$i]}" >> "$STAGE/SHA256SUMS"
done

echo "打包目录：${STAGE#"$ROOT/"}"

# ---------- 打包（tar.gz；git-bash 没有 zip，站点按文件头识别格式，不看扩展名） ----------

TARBALL="$ROOT/dist/release/${APP_ID}-${VERSION}.tar.gz"
rm -f "$TARBALL"
tar -czf "$TARBALL" -C "$STAGE" .
echo "上传包：${TARBALL#"$ROOT/"}（$(stat -c %s "$TARBALL") 字节，$(tar -tzf "$TARBALL" | wc -l) 个条目）"

# ---------- 上传 ----------

if [ "$DRY" = 1 ]; then
    echo "dry-run：不上传（应用信息 / 图标 / 发布包都不写站点）。"
    echo "         产物在 dist/release/，确认无误后去掉 --dry-run 重跑。"
    return 0
fi

# api_put_file 把文件 PUT 到站点接口：2xx 成功，其余打印服务端错误后返回 1。
# 站点错误码含义见其《REST API》文档（400 字段非法/图片无法识别、401 令牌错、
# 403 站点未开上传、413 超过大小上限、503 归档目录不可写）。
api_put_file() {
    local label="$1" path="$2" url="$3" ctype="$4"
    local arg body code
    arg="$(cygpath -m "$path" 2>/dev/null || echo "$path")"
    body="$(mktemp)"
    code="$(curl -sS --max-time 120 "${CURL_TLS[@]}" -o "$body" -w '%{http_code}' -X PUT \
        -H "Authorization: Bearer $UPLOAD_TOKEN" \
        -H "Content-Type: $ctype" \
        --data-binary "@$arg" "$url")" || {
        echo "$label 写入失败：无法连接 $UPDATE_SITE" >&2
        rm -f "$body"
        return 1
    }
    if [ "$code" = 200 ] || [ "$code" = 201 ]; then
        echo "$label 已写入"
        rm -f "$body"
        return 0
    fi
    echo "$label 写入失败（HTTP $code）：" >&2
    cat "$body" >&2
    echo >&2
    case "$code" in
        400) echo "400：字段非法/超限或含未知字段（图标则是不认识的图片格式）" >&2 ;;
        401) echo "401：令牌缺失或错误（检查 release.conf 的 UPLOAD_TOKEN）" >&2 ;;
        403) echo "403：站点未开启上传功能" >&2 ;;
        413) echo "413：超过大小上限（图标最大 2 MB）" >&2 ;;
        503) echo "503：归档目录不可写（挂载成了只读？）" >&2 ;;
    esac
    rm -f "$body"
    return 1
}

# ---------- 应用级元数据（走站点接口，不直接动归档目录） ----------
# PUT /metadata 是**整份替换**（没写的字段会被清空），所以 release/app.json 要
# 写全；图标单独走 /icon（类型按文件内容判定，文件名由站点决定，别在 app.json
# 里手工写 icon 字段——站点保存图标时会自己把它指过去）。
echo "写入应用级元数据（站点接口）..."
api_put_file "应用信息 app.json" "$APP_JSON" \
    "$UPDATE_SITE/api/v1/apps/$APP_ID/metadata" "application/json" ||
    { echo "应用信息写入失败，发布中止（发布包未上传）" >&2; exit 1; }
api_put_file "应用图标 icon.svg" "$ROOT/release/icon.svg" \
    "$UPDATE_SITE/api/v1/apps/$APP_ID/icon" "image/svg+xml" ||
    { echo "应用图标写入失败，发布中止（发布包未上传）" >&2; exit 1; }

TARBALL_ARG="$(cygpath -m "$TARBALL" 2>/dev/null || echo "$TARBALL")"
BODY="$(mktemp)"
# 上传几十 MB，给个进度条：--progress-bar 走 stderr（终端可见），
# stdout 只留 -w 的状态码，仍能被命令替换干净地捕获（所以这里不用 -s/-sS）。
echo "上传发布包（$(stat -c %s "$TARBALL") 字节，$(basename "$TARBALL")）…"
CODE="$(curl -S --progress-bar --max-time 600 "${CURL_TLS[@]}" -o "$BODY" -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $UPLOAD_TOKEN" \
    -F "file=@$TARBALL_ARG" \
    -F "version=$VERSION" \
    "$UPDATE_SITE/api/v1/apps/$APP_ID/upload")" || {
    echo "上传失败：无法连接 $UPDATE_SITE" >&2
    rm -f "$BODY"
    exit 1
}

if [ "$CODE" = 201 ]; then
    echo "上传成功（201）："
    cat "$BODY"
    echo
    if grep -q '"replaced": *true' "$BODY"; then
        echo "注意：站点上已存在同名版本，已整体替换。"
    fi
else
    echo "上传失败（HTTP $CODE）：" >&2
    cat "$BODY" >&2
    echo >&2
    case "$CODE" in
        400) echo "400：应用 ID 非法、版本号无法确定、压缩包损坏或不含可安装文件" >&2 ;;
        401) echo "401：令牌缺失或错误（检查 release.conf 的 UPLOAD_TOKEN）" >&2 ;;
        403) echo "403：站点关闭了上传（UPLOAD_ENV 里 UPLOAD_ENABLED=false）" >&2 ;;
        413) echo "413：超过站点 MAX_UPLOAD 限制" >&2 ;;
        503) echo "503：归档目录不可写（挂载成了只读？）" >&2 ;;
    esac
    rm -f "$BODY"
    exit 1
fi
rm -f "$BODY"

# ---------- 发布后核对 ----------

PROBE="$UPDATE_SITE/api/v1/apps/$APP_ID/check?version=$VERSION_BASE&os=windows&arch=amd64&prerelease=true"
if CHECK="$(curl -sS --max-time 30 "${CURL_TLS[@]}" "$PROBE" 2>/dev/null)"; then
    if grep -q '"updateAvailable": *true' <<<"$CHECK"; then
        echo "站点核对：已能看到新版本（低版本客户端会收到更新提示）"
    else
        echo "站点核对：check 尚未反映新版本（扫描周期默认 15 秒，稍后可重试）"
    fi
fi

echo
echo "发布完成：$APP_NAME $VERSION"
echo "  Windows/Linux/macOS 下载页：$UPDATE_SITE/a/$APP_ID/$VERSION"
echo "  API：          $UPDATE_SITE/api/v1/apps/$APP_ID/releases/$VERSION"
echo "  最新版查询：    $UPDATE_SITE/api/v1/apps/$APP_ID/check?version=$VERSION_BASE&os=windows&arch=amd64&prerelease=true"
}

# ---------- 按变体逐个发布 ----------
# 默认两个应用一起发（完整版在前）；--only / --no-ui 时只发一个。
VARIANTS=()
if [ -z "$ONLY" ] || [ "$ONLY" = "full" ]; then
    VARIANTS+=(full)
fi
if [ -z "$ONLY" ] || [ "$ONLY" = "no-ui" ]; then
    VARIANTS+=(no-ui)
fi

FAILED=""
for v in "${VARIANTS[@]}"; do
    if ! publish_app "$v"; then
        # 一个应用失败不阻断另一个，最后统一报错退出
        FAILED="$FAILED $v"
    fi
done

if [ -n "$FAILED" ]; then
    echo >&2
    echo "以下应用发布失败：$FAILED" >&2
    exit 1
fi

echo
echo "全部完成：$VERSION（${VARIANTS[*]}）"
exit 0
