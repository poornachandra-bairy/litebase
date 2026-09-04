package database

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
	"time"
)

// timeLayout is the storage format for timestamps in the metadata database.
// RFC3339 in UTC sorts lexicographically, which lets SQLite order by the raw
// text column without a conversion.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewID returns a random, URL-safe identifier for metadata records.
//
// 120 bits of entropy makes collisions negligible without needing a central
// sequence, which keeps inserts free of read-then-write races.
func NewID() string {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing means the system has no usable entropy source;
		// continuing would silently weaken every identifier and token.
		panic("litebase: cannot read random bytes: " + err.Error())
	}
	return strings.ToLower(idEncoding.EncodeToString(b))
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseTime reads a stored timestamp, tolerating the handful of layouts SQLite
// itself emits from functions like datetime('now').
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{timeLayout, time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// FormatTime and ParseTime expose the storage format to other packages so that
// timestamp handling stays consistent across the metadata schema.
func FormatTime(t time.Time) string { return formatTime(t) }
func ParseTime(s string) time.Time  { return parseTime(s) }

// NullableTime renders a zero time as SQL NULL.
func NullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}
