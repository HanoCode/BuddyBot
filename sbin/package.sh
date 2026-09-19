#!/usr/bin/env bash
# sbin/package.sh — 本地打包生产版本
# 用法: sbin/package.sh [--zip]
#   darwin:  bin/BuddyBot.app；--zip 额外产出 bin/BuddyBot-darwin-<arch>.zip（与发布产物同名，可用于应用内自动更新）
#   windows: bin/BuddyBot.exe（便携版），并复制为 bin/BuddyBot-windows-amd64.exe
# 可选环境变量: ARCH=arm64|amd64（默认本机架构）
source "$(dirname "$0")/common.sh"
require_wails3
ZIP=0
for arg in "$@"; do case "$arg" in --zip) ZIP=1 ;; *) fail "未知参数: $arg（仅支持 --zip）" ;; esac; done

VERSION="${APP_VERSION:-$(git describe --tags --abbrev=0 2>/dev/null || echo 0.0.0-dev)}"
VERSION="${VERSION#v}"
log "打包生产版本 v$VERSION（ARCH=${ARCH:-默认}）"

OS="$(uname -s)"
if [ "$OS" = "Darwin" ]; then
  wails3 task darwin:package ARCH="${ARCH:-$(uname -m | sed 's/aarch64/arm64/;s/x86_64/amd64/')}" APP_VERSION="$VERSION"
  log "产物: bin/$APP_NAME.app"
  if [ "$ZIP" = "1" ]; then
    ARCH_N="${ARCH:-$(uname -m | sed 's/aarch64/arm64/;s/x86_64/amd64/')}"
    ditto -c -k --sequesterRsrc --keepParent \
      "bin/$APP_NAME.app" "bin/$APP_NAME-darwin-$ARCH_N.zip"
    log "发布包: bin/$APP_NAME-darwin-$ARCH_N.zip"
  fi
elif [ "$OS" = "MINGW64_NT"* ] || [ "$OS" = "MSYS_NT"* ]; then
  wails3 task windows:build APP_VERSION="$VERSION"
  cp "bin/$APP_NAME.exe" "bin/$APP_NAME-windows-amd64.exe"
  log "产物: bin/$APP_NAME.exe（便携版）/ bin/$APP_NAME-windows-amd64.exe"
else
  fail "当前脚本仅支持 macOS/Windows 打包；Linux 请直接使用 CI"
fi

log "完成"
