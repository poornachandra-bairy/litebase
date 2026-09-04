package database

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateIdentifierRejectsInjection(t *testing.T) {
	bad := []string{
		"",
		"users; DROP TABLE users",
		`users"`,
		"users'",
		"users--",
		"users users",
		"1users",
		"users\x00",
		"sqlite_master",
		"SQLITE_stat1",
		"users\n",
		"users/*",
		"users)",
		"`users`",
		"[users]",
		strings.Repeat("a", MaxIdentifierLen+1),
		"用户", // non-ASCII letters are outside the supported set
	}
	for _, name := range bad {
		if err := ValidateIdentifier(name); err == nil {
			t.Errorf("ValidateIdentifier(%q) = nil, want error", name)
		}
		if _, err := QuoteIdent(name); err == nil {
			t.Errorf("QuoteIdent(%q) = nil error, want error", name)
		}
	}
}

func TestValidateIdentifierAcceptsNormalNames(t *testing.T) {
	good := []string{"users", "_private", "order_items", "t1", "A", strings.Repeat("a", MaxIdentifierLen)}
	for _, name := range good {
		if err := ValidateIdentifier(name); err != nil {
			t.Errorf("ValidateIdentifier(%q) = %v, want nil", name, err)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	got, err := QuoteIdent("order_items")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"order_items"` {
		t.Errorf("QuoteIdent = %s, want \"order_items\"", got)
	}
}

func TestQuoteStringLiteralEscapesQuotes(t *testing.T) {
	if got := QuoteStringLiteral("it's"); got != "'it''s'" {
		t.Errorf("QuoteStringLiteral = %s, want 'it''s'", got)
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	bad := []string{
		"../escape.db",
		"../../etc/passwd",
		"sub/child.db",
		`sub\child.db`,
		"/etc/passwd",
		"",
		"..",
	}
	for _, name := range bad {
		if _, err := safeJoin(dir, name); err == nil {
			t.Errorf("safeJoin(%q) = nil error, want error", name)
		}
	}
}

func TestSafeJoinAcceptsChild(t *testing.T) {
	dir := t.TempDir()
	got, err := safeJoin(dir, "app.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(dir, "app.db"); got != want {
		t.Errorf("safeJoin = %s, want %s", got, want)
	}
}
