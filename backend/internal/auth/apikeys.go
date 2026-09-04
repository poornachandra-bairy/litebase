package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/litebase/litebase/internal/database"
)

var (
	ErrKeyNotFound = errors.New("api key not found")
	ErrKeyInvalid  = errors.New("invalid api key")
	ErrKeyExpired  = errors.New("api key has expired")
	ErrKeyDisabled = errors.New("api key is disabled")
)

// APIKey authenticates machine callers against the generated data API.
type APIKey struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Prefix       string     `json:"prefix"`
	Scopes       []string   `json:"scopes"`
	AllEndpoints bool       `json:"all_endpoints"`
	EndpointIDs  []string   `json:"endpoint_ids"`
	RateLimit    int        `json:"rate_limit"`
	Disabled     bool       `json:"disabled"`
	ExpiresAt    *time.Time `json:"expires_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// CanScope reports whether the key carries the scope an HTTP method requires.
func (k *APIKey) CanScope(scope string) bool {
	return k != nil && slices.Contains(k.Scopes, scope)
}

// CanEndpoint reports whether the key may call a specific endpoint.
func (k *APIKey) CanEndpoint(endpointID string) bool {
	if k == nil {
		return false
	}
	if k.AllEndpoints {
		return true
	}
	return slices.Contains(k.EndpointIDs, endpointID)
}

// CreateAPIKeyInput describes a key to issue.
type CreateAPIKeyInput struct {
	Name         string
	Scopes       []string
	AllEndpoints bool
	EndpointIDs  []string
	RateLimit    int
	ExpiresAt    *time.Time
	CreatedBy    string
}

func (in *CreateAPIKeyInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return errors.New("key name must be between 1 and 100 characters")
	}
	if len(in.Scopes) == 0 {
		return errors.New("at least one scope is required")
	}
	for _, s := range in.Scopes {
		if !ValidScope(s) {
			return fmt.Errorf("unknown scope %q", s)
		}
	}
	if in.RateLimit < 0 {
		return errors.New("rate limit must not be negative")
	}
	if !in.AllEndpoints && len(in.EndpointIDs) == 0 {
		return errors.New("grant at least one endpoint, or allow all endpoints")
	}
	if in.ExpiresAt != nil && in.ExpiresAt.Before(time.Now()) {
		return errors.New("expiry must be in the future")
	}
	return nil
}

// CreateAPIKey issues a key and returns the plaintext exactly once. The
// plaintext is never stored and cannot be recovered afterwards.
func (s *Service) CreateAPIKey(ctx context.Context, in CreateAPIKeyInput) (*APIKey, string, error) {
	if err := in.validate(); err != nil {
		return nil, "", err
	}
	plaintext, display, err := newAPIKey()
	if err != nil {
		return nil, "", err
	}
	scopesJSON, err := json.Marshal(in.Scopes)
	if err != nil {
		return nil, "", err
	}

	now := time.Now().UTC()
	key := &APIKey{
		ID:           database.NewID(),
		Name:         in.Name,
		Prefix:       display,
		Scopes:       in.Scopes,
		AllEndpoints: in.AllEndpoints,
		EndpointIDs:  in.EndpointIDs,
		RateLimit:    in.RateLimit,
		ExpiresAt:    in.ExpiresAt,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()

	var expires any
	if in.ExpiresAt != nil {
		expires = database.FormatTime(in.ExpiresAt.UTC())
	}
	var createdBy any
	if in.CreatedBy != "" {
		createdBy = in.CreatedBy
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, name, key_hash, key_prefix, scopes, all_endpoints, rate_limit,
		                       disabled, expires_at, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		key.ID, key.Name, hashToken(plaintext), key.Prefix, string(scopesJSON),
		boolToInt(key.AllEndpoints), key.RateLimit, expires, createdBy,
		database.FormatTime(now), database.FormatTime(now)); err != nil {
		return nil, "", fmt.Errorf("create api key: %w", err)
	}
	if err := replaceKeyEndpoints(ctx, tx, key.ID, in.AllEndpoints, in.EndpointIDs); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return key, plaintext, nil
}

func replaceKeyEndpoints(ctx context.Context, tx *sql.Tx, keyID string, all bool, endpointIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_key_endpoints WHERE key_id = ?`, keyID); err != nil {
		return err
	}
	if all {
		return nil
	}
	for _, id := range endpointIDs {
		// The foreign key rejects unknown endpoint ids, so a grant can never
		// point at an endpoint that does not exist.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO api_key_endpoints (key_id, endpoint_id) VALUES (?, ?)`, keyID, id); err != nil {
			return fmt.Errorf("grant endpoint %s: %w", id, err)
		}
	}
	return nil
}

