#!/bin/sh
# YC-7ZIP — 裸机（二进制）启动脚本
#
# 适用于直接用发布包在 Linux 上运行，不经过 Docker 的场景。
# Docker / 飞牛 fpk 部署不需要这个脚本。
#
# 用法：
#   ./start.sh [端口]        默认 8090
#   ./start.sh 9090

set -e

DIR=$(cd "$(dirname "$0")" && pwd)
PORT=${1:-8090}
DATA="$DIR/data"
LOG="$DIR/yc7zip.log"

# 7-Zip 必须可用：优先用发布包自带的 7zz，其次用系统里的
if [ -x "$DIR/7zz" ]; then
  export YC7ZIP_7Z="$DIR/7zz"
fi

# 按进程名精确匹配：进程的命令行是 "./yc7zip"，用绝对路径匹配不到。
pkill -x yc7zip 2>/dev/null || true
sleep 1

mkdir -p "$DATA"

setsid --fork "$DIR/yc7zip" \
  -addr ":$PORT" \
  -data "$DATA" \
  >>"$LOG" 2>&1 </dev/null

sleep 1

if pgrep -f "$DIR/yc7zip" >/dev/null 2>&1; then
  echo "YC-7ZIP 已启动：http://$(hostname -I 2>/dev/null | awk '{print $1}'):$PORT"
  echo "日志：$LOG"
else
  echo "启动失败，请查看日志：$LOG" >&2
  tail -20 "$LOG" >&2 || true
  exit 1
fi
