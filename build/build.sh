#!/bin/bash
# ============================================
#  ClipSync 完整离线开发包
#  本包自带 Go 工具链(go-toolchain)，解压后直接运行：
#     bash build.sh
#  即可编译 macOS 版(ClipSync-mac) 和 Windows 版(ClipSync.exe)
#  全程离线，无需安装 Go、无需下载任何依赖。
#
#  说明：
#   - Windows 版(exe)编译无需任何额外环境，一定可以出。
#   - macOS 版编译需要系统有 clang（Xcode 命令行工具）。
#     若没有，脚本会提示并跳过 macOS 版，仍正常出 exe。
# ============================================
set -e

# 脚本位于 build/ 目录：以脚本所在目录的父目录作为项目根，
# 使源码 src/ 与产物 build/ 都能被正确定位（脚本放根目录或 build/ 下均可运行）
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [ "$(basename "$SCRIPT_DIR")" = "build" ]; then
  DIR="$(dirname "$SCRIPT_DIR")"
else
  DIR="$SCRIPT_DIR"
fi
GO="$DIR/go-toolchain/bin/go"

# zip 解压不保留执行权限，这里补上，确保自带工具链可运行
if [ -d "$DIR/go-toolchain" ]; then
  chmod +x "$GO" "$DIR/go-toolchain/bin/gofmt" 2>/dev/null || true
  find "$DIR/go-toolchain/pkg/tool" -type f -exec chmod +x {} \; 2>/dev/null || true
fi

# 兜底：若包内无工具链，尝试用系统 go
if [ ! -x "$GO" ]; then
  GO="$(command -v go || true)"
fi
if [ -z "$GO" ] || ! "$GO" version >/dev/null 2>&1; then
  echo "错误：未找到 Go 工具链（包内 go-toolchain 缺失，或系统未装 Go）"
  exit 1
fi

# 产物统一输出到 build/ 目录，保持根目录清爽（只留源码 src/ 与 build.sh）
mkdir -p "$DIR/build"

cd "$DIR/src"

# ---- 版本号：唯一来源是 src/version.go 里的 appVersion，这里只读取不做修改 ----
# 改版本号请改 version.go，不要在本脚本里写死，否则两处会打架。
VER="$(sed -n 's/^const appVersion = "\([0-9.]*\)"/\1/p' version.go)"
if [ -z "$VER" ]; then
  echo "错误：无法从 src/version.go 读取 appVersion"
  exit 1
fi
# 构建标识：日期，写进 -ldflags，让托盘/日志能区分同一版本号的多次构建。
# 只到天不带时分——它要显示在托盘菜单里，"20261028-2142" 那串太长。
BUILD_STAMP="$(date +%Y%m%d)"
VER_MAJOR="$(echo "$VER" | cut -d. -f1)"
VER_MINOR="$(echo "$VER" | cut -d. -f2)"
VER_PATCH="$(echo "$VER" | cut -d. -f3)"
: "${VER_MINOR:=0}"
: "${VER_PATCH:=0}"
# 版本资源里的版本串直接写 appVersion（1.0），跟托盘菜单保持一致。
# 早期版本在这里补零成四段 1.0.0.0 喂进去，结果 exe 属性页显示 1.0.0.0，
# 跟界面上的 v1.0 对不上。四段是 VS_FIXEDFILEINFO 内部 DWORD 的存储形式，
# 不是给人看的，属性页读的是 StringFileInfo 里的字符串。
FILE_VER="$VER"

echo ">>> ClipSync v${VER}（构建 ${BUILD_STAMP}）"

# Windows 版本资源（exe 右键→属性→详细信息 里显示的内容）。
# 生成的 .syso 会被 go build 自动链接进 Windows 版；文件名带 _windows_amd64
# 后缀，编译 macOS 版时自动忽略，不需要删。
# 注意 goversioninfo 默认会生成 ARM64 对象，必须显式 -arm=false -64=true，
# 否则链接报 "too many .rsrc sections"（架构不符）。
if command -v goversioninfo >/dev/null 2>&1; then
  echo ">>> 生成 Windows 版本资源 ..."
  VER_ERR="$(mktemp)"
  if ! goversioninfo -arm=false -64=true -o resource_windows_amd64.syso \
      -ver-major "$VER_MAJOR" -ver-minor "$VER_MINOR" -ver-patch "$VER_PATCH" -ver-build 0 \
      -file-version "$FILE_VER" -product-version "$FILE_VER" \
      -product-name "ClipSync" -internal-name "ClipSync" -original-name "ClipSync.exe" \
      -description "ClipSync 跨设备剪贴板/文件同步" -company "ClipSync" \
      -copyright "Copyright (C) 2026 ClipSync" \
      -icon icon.ico >/dev/null 2>"$VER_ERR"; then
    echo "错误：生成 Windows 版本资源失败"
    cat "$VER_ERR"
    rm -f "$VER_ERR"
    exit 1
  fi
  # goversioninfo 只认 x.y.z 三段，两段的 "1.0" 会打两条 "could not be parsed" 警告。
  # 这是预期内的：字符串值照样原样写入（属性页就显示 1.0），数字字段由 -ver-* 参数决定。
  grep -v "could not be parsed" "$VER_ERR" >&2 || true
  rm -f "$VER_ERR"
