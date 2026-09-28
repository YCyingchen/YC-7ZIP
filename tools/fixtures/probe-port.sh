#!/bin/sh
# 找出飞牛把向导变量（wizard_app_port）存在哪里
echo "=== 哪些已装应用用了 wizard_app_port ==="
grep -rl 'wizard_app_port' /vol1/@appcenter/*/docker/*.yaml /vol1/@appcenter/*/docker/*.yml 2>/dev/null | head -5

echo
echo "=== 这些应用的配置目录 ==="
for a in iconstation coder-docker; do
  echo "--- /vol1/@appconf/$a"
  find "/vol1/@appconf/$a" -maxdepth 2 2>/dev/null | head -10
  echo "--- /vol1/@apphome/$a"
  find "/vol1/@apphome/$a" -maxdepth 2 2>/dev/null | head -10
done

echo
echo "=== 全局搜含 wizard_app_port 或端口值的配置文件（近 2 天）==="
find /vol1/@appconf /vol1/@apphome -maxdepth 3 -type f -mtime -2 2>/dev/null | head -20

echo
echo "=== yc7zip 相关目录全貌 ==="
for d in /vol1/@appconf/yc7zip /vol1/@apphome/yc7zip /vol1/@appdata/yc7zip /vol1/@appmeta/yc7zip /vol1/@apptemp/yc7zip; do
  echo "--- $d"
  ls -la "$d" 2>&1 | head -6
done

echo
echo "=== 搜 8090 出现在哪些 app 配置里 ==="
grep -rl '8090' /vol1/@appconf /vol1/@apphome /vol1/@appmeta 2>/dev/null | head -10
