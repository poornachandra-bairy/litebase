package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// driverName is the pure-Go SQLite driver. It avoids cgo, which keeps the
// server a single statically linked binary that is trivial to ship to a VPS or
// into a scratch container.
const driverName = "sqlite"

// sqliteHeader is the magic string at the start of every SQLite database file.
var sqliteHeader = []byte("SQLite format 3\x00")

// OpenOptions controls how a SQLite file is opened.
type OpenOptions struct {
	ReadOnly bool
	// BusyTimeout is how long a blocked writer waits for the write lock before
	// returning SQLITE_BUSY.
	BusyTimeout time.Duration
	// MaxOpenConns caps the pool. Writers are serialised separately; see Handle.
	MaxOpenConns int
}

func (o OpenOptions) withDefaults() OpenOptions {
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = 10 * time.Second
	}
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = 8
	}
	return o
}

// buildDSN produces a driver DSN with the pragmas Litebase relies on.
//
// The pragmas are applied per connection by the driver, which matters because
// database/sql opens connections lazily: setting them once after Open would
// leave later connections with default settings.
func buildDSN(path string, o OpenOptions) string {
	v := url.Values{}
	add := func(p string) { v.Add("_pragma", p) }

	// WAL gives us concurrent readers alongside a single writer, which is what
	// a dashboard plus API traffic needs. It persists in the database file.
	if !o.ReadOnly {
		add("journal_mode(WAL)")
	}
	// NORMAL is the recommended durability level under WAL: safe against
	// application crashes, and only at risk from an OS-level power loss.
	add("synchronous(NORMAL)")
	add("foreign_keys(1)")
	add(fmt.Sprintf("busy_timeout(%d)", o.BusyTimeout.Milliseconds()))
	// Keep temp material in memory rather than writing it beside the database.
	add("temp_store(MEMORY)")

	if o.ReadOnly {
		v.Set("mode", "ro")
	}

	return "file:" + path + "?" + v.Encode()
}

// open opens a SQLite database file and verifies connectivity.
func open(ctx context.Context, path string, o OpenOptions) (*sql.DB, error) {
	o = o.withDefaults()

	db, err := sql.Open(driverName, buildDSN(path, o))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(o.MaxOpenConns)
	db.SetMaxIdleConns(o.MaxOpenConns)
	// SQLite connections are cheap and local; recycling them mainly bounds the
	// lifetime of any per-connection state.
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to sqlite: %w", err)
	}
	return db, nil
}

// IsSQLiteFile reports whether the file at path carries the SQLite header.
// Import paths are checked with this before a file is accepted, so that a
// corrupt or unrelated upload fails immediately with a clear message.
func IsSQLiteFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, len(sqliteHeader))
	n, err := f.Read(buf)
	if err != nil || n < len(sqliteHeader) {
		// Too short to be a database; not an error condition for the caller.
		return false, nil
	}
	return string(buf) == string(sqliteHeader), nil
}

// VerifySQLiteFile confirms a file is a readable SQLite database whose schema
// can be parsed. It guards imports and restores, where accepting a damaged file
// would surface as confusing failures later.
func VerifySQLiteFile(ctx context.Context, path string) error {
	ok, err := IsSQLiteFile(path)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	if !ok {
		return ErrNotSQLite
	}

	db, err := open(ctx, path, OpenOptions{ReadOnly: true, MaxOpenConns: 1})
	if err != nil {
		return ErrNotSQLite
	}
	defer db.Close()

	// Reading the schema forces SQLite to parse the file's header and b-tree
	// root pages, which catches truncation and gross corruption.
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema`).Scan(&n); err != nil {
		return fmt.Errorf("%w: %v", ErrNotSQLite, err)
	}
	return nil
}

// sidecarPaths returns the WAL and shared-memory files that accompany a
// database. They must be moved and removed alongside the main file.
func sidecarPaths(path string) []string {
	return []string{path + "-wal", path + "-shm", path + "-journal"}
}

// safeJoin joins name onto dir and confirms the result stays inside dir.
//
// Every filesystem path Litebase derives from user input passes through here.
// Rejecting separators outright stops "../" traversal and absolute-path
// escapes before they reach the filesystem.
func safeJoin(dir, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty path", ErrInvalidName)
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("%w: path must not contain separators or parent references", ErrInvalidName)
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("%w: path must be relative", ErrInvalidName)
	}

	cleanDir := filepath.Clean(dir)
	joined := filepath.Join(cleanDir, name)

	// Clean() plus a prefix check is enough here because name is already known
	// to contain no separators, so joined is always a direct child.
	if filepath.Dir(joined) != cleanDir {
		return "", fmt.Errorf("%w: path escapes the data directory", ErrInvalidName)
	}
	return joined, nil
}
