package tgc

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/go-faster/errors"
	"github.com/gotd/contrib/clock"
	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/tgdrive/teldrive/internal/cache"
	"github.com/tgdrive/teldrive/internal/config"
	"github.com/tgdrive/teldrive/internal/logging"
	"github.com/tgdrive/teldrive/internal/recovery"
	"github.com/tgdrive/teldrive/internal/retry"
	"github.com/tgdrive/teldrive/internal/tgstorage"
	"github.com/tgdrive/teldrive/internal/utils"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

// applyTGEnvOverrides 允许通过环境变量覆盖 Telegram 应用凭据与设备信息。
//
// 背景：Teldrive 默认使用 app-id=2496 / device-model=Firefox 的"网页版"伪装，
// Telegram 对这种客户端可能不投递登录验证码（官方文档明确网页版 WebK/WebA
// 不属于可接收验证码的客户端类型）。通过环境变量换成用户自己的应用凭据 +
// 真实移动端设备信息，可显著提高验证码投递成功率。
//
// 支持的环境变量（优先级高于配置文件）：
//
//	TELDRIVE_TG_APP_ID        整数，Telegram api_id
//	TELDRIVE_TG_APP_HASH      字符串，Telegram api_hash
//	TELDRIVE_TG_DEVICE_MODEL  设备型号，如 SM-G9980
//	TELDRIVE_TG_SYSTEM_VERSION 系统版本，如 SDK 34
//	TELDRIVE_TG_APP_VERSION   应用版本，如 11.2.0
//	TELDRIVE_TG_LANG_CODE     语言代码，如 zh
//	TELDRIVE_TG_LANG_PACK     语言包，留空则使用默认
//	TELDRIVE_TG_REAL_DEVICE   设为 1 时，强制切换为真实移动端身份（默认开启）
func applyTGEnvOverrides(cfg *config.TGConfig) {
	if cfg == nil {
		return
	}

	// 兜底：若配置里仍是 Teldrive 内置的 web.telegram.org 共享凭据（2496），
	// 则替换为 TGPan 内置的应用凭据（官方已确认网页版凭据无法接收登录验证码）。
	if cfg.AppId == 2496 || cfg.AppHash == "8da85b0d5bfe62527e5b244c209159c3" {
		cfg.AppId = 27335138
		cfg.AppHash = "2459555ba95421148c682e2dc3031bb6"
	}

	// 环境变量优先级最高，可覆盖内置凭据
	if v := strings.TrimSpace(os.Getenv("TELDRIVE_TG_APP_ID")); v != "" {
		if id, err := strconv.Atoi(v); err == nil && id > 0 {
			cfg.AppId = id
		}
	}
	if v := strings.TrimSpace(os.Getenv("TELDRIVE_TG_APP_HASH")); v != "" {
		cfg.AppHash = v
	}

	// 真实设备身份：只有在用户显式覆盖或未禁用时才启用。
	// TELDRIVE_TG_REAL_DEVICE=0 可关闭，回到 Teldrive 原版行为。
	realDevice := strings.TrimSpace(os.Getenv("TELDRIVE_TG_REAL_DEVICE")) != "0"

	if realDevice {
		// 去掉 Firefox 浏览器伪装，改用真实 Android 设备身份
		cfg.DeviceModel = "SM-G9980"
		cfg.SystemVersion = "SDK 34"
		cfg.AppVersion = "11.2.0"
		cfg.SystemLangCode = "zh-CN"
		cfg.LangCode = "zh"
		cfg.LangPack = ""
	}

	if v := strings.TrimSpace(os.Getenv("TELDRIVE_TG_DEVICE_MODEL")); v != "" {
		cfg.DeviceModel = v
	}
	if v := strings.TrimSpace(os.Getenv("TELDRIVE_TG_SYSTEM_VERSION")); v != "" {
		cfg.SystemVersion = v
	}
	if v := strings.TrimSpace(os.Getenv("TELDRIVE_TG_APP_VERSION")); v != "" {
		cfg.AppVersion = v
	}
	if v, ok := os.LookupEnv("TELDRIVE_TG_LANG_CODE"); ok {
		cfg.LangCode = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("TELDRIVE_TG_LANG_PACK"); ok {
		cfg.LangPack = strings.TrimSpace(v)
	}
}

func newClient(ctx context.Context, config *config.TGConfig, handler telegram.UpdateHandler, storage session.Storage, middlewares ...telegram.Middleware) (*telegram.Client, error) {

	// 应用环境变量覆盖（应用凭据 + 真实设备身份），必须在构造客户端之前。
	applyTGEnvOverrides(config)

	var dialer dcs.DialFunc = proxy.Direct.DialContext
	if config.Proxy != "" {
		d, err := utils.Proxy.GetDial(config.Proxy)
		if err != nil {
			return nil, errors.Wrap(err, "get dialer")
		}
		dialer = d.DialContext
	}

	var logger *zap.Logger
	if config.EnableLogging {
		logger = logging.Component("TG")
	}

	opts := telegram.Options{
		Resolver: dcs.Plain(dcs.PlainOptions{
			Dial: dialer,
		}),
		ReconnectionBackoff: func() backoff.BackOff {
			return newBackoff(config.ReconnectTimeout)
		},
		Device: telegram.DeviceConfig{
			DeviceModel:    config.DeviceModel,
			SystemVersion:  config.SystemVersion,
			AppVersion:     config.AppVersion,
			SystemLangCode: config.SystemLangCode,
			LangPack:       config.LangPack,
			LangCode:       config.LangCode,
		},
		SessionStorage: storage,
		RetryInterval:  2 * time.Second,
		MaxRetries:     20,
		DialTimeout:    10 * time.Second,
		Middlewares:    middlewares,
		UpdateHandler:  handler,
		Logger:         logger,
	}
	if config.Ntp {
		c, err := clock.NewNTP()
		if err != nil {
			return nil, errors.Wrap(err, "create clock")
		}
		opts.Clock = c

	}

	return telegram.NewClient(config.AppId, config.AppHash, opts), nil
}

func NoAuthClient(ctx context.Context, config *config.TGConfig, handler telegram.UpdateHandler, storage session.Storage) (*telegram.Client, error) {
	middlewares := []telegram.Middleware{
		floodwait.NewSimpleWaiter(),
	}
	middlewares = append(middlewares, ratelimit.New(rate.Every(time.Millisecond*100), 5))
	return newClient(ctx, config, handler, storage, middlewares...)
}

func AuthClient(ctx context.Context, config *config.TGConfig, sessionStr string, middlewares ...telegram.Middleware) (*telegram.Client, error) {
	data, err := session.TelethonSession(sessionStr)

	if err != nil {
		return nil, err
	}

	var (
		storage = new(session.StorageMemory)
		loader  = session.Loader{Storage: storage}
	)

	if err := loader.Save(ctx, data); err != nil {
		return nil, err
	}
	return newClient(ctx, config, nil, storage, middlewares...)
}

// BotClient creates a Telegram client for bot authentication.
// Uses database-backed session storage for persistent bot sessions.
// Note: storage remains open for client's lifetime - do not close it here
func BotClient(ctx context.Context, db *gorm.DB, cache cache.Cacher, config *config.TGConfig, token string, middlewares ...telegram.Middleware) (*telegram.Client, error) {
	// Use bot token ID (part before colon) as session key
	botID := strings.Split(token, ":")[0]
	storage, err := tgstorage.NewSessionStorage(config.Session, db, cache, botID)
	if err != nil {
		return nil, err
	}
	// Storage must remain open for the client's entire lifetime
	// It will be garbage collected when the client is no longer referenced
	return newClient(ctx, config, nil, storage, middlewares...)
}

type middlewareOption func(*middlewareConfig)

type middlewareConfig struct {
	config      *config.TGConfig
	middlewares []telegram.Middleware
}

func NewMiddleware(config *config.TGConfig, opts ...middlewareOption) []telegram.Middleware {
	mc := &middlewareConfig{
		config:      config,
		middlewares: []telegram.Middleware{},
	}
	for _, opt := range opts {
		opt(mc)
	}
	return mc.middlewares
}

func WithFloodWait() middlewareOption {
	return func(mc *middlewareConfig) {
		mc.middlewares = append(mc.middlewares, floodwait.NewSimpleWaiter())
	}
}

func WithRecovery(ctx context.Context) middlewareOption {
	return func(mc *middlewareConfig) {
		mc.middlewares = append(mc.middlewares,
			recovery.New(ctx, newBackoff(mc.config.ReconnectTimeout)))
	}
}

func WithRetry(retries int) middlewareOption {
	return func(mc *middlewareConfig) {
		mc.middlewares = append(mc.middlewares, retry.New(retries))
	}
}

func WithRateLimit() middlewareOption {
	return func(mc *middlewareConfig) {
		if mc.config.RateLimit {
			mc.middlewares = append(mc.middlewares,
				ratelimit.New(rate.Every(time.Millisecond*time.Duration(mc.config.Rate)), mc.config.RateBurst))
		}
	}
}

func newBackoff(timeout time.Duration) backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.Multiplier = 1.1
	b.MaxElapsedTime = timeout
	b.MaxInterval = 10 * time.Second
	return b
}
