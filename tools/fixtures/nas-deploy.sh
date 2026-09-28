#!/bin/bash
# 在飞牛 NAS 上重建镜像、推仓库、打包 fpk 并重装
set -eux

VERSION=$(cat /root/yc7zip-build/VERSION)
cd /root/yc7zip-build
TAG="ycyingchen/yc-7zip:$VERSION"

echo "=== 版本 $VERSION ==="

echo "=== 构建镜像 ==="
DOCKER_BUILDKIT=0 docker build --build-arg TARGETARCH=amd64 -t "$TAG" -t ycyingchen/yc-7zip:latest .
docker tag "$TAG" "ycyingchen/yc-7zip:$VERSION"

echo "=== 推送镜像（安装 fpk 时应用中心会去拉，必须先有）==="
docker push "$TAG"
docker push ycyingchen/yc-7zip:latest

echo "=== 打包 fpk ==="
cd /root/yc7zip-build/fpk
grep '^version' manifest
fnpack build 2>&1 | tail -2
ls -la ./*.fpk

echo "=== 重装应用 ==="
cd /root
appcenter-cli uninstall yc7zip 2>&1 | tr '\r' '\n' | grep -E 'success|Error' | tail -2 || true
sleep 3
appcenter-cli install-fpk /root/yc7zip-build/fpk/yc7zip.fpk -e /root/yc7zip-build/env.txt -v 1 \
  > /root/yc7zip-build/install.log 2>&1 || true
tr '\r' '\n' < /root/yc7zip-build/install.log | grep -E 'Error|complete' | tail -3

sleep 12
echo "=== 结果 ==="
docker ps --filter name=yc7zip --format '{{.Names}} | {{.Status}}'
docker inspect yc7zip --format 'health={{.State.Health.Status}}' 2>&1
echo "--- 版本 ---"
curl -s http://127.0.0.1:8090/api/health | head -c 260
echo
echo "--- 网关前缀 ---"
curl -s -o /dev/null -w '/app/yc7zip/ = %{http_code}\n' "http://127.0.0.1:8090/app/yc7zip/"
echo "--- 根目录浏览 ---"
curl -s 'http://127.0.0.1:8090/app/yc7zip/api/browse' | head -c 400
