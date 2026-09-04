package auth

import (
	"context"
	"database/sql"
	"time"

	"github.com/litebase/litebase/internal/database"
)

// Service provides authentication and credential management backed by the
// metadata database.
type Service struct {
	db     *sql.DB // serialised writer
	readDB *sql.DB // concurrent readers
	ttl    time.Duration
}

// NewService builds a Service over the metadata database handle.
func NewService(meta *database.Handle, sessionTTL time.Duration) *Service {
	if sessionTTL <= 0 {
		sessionTTL = 7 * 24 * time.Hour
	}
	return &Service{db: meta.Writer(), readDB: meta.Reader(), ttl: sessionTTL}
}

// SessionTTL is how long a new session remains valid.
func (s *Service) SessionTTL() time.Duration { return s.ttl }

// Bootstrap creates the first owner account when the instance has no users.
//
// It reports whether an account was created, so the caller can log the
// generated password exactly once at first start.
func (s *Service) Bootstrap(ctx context.Context, email, password string) (created bool, generatedPassword string, err error) {
	n, err := s.CountUsers(ctx)
	if err != nil {
		return false, "", err
	}
	if n > 0 {
		return false, "", nil
	}
	if email == "" {
		email = "admin@litebase.local"
	}
	if password == "" {
		// No password was configured, so generate a strong one rather than
		// falling back to a default that would be identical on every install.
		password, err = newToken()
		if err != nil {
			return false, "", err
		}
		generatedPassword = password
	}
	if _, err := s.CreateUser(ctx, email, "Administrator", password, RoleOwner); err != nil {
		return false, "", err
	}
	return true, generatedPassword, nil
}
