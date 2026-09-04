// Package query executes ad-hoc SQL on behalf of an authenticated
// administrator and reports results, errors and timings.
package query

import "strings"

// SplitStatements divides a SQL script into individual statements.
//
// Splitting on ";" alone would break scripts containing semicolons inside
// string literals, comments or trigger bodies, so this walks the text tracking
// the lexical state SQLite itself recognises.
func SplitStatements(script string) []string {
	var (
		out        []string
		current    strings.Builder
		inSingle   bool // '...'
		inDouble   bool // "..."
		inBracket  bool // [...]
		inBacktick bool
		inLine     bool // -- ...
		inBlock    bool // /* ... */
		beginDepth int  // BEGIN ... END, as used in trigger bodies
	)

	runes := []rune(script)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}

		switch {
		case inLine:
			current.WriteRune(ch)
			if ch == '\n' {
				inLine = false
			}
			continue
		case inBlock:
			current.WriteRune(ch)
			if ch == '*' && next == '/' {
				current.WriteRune(next)
				i++
				inBlock = false
			}
			continue
		case inSingle:
			current.WriteRune(ch)
			if ch == '\'' {
				// A doubled quote is an escaped quote, not a terminator.
				if next == '\'' {
					current.WriteRune(next)
					i++
				} else {
					inSingle = false
				}
			}
			continue
		case inDouble:
			current.WriteRune(ch)
			if ch == '"' {
				if next == '"' {
					current.WriteRune(next)
					i++
				} else {
					inDouble = false
				}
			}
			continue
		case inBracket:
			current.WriteRune(ch)
			if ch == ']' {
				inBracket = false
			}
			continue
		case inBacktick:
			current.WriteRune(ch)
			if ch == '`' {
				inBacktick = false
			}
			continue
		}

		switch {
		case ch == '-' && next == '-':
			inLine = true
			current.WriteRune(ch)
		case ch == '/' && next == '*':
			inBlock = true
			current.WriteRune(ch)
		case ch == '\'':
			inSingle = true
			current.WriteRune(ch)
		case ch == '"':
			inDouble = true
			current.WriteRune(ch)
		case ch == '[':
			inBracket = true
			current.WriteRune(ch)
		case ch == '`':
			inBacktick = true
			current.WriteRune(ch)
		case ch == ';':
			// Inside a BEGIN...END block a semicolon separates the body's
			// statements rather than ending the outer one.
			if beginDepth > 0 {
				current.WriteRune(ch)
				continue
			}
			if stmt := strings.TrimSpace(current.String()); hasSQL(stmt) {
				out = append(out, stmt)
			}
			current.Reset()
		default:
			current.WriteRune(ch)
			if isWordBoundary(runes, i) {
				switch {
				case matchesKeyword(runes, i, "BEGIN"):
					// Only a trigger body's BEGIN nests; a transaction's does
					// not, and is followed by a statement separator.
					if isTriggerBody(current.String()) {
						beginDepth++
					}
				case matchesKeyword(runes, i, "END"):
					if beginDepth > 0 {
						beginDepth--
					}
				}
			}
		}
	}

	if stmt := strings.TrimSpace(current.String()); hasSQL(stmt) {
		out = append(out, stmt)
	}
	return out
}

// hasSQL reports whether a fragment contains anything executable, so that a
// trailing comment or a stray semicolon is not submitted to SQLite as a
// statement of its own.
func hasSQL(stmt string) bool {
	return stripLeadingNoise(stmt) != ""
}

// isWordBoundary reports whether position i starts a new word.
func isWordBoundary(runes []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := runes[i-1]
	return !isWordRune(prev)
}

func isWordRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// matchesKeyword reports whether the keyword starts at position i, as a whole
// word.
func matchesKeyword(runes []rune, i int, keyword string) bool {
	kw := []rune(keyword)
	if i+len(kw) > len(runes) {
		return false
	}
	for j, k := range kw {
		c := runes[i+j]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		if c != k {
			return false
		}
	}
	if i+len(kw) < len(runes) && isWordRune(runes[i+len(kw)]) {
		return false
	}
	return true
}

// isTriggerBody reports whether the statement so far is a CREATE TRIGGER, whose
// BEGIN opens a nested block.
func isTriggerBody(sofar string) bool {
	upper := strings.ToUpper(sofar)
	return strings.Contains(upper, "CREATE TRIGGER") ||
		strings.Contains(upper, "CREATE TEMP TRIGGER") ||
		strings.Contains(upper, "CREATE TEMPORARY TRIGGER")
}
