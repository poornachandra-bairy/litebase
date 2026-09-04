package api

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/query"
)

// ReservedPathPrefix is the namespace the dashboard's own API occupies.
// A generated endpoint may not claim a path underneath it, or it could shadow
// an administrative route.
const ReservedPathPrefix = "/admin"

var (
	pathSegmentRe = regexp.MustCompile(`^[A-Za-z0-9_.~-]+$`)
	paramNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	pathParamRe   = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
)

var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// maxSQLLength bounds a stored statement so the editor cannot be used to park
// unbounded text in the metadata database.
const maxSQLLength = 20000

// ValidatePath checks an endpoint path and returns its canonical form.
//
// Paths are restricted to simple segments and {name} placeholders. Rejecting
// "." and ".." here means a generated route can never be used to walk outside
// its namespace, and rejecting anything outside the character class keeps the
// path from carrying a query string or fragment.
func ValidatePath(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" {
		return "", fmt.Errorf("path must name at least one segment")
	}
	if len(trimmed) > 256 {
		return "", fmt.Errorf("path is too long")
	}
	if strings.Contains(trimmed, "//") {
		return "", fmt.Errorf("path must not contain empty segments")
	}
	if strings.ContainsAny(trimmed, "?#\\ ") {
		return "", fmt.Errorf("path must not contain spaces, '?', '#' or '\\'")
	}

	seen := map[string]bool{}
	for _, seg := range strings.Split(strings.TrimPrefix(trimmed, "/"), "/") {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("path must not contain '.' or '..' segments")
		}
		if m := pathParamRe.FindStringSubmatch(seg); m != nil {
			if seen[m[1]] {
				return "", fmt.Errorf("path parameter {%s} appears more than once", m[1])
			}
			seen[m[1]] = true
			continue
		}
		if !pathSegmentRe.MatchString(seg) {
			return "", fmt.Errorf("path segment %q may only contain letters, digits, '-', '_', '.' and '~', or be a {parameter}", seg)
		}
	}

	lower := strings.ToLower(trimmed)
	if lower == ReservedPathPrefix || strings.HasPrefix(lower, ReservedPathPrefix+"/") {
		return "", fmt.Errorf("path %q is reserved for the dashboard API", trimmed)
	}
	return trimmed, nil
}

// PathParams returns the parameter names a path declares.
func PathParams(p string) []string {
	out := []string{}
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if m := pathParamRe.FindStringSubmatch(seg); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// ValidateMethod checks an HTTP method.
func ValidateMethod(m string) (string, error) {
	upper := strings.ToUpper(strings.TrimSpace(m))
	if !allowedMethods[upper] {
		return "", fmt.Errorf("method %q is not supported; use GET, POST, PUT, PATCH or DELETE", m)
	}
	return upper, nil
}

// CountPlaceholders counts the "?" bind markers in a statement, ignoring any
// that appear inside string literals, quoted identifiers or comments.
func CountPlaceholders(sqlText string) int {
	var (
		count                                          int
		inSingle, inDouble, inBracket, inLine, inBlock bool
	)
	runes := []rune(sqlText)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}

		switch {
		case inLine:
			if ch == '\n' {
				inLine = false
			}
		case inBlock:
			if ch == '*' && next == '/' {
				inBlock = false
				i++
			}
		case inSingle:
			if ch == '\'' {
				if next == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if ch == '"' {
				if next == '"' {
					i++
				} else {
					inDouble = false
				}
			}
		case inBracket:
			if ch == ']' {
				inBracket = false
			}
		case ch == '-' && next == '-':
			inLine = true
			i++
		case ch == '/' && next == '*':
			inBlock = true
			i++
		case ch == '\'':
			inSingle = true
		case ch == '"':
			inDouble = true
		case ch == '[':
			inBracket = true
		case ch == '?':
			count++
		}
	}
	return count
}

