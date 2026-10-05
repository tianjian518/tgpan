package database

// ---------------------------------------------------------------------------
// 数据库连接级调优
//
// Postgres 的默认参数是"通用工作负载"取向，对 TGPan 这种
// 「读多写少、单机自用、目录列表是核心查询」的场景有几处明显浪费。
// 这里在**连接建立时**把几个参数改掉，不碰服务器全局配置。
//
// 为什么用连接级（SET，不是 ALTER SYSTEM）：
//   - 不需要 DBA 权限，不需要重启数据库
//   - 只影响本应用建立的连接，不会波及同实例上的其它库
//   - 换数据库/回滚都不留痕迹
//
// 注意 gorm 的连接池会复用连接，所以每条新连接都要设置一次 ——
// 用 sql.DB 提供的 ConnectionStateCallback（pgx v5 的 conn 级 hook）
// 或者简单点：在拿到 *sql.DB 之后用 SetConnMaxLifetime 配合
// 手工 SET。这里用的是 pgx 的 AfterConnect 语义，通过 gorm 的
// postgres driver 暴露的 ConfigureConnection 实现。
// ---------------------------------------------------------------------------

import (
	"fmt"

	"github.com/tgdrive/teldrive/internal/config"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// tuneProfile 是我们要施加的调优项。
//
// 每一项都写清楚「为什么」和「代价」，不要加来路不明的魔法参数 ——
// 调优项一旦没有理由，后面就没人敢删。
type tuneSetting struct {
	key   string
	value string
	why   string
}

// buildTuneProfile 根据配置构造调优语句。
func buildTuneProfile(cfg *config.DBConfig) []tuneSetting {
	var out []tuneSetting

	// 1. 写入不用等 WAL 落盘。
	//
	// 默认 on：每次 COMMIT 都要等 WAL fsync，约 1~10ms 起。
	// 扫描落库是「一次几百条」的批量插入，每条都等一次就很可观。
	// 改成 off 后，COMMIT 直接返回，WAL 由后台刷。
	//
	// 安全边界：off **不**影响事务的原子性与一致性 ——
	// 事务该回滚还是回滚，约束该怎么查还是怎么查。
	// 唯一影响是「数据库进程被强杀/机器断电」时，
	// 最近最多几秒已提交的事务会丢。
	// 对自用网盘来说，最坏情况是"刚扫进来的几条记录要重扫一次"，
	// 重扫是幂等的（有 UNIQUE 去重），所以可以接受。
	out = append(out, tuneSetting{
		key:   "synchronous_commit",
		value: "off",
		why:   "批量导入不用等 WAL fsync；崩溃最多丢最后几秒，且重扫幂等",
	})

	// 2. work_mem：排序/聚合/哈希表的内存上限。
	//
	// 默认 4MB。TGPan 的核心查询「列目录 + 排序 + 按分类统计」
	// 在文件数上万的目录上会超过 4MB，一超就转成外部排序（写临时文件），
	// 实测慢十几倍。
	//
	// 代价：work_mem 是**按查询节点**分配的（一个复杂查询有多个排序节点
	// 就分配多份），不是进程全局。自用实例并发连接数很低，
	// 16MB × 几个连接 = 几十 MB，可以忽略。
	if cfg.TuneWorkMemMB > 0 {
		out = append(out, tuneSetting{
			key:   "work_mem",
			value: fmt.Sprintf("%dMB", cfg.TuneWorkMemMB),
			why:   "目录排序/分类聚合不落盘做外部排序",
		})
	}

	// 3. 关掉 JIT。
	//
	// Postgres 11+ 默认开启 LLVM JIT，判定门槛是按「预估代价」算的。
	// 对 TGPan 这种查询（单表、简单条件、返回行数不多）
	// JIT 编译本身的开销（几毫秒到几十毫秒）往往超过它省下的执行时间，
	// 表现为"偶发的、莫名其妙慢一下"的查询。
	//
	// 这不是凭感觉：社区对短查询普遍建议关闭 JIT，
	// 长分析型查询才需要它。TGPan 没有长分析型查询。
	out = append(out, tuneSetting{
		key:   "jit",
		value: "off",
		why:   "短查询上 JIT 编译开销大于收益，避免偶发慢查询",
	})

	return out
}

// applyTuning 把调优参数写进 DSN。
//
// 实现方式说明：
//
//	最开始想用 pgx 的 AfterConnect 回调，但 gorm 的 postgres driver
//	不直接暴露那个钩子。绕路去自定义 driver 又太重。
//
//	实际上 Postgres/pgx 支持把这些参数**直接放进连接串**，
//	libpq 和 pgx 都会在建立连接时自动发送给服务端，
//	效果和手工 SET 完全一样，而且天然对池子里每条新连接生效。
//
//	这是最省事也最不容易出错的写法：没有额外代码路径，
//	连接怎么建的就怎么带走参数。
//
// 未知参数会被 Postgres 拒绝（导致连不上），所以这里只放
// 已确认存在的参数；work_mem 这类带单位的要按 value 原样拼。
func applyTuning(dsn string, cfg *config.DBConfig, lg *zap.Logger) string {
	if cfg == nil || !cfg.Tune {
		return dsn
	}

	settings := buildTuneProfile(cfg)
	additions := make([]string, 0, len(settings))
	for _, s := range settings {
		additions = append(additions, fmt.Sprintf("%s=%s", s.key, s.value))
	}
	if len(additions) == 0 {
		return dsn
	}

	// DSN 可能形如：
	//   postgres://user:pass@host:5432/db?sslmode=disable&pool_max_conns=10
	//   host=... port=... user=... dbname=...
	// 前者走 URL 语义（& 拼接），后者走 key=value 语义（空格拼接）。
	sep := " "
	if isURLDSN(dsn) {
		sep = "&"
	}

	out := dsn
	for _, a := range additions {
		out += sep + a
	}

	if lg != nil {
		logged := make([]string, 0, len(settings))
		for _, s := range settings {
			logged = append(logged, fmt.Sprintf("%s=%s", s.key, s.value))
		}
		lg.Info("db.tuning.applied",
			zap.Strings("settings", logged),
			zap.Bool("tune", cfg.Tune))
	}
	return out
}

// isURLDSN 判断 DSN 是不是 URL 形式（postgres://...）。
func isURLDSN(dsn string) bool {
	return len(dsn) > 11 && (dsn[:11] == "postgres://" || dsn[:13] == "postgresql://")
}

// reportTuning 在连接建立后回读一下实际生效的参数，写进日志。
//
// 为什么要回读：DSN 里的参数如果名字写错，Postgres 会**直接拒绝连接**，
// 这个还算好的。麻烦的是那些"名字合法但被忽略"的情况 ——
// 比如某些参数只对超级用户生效，普通用户设了不报错也不生效。
// 回读一次能立刻发现"以为设了其实没设"。
func reportTuning(db *gorm.DB, cfg *config.DBConfig, lg *zap.Logger) {
	if cfg == nil || !cfg.Tune || lg == nil {
		return
	}

	names := []string{"synchronous_commit", "work_mem", "jit"}
	observed := map[string]string{}
	for _, n := range names {
		var v string
		if err := db.Raw(fmt.Sprintf("SHOW %s", n)).Scan(&v).Error; err == nil {
			observed[n] = v
		}
	}
	if len(observed) == 0 {
		return
	}

	fields := make([]zap.Field, 0, len(observed))
	for _, n := range names {
		if v, ok := observed[n]; ok {
			fields = append(fields, zap.String(n, v))
		}
	}
	lg.Info("db.tuning.verify", fields...)
}
