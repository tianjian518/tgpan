package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"
	"github.com/tgdrive/teldrive/internal/api"
	"github.com/tgdrive/teldrive/internal/appcontext"
	"github.com/tgdrive/teldrive/internal/auth"
	"github.com/tgdrive/teldrive/internal/banner"
	"github.com/tgdrive/teldrive/internal/cache"
	"github.com/tgdrive/teldrive/internal/chizap"
	"github.com/tgdrive/teldrive/internal/config"
	"github.com/tgdrive/teldrive/internal/database"
	"github.com/tgdrive/teldrive/internal/events"
	"github.com/tgdrive/teldrive/internal/logging"
	"github.com/tgdrive/teldrive/internal/middleware"
	"github.com/tgdrive/teldrive/internal/tgc"
	"github.com/tgdrive/teldrive/internal/version"
	"github.com/tgdrive/teldrive/ui"

	"github.com/tgdrive/teldrive/pkg/cron"
	"github.com/tgdrive/teldrive/pkg/services"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gorm.io/gorm"
)

func NewRun() *cobra.Command {
	var cfg config.ServerCmdConfig
	loader := config.NewConfigLoader()
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start Teldrive Server",
		Run: func(cmd *cobra.Command, args []string) {
			runApplication(cmd.Context(), &cfg)

		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if err := loader.Load(cmd, &cfg); err != nil {
				return err
			}
			if err := loader.Validate(&cfg); err != nil {
				return err
			}
			return nil
		},
	}
	loader.RegisterFlags(cmd.Flags(), reflect.TypeFor[config.ServerCmdConfig]())
	return cmd
}

