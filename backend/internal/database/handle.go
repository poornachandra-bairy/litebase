package database

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Handle is an open SQLite database with separate read and write pools.
//
// SQLite permits many concurrent readers but only one writer. Rather than let
// database/sql hand writes to arbitrary pooled connections and rely on
// SQLITE_BUSY retries, writes are funnelled through a single-connection pool.
// Readers then proceed in parallel against the WAL snapshot without ever
// blocking on the writer.
type Handle struct {
	name string
	path string

	writer *sql.DB
	reader *sql.DB

	// mu guards the reference count and closed flag, not the pools themselves;
	// *sql.DB is already safe for concurrent use.
	mu     sync.Mutex
	refs   int
	closed bool
}

// Name is the logical database name shown in the dashboard.
func (h *Handle) Name() string { return h.name }

// Path is the absolute location of the SQLite file.
func (h *Handle) Path() string { return h.path }

// Writer returns the serialised write pool. Use it for INSERT/UPDATE/DELETE,
// DDL, and any statement whose effect is unknown, such as ad-hoc SQL.
func (h *Handle) Writer() *sql.DB { return h.writer }

// Reader returns the concurrent read pool. Use it only for statements known to
// be read-only.
func (h *Handle) Reader() *sql.DB { return h.reader }

// Tx runs fn inside a write transaction, rolling back on error or panic.
func (h *Handle) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := h.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// Checkpoint folds the WAL back into the main database file. It is run before
// operations that read the database file directly, so that recent commits are
// not left behind in the WAL.
func (h *Handle) Checkpoint(ctx context.Context) error {
	// TRUNCATE waits for readers, then resets the WAL to zero length.
	_, err := h.writer.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	return nil
}

// acquire increments the reference count, keeping the handle alive while a
// request is using it.
func (h *Handle) acquire() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	h.refs++
	return nil
}

// Release drops a reference taken by Manager.Get. Every successful Get must be
// paired with exactly one Release.
func (h *Handle) Release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.refs > 0 {
		h.refs--
	}
}

func (h *Handle) inUse() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.refs > 0
}

// close shuts both pools down. Callers must ensure the handle is no longer
// referenced.
func (h *Handle) close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()

	// Checkpoint on the way out so the WAL does not linger next to an idle
	// database file.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = h.writer.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	cancel()

	errR := h.reader.Close()
	errW := h.writer.Close()
	if errW != nil {
		return errW
	}
	return errR
}

// openHandle opens the write and read pools for a database file.
func openHandle(ctx context.Context, name, path string, readerConns int, busy time.Duration) (*Handle, error) {
	writer, err := open(ctx, path, OpenOptions{MaxOpenConns: 1, BusyTimeout: busy})
	if err != nil {
		return nil, err
	}
	reader, err := open(ctx, path, OpenOptions{MaxOpenConns: readerConns, BusyTimeout: busy})
	if err != nil {
		writer.Close()
		return nil, err
	}
	return &Handle{name: name, path: path, writer: writer, reader: reader}, nil
}
