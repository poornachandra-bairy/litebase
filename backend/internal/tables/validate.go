package tables

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/litebase/litebase/internal/database"
)

// Column types, defaults and foreign key actions all end up as SQL text inside
// a DDL statement, where they cannot be bound as parameters. Each is therefore
// matched against a strict whitelist here; anything unrecognised is rejected
// rather than escaped, so no input can extend a statement.

// allowedTypes are the type names offered by the dashboard. SQLite applies
// affinity rules rather than strict typing, so this list is about giving
// operators a predictable, documented set rather than a limitation of SQLite.
var allowedTypes = map[string]bool{
	"INTEGER": true, "INT": true, "BIGINT": true, "SMALLINT": true, "TINYINT": true,
	"REAL": true, "DOUBLE": true, "FLOAT": true,
	"NUMERIC": true, "DECIMAL": true,
	"TEXT": true, "VARCHAR": true, "CHAR": true, "CLOB": true,
	"BLOB":    true,
	"BOOLEAN": true,
	"DATE":    true, "DATETIME": true, "TIMESTAMP": true,
	"JSON": true,
}

// typePattern splits a declared type into a base name and an optional
// precision such as VARCHAR(255) or DECIMAL(10,2).
var typePattern = regexp.MustCompile(`^([A-Za-z]+)(?:\s*\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\))?$`)

// ValidateColumnType checks a declared type and returns its canonical form.
func ValidateColumnType(t string) (string, error) {
	trimmed := strings.TrimSpace(t)
	if trimmed == "" {
		// SQLite permits a column with no declared type (BLOB affinity), but
		// leaving it implicit tends to confuse rather than help.
		return "TEXT", nil
	}
	if len(trimmed) > 64 {
		return "", fmt.Errorf("column type %q is too long", t)
	}

	m := typePattern.FindStringSubmatch(trimmed)
	if m == nil {
		return "", fmt.Errorf("column type %q is not a supported SQLite type", t)
	}
	base := strings.ToUpper(m[1])
	if !allowedTypes[base] {
		return "", fmt.Errorf("column type %q is not supported; use one of %s",
			t, strings.Join(SupportedTypes(), ", "))
	}

	switch {
	case m[3] != "":
		return fmt.Sprintf("%s(%s,%s)", base, m[2], m[3]), nil
	case m[2] != "":
		return fmt.Sprintf("%s(%s)", base, m[2]), nil
	default:
		return base, nil
	}
}

// SupportedTypes lists the accepted base type names, for error messages and
// for the dashboard's type picker.
func SupportedTypes() []string {
	return []string{
		"INTEGER", "REAL", "TEXT", "BLOB", "NUMERIC", "BOOLEAN",
		"DATE", "DATETIME", "TIMESTAMP", "JSON",
		"VARCHAR", "CHAR", "DECIMAL", "BIGINT", "DOUBLE",
	}
}

var (
	numericLiteral = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][+-]?\d+)?$`)
	stringLiteral  = regexp.MustCompile(`^'([^']|'')*'$`)
	blobLiteral    = regexp.MustCompile(`^[xX]'([0-9a-fA-F]{2})*'$`)
)

// allowedDefaultKeywords are the only bare expressions permitted as defaults.
// Arbitrary expressions are refused because they would be executed verbatim.
var allowedDefaultKeywords = map[string]bool{
	"NULL": true, "TRUE": true, "FALSE": true,
	"CURRENT_TIME": true, "CURRENT_DATE": true, "CURRENT_TIMESTAMP": true,
}

// ValidateDefault checks a DEFAULT clause and returns its canonical SQL text.
//
// Accepted forms are a numeric literal, a single-quoted string literal, a blob
// literal, and the keywords NULL, TRUE, FALSE and CURRENT_TIME/DATE/TIMESTAMP.
// Anything else, including function calls, is rejected.
func ValidateDefault(expr string) (string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > 512 {
		return "", fmt.Errorf("default value is too long")
	}

	if upper := strings.ToUpper(trimmed); allowedDefaultKeywords[upper] {
		return upper, nil
	}
	switch {
	case numericLiteral.MatchString(trimmed):
		return trimmed, nil
	case stringLiteral.MatchString(trimmed):
		return trimmed, nil
	case blobLiteral.MatchString(trimmed):
		return trimmed, nil
	}
	return "", fmt.Errorf(
		"default %q is not allowed; use a number, a quoted string such as 'pending', "+
			"a blob literal, or one of NULL, TRUE, FALSE, CURRENT_TIMESTAMP, CURRENT_DATE, CURRENT_TIME",
		expr)
}

// fkActions are the referential actions SQLite understands.
var fkActions = map[string]string{
	"":            "",
	"NO ACTION":   "NO ACTION",
	"RESTRICT":    "RESTRICT",
	"SET NULL":    "SET NULL",
	"SET DEFAULT": "SET DEFAULT",
	"CASCADE":     "CASCADE",
}

// ValidateFKAction checks a referential action and returns its canonical form.
func ValidateFKAction(a string) (string, error) {
	canonical, ok := fkActions[strings.ToUpper(strings.TrimSpace(a))]
	if !ok {
		return "", fmt.Errorf("foreign key action %q is not valid; use NO ACTION, RESTRICT, SET NULL, SET DEFAULT or CASCADE", a)
	}
	return canonical, nil
}

// validateColumn normalises a column definition in place.
func validateColumn(c *Column) error {
	if err := database.ValidateIdentifier(c.Name); err != nil {
		return fmt.Errorf("column name: %w", err)
	}
	t, err := ValidateColumnType(c.Type)
	if err != nil {
		return err
	}
	c.Type = t

	if c.Default != nil {
		d, err := ValidateDefault(*c.Default)
		if err != nil {
			return fmt.Errorf("column %q: %w", c.Name, err)
		}
		if d == "" {
			c.Default = nil
		} else {
			c.Default = &d
		}
	}

	// AUTOINCREMENT is only legal on an INTEGER PRIMARY KEY, and SQLite would
	// otherwise fail with a message that does not explain why.
	if c.AutoIncrement {
		if !c.PrimaryKey {
			return fmt.Errorf("column %q: AUTOINCREMENT requires the column to be the primary key", c.Name)
		}
		if strings.ToUpper(c.Type) != "INTEGER" {
			return fmt.Errorf("column %q: AUTOINCREMENT requires type INTEGER, not %s", c.Name, c.Type)
		}
	}

	if c.References != nil {
		if err := database.ValidateIdentifier(c.References.Table); err != nil {
			return fmt.Errorf("column %q references an invalid table name: %w", c.Name, err)
		}
		if c.References.Column != "" {
			if err := database.ValidateIdentifier(c.References.Column); err != nil {
				return fmt.Errorf("column %q references an invalid column name: %w", c.Name, err)
			}
		}
		if c.References.OnDelete, err = ValidateFKAction(c.References.OnDelete); err != nil {
			return fmt.Errorf("column %q: %w", c.Name, err)
		}
		if c.References.OnUpdate, err = ValidateFKAction(c.References.OnUpdate); err != nil {
			return fmt.Errorf("column %q: %w", c.Name, err)
		}
	}
	return nil
}
