package query

import "strings"

// Kind classifies a statement so the server can decide which connection to run
// it on and what to report afterwards.
type Kind string

const (
	KindSelect Kind = "select" // returns rows
	KindWrite  Kind = "write"  // INSERT, UPDATE, DELETE
	KindDDL    Kind = "ddl"    // CREATE, ALTER, DROP
	KindPragma Kind = "pragma"
	KindOther  Kind = "other"
)

// readOnlyLeaders are the statement keywords that cannot modify data.
var readOnlyLeaders = map[string]bool{
	"SELECT": true, "WITH": true, "EXPLAIN": true, "VALUES": true,
}

var writeLeaders = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true, "UPSERT": true,
}

var ddlLeaders = map[string]bool{
	"CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true,
	"REINDEX": true, "VACUUM": true, "ANALYZE": true,
}

// Classify determines what a statement does.
//
// This drives presentation and reporting. It is deliberately not the security
// boundary: a read-only user's SQL runs on a mode=ro connection, so SQLite
// rejects writes even if this classification is wrong.
func Classify(stmt string) Kind {
	leader := leadingKeyword(stmt)
	switch {
	case readOnlyLeaders[leader]:
		// "WITH ... DELETE" and "EXPLAIN ..." can still write, but they run on
		// the writer pool anyway when the user holds write permission.
		if leader == "WITH" && containsWriteKeyword(stmt) {
			return KindWrite
		}
		return KindSelect
	case writeLeaders[leader]:
		return KindWrite
	case ddlLeaders[leader]:
		return KindDDL
	case leader == "PRAGMA":
		return KindPragma
	default:
		return KindOther
	}
}

// IsReadOnly reports whether a statement is certainly read-only.
//
// It errs toward reporting false: an unrecognised statement is treated as a
// write so it is never given the benefit of the doubt.
func IsReadOnly(stmt string) bool {
	switch Classify(stmt) {
	case KindSelect:
		return true
	case KindPragma:
		// A PRAGMA that assigns a value changes engine state.
		return !strings.Contains(stmt, "=")
	default:
		return false
	}
}

// ScriptIsReadOnly reports whether every statement in a script is read-only.
func ScriptIsReadOnly(script string) bool {
	stmts := SplitStatements(script)
	if len(stmts) == 0 {
		return true
	}
	for _, s := range stmts {
		if !IsReadOnly(s) {
			return false
		}
	}
	return true
}

func containsWriteKeyword(stmt string) bool {
	upper := strings.ToUpper(stmt)
	for kw := range writeLeaders {
		if strings.Contains(upper, kw+" ") {
			return true
		}
	}
	return false
}

// leadingKeyword returns the first keyword of a statement, skipping comments
// and whitespace so that a commented preamble does not hide the real verb.
func leadingKeyword(stmt string) string {
	s := stripLeadingNoise(stmt)
	end := 0
	for end < len(s) && isWordRune(rune(s[end])) {
		end++
	}
	return strings.ToUpper(s[:end])
}

func stripLeadingNoise(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n(")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
				continue
			}
			return ""
		}
		return s
	}
}