func findAvailablePort(startPort int) (int, error) {
	for port := startPort; port < startPort+100; port++ {
		addr := fmt.Sprintf(":%d", port)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		listener.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no available ports found between %d and %d", startPort, startPort+100)
}

func runApplication(ctx context.Context, conf *config.ServerCmdConfig) {
	lvl, err := zapcore.ParseLevel(conf.Log.Level)
	if err != nil {
		lvl = zapcore.InfoLevel
	}
	logging.SetConfig(&logging.Config{
		Level:    lvl,
		FilePath: conf.Log.File,
	})

	lg := logging.Component("APP")
	defer lg.Sync()

	banner.PrintBanner(banner.StartupInfo{
		Version:  version.Version,
		Addr:     fmt.Sprintf(":%d", conf.Server.Port),
		LogLevel: conf.Log.Level,
	})

	port, err := findAvailablePort(conf.Server.Port)
	if err != nil {
		lg.Error("failed to find available port", zap.Error(err))
		os.Exit(1)
	}
	if port != conf.Server.Port {
		lg.Info("server.port_occupied", zap.Int("occupied_port", conf.Server.Port), zap.Int("new_port", port))
		conf.Server.Port = port
	}

	// Channel for background service initialization errors
	initErrCh := make(chan error, 3)

	// Create cancellable context for background services
	bgCtx, bgCancel := context.WithCancel(ctx)
	defer bgCancel()

	// Start Redis and cache initialization in background
	var redisClient *redis.Client
	var cacher cache.Cacher
	var botSelector tgc.BotSelector
	var redisOnce sync.Once
	var redisReady = make(chan struct{})

	go func() {
		client, err := cache.NewRedisClient(bgCtx, &conf.Redis)
		if err != nil {
			lg.Error("redis.client_failed", zap.Error(err))
			initErrCh <- fmt.Errorf("redis connection failed: %w", err)
			return
		}
		redisClient = client
		cacher = cache.NewCache(bgCtx, conf.Cache.MaxSize, redisClient, lg)
		botSelector = tgc.NewBotSelector(redisClient)
		redisOnce.Do(func() { close(redisReady) })
	}()

	// Initialize database (blocking - server needs this)
	db, err := database.NewDatabase(ctx, &conf.DB, &conf.Log.DB, lg)
	if err != nil {
		lg.Error("failed to create database", zap.Error(err))
		os.Exit(1)
	}

	if err := database.MigrateDB(db); err != nil {
		lg.Error("failed to migrate database", zap.Error(err))
		os.Exit(1)
	}

	// Wait for cache to be ready before setting up server
	select {
	case <-redisReady:
		// Cache ready, continue
	case <-ctx.Done():
		lg.Error("server.startup_cancelled")
		os.Exit(1)
	}

	// Create broadcaster config from settings
	broadcasterConfig := events.BroadcasterConfig{
		DBWorkers:        conf.Events.DBWorkers,
		DBBufferSize:     conf.Events.DBBufferSize,
		DeduplicationTTL: conf.Events.DeduplicationTTL,
	}

	// Start event broadcaster in background
	var eventBroadcaster events.EventBroadcaster
	var eventsOnce sync.Once
	var eventsReady = make(chan struct{})

	go func() {
		eventBroadcaster = events.NewBroadcaster(bgCtx, db, redisClient, conf.Events.PollInterval, broadcasterConfig, logging.Component("EVENT"))
		eventsOnce.Do(func() { close(eventsReady) })
	}()

	// Wait for events to be ready
	select {
	case <-eventsReady:
		// Events ready, continue
	case <-ctx.Done():
		lg.Error("server.startup_cancelled")
		os.Exit(1)
	}

	// Setup and start HTTP server immediately
	srv := setupServer(conf, db, cacher, lg, botSelector, eventBroadcaster)

	serverErrCh := make(chan error, 1)
	go func() {
		lg.Info("server.started", zap.String("address", fmt.Sprintf("http://localhost:%d", conf.Server.Port)))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()

	// Start cron jobs in background if enabled
	if conf.CronJobs.Enable {
		go func() {
			if err := cron.StartCronJobs(bgCtx, db, conf); err != nil {
				lg.Error("cron.init.failed", zap.Error(err))
				initErrCh <- fmt.Errorf("cron scheduler failed: %w", err)
				return
			}
			lg.Debug("cron.init.completed")
		}()
	}

	// Main thread: wait for shutdown signal or fatal error
	select {
	case <-ctx.Done():
		lg.Info("server.shutdown_signal_received")
	case err := <-initErrCh:
		lg.Error("background_service.failed", zap.Error(err))
		os.Exit(1)
	case err := <-serverErrCh:
		lg.Error("server.crashed", zap.Error(err))
		os.Exit(1)
	}

	// Graceful shutdown sequence
	lg.Info("server.shutdown.starting")

	// Cancel background context to stop all background services
	bgCancel()

	// Shutdown event broadcaster
	if eventBroadcaster != nil {
		eventBroadcaster.Shutdown()
	}

	// Shutdown HTTP server with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), conf.Server.GracefulShutdown)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		lg.Error("server.shutdown.failed", zap.Error(err))
	}

	// Close Redis client if it was created
	if redisClient != nil {
		redisClient.Close()
	}

	lg.Info("server.stopped")
}

func setupServer(cfg *config.ServerCmdConfig, db *gorm.DB, cache cache.Cacher, lg *zap.Logger, botSelector tgc.BotSelector, eventBroadcaster events.EventBroadcaster) *http.Server {

	apiSrv := services.NewApiService(db, cfg, cache, botSelector, eventBroadcaster)

	secHandler := auth.NewSecurityHandler(db, cache, &cfg.JWT)

	// 把「TG 主凭证兜底」接到鉴权层：
	// 浏览器没有有效 Cookie 时（内网免密入口 / 换设备），
	// 只要已经配对过 TG，就用服务端主凭证放行，不再要求扫码。
	services.WireMasterClaims(secHandler, apiSrv)

	srv, err := api.NewServer(apiSrv, secHandler)

	if err != nil {
		lg.Error("failed to create server", zap.Error(err))
		os.Exit(1)
		return nil // unreachable but required for compilation
	}

	extendedSrv := services.NewExtendedMiddleware(srv, services.NewExtendedService(apiSrv))

	// 启动频道自动扫描调度器。
	// 内部有 45 秒启动延迟，会等 TG 连接池与数据库就绪后再开始，
	// 因此在这里立即启动是安全的。
	services.StartScanSchedulerFor(extendedSrv)

	mux := chi.NewRouter()

	mux.Use(chimiddleware.Recoverer)
	mux.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		// 必须显式列出 PROPFIND/PROPPATCH/MKCOL/COPY/MOVE/LOCK/UNLOCK。
		// WebDAV 播放器（网易爆米花 / Infuse / VidHub）用 PROPFIND 列目录，
		// 这些方法不在白名单时，CORS 中间件会直接回 405，请求根本到不了
		// WebDAV 处理器——表现为"挂载失败"，但日志里看不到任何 WebDAV 记录。
		AllowedMethods: []string{
			"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH", "HEAD",
			"PROPFIND", "PROPPATCH", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK", "REPORT",
		},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "Depth", "If", "Destination", "Overwrite", "Range"},
		ExposedHeaders: []string{"DAV", "Content-Range", "Accept-Ranges", "Content-Length", "ETag", "Last-Modified"},
		MaxAge:         86400,
	}))
	mux.Use(chimiddleware.RealIP)
	mux.Use(middleware.InjectLogger(lg))
	mux.Use(chizap.ChizapWithConfig(logging.Component("HTTP"), &chizap.Config{
		SkipPathRegexps: []*regexp.Regexp{
			regexp.MustCompile(`^/(assets|images|docs)/.*`),
		},
		HTTPConfig: &cfg.Log.HTTP,
	}))
	mux.Use(appcontext.Middleware)
	// 诊断页：必须挂在根路径，否则会被下面的 SPAHandler("/*") 兜走
	mux.Handle("/diag", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/diag"
		extendedSrv.ServeHTTP(w, r2)
	}))
	mux.Handle("/diag.json", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/diag.json"
		extendedSrv.ServeHTTP(w, r2)
	}))
	mux.Handle("/logincheck", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/logincheck"
		extendedSrv.ServeHTTP(w, r2)
	}))
	mux.Handle("/authlog", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/authlog"
		extendedSrv.ServeHTTP(w, r2)
	}))

	// WebDAV 根挂载：播放器（网易爆米花 / Infuse / VidHub）填写的是
	// http://<内网IP>:<端口>/webdav ，不带 /api 前缀。
	//
	// 这里不能用 mux.Handle / mux.Mount：chi v5 的 Handle 内部走 mALL 方法掩码，
	// 而它的 methodMap 只认识标准 HTTP 方法，PROPFIND / PROPPATCH / MKCOL
	// 这些 WebDAV 专用方法压根不在里面，注册了也不会命中，一律回 405。
	//
	// 正确做法是把它挂到 chi 之前的 HTTP 层：下面用 webdavRouter 包住整个 mux，
	// 只要路径以 /webdav 开头就交给 WebDAV 处理器，其余请求原样透传给 chi。
	// 这样 WebDAV 完全不受 chi 方法白名单影响。
	//
	// 注意不要 StripPrefix：处理器内部按 /webdav 前缀解析路径并生成 href，
	// 剥掉前缀会导致 PROPFIND 返回的链接指向错误位置，播放器点不开。
	mux.Mount("/api/", http.StripPrefix("/api", extendedSrv))

	// 凭据管理接口的「不带 /api」形式：/webdav/credentials[/{id}]
	//
	// 为什么需要这条：上面 rootHandler 已把 /webdav/* 整体交给 WebDAV 处理器，
	// 但凭据接口是网页调用的、走闸门 cookie 而非 Basic。所以要在这里
	// 把它单独接管回来，交给 extendedSrv（它会剥掉 /webdav 前缀再分发）。
	//
	// 用 HandleFunc + 方法掩码：这几个都是标准方法（GET/POST/DELETE），
	// 不涉及 PROPFIND 那类 chi 不认的方法。
	mux.HandleFunc("/webdav/credentials", func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/webdav/credentials"
		extendedSrv.ServeHTTP(w, r2)
	})
	mux.HandleFunc("/webdav/credentials/", func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = r.URL.Path
		extendedSrv.ServeHTTP(w, r2)
	})

	mux.Handle("/*", middleware.SPAHandler(ui.StaticFS))

	rootHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path

		// ---- WebDAV 文件访问：绕开 chi ----
		//
		// 挂到 HTTP 层是为了绕开 chi 的方法白名单（PROPFIND/MKCOL 等
		// 不在 chi 的 methodMap 里，走 mux 一律 405）。
		//
		// 两种入口都要接管：
		//   /webdav/...      播放器直接填的地址
		//   /api/webdav/...  某些客户端会自动加 /api
		//
		// 但都要排除 /webdav/credentials —— 那是网页用的凭据管理接口，
		// 走闸门 cookie 认证，必须交给 extendedSrv 正常分发，
		// 不能被当成 DAV 路径返回 Basic 401。
		isCred := p == "/webdav/credentials" ||
			strings.HasPrefix(p, "/webdav/credentials/") ||
			p == "/api/webdav/credentials" ||
			strings.HasPrefix(p, "/api/webdav/credentials/")

		if !isCred && (strings.HasPrefix(p, "/webdav") || strings.HasPrefix(p, "/api/webdav")) {
			// 把 /api/webdav/... 归一成 /webdav/... 再交给处理器。
			//
			// 为什么不在处理器里兼容两种前缀：WebDAV 处理器要按路径
			// 反查网盘目录（parsePath），前缀形态越多越容易漏。在这里
			// 一次性归一，下游就只需认识一种形态，href 也不会指错。
			if strings.HasPrefix(p, "/api/webdav") {
				r2 := r.Clone(r.Context())
				r2.URL.Path = strings.TrimPrefix(p, "/api")
				r2.RequestURI = r2.URL.Path
				extendedSrv.ServeHTTP(w, r2)
				return
			}
			extendedSrv.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})

	return &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           rootHandler,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
