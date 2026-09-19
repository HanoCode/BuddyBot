#!/usr/bin/env bash
# sbin/common.sh — BuddyBot 脚本公共配置（被其他 sbin 脚本 source，不直接执行）
# shellcheck shell=bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
APP_NAME="BuddyBot"
UPDATE_REPO="HanoCode/BuddyBot" # GitHub 发布仓库（owner/repo）
WAILS_VERSION="v3.0.0-beta.23"  # 与 go.mod 保持一致

cd "$ROOT"

# go：优先 PATH，其次常见 Homebrew/系统路径
if ! command -v go >/dev/null 2>&1; then
  for cand in /opt/homebrew/bin /usr/local/go/bin; do
    if [ -x "$cand/go" ]; then
      export PATH="$cand:$PATH"
      break
    fi
  done
fi

require() { # require <cmd> <安装提示>
  command -v "$1" >/dev/null 2>&1 || { echo "错误：缺少 $1。$2" >&2; exit 1; }
}

require_wails3() {
  # 先补常规 GOPATH/bin，wails3 多装在这里
  [ -x "$HOME/go/bin/wails3" ] && export PATH="$HOME/go/bin:$PATH"
  command -v wails3 >/dev/null 2>&1 || {
    echo "wails3 未安装，正在安装（约 1-2 分钟）..." >&2
    go install "github.com/wailsapp/wails/v3/cmd/wails3@${WAILS_VERSION}"
    export PATH="$HOME/go/bin:$PATH"
  }
}

log()  { printf '\033[1;36m[sbin]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[sbin] 错误:\033[0m %s\n' "$*" >&2; exit 1; }
