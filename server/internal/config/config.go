package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config holds all application configuration
type Config struct {
	// Environment
	Env      string `toml:"env"`
	LogLevel string `toml:"log_level"`
	Port     int    `toml:"port"`

	// Database
	DatabaseURL string `toml:"database_url"`
	DBDriver    string `toml:"db_driver"`   // sqlite or postgres
	SQLiteKey   string `toml:"sqlite_key"`  // SQLCipher encryption key for SQLite
	PGSSLMode   string `toml:"pg_ssl_mode"` // PostgreSQL sslmode override (disable/allow/prefer/require/verify-ca/verify-full)

	// Security
	JWTSecret       string `toml:"jwt_secret"`
	CSRFTokenSecret string `toml:"csrf_token_secret"`
	EncryptionKey   string `toml:"encryption_key"`            // AES-256 key for sensitive data encryption (whiteboard etc.)
	JWTAccessTTL    int    `toml:"jwt_access_ttl_minutes"`    // default 15
	JWTRefreshTTL   int    `toml:"jwt_refresh_ttl_days"`      // default 7
	AdminSessionTTL int    `toml:"admin_session_ttl_minutes"` // default 15 (design doc §8.1)
	BcryptCost      int    `toml:"bcrypt_cost"`               // default 12

	// Embedded dependency management
	EmbeddedDeps  bool   `toml:"embedded_deps"`   // default true: server manages LiveKit/Netease/FFmpeg
	ThirdPartyDir string `toml:"third_party_dir"` // default ./third_party
	ModelsDir     string `toml:"models_dir"`      // default ./models

	// LiveKit
	LiveKitAPIKey      string `toml:"livekit_api_key"`
	LiveKitAPISecret   string `toml:"livekit_api_secret"`
	LiveKitURL         string `toml:"livekit_url"`          // internal URL for server-side connections (bot player, RoomService)
	LiveKitPublicURL   string `toml:"livekit_public_url"`   // public URL returned to frontend clients (defaults to LiveKitURL)
	LiveKitBinaryPath  string `toml:"livekit_binary_path"`  // explicit path to livekit-server executable
	LiveKitAutoStart   bool   `toml:"livekit_auto_start"`   // default true
	LiveKitE2EEEnabled bool   `toml:"livekit_e2ee_enabled"` // default false

	// FFmpeg (used by music bot and TTS)
	FFmpegBinaryPath  string `toml:"ffmpeg_binary_path"`  // explicit path to ffmpeg executable; auto-populated when embedded deps are enabled
	FFprobeBinaryPath string `toml:"ffprobe_binary_path"` // explicit path to ffprobe executable; auto-populated when embedded deps are enabled

	// Music Bot Worker (Node.js LiveKit Agents Worker)
	// 阶段 2 起：Go 服务端可通过此开关将播放逻辑委托给 Node.js Worker。
	// 默认 false：仍使用 Go 端 player_cgo.go 的旧播放逻辑（fallback）。
	// 设为 true 时，startPlayback 优先调用 Worker HTTP API，失败则回退到旧逻辑。
	UseMusicBotWorker      bool   `toml:"use_music_bot_worker"`       // default false
	MusicBotWorkerPort     int    `toml:"music_bot_worker_port"`      // default 5012
	MusicBotWorkerToken    string `toml:"music_bot_worker_token"`     // Go 与 Worker 双向内部 HTTP 调用鉴权 token
	BotUploadMaxFiles      int    `toml:"bot_upload_max_files"`       // default 200
	BotUploadMaxTotalBytes int64  `toml:"bot_upload_max_total_bytes"` // default 5 GiB

	// SFX (entrance/exit sounds)
	SfxMaxPerUser int `toml:"sfx_max_per_user"` // default 20

	// Netease Cloud Music API
	NeteaseAPIEndpoint string `toml:"netease_api_endpoint"`

	// File storage
	LocalDataPath      string `toml:"local_data_path"`       // default ./storage
	CloudFSMaxFileSize int64  `toml:"cloudfs_max_file_size"` // bytes, default 100 MiB
	CloudFSQuotaGB     int    `toml:"cloudfs_quota_gb"`      // default 10
	// CloudFSTrashRetentionDays is how long a soft-deleted cloud file/folder is
	// kept in the trash before the background job purges it for good.
	// 0 or negative falls back to the 30-day default (DES-2026-0912-02 §4.3).
	CloudFSTrashRetentionDays int `toml:"cloudfs_trash_retention_days"` // default 30

	// Cache (ristretto W-TinyLFU, design doc §5B.3 + P2 spec)
	CacheMaxCost     int64 `toml:"cache_max_cost"`     // bytes, default 1 GiB (1073741824)
	CacheNumCounters int64 `toml:"cache_num_counters"` // default 1e7 (~10x expected items)

	// Rate limiting
	RateLimitRequests int `toml:"rate_limit_requests"` // per minute
	RateLimitBurst    int `toml:"rate_limit_burst"`

	// HIBP (Have I Been Pwned) password breach check
	HIBPEnabled bool   `toml:"hibp_enabled"` // default true (C17: aligned with design doc)
	HIBPTimeout int    `toml:"hibp_timeout"` // seconds, default 5
	HIBPAPIURL  string `toml:"hibp_api_url"` // default https://api.pwnedpasswords.com/range/

	// Password history (H10: prevent password reuse)
	PasswordHistoryCount int `toml:"password_history_count"` // default 5

	// Server info
	ServerName      string   `toml:"server_name"`
	PublicAddress   string   `toml:"public_address"`
	MaxUsers        int      `toml:"max_users"`
	AllowRegister   bool     `toml:"allow_register"`
	AdminPort       int      `toml:"admin_port"`
	AdminPortShared bool     `toml:"admin_port_shared"` // fallback: keep admin routes on API port
	AllowedOrigins  []string `toml:"allowed_origins"`
	LiveKitPort     int      `toml:"livekit_port"`
	VPNPort         int      `toml:"vpn_port"`
	DeployMode      string   `toml:"deploy_mode"` // native or docker

	// M2: Cookie Secure attribute override (design doc §3.4).
	// When nil, CookieSecureEnabled() falls back to Env == "production".
	// Set explicitly to force Secure on/off in non-production environments
	// (e.g., when testing over HTTPS with a self-signed cert on LAN).
	CookieSecureOverride *bool `toml:"cookie_secure"`

	// EasyTier (virtual network, replaces Headscale/WireGuard)
	// EasyTier 是去中心化组网工具，服务端仅作为控制平面：通过 HTTP API
	// 向客户端下发网络名称 + 网络密钥，客户端本地启动 easytier-core 子进程加入网络。
	// 服务端不维护对等节点状态，仅在数据库中记录会话和节点元数据。
	EasytierURL    string `toml:"easytier_url"`    // EasyTier Web API 地址（默认 http://127.0.0.1:11210）
	EasytierSecret string `toml:"easytier_secret"` // 部署时生成的网络密钥（10.126.126.0/24 网络共享）
	EasytierPort   int    `toml:"easytier_port"`   // EasyTier 监听端口（默认 5007）

	// 保留 Headscale 字段以向后兼容旧配置文件解析，但服务端不再使用
	HeadscaleURL    string `toml:"headscale_url"`
	HeadscaleAPIKey string `toml:"headscale_api_key"`

	// Owner pre-configuration (H1: env-based owner initialization, skips bootstrap token)
	Owner OwnerConfig `toml:"owner"`
}

