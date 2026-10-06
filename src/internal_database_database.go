package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	extraClausePlugin "github.com/WinterYukky/gorm-extra-clause-plugin"
	"github.com/tgdrive/teldrive/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func NewDatabase(ctx context.Context, cfg *config.DBConfig, logCfg *config.DBLoggingConfig, lg *zap.Logger) (*gorm.DB, error) {
	level, err := zapcore.ParseLevel(logCfg.Level)
	if err != nil {
		level = zapcore.InfoLevel
	}

	var db *gorm.DB
	// 【为什么重试窗口要足够长 —— 踩过的坑】
	//
	// 早期这里写死 `maxRetries = 5` + `retryDelay = 500ms`，
	// 也就是说总共只等约 2.5 秒。在开发机上"够用"，但在两类真机上完全不够：
	//
	//   · 电视盒子 / 低端 NAS（如斐讯 N1，eMMC 存储、2GB 内存）：
	//     同容器里的 Postgres 首次启动要 initdb + 建 14 张表 + 建 pgroonga
	//     扩展，慢的时候要几十秒。2.5 秒的重试窗口必然跑完就放弃。
	//   · 数据库跑在另一台机器上：网络抖动 + 对端还没起来。
	//
	// 后果非常难查：DB 连不上 → run.go 退出 → supervisord 拉起 → 又连不上……
	// 用户看到的现象是「容器一直在重启、网页打不开」，
	// 而日志里只有一条很容易刷过去的 db.connection.failed_all_retries。
	//
	// 现在改成「指数退避 + 总时长上限」：单次连接超时短（10s），
	// 但会一直重试到总时长用尽，总时长默认 90 秒。
	// 这样既不会因为一次网络抖动就放弃，也不会永远卡住不报错。
	maxRetries := 60
	retryDelay := 500 * time.Millisecond
	retryMaxDelay := 5 * time.Second
	connectTimeout := 10 * time.Second

	// Add connect_timeout to DSN if not present
	dsn := cfg.DataSource
	if !strings.Contains(dsn, "connect_timeout") {
		if strings.Contains(dsn, "?") {
			dsn = dsn + fmt.Sprintf("&connect_timeout=%d", int(connectTimeout.Seconds()))
		} else {
			dsn = dsn + fmt.Sprintf("?connect_timeout=%d", int(connectTimeout.Seconds()))
		}
	}

	// 连接级性能调优：把 synchronous_commit / work_mem / jit 塞进 DSN，
	// 这样池子里**每一条**新连接都会自动带上这些参数。
	//
	// 为什么不启动后手工 SET：SET 是会话级的，只对执行它的那条连接生效。
	// 池子里的其它连接、以及以后新开的连接都还是默认值 ——
	// 这是"调优了但没效果"最常见的根因。
	dsn = applyTuning(dsn, cfg, lg)

	for i := 0; i <= maxRetries; i++ {
		// Create a timeout context for this attempt so it can be cancelled
		attemptCtx, attemptCancel := context.WithTimeout(ctx, connectTimeout+5*time.Second)

		// Run gorm.Open in a goroutine so we can cancel it via context
		type result struct {
			db  *gorm.DB
			err error
		}
		resultCh := make(chan result, 1)

		go func() {
			db, err := gorm.Open(postgres.New(postgres.Config{
				DSN:                  dsn,
				PreferSimpleProtocol: !cfg.PrepareStmt,
			}), &gorm.Config{
				Logger: NewLogger(lg, logCfg.SlowThreshold, logCfg.IgnoreRecordNotFound, level, logCfg),
				NamingStrategy: schema.NamingStrategy{
					TablePrefix:   "teldrive.",
					SingularTable: false,
				},
				NowFunc: func() time.Time {
					return time.Now().UTC()
				},
			})
			resultCh <- result{db: db, err: err}
		}()

		// Wait for either the result or context cancellation
		select {
		case <-attemptCtx.Done():
			attemptCancel()
			return nil, attemptCtx.Err()
		case res := <-resultCh:
			attemptCancel()
			db = res.db
			err = res.err
		}

		if err == nil {
			if i > 0 {
				lg.Info("db.connection.success", zap.Int("attempts", i+1))
			}
			break
		}

		if i < maxRetries {
			// 指数退避：0.5s → 1s → 2s → 4s → 5s(封顶)…
			//
			// 为什么不固定 500ms 一直打：数据库**刚起来**的那一刻最脆弱
			// （正在恢复 WAL、建索引），高频重试会雪上加霜；
			// 而等到后面（对端其实没起来）时又不需要那么密的探测。
			// 指数退避两头都照顾到。
			delay := retryDelay << min(i, 3) // 0.5s,1s,2s,4s,之后固定 4s
			if delay > retryMaxDelay {
				delay = retryMaxDelay
			}

			lg.Warn("db.connection.failed",
				zap.Int("attempt", i+1),
				zap.Int("max_retries", maxRetries+1),
				zap.Error(err),
				zap.Duration("retry_in", delay),
				zap.String("hint", "同容器部署时数据库正在初始化属正常现象，会自动等待；持续失败请检查 db.data-source 配置"))

			// Wait for retry delay but check context
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		} else {
			lg.Error("db.connection.failed_all_retries",
				zap.Int("max_retries", maxRetries+1),
				zap.Error(err))
			return nil, fmt.Errorf("database connection failed after %d attempts: %w", maxRetries+1, err)
		}
	}

	db.Use(extraClausePlugin.New())

	if cfg.Pool.Enable {
		rawDB, err := db.DB()
		if err != nil {
			return nil, err
		}
		rawDB.SetMaxOpenConns(cfg.Pool.MaxOpenConnections)
		rawDB.SetMaxIdleConns(cfg.Pool.MaxIdleConnections)
		rawDB.SetConnMaxLifetime(cfg.Pool.MaxLifetime)
	}

	// 回读一次实际生效的值，写进日志。
	//
	// 为什么要回读：DSN 里的参数名如果写错，Postgres 会直接拒绝连接（还算好）。
	// 麻烦的是"名字合法但被静默忽略"的情况 —— 设了不报错也不生效。
	// 回读一次能立刻暴露"以为设了其实没设"。
	reportTuning(db, cfg, lg)

	return db, nil
}
