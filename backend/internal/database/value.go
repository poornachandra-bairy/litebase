package database

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
	"unicode/utf8"
)

// Converting between SQLite's storage classes and JSON is centralised here so
// that the row browser, the SQL editor and the generated API all present values
// identically.

// BlobValue wraps binary data so it survives a JSON round trip.
//
// Raw bytes are not valid JSON and are frequently not valid UTF-8, so blobs are
// base64 encoded and tagged, which also lets the dashboard render them as
// binary rather than as mangled text.
type BlobValue struct {
	Base64 string `json:"base64"`
	Size   int    `json:"size"`
}

// MarshalJSON tags the value so a client can tell a blob from a string.
func (b BlobValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type   string `json:"$type"`
		Base64 string `json:"base64"`
		Size   int    `json:"size"`
	}{Type: "blob", Base64: b.Base64, Size: b.Size})
}

// ScanTargets allocates generic scan destinations for a result row.
func ScanTargets(n int) ([]any, []any) {
	values := make([]any, n)
	targets := make([]any, n)
	for i := range values {
		targets[i] = &values[i]
	}
	return values, targets
}

// NormalizeValue converts a driver value into something JSON can represent.
//
// The driver returns SQLite's five storage classes as []byte, string, int64,
// float64 or nil. Anything unexpected is rendered as a string rather than
// failing the whole query.
func NormalizeValue(v any) any {
	switch val := v.(type) {
	case nil:
		return nil
	case []byte:
		// Text and blobs both arrive as []byte. Valid UTF-8 is far more likely
		// to be text the user wants to read, so only genuinely binary data is
		// base64 encoded.
		if utf8.Valid(val) {
			return string(val)
		}
		return BlobValue{
			Base64: base64.StdEncoding.EncodeToString(val),
			Size:   len(val),
		}
	case string:
		return val
	case int64:
		return val
	case float64:
		// JSON cannot represent these, and encoding/json would error out on
		// the whole response.
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return nil
		}
		return val
	case bool:
		return val
	case time.Time:
		// Some pragma results come back as timestamps rather than text.
		return val.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// BindValue converts a decoded JSON value into a parameter SQLite accepts.
//
// Every value bound into a statement passes through here, which is what makes
// user data incapable of altering a query: it travels as a parameter, never as
// SQL text.
func BindValue(v any) (any, error) {
	switch val := v.(type) {
	case nil:
		return nil, nil
	case string:
		return val, nil
	case bool:
		return val, nil
	case float64:
		// JSON numbers decode as float64; store whole numbers as integers so
		// they land in an INTEGER column with the expected affinity.
		if val == math.Trunc(val) && math.Abs(val) <= float64(math.MaxInt64) {
			return int64(val), nil
		}
		return val, nil
	case int64:
		return val, nil
	case int:
		return int64(val), nil
	case time.Time:
		return val.UTC().Format(time.RFC3339Nano), nil
	case json.Number:
		if i, err := val.Int64(); err == nil {
			return i, nil
		}
		f, err := val.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", val.String())
		}
		return f, nil
	case map[string]any:
		// The tagged blob form produced by BlobValue, sent back on an update.
		if t, _ := val["$type"].(string); t == "blob" {
			enc, _ := val["base64"].(string)
			raw, err := base64.StdEncoding.DecodeString(enc)
			if err != nil {
				return nil, fmt.Errorf("invalid base64 blob value")
			}
			return raw, nil
		}
		// Any other object is stored as JSON text, which is how SQLite's own
		// JSON functions expect to find it.
		encoded, err := json.Marshal(val)
		if err != nil {
			return nil, fmt.Errorf("value cannot be stored: %w", err)
		}
		return string(encoded), nil
	case []any:
		encoded, err := json.Marshal(val)
		if err != nil {
			return nil, fmt.Errorf("value cannot be stored: %w", err)
		}
		return string(encoded), nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", v)
	}
}

// CoerceToColumnType converts a bound value to match a column's declared type.
//
// SQLite would accept a mismatch and store it under a different storage class,
// which then confuses sorting and comparison, so numeric columns get an
// explicit conversion from string input.
func CoerceToColumnType(v any, declaredType string) (any, error) {
	if v == nil {
		return nil, nil
	}
	s, isString := v.(string)
	if !isString {
		return v, nil
	}
	switch NormalizeType(declaredType) {
	case "INTEGER":
		if s == "" {
			return nil, nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid integer", s)
		}
		return n, nil
	case "REAL":
		if s == "" {
			return nil, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid number", s)
		}
		return f, nil
	default:
		return v, nil
	}
}

// NormalizeType reduces a declared column type to a SQLite affinity, following
// the rules in https://sqlite.org/datatype3.html.
func NormalizeType(declared string) string {
	upper := ""
	for _, r := range declared {
		if r >= 'a' && r <= 'z' {
			r -= 32
		}
		upper += string(r)
	}
	switch {
	case contains(upper, "INT"):
		return "INTEGER"
	case contains(upper, "CHAR"), contains(upper, "CLOB"), contains(upper, "TEXT"):
		return "TEXT"
	case contains(upper, "BLOB"), upper == "":
		return "BLOB"
	case contains(upper, "REAL"), contains(upper, "FLOA"), contains(upper, "DOUB"):
		return "REAL"
	default:
		return "NUMERIC"
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// NullString renders a nullable string column as a Go string.
func NullString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}