// OwnerConfig holds owner pre-configuration for env-based initialization (H1).
// When IsConfigured() returns true and server is not initialized, the server
// auto-creates the owner on startup without the bootstrap token flow.
type OwnerConfig struct {
	Username    string `toml:"username"`
	Password    string `toml:"password"`
	Email       string `toml:"email"`
	DisplayName string `toml:"display_name"`
	ServerName  string `toml:"server_name"`
}

// IsConfigured returns true when the required owner fields are set.
// Username is optional: the login identity is the email (L4), and a system-generated
// username is derived from it. DisplayName falls back to Username at bootstrap.
func (o OwnerConfig) IsConfigured() bool {
	return o.Password != "" && o.Email != "" && (o.Username != "" || o.DisplayName != "")
}

// CookieSecureEnabled reports whether the auth cookie Secure attribute should
// be set. M2 (design doc §3.4): when CookieSecureOverride is non-nil, return
// the explicit value; otherwise fall back to Env == "production".
func (cfg *Config) CookieSecureEnabled() bool {
	if cfg.CookieSecureOverride != nil {
		return *cfg.CookieSecureOverride
	}
	return cfg.Env == "production"
}

// LiveKitURLForClient returns the public LiveKit URL to send to frontend clients.
// Falls back to LiveKitURL when LiveKitPublicURL is not configured.
func (cfg *Config) LiveKitURLForClient() string {
	if cfg.LiveKitPublicURL != "" {
		return cfg.LiveKitPublicURL
	}
	return cfg.LiveKitURL
}

