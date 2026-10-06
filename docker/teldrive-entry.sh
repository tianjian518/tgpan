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
DB_READY=0
for i in $(seq 1 120); do
  if pg_isready -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" >/dev/null 2>&1; then
    echo "[teldrive] 数据库已就绪 (第 ${i} 次尝试)"
    DB_READY=1
    break
  fi
  sleep 1
done

# 【关键】等不到数据库就**明确报错退出**，不要硬着头皮往下跑。
# 老版本不管等没等到都继续执行，于是 teldrive 连不上库 → 立刻报错退出
# → supervisord 重启 → 再等 60 秒 → 再失败，日志里刷满重复内容，
# 真正的根因（数据库没起来）反而被淹没了。
# 主动退出并打印醒目提示，supervisord 会重启我们，但日志一眼能看出问题。
if [ "$DB_READY" != "1" ]; then
  echo "[teldrive][error] 等待 120 秒后数据库仍未就绪，暂不启动。" >&2
  echo "                 请检查 postgres 进程日志：/var/log/tgdrive/postgres.err" >&2
  exit 1
fi

# 确保 pgroonga 扩展存在（Teldrive 全文搜索硬依赖）
echo "[teldrive] 检查 pgroonga 扩展..."
OK=0
for i in $(seq 1 20); do
  if psql "postgres://${PGUSER}:secret@${PGHOST}:${PGPORT}/${PGDB}" \
       -c "CREATE EXTENSION IF NOT EXISTS pgroonga;" >/dev/null 2>&1; then
    echo "[teldrive] pgroonga 就绪"
    OK=1
    break
  fi
  sleep 1
done
if [ "$OK" != "1" ]; then
  echo "[teldrive][error] pgroonga 扩展创建失败，全文搜索将不可用。" >&2
fi

echo "[teldrive] 启动服务，配置：$CONF"
exec teldrive run -c "$CONF"