// hasNamedParameters reports whether a statement uses SQLite's named parameter
// syntax, which this builder does not support.
func hasNamedParameters(sqlText string) bool {
	// Look for :name, @name or $name outside of literals. A cast such as
	// "x::text" is not valid SQLite, so a bare ':' is a parameter marker.
	stripped := stripLiterals(sqlText)
	for _, marker := range []string{":", "@", "$"} {
		idx := strings.Index(stripped, marker)
		for idx >= 0 {
			rest := stripped[idx+1:]
			if rest != "" && (isLetter(rune(rest[0])) || rest[0] == '_') {
				return true
			}
			nextIdx := strings.Index(stripped[idx+1:], marker)
			if nextIdx < 0 {
				break
			}
			idx = idx + 1 + nextIdx
		}
	}
	return false
}

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// stripLiterals blanks out string literals and comments so that scanning for
// syntax does not trip over their contents.
func stripLiterals(s string) string {
	var b strings.Builder
	var inSingle, inDouble, inLine, inBlock bool
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case inLine:
			if ch == '\n' {
				inLine = false
				b.WriteRune(ch)
			}
		case inBlock:
			if ch == '*' && next == '/' {
				inBlock = false
				i++
			}
		case inSingle:
			if ch == '\'' {
				inSingle = false
			}
		case inDouble:
			if ch == '"' {
				inDouble = false
			}
		case ch == '-' && next == '-':
			inLine = true
			i++
		case ch == '/' && next == '*':
			inBlock = true
			i++
		case ch == '\'':
			inSingle = true
		case ch == '"':
			inDouble = true
		default:
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// ValidateSQL checks a custom query statement.
func ValidateSQL(sqlText string, params []Param) error {
	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		return fmt.Errorf("SQL is required for a query endpoint")
	}
	if len(trimmed) > maxSQLLength {
		return fmt.Errorf("SQL is too long; the maximum is %d characters", maxSQLLength)
	}

	// One statement only. Allowing several would let a single endpoint perform
	// unrelated work, and makes the parameter mapping ambiguous.
	statements := query.SplitStatements(trimmed)
	if len(statements) == 0 {
		return fmt.Errorf("SQL is required for a query endpoint")
	}
	if len(statements) > 1 {
		return fmt.Errorf("an endpoint must contain exactly one SQL statement, but %d were found", len(statements))
	}

	if hasNamedParameters(statements[0]) {
		return fmt.Errorf("use positional '?' placeholders rather than named parameters")
	}

	// The placeholder count must match the declared parameters exactly, or
	// values would bind to the wrong positions at runtime.
	placeholders := CountPlaceholders(statements[0])
	if placeholders != len(params) {
		return fmt.Errorf(
			"the statement has %d '?' placeholder(s) but %d parameter(s) are declared; they must match, in order",
			placeholders, len(params))
	}
	return nil
}