// DefaultConfig returns a config with sensible defaults
func DefaultConfig() *Config {
	cfg := &Config{
		Env:                       "development",
		LogLevel:                  "info",
		Port:                      8080,
		DatabaseURL:               "./storage/ridgericetalk.db",
		DBDriver:                  "sqlite",
		JWTAccessTTL:              15,
		JWTRefreshTTL:             7,
		AdminSessionTTL:           15,
		BcryptCost:                12,
		LocalDataPath:             "./storage",
		CloudFSMaxFileSize:        100 << 20,
		CloudFSQuotaGB:            10,
		CloudFSTrashRetentionDays: 30,
		CacheMaxCost:              1073741824, // 1 GiB (design doc §5B.3)
		CacheNumCounters:          10000000,   // 1e7 (~10x expected items)
		RateLimitRequests:         300,
		RateLimitBurst:            100,
		ServerName:                "RidgeRiceTalk",
		AllowRegister:             true,
		MaxUsers:                  50,
		AdminPort:                 9090,
		AdminPortShared:           false,
		LiveKitURL:                "ws://localhost:7880",
		LiveKitAPIKey:             "devkey",
		LiveKitAPISecret:          "devsecret",
		LiveKitAutoStart:          true,
		LiveKitPort:               7880,
		VPNPort:                   41641,
		DeployMode:                "native",
		HIBPTimeout:               5,
		HIBPAPIURL:                "https://api.pwnedpasswords.com/range/",
		HIBPEnabled:               true, // C17: default enabled per design doc
		// ISSUE-032: NeteaseCloudMusicApi is documented to run on port 3300.
		NeteaseAPIEndpoint:   "http://localhost:3300",
		PasswordHistoryCount: 5,
		// Embedded dependency management defaults
		EmbeddedDeps:  true,
		ThirdPartyDir: "./third_party",
		ModelsDir:     "./models",
		// EasyTier 默认配置（DES-2026-0731-02）
		EasytierURL:  "http://127.0.0.1:11210",
		EasytierPort: 5007,
	}
	cfg.BotUploadMaxFiles = 200
	cfg.BotUploadMaxTotalBytes = 5 << 30
	cfg.SfxMaxPerUser = 20
	return cfg
}

