package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/maps"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/tgdrive/teldrive/internal/duration"
)

var (
	matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
	matchAllCap   = regexp.MustCompile("([a-z0-9])([A-Z])")
)

func toKebabCase(str string) string {
	snake := matchFirstCap.ReplaceAllString(str, "${1}-${2}")
	snake = matchAllCap.ReplaceAllString(snake, "${1}-${2}")
	return strings.ToLower(snake)
}

func getKey(f reflect.StructField) string {
	if t := f.Tag.Get("koanf"); t != "" {
		return t
	}
	return toKebabCase(f.Name)
}

type EventConfig struct {
	PollInterval     time.Duration `default:"10s" description:"Event polling interval for single-instance mode"`
	DBWorkers        int           `default:"10" description:"Number of DB worker goroutines for event persistence"`
	DBBufferSize     int           `default:"1000" description:"Size of DB worker queue buffer"`
	DeduplicationTTL time.Duration `default:"5s" description:"Event deduplication time-to-live"`
}

// GateConfig 控制「TGPan 自有登录」（俗称闸门 / gate）。
//
// 设计背景：TG 的扫码 / 验证码登录在很多场景下不可用（手机无法扫自己的码、
// 某些客户端收不到验证码）。因此把「TG 配对」与「日常进入」彻底解耦：
//
//	TG 配对  —— 只做一次，成功后主凭证落盘到 DataFile。
//	日常进入 —— 用 TGPan 自己的管理密码（或内网免密）。
//
// DataFile 存两样东西：管理密码哈希 + TG 主凭证。
// 放在 /data 目录下，容器重启 / 重建都不会丢。
type GateConfig struct {
	// DataFile 闸门数据文件路径（管理密码哈希 + TG 主凭证）
	DataFile string `default:"/data/tgpan-gate.json" description:"Gate data file: admin password hash and TG master credentials"`

	// RequireLoginHosts 需要登录的 Host 白名单。
	// 约定：凡是 Request.Host（去掉端口）命中此列表的，必须输入管理密码；
	// 其余 Host（内网地址、localhost、飞牛 OS 入口等）一律免密放行。
	//
	// 注意这里是「反向白名单」：列出来的是「要登录的」，不是「免登录的」。
	// 目的是无需事先知道内网 IP 是多少，默认放行内网，只锁死对外域名。
	RequireLoginHosts []string `default:"pan.2016.de5.net" description:"Hosts that REQUIRE the admin password (usually the public domain); all other hosts bypass login"`

	// SessionTTL 管理密码登录后颁发的通行证有效期
	SessionTTL time.Duration `default:"30d" description:"Gate session validity duration"`

	// Disable 彻底关闭闸门（回到原版行为：只有 TG 扫码）
	Disable bool `default:"false" description:"Disable the gate entirely and fall back to Telegram-only login"`
}

type ServerCmdConfig struct {
	Server   ServerConfig
	Log      LoggingConfig
	JWT      JWTConfig
	DB       DBConfig
	TG       TGConfig
	CronJobs CronJobConfig
	Cache    CacheConfig
	Redis    RedisConfig
	Events   EventConfig
	Gate     GateConfig
}

type CheckCmdConfig struct {
	Log          LoggingConfig `skipPflag:"true"`
	DB           DBConfig      `skipPflag:"true"`
	TG           TGConfig      `skipPflag:"true"`
	ExportFile   string        `default:"results.json" description:"Path for exported JSON file"`
	DryRun       bool          `default:"false" description:"Simulate check/clean process without making changes"`
	User         string        `default:"" description:"Telegram username to check (prompts if not specified)"`
	Concurrent   int           `default:"4" description:"Number of concurrent channel processing"`
	CleanUploads bool          `default:"false" description:"Clean incomplete uploads"`
	CleanPending bool          `default:"false" description:"Clean files with pending_deletion status"`
}

