#!/usr/bin/env bash
# macOS 打包：把 dist/ 里的 darwin 二进制打成 .app，再打成 .dmg（拖到「应用程序」）。
#
# 用法（须在 macOS 上跑）：
#   ./scripts/package-macos.sh                  # 默认：仅托盘无 UI 版
#   ./scripts/package-macos.sh --variant both   # 完整版 + 无 UI 各出一个 DMG
#   ./scripts/package-macos.sh --arch universal # universal（Intel + Apple Silicon）包，推荐
#   ./scripts/package-macos.sh --arch arm64     # 只打某个架构（默认取本机 go env GOARCH）
#   ./scripts/package-macos.sh --build          # 缺产物时先 ./build.sh --target darwin
#
# 产物（dist/macos/）：
#   MTI软电话.app（完整版）/ MTI软电话-无UI版.app
#   MTI-SIP-Phone[-no-ui|-full]-<版本>-darwin-<arch>.dmg
#
# universal：build.sh 在 macOS 上会同时编 amd64 与 arm64，这里用 lipo 合并后
# **重新做 ad-hoc 签名**（lipo 会破坏每个架构切片里 Go 自带的签名；Apple Silicon
# 上签名无效的二进制会被内核直接杀掉）。
#
# 说明：未做 Apple 开发者签名 / 公证。内网分发可先右键打开，或执行
#   xattr -cr /Applications/MTI软电话.app
# 清除隔离属性。.app 与 Info.plist 的字段与自安装（internal/macbundle）一致：
# bundle 标识、图标文件名、sip/sips URL scheme、LSUIElement。
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$(pwd)"

if [ "$(uname -s)" != "Darwin" ]; then
    echo "只能在 macOS 上打包（当前：$(uname -s)）" >&2
    exit 1
fi

VARIANT="noui"   # noui | full | both
DO_BUILD=0
NATIVE_ARCH="$(go env GOARCH 2>/dev/null || uname -m)"
case "$NATIVE_ARCH" in
    arm64|aarch64) NATIVE_ARCH=arm64 ;;
    x86_64|amd64)  NATIVE_ARCH=amd64 ;;
esac
ARCH="$NATIVE_ARCH"   # arm64 | amd64 | universal

while [ $# -gt 0 ]; do
    case "$1" in
        --variant) VARIANT="${2:?}"; shift ;;
        --arch)    ARCH="${2:?}"; shift ;;
        --build)   DO_BUILD=1 ;;
        -h|--help)
            sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "未知参数：$1（--help 看用法）" >&2; exit 2 ;;
    esac
    shift
done

case "$VARIANT" in
    noui|full|both) ;;
    *) echo "--variant 只能是 noui / full / both" >&2; exit 2 ;;
esac
case "$ARCH" in
    arm64|amd64|universal) ;;
    *) echo "--arch 只能是 arm64 / amd64 / universal" >&2; exit 2 ;;
esac

