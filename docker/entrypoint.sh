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
#
# 【为什么不能只看 PG_VERSION 是否存在】
# initdb 会**先写 PG_VERSION**，再建其它文件。如果它在中途失败
# （断电、磁盘满、被 kill、慢速设备上超时），目录里会留下一个
# 「有 PG_VERSION 但没有真实数据库」的残缺目录。下次启动时按
# 老逻辑就会跳过初始化，直接拿这个坏目录去启动 → 反复崩溃，
# 且报错信息完全指不到根因。
#
# 所以判定「已初始化」要更严格：PG_VERSION 和 global/pg_control 都在，
# 才算真的建好了。残缺目录一律**移到一边重新建**，绝不原地删（留证据）。
if [ ! -f "$PGDATA/PG_VERSION" ] || [ ! -f "$PGDATA/global/pg_control" ]; then
  if [ -d "$PGDATA" ] && [ -n "$(ls -A "$PGDATA" 2>/dev/null)" ]; then
    BROKEN="$PGDATA.broken.$(date +%Y%m%d%H%M%S)"
    echo "[init] 检测到残缺的数据目录（initdb 上次没跑完），移到一边：$BROKEN"
    mv "$PGDATA" "$BROKEN" || true
  fi
  echo "[init] 首次启动，初始化数据库..."
  mkdir -p "$PGDATA"
  chown postgres:postgres "$PGDATA" 2>/dev/null || true
  # 去掉 stdout 重定向：initdb 的报错必须让用户看见，
  # 否则失败时只有一个干巴巴的 "初始化失败"，无从排查。
  if ! su postgres -c "initdb -D '$PGDATA' -E UTF8 --locale=C"; then
    echo "[error] initdb 失败，详见上面的输出。" >&2
    echo "        常见原因：/data 所在磁盘空间不足、或该目录无写权限。" >&2
    exit 1
  fi
  echo "[init] 数据库初始化完成"
fi

# ---- 确保 teldrive 角色和数据库存在（每次启动都检查，幂等）----
echo "[init] 检查数据库用户与库..."
# 先启动一个临时 postgres 用于初始化（supervisord 随后会接管同名端口，故用独立 socket 目录）
TMP_SOCK="/tmp/pginit"
mkdir -p "$TMP_SOCK" && chown postgres:postgres "$TMP_SOCK"

# 【重要】这段临时实例必须**确保收尾关掉**。
# 以前这里每一步都挂 `|| true` 把失败吞掉，万一 pg_ctl stop 没生效，
# 临时实例就继续活着。等 supervisord 再去起 postgres 绑 5432 时，
# 端口已被自己人占住 → 起不来 → 无限重启 → 表现为网页打不开。
# 现在：启动失败要能看见；收尾用带重试的强制关闭，确认干净。
if ! su postgres -c "pg_ctl -D '$PGDATA' -o '-c listen_addresses= -c unix_socket_directories=$TMP_SOCK' -w start"; then
  echo "[error] 临时数据库启动失败，无法完成初始化。" >&2
  exit 1
fi

su postgres -c "psql -h '$TMP_SOCK' -tAc \"SELECT 1 FROM pg_roles WHERE rolname='teldrive'\"" | grep -q 1 \
  || su postgres -c "psql -h '$TMP_SOCK' -c \"CREATE ROLE teldrive LOGIN PASSWORD 'secret' SUPERUSER;\"" >/dev/null
su postgres -c "psql -h '$TMP_SOCK' -c \"ALTER ROLE teldrive WITH LOGIN PASSWORD 'secret' SUPERUSER;\"" >/dev/null
su postgres -c "psql -h '$TMP_SOCK' -d postgres -c \"CREATE EXTENSION IF NOT EXISTS pgroonga;\"" >/dev/null 2>&1 || true

# 收尾：先 fast 关，若失败再 immediate 强关，最后确认 socket 已消失
su postgres -c "pg_ctl -D '$PGDATA' -m fast -w stop" >/dev/null 2>&1 \
  || su postgres -c "pg_ctl -D '$PGDATA' -m immediate -w stop" >/dev/null 2>&1 \
  || true
# 再确认一遍真的关干净了（进程还在就报出来，不要假装没事）
for i in $(seq 1 10); do
  if ! su postgres -c "pg_ctl -D '$PGDATA' status" >/dev/null 2>&1; then break; fi
  echo "[init] 等待临时数据库退出... ($i)"
  sleep 1