type ServerConfig struct {
	Port             int           `default:"8080" description:"HTTP port for the server to listen on"`
	GracefulShutdown time.Duration `default:"10s" description:"Grace period for server shutdown"`
	EnablePprof      bool          `default:"false" description:"Enable pprof debugging endpoints"`
	ReadTimeout      time.Duration `default:"1h" description:"Maximum duration for reading entire request"`
	WriteTimeout     time.Duration `default:"1h" description:"Maximum duration for writing response"`
}

type CacheConfig struct {
	MaxSize int `default:"10485760" description:"Maximum cache size in bytes (used for memory cache)"`
}

type RedisConfig struct {
	Addr            string        `default:"" description:"Redis server address (empty to disable Redis)"`
	Password        string        `default:"" description:"Redis server password"`
	PoolSize        int           `default:"10" description:"Redis connection pool size"`
	MinIdleConns    int           `default:"5" description:"Redis minimum idle connections"`
	MaxIdleConns    int           `default:"10" description:"Redis maximum idle connections"`
	ConnMaxIdleTime time.Duration `default:"5m" description:"Redis connection maximum idle time"`
	ConnMaxLifetime time.Duration `default:"1h" description:"Redis connection maximum lifetime"`
}

// HTTPLoggingConfig holds HTTP request logging configuration
type HTTPLoggingConfig struct {
	Enabled            bool     `default:"true" description:"Enable HTTP request logging"`
	LogQueries         bool     `default:"false" description:"Log full query strings (use with caution)"`
	SanitizeQueries    bool     `default:"true" description:"Remove sensitive params from query preview"`
	MaxQueryLength     int      `default:"100" description:"Maximum length of query preview"`
	LogUserAgent       bool     `default:"true" description:"Log user agent (truncated)"`
	LogRequestBodySize bool     `default:"true" description:"Log request Content-Length"`
	LogResponseSize    bool     `default:"true" description:"Log response bytes written"`
	SkipPaths          []string `default:"/health,/metrics" description:"Paths to skip from logging"`
}

// DBLoggingConfig holds database query logging configuration
type DBLoggingConfig struct {
	Level                string        `default:"error" description:"Database logging level (silent, error, warn, info, debug)"`
	SlowThreshold        time.Duration `default:"1s" description:"Log queries slower than this threshold"`
	IgnoreRecordNotFound bool          `default:"true" description:"Don't log 'record not found' errors"`
	LogSQL               bool          `default:"true" description:"LogSQL"`
}

// TGLoggingConfig holds Telegram client logging configuration
type TGLoggingConfig struct {
	Enabled bool   `default:"false" description:"Enable Telegram client internal logging"`
	Level   string `default:"warn" description:"Telegram client logging level (debug, info, warn, error)"`
}

type LoggingConfig struct {
	Level      string `default:"info" description:"Global logging level (debug, info, warn, error)"`
	TimeFormat string `default:"2006-01-02 15:04:05" description:"Log time format"`
	File       string `default:"" description:"Log file path, if empty logs to stdout only"`
	HTTP       HTTPLoggingConfig
	DB         DBLoggingConfig
	TG         TGLoggingConfig
}

type JWTConfig struct {
	Secret       string        `validate:"required" default:"" description:"JWT signing secret key"`
	SessionTime  time.Duration `default:"30d" description:"JWT token validity duration"`
	AllowedUsers []string      `default:"" description:"List of allowed usernames"`
}

type DBPool struct {
	Enable             bool          `default:"true" description:"Enable connection pooling"`
	MaxOpenConnections int           `default:"25" description:"Maximum number of open connections"`
	MaxIdleConnections int           `default:"25" description:"Maximum number of idle connections"`
	MaxLifetime        time.Duration `default:"10m" description:"Maximum connection lifetime"`
}
type DBConfig struct {
	DataSource  string `validate:"required" default:"" description:"Database connection string"`
	PrepareStmt bool   `default:"true" description:"Use prepared statements"`
	Pool        DBPool
}

type CronJobConfig struct {
	Enable               bool          `default:"true" description:"Enable scheduled background jobs"`
	LockerInstance       string        `default:"cron-locker" description:"Distributed unique cron locker name"`
	CleanFilesInterval   time.Duration `default:"1h" description:"Interval for cleaning expired files"`
	CleanUploadsInterval time.Duration `default:"12h" description:"Interval for cleaning incomplete uploads"`
	FolderSizeInterval   time.Duration `default:"2h" description:"Interval for updating folder sizes"`
}