// ValidateParams checks a parameter list.
func ValidateParams(params []Param, path string) error {
	if len(params) > 50 {
		return fmt.Errorf("an endpoint may declare at most 50 parameters")
	}

	declaredInPath := map[string]bool{}
	for _, name := range PathParams(path) {
		declaredInPath[name] = true
	}

	seen := map[string]bool{}
	for i := range params {
		p := &params[i]
		p.Name = strings.TrimSpace(p.Name)
		if !paramNameRe.MatchString(p.Name) {
			return fmt.Errorf("parameter name %q must start with a letter or underscore and contain only letters, digits and underscores", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("parameter %q is declared more than once", p.Name)
		}
		seen[p.Name] = true

		switch p.In {
		case InQuery, InBody:
		case InPath:
			if !declaredInPath[p.Name] {
				return fmt.Errorf("parameter %q is declared as a path parameter but {%s} does not appear in the path", p.Name, p.Name)
			}
		case "":
			p.In = InQuery
		default:
			return fmt.Errorf("parameter %q has an unknown location %q; use query, path or body", p.Name, p.In)
		}

		switch p.Type {
		case TypeString, TypeInteger, TypeNumber, TypeBoolean:
		case "":
			p.Type = TypeString
		default:
			return fmt.Errorf("parameter %q has an unknown type %q; use string, integer, number or boolean", p.Name, p.Type)
		}

		// A path parameter is always present, so marking it optional would be
		// misleading.
		if p.In == InPath {
			p.Required = true
		}
		if p.Pattern != "" {
			if len(p.Pattern) > 200 {
				return fmt.Errorf("parameter %q has an over-long pattern", p.Name)
			}
			if _, err := regexp.Compile(p.Pattern); err != nil {
				return fmt.Errorf("parameter %q has an invalid pattern: %v", p.Name, err)
			}
		}
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			return fmt.Errorf("parameter %q has a minimum greater than its maximum", p.Name)
		}
		if p.MinLength != nil && *p.MinLength < 0 {
			return fmt.Errorf("parameter %q has a negative minimum length", p.Name)
		}
		if p.MinLength != nil && p.MaxLength != nil && *p.MinLength > *p.MaxLength {
			return fmt.Errorf("parameter %q has a minimum length greater than its maximum", p.Name)
		}
	}

	// Every {placeholder} in the path must have a declaration, or the route
	// would capture a value nothing consumes.
	for name := range declaredInPath {
		if !seen[name] {
			return fmt.Errorf("the path declares {%s} but no matching parameter is defined", name)
		}
	}
	return nil
}

// Validate checks a whole endpoint definition and normalises it in place.
func (e *Endpoint) Validate() error {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" || len(e.Name) > 100 {
		return fmt.Errorf("name must be between 1 and 100 characters")
	}
	if len(e.Description) > 1000 {
		return fmt.Errorf("description must be at most 1000 characters")
	}

	path, err := ValidatePath(e.Path)
	if err != nil {
		return err
	}
	e.Path = path

	if e.RateLimit < 0 {
		return fmt.Errorf("rate limit must not be negative")
	}
	if e.MaxRows < 0 {
		return fmt.Errorf("max rows must not be negative")
	}

	switch e.Kind {
	case KindQuery:
		if e.Method, err = ValidateMethod(e.Method); err != nil {
			return err
		}
		if err := ValidateParams(e.Params, e.Path); err != nil {
			return err
		}
		if err := ValidateSQL(e.SQL, e.Params); err != nil {
			return err
		}
		e.Table = ""
		e.Config = CRUDConfig{}

	case KindCRUD:
		if err := database.ValidateIdentifier(e.Table); err != nil {
			return fmt.Errorf("table: %w", err)
		}
		// A CRUD resource owns a fixed set of routes, so the base path must not
		// itself contain parameters.
		if len(PathParams(e.Path)) > 0 {
			return fmt.Errorf("a CRUD endpoint's path must not contain {parameters}; the item routes are generated automatically")
		}
		if len(e.Config.Operations) == 0 {
			e.Config.Operations = AllCRUDOperations()
		}
		valid := map[CRUDOperation]bool{}
		for _, op := range AllCRUDOperations() {
			valid[op] = true
		}
		for _, op := range e.Config.Operations {
			if !valid[op] {
				return fmt.Errorf("unknown operation %q; use list, read, create, update or delete", op)
			}
		}
		for _, col := range append(append([]string{}, e.Config.ReadableColumns...), e.Config.WritableColumns...) {
			if err := database.ValidateIdentifier(col); err != nil {
				return fmt.Errorf("column %q: %w", col, err)
			}
		}
		if e.Config.DefaultLimit <= 0 {
			e.Config.DefaultLimit = 50
		}
		if e.Config.MaxLimit <= 0 {
			e.Config.MaxLimit = 200
		}
		if e.Config.DefaultLimit > e.Config.MaxLimit {
			return fmt.Errorf("the default limit must not exceed the maximum limit")
		}
		e.SQL = ""
		e.Params = nil
		e.Method = ""

	default:
		return fmt.Errorf("unknown endpoint kind %q; use crud or query", e.Kind)
	}
	return nil
}
