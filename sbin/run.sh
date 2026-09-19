#!/usr/bin/env bash
# sbin/run.sh — 开发模式运行（热重载）
# 用法: sbin/run.sh [wails3 dev 额外参数]
#   例: sbin/run.sh            # 默认端口/配置
#       sbin/run.sh -port 9245
source "$(dirname "$0")/common.sh"
require_wails3
log "启动开发模式：wails3 dev（Ctrl+C 退出）"
exec wails3 dev "$@"