type TGStream struct {
	Concurrency  int           `default:"1" description:"Number of concurrent threads for concurrent reader"`
	Buffers      int           `default:"8" description:"Number of stream buffers"`
	ChunkTimeout time.Duration `default:"30s" description:"Chunk download timeout"`
	BotsLimit    int           `default:"0" description:"Maximum number of bots for streaming (0 = use all bots)"`
}

type TGUpload struct {
	EncryptionKey string        `default:"" description:"Encryption key for uploads"`
	Threads       int           `default:"8" description:"Number of upload threads"`
	MaxRetries    int           `default:"10" description:"Maximum upload retry attempts"`
	Retention     time.Duration `default:"7d" description:"Upload retention period"`
}
type TGConfig struct {
	RateLimit         bool          `default:"true" description:"Enable rate limiting for API calls"`
	RateBurst         int           `default:"5" description:"Maximum burst size for rate limiting"`
	Rate              int           `default:"100" description:"Rate limit in requests per minute"`
	Ntp               bool          `default:"false" description:"Use NTP for time synchronization"`
	Proxy             string        `default:"" description:"HTTP/SOCKS5 proxy URL"`
	ReconnectTimeout  time.Duration `default:"5m" description:"Client reconnection timeout"`
	PoolSize          int           `default:"8" description:"Session pool size"`
	EnableLogging     bool          `default:"false" description:"Enable Telegram client logging (deprecated: use logging.tg.enabled instead)"`
	AppId             int           `default:"27335138" description:"Telegram app ID (override with TELDRIVE_TG_APP_ID)"`
	AppHash           string        `default:"2459555ba95421148c682e2dc3031bb6" description:"Telegram app hash (override with TELDRIVE_TG_APP_HASH)"`
	DeviceModel       string        `default:"SM-G9980" description:"Device model"`
	SystemVersion     string        `default:"SDK 34" description:"System version"`
	AppVersion        string        `default:"11.2.0" description:"App version"`
	LangCode          string        `default:"zh" description:"Language code"`
	SystemLangCode    string        `default:"zh-CN" description:"System language code"`
	LangPack          string        `default:"" description:"Language pack"`
	SessionInstance   string        `default:"teldrive" description:"Bot session instance name for multi-instance deployments"`
	AutoChannelCreate bool          `default:"true" description:"Auto Create Channel"`
	ChannelLimit      int64         `default:"500000" description:"Channel message limit before auto channel creation"`
	Uploads           TGUpload
	Stream            TGStream
	// Session storage configuration for Telegram sessions
	Session SessionStorageConfig
}

type BoltSessionConfig struct {
	Path       string        `default:"" description:"Path to BoltDB session file (empty for auto-detect)"`
	Timeout    time.Duration `default:"1s" description:"Timeout for opening BoltDB"`
	NoGrowSync bool          `default:"false" description:"Disable grow sync for performance"`
}

type SessionStorageConfig struct {
	Type string            `default:"postgres" description:"Session storage type: postgres, bolt, memory"`
	Key  string            `default:"session" description:"Key prefix for session storage"`
	Bolt BoltSessionConfig `koanf:"bolt"`
}

type ConfigLoader struct {
	k       *koanf.Koanf
	flagMap map[string]string
	envMap  map[string]string
}

func NewConfigLoader() *ConfigLoader {
	return &ConfigLoader{
		k:       koanf.New("."),
		flagMap: make(map[string]string),
		envMap:  make(map[string]string),
	}
}

// customFlagProvider loads flags from a pflag.FlagSet.
type customFlagProvider struct {
	f           *pflag.FlagSet
	flagMap     map[string]string
	onlyChanged bool
	defaults    bool
}

