# syntax=docker/dockerfile:1
# ==========================================================================
#  TGPan —— 把 Telegram 频道变成无限云盘（开箱即用单镜像）
#
#  内含：
#    - PostgreSQL 17 + pgroonga 扩展（Teldrive 必需的全文搜索）
#    - Teldrive 服务端（连 TG、切片存储、文件流、302）
#    - TGPan Web 界面（自制前端，已 embed 进二进制，无需额外静态文件兜底）
#      · 我的网盘 / 频道扫描 / 自动扫描 / WebDAV / 剧集归档 / 关于
#      · 登录引导 tgpan-gate.js（扫码 / 手机验证码登 TG，一步到位）
#
#  对外端口：
#    8080  → Web 界面（看片、管理文件、挂载播放器）
#
#  对外挂载：
#    /data → 数据库 + TG 会话 + 配置（容器重建不丢）
#
#  只有这一挂载、这一端口。界面上新建容器时不会有别的多余项。
#
#  构建：docker build -t tgpan .
#  运行：docker run -d -p 8080:8080 -v tgpan-data:/data tgpan
#
#  支持的平台：linux/amd64、linux/arm64（见 vendor/ 下的两个包）
# ==========================================================================

# 为什么不用 `FROM ${PG_IMAGE}` 一行搞定？
# ----------------------------------------------------------------------
# pgroonga 官方镜像自带两条我们**用不上**的元数据：
#     VOLUME /var/lib/postgresql/data   ← 我们的数据在 /data/postgres
#     EXPOSE 5432                       ← 我们只监听 127.0.0.1，不对外
# 这两条会一路继承到最终镜像，导致界面上（飞牛 / 群晖 / 各种 NAS）新建容器时
# 被要求**多填一个存储挂载、多填一个端口**。而 Docker 的 VOLUME / EXPOSE
# **只能加不能删** —— FROM 之后没有任何指令能撤销继承来的这两条。
#
# 所以改成「两段式」：先用 pgroonga 当素材，挂到 alpine 上，再 COPY 整个根文件系统。
# 继承链一断，元数据就只剩我们自己声明的那一个挂载、一个端口。
# 依赖（apk 包 + /usr/local 下从源码编的 PG 与 groonga）全部原样搬过去，一个不漏。
ARG PG_SRC=groonga/pgroonga:latest-alpine-17
ARG ALPINE_VER=3.24

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

# ---------- Stage 2: 数据库素材（只用来取文件，元数据不带下去）----------
FROM ${PG_SRC} AS dbsrc

# --------------------------------------------------------------------------
#  瘦身：删掉 pgroonga 官方镜像里我们**用不到**的东西（约 320MB）
# --------------------------------------------------------------------------
# pgroonga 官方镜像是个「什么都有」的通用镜像，默认带了大量对 TGPan 无用
# 的重量。关键是：**必须在 Stage 2（素材阶段）里删** —— 这样被删的文件
# 根本不会进入最终镜像。如果放到 Stage 3 再删，因为 Docker 层是叠加的，
# 数据仍留在前一层，镜像一点都不会变小（这是实测过的坑）。
#
# 逐项说明（每一项都核对过依赖关系，并用真机验证过删完搜索照常工作）：
#
#   1. LLVM（libLLVM.so.21.1，170MB）
#      PG 的 JIT 编译后端。我们在 internal_database_tuning.go 里已经把
#      jit 设成 off（短查询上 JIT 编译开销大于收益），这个库从头到尾
#      不会被加载。删掉 llvmjit.so + bitcode 后 JIT 就算想开也开不了，
#      正好和我们的设定一致。
#
#   2. MeCab 日语分词词典（naist-jdic，84MB）
#      Groonga 的日语全文检索词典。TGPan 处理的是中文/英文文件名，
#      这个词典一次都不会被用到。
#
#   3. groonga/ggml（16MB）
#      Groonga 的大语言模型向量检索后端（llama.cpp）。网盘场景用不到。
#
#   4. libgrpc / libprotoc / libupb（19MB）
#      Groonga 的远程 RPC 接口。本地单机部署用不到；实测 ldd 确认
#      groonga 与 pgroonga 都没有链接 grpc（0 个引用）。
#
#   5. /usr/local/include + *.a（10MB）
#      编译期的头文件与静态库，运行时完全不需要。
#
#   ⚠️ 注意：libgroonga-llama.so / libgroonga-ggml*.so 这两个**不能删** ——
#      它们是 libgroonga.so 的硬依赖，删了会导致 pgroonga.so 加载失败
#      （实测报 "Error loading shared library libgroonga-llama.so.0"）。
#      这里只删 ggml 目录下的**模型数据**，保留库文件本身。
RUN set -eux; \
    rm -f /usr/lib/libLLVM*.so* \
          /usr/local/lib/postgresql/llvmjit.so; \
    rm -rf /usr/local/lib/postgresql/bitcode; \
    rm -rf /usr/local/etc/mecab; \
    rm -f /usr/local/bin/mecab /usr/local/bin/mecab-dict-index; \
    rm -rf /usr/local/lib/groonga/ggml; \
    rm -f /usr/lib/libgrpc*.so* /usr/lib/libprotoc*.so* /usr/lib/libupb*.so*; \
    rm -rf /usr/local/include; \
    rm -f /usr/local/lib/*.a; \
    true


# ---------- Stage 3: 运行时 ----------
# 用干净的 alpine 起手，把 Stage 2 的根文件系统整个搬过来。
# 这样继承链断开，pgroonga 那两条多余的 VOLUME / EXPOSE 不会被继承。
FROM alpine:${ALPINE_VER}

COPY --from=dbsrc / /

# 保留 pgroonga 镜像原本的入口脚本（我们用不到它的 ENTRYPOINT，
# 但 postgres 的 initdb 逻辑有些会调它，留着以防万一）。
# 注意：/usr/local/bin/docker-entrypoint.sh 已随根文件系统一起搬过来了。

# TGPan 版本号。容器启动横幅会打印它（见 docker/entrypoint.sh）。
# 默认值与仓库根目录的 VERSION 文件保持一致，构建时可覆盖：
#   docker build --build-arg TGPAN_VERSION=2.8.1 -t tgpan .
ARG TGPAN_VERSION=2.8.1
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
