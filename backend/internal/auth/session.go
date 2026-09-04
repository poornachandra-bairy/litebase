package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/litebase/litebase/internal/database"
)

var (
	ErrSessionNotFound = errors.New("session not found or expired")
)

// Session is an authenticated browser session.
type Session struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	CSRFToken  string    `json:"-"`
	UserAgent  string    `json:"user_agent"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// CreateSession issues a new session and returns the opaque token to place in
// the cookie. Only the token's hash is persisted, so the database never holds a
// credential that could be replayed.
func (s *Service) CreateSession(ctx context.Context, userID, userAgent, ip string) (token string, sess *Session, err error) {
	token, err = newToken()
	if err != nil {
		return "", nil, err
	}
	csrf, err := newToken()
	if err != nil {
		return "", nil, err
	}

	now := time.Now().UTC()
	sess = &Session{
		ID:         database.NewID(),
		UserID:     userID,
		CSRFToken:  csrf,
		UserAgent:  truncate(userAgent, 512),
		IP:         truncate(ip, 64),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.ttl),
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, token_hash, user_id, csrf_token, user_agent, ip, created_at, last_seen_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, hashToken(token), sess.UserID, sess.CSRFToken, sess.UserAgent, sess.IP,
		database.FormatTime(sess.CreatedAt), database.FormatTime(sess.LastSeenAt),
		database.FormatTime(sess.ExpiresAt)); err != nil {
		return "", nil, fmt.Errorf("create session: %w", err)
	}
	return token, sess, nil
}

// LookupSession resolves a session token to its session and user.
//
// Expiry is enforced in the query, so a stale row can never authenticate a
// request even if the cleanup job has not yet removed it.
func (s *Service) LookupSession(ctx context.Context, token string) (*Session, *User, error) {
	if token == "" {
		return nil, nil, ErrSessionNotFound
	}

	var (
		sess                       Session
		u                          User
		role                       string
		disabled                   int
		created, lastSeen, expires string
		uCreated, uUpdated         string
	)
	err := s.readDB.QueryRowContext(ctx,
		`SELECT s.id, s.user_id, s.csrf_token, s.user_agent, s.ip, s.created_at, s.last_seen_at, s.expires_at,
		        u.id, u.email, u.name, u.role, u.disabled, u.created_at, u.updated_at
		 FROM sessions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ?`,
		hashToken(token), database.FormatTime(time.Now().UTC())).
		Scan(&sess.ID, &sess.UserID, &sess.CSRFToken, &sess.UserAgent, &sess.IP,
			&created, &lastSeen, &expires,
			&u.ID, &u.Email, &u.Name, &role, &disabled, &uCreated, &uUpdated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if disabled != 0 {
		return nil, nil, ErrUserDisabled
	}

	sess.CreatedAt = database.ParseTime(created)
	sess.LastSeenAt = database.ParseTime(lastSeen)
	sess.ExpiresAt = database.ParseTime(expires)
	u.Role = Role(role)
	u.CreatedAt = database.ParseTime(uCreated)
	u.UpdatedAt = database.ParseTime(uUpdated)
	return &sess, &u, nil
}

// TouchSession records activity and slides the expiry forward.
//
// The write is skipped unless a minute has passed, so ordinary dashboard
// polling does not turn every read into a database write.
func (s *Service) TouchSession(ctx context.Context, sess *Session) error {
	now := time.Now().UTC()
	if now.Sub(sess.LastSeenAt) < time.Minute {
		return nil
	}
	newExpiry := now.Add(s.ttl)
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
		database.FormatTime(now), database.FormatTime(newExpiry), sess.ID)
	if err == nil {
		sess.LastSeenAt = now
		sess.ExpiresAt = newExpiry
	}
	return err
}

// VerifyCSRF compares a submitted CSRF token with the session's, in constant
// time.
func (sess *Session) VerifyCSRF(token string) bool {
	if sess == nil || token == "" || sess.CSRFToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sess.CSRFToken), []byte(token)) == 1
}

// DeleteSession revokes one session.
func (s *Service) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteSessionsForUser revokes every session belonging to a user.
func (s *Service) DeleteSessionsForUser(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// PurgeExpiredSessions deletes sessions past their expiry and returns how many
// rows were removed. The scheduler calls this periodically.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`,
		database.FormatTime(time.Now().UTC()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
