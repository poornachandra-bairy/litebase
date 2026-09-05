// Package config loads Litebase configuration from a file and the environment.
//
// Precedence, lowest to highest: built-in defaults, config file, environment
// variables. Every setting has a usable default so that running the binary with
// no configuration at all still produces a working server.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// Server
	Addr            string        `json:"addr"`
	BaseURL         string        `json:"base_url"`
	ShutdownTimeout time.Duration `json:"shutdown_timeout"`
	ReadTimeout     time.Duration `json:"read_timeout"`
	WriteTimeout    time.Duration `json:"write_timeout"`
	MaxBodyBytes    int64         `json:"max_body_bytes"`
	MaxUploadBytes  int64         `json:"max_upload_bytes"`
	TrustProxy      bool          `json:"trust_proxy"`

	// Storage layout. All persistent state lives under DataDir.
	DataDir string `json:"data_dir"`

	// Security
	SessionTTL     time.Duration `json:"session_ttl"`
	SecureCookies  bool          `json:"secure_cookies"`
	CookieDomain   string        `json:"cookie_domain"`
	AllowedOrigins []string      `json:"allowed_origins"`

	// Bootstrap admin. Only used when no admin user exists yet.
	BootstrapEmail    string `json:"bootstrap_email"`
	BootstrapPassword string `json:"-"`

	// Query execution guards
	QueryTimeout     time.Duration `json:"query_timeout"`
	MaxQueryRows     int           `json:"max_query_rows"`
	MaxOpenDatabases int           `json:"max_open_databases"`

	// Rate limiting (token bucket, per identity)
	AdminRatePerMin int `json:"admin_rate_per_min"`
	APIRatePerMin   int `json:"api_rate_per_min"`
	AuthRatePerMin  int `json:"auth_rate_per_min"`

	// Backups
	BackupDir           string        `json:"backup_dir"`
	BackupEncryptionKey string        `json:"-"`
	SchedulerInterval   time.Duration `json:"scheduler_interval"`

	// Logging
	LogLevel  string `json:"log_level"`
	LogFormat string `json:"log_format"` // json | text

	// Frontend
	StaticDir string `json:"static_dir"`
	DevMode   bool   `json:"dev_mode"`
}

// Paths derived from DataDir.
func (c *Config) MetaDBPath() string   { return filepath.Join(c.DataDir, "litebase.db") }
func (c *Config) DatabasesDir() string { return filepath.Join(c.DataDir, "databases") }
func (c *Config) TempDir() string      { return filepath.Join(c.DataDir, "tmp") }
func (c *Config) BackupsDir() string {
	if c.BackupDir != "" {
		return c.BackupDir
	}
	return filepath.Join(c.DataDir, "backups")
}

func defaults() Config {
	return Config{
		Addr:              ":8090",
		ShutdownTimeout:   15 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		MaxBodyBytes:      2 << 20,   // 2 MiB for JSON bodies
		MaxUploadBytes:    512 << 20, // 512 MiB for database/backup uploads
		DataDir:           "./data",
		SessionTTL:        24 * time.Hour * 7,
		SecureCookies:     false,
		QueryTimeout:      30 * time.Second,
		MaxQueryRows:      5000,
		MaxOpenDatabases:  64,
		AdminRatePerMin:   600,
		APIRatePerMin:     120,
		AuthRatePerMin:    10,
		SchedulerInterval: time.Minute,
		LogLevel:          "info",
		LogFormat:         "json",
		StaticDir:         "",
	}
}

// Load resolves configuration from an optional JSON config file plus the
// environment. An empty path skips file loading; a non-empty path that does not
// exist is an error, so typos are not silently ignored.
func Load(path string) (*Config, error) {
	c := defaults()

	if path == "" {
		path = os.Getenv("LITEBASE_CONFIG")
	}
	if path != "" {
		if err := loadFile(&c, path); err != nil {
			return nil, err
		}
	}
	if err := loadEnv(&c); err != nil {
		return nil, err
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) normalize() error {
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return fmt.Errorf("resolve data dir: %w", err)
	}
	c.DataDir = abs

	if c.BackupDir != "" {
		if c.BackupDir, err = filepath.Abs(c.BackupDir); err != nil {
			return fmt.Errorf("resolve backup dir: %w", err)
		}
	}
	if c.StaticDir != "" {
		if c.StaticDir, err = filepath.Abs(c.StaticDir); err != nil {
			return fmt.Errorf("resolve static dir: %w", err)
		}
	}

	if c.Addr == "" {
		return errors.New("addr must not be empty")
	}

	// A platform such as Coolify may supply the assigned domain with or
	// without a scheme. Normalise it so the generated API documentation always
	// carries a usable absolute URL.
	if c.BaseURL = strings.TrimSpace(strings.TrimSuffix(c.BaseURL, "/")); c.BaseURL != "" {
		if !strings.Contains(c.BaseURL, "://") {
			c.BaseURL = "https://" + c.BaseURL
		}
	}
	if c.MaxQueryRows <= 0 {
		return errors.New("max_query_rows must be positive")
	}
	if c.MaxOpenDatabases <= 0 {
		return errors.New("max_open_databases must be positive")
	}
	if c.SessionTTL <= 0 {
		return errors.New("session_ttl must be positive")
	}
	if c.MaxBodyBytes <= 0 || c.MaxUploadBytes <= 0 {
		return errors.New("body size limits must be positive")
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		return fmt.Errorf("invalid log_format %q (want json or text)", c.LogFormat)
	}
	if c.BootstrapPassword != "" && len(c.BootstrapPassword) < 10 {
		return errors.New("bootstrap password must be at least 10 characters")
	}
	// An encryption key, when supplied, must be usable as an AES-256 key source.
	if c.BackupEncryptionKey != "" && len(c.BackupEncryptionKey) < 16 {
		return errors.New("backup encryption key must be at least 16 characters")
	}
	for i, o := range c.AllowedOrigins {
		c.AllowedOrigins[i] = strings.TrimSuffix(strings.TrimSpace(o), "/")
	}
	return nil
}

