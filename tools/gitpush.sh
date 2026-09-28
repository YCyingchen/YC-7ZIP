#!/usr/bin/env bash
# 推送并核对远端。令牌从 .env.local 读，不经过命令行参数，
# 避免出现在进程列表里，也避开 PowerShell 对 $( ) 的处理。
#
#   bash tools/gitpush.sh            # 推送 main 并核对
#   bash tools/gitpush.sh --tags     # 连同标签一起推
set -euo pipefail

cd "$(dirname "$0")/.."

if [ ! -f .env.local ]; then
  echo "缺少 .env.local" >&2
  exit 1
fi

TOKEN=$(grep -m1 '^GITHUB_TOKEN=' .env.local | cut -d= -f2- | tr -d ' \r')
if [ -z "$TOKEN" ]; then
  echo ".env.local 里没有 GITHUB_TOKEN" >&2
  exit 1
fi

# 直连 github.com 经常超时（这个网络环境对 443 很不友好），走本地代理
PROXY=$(grep -m1 '^NAS_PROXY=' .env.local | cut -d= -f2- | tr -d ' \r')
PROXY=${PROXY:-http://192.168.1.8:7890}
GIT=(-c "http.proxy=$PROXY" -c "https.proxy=$PROXY")

REMOTE="https://x-access-token:${TOKEN}@github.com/YCyingchen/YC-7ZIP.git"

echo "代理      : $PROXY"
echo "本地 HEAD : $(git rev-parse --short HEAD)"
echo "远端 main : $(git "${GIT[@]}" ls-remote "$REMOTE" refs/heads/main 2>/dev/null | cut -c1-7)"

git "${GIT[@]}" push "$REMOTE" HEAD:main "$@" 2>&1 | tail -2 || true

echo "--- 推送后 ---"
echo "本地 HEAD : $(git rev-parse --short HEAD)"
REMOTE_SHA=$(git "${GIT[@]}" ls-remote "$REMOTE" refs/heads/main 2>/dev/null | cut -f1)
echo "远端 main : $(printf '%s' "$REMOTE_SHA" | cut -c1-7)"

if [ "$(git rev-parse HEAD)" = "$REMOTE_SHA" ]; then
  echo "✅ 已同步"
else
  echo "❌ 未同步（可能是网络问题，重跑一次）"
  exit 1
fi