// Load loads configuration from file and environment variables
func Load() (*Config, error) {
	cfg := DefaultConfig()

	// Try to load from config files
	configPaths := []string{"server-config.toml", "server-config.json", ".env"}
	for _, path := range configPaths {
		if _, err := os.Stat(path); err == nil {
			if strings.HasSuffix(path, ".toml") {
				data, err := os.ReadFile(path)
				if err != nil {
					return nil, fmt.Errorf("read config file: %w", err)
				}
				if err := toml.Unmarshal(data, cfg); err != nil {
					return nil, fmt.Errorf("parse config file: %w", err)
				}
			} else if path == ".env" {
				if err := loadEnvFile(path); err != nil {
					return nil, fmt.Errorf("load .env file: %w", err)
				}
			}
			break
		}
	}

	// Override with environment variables
	if v := os.Getenv("RRT_ENV"); v != "" {
		cfg.Env = v
	}
	if v := os.Getenv("RRT_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("RRT_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("RRT_DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	if v := os.Getenv("RRT_DB_DRIVER"); v != "" {
		cfg.DBDriver = v
	}
	if v := os.Getenv("RRT_SQLITE_KEY"); v != "" {
		cfg.SQLiteKey = v
	}
	if v := os.Getenv("RRT_PG_SSL_MODE"); v != "" {
		cfg.PGSSLMode = v
		// Synchronize the database package's expected environment variable.
		os.Setenv("RRT_PG_SSL_MODE", v)
	} else if cfg.PGSSLMode != "" {
		os.Setenv("RRT_PG_SSL_MODE", cfg.PGSSLMode)
	}
	if v := os.Getenv("RRT_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := os.Getenv("RRT_CSRF_TOKEN_SECRET"); v != "" {
		cfg.CSRFTokenSecret = v
	}
	if v := os.Getenv("RRT_ENCRYPTION_KEY"); v != "" {
		cfg.EncryptionKey = v
	}
	if v := os.Getenv("RRT_JWT_ACCESS_TTL"); v != "" {
		if t, err := strconv.Atoi(v); err == nil {
			cfg.JWTAccessTTL = t
		}
	}
	if v := os.Getenv("RRT_JWT_REFRESH_TTL"); v != "" {
		if t, err := strconv.Atoi(v); err == nil {
			cfg.JWTRefreshTTL = t
		}
	}
	if v := os.Getenv("RRT_ADMIN_SESSION_TTL"); v != "" {
		if t, err := strconv.Atoi(v); err == nil {
			cfg.AdminSessionTTL = t
		}
	}
	if v := os.Getenv("RRT_LIVEKIT_API_KEY"); v != "" {
		cfg.LiveKitAPIKey = v
	}
	if v := os.Getenv("RRT_LIVEKIT_API_SECRET"); v != "" {
		cfg.LiveKitAPISecret = v
	}
	if v := os.Getenv("RRT_LIVEKIT_URL"); v != "" {
		cfg.LiveKitURL = v
	}
	if v := os.Getenv("RRT_LIVEKIT_PUBLIC_URL"); v != "" {
		cfg.LiveKitPublicURL = v
	}
	if v := os.Getenv("RRT_LIVEKIT_E2EE_ENABLED"); v != "" {
		cfg.LiveKitE2EEEnabled = v == "true" || v == "1"
	}
	// Music Bot Worker
	if v := os.Getenv("RRT_USE_MUSIC_BOT_WORKER"); v != "" {
		cfg.UseMusicBotWorker = v == "true" || v == "1"
	}
	if v := os.Getenv("RRT_MUSIC_BOT_WORKER_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.MusicBotWorkerPort = p
		}
	}
	if v := os.Getenv("RRT_MUSIC_BOT_WORKER_TOKEN"); v != "" {
		cfg.MusicBotWorkerToken = v
	}
	if v := os.Getenv("RRT_BOT_UPLOAD_MAX_FILES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.BotUploadMaxFiles = n
		}
	}
	if v := os.Getenv("RRT_BOT_UPLOAD_MAX_TOTAL_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.BotUploadMaxTotalBytes = n
		}
	}
	if v := os.Getenv("RRT_SFX_MAX_PER_USER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SfxMaxPerUser = n
		}
	}
	if v := os.Getenv("RRT_LOCAL_DATA_PATH"); v != "" {
		cfg.LocalDataPath = v
	}
	if v := os.Getenv("RRT_CLOUDFS_MAX_FILE_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.CloudFSMaxFileSize = n
		}
	}
	if v := os.Getenv("RRT_CLOUDFS_QUOTA_GB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.CloudFSQuotaGB = n
		}
	}
	if v := os.Getenv("RRT_CLOUDFS_TRASH_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.CloudFSTrashRetentionDays = n
		}
	}
	// Cache (ristretto W-TinyLFU, design doc §5B.3 + P2 spec)
	if v := os.Getenv("RRT_CACHE_MAX_COST"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.CacheMaxCost = n
		}
	}
	if v := os.Getenv("RRT_CACHE_NUM_COUNTERS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.CacheNumCounters = n
		}
	}
	if v := os.Getenv("RRT_LIVEKIT_BINARY_PATH"); v != "" {
		cfg.LiveKitBinaryPath = v
	}
	if v := os.Getenv("RRT_FFMPEG_BINARY_PATH"); v != "" {
		cfg.FFmpegBinaryPath = v
	}
	if v := os.Getenv("RRT_FFPROBE_BINARY_PATH"); v != "" {
		cfg.FFprobeBinaryPath = v
	}
	if v := os.Getenv("RRT_LIVEKIT_AUTO_START"); v != "" {
		cfg.LiveKitAutoStart = v == "true" || v == "1"
	}
	if v := os.Getenv("RRT_LIVEKIT_AUTOSTART"); v != "" {
		cfg.LiveKitAutoStart = v == "true" || v == "1"
	}
	if v := os.Getenv("RRT_LIVEKIT_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.LiveKitPort = p
		}
	}
	if v := os.Getenv("RRT_VPN_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.VPNPort = p
		}
	}
	if v := os.Getenv("RRT_DEPLOY_MODE"); v != "" {
		cfg.DeployMode = v
	}
	if v := os.Getenv("RRT_RATE_LIMIT_REQUESTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RateLimitRequests = n
		}
	}
	if v := os.Getenv("RRT_RATE_LIMIT_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RateLimitBurst = n
		}
	}
	if v := os.Getenv("RRT_BCRYPT_COST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.BcryptCost = n
		}
	}
	if v := os.Getenv("RRT_HIBP_ENABLED"); v != "" {
		cfg.HIBPEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("RRT_HIBP_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.HIBPTimeout = n
		}
	}
	if v := os.Getenv("RRT_HIBP_API_URL"); v != "" {
		cfg.HIBPAPIURL = v
	}
	if v := os.Getenv("RRT_SERVER_NAME"); v != "" {
		cfg.ServerName = v
	}
	if v := os.Getenv("RRT_PUBLIC_ADDRESS"); v != "" {
		cfg.PublicAddress = v
	}
	if v := os.Getenv("RRT_MAX_USERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxUsers = n
		}
	}
	if v := os.Getenv("RRT_ALLOW_REGISTER"); v != "" {
		cfg.AllowRegister = v == "true" || v == "1"
	}
	if v := os.Getenv("RRT_ADMIN_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.AdminPort = p
		}
	}
	if v := os.Getenv("RRT_ADMIN_PORT_SHARED"); v != "" {
		cfg.AdminPortShared = v == "true" || v == "1"
	}
	// M2: Cookie Secure override (design doc §3.4).
	if v := os.Getenv("RRT_COOKIE_SECURE"); v != "" {
		b := v == "true" || v == "1"
		cfg.CookieSecureOverride = &b
	}
	if v := os.Getenv("RRT_CORS_ORIGINS"); v != "" {
		cfg.AllowedOrigins = strings.Split(v, ",")
		for i := range cfg.AllowedOrigins {
			cfg.AllowedOrigins[i] = strings.TrimSpace(cfg.AllowedOrigins[i])
		}
	}
	if v := os.Getenv("RRT_ALLOWED_ORIGINS"); v != "" {
		cfg.AllowedOrigins = strings.Split(v, ",")
		for i := range cfg.AllowedOrigins {
			cfg.AllowedOrigins[i] = strings.TrimSpace(cfg.AllowedOrigins[i])
		}
	}
	if v := os.Getenv("RRT_NETEASE_API_ENDPOINT"); v != "" {
		cfg.NeteaseAPIEndpoint = v
	}
	if v := os.Getenv("RRT_HEADSCALE_URL"); v != "" {
		cfg.HeadscaleURL = v
	}
	if v := os.Getenv("RRT_HEADSCALE_API_KEY"); v != "" {
		cfg.HeadscaleAPIKey = v
	}
	// EasyTier 环境变量（DES-2026-0731-02）
	if v := os.Getenv("RRT_EASYTIER_URL"); v != "" {
		cfg.EasytierURL = v
	}
	if v := os.Getenv("RRT_EASYTIER_SECRET"); v != "" {
		cfg.EasytierSecret = v
	}
	if v := os.Getenv("RRT_EASYTIER_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.EasytierPort = p
		}
	}

	// Embedded dependency management
	if v := os.Getenv("RRT_EMBEDDED_DEPS"); v != "" {
		cfg.EmbeddedDeps = v == "true" || v == "1"
	}
	if v := os.Getenv("RRT_THIRD_PARTY_DIR"); v != "" {
		cfg.ThirdPartyDir = v
	}
	if v := os.Getenv("RRT_MODELS_DIR"); v != "" {
		cfg.ModelsDir = v
	}

	// Owner pre-configuration (H1: env-based owner initialization)
	if v := os.Getenv("RRT_OWNER_USERNAME"); v != "" {
		cfg.Owner.Username = v
	}
	if v := os.Getenv("RRT_OWNER_PASSWORD"); v != "" {
		cfg.Owner.Password = v
	}
	if v := os.Getenv("RRT_OWNER_EMAIL"); v != "" {
		cfg.Owner.Email = v
	}
	if v := os.Getenv("RRT_OWNER_DISPLAY_NAME"); v != "" {
		cfg.Owner.DisplayName = v
	}
	if v := os.Getenv("RRT_OWNER_SERVER_NAME"); v != "" {
		cfg.Owner.ServerName = v
	}

	// 自动将 PublicAddress 加入 AllowedOrigins（CHANGE.md 宣称但缺失的逻辑），
	// 让运维只设 RRT_PUBLIC_ADDRESS 即可让 Web/Admin 端自动放行。
	appendAllowedOriginsFromPublicAddress(cfg)

	// Validate required fields
	if cfg.Env == "production" {
		// 生产环境：从 secrets.json 加载或自动生成并持久化密钥。
		// 环境变量已提供的字段优先保留，仅填充为空的字段。
		if err := loadOrGenerateProductionSecrets(cfg); err != nil {
			return nil, err
		}
	} else {
		// 开发环境：保留原自动生成内存密钥的逻辑（不持久化）
		if cfg.JWTSecret == "" {
			// Generate a random development secret instead of hardcoded value
			b := make([]byte, 32)
			if _, err := rand.Read(b); err == nil {
				cfg.JWTSecret = hex.EncodeToString(b)
			} else {
				return nil, fmt.Errorf("failed to generate JWT secret: %w", err)
			}
		}

		if cfg.CSRFTokenSecret == "" {
			// Generate a random development secret
			b := make([]byte, 32)
			if _, err := rand.Read(b); err == nil {
				cfg.CSRFTokenSecret = hex.EncodeToString(b)
			} else {
				return nil, fmt.Errorf("failed to generate CSRF secret: %w", err)
			}
		}

		if cfg.EncryptionKey == "" {
			// Generate a random development encryption key (AES-256 requires 32 bytes)
			b := make([]byte, 32)
			if _, err := rand.Read(b); err == nil {
				cfg.EncryptionKey = hex.EncodeToString(b)
			} else {
				return nil, fmt.Errorf("failed to generate encryption key: %w", err)
			}
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// IsAllowedOrigin checks whether an Origin header value is allowed.
// It normalizes default ports (80 for http, 443 for https) so that
// "https://example.com" matches "https://example.com:443".
func IsAllowedOrigin(origin string, allowedOrigins []string) bool {
	normalizedOrigin := normalizeOrigin(origin)
	for _, o := range allowedOrigins {
		if normalizeOrigin(o) == normalizedOrigin {
			return true
		}
	}
	return false
}

func normalizeOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return origin
	}
	u, err := url.Parse(origin)
	if err != nil {
		return origin
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		case "ws":
			port = "80"
		case "wss":
			port = "443"
		}
	}
	return fmt.Sprintf("%s://%s:%s", u.Scheme, host, port)
}

