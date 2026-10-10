package tgc

import (
	"context"
	"net"
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
	"github.com/gotd/td/transport"
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

// ApplyEnvOverrides 允许通过环境变量覆盖 Telegram 应用凭据与设备信息。
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
//
// 【为什么必须是公开函数 + 必须在启动时先调一次】
//
// 这个函数原本是私有的，只在 newClient 里调用 —— 也就是**第一次要连 TG 时**
// 才执行。问题在于：
//
//	配置加载（cmd/run.go 的 PersistentPreRunE）
//	  ↓
//	  读取 app-id = 2496（配置文件里就是这么写的）
//	  ↓
//	  ★ 这里有个空档：还没人连 TG，applyTGEnvOverrides 尚未执行 ★
//	  ↓
//	  「自检」页面（pkg/services/diag.go）读 cnf.TG.AppId
//	  ↓
//	  显示 app-id=2496  ← 用户看到的就是这个
//
// 结果是：**TG 客户端用的凭据其实是对的（27335138），但界面显示的是错的（2496）**。
// 用户据此误判「凭据没生效 / 收不到验证码」，实际两者不一致，排查方向被带偏。
//
// 修法：把这个函数导出，在配置加载完成后**立刻**调用一次，让内存里的配置
// 从一开始就是终态。newClient 里仍保留调用（幂等，无害），兼顾其他入口
// （如 cmd/check.go）不经过 runApplication 的情况。
func ApplyEnvOverrides(cfg *config.TGConfig) {
	if cfg == nil {
		return
	}

	// 兜底：若配置里仍是 Teldrive 内置的 web.telegram.org 共享凭据（2496），
	// 则替换为 TGPan 内置的应用凭据。
	//
	// 【2026-10 修正】这里原本是 `||`，只要 app-hash 命中了旧常量就强行改写
	// app-id。后果是：用户即使已经配置好了自己申请的应用凭据、且恰好沿用
	// 了同一个 hash，也会被无声改写成 27335138 —— 而该账号早已被 Telegram
	// 判定为 API_ID_INVALID，表现为「永远连不上 / 收不到验证码」，
	// 且日志里看不出任何线索。
	//
	// 改为 `&&`：只有 app-id 与 app-hash **双双**落在内置默认值上时才兜底。
	// 只要用户动过其中任意一项，就完全尊重用户配置，不再插手。
	if cfg.AppId == 2496 && cfg.AppHash == "8da85b0d5bfe62527e5b244c209159c3" {
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
	ApplyEnvOverrides(config)

	var dialer dcs.DialFunc = proxy.Direct.DialContext
	if config.Proxy != "" {
		d, err := utils.Proxy.GetDial(config.Proxy)
		if err != nil {
			return nil, errors.Wrap(err, "get dialer")
		}
		dialer = d.DialContext
	}

	// 【2026-10 关键修复】给拨号器套一层「重试 + 预检」。
	//
	// 现象：登录恒失败，前端收到 `export: waitSession: connection dead`，
	//       但同一台机器上用官方 gotd 库单独拨号，四条传输协议全部握手成功。
	//
	// 根因：gotd 的 dcs.connect() 会把同一个 DC 的**所有候选地址
	//       （IPv4 + IPv6）并发拨号**，谁先连上算谁。当代理链路对其中一部分
	//       地址（尤其是 IPv6 的 2001:67c:4e8:f002::a）响应慢或直接不回时，
	//       dial 会以 timeout 收场。gotd 的连接池按「池内任意一条连接死亡即
	//       判定会话不可用」处理，于是 waitSession 立刻返回 ErrConnDead，
	//       整个登录流程在**尚未发出任何请求**的情况下就失败了。
	//
	//       池越大越糟：pool-size=8 时有 8 条连接同时在拨，
	//       只要命中一次坏地址，会话就被判死。
	//
	// 修法：在拨号这一层做两件事 ——
	//   (1) 单次拨号套上重试：失败立刻换一次重试，把「偶发超时」抹平；
	//   (2) 过滤掉 IPv6 目标：代理链路对 IPv6 支持普遍不稳定，
	//       而 TG 的 IPv4 节点完全够用。这一条能消掉绝大部分失败。
	//       可通过 TELDRIVE_TG_ALLOW_IPV6=1 关闭（回到原行为）。
	allowIPv6 := strings.TrimSpace(os.Getenv("TELDRIVE_TG_ALLOW_IPV6")) == "1"
	dialer = wrapDialWithRetry(dialer, allowIPv6)

	var logger *zap.Logger
	if config.EnableLogging {
		logger = logging.Component("TG")
	}

	// 【2026-10 修复】显式指定传输协议。
	//
	// 原代码只传了 Dial，没传 Protocol，gotd 会默认成 transport.Intermediate。
	// Intermediate 是明文 MTProto（无额外包头），在部分网络路径上
	// 会被中间设备干扰得更厉害。改为可通过环境变量切换，
	// 默认用 Abridged —— 报文最小、握手最快，最不容易在慢链路上超时。
	proto := transport.Intermediate
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TELDRIVE_TG_PROTOCOL"))) {
	case "abridged", "":
		proto = transport.Abridged
	case "intermediate":
		proto = transport.Intermediate
	case "padded":
		proto = transport.PaddedIntermediate
	case "full":
		proto = transport.Full
	}

	// 【2026-10 修复】连接阶段的时序参数，全部可通过环境变量微调。
	//
	// 这些值在弱网 / 需要翻墙的场景下差异极大：默认值是按「服务器能直连 TG」
	// 假设的，一旦真实链路要经过代理，10 秒的拨号超时就会把「慢但通」的
	// 连接误判为死连接。这里放宽的同时保留环境变量，便于现场调参。
	dialTimeout := 30 * time.Second
	if v := strings.TrimSpace(os.Getenv("TELDRIVE_TG_DIAL_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			dialTimeout = d
		}
	}
	retryInterval := 2 * time.Second
	if v := strings.TrimSpace(os.Getenv("TELDRIVE_TG_RETRY_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			retryInterval = d
		}
	}

	// 【2026-10 修复，最后一块拼图】DC 迁移超时。
	//
	// gotd 在登录时若发现账号主 DC 与当前 DC 不同，会先做一次「迁移」：
	// 断开当前连接、连到目标 DC、重建会话。这一步有独立超时
	// （telegram.Options.MigrationTimeout），**默认只有 15 秒**
	// （见 gotd/telegram/options.go:141）。
	//
	// 而实测本环境通过代理连 TG 需要 10-19 秒：
	//     DC2 149.154.167.51   1.3s
	//     DC1 149.154.175.50   3.1s
	//     DC5 91.108.56.130   11.3s
	//     实际拨号+握手       18.6s   ← 超过 15 秒
	//
	// 于是迁移必然超时，gotd 的会话被判死，登录直接以
	// "migrate to dc: context deadline exceeded" 收场 ——
	// 在上层被包装成用户看到的那句 "export: waitSession: connection dead"。
	//
	// 放宽到 90 秒：迁移本身要断+连+重建，比首次连接更费时，
	// 必须留足余量。可用 TELDRIVE_TG_MIGRATION_TIMEOUT 微调。
	migrationTimeout := 90 * time.Second
	if v := strings.TrimSpace(os.Getenv("TELDRIVE_TG_MIGRATION_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			migrationTimeout = d
		}
	}

	opts := telegram.Options{
		Resolver: dcs.Plain(dcs.PlainOptions{
			Dial:   dialer,
			Protocol: proto,
			// 明确不优先 IPv6：与上面的拨号过滤形成双保险。
			PreferIPv6: false,
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
		RetryInterval:  retryInterval,
		MaxRetries:     20,
		// 【2026-10 修复】拨号超时从 10s 放宽到 30s。
		//
		// 实测（盒子上直连 TG DC，走代理）：
		//   DC2 149.154.167.51   1.3s
		//   DC1 149.154.175.50   3.1s
		//   DC5 91.108.56.130   11.3s   ← 超过原来的 10s
		//
		// 代理链路对个别 DC 的握手要 10 秒以上。原来的 DialTimeout=10s
		// 会让这些连接恰好卡在超时线上，gotd 随即判定会话死亡，
		// 前端只能看到 "connection dead"，而实际链路是通的 —— 只是慢。
		// 30s 给足余量，且不会让真正不通的地址拖太久
		// （gotd 是并发拨多个候选，最快的那个赢）。
		DialTimeout: dialTimeout,
		// 迁移超时也必须放宽，原因见上面 migrationTimeout 的注释。
		MigrationTimeout: migrationTimeout,
		Middlewares:      middlewares,
		UpdateHandler:    handler,
		Logger:           logger,
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

// ---------------------------------------------------------------------------
//  拨号加固：重试 + IPv6 过滤
// ---------------------------------------------------------------------------

// wrapDialWithRetry 把一个普通拨号器包装成「带重试、且默认屏蔽 IPv6」的版本。
//
// 为什么需要：
//
//	gotd 在建连阶段会并发拨同一 DC 的多个地址。代理链路对其中一部分地址
//	（典型是 IPv6）响应慢，一次 timeout 就会让整条会话被判死 ——
//	表现是登录直接返回 "connection dead"，而实际上 IPv4 那几条是通的。
//
//	在拨号这一层重试一次的成本极低（失败的分支本来就白等），
//	收益是把偶发超时彻底抹平。
//
// allowIPv6 为 false 时，遇到 IPv6 字面量地址直接返回错误，
// 让 gotd 立即改用同一 DC 的 IPv4 候选，而不是干等它超时。
func wrapDialWithRetry(inner dcs.DialFunc, allowIPv6 bool) dcs.DialFunc {
	const attempts = 3
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !allowIPv6 && isIPv6Addr(addr) {
			// 返回一个立刻失败的错误，促使 gotd 换用 IPv4 候选。
			return nil, errors.New("ipv6 dial disabled by TGPan (set TELDRIVE_TG_ALLOW_IPV6=1 to enable)")
		}

		var lastErr error
		for i := 0; i < attempts; i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			conn, err := inner(ctx, network, addr)
			if err == nil {
				return conn, nil
			}
			lastErr = err
			// 指数退避，但上限很短 —— 拨号失败通常要立刻换路，
			// 而不是原地傻等。
			wait := time.Duration(200*(i+1)) * time.Millisecond
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, errors.Wrapf(lastErr, "dial %s failed after %d attempts", addr, attempts)
	}
}

// isIPv6Addr 判断 "host:port" 形式的地址是否指向 IPv6。
//
// 直接看 host 部分有没有冒号：IPv6 字面量必然带冒号
// （net.JoinHostPort 生成时还会加方括号），IPv4 与域名都不会。
func isIPv6Addr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// 解析不出来就保守放行，交给下游处理。
		return false
	}
	if strings.HasPrefix(host, "[") {
		return true
	}
	return strings.Contains(host, ":")
}