// VerifyAPIKey authenticates a presented key.
//
// Lookup is by hash, which is both constant work and impossible to satisfy from
// a stolen database copy without the original secret.
func (s *Service) VerifyAPIKey(ctx context.Context, presented string) (*APIKey, error) {
	if !looksLikeAPIKey(presented) {
		return nil, ErrKeyInvalid
	}

	var (
		k        APIKey
		scopes   string
		all      int
		disabled int
		expires  sql.NullString
		lastUsed sql.NullString
		created  string
		updated  string
	)
	err := s.readDB.QueryRowContext(ctx,
		`SELECT id, name, key_prefix, scopes, all_endpoints, rate_limit, disabled,
		        expires_at, last_used_at, created_at, updated_at
		 FROM api_keys WHERE key_hash = ?`, hashToken(presented)).
		Scan(&k.ID, &k.Name, &k.Prefix, &scopes, &all, &k.RateLimit, &disabled,
			&expires, &lastUsed, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrKeyInvalid
	}
	if err != nil {
		return nil, err
	}
	if disabled != 0 {
		return nil, ErrKeyDisabled
	}
	if expires.Valid && expires.String != "" {
		exp := database.ParseTime(expires.String)
		if !exp.IsZero() && exp.Before(time.Now().UTC()) {
			return nil, ErrKeyExpired
		}
		k.ExpiresAt = &exp
	}

	if err := json.Unmarshal([]byte(scopes), &k.Scopes); err != nil {
		k.Scopes = nil
	}
	k.AllEndpoints = all != 0
	k.CreatedAt = database.ParseTime(created)
	k.UpdatedAt = database.ParseTime(updated)
	if lastUsed.Valid && lastUsed.String != "" {
		t := database.ParseTime(lastUsed.String)
		k.LastUsedAt = &t
	}
	if !k.AllEndpoints {
		if k.EndpointIDs, err = s.keyEndpointIDs(ctx, k.ID); err != nil {
			return nil, err
		}
	}
	return &k, nil
}

func (s *Service) keyEndpointIDs(ctx context.Context, keyID string) ([]string, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT endpoint_id FROM api_key_endpoints WHERE key_id = ?`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// TouchAPIKey records that a key was used. Failures are the caller's to ignore:
// usage tracking must never block a request.
func (s *Service) TouchAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`,
		database.FormatTime(time.Now().UTC()), id)
	return err
}

// ListAPIKeys returns key metadata. The secret is never included.
func (s *Service) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id, name, key_prefix, scopes, all_endpoints, rate_limit, disabled,
		        expires_at, last_used_at, created_at, updated_at
		 FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []APIKey{}
	for rows.Next() {
		var (
			k        APIKey
			scopes   string
			all      int
			disabled int
			expires  sql.NullString
			lastUsed sql.NullString
			created  string
			updated  string
		)
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &scopes, &all, &k.RateLimit, &disabled,
			&expires, &lastUsed, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(scopes), &k.Scopes)
		k.AllEndpoints = all != 0
		k.Disabled = disabled != 0
		k.CreatedAt = database.ParseTime(created)
		k.UpdatedAt = database.ParseTime(updated)
		if expires.Valid && expires.String != "" {
			t := database.ParseTime(expires.String)
			k.ExpiresAt = &t
		}
		if lastUsed.Valid && lastUsed.String != "" {
			t := database.ParseTime(lastUsed.String)
			k.LastUsedAt = &t
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].AllEndpoints {
			continue
		}
		ids, err := s.keyEndpointIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].EndpointIDs = ids
	}
	return out, nil
}

// UpdateAPIKeyInput describes an edit to an existing key. The secret itself is
// immutable; rotating a key means issuing a new one.
type UpdateAPIKeyInput struct {
	Name         string
	Scopes       []string
	AllEndpoints bool
	EndpointIDs  []string
	RateLimit    int
	Disabled     bool
	ExpiresAt    *time.Time
}

// UpdateAPIKey edits a key's metadata and grants.
func (s *Service) UpdateAPIKey(ctx context.Context, id string, in UpdateAPIKeyInput) error {
	create := CreateAPIKeyInput{
		Name: in.Name, Scopes: in.Scopes, AllEndpoints: in.AllEndpoints,
		EndpointIDs: in.EndpointIDs, RateLimit: in.RateLimit,
	}
	// Reuse the create-time rules, minus the future-expiry check: an operator
	// may legitimately set a past expiry to retire a key immediately.
	expiry := in.ExpiresAt
	in.ExpiresAt = nil
	if err := create.validate(); err != nil {
		return err
	}
	scopesJSON, err := json.Marshal(in.Scopes)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var expires any
	if expiry != nil {
		expires = database.FormatTime(expiry.UTC())
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE api_keys SET name = ?, scopes = ?, all_endpoints = ?, rate_limit = ?,
		        disabled = ?, expires_at = ?, updated_at = ? WHERE id = ?`,
		create.Name, string(scopesJSON), boolToInt(in.AllEndpoints), in.RateLimit,
		boolToInt(in.Disabled), expires, database.FormatTime(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrKeyNotFound
	}
	if err := replaceKeyEndpoints(ctx, tx, id, in.AllEndpoints, in.EndpointIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteAPIKey permanently revokes a key.
func (s *Service) DeleteAPIKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrKeyNotFound
	}
	return nil
}