// appendAllowedOriginsFromPublicAddress 将 PublicAddress 与 localhost 兜底地址
// 去重追加到 AllowedOrigins。显式 RRT_CORS_ORIGINS 已在前面解析，
// 此处仅补充而非覆盖。
func appendAllowedOriginsFromPublicAddress(cfg *Config) {
	seen := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		seen[o] = struct{}{}
	}
	addIfNew := func(origin string) {
		if origin == "" {
			return
		}
		if _, ok := seen[origin]; ok {
			return
		}
		seen[origin] = struct{}{}
		cfg.AllowedOrigins = append(cfg.AllowedOrigins, origin)
	}
	// 1. PublicAddress 自动放行
	if cfg.PublicAddress != "" {
		// Normalize: strip http:// or https:// prefix then strip port.
		// This handles all forms of PublicAddress:
		//   "http://<REDACTED-OLD-SERVER-IP>:50100"  → "<REDACTED-OLD-SERVER-IP>"
		//   "<REDACTED-OLD-SERVER-IP>:50100"         → "<REDACTED-OLD-SERVER-IP>"
		//   "http://<REDACTED-OLD-SERVER-IP>"        → "<REDACTED-OLD-SERVER-IP>"
		//   "<REDACTED-OLD-SERVER-IP>"              → "<REDACTED-OLD-SERVER-IP>"
		cleanAddr := cfg.PublicAddress
		cleanAddr = strings.TrimPrefix(cleanAddr, "http://")
		cleanAddr = strings.TrimPrefix(cleanAddr, "https://")
		if host, _, err := net.SplitHostPort(cleanAddr); err == nil {
			cleanAddr = host
		}
		// 1a. Add the public address itself as-is (browser origin match)
		addIfNew(cfg.PublicAddress)
		// 1b. Auto-add port-qualified origins for PublicAddress.
		// Admin panel and voice client may run on different ports, and
		// browsers send the Origin header with the port. Without this
		// the admin panel (port 50103) is blocked by CORS middleware.
		// Uses cleanAddr (host-only, no scheme, no port) to build correct origins.
		if cfg.Port > 0 {
			addIfNew(fmt.Sprintf("http://%s:%d", cleanAddr, cfg.Port))
		}
		if !cfg.AdminPortShared && cfg.AdminPort > 0 {
			addIfNew(fmt.Sprintf("http://%s:%d", cleanAddr, cfg.AdminPort))
		}
	}
	// 2. localhost 兜底（Web/Admin 端本机访问）
	addIfNew(fmt.Sprintf("http://localhost:%d", cfg.Port))
	if !cfg.AdminPortShared && cfg.AdminPort > 0 {
		addIfNew(fmt.Sprintf("http://localhost:%d", cfg.AdminPort))
	}
}

