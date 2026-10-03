#!/bin/sh
# ==========================================================================
#  Teldrive 启动包装
#  等数据库就绪 → 确保 pgroonga 扩展 → 启动 Teldrive 服务
# ==========================================================================
set -e

CONF="${DATA_DIR:-/data}/config.toml"
PGHOST=127.0.0.1
PGPORT=5432
PGUSER=teldrive
PGDB=postgres

echo "[teldrive] 等待数据库就绪..."
for i in $(seq 1 60); do
  if pg_isready -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" >/dev/null 2>&1; then
    echo "[teldrive] 数据库已就绪 (第 ${i} 次尝试)"
    break
  fi
  sleep 1
done

# 确保 pgroonga 扩展存在（Teldrive 全文搜索硬依赖）
echo "[teldrive] 检查 pgroonga 扩展..."
for i in $(seq 1 20); do
  if psql "postgres://${PGUSER}:secret@${PGHOST}:${PGPORT}/${PGDB}" \
       -c "CREATE EXTENSION IF NOT EXISTS pgroonga;" >/dev/null 2>&1; then
    echo "[teldrive] pgroonga 就绪"
    break
  fi
  sleep 1
done

echo "[teldrive] 启动服务，配置：$CONF"
exec teldrive run -c "$CONF"
