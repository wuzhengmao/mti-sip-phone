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
#   ./scripts/release.sh --no-ui --pre    # 「仅托盘无 UI 版」：独立应用 ID，见 release-no-ui.conf
#   ./scripts/release.sh --title "体验优化版" --mandatory
#
# 配置：release/release.conf（站点地址 / 应用 ID / 上传令牌 / 可选站点证书），
#       同名环境变量可覆盖（UPDATE_SITE / APP_ID / UPLOAD_TOKEN / CA_CERT /
#       INSECURE_TLS）。站点启用 TLS 时 UPDATE_SITE 必须直接写 https 地址
#       （HTTP 端口会 301 跳转，而 POST 跟随 301 会退化成 GET）。
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
#   - Windows 上一键只能产出 Windows 产物（Linux 产物由 Linux 机器上的 build.sh 产出）；
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
  ./scripts/release.sh --no-ui          # 发布「仅托盘无 UI 版」（独立应用，配置见 release-no-ui.conf）
  ./scripts/release.sh --title "体验优化版" --mandatory
选项：--pre / --dry-run / --no-build / --no-ui / -v <版本> / --title <标题> / --channel <通道> / --mandatory
配置：release/release.conf（--no-ui 时读 release/release-no-ui.conf）：
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
NOUI=0

while [ $# -gt 0 ]; do
    case "$1" in
        --pre) PRE=1 ;;
        --dry-run) DRY=1 ;;
        --no-build) DO_BUILD=0 ;;
        --no-ui) NOUI=1 ;;
        -v|--version) VERSION="${2:-}"; shift ;;
        --title) TITLE="${2:-}"; shift ;;
        --channel) CHANNEL="${2:-}"; shift ;;
        --mandatory) MANDATORY=1 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
    esac
    shift
done

# ---------- 配置 ----------

# --no-ui：换配置与元数据文件（应用 ID、令牌、产物清单都不同）
APP_JSON="$ROOT/release/app.json"
if [ "$NOUI" = "1" ]; then
    CONF="$ROOT/release/release-no-ui.conf"
    APP_JSON="$ROOT/release/app-no-ui.json"
fi

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

# ---------- 版本 ----------

VERSION_BASE="$(sed -n 's/^var Version = "\(.*\)"$/\1/p' "$ROOT/internal/core/buildinfo.go")"
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
[ -n "$TITLE" ] || TITLE="${APP_ID} ${VERSION}"

echo "发布配置：站点 $UPDATE_SITE，应用 $APP_ID，版本 $VERSION（pre=$PRE dry-run=$DRY）"

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

if [ "$DO_BUILD" = 1 ]; then
    if [ "$HOST" = windows ]; then
        # Windows 上必须走 build.bat：git-bash 里跑 build.sh 会因 GOOS 默认
        # windows 而产出「名为 linux-* 的 Windows 二进制」。
        BUILD_BAT="$(cygpath -w "$ROOT/build.bat" 2>/dev/null || echo "$ROOT/build.bat")"
        if [ "$PRERELEASE" = true ]; then
            echo "编译（开发版，版本带 -rc）：build.bat"
            cmd //c "$BUILD_BAT"
        else
            echo "编译（正式版，SIP_PHONE_RELEASE=1）：build.bat"
            SIP_PHONE_RELEASE=1 cmd //c "$BUILD_BAT"
        fi
    else
        if [ "$PRERELEASE" = true ]; then
            echo "编译（开发版，版本带 -rc）：build.sh"
            bash "$ROOT/build.sh"
        else
            echo "编译（正式版，SIP_PHONE_RELEASE=1）：build.sh"
            SIP_PHONE_RELEASE=1 bash "$ROOT/build.sh"
        fi
    fi
else
    echo "跳过编译（--no-build）：直接使用 dist/ 里已有的产物"
fi

# ---------- 收集产物 ----------
# 源文件 → 发布文件名 <APP_ID>-<平台>-<架构>[.exe]（固定不含版本号：同一平台的
# 下载地址跨版本不变，用户与脚本收藏的链接不会失效；版本靠版本目录区分，
# 站点文档也允许这种命名——文件名里有平台/架构词即可，何况 release.json 还显式声明了）。
# 只列托盘 GUI 产物：sip-phone-console.exe 是同平台第二个候选，不发布。
# windows-arm64 是**基础版**（无视频/Opus/音频预处理：这三套静态库还没有 arm64 版本，
# 见 build.bat / build.sh 的说明），标题里写明，免得 ARM64 用户以为是完整功能版；
# 该产物需要 llvm-mingw 工具链，没装时构建脚本会跳过它，这里按"缺失"处理（不影响其余）。
# 变体：完整版 = dist/sip-phone-*；仅托盘无 UI 版 = dist/sip-phone-no-ui-*（--no-ui）。
if [ "$NOUI" = "1" ]; then
    ART_SRC=(
        "dist/sip-phone-no-ui-windows-amd64.exe"
        "dist/sip-phone-no-ui-windows-386.exe"
        "dist/sip-phone-no-ui-windows-arm64.exe"
        "dist/sip-phone-no-ui-linux-amd64"
        "dist/sip-phone-no-ui-linux-arm64"
    )
    ART_TITLE=(
        "Windows 64 位（免安装，仅托盘）"
        "Windows 32 位（免安装，仅托盘）"
        "Windows ARM64（免安装，仅托盘，无视频/Opus/AEC）"
        "Linux 64 位（仅托盘）"
        "Linux ARM64（仅托盘）"
    )
else
    ART_SRC=(
        "dist/sip-phone-windows-amd64.exe"
        "dist/sip-phone-windows-386.exe"
        "dist/sip-phone-windows-arm64.exe"
        "dist/sip-phone-linux-amd64"
        "dist/sip-phone-linux-arm64"
    )
    ART_TITLE=(
        "Windows 64 位（免安装）"
        "Windows 32 位（免安装）"
        "Windows ARM64（免安装，无视频/Opus/AEC）"
        "Linux 64 位"
        "Linux ARM64"
    )
fi
ART_OS=(windows windows windows linux linux)
ART_ARCH=(x64 x86 arm64 x64 arm64)

STAGE="$ROOT/dist/release/$VERSION"
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
    case "$src" in *.exe) name="$name.exe" ;; esac
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
        printf '    {"file": "%s", "name": "%s", "kind": "portable", "os": "%s", "arch": "%s", "sha256": "%s", "size": %s}%s\n' \
            "$(json_escape "${FILES[$i]}")" "$(json_escape "${ART_TITLE[$i]}")" \
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
    exit 0
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
echo "发布完成：$VERSION"
echo "  Windows 下载页：$UPDATE_SITE/a/$APP_ID/$VERSION"
echo "  API：          $UPDATE_SITE/api/v1/apps/$APP_ID/releases/$VERSION"
echo "  最新版查询：    $UPDATE_SITE/api/v1/apps/$APP_ID/check?version=$VERSION_BASE&os=windows&arch=amd64&prerelease=true"
exit 0
