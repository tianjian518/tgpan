# syntax=docker/dockerfile:1
# ==========================================================================
#  TGPan —— 把 Telegram 频道变成无限云盘（开箱即用单镜像）
#
#  内含：
#    - PostgreSQL 17 + pgroonga 扩展（Teldrive 必需的全文搜索）
#    - Teldrive 服务端（连 TG、切片存储、文件流、302）
#    - Teldrive Web UI（官方界面，支持手机号 / 扫码登录）
#
#  对外端口：
#    8080  → Web 界面（看片、管理文件、挂载播放器）
#
#  构建：docker build -t tgpan .
#  运行：docker run -d -p 8080:8080 -v tgpan-data:/data tgpan
# ==========================================================================

ARG PG_IMAGE=groonga/pgroonga:latest-alpine-17

# ---------- Stage 1: 取 Teldrive 二进制（从本地 vendor 目录，避免构建时联网）----------
FROM alpine:3.20 AS fetcher
ARG TARGETARCH
COPY vendor/ /vendor/
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) A="amd64" ;; \
      arm64) A="arm64" ;; \
      arm)   A="arm" ;; \
      *)     A="amd64" ;; \
    esac; \
    mkdir -p /out; \
    tar -xzf "/vendor/teldrive-${A}.tar.gz" -C /out; \
    chmod +x /out/teldrive

# ---------- Stage 2: 运行时 ----------
FROM ${PG_IMAGE}

ARG TELDRIVE_VERSION=1.8.3

# 运行所需工具（supervisor 同时管理数据库和 Teldrive）
RUN apk add --no-cache supervisor tzdata curl bash ca-certificates \
 && ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
 && echo "Asia/Shanghai" > /etc/timezone

# Teldrive 二进制
COPY --from=fetcher /out/teldrive /usr/local/bin/teldrive

# 启动脚本与配置
COPY docker/entrypoint.sh /entrypoint.sh
COPY docker/supervisord.conf /etc/supervisord.conf
COPY docker/teldrive-entry.sh /teldrive-entry.sh
RUN chmod +x /entrypoint.sh /teldrive-entry.sh

# 数据目录（数据库 + Teldrive 会话 + 配置都在这里，容器重建不丢）
VOLUME ["/data"]
ENV PGDATA=/data/postgres \
    PGPORT=5432 \
    TZ=Asia/Shanghai

EXPOSE 8080

ENTRYPOINT ["/entrypoint.sh"]
