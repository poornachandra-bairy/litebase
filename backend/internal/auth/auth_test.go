package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/litebase/litebase/internal/database"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	m, err := database.NewManager(context.Background(), database.ManagerOptions{
		DatabasesDir: filepath.Join(dir, "databases"),
		MetaPath:     filepath.Join(dir, "litebase.db"),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return NewService(m.Meta(), time.Hour)
}

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash = %q, want argon2id PHC string", hash)
	}
	// The plaintext must not be recoverable from, or present in, the hash.
	if strings.Contains(hash, "correct horse battery") {
		t.Error("hash contains the plaintext password")
	}

	ok, err := VerifyPassword("correct horse battery", hash)
	if err != nil || !ok {
		t.Errorf("VerifyPassword(correct) = %v, %v; want true, nil", ok, err)
	}
	ok, err = VerifyPassword("wrong password!!", hash)
	if err != nil {
		t.Fatalf("VerifyPassword(wrong): %v", err)
	}
	if ok {
		t.Error("VerifyPassword accepted an incorrect password")
	}
}

func TestHashPasswordIsSalted(t *testing.T) {
	a, _ := HashPassword("same password 123")
	b, _ := HashPassword("same password 123")
	if a == b {
		t.Error("identical passwords produced identical hashes; salt is not applied")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, h := range []string{"", "notahash", "$argon2id$v=19$bad$x$y", "$bcrypt$x$y$z$w$v"} {
		if _, err := VerifyPassword("whatever", h); err == nil {
			t.Errorf("VerifyPassword(%q) = nil error, want error", h)
		}
	}
}

func TestValidatePasswordPolicy(t *testing.T) {
	if err := ValidatePassword("short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("short password err = %v, want ErrWeakPassword", err)
	}
	if err := ValidatePassword(strings.Repeat("a", MaxPasswordLen+1)); !errors.Is(err, ErrWeakPassword) {
		t.Error("oversized password accepted")
	}
	if err := ValidatePassword("a reasonable password"); err != nil {
		t.Errorf("valid password rejected: %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	if _, err := s.CreateUser(ctx, "Admin@Example.COM", "Admin", "supersecret123", RoleOwner); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Email matching is case-insensitive.
	u, err := s.Authenticate(ctx, "admin@example.com", "supersecret123")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if u.Role != RoleOwner {
		t.Errorf("role = %q, want owner", u.Role)
	}

	if _, err := s.Authenticate(ctx, "admin@example.com", "wrongpassword"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password err = %v, want ErrInvalidCredentials", err)
	}
	// An unknown account must be indistinguishable from a wrong password.
	if _, err := s.Authenticate(ctx, "nobody@example.com", "supersecret123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := s.CreateUser(ctx, "admin@example.com", "Dup", "supersecret123", RoleAdmin); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate email err = %v, want ErrUserExists", err)
	}
}

func TestDisabledUserCannotAuthenticate(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	owner, _ := s.CreateUser(ctx, "owner@example.com", "Owner", "ownerpassword1", RoleOwner)
	u, err := s.CreateUser(ctx, "editor@example.com", "Editor", "editorpassword1", RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	_ = owner

	if _, err := s.UpdateUser(ctx, u.ID, "Editor", RoleEditor, true); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if _, err := s.Authenticate(ctx, "editor@example.com", "editorpassword1"); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("disabled user err = %v, want ErrUserDisabled", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	u, err := s.CreateUser(ctx, "admin@example.com", "Admin", "supersecret123", RoleOwner)
	if err != nil {
		t.Fatal(err)
	}

	token, sess, err := s.CreateSession(ctx, u.ID, "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if token == "" || sess.CSRFToken == "" {
		t.Fatal("empty session or csrf token")
	}

	got, gotUser, err := s.LookupSession(ctx, token)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if got.ID != sess.ID || gotUser.ID != u.ID {
		t.Error("LookupSession returned the wrong session or user")
	}

	// A tampered token must not resolve.
	if _, _, err := s.LookupSession(ctx, token+"x"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("tampered token err = %v, want ErrSessionNotFound", err)
	}

	if !got.VerifyCSRF(sess.CSRFToken) {
		t.Error("VerifyCSRF rejected the correct token")
	}
	if got.VerifyCSRF("wrong") || got.VerifyCSRF("") {
		t.Error("VerifyCSRF accepted an incorrect token")
	}

	if err := s.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LookupSession(ctx, token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("revoked session err = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionTokenIsNotStoredInPlaintext(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	u, _ := s.CreateUser(ctx, "admin@example.com", "Admin", "supersecret123", RoleOwner)

	token, _, err := s.CreateSession(ctx, u.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT count(*) FROM sessions WHERE token_hash = ?`, token).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("session token is stored in plaintext")
	}
}

func TestExpiredSessionIsRejectedAndPurged(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	// A negative TTL makes every new session already expired.
	s.ttl = -time.Minute

	u, _ := s.CreateUser(ctx, "admin@example.com", "Admin", "supersecret123", RoleOwner)
	token, _, err := s.CreateSession(ctx, u.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LookupSession(ctx, token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("expired session err = %v, want ErrSessionNotFound", err)
	}

	n, err := s.PurgeExpiredSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged %d sessions, want 1", n)
	}
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	u, _ := s.CreateUser(ctx, "admin@example.com", "Admin", "supersecret123", RoleOwner)

	keepToken, keepSess, _ := s.CreateSession(ctx, u.ID, "", "")
	otherToken, _, _ := s.CreateSession(ctx, u.ID, "", "")

	if err := s.ChangePassword(ctx, u.ID, "supersecret123", "brandnewpassword", keepSess.ID); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	// The session that made the change survives; every other one is revoked.
	if _, _, err := s.LookupSession(ctx, keepToken); err != nil {
		t.Errorf("current session revoked: %v", err)
	}
	if _, _, err := s.LookupSession(ctx, otherToken); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("other session err = %v, want ErrSessionNotFound", err)
	}
	if _, err := s.Authenticate(ctx, "admin@example.com", "brandnewpassword"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
	if err := s.ChangePassword(ctx, u.ID, "wrongcurrent", "another", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong current password err = %v, want ErrInvalidCredentials", err)
	}
}

func TestLastOwnerIsProtected(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	owner, _ := s.CreateUser(ctx, "owner@example.com", "Owner", "ownerpassword1", RoleOwner)

	if _, err := s.UpdateUser(ctx, owner.ID, "Owner", RoleViewer, false); !errors.Is(err, ErrLastOwner) {
		t.Errorf("demote last owner err = %v, want ErrLastOwner", err)
	}
	if err := s.DeleteUser(ctx, owner.ID); !errors.Is(err, ErrLastOwner) {
		t.Errorf("delete last owner err = %v, want ErrLastOwner", err)
	}

	// With a second owner present the first may be removed.
	if _, err := s.CreateUser(ctx, "owner2@example.com", "Owner2", "ownerpassword2", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, owner.ID); err != nil {
		t.Errorf("delete owner with a spare owner present: %v", err)
	}
}

func TestBootstrapOnlyRunsOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	created, generated, err := s.Bootstrap(ctx, "first@example.com", "")
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if !created {
		t.Fatal("Bootstrap did not create the first user")
	}
	// With no password configured a strong one must be generated, not defaulted.
	if len(generated) < 20 {
		t.Errorf("generated password too short: %d chars", len(generated))
	}

	created2, _, err := s.Bootstrap(ctx, "second@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Error("Bootstrap created a second user on an initialised instance")
	}
}

func TestRolePermissions(t *testing.T) {
	if !RoleOwner.Can(PermUsersWrite) {
		t.Error("owner cannot manage users")
	}
	if RoleAdmin.Can(PermUsersWrite) {
		t.Error("admin should not manage users")
	}
	if RoleViewer.Can(PermRowsWrite) || RoleViewer.Can(PermSQLWrite) {
		t.Error("viewer has write permissions")
	}
	if !RoleViewer.Can(PermRowsRead) {
		t.Error("viewer cannot read rows")
	}
	if RoleEditor.Can(PermSchemaWrite) {
		t.Error("editor should not alter schemas")
	}
	// A disabled user holds no permissions whatever their role.
	u := &User{Role: RoleOwner, Disabled: true}
	if u.Can(PermDatabaseRead) {
		t.Error("disabled user retained permissions")
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	key, plaintext, err := s.CreateAPIKey(ctx, CreateAPIKeyInput{
		Name: "reporting", Scopes: []string{ScopeRead}, AllEndpoints: true,
	})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if !strings.HasPrefix(plaintext, APIKeyPrefix) {
		t.Errorf("key %q lacks the %q prefix", plaintext, APIKeyPrefix)
	}

	got, err := s.VerifyAPIKey(ctx, plaintext)
	if err != nil {
		t.Fatalf("VerifyAPIKey: %v", err)
	}
	if got.ID != key.ID {
		t.Error("VerifyAPIKey returned the wrong key")
	}
	if !got.CanScope(ScopeRead) || got.CanScope(ScopeWrite) {
		t.Errorf("scopes = %v, want read only", got.Scopes)
	}
	if !got.CanEndpoint("anything") {
		t.Error("all-endpoints key denied an endpoint")
	}

	for _, bad := range []string{"", "nope", plaintext + "x", APIKeyPrefix + "short"} {
		if _, err := s.VerifyAPIKey(ctx, bad); err == nil {
			t.Errorf("VerifyAPIKey(%q) = nil error, want rejection", bad)
		}
	}

	// Disabling must take effect immediately.
	if err := s.UpdateAPIKey(ctx, key.ID, UpdateAPIKeyInput{
		Name: "reporting", Scopes: []string{ScopeRead}, AllEndpoints: true, Disabled: true,
	}); err != nil {
		t.Fatalf("UpdateAPIKey: %v", err)
	}
	if _, err := s.VerifyAPIKey(ctx, plaintext); !errors.Is(err, ErrKeyDisabled) {
		t.Errorf("disabled key err = %v, want ErrKeyDisabled", err)
	}

	if err := s.DeleteAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyAPIKey(ctx, plaintext); !errors.Is(err, ErrKeyInvalid) {
		t.Errorf("deleted key err = %v, want ErrKeyInvalid", err)
	}
}

func TestExpiredAPIKeyIsRejected(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	key, plaintext, err := s.CreateAPIKey(ctx, CreateAPIKeyInput{
		Name: "temp", Scopes: []string{ScopeRead}, AllEndpoints: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := s.UpdateAPIKey(ctx, key.ID, UpdateAPIKeyInput{
		Name: "temp", Scopes: []string{ScopeRead}, AllEndpoints: true, ExpiresAt: &past,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyAPIKey(ctx, plaintext); !errors.Is(err, ErrKeyExpired) {
		t.Errorf("expired key err = %v, want ErrKeyExpired", err)
	}
}

func TestCreateAPIKeyValidation(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)

	bad := []CreateAPIKeyInput{
		{Name: "", Scopes: []string{ScopeRead}, AllEndpoints: true},
		{Name: "ok", Scopes: nil, AllEndpoints: true},
		{Name: "ok", Scopes: []string{"superuser"}, AllEndpoints: true},
		{Name: "ok", Scopes: []string{ScopeRead}, AllEndpoints: false},
		{Name: "ok", Scopes: []string{ScopeRead}, AllEndpoints: true, RateLimit: -1},
	}
	for i, in := range bad {
		if _, _, err := s.CreateAPIKey(ctx, in); err == nil {
			t.Errorf("case %d: CreateAPIKey = nil error, want rejection", i)
		}
	}
}
