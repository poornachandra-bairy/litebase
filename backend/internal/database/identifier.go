package database

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SQLite identifiers (table, column, index and trigger names) cannot be bound
// as statement parameters, so they must be validated and quoted instead. This
// file is the single place where identifiers become SQL text; every DDL and DML
// builder in the codebase routes through QuoteIdent so that no caller can
// accidentally interpolate raw user input.

// MaxIdentifierLen bounds identifier length to keep schemas and error messages
// manageable. SQLite itself has no such limit.
const MaxIdentifierLen = 128

// reservedPrefixes are namespaces SQLite reserves for its own bookkeeping.
var reservedPrefixes = []string{"sqlite_"}

// ValidateIdentifier reports whether name is acceptable as a user-supplied
// SQLite identifier.
//
// The rules are deliberately stricter than SQLite's: the first rune must be a
// letter or underscore and the rest must be letters, digits or underscores.
// This rejects quotes, semicolons, whitespace and every other character that
// could alter the shape of a statement, which means a validated identifier is
// safe to embed once QuoteIdent has wrapped it.
func ValidateIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalidName)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: name must be valid UTF-8", ErrInvalidName)
	}
	if len(name) > MaxIdentifierLen {
		return fmt.Errorf("%w: name must be at most %d characters", ErrInvalidName, MaxIdentifierLen)
	}
	for i, r := range name {
		switch {
		case r == '_':
		case unicode.IsLetter(r) && r < unicode.MaxASCII:
		case i > 0 && r >= '0' && r <= '9':
		default:
			return fmt.Errorf("%w: %q contains an unsupported character; use letters, digits and underscores, and do not start with a digit", ErrInvalidName, name)
		}
	}
	lower := strings.ToLower(name)
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(lower, p) {
			return fmt.Errorf("%w: names starting with %q are reserved by SQLite", ErrInvalidName, p)
		}
	}
	return nil
}

// QuoteIdent validates and double-quotes an identifier for use in SQL text.
// It returns an error rather than a best-effort string so that a validation
// failure can never be ignored into an injectable query.
func QuoteIdent(name string) (string, error) {
	if err := ValidateIdentifier(name); err != nil {
		return "", err
	}
	// Escaping embedded quotes is redundant given validation, but it is kept as
	// defence in depth in case the rules above are ever relaxed.
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
}

// MustQuoteIdent is QuoteIdent for identifiers that came from SQLite's own
// schema tables rather than from a user. It falls back to plain quoting rather
// than panicking, because SQLite permits names our validator rejects.
func MustQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QuoteStringLiteral renders a Go string as a SQL string literal. It exists for
// the few statements where SQLite forbids bound parameters, such as
// "VACUUM INTO". Callers must still validate the underlying value; for file
// paths that means confirming the path is inside a permitted directory.
func QuoteStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
