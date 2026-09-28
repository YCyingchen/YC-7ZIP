#!/bin/sh
# 准备 Docker 构建上下文并构建镜像，或直接产出可发布的安装包。
#
# 用法：
#   deploy/build.sh docker [标签]     仅构建镜像
#   deploy/build.sh dist              产出 dist/ 下的发布件（二进制 + 7-Zip）
#
# 在 Windows 上可以用 Git Bash / WSL 运行；CI 里也走同一个脚本。

set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

# 版本号只有一个来源：VERSION 文件（格式 zip<YY><MM>.<NNN>）
VERSION=$(deploy/version.sh show)
deploy/version.sh check >/dev/null
ZIP_VERSION=2501

# 官方 7-Zip 的 Linux 包。amd64 与 arm64 用不同的压缩包。
sevenzip_url () {
  case "$1" in
    amd64) echo "https://www.7-zip.org/a/7z${ZIP_VERSION}-linux-x64.tar.xz" ;;
    arm64) echo "https://www.7-zip.org/a/7z${ZIP_VERSION}-linux-arm64.tar.xz" ;;
    *)     echo "不支持的架构：$1" >&2; exit 2 ;;
  esac
}

# 下载并解出某一架构的 7zz，放进构建上下文
prepare_sevenzip () {
  arch=$1
  out="$ROOT/dist/$arch/7zz"
  [ -x "$out" ] && return 0

  mkdir -p "$ROOT/dist/$arch"
  tmp="$ROOT/dist/.7z-$arch.tar.xz"
  url=$(sevenzip_url "$arch")

  echo "下载 7-Zip：$url"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$tmp" "$url"
  else
    wget -qO "$tmp" "$url"
  fi
  tar -xJf "$tmp" -C "$ROOT/dist/$arch" 7zz
  rm -f "$tmp"
  chmod +x "$out"
}

build_binary () {
  arch=$1
  mkdir -p "$ROOT/dist/$arch"
  echo "编译 linux/$arch"
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" \
    -o "$ROOT/dist/$arch/yc7zip" .
}

cmd_dist () {
  for arch in amd64 arm64; do
    build_binary "$arch"
    prepare_sevenzip "$arch"
  done
  echo "完成，产物在 dist/<arch>/"
}

cmd_docker () {
  tag=${1:-$VERSION}
  for arch in amd64 arm64; do
    build_binary "$arch"
    prepare_sevenzip "$arch"
  done

  if command -v docker >/dev/null 2>&1 && docker buildx version >/dev/null 2>&1; then
    echo "用 buildx 构建多架构镜像：$tag"
    docker buildx build \
      --platform linux/amd64,linux/arm64 \
      -t "ycyingchen/yc-7zip:$tag" \
      -t "ycyingchen/yc-7zip:latest" \
      --load "$ROOT"
  else
    echo "没有可用的 docker buildx；产物已就绪，可先执行 deploy/build.sh dist" >&2
    exit 1
  fi
}

# 单机镜像：某些环境下 buildx 解析不了基础镜像的元数据，
# 而经典构建器只要本地已有基础镜像就能跑通。
cmd_image () {
  arch=${1:-amd64}
  tag=${2:-$VERSION}

  build_binary "$arch"
  prepare_sevenzip "$arch"

  echo "预拉基础镜像（经典构建器不会自己去解析元数据）"
  docker pull debian:12-slim

  echo "用经典构建器构建 linux/$arch 镜像：$tag"
  DOCKER_BUILDKIT=0 docker build \
    --build-arg "TARGETARCH=$arch" \
    -t "ycyingchen/yc-7zip:$tag" \
    -t "ycyingchen/yc-7zip:latest" \
    "$ROOT"
}

case "${1:-}" in
  dist)   cmd_dist ;;
  docker) shift; cmd_docker "$@" ;;
  image)  shift; cmd_image "$@" ;;
  version) shift; exec "$ROOT/deploy/version.sh" "$@" ;;
  *)
    echo "用法：deploy/build.sh {dist | docker [标签] | image [架构] [标签] | version ...}" >&2
    exit 2
    ;;
esac
