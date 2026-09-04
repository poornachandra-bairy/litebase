package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Ensure a clean environment so defaults are what is actually tested.
	for _, k := range os.Environ() {
		if len(k) > 9 && k[:9] == "LITEBASE_" {
			name := k[:len(k)]
			for i := range name {
				if name[i] == '=' {
					os.Unsetenv(name[:i])
					break
				}
			}
		}
	}

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":8090" {
		t.Errorf("addr = %q", c.Addr)
	}
	if c.LogFormat != "json" {
		t.Errorf("log format = %q", c.LogFormat)
	}
	// A default install must not be insecure by accident, but must also work
	// over plain HTTP on first run.
	if c.SecureCookies {
		t.Error("secure cookies default to on, which breaks a plain HTTP first run")
	}
	if c.TrustProxy {
		t.Error("trust proxy defaults to on, which would allow address spoofing")
	}
	if !filepath.IsAbs(c.DataDir) {
		t.Errorf("data dir %q is not absolute", c.DataDir)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("LITEBASE_ADDR", "127.0.0.1:9999")
	t.Setenv("LITEBASE_LOG_LEVEL", "debug")
	t.Setenv("LITEBASE_SECURE_COOKIES", "true")
	t.Setenv("LITEBASE_SESSION_TTL", "48h")
	t.Setenv("LITEBASE_MAX_QUERY_ROWS", "250")
	t.Setenv("LITEBASE_MAX_UPLOAD_BYTES", "10MB")
	t.Setenv("LITEBASE_ALLOWED_ORIGINS", "https://a.example.com, https://b.example.com/")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != "127.0.0.1:9999" || c.LogLevel != "debug" || !c.SecureCookies {
		t.Errorf("env not applied: %+v", c)
	}
	if c.SessionTTL != 48*time.Hour {
		t.Errorf("session ttl = %v", c.SessionTTL)
	}
	if c.MaxQueryRows != 250 {
		t.Errorf("max query rows = %d", c.MaxQueryRows)
	}
	if c.MaxUploadBytes != 10<<20 {
		t.Errorf("max upload = %d, want 10MB", c.MaxUploadBytes)
	}
	// Trailing slashes are trimmed so origin comparison is exact.
	if len(c.AllowedOrigins) != 2 || c.AllowedOrigins[1] != "https://b.example.com" {
		t.Errorf("allowed origins = %v", c.AllowedOrigins)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	t.Setenv("LITEBASE_SESSION_TTL", "not-a-duration")
	if _, err := Load(""); err == nil {
		t.Error("invalid duration accepted")
	}
	os.Unsetenv("LITEBASE_SESSION_TTL")

	t.Setenv("LITEBASE_LOG_FORMAT", "yaml")
	if _, err := Load(""); err == nil {
		t.Error("invalid log format accepted")
	}
	os.Unsetenv("LITEBASE_LOG_FORMAT")

	// A weak bootstrap password must be refused rather than silently used.
	t.Setenv("LITEBASE_ADMIN_PASSWORD", "short")
	if _, err := Load(""); err == nil {
		t.Error("short bootstrap password accepted")
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// Comments are supported so an operator can annotate their config.
	content := `{
	  // The address the server listens on.
	  "addr": "0.0.0.0:7000",
	  "log_level": "warn",
	  "session_ttl": "12h",
	  "max_body_bytes": "4MB",
	  "max_query_rows": 123
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != "0.0.0.0:7000" || c.LogLevel != "warn" {
		t.Errorf("file values not applied: %+v", c)
	}
	if c.SessionTTL != 12*time.Hour {
		t.Errorf("session ttl = %v", c.SessionTTL)
	}
	if c.MaxBodyBytes != 4<<20 {
		t.Errorf("max body = %d", c.MaxBodyBytes)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"addr": "0.0.0.0:7000"}`), 0o600)

	t.Setenv("LITEBASE_ADDR", "127.0.0.1:8888")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:8888" {
		t.Errorf("addr = %q; the environment must win over the file", c.Addr)
	}
}

func TestLoadRejectsMissingFileAndUnknownKeys(t *testing.T) {
	// A typo in the path should not be silently ignored.
	if _, err := Load("/nonexistent/litebase.json"); err == nil {
		t.Error("missing config file accepted")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"addrr": "typo"}`), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("unknown config key accepted; a typo would be silently ignored")
	}
}

func TestEnsureDirsCreatesOwnerOnlyTree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LITEBASE_DATA_DIR", filepath.Join(dir, "data"))

	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	for _, p := range []string{c.DataDir, c.DatabasesDir(), c.BackupsDir(), c.TempDir()} {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("%s not created: %v", p, err)
			continue
		}
		// Databases and backups are sensitive, so the tree is owner-only.
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s has mode %o, want 700", p, perm)
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"1024": 1024, "1KB": 1 << 10, "2MB": 2 << 20, "1GB": 1 << 30, "512B": 512,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		if err != nil {
			t.Errorf("parseSize(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parseSize("huge"); err == nil {
		t.Error("invalid size accepted")
	}
}