done
if su postgres -c "pg_ctl -D '$PGDATA' status" >/dev/null 2>&1; then
  echo "[error] 临时数据库没关掉，supervisord 将无法绑定端口。" >&2
  exit 1
fi
rm -rf "$TMP_SOCK"
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

[gate]
# TGPan 登录。
#
# 工作方式（简单到不能再简单）：
#   1. 第一次打开网页 → 扫码或手机验证码登 TG，只做一次
#   2. 以后           → 直接进，不用再登
#
# 登录凭证存在 /data 下，容器重启 / 重建 / 升级镜像都不会丢。
#
# 【安全设计】必须同时满足两个条件才放行：
#   · 服务端有 TG 凭证（证明"这个容器归谁"）
#   · 浏览器有签名门票（证明"这个浏览器是主人"）
# 所以网址被别人拿到也没用 —— 换个浏览器打开只会看到登录页。
#
# 不看 Host、不分内外网、没有管理密码。这些"贴心功能"都试过，
# 全都会导致「某些设备莫名其妙免密直进」，已全部移除。
#
# 【注意】删掉这个文件 = 退出登录（需要重新扫码）。
data-file = '/data/tgpan-gate.json'

# 浏览器门票有效期。30 天，过期后重新登录一次即可。
session-ttl = '30d'

# 设为 true 可彻底关闭此功能，恢复「只能用 TG 扫码」的原版行为
disable = false

[tg]
# Telegram 应用凭据。
#
# 【这里的两行是「代理凭据占位」，程序启动时会自动换掉】
# 2496 是 Teldrive 上游默认的 web.telegram.org 网页版共享凭据。
# Telegram 官方明确网页版不属于「可接收登录验证码的客户端类型」，
# 用它**收不到验证码**——很多人卡在这一步。
#
# 程序启动时只要读到 2496（或配套的 8da85b0d... 这个 hash），
# 就会自动替换为 TGPan 内置的移动端凭据，无需你手动改。
# 因此你在「自检」页面看到 27335138 是正常的、正确的。
#
# 想用你自己申请的 api_id / api_hash，二选一：
#   1. 直接改下面两行
#   2. 设环境变量 TELDRIVE_TG_APP_ID / TELDRIVE_TG_APP_HASH（优先级最高）
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
# 首窗口之后的并发放大倍数。
#
# 【为什么默认值从 8 降到 3】
# 真机实测（internal/reader/timing_test.go，单块耗时基准 600ms）：
#   prefetch=1  吞吐 1.66 MB/s  —— 只够 1080p
#   prefetch=2  吞吐 3.13 MB/s  —— 可播 4K
#   prefetch=3  吞吐 4.44 MB/s  —— 4K 有余量  ← 默认值
#   prefetch=8  吞吐 10.65 MB/s —— 远超需求，纯属抢带宽
#
# 1080p 只要 0.5-1.25 MB/s，4K 也就 1.9-5 MB/s。开 8 路是拿 10 MB/s
# 的能力去干 4 MB/s 的活，多出来的并发不会更快，反而：
#   1. 抢占首块带宽 —— 你要播的第一块得跟自己后面 7 路抢，起播更慢
#   2. 打满隧道 —— 在 CF 免费隧道这种受条款限制的链路上尤其容易触发限速
# 3 是实测出来的平衡点：4K 有富余，又不会把链路占满。
prefetch-windows = 3
# 单个分片下载超时。30s 太长 —— 真撞上坏块时，一整轮都得陪它等满才报错。
chunk-timeout = '20s'

[cronjobs]
enable = true
# 必须显式写这一项。
# Teldrive 的 cron 底层用 gocron-gorm-lock 做分布式锁，它要求实例名非空，
# 否则启动时报 "cron.init.failed: worker is required"。
# 结构体标签里虽有 default:"cron-locker"，但配置文件中一旦出现 [cronjobs] 段，
# 该段的未列字段不会回填默认值，所以这里必须写死。
locker-instance = 'tgpan'
clean-files-interval = '1h'
clean-uploads-interval = '12h'
folder-size-interval = '2h'
EOF
  echo "[init] 配置写入 $CONF"
fi

echo "[start] 启动服务..."
exec /usr/bin/supervisord -c /etc/supervisord.conf