// loadOrGenerateProductionSecrets 在生产环境下从 secrets.json 加载或生成并持久化密钥。
// 仅填充 cfg 中为空的字段，已通过环境变量提供的字段保留原值；若所有核心密钥均已
// 通过环境变量提供，则直接返回，不读取也不写入 secrets.json。secrets.json 路径
// 为 <LocalDataPath>/secrets.json。文件不存在时自动生成新密钥并持久化；文件存在
// 但格式错误或字段不完整时报错退出，提示用户删除或修复文件。
func loadOrGenerateProductionSecrets(cfg *Config) error {
	// 若所有核心密钥已通过环境变量提供，跳过 secrets.json 读取与写入。
	if cfg.JWTSecret != "" && cfg.CSRFTokenSecret != "" && cfg.EncryptionKey != "" &&
		cfg.LiveKitAPIKey != "" && cfg.LiveKitAPISecret != "" {
		return nil
	}

	secretsPath := filepath.Join(cfg.LocalDataPath, "secrets.json")

	// 尝试加载现有 secrets.json
	s, err := LoadSecrets(secretsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 文件不存在：生成新密钥并持久化
			s = GenerateSecrets()
			if err := SaveSecrets(secretsPath, s); err != nil {
				return fmt.Errorf("save generated secrets to %s: %w", secretsPath, err)
			}
			log.Printf("WARN: 自动生成生产密钥并持久化到 %s，请妥善备份", secretsPath)
		} else {
			// 文件存在但 JSON 格式错误或字段不完整：报错退出
			return fmt.Errorf("load secrets file %s: %w；请删除该文件让系统重新生成，或修复其中的字段", secretsPath, err)
		}
	} else {
		log.Printf("INFO: 从 secrets.json 加载密钥: %s", secretsPath)
	}

	// 仅填充为空的字段，保留环境变量已设置的值
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = s.JWTSecret
	}
	if cfg.CSRFTokenSecret == "" {
		cfg.CSRFTokenSecret = s.CSRFTokenSecret
	}
	if cfg.EncryptionKey == "" {
		cfg.EncryptionKey = s.EncryptionKey
	}
	if cfg.LiveKitAPIKey == "" {
		cfg.LiveKitAPIKey = s.LiveKitAPIKey
	}
	if cfg.LiveKitAPISecret == "" {
		cfg.LiveKitAPISecret = s.LiveKitAPISecret
	}

	// 首次启动或 secrets.json 已存在但 livekit.yaml 缺失时，自动渲染 livekit.yaml。
	// 输出路径：<LocalDataPath>/livekit.yaml；模板路径：livekit/livekit.yaml.template（相对工作目录）。
	// 已存在的 livekit.yaml 不会被覆盖（RenderLiveKitConfig 内部检测）。
	livekitOutputPath := filepath.Join(cfg.LocalDataPath, "livekit.yaml")
	livekitTemplatePath := "livekit/livekit.yaml.template"
	nodeIP, err := DetectNodeIP()
	if err != nil {
		log.Printf("WARN: 检测节点 IP 失败，livekit.yaml 渲染将使用占位符: %v", err)
		nodeIP = "127.0.0.1"
	}
	if err := RenderLiveKitConfig(livekitTemplatePath, livekitOutputPath, s, nodeIP, DefaultLiveKitPorts()); err != nil {
		// 渲染失败不阻塞启动，仅记录警告（用户可手动配置 livekit.yaml）
		log.Printf("WARN: 渲染 livekit.yaml 失败，请手动配置: %v", err)
	}

	return nil
}

