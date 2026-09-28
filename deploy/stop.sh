#!/bin/sh
# YC-7ZIP — 停止裸机（二进制）进程

if pkill -x yc7zip; then
  echo "已停止 YC-7ZIP"
else
  echo "没有正在运行的 YC-7ZIP"
fi
