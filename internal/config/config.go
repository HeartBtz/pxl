// Package config gère le chargement et la validation de la configuration
// de PXL à partir des variables d'environnement.
//
// Toutes les variables sont préfixées PXL_ et organisées en groupes :
// serveur, base de données, stockage, authentification, upload,
// miniatures, sécurité et métriques.
//
// Utilisation :
//
//	cfg := config.Load()
//	if err := cfg.Validate(); err != nil { ... }
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config centralise toute la configuration de PXL.
// Chaque sous-structure correspond à un groupe de variables d'environnement.
type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Storage   StorageConfig
	Auth      AuthConfig
	Upload    UploadConfig
	Thumbnail ThumbnailConfig
	Security  SecurityConfig
	Metrics   MetricsConfig
	Log       LogConfig
}

type ServerConfig struct {
	Host              string
	Port              int
	BaseURL           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
}

type DatabaseConfig struct {
	Host            string
	Port            int
	Name            string
	User            string
	Password        string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	MigrationsPath  string
}

func (d *DatabaseConfig) DSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(d.User, d.Password),
		Host:   fmt.Sprintf("%s:%d", d.Host, d.Port),
		Path:   d.Name,
	}
	q := u.Query()
	q.Set("sslmode", d.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

type StorageConfig struct {
	Backend          string
	LocalPath        string
	S3Endpoint       string
	S3Region         string
	S3Bucket         string
	S3AccessKey      string
	S3SecretKey      string
	S3UseSSL         bool
	S3ForcePathStyle bool
}

type AuthConfig struct {
	JWTSecret          string
	JWTExpiry          time.Duration
	RefreshExpiry      time.Duration
	AdminUsername      string
	AdminPassword      string
	AllowAnonymous     bool
	AllowRegistration  bool
	DefaultQuotaBytes  int64
	DefaultQuotaImages int
}

type UploadConfig struct {
	MaxSizeBytes int64
	MaxFiles     int
	MaxPixels    int64
	TempDir      string
	MinFreeBytes int64
	AllowedTypes []string
}

type ThumbnailConfig struct {
	Enabled bool
	Workers int
	Size    int
	Quality int
}

type SecurityConfig struct {
	TrustedProxies          []string
	CORSAllowedOrigin       string
	RateLimitRequests       int
	PublicRateLimitRequests int
	RateLimitWindow         time.Duration
}

type MetricsConfig struct {
	Enabled bool
	Port    int
}

type LogConfig struct {
	Level  string
	Format string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	cfg := &Config{
		Server: ServerConfig{
			Host:              envStr("PXL_SERVER_HOST", "0.0.0.0"),
			Port:              envInt("PXL_SERVER_PORT", 8080),
			BaseURL:           envStr("PXL_SERVER_BASE_URL", "http://localhost:8080"),
			ReadHeaderTimeout: envDuration("PXL_SERVER_READ_HEADER_TIMEOUT", 10*time.Second),
			// Body and response deadlines are disabled by default: uploads and image
			// downloads can legitimately take a long time. Slowloris protection is
			// provided by ReadHeaderTimeout and upload size limits.
			ReadTimeout:     envDuration("PXL_SERVER_READ_TIMEOUT", 0),
			WriteTimeout:    envDuration("PXL_SERVER_WRITE_TIMEOUT", 0),
			IdleTimeout:     envDuration("PXL_SERVER_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout: envDuration("PXL_SERVER_SHUTDOWN_TIMEOUT", 30*time.Second),
			MaxHeaderBytes:  envInt("PXL_SERVER_MAX_HEADER_BYTES", 1<<20),
		},
		Database: DatabaseConfig{
			Host:            envStr("PXL_DB_HOST", "localhost"),
			Port:            envInt("PXL_DB_PORT", 5432),
			Name:            envStr("PXL_DB_NAME", "pxl"),
			User:            envStr("PXL_DB_USER", "pxl"),
			Password:        envStr("PXL_DB_PASS", ""),
			SSLMode:         envStr("PXL_DB_SSLMODE", "require"),
			MaxOpenConns:    envInt("PXL_DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    envInt("PXL_DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: envDuration("PXL_DB_CONN_MAX_LIFETIME", 5*time.Minute),
			MigrationsPath:  envStr("PXL_DB_MIGRATIONS_PATH", "migrations"),
		},
		Storage: StorageConfig{
			Backend:          envStr("PXL_STORAGE_BACKEND", "local"),
			LocalPath:        envStr("PXL_STORAGE_LOCAL_PATH", "./data/images"),
			S3Endpoint:       envStr("PXL_STORAGE_S3_ENDPOINT", ""),
			S3Region:         envStr("PXL_STORAGE_S3_REGION", "us-east-1"),
			S3Bucket:         envStr("PXL_STORAGE_S3_BUCKET", ""),
			S3AccessKey:      envStr("PXL_STORAGE_S3_ACCESS_KEY", ""),
			S3SecretKey:      envStr("PXL_STORAGE_S3_SECRET_KEY", ""),
			S3UseSSL:         envBool("PXL_STORAGE_S3_USE_SSL", true),
			S3ForcePathStyle: envBool("PXL_STORAGE_S3_FORCE_PATH_STYLE", false),
		},
		Auth: AuthConfig{
			JWTSecret:          envStr("PXL_AUTH_JWT_SECRET", ""),
			JWTExpiry:          envDuration("PXL_AUTH_JWT_EXPIRY", 15*time.Minute),
			RefreshExpiry:      envDuration("PXL_AUTH_REFRESH_EXPIRY", 7*24*time.Hour),
			AdminUsername:      envStr("PXL_AUTH_ADMIN_USERNAME", "admin"),
			AdminPassword:      envStr("PXL_AUTH_ADMIN_PASSWORD", ""),
			AllowAnonymous:     envBool("PXL_AUTH_ALLOW_ANONYMOUS", true),
			AllowRegistration:  envBool("PXL_AUTH_ALLOW_REGISTRATION", true),
			DefaultQuotaBytes:  envInt64("PXL_AUTH_DEFAULT_QUOTA_BYTES", 1<<30),
			DefaultQuotaImages: envInt("PXL_AUTH_DEFAULT_QUOTA_IMAGES", 5000),
		},
		Upload: UploadConfig{
			MaxSizeBytes: envInt64("PXL_UPLOAD_MAX_SIZE", 50<<20),
			MaxFiles:     envInt("PXL_UPLOAD_MAX_FILES", 10),
			MaxPixels:    envInt64("PXL_UPLOAD_MAX_PIXELS", 40_000_000),
			TempDir:      envStr("PXL_UPLOAD_TEMP_DIR", "./data/images/.staging"),
			MinFreeBytes: envInt64("PXL_UPLOAD_MIN_FREE_BYTES", 1<<30),
			AllowedTypes: envList("PXL_UPLOAD_ALLOWED_TYPES", []string{
				"image/jpeg", "image/png", "image/gif", "image/webp",
			}),
		},
		Thumbnail: ThumbnailConfig{
			Enabled: envBool("PXL_THUMB_ENABLED", true),
			Workers: envInt("PXL_THUMB_WORKERS", 2),
			Size:    envInt("PXL_THUMB_SIZE", 300),
			Quality: envInt("PXL_THUMB_QUALITY", 85),
		},
		Security: SecurityConfig{
			CORSAllowedOrigin:       envStr("PXL_CORS_ORIGIN", ""),
			RateLimitRequests:       envInt("PXL_RATE_LIMIT_REQUESTS", 60),
			PublicRateLimitRequests: envInt("PXL_PUBLIC_RATE_LIMIT_REQUESTS", 600),
			RateLimitWindow:         envDuration("PXL_RATE_LIMIT_WINDOW", time.Minute),
		},
		Metrics: MetricsConfig{
			Enabled: envBool("PXL_METRICS_ENABLED", true),
			Port:    envInt("PXL_METRICS_PORT", 9090),
		},
		Log: LogConfig{
			Level:  envStr("PXL_LOG_LEVEL", "info"),
			Format: envStr("PXL_LOG_FORMAT", "json"),
		},
	}

	if tp := envStr("PXL_TRUSTED_PROXIES", ""); tp != "" {
		cfg.Security.TrustedProxies = strings.Split(tp, ",")
		for i := range cfg.Security.TrustedProxies {
			cfg.Security.TrustedProxies[i] = strings.TrimSpace(cfg.Security.TrustedProxies[i])
		}
	}

	return cfg
}

// Validate checks that required configuration is present and sane.
func (c *Config) Validate() error {
	publicURL, err := url.Parse(c.Server.BaseURL)
	if err != nil || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.Hostname() == "" || publicURL.User != nil || strings.ContainsAny(c.Server.BaseURL, "?#") || publicURL.RawPath != "" || (publicURL.Path != "" && publicURL.Path != "/") || strings.ContainsAny(publicURL.Host, "*\\\"'<>") {
		return fmt.Errorf("PXL_SERVER_BASE_URL must be an absolute http or https origin without credentials, query or path")
	}
	c.Server.BaseURL = strings.TrimSuffix(c.Server.BaseURL, "/")
	if origin := c.Security.CORSAllowedOrigin; origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.Host, "*") {
			return fmt.Errorf("PXL_CORS_ORIGIN must be one exact http or https origin")
		}
	}
	for _, proxy := range c.Security.TrustedProxies {
		_, network, err := net.ParseCIDR(proxy)
		if err != nil && net.ParseIP(proxy) == nil {
			return fmt.Errorf("PXL_TRUSTED_PROXIES must contain IPs or CIDRs")
		}
		if network != nil {
			ones, _ := network.Mask.Size()
			if ones == 0 {
				return fmt.Errorf("PXL_TRUSTED_PROXIES must not trust all addresses")
			}
		}
	}
	if c.Auth.JWTSecret == "" {
		return fmt.Errorf("PXL_AUTH_JWT_SECRET is required")
	}
	if len(c.Auth.JWTSecret) < 32 {
		return fmt.Errorf("PXL_AUTH_JWT_SECRET must be at least 32 characters")
	}
	if c.Auth.AdminPassword != "" && (len(c.Auth.AdminPassword) < 8 || len(c.Auth.AdminPassword) > 72) {
		return fmt.Errorf("PXL_AUTH_ADMIN_PASSWORD must be between 8 and 72 bytes")
	}
	if c.Auth.JWTExpiry <= 0 || c.Auth.RefreshExpiry < c.Auth.JWTExpiry || c.Auth.DefaultQuotaBytes < 0 || c.Auth.DefaultQuotaImages < 0 {
		return fmt.Errorf("PXL authentication expiry or quota settings are invalid")
	}
	if c.Database.Password == "" {
		return fmt.Errorf("PXL_DB_PASS is required")
	}
	if c.Storage.Backend != "local" && c.Storage.Backend != "s3" {
		return fmt.Errorf("PXL_STORAGE_BACKEND must be 'local' or 's3'")
	}
	if c.Storage.Backend == "s3" && c.Storage.S3Bucket == "" {
		return fmt.Errorf("PXL_STORAGE_S3_BUCKET is required when using S3")
	}
	if c.Storage.Backend == "s3" && c.Storage.S3Endpoint == "" {
		return fmt.Errorf("PXL_STORAGE_S3_ENDPOINT is required when using S3")
	}
	if c.Storage.Backend == "s3" && (c.Storage.S3AccessKey == "" || c.Storage.S3SecretKey == "") {
		return fmt.Errorf("PXL_STORAGE_S3_ACCESS_KEY and PXL_STORAGE_S3_SECRET_KEY are required when using S3")
	}
	if c.Upload.MaxSizeBytes <= 0 || c.Upload.MaxSizeBytes == int64(^uint64(0)>>1) {
		return fmt.Errorf("PXL_UPLOAD_MAX_SIZE must be greater than zero")
	}
	if c.Upload.MaxFiles < 1 || c.Upload.MaxFiles > 100 {
		return fmt.Errorf("PXL_UPLOAD_MAX_FILES must be between 1 and 100")
	}
	if c.Upload.MaxPixels <= 0 {
		return fmt.Errorf("PXL_UPLOAD_MAX_PIXELS must be greater than zero")
	}
	if c.Thumbnail.Enabled && (c.Thumbnail.Workers < 1 || c.Thumbnail.Size < 1 || c.Thumbnail.Quality < 1 || c.Thumbnail.Quality > 100) {
		return fmt.Errorf("PXL thumbnail settings are invalid")
	}
	if c.Upload.TempDir == "" {
		return fmt.Errorf("PXL_UPLOAD_TEMP_DIR is required")
	}
	if c.Upload.MinFreeBytes < 0 {
		return fmt.Errorf("PXL_UPLOAD_MIN_FREE_BYTES must not be negative")
	}
	if c.Server.ReadHeaderTimeout <= 0 || c.Server.MaxHeaderBytes < 4096 {
		return fmt.Errorf("PXL_SERVER_READ_HEADER_TIMEOUT must be greater than zero")
	}
	if c.Server.ReadTimeout < 0 || c.Server.WriteTimeout < 0 || c.Server.IdleTimeout <= 0 {
		return fmt.Errorf("PXL server timeouts must not be negative and idle timeout must be greater than zero")
	}
	if c.Security.RateLimitRequests <= 0 || c.Security.PublicRateLimitRequests <= 0 || c.Security.RateLimitWindow <= 0 {
		return fmt.Errorf("PXL rate limits must be greater than zero")
	}
	if c.Database.MaxOpenConns < 1 || c.Database.MaxIdleConns < 0 || c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("PXL database pool settings are invalid")
	}
	return nil
}

// ── env helpers ──────────────────────────────────────────

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		slog.Warn("invalid env var, using default", slog.String("key", key), slog.String("value", v), slog.Int("default", def))
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
		slog.Warn("invalid env var, using default", slog.String("key", key), slog.String("value", v), slog.Int64("default", def))
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
		slog.Warn("invalid env var, using default", slog.String("key", key), slog.String("value", v), slog.Bool("default", def))
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		slog.Warn("invalid env var, using default", slog.String("key", key), slog.String("value", v), slog.String("default", def.String()))
	}
	return def
}

func envList(key string, def []string) []string {
	if v := os.Getenv(key); v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				out = append(out, t)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return def
}
