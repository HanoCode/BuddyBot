#!/usr/bin/env bash
# sbin/release.sh — 打 tag 并推送，触发 GitHub Actions 发布流水线
# 用法: sbin/release.sh <版本>
#   例: sbin/release.sh v1.0.1     （也可省略 v：release.sh 1.0.1）
#
# 前置条件：
#   1. 当前分支已全部提交（工作区干净）
#   2. 远程 origin 指向发布仓库（HanoCode/BuddyBot）
#   3. tag 不与历史重复
# 推送后由 .github/workflows/release.yml 自动构建三平台并创建 GitHub Release。
source "$(dirname "$0")/common.sh"

[ $# -eq 1 ] || fail "用法: sbin/release.sh <版本>（如 v1.0.1）"
VER="$1"
case "$VER" in v*) ;; *) VER="v$VER" ;; esac
[[ "$VER" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || fail "版本号格式应为 x.y.z：$1"

require git
BRANCH="$(git branch --show-current)"
BRANCH="${BRANCH:-main}"

git diff --quiet && git diff --cached --quiet || \
  fail "工作区有未提交改动，请先 commit。当前更改：\n$(git status --short)"
git fetch --tags origin >/dev/null 2>&1 || log "警告: 无法拉取远程 tag（离线？），仅校验本地"
[ -z "$(git tag -l "$VER")" ] || fail "tag $VER 已存在，请换一个版本号"

REMOTE_URL="$(git remote get-url origin 2>/dev/null || echo '(无远程)')"
log "分支=$BRANCH  远程=$REMOTE_URL  即将发布 $VER"
git tag -a "$VER" -m "release $VER"
git push origin "$BRANCH" && git push origin "$VER"

REPO_PATH="${REMOTE_URL#*github.com[:/]}"; REPO_PATH="${REPO_PATH%.git}"
if [ "$REPO_PATH" != "$REMOTE_URL" ]; then
  log "已推送。CI 进度: https://github.com/$REPO_PATH/actions"
  log "发布页:   https://github.com/$REPO_PATH/releases/tag/$VER"
else
  log "已推送。当前远程不是 GitHub，CI 不会触发；请确认 origin 指向 github.com/$UPDATE_REPO"
fi
