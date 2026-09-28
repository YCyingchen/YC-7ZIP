#!/bin/sh
# 验证装上之后的读写范围与网关路由
B=http://127.0.0.1:8090/app/yc7zip

echo "=== 健康（应含 allow_roots 与版本）==="
curl -s "$B/api/health"
echo

echo
echo "=== 根目录快捷入口 ==="
curl -s "$B/api/browse" | head -c 500
echo

echo
echo "=== 浏览 /vol5/1000（NAS 数据）==="
curl -s "$B/api/browse?path=%2Fvol5%2F1000" | head -c 400
echo

echo
echo "=== 浏览 /etc（验证根已放开）==="
curl -s "$B/api/browse?path=%2Fetc" | head -c 240
echo

echo
echo "=== inspect 一个分卷 ==="
curl -s "$B/api/inspect?path=%2Fvol5%2F1000%2F%E7%A9%BA%E9%97%B44%2FYC-7ZIP%2F_uitest%2Fparts%2Fmovie.7z.003"
echo

echo
echo "=== 越界路径应被拒（allow_roots 含 / 时不会拒，这里只是确认不崩）==="
curl -s -o /dev/null -w 'status=%{http_code}\n' "$B/api/browse?path=%2Fnonexistent-xyz"

echo
echo "=== 容器健康 ==="
docker inspect yc7zip --format '{{.State.Health.Status}}'