// EnsureDirs creates the directory tree the server needs, with owner-only
// permissions since these hold databases, backups and credentials.
func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.DatabasesDir(), c.BackupsDir(), c.TempDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}
	return nil
}

func envStr(key string, dst *string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func envBool(key string, dst *bool) error {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = b
	return nil
}

func envInt(key string, dst *int) error {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = n
	return nil
}

func envBytes(key string, dst *int64) error {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	n, err := parseSize(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = n
	return nil
}

func envDur(key string, dst *time.Duration) error {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = d
	return nil
}

func loadEnv(c *Config) error {
	envStr("LITEBASE_ADDR", &c.Addr)
	envStr("LITEBASE_BASE_URL", &c.BaseURL)
	envStr("LITEBASE_DATA_DIR", &c.DataDir)
	envStr("LITEBASE_BACKUP_DIR", &c.BackupDir)
	envStr("LITEBASE_STATIC_DIR", &c.StaticDir)
	envStr("LITEBASE_LOG_LEVEL", &c.LogLevel)
	envStr("LITEBASE_LOG_FORMAT", &c.LogFormat)
	envStr("LITEBASE_COOKIE_DOMAIN", &c.CookieDomain)
	envStr("LITEBASE_ADMIN_EMAIL", &c.BootstrapEmail)
	envStr("LITEBASE_ADMIN_PASSWORD", &c.BootstrapPassword)
	envStr("LITEBASE_BACKUP_ENCRYPTION_KEY", &c.BackupEncryptionKey)

	if v, ok := os.LookupEnv("LITEBASE_ALLOWED_ORIGINS"); ok && strings.TrimSpace(v) != "" {
		c.AllowedOrigins = splitList(v)
	}

	for _, f := range []func() error{
		func() error { return envBool("LITEBASE_SECURE_COOKIES", &c.SecureCookies) },
		func() error { return envBool("LITEBASE_TRUST_PROXY", &c.TrustProxy) },
		func() error { return envBool("LITEBASE_DEV_MODE", &c.DevMode) },
		func() error { return envInt("LITEBASE_MAX_QUERY_ROWS", &c.MaxQueryRows) },
		func() error { return envInt("LITEBASE_MAX_OPEN_DATABASES", &c.MaxOpenDatabases) },
		func() error { return envInt("LITEBASE_ADMIN_RATE_PER_MIN", &c.AdminRatePerMin) },
		func() error { return envInt("LITEBASE_API_RATE_PER_MIN", &c.APIRatePerMin) },
		func() error { return envInt("LITEBASE_AUTH_RATE_PER_MIN", &c.AuthRatePerMin) },
		func() error { return envBytes("LITEBASE_MAX_BODY_BYTES", &c.MaxBodyBytes) },
		func() error { return envBytes("LITEBASE_MAX_UPLOAD_BYTES", &c.MaxUploadBytes) },
		func() error { return envDur("LITEBASE_SESSION_TTL", &c.SessionTTL) },
		func() error { return envDur("LITEBASE_QUERY_TIMEOUT", &c.QueryTimeout) },
		func() error { return envDur("LITEBASE_SHUTDOWN_TIMEOUT", &c.ShutdownTimeout) },
		func() error { return envDur("LITEBASE_READ_TIMEOUT", &c.ReadTimeout) },
		func() error { return envDur("LITEBASE_WRITE_TIMEOUT", &c.WriteTimeout) },
		func() error { return envDur("LITEBASE_SCHEDULER_INTERVAL", &c.SchedulerInterval) },
	} {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseSize accepts a plain byte count or a suffixed value such as "8MB".
func parseSize(v string) (int64, error) {
	s := strings.TrimSpace(strings.ToUpper(v))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "KB"):
		mult, s = 1<<10, strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1<<20, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "GB"):
		mult, s = 1<<30, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", v)
	}
	return n * mult, nil
}
