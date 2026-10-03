#!/bin/sh
# ==========================================================================
#  TGPan 容器入口
#  职责：初始化数据目录 → 生成配置 → 交给 supervisord 同时拉起 PG + Teldrive
# ==========================================================================
set -e

DATA_DIR="${DATA_DIR:-/data}"
PGDATA="${PGDATA:-$DATA_DIR/postgres}"
CONF="$DATA_DIR/config.toml"

echo "=============================================="
echo "   TGPan · 把 TG 频道变成无限云盘"
echo "   版本：${TGPAN_VERSION:-dev}"
echo "=============================================="

mkdir -p "$DATA_DIR" "$PGDATA" /var/log/tgdrive

# ---------------------------------------------------------------------------
#  权限处理（关键，踩过的坑）
#
#  背景：容器里 postgres 用户要去 /data/postgres 初始化数据库，
#        但它必须先能「走进」父目录 /data。
#        挂载进来的目录通常属主 root、mode 700，postgres 会被挡在门外：
#          initdb: error: could not access directory "/data/postgres": Permission denied
#
#  处理：给父目录加 o+x（其他人可进入）。目录内文件仍受各自权限约束，不会泄露。
#        若失败（如只读挂载、特殊文件系统），打印警告但不中断。
# ---------------------------------------------------------------------------
if ! chmod o+x "$DATA_DIR" 2>/dev/null; then
  echo "[warn] 无法修改 $DATA_DIR 权限，若后续报 Permission denied，请手动执行："
  echo "       chmod 755 <你映射到 /data 的主机目录>"
fi
# 数据库目录属主必须是 postgres，才能初始化
chown -R postgres:postgres "$PGDATA" 2>/dev/null || true

# ---- PostgreSQL 初始化（仅首次）----
if [ ! -f "$PGDATA/PG_VERSION" ]; then
  echo "[init] 首次启动，初始化数据库..."
  su postgres -c "initdb -D '$PGDATA' -E UTF8 --locale=C" >/dev/null
  echo "[init] 数据库初始化完成"
fi

# ---- 确保 teldrive 角色和数据库存在（每次启动都检查，幂等）----
echo "[init] 检查数据库用户与库..."
# 先启动一个临时 postgres 用于初始化（supervisord 随后会接管同名端口，故用独立 socket 目录）
TMP_SOCK="/tmp/pginit"
mkdir -p "$TMP_SOCK" && chown postgres:postgres "$TMP_SOCK"
su postgres -c "pg_ctl -D '$PGDATA' -o '-c listen_addresses= -c unix_socket_directories=$TMP_SOCK' -w start" >/dev/null 2>&1 || true
su postgres -c "psql -h '$TMP_SOCK' -tAc \"SELECT 1 FROM pg_roles WHERE rolname='teldrive'\"" | grep -q 1 \
  || su postgres -c "psql -h '$TMP_SOCK' -c \"CREATE ROLE teldrive LOGIN PASSWORD 'secret' SUPERUSER;\"" >/dev/null
su postgres -c "psql -h '$TMP_SOCK' -tAc \"SELECT 1 FROM pg_database WHERE datname='postgres'\"" >/dev/null 2>&1
su postgres -c "psql -h '$TMP_SOCK' -c \"ALTER ROLE teldrive WITH LOGIN PASSWORD 'secret' SUPERUSER;\"" >/dev/null
su postgres -c "psql -h '$TMP_SOCK' -d postgres -c \"CREATE EXTENSION IF NOT EXISTS pgroonga;\"" >/dev/null 2>&1 || true
su postgres -c "pg_ctl -D '$PGDATA' -m fast -w stop" >/dev/null 2>&1 || true
echo "[init] 数据库用户与库就绪"

# ---- 生成 Teldrive 配置（仅首次）----
if [ ! -f "$CONF" ]; then
  echo "[init] 生成 Teldrive 配置..."
  JWT_SECRET="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  cat > "$CONF" <<EOF
# TGPan 自动生成的配置。修改后重启容器生效。
[db]
data-source = 'postgres://teldrive:secret@127.0.0.1:5432/postgres?sslmode=disable'
log-level = 'info'

[jwt]
secret = '${JWT_SECRET}'
session-time = '30d'

[server]
port = 8080
graceful-shutdown = '10s'
read-timeout = '1h'
write-timeout = '1h'

[tg]
# 使用 Teldrive 内置的公共应用凭据，用户侧只需手机号/扫码登录
app-id = 2496
app-hash = '8da85b0d5bfe62527e5b244c209159c3'
auto-channel-create = true
channel-limit = 500000
rate = 100
rate-burst = 5
rate-limit = true
pool-size = 8
reconnect-timeout = '5m'

[tg.session]
type = 'postgres'
key = 'session'

[tg.stream]
buffers = 8
concurrency = 1

[cronjobs]
enable = true
clean-files-interval = '1h'
clean-uploads-interval = '12h'
folder-size-interval = '2h'
EOF
  echo "[init] 配置写入 $CONF"
fi

echo "[start] 启动服务..."
exec /usr/bin/supervisord -c /etc/supervisord.conf
