#!/bin/sh
# 生成浏览器验收用的样例数据（在 NAS 上运行）
#
# 注意：远程脚本一律以文件形式编辑上传，不要在 PowerShell 的双引号
# here-string 里写 —— 那里 $ROOT 会被 PowerShell 当成变量展开成空。

set -e

ROOT='/vol5/1000/空间4/YC-7ZIP/_uitest'

rm -rf "$ROOT"
mkdir -p "$ROOT/src/相册" "$ROOT/out"

echo "hello yc-7zip" > "$ROOT/src/说明.txt"
echo "中文文件名测试" > "$ROOT/src/相册/照片.txt"
head -c 3000000 /dev/urandom > "$ROOT/src/blob.bin"

mkdir -p "$ROOT/parts"
/root/yc7zip-test/7zz a -t7z -mx=1 -v512k "$ROOT/parts/movie.7z" "$ROOT/src/blob.bin" > /dev/null

echo "--- src ---"
ls -1 "$ROOT/src"
echo "--- parts ---"
ls -1 "$ROOT/parts"
