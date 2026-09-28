#!/bin/sh
# 查看 fpk 安装后的实际落盘结构与飞牛注入的变量
echo "=== 安装后的 compose ==="
cat /vol1/@appcenter/yc7zip/docker/docker-compose.yaml

echo
echo "=== resource ==="
cat /vol1/@appcenter/yc7zip/config/resource 2>/dev/null

echo
echo "=== @appconf/yc7zip ==="
find /vol1/@appconf/yc7zip -maxdepth 3 2>/dev/null | head -20

echo
echo "=== 其中的文件内容 ==="
for f in $(find /vol1/@appconf/yc7zip -maxdepth 3 -type f 2>/dev/null | head -8); do
  echo "--- $f"
  head -25 "$f"
  echo
done

echo "=== 应用目录 ==="
ls -la /vol1/@appcenter/yc7zip/
echo "=== 数据目录 ==="
ls -la /var/apps/yc7zip/ 2>&1 | head
