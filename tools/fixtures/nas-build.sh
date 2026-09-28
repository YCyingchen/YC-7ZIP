#!/bin/bash
# 在飞牛 NAS 上构建镜像并打包 fpk（本地联调用）
set -eux

cd /root/yc7zip-build

echo "=== 构建镜像 ==="
docker build -t ycyingchen/yc-7zip:1.0.0 -t ycyingchen/yc-7zip:latest .

echo "=== 镜像信息 ==="
docker images ycyingchen/yc-7zip --format '{{.Repository}}:{{.Tag}} {{.Size}}'

echo "=== 冒烟测试容器 ==="
docker rm -f yc7zip-smoke >/dev/null 2>&1 || true
mkdir -p /root/yc7zip-smoke/vol
echo "hello from smoke test" > /root/yc7zip-smoke/vol/a.txt
docker run -d --name yc7zip-smoke -p 8099:8080 \
  -v /root/yc7zip-smoke/vol:/vol1 \
  ycyingchen/yc-7zip:1.0.0 \
  -addr :8080 -data /data -allow-root /vol1
sleep 4
echo "--- health ---"
curl -s http://127.0.0.1:8099/api/health | head -c 300
echo
echo "--- 7-Zip 可用性 ---"
curl -s http://127.0.0.1:8099/api/health | grep -o '"version":"[0-9.]*"' | head -1
echo "--- browse /vol1 ---"
curl -s 'http://127.0.0.1:8099/api/browse?path=%2Fvol1' | head -c 300
echo
echo "--- 容器日志 ---"
docker logs yc7zip-smoke 2>&1 | tail -5
docker rm -f yc7zip-smoke >/dev/null

echo "=== 打包 fpk ==="
cd /root/yc7zip-build/fpk
fnpack build 2>&1 | tail -5
ls -la ./*.fpk
