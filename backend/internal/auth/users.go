package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/litebase/litebase/internal/database"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrUserNotFound       = errors.New("user not found")
	ErrUserExists         = errors.New("a user with that email already exists")
	ErrUserDisabled       = errors.New("account is disabled")
	ErrInvalidEmail       = errors.New("invalid email address")
	ErrLastOwner          = errors.New("cannot remove the last owner")
)

// User is an administrator of the Litebase instance.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Can reports whether the user may perform p. A disabled account holds no
// permissions regardless of role.
func (u *User) Can(p Permission) bool {
	if u == nil || u.Disabled {
		return false
	}
	return u.Role.Can(p)
}

// NormalizeEmail validates an address and returns its canonical lowercase form,
// which is what uniqueness is enforced on.
func NormalizeEmail(email string) (string, error) {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" || len(trimmed) > 320 {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidEmail, trimmed)
	}
	return strings.ToLower(addr.Address), nil
}

// CreateUser adds an administrator.
func (s *Service) CreateUser(ctx context.Context, email, name, password string, role Role) (*User, error) {
	lower, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if !ValidRole(role) {
		return nil, fmt.Errorf("unknown role %q", role)
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	u := &User{
		ID:        database.NewID(),
		Email:     lower,
		Name:      strings.TrimSpace(name),
		Role:      role,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users (id, email, email_lower, name, password_hash, role, disabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		u.ID, u.Email, lower, u.Name, hash, string(u.Role),
		database.FormatTime(now), database.FormatTime(now))
	if err != nil {
		if isUnique(err) {
			return nil, ErrUserExists
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

const userColumns = `id, email, name, role, disabled, created_at, updated_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var role string
	var disabled int
	var created, updated string
	if err := sc.Scan(&u.ID, &u.Email, &u.Name, &role, &disabled, &created, &updated); err != nil {
		return nil, err
	}
	u.Role = Role(role)
	u.Disabled = disabled != 0
	u.CreatedAt = database.ParseTime(created)
	u.UpdatedAt = database.ParseTime(updated)
	return &u, nil
}

// GetUser looks up a user by id.
func (s *Service) GetUser(ctx context.Context, id string) (*User, error) {
	row := s.readDB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

// ListUsers returns every administrator, oldest first.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// CountUsers reports how many administrators exist. A zero count means the
// instance still needs bootstrapping.
func (s *Service) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.readDB.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// Authenticate verifies an email and password pair.
//
// Unknown addresses are still run through a hash comparison so that response
// timing does not reveal whether an account exists.
func (s *Service) Authenticate(ctx context.Context, email, password string) (*User, error) {
	lower, err := NormalizeEmail(email)
	if err != nil {
		// Compare against the dummy hash anyway, keeping the cost of a
		// malformed address indistinguishable from a wrong password.
		_, _ = VerifyPassword(password, dummyHash)
		return nil, ErrInvalidCredentials
	}

	var (
		u        User
		role     string
		disabled int
		hash     string
		created  string
		updated  string
	)
	err = s.readDB.QueryRowContext(ctx,
		`SELECT id, email, name, role, disabled, password_hash, created_at, updated_at
		 FROM users WHERE email_lower = ?`, lower).
		Scan(&u.ID, &u.Email, &u.Name, &role, &disabled, &hash, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = VerifyPassword(password, dummyHash)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}

	ok, err := VerifyPassword(password, hash)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if disabled != 0 {
		return nil, ErrUserDisabled
	}

	u.Role = Role(role)
	u.CreatedAt = database.ParseTime(created)
	u.UpdatedAt = database.ParseTime(updated)
	return &u, nil
}

// ChangePassword updates a user's password after confirming the current one.
// Every other session for that user is revoked, so a stolen session cannot
// outlive the password it was created under.
func (s *Service) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string, keepSessionID string) error {
	var hash string
	err := s.readDB.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	ok, err := VerifyPassword(currentPassword, hash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCredentials
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		newHash, database.FormatTime(time.Now().UTC()), userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`,
		userID, keepSessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateUser changes a user's profile and role.
func (s *Service) UpdateUser(ctx context.Context, id, name string, role Role, disabled bool) (*User, error) {
	if !ValidRole(role) {
		return nil, fmt.Errorf("unknown role %q", role)
	}
	current, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	// Losing the last owner would leave the instance without anyone able to
	// manage users, so demoting or disabling one is refused.
	if current.Role == RoleOwner && (role != RoleOwner || disabled) {
		owners, err := s.countOwners(ctx)
		if err != nil {
			return nil, err
		}
		if owners <= 1 {
			return nil, ErrLastOwner
		}
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET name = ?, role = ?, disabled = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(name), string(role), boolToInt(disabled),
		database.FormatTime(time.Now().UTC()), id); err != nil {
		return nil, err
	}
	if disabled {
		// A disabled account must not keep working through an existing cookie.
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return nil, err
		}
	}
	return s.GetUser(ctx, id)
}

// SetPassword overwrites a password without knowing the old one. It backs the
// CLI recovery command and owner-initiated resets.
func (s *Service) SetPassword(ctx context.Context, id, newPassword string) error {
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, database.FormatTime(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUser removes an administrator and their sessions.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.Role == RoleOwner {
		owners, err := s.countOwners(ctx)
		if err != nil {
			return err
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	// Sessions cascade via the foreign key, which is enforced because every
	// connection sets foreign_keys=1.
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// FindUserByEmail looks up a user by address.
func (s *Service) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	lower, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	row := s.readDB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email_lower = ?`, lower)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

func (s *Service) countOwners(ctx context.Context) (int, error) {
	var n int
	err := s.readDB.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE role = ? AND disabled = 0`, string(RoleOwner)).Scan(&n)
	return n, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
