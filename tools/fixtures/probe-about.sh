#!/bin/sh
# 飞牛的「关于」页文字来自哪里？对比已装应用
echo "=== /vol1/@appmeta/yc7zip ==="
find /vol1/@appmeta/yc7zip -maxdepth 3 2>/dev/null | head -20

echo
echo "=== /vol1/@appmeta/xinZip 作对照 ==="
find /vol1/@appmeta/xinZip -maxdepth 3 2>/dev/null | head -20

echo
echo "=== appmeta 里带 desc 的文件 ==="
grep -rl 'desc' /vol1/@appmeta/yc7zip /vol1/@appmeta/xinZip 2>/dev/null | head -10

echo
echo "=== 谁在提供 About 文本：找含 Docker 与 fnOS 的文件 ==="
grep -ral 'fnOS' /vol1/@appmeta /var/apps /usr/trim/etc 2>/dev/null | head -10

echo
echo "=== 我们的 appmeta 里所有文件内容 ==="
for f in $(find /vol1/@appmeta/yc7zip -maxdepth 3 -type f 2>/dev/null | head -8); do
  echo "--- $f"
  head -c 600 "$f"
  echo
done

echo
echo "=== 飞牛 appcenter 的数据库里有没有描述字段 ==="
find /usr/trim /var/lib -maxdepth 4 -name '*.db' -o -maxdepth 4 -name '*.db3' 2>/dev/null | head -10
