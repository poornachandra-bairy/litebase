package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/litebase/litebase/internal/database"
	"golang.org/x/crypto/hkdf"
)

// Settings hold operator-managed values in the metadata database.
//
// Values marked secret — currently the Google Drive OAuth credentials — are
// encrypted at rest with a key derived from the server's own secret, so a
// copied database file does not hand over the operator's cloud storage.

// settingsStore reads and writes the settings table.
type settingsStore struct {
	db     *sql.DB
	readDB *sql.DB
	// secret is the key material used to encrypt secret values.
	secret string
}

const gdriveSettingKey = "storage.gdrive"

func (s *settingsStore) get(ctx context.Context, key string) (string, bool, error) {
	var value string
	var secret int
	err := s.readDB.QueryRowContext(ctx,
		`SELECT value, secret FROM settings WHERE key = ?`, key).Scan(&value, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	if secret != 0 {
		decrypted, err := s.decrypt(value)
		if err != nil {
			return "", false, err
		}
		return decrypted, true, nil
	}
	return value, true, nil
}

func (s *settingsStore) set(ctx context.Context, key, value string, secret bool) error {
	stored := value
	if secret {
		if s.secret == "" {
			return errors.New("a secret setting cannot be stored without a server secret; set LITEBASE_BACKUP_ENCRYPTION_KEY")
		}
		encrypted, err := s.encrypt(value)
		if err != nil {
			return err
		}
		stored = encrypted
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, secret, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value,
			secret = excluded.secret, updated_at = excluded.updated_at`,
		key, stored, boolToInt(secret), database.FormatTime(nowUTC()))
	return err
}

func (s *settingsStore) delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// encrypt seals a secret setting with AES-GCM.
func (s *settingsStore) encrypt(plaintext string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := make([]byte, 32)
	kdf := hkdf.New(sha256.New, []byte(s.secret), salt, []byte("litebase-settings-v1"))
	if _, err := io.ReadFull(kdf, key); err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	sealed := aead.Seal(nil, nonce, []byte(plaintext), nil)
	// salt || nonce || ciphertext, base64 encoded for storage in a TEXT column.
	payload := append(append(salt, nonce...), sealed...)
	return base64.StdEncoding.EncodeToString(payload), nil
}

func (s *settingsStore) decrypt(encoded string) (string, error) {
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("stored setting is not readable")
	}
	if s.secret == "" {
		return "", errors.New("this setting is encrypted but no server secret is configured")
	}
	if len(payload) < 16+12+16 {
		return "", errors.New("stored setting is truncated")
	}

	salt, rest := payload[:16], payload[16:]
	key := make([]byte, 32)
	kdf := hkdf.New(sha256.New, []byte(s.secret), salt, []byte("litebase-settings-v1"))
	if _, err := io.ReadFull(kdf, key); err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(rest) < aead.NonceSize() {
		return "", errors.New("stored setting is truncated")
	}

	nonce, sealed := rest[:aead.NonceSize()], rest[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		// The key changed or the row was tampered with; either way the value
		// is unusable and must not be returned.
		return "", errors.New("this setting could not be decrypted; it may have been stored with a different encryption key")
	}
	return string(plaintext), nil
}

// getJSON reads a JSON-encoded setting into dst.
func (s *settingsStore) getJSON(ctx context.Context, key string, dst any) (bool, error) {
	raw, ok, err := s.get(ctx, key)
	if err != nil || !ok {
		return ok, err
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("setting %s is not valid JSON", key)
	}
	return true, nil
}

func (s *settingsStore) setJSON(ctx context.Context, key string, value any, secret bool) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.set(ctx, key, string(encoded), secret)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
