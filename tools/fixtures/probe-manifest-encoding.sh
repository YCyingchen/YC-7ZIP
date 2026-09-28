#!/bin/sh
# 查清 manifest 里中文该用什么编码
echo "=== 我们自己装的那份（前 12 行，含不可见字符）==="
for f in /vol1/@appmeta/yc7zip/manifest /vol1/@appcenter/yc7zip/manifest /var/apps/yc7zip/manifest; do
  if [ -f "$f" ]; then
    echo "--- $f"
    sed -n '1,12p' "$f" | cat -v
    echo "--- desc 行的原始字节 ---"
    grep -a '^desc' "$f" | head -1 | hexdump -C | head -6
    break
  fi
done

echo
echo "=== 找一个中文的第三方应用做对照 ==="
for a in xinZip Fluxor coder-docker iconstation; do
  for f in "/vol1/@appcenter/$a/manifest" "/vol1/@appmeta/$a/manifest" "/var/apps/$a/manifest"; do
    [ -f "$f" ] || continue
    echo "--- $f"
    grep -aE '^(desc|display_name)' "$f" | head -3
    echo "    display_name 原始字节："
    grep -a '^display_name' "$f" | head -1 | hexdump -C | head -3
  done
done

echo
echo "=== 飞牛是否有本地化文件（locales）==="
find /vol1/@appcenter -maxdepth 2 -name 'locales' -o -maxdepth 2 -name '*.json' -path '*locale*' 2>/dev/null | head -10
ls /usr/trim/www/locales/ 2>/dev/null | head -5
