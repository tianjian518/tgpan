# syntax=docker/dockerfile:1
# ==========================================================================
#  TGPan —— 把 Telegram 频道变成无限云盘（开箱即用单镜像）
#
#  内含：
#    - PostgreSQL 17 + pgroonga 扩展（Teldrive 必需的全文搜索）
#    - Teldrive 服务端（连 TG、切片存储、文件流、302）
#    - TGPan Web 界面（自制前端，已 embed 进二进制，无需额外静态文件兜底）
#      · 我的网盘 / 频道扫描 / 自动扫描 / WebDAV / 剧集归档 / 关于
#      · 闸门脚本 tgpan-gate.js（管理密码 + 内网免密）
#
#  对外端口：
#    8080  → Web 界面（看片、管理文件、挂载播放器）
#
#  构建：docker build -t tgpan .
#  运行：docker run -d -p 8080:8080 -v tgpan-data:/data tgpan
#
#  支持的平台：linux/amd64、linux/arm64（见 vendor/ 下的两个包）
# ==========================================================================

ARG PG_IMAGE=groonga/pgroonga:latest-alpine-17

# ---------- Stage 1: 取 Teldrive 二进制（从本地 vendor 目录，避免构建时联网）----------
FROM alpine:3.20 AS fetcher
ARG TARGETARCH
COPY vendor/ /vendor/
# 架构映射。这里**显式失败**而不是回退到 amd64：
# 若某架构没有对应的包，构建期就报错，好过装出一个跑不起来的镜像。
# （曾经写成 `*) A="amd64"`，结果在 arm 上会 exec format error，且报错指不到根因）
RUN set -eux; \
    case "${TARGETARCH:-amd64}" in \
      amd64) A="amd64" ;; \
      arm64) A="arm64" ;; \
      *) \
        echo "不支持的架构：TARGETARCH=${TARGETARCH}" >&2; \
        echo "vendor/ 下现有：" >&2; ls -1 /vendor/ >&2; \
        exit 1 ;; \
    esac; \
    test -f "/vendor/teldrive-${A}.tar.gz" || { \
      echo "缺少 vendor/teldrive-${A}.tar.gz —— 无法为 $A 构建" >&2; exit 1; }; \
    mkdir -p /out; \
    tar -xzf "/vendor/teldrive-${A}.tar.gz" -C /out; \
    chmod +x /out/teldrive

# ---------- Stage 2: 运行时 ----------
FROM ${PG_IMAGE}

# TGPan 版本号。容器启动横幅会打印它（见 docker/entrypoint.sh）。
# 默认值与仓库根目录的 VERSION 文件保持一致，构建时可覆盖：
#   docker build --build-arg TGPAN_VERSION=2.7.1 -t tgpan .
ARG TGPAN_VERSION=2.7.1
ENV TGPAN_VERSION=${TGPAN_VERSION}

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
