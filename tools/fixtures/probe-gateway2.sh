#!/bin/sh
# 验证经飞牛应用网关访问 YC-7ZIP 是否正常
SOCK=/vol1/@appcenter/yc7zip/app.sock
NGINX=http://127.0.0.1:5666

echo "=== 1. 经 nginx 网关 ==="
for u in "/app/yc7zip/" "/app/yc7zip/api/health" "/app/yc7zip/assets/style.css" "/app/yc7zip/assets/app.js"; do
  printf '%-34s -> %s\n' "$u" "$(curl -s -o /dev/null -w '%{http_code}' "$NGINX$u")"
done

echo
echo "=== 2. 页面片段 ==="
curl -s "$NGINX/app/yc7zip/" | head -c 200
echo

echo
echo "=== 3. 健康检查 JSON ==="
curl -s "$NGINX/app/yc7zip/api/health" | head -c 260
echo

echo
echo "=== 4. 静态资源是不是真内容（不是被 SPA 兜底成 html）==="
echo "style.css 前 60 字节："
curl -s "$NGINX/app/yc7zip/assets/style.css" | head -c 60
echo

echo
echo "=== 5. 直连 unix socket（模拟网关转发）==="
for u in "/app/yc7zip/" "/app/yc7zip/api/health" "/"; do
  printf '%-30s -> %s\n' "$u" "$(curl -s -o /dev/null -w '%{http_code}' --unix-socket "$SOCK" "http://localhost$u")"
done

echo
echo "=== 6. 直连容器端口 ==="
for u in "/api/health" "/app/yc7zip/api/health" "/"; do
  printf '%-30s -> %s\n' "$u" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:8090$u")"
done

echo
echo "=== 7. 容器健康状态 ==="
docker inspect yc7zip --format '{{.State.Health.Status}}'