func (p *customFlagProvider) Read() (map[string]any, error) {
	m := make(map[string]any)
	p.f.VisitAll(func(f *pflag.Flag) {
		if p.defaults && f.Changed {
			return
		}
		if p.onlyChanged && !f.Changed {
			return
		}

		var key string
		if mapped, ok := p.flagMap[f.Name]; ok {
			key = mapped
		} else {
			// Fallback: simple dash replacement if not mapped (should not happen if registered correctly)
			key = strings.ReplaceAll(f.Name, "-", ".")
		}

		// Handle slices
		if sliceVal, ok := f.Value.(pflag.SliceValue); ok {
			m[key] = sliceVal.GetSlice()
		} else {
			m[key] = f.Value.String()
		}
	})
	return maps.Unflatten(m, "."), nil
}

func (p *customFlagProvider) ReadBytes() ([]byte, error) {
	return nil, nil
}

type unflattenProvider struct {
	p     koanf.Provider
	delim string
}

func (p *unflattenProvider) Read() (map[string]any, error) {
	m, err := p.p.Read()
	if err != nil {
		return nil, err
	}
	return maps.Unflatten(m, p.delim), nil
}

func (p *unflattenProvider) ReadBytes() ([]byte, error) {
	return nil, nil
}

func (cl *ConfigLoader) Load(cmd *cobra.Command, cfg any) error {

	cfgFile := cmd.Flags().Lookup("config").Value.String()
	var parser koanf.Parser

	if cfgFile != "" {
		if strings.HasSuffix(cfgFile, ".yaml") || strings.HasSuffix(cfgFile, ".yml") {
			parser = yaml.Parser()
		} else {
			parser = toml.Parser()
		}
	} else {
		parser = toml.Parser()
	}

	// 1. Load defaults from flags
	if err := cl.k.Load(&customFlagProvider{f: cmd.Flags(), flagMap: cl.flagMap, defaults: true}, nil); err != nil {
		return err
	}

	// Load defaults for skipped flags
	cl.loadSkippedDefaults(reflect.TypeOf(cfg), "")

	// 2. Load config file
	if cfgFile != "" {
		if err := cl.k.Load(file.Provider(cfgFile), parser); err != nil {
			return fmt.Errorf("error reading config file: %w", err)
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("error getting home directory: %w", err)
		}
		paths := []string{
			filepath.Join(home, ".teldrive", "config.toml"),
			"config.toml",
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				if err := cl.k.Load(file.Provider(path), toml.Parser()); err != nil {
					return fmt.Errorf("error reading config file: %w", err)
				}
				break
			}
		}
	}

	// 3. Load environment variables
	cl.generateEnvMap(reflect.TypeOf(cfg), "", "")

	if err := cl.k.Load(&unflattenProvider{
		p: env.Provider("TELDRIVE_", ".", func(s string) string {
			key := strings.TrimPrefix(s, "TELDRIVE_")
			if val, ok := cl.envMap[key]; ok {
				return val
			}
			return strings.ReplaceAll(strings.ToLower(key), "_", "-")
		}),
		delim: ".",
	}, nil); err != nil {

		return err
	}

	// 4. Load explicit flags
	if err := cl.k.Load(&customFlagProvider{f: cmd.Flags(), flagMap: cl.flagMap, onlyChanged: true}, nil); err != nil {
		return err
	}

	unmarshalCfg := koanf.UnmarshalConf{
		Tag: "koanf",
		DecoderConfig: &mapstructure.DecoderConfig{
			MatchName: func(mapKey, fieldName string) bool {
				return strings.EqualFold(strings.ReplaceAll(mapKey, "-", ""), strings.ReplaceAll(fieldName, "-", "")) ||
					strings.EqualFold(strings.ReplaceAll(mapKey, "_", ""), strings.ReplaceAll(fieldName, "_", ""))
			},
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToSliceHookFunc(","),
				func(f reflect.Type, t reflect.Type, data any) (any, error) {
					if f.Kind() != reflect.String {
						return data, nil
					}
					if t != reflect.TypeFor[time.Duration]() {
						return data, nil
					}
					return duration.ParseDuration(data.(string))
				},
			),
			Result:           cfg,
			WeaklyTypedInput: true,
		},
	}

	if err := cl.k.UnmarshalWithConf("", cfg, unmarshalCfg); err != nil {
		return err
	}

	return nil
}

