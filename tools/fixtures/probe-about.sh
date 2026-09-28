#!/bin/sh
# 定位飞牛「关于」页的文字来源
echo "=== /vol1/@appmeta/yc7zip 全部内容 ==="
find /vol1/@appmeta/yc7zip -maxdepth 3 2>/dev/null

echo
echo "=== yc7zip 里每个文件的开头 ==="
for f in $(find /vol1/@appmeta/yc7zip -maxdepth 3 -type f 2>/dev/null | head -10); do
  echo "--- $f ($(wc -c < "$f") bytes)"
  head -c 400 "$f" | cat -v
  echo
done

echo
echo "=== 对照 xinZip 的 appmeta ==="
find /vol1/@appmeta/xinZip -maxdepth 3 2>/dev/null

echo
echo "=== 哪些文件同时含 Docker 和 fnOS ==="
grep -rl 'fnOS' /vol1/@appmeta /var/apps /vol1/@appconf 2>/dev/null | head -10
grep -rl 'Docker' /vol1/@appmeta/yc7zip /var/apps/yc7zip 2>/dev/null | head -10

echo
echo "=== 飞牛的元数据库文件 ==="
find /usr/trim/var /var/lib/trim -maxdepth 3 \( -name '*.db' -o -name '*.db3' -o -name '*.sqlite*' \) 2>/dev/null | head -10

echo
echo "=== appcenter 进程与它的数据目录 ==="
ps aux | grep -i appcent | grep -v grep | head -3
ls -la /usr/trim/var/ 2>/dev/null | head -20
