# YC-7ZIP 运行镜像
#
# 构建上下文里需要先放好两样东西（由 CI 或 deploy/build.sh 准备）：
#   dist/${TARGETARCH}/yc7zip   本项目的 Linux 二进制
#   dist/${TARGETARCH}/7zz      官方 7-Zip 控制台程序
# 这样构建阶段完全不需要联网，镜像也因此可复现。
#
# 基镜像选 glibc 的 Debian 而不是 Alpine：官方 7-Zip 的 Linux 版本是动态链接
# 到 glibc 的（ldd 可见 libc.so.6 / ld-linux-x86-64.so.2），在 musl 上根本起不来。
# 这也正好和本项目在飞牛 NAS（Debian 12）上实测通过的环境一致。

FROM debian:12-slim

# buildx 会自动注入 TARGETARCH；用经典构建器（DOCKER_BUILDKIT=0）时它是空的，
# 所以这里给一个默认值，让两种构建方式都能跑。
ARG TARGETARCH=amd64

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates tzdata libstdc++6; \
    rm -rf /var/lib/apt/lists/*

COPY dist/${TARGETARCH}/yc7zip /usr/local/bin/yc7zip
COPY dist/${TARGETARCH}/7zz    /usr/local/bin/7zz
RUN chmod 0755 /usr/local/bin/yc7zip /usr/local/bin/7zz \
 && ln -s /usr/local/bin/7zz /usr/local/bin/7z

# 7-Zip 自带解压 rar 的能力，这里只是把两条路径都准备好，方便换成系统版本
ENV YC7ZIP_7Z=/usr/local/bin/7zz \
    YC7ZIP_ADDR=:8080 \
    YC7ZIP_DATA=/data \
    LANG=C.UTF-8

WORKDIR /data

# 8090 是飞牛 fpk 默认映射的端口，8080 是裸容器端口
EXPOSE 8080 8090

# 用 bash 的 /dev/tcp 探测，省掉 curl 依赖。
# 只看 HTTP 状态行，不解析响应体：接口字段是顺序不定的，按固定字节数截取会误判。
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD bash -c 'exec 3<>/dev/tcp/127.0.0.1/8080 && printf "GET /api/health HTTP/1.0\r\nHost: localhost\r\n\r\n" >&3 && head -n 1 <&3 | grep -q "200"' || exit 1

ENTRYPOINT ["/usr/local/bin/yc7zip"]
CMD ["-addr", ":8080", "-data", "/data"]
