package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// fileConfig mirrors Config but uses string durations so the config file can be
// written with human-friendly values like "30s" instead of nanosecond counts.
type fileConfig struct {
	Addr            *string `json:"addr"`
	BaseURL         *string `json:"base_url"`
	ShutdownTimeout *string `json:"shutdown_timeout"`
	ReadTimeout     *string `json:"read_timeout"`
	WriteTimeout    *string `json:"write_timeout"`
	MaxBodyBytes    *string `json:"max_body_bytes"`
	MaxUploadBytes  *string `json:"max_upload_bytes"`
	TrustProxy      *bool   `json:"trust_proxy"`

	DataDir   *string `json:"data_dir"`
	BackupDir *string `json:"backup_dir"`
	StaticDir *string `json:"static_dir"`

	SessionTTL     *string   `json:"session_ttl"`
	SecureCookies  *bool     `json:"secure_cookies"`
	CookieDomain   *string   `json:"cookie_domain"`
	AllowedOrigins *[]string `json:"allowed_origins"`

	QueryTimeout     *string `json:"query_timeout"`
	MaxQueryRows     *int    `json:"max_query_rows"`
	MaxOpenDatabases *int    `json:"max_open_databases"`

	AdminRatePerMin *int `json:"admin_rate_per_min"`
	APIRatePerMin   *int `json:"api_rate_per_min"`
	AuthRatePerMin  *int `json:"auth_rate_per_min"`

	SchedulerInterval *string `json:"scheduler_interval"`

	LogLevel  *string `json:"log_level"`
	LogFormat *string `json:"log_format"`
	DevMode   *bool   `json:"dev_mode"`
}

func loadFile(c *Config, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	var fc fileConfig
	dec := json.NewDecoder(newCommentStripper(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fc); err != nil {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}

	setStr(fc.Addr, &c.Addr)
	setStr(fc.BaseURL, &c.BaseURL)
	setStr(fc.DataDir, &c.DataDir)
	setStr(fc.BackupDir, &c.BackupDir)
	setStr(fc.StaticDir, &c.StaticDir)
	setStr(fc.CookieDomain, &c.CookieDomain)
	setStr(fc.LogLevel, &c.LogLevel)
	setStr(fc.LogFormat, &c.LogFormat)
	setBool(fc.TrustProxy, &c.TrustProxy)
	setBool(fc.SecureCookies, &c.SecureCookies)
	setBool(fc.DevMode, &c.DevMode)
	setInt(fc.MaxQueryRows, &c.MaxQueryRows)
	setInt(fc.MaxOpenDatabases, &c.MaxOpenDatabases)
	setInt(fc.AdminRatePerMin, &c.AdminRatePerMin)
	setInt(fc.APIRatePerMin, &c.APIRatePerMin)
	setInt(fc.AuthRatePerMin, &c.AuthRatePerMin)
	if fc.AllowedOrigins != nil {
		c.AllowedOrigins = *fc.AllowedOrigins
	}

	for _, d := range []struct {
		name string
		src  *string
		dst  *time.Duration
	}{
		{"shutdown_timeout", fc.ShutdownTimeout, &c.ShutdownTimeout},
		{"read_timeout", fc.ReadTimeout, &c.ReadTimeout},
		{"write_timeout", fc.WriteTimeout, &c.WriteTimeout},
		{"session_ttl", fc.SessionTTL, &c.SessionTTL},
		{"query_timeout", fc.QueryTimeout, &c.QueryTimeout},
		{"scheduler_interval", fc.SchedulerInterval, &c.SchedulerInterval},
	} {
		if d.src == nil || *d.src == "" {
			continue
		}
		v, err := time.ParseDuration(*d.src)
		if err != nil {
			return fmt.Errorf("config %s: %w", d.name, err)
		}
		*d.dst = v
	}

	for _, b := range []struct {
		name string
		src  *string
		dst  *int64
	}{
		{"max_body_bytes", fc.MaxBodyBytes, &c.MaxBodyBytes},
		{"max_upload_bytes", fc.MaxUploadBytes, &c.MaxUploadBytes},
	} {
		if b.src == nil || *b.src == "" {
			continue
		}
		v, err := parseSize(*b.src)
		if err != nil {
			return fmt.Errorf("config %s: %w", b.name, err)
		}
		*b.dst = v
	}
	return nil
}

func setStr(src *string, dst *string) {
	if src != nil {
		*dst = *src
	}
}
func setBool(src *bool, dst *bool) {
	if src != nil {
		*dst = *src
	}
}
func setInt(src *int, dst *int) {
	if src != nil {
		*dst = *src
	}
}