else
  echo ">>> 跳过版本资源：未安装 goversioninfo（沿用 src/resource_windows_amd64.syso 里已有的版本信息）"
fi

echo ">>> 开始编译（使用 ${GO}）"

if command -v clang >/dev/null 2>&1; then
  echo ">>> 编译 macOS 版 ..."
  "$GO" build -mod=vendor -ldflags="-X main.appBuild=$BUILD_STAMP" -o "$DIR/build/ClipSync-mac" .
  echo ">>> macOS 版完成: $DIR/build/ClipSync-mac"
else
  echo ">>> 跳过 macOS 版：系统未找到 clang（需 Xcode 命令行工具），Windows 版不受影响"
fi

# Windows 版 exe 输出到根目录（与安装包 dmg 一起，习惯放根目录）
echo ">>> 编译 Windows 版 ..."
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 "$GO" build -mod=vendor \
  -ldflags="-H windowsgui -X main.appBuild=$BUILD_STAMP" -o "$DIR/ClipSync.exe" .
echo ">>> Windows 版完成: $DIR/ClipSync.exe"

# 自检：确认 exe 里真的是期望的版本串。goversioninfo 解析不了两段版本号时只打
# warning 不报错，曾经就是因为这个静默写出过 0.0.0.0，属性页看起来"没版本号"。
if command -v python3 >/dev/null 2>&1; then
  python3 - "$DIR/ClipSync.exe" "$FILE_VER" <<'PYEOF'
import sys
data = open(sys.argv[1], 'rb').read()
want = sys.argv[2]
if want.encode('utf-16-le') not in data:
    sys.exit("错误：exe 里找不到版本串 %s，版本资源没嵌进去" % want)
print(">>> 版本资源自检通过：FileVersion = %s" % want)
PYEOF
fi

# 打包 macOS 的 .app 与 dmg（需要 macOS + 已有的 .app 骨架/图标；失败不影响已产出的 exe）
APP="$DIR/build/ClipSync.app"
if [ -d "$APP" ] && command -v hdiutil >/dev/null 2>&1; then
  PLIST="$APP/Contents/Info.plist"

  # 版本号同步进 Info.plist：Finder「显示简介」、系统报告、`mdls` 读的都是这里。
  # 用 Set 优先、失败再 Add，这样 plist 里原本有没有这两个键都不影响。
  if [ -f "$PLIST" ]; then
    /usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $VER" "$PLIST" 2>/dev/null \
      || /usr/libexec/PlistBuddy -c "Add :CFBundleShortVersionString string $VER" "$PLIST"
    # CFBundleVersion 是构建号。Apple 要求它只能由数字和点组成（1~4 段）；
    # BUILD_STAMP 本身就是纯数字日期（20261028），可以直接用。
    PLIST_BUILD="$BUILD_STAMP"
    /usr/libexec/PlistBuddy -c "Set :CFBundleVersion $PLIST_BUILD" "$PLIST" 2>/dev/null \
      || /usr/libexec/PlistBuddy -c "Add :CFBundleVersion string $PLIST_BUILD" "$PLIST"
    echo ">>> Info.plist 版本号: ${VER}（构建 ${BUILD_STAMP}）"
  fi

  # 把刚编出来的二进制塞进 .app，否则 dmg 里还是上一次的可执行文件
  if [ -f "$DIR/build/ClipSync-mac" ]; then
    cp "$DIR/build/ClipSync-mac" "$APP/Contents/MacOS/ClipSync"
    chmod +x "$APP/Contents/MacOS/ClipSync"
    # ad-hoc 签名：改了二进制必须重新签，否则 Gatekeeper 认为包被篡改
    codesign --force --deep --sign - "$APP" >/dev/null 2>&1 && echo ">>> .app 已重新签名"
  fi

  rm -rf /tmp/clipsync-dmgroot
  mkdir -p /tmp/clipsync-dmgroot
  ditto "$APP" /tmp/clipsync-dmgroot/ClipSync.app
  ln -s /Applications /tmp/clipsync-dmgroot/Applications
  rm -f "$DIR/ClipSync.dmg"
  # -fs HFS+ 不能省：新版 macOS 默认用 APFS 建镜像，而 APFS 下 -volname 会被
  # 忽略，卷名退化成源目录名，装机时看到的就是一个没有版本号的 "ClipSync"。
  if hdiutil create -volname "ClipSync ${VER}" -srcfolder /tmp/clipsync-dmgroot -fs HFS+ \
      -ov -format UDZO "$DIR/ClipSync.dmg" >/dev/null 2>&1; then
    echo ">>> dmg 完成: $DIR/ClipSync.dmg"
  else
    echo ">>> dmg 打包失败（不影响已产出的 exe）"
  fi
fi

echo ""
echo "全部完成。版本 v${VER}，构建 ${BUILD_STAMP}。"
