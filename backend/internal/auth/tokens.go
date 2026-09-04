package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// Secret tokens (session cookies, API keys, CSRF tokens) are generated here and
// only ever stored as SHA-256 hashes.
//
// SHA-256 without a work factor is the right choice for these: unlike
// passwords they carry full 256-bit entropy, so there is nothing to brute
// force, and lookups must stay cheap enough to run on every request.

const tokenBytes = 32

// newToken returns a URL-safe random token.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken derives the stored form of a token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// APIKeyPrefix is the identifying prefix on every issued API key. It makes keys
// recognisable in logs and secret scanners.
const APIKeyPrefix = "lbk_"

// newAPIKey returns a full API key and the short, non-secret fragment shown in
// the dashboard so operators can tell keys apart.
func newAPIKey() (key string, display string, err error) {
	raw, err := newToken()
	if err != nil {
		return "", "", err
	}
	key = APIKeyPrefix + raw
	// The display fragment is short enough to be non-sensitive but long enough
	// to distinguish keys in a list.
	display = key[:len(APIKeyPrefix)+6]
	return key, display, nil
}

// looksLikeAPIKey reports whether a credential has the shape of an API key.
// It is a cheap filter, not an authentication decision.
func looksLikeAPIKey(s string) bool {
	return strings.HasPrefix(s, APIKeyPrefix) && len(s) > len(APIKeyPrefix)+16
}