// validate checks configuration bounds and allowed values.
func (cfg *Config) validate() error {
	if cfg.Port <= 0 {
		return fmt.Errorf("port must be greater than 0")
	}
	if cfg.DBDriver != "" && cfg.DBDriver != "sqlite" && cfg.DBDriver != "postgres" {
		return fmt.Errorf("db_driver must be sqlite or postgres")
	}
	if cfg.BcryptCost < 4 || cfg.BcryptCost > 31 {
		return fmt.Errorf("bcrypt_cost must be between 4 and 31")
	}
	if cfg.JWTAccessTTL <= 0 {
		cfg.JWTAccessTTL = 15
	}
	if cfg.JWTRefreshTTL <= 0 {
		cfg.JWTRefreshTTL = 7
	}
	if cfg.AdminSessionTTL <= 0 {
		cfg.AdminSessionTTL = 15
	}
	if cfg.RateLimitRequests <= 0 {
		cfg.RateLimitRequests = 300
	}
	if cfg.RateLimitBurst <= 0 {
		cfg.RateLimitBurst = 100
	}
	if cfg.HIBPTimeout <= 0 {
		cfg.HIBPTimeout = 5
	}
	if cfg.HIBPAPIURL == "" {
		cfg.HIBPAPIURL = "https://api.pwnedpasswords.com/range/"
	}
	if cfg.PasswordHistoryCount <= 0 {
		cfg.PasswordHistoryCount = 5
	}
	if cfg.LiveKitPort <= 0 {
		cfg.LiveKitPort = 7880
	}
	if cfg.VPNPort <= 0 {
		cfg.VPNPort = 41641
	}
	// EasyTier 默认配置兜底（DES-2026-0731-02）
	if cfg.EasytierURL == "" {
		cfg.EasytierURL = "http://127.0.0.1:11210"
	}
	if cfg.EasytierPort <= 0 {
		cfg.EasytierPort = 5007
	}
	if cfg.DeployMode == "" {
		cfg.DeployMode = "native"
	}
	if cfg.NeteaseAPIEndpoint == "" {
		cfg.NeteaseAPIEndpoint = "http://localhost:3300"
	}
	// Music Bot Worker 默认端口 5012（与 Worker .env.example 一致）
	if cfg.MusicBotWorkerPort <= 0 {
		cfg.MusicBotWorkerPort = 5012
	}
	if cfg.BotUploadMaxFiles <= 0 {
		cfg.BotUploadMaxFiles = 200
	}
	if cfg.BotUploadMaxTotalBytes <= 0 {
		cfg.BotUploadMaxTotalBytes = 5 << 30
	}
	if cfg.CloudFSMaxFileSize <= 0 {
		cfg.CloudFSMaxFileSize = 100 << 20
	}
	if cfg.CloudFSQuotaGB <= 0 {
		cfg.CloudFSQuotaGB = 10
	}
	if cfg.CloudFSTrashRetentionDays <= 0 {
		cfg.CloudFSTrashRetentionDays = 30
	}
	// Cache (ristretto W-TinyLFU): ensure sane defaults if misconfigured.
	if cfg.CacheMaxCost <= 0 {
		cfg.CacheMaxCost = 1073741824 // 1 GiB
	}
	if cfg.CacheNumCounters <= 0 {
		cfg.CacheNumCounters = 10000000 // 1e7
	}
	return nil
}

// loadEnvFile reads KEY=VALUE pairs from a .env file and sets them as environment variables.
func loadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		// Remove surrounding quotes if present
		value = strings.Trim(value, `"'`)
		// Process environment variables take precedence over .env file values;
		// only set the variable if it is not already defined.
		if key != "" && os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
	return nil
}
