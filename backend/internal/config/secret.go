package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// secretFileName holds the auto-generated instance secret, kept inside the data
// directory so it survives restarts and redeploys.
const secretFileName = ".instance_key"

// EnsureSecret guarantees the instance has an encryption key.
//
// A key is needed to encrypt backups before they are uploaded to remote storage
// and to protect stored cloud credentials. Requiring the operator to supply one
// means a deployment that skips it silently loses both features, so one is
// generated and persisted on first start instead.
//
// A key given through configuration always wins: an operator who manages
// secrets externally keeps full control, and the generated file is ignored.
//
// The generated key sits beside the data it protects, which is weaker than an
// externally managed secret. It is still worthwhile: the threat it addresses is
// a backup copied to Google Drive or another remote provider, where the key
// does not travel with the data.
func (c *Config) EnsureSecret() (generated bool, err error) {
	if c.BackupEncryptionKey != "" {
		return false, nil
	}

	path := filepath.Join(c.DataDir, secretFileName)

	// Reuse the existing key when one is already present, so backups taken by
	// an earlier run stay readable.
	if data, err := os.ReadFile(path); err == nil {
		if key := strings.TrimSpace(string(data)); len(key) >= 16 {
			c.BackupEncryptionKey = key
			return false, nil
		}
		// A truncated or empty file is unusable; fall through and replace it
		// rather than starting with a weak key.
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("read instance key: %w", err)
	}

	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		return false, fmt.Errorf("generate instance key: %w", err)
	}
	key := base64.RawStdEncoding.EncodeToString(buf)

	// Written owner-only, and created exclusively so two processes starting at
	// once cannot race into different keys.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Another process won the race; adopt whatever it wrote.
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return false, fmt.Errorf("read instance key: %w", readErr)
			}
			c.BackupEncryptionKey = strings.TrimSpace(string(data))
			return false, nil
		}
		return false, fmt.Errorf("write instance key: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(key); err != nil {
		return false, fmt.Errorf("write instance key: %w", err)
	}
	if err := f.Sync(); err != nil {
		return false, fmt.Errorf("write instance key: %w", err)
	}

	c.BackupEncryptionKey = key
	return true, nil
}

// SecretPath is where the generated instance key lives, for operator messages.
func (c *Config) SecretPath() string { return filepath.Join(c.DataDir, secretFileName) }