func (cl *ConfigLoader) Validate(cfg any) error {
	validate := validator.New()
	return validate.Struct(cfg)
}

func (cl *ConfigLoader) RegisterFlags(flags *pflag.FlagSet, t reflect.Type) {
	flags.StringP("config", "c", "", "Config file path (default $HOME/.teldrive/config.toml)")
	cl.registerStruct(flags, "", t)
}

func (cl *ConfigLoader) generateEnvMap(t reflect.Type, prefix string, envPrefix string) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		koanfTag := getKey(field)

		key := koanfTag
		if prefix != "" {
			key = prefix + "." + koanfTag
		}

		envKey := strings.ToUpper(strings.ReplaceAll(koanfTag, "-", "_"))
		if envPrefix != "" {
			envKey = envPrefix + "_" + envKey
		}

		if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeFor[time.Duration]() {
			cl.generateEnvMap(field.Type, key, envKey)
		} else {
			cl.envMap[envKey] = key
		}
	}
}

func (cl *ConfigLoader) loadSkippedDefaults(t reflect.Type, prefix string) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		koanfTag := getKey(field)

		key := koanfTag
		if prefix != "" {
			key = prefix + "." + koanfTag
		}

		if field.Tag.Get("skipPflag") == "true" {
			cl.registerDefaultsRecursive(field.Type, key)
			continue
		}

		if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeFor[time.Duration]() {
			cl.loadSkippedDefaults(field.Type, key)
		}
	}
}

func (cl *ConfigLoader) registerDefaultsRecursive(t reflect.Type, prefix string) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		koanfTag := getKey(field)

		key := prefix + "." + koanfTag

		if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeFor[time.Duration]() {
			cl.registerDefaultsRecursive(field.Type, key)
			continue
		}

		defaultValue := field.Tag.Get("default")
		if defaultValue != "" {
			var val any = defaultValue
			switch field.Type.Kind() {
			case reflect.Int:
				val, _ = strconv.Atoi(defaultValue)
			case reflect.Int64:
				if field.Type != reflect.TypeFor[time.Duration]() {
					val, _ = strconv.ParseInt(defaultValue, 10, 64)
				}
			case reflect.Bool:
				val, _ = strconv.ParseBool(defaultValue)
			case reflect.Slice:
				if field.Type.Elem().Kind() == reflect.String {
					val = strings.Split(defaultValue, ",")
				}
			}
			cl.k.Set(key, val)
		}
	}
}

func (cl *ConfigLoader) registerStruct(flags *pflag.FlagSet, prefix string, t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		koanfTag := getKey(field)

		key := koanfTag
		if prefix != "" {
			key = prefix + "." + koanfTag
		}

		if field.Tag.Get("skipPflag") == "true" {
			continue
		}

		if field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeFor[time.Duration]() {
			cl.registerStruct(flags, key, field.Type)
			continue
		}

		defaultValue := field.Tag.Get("default")
		description := field.Tag.Get("description")
		name := strings.ReplaceAll(key, ".", "-")
		cl.flagMap[name] = key

		switch field.Type.Kind() {
		case reflect.String:
			flags.String(name, defaultValue, description)
		case reflect.Int:
			val, _ := strconv.Atoi(defaultValue)
			flags.Int(name, val, description)
		case reflect.Int64:
			if field.Type == reflect.TypeFor[time.Duration]() {
				val, _ := duration.ParseDuration(defaultValue)
				d := duration.Duration(val)
				flags.Var(&d, name, description)
			} else {
				val, _ := strconv.ParseInt(defaultValue, 10, 64)
				flags.Int64(name, val, description)
			}
		case reflect.Bool:
			val, _ := strconv.ParseBool(defaultValue)
			flags.Bool(name, val, description)
		case reflect.Slice:
			if field.Type.Elem().Kind() == reflect.String {
				var val []string
				if defaultValue != "" {
					val = strings.Split(defaultValue, ",")
				}
				flags.StringSlice(name, val, description)
			}
		}
	}
}
