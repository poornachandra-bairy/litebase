// Package auth implements administrator identity: password verification,
// sessions, API keys and the role/permission model.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. These target roughly 50-100ms per hash on a small VPS,
// which keeps interactive login fast while making offline cracking expensive.
// They are stored alongside each hash so they can be raised later without
// invalidating existing passwords.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Password policy. Length is the dominant factor in resisting guessing, so the
// minimum is a length floor rather than a composition rule.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 1024
)

var (
	ErrInvalidHash        = errors.New("invalid password hash")
	ErrIncompatibleParams = errors.New("incompatible password hash version")
	ErrWeakPassword       = errors.New("password does not meet requirements")
)

// ValidatePassword enforces the password policy.
func ValidatePassword(pw string) error {
	if !utf8.ValidString(pw) {
		return fmt.Errorf("%w: password must be valid UTF-8", ErrWeakPassword)
	}
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return fmt.Errorf("%w: password must be at least %d characters", ErrWeakPassword, MinPasswordLen)
	}
	// Bound the input so an enormous password cannot be used to force
	// expensive hashing.
	if len(pw) > MaxPasswordLen {
		return fmt.Errorf("%w: password must be at most %d bytes", ErrWeakPassword, MaxPasswordLen)
	}
	return nil
}

// HashPassword derives an Argon2id hash and returns it in the standard PHC
// string format, which embeds the parameters and salt.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches encodedHash.
//
// The comparison is constant time so that a caller cannot learn how much of a
// guess was correct by measuring how long verification took.
func VerifyPassword(password, encodedHash string) (bool, error) {
	params, salt, want, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	var p argonParams
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return p, nil, nil, ErrIncompatibleParams
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 {
		return p, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return p, nil, nil, ErrInvalidHash
	}
	return p, salt, key, nil
}

// dummyHash is verified against when an account does not exist, so that a
// login attempt for an unknown address costs the same as one for a real
// account and cannot be used to enumerate users.
var dummyHash string

func init() {
	h, err := HashPassword(strings.Repeat("x", MinPasswordLen))
	if err != nil {
		panic("litebase: cannot initialise password hasher: " + err.Error())
	}
	dummyHash = h
}

// GenerateRecoveryPassword returns a strong random password for the
// --reset-password recovery flow, so an operator locked out of the dashboard
// never has to invent one under pressure.
func GenerateRecoveryPassword() (string, error) {
	return newToken()
}