# 版本：与 build.sh 一致（开发版带 -rc）
VERSION_BASE="$(sed -n 's/^var Version = "\([^"]*\)".*$/\1/p' internal/core/buildinfo.go)"
if [ -z "$VERSION_BASE" ]; then
    echo "无法从 internal/core/buildinfo.go 解析版本" >&2
    exit 1
fi
if [ "${SIP_PHONE_RELEASE:-}" = "1" ]; then
    APP_VERSION="$VERSION_BASE"
else
    APP_VERSION="${VERSION_BASE}-rc"
fi

need_go_path() {
    if command -v go >/dev/null 2>&1; then
        return 0
    fi
    for d in "$HOME/sdk/go/bin" /usr/local/go/bin /opt/homebrew/bin; do
        if [ -x "$d/go" ]; then
            export PATH="$d:$PATH"
            return 0
        fi
    done
    return 1
}

if [ "$DO_BUILD" = "1" ]; then
    need_go_path || { echo "未找到 go，无法 --build" >&2; exit 1; }
    export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
    # --target darwin 一次出两个架构（打包脚本自己决定要哪份）
    ./build.sh --target darwin
fi

PNG_SRC="$ROOT/internal/appicon/assets/mti-phone-green.png"
if [ ! -f "$PNG_SRC" ]; then
    echo "缺少图标：$PNG_SRC" >&2
    exit 1
fi

OUT_DIR="$ROOT/dist/macos"
mkdir -p "$OUT_DIR"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/mti-macos-pkg.XXXXXX")"
trap 'rm -rf "$WORKDIR"' EXIT

# 从 PNG 生成 .icns（iconutil 要标准 iconset 文件名）
make_icns() {
    local png="$1" icns="$2"
    local set="$WORKDIR/app.iconset"
    rm -rf "$set"
    mkdir -p "$set"
    # 源图 256；不足 512/1024 时 sips 放大，观感够内测用
    sips -z 16 16     "$png" --out "$set/icon_16x16.png" >/dev/null
    sips -z 32 32     "$png" --out "$set/icon_16x16@2x.png" >/dev/null
    sips -z 32 32     "$png" --out "$set/icon_32x32.png" >/dev/null
    sips -z 64 64     "$png" --out "$set/icon_32x32@2x.png" >/dev/null
    sips -z 128 128   "$png" --out "$set/icon_128x128.png" >/dev/null
    sips -z 256 256   "$png" --out "$set/icon_128x128@2x.png" >/dev/null
    sips -z 256 256   "$png" --out "$set/icon_256x256.png" >/dev/null
    sips -z 512 512   "$png" --out "$set/icon_256x256@2x.png" >/dev/null
    sips -z 512 512   "$png" --out "$set/icon_512x512.png" >/dev/null
    sips -z 1024 1024 "$png" --out "$set/icon_512x512@2x.png" >/dev/null
    iconutil -c icns "$set" -o "$icns"
}

ICNS="$WORKDIR/AppIcon.icns"
make_icns "$PNG_SRC" "$ICNS"

# 写 Info.plist；$1=可执行文件名 $2=bundle id $3=显示名 $4=LSUIElement true|false
# 字段与 internal/macbundle 的 renderPlist 保持一致（两条安装路径行为一致）。
write_plist() {
    local exe_name="$1" bundle_id="$2" display_name="$3" agent="$4" plist="$5"
    cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>zh-Hans</string>
	<key>CFBundleExecutable</key>
	<string>${exe_name}</string>
	<key>CFBundleIconFile</key>
	<string>AppIcon</string>
	<key>CFBundleIdentifier</key>
	<string>${bundle_id}</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>${display_name}</string>
	<key>CFBundleDisplayName</key>
	<string>${display_name}</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>${APP_VERSION}</string>
	<key>CFBundleVersion</key>
	<string>${APP_VERSION}</string>
	<key>LSMinimumSystemVersion</key>
	<string>12.0</string>
	<key>LSUIElement</key>
	<${agent}/>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSMicrophoneUsageDescription</key>
	<string>MTI 软电话需要使用麦克风进行语音通话。</string>
	<key>NSCameraUsageDescription</key>
	<string>MTI 软电话需要使用摄像头进行视频通话（若已启用视频）。</string>
	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLName</key>
			<string>${bundle_id}</string>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>sip</string>
				<string>sips</string>
			</array>
		</dict>
	</array>
</dict>
</plist>
EOF
}

# 取某个变体的可执行文件：$1=产物前缀（sip-phone / sip-phone-no-ui）
# 输出 dist/macos 下的最终路径（universal 时就地 lipo 合并）。
stage_binary() {
    local stem="$1"
    if [ "$ARCH" = "universal" ]; then
        local out="$ROOT/dist/${stem}-darwin-universal"
        local src_a="$ROOT/dist/${stem}-darwin-amd64"
        local src_b="$ROOT/dist/${stem}-darwin-arm64"
        for f in "$src_a" "$src_b"; do
            if [ ! -x "$f" ]; then
                echo "缺少可执行文件：$f" >&2
                echo "请先：./build.sh --target darwin  或加 --build" >&2
                exit 1
            fi
        done
        lipo -create "$src_a" "$src_b" -output "$out"
        chmod 755 "$out"
        # lipo 合并后每个切片的签名不再可信：重新 ad-hoc 签名（Apple Silicon
        # 上没有有效签名的二进制无法执行）
        codesign --force --sign - "$out" >/dev/null
        echo "$out"
        return 0
    fi
    local f="$ROOT/dist/${stem}-darwin-${ARCH}"
    if [ ! -x "$f" ]; then
        echo "缺少可执行文件：$f" >&2
        echo "请先：./build.sh --target darwin-${ARCH}  或加 --build" >&2
        exit 1
    fi
    echo "$f"
}

# 打一个变体：$1=noui|full
package_one() {
    local kind="$1"
    local stem app_display bundle_id agent dmg_stem exe_in_app
    case "$kind" in
        noui)
            stem="sip-phone-no-ui"
            app_display="MTI软电话-无UI版"
            bundle_id="cn.mti.sip-phone-no-ui"
            agent="true"   # 仅托盘，不占 Dock
            dmg_stem="MTI-SIP-Phone-no-ui"
            exe_in_app="mti-sip-phone-no-ui"
            ;;
        full)
            stem="sip-phone"
            app_display="MTI软电话"
            bundle_id="cn.mti.sip-phone"
            agent="false"
            dmg_stem="MTI-SIP-Phone-full"
            exe_in_app="mti-sip-phone"
            ;;
        *) echo "内部错误：未知变体 $kind" >&2; exit 1 ;;
    esac

    local src
    src="$(stage_binary "$stem")"

    local app_name="${app_display}.app"
    local app_path="$OUT_DIR/$app_name"
    rm -rf "$app_path"
    mkdir -p "$app_path/Contents/MacOS" "$app_path/Contents/Resources"

    cp "$src" "$app_path/Contents/MacOS/$exe_in_app"
    chmod 755 "$app_path/Contents/MacOS/$exe_in_app"
    cp "$ICNS" "$app_path/Contents/Resources/AppIcon.icns"
    write_plist "$exe_in_app" "$bundle_id" "$app_display" "$agent" \
        "$app_path/Contents/Info.plist"

    # ad-hoc 签名应用包（尽力而为：没有开发者证书时也能让 Gatekeeper 少拦一层）
    if ! codesign --force --deep --sign - "$app_path" >/dev/null 2>&1; then
        echo "提示：应用包 ad-hoc 签名失败（未影响打包；首次打开需右键「打开」）" >&2
    fi

    # DMG：App + Applications 快捷方式
    local stage="$WORKDIR/dmg-$kind"
    rm -rf "$stage"
    mkdir -p "$stage"
    cp -R "$app_path" "$stage/"
    ln -s /Applications "$stage/Applications"

    local dmg="$OUT_DIR/${dmg_stem}-${APP_VERSION}-darwin-${ARCH}.dmg"
    rm -f "$dmg"
    hdiutil create \
        -volname "$app_display" \
        -srcfolder "$stage" \
        -ov -format UDZO \
        "$dmg" >/dev/null

    echo "已生成："
    echo "  $app_path"
    echo "  $dmg"
}

case "$VARIANT" in
    noui) package_one noui ;;
    full) package_one full ;;
    both)
        package_one noui
        package_one full
        ;;
esac

echo "版本 ${APP_VERSION} / darwin-${ARCH} 打包完成 → $OUT_DIR/"
ls -la "$OUT_DIR"
