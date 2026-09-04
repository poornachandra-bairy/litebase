package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Meta describes a user database as recorded in the metadata store.
type Meta struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Filename    string    `json:"filename"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	SizeBytes   int64     `json:"size_bytes"`
}

// Manager owns the lifecycle of every SQLite database the server manages: the
// internal metadata database and the user databases beneath the data
// directory.
//
// It is the only component that maps a logical database name onto a filesystem
// path, which keeps path handling auditable in one place.
type Manager struct {
	dir         string
	maxOpen     int
	readerConns int
	busyTimeout time.Duration

	meta *Handle

	mu   sync.Mutex
	open map[string]*Handle
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	DatabasesDir string
	MetaPath     string
	MaxOpen      int
	ReaderConns  int
	BusyTimeout  time.Duration
}

// NewManager opens the metadata database, applies migrations and prepares the
// user-database directory.
func NewManager(ctx context.Context, o ManagerOptions) (*Manager, error) {
	if o.MaxOpen <= 0 {
		o.MaxOpen = 64
	}
	if o.ReaderConns <= 0 {
		o.ReaderConns = 4
	}
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = 10 * time.Second
	}
	if err := os.MkdirAll(o.DatabasesDir, 0o700); err != nil {
		return nil, fmt.Errorf("create databases dir: %w", err)
	}

	meta, err := openHandle(ctx, "_meta", o.MetaPath, o.ReaderConns, o.BusyTimeout)
	if err != nil {
		return nil, fmt.Errorf("open metadata database: %w", err)
	}
	if err := migrate(ctx, meta.Writer()); err != nil {
		meta.close()
		return nil, fmt.Errorf("migrate metadata database: %w", err)
	}

	return &Manager{
		dir:         o.DatabasesDir,
		maxOpen:     o.MaxOpen,
		readerConns: o.ReaderConns,
		busyTimeout: o.BusyTimeout,
		meta:        meta,
		open:        make(map[string]*Handle),
	}, nil
}

// Meta exposes the internal metadata database. Application state (users,
// sessions, endpoint definitions) lives here, never in a user database.
func (m *Manager) Meta() *Handle { return m.meta }

// MetaDB is shorthand for the metadata write pool.
func (m *Manager) MetaDB() *sql.DB { return m.meta.Writer() }

// MetaRead is shorthand for the metadata read pool.
func (m *Manager) MetaRead() *sql.DB { return m.meta.Reader() }

// Close releases every open database. It is safe to call more than once.
func (m *Manager) Close() error {
	m.mu.Lock()
	handles := make([]*Handle, 0, len(m.open))
	for _, h := range m.open {
		handles = append(handles, h)
	}
	m.open = make(map[string]*Handle)
	m.mu.Unlock()

	var firstErr error
	for _, h := range handles {
		if err := h.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := m.meta.close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// pathFor maps a database name onto its file, refusing anything that would
// escape the databases directory.
func (m *Manager) pathFor(name string) (string, error) {
	if err := ValidateIdentifier(name); err != nil {
		return "", err
	}
	return safeJoin(m.dir, name+".db")
}

// Get returns an open handle for name, opening it on first use.
//
// The caller owns a reference and must call Release when finished; the manager
// will not close a handle that is still referenced.
func (m *Manager) Get(ctx context.Context, name string) (*Handle, error) {
	path, err := m.pathFor(name)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if h, ok := m.open[name]; ok {
		m.mu.Unlock()
		if err := h.acquire(); err != nil {
			// Raced with a close; fall through and reopen.
			return m.reopen(ctx, name, path)
		}
		return h, nil
	}
	m.mu.Unlock()

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: database %q", ErrNotFound, name)
		}
		return nil, err
	}
	return m.reopen(ctx, name, path)
}

func (m *Manager) reopen(ctx context.Context, name, path string) (*Handle, error) {
	h, err := openHandle(ctx, name, path, m.readerConns, m.busyTimeout)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	// Another goroutine may have opened the same database while we were
	// waiting on the filesystem; prefer the existing handle so a database is
	// never backed by two independent pools.
	if existing, ok := m.open[name]; ok {
		m.mu.Unlock()
		h.close()
		if err := existing.acquire(); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if len(m.open) >= m.maxOpen {
		m.evictIdleLocked()
	}
	if len(m.open) >= m.maxOpen {
		m.mu.Unlock()
		h.close()
		return nil, fmt.Errorf("%w: limit is %d", ErrTooManyOpen, m.maxOpen)
	}
	m.open[name] = h
	_ = h.acquire()
	m.mu.Unlock()
	return h, nil
}

// evictIdleLocked closes handles with no outstanding references. The caller
// must hold m.mu.
func (m *Manager) evictIdleLocked() {
	for name, h := range m.open {
		if !h.inUse() {
			delete(m.open, name)
			go h.close()
		}
	}
}

// closeHandle removes a database from the pool and closes it, waiting briefly
// for in-flight requests to finish. Used before rename, delete and restore,
// where the file must not be open.
func (m *Manager) closeHandle(ctx context.Context, name string) error {
	m.mu.Lock()
	h, ok := m.open[name]
	if ok {
		delete(m.open, name)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}

	deadline := time.Now().Add(10 * time.Second)
	for h.inUse() && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return h.close()
}

// Create makes a new empty database file and registers it.
func (m *Manager) Create(ctx context.Context, name, description string) (*Meta, error) {
	path, err := m.pathFor(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%w: database %q", ErrAlreadyExists, name)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// Opening a non-existent path creates the file; the write forces SQLite to
	// lay down a valid header rather than leaving a zero-length file behind.
	h, err := openHandle(ctx, name, path, 1, m.busyTimeout)
	if err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	_, execErr := h.Writer().ExecContext(ctx, `PRAGMA user_version = 0`)
	closeErr := h.close()
	if execErr != nil {
		os.Remove(path)
		return nil, fmt.Errorf("initialise database: %w", execErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}

	meta, err := m.registerDatabase(ctx, name, filepath.Base(path), description)
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	return meta, nil
}

// ImportFile registers an existing SQLite file as a new database. The source is
// verified before it is moved into place, so a bad upload never becomes a
// half-registered database.
func (m *Manager) ImportFile(ctx context.Context, name, description, srcPath string) (*Meta, error) {
	dstPath, err := m.pathFor(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dstPath); err == nil {
		return nil, fmt.Errorf("%w: database %q", ErrAlreadyExists, name)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := VerifySQLiteFile(ctx, srcPath); err != nil {
		return nil, err
	}
	if err := copyFile(srcPath, dstPath); err != nil {
		return nil, fmt.Errorf("install database file: %w", err)
	}

	meta, err := m.registerDatabase(ctx, name, filepath.Base(dstPath), description)
	if err != nil {
		os.Remove(dstPath)
		return nil, err
	}
	return meta, nil
}

func (m *Manager) registerDatabase(ctx context.Context, name, filename, description string) (*Meta, error) {
	now := time.Now().UTC()
	id := NewID()
	_, err := m.MetaDB().ExecContext(ctx,
		`INSERT INTO databases (id, name, filename, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, filename, description, formatTime(now), formatTime(now))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: database %q", ErrAlreadyExists, name)
		}
		return nil, fmt.Errorf("register database: %w", err)
	}
	return &Meta{ID: id, Name: name, Filename: filename, Description: description,
		CreatedAt: now, UpdatedAt: now}, nil
}

// Rename moves a database and its WAL sidecars to a new name.
func (m *Manager) Rename(ctx context.Context, oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	oldPath, err := m.pathFor(oldName)
	if err != nil {
		return err
	}
	newPath, err := m.pathFor(newName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(oldPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: database %q", ErrNotFound, oldName)
		}
		return err
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("%w: database %q", ErrAlreadyExists, newName)
	} else if !os.IsNotExist(err) {
		return err
	}

	// Update metadata first: if the rename fails afterwards the transaction is
	// rolled back, whereas a successful move with failed bookkeeping would
	// leave an unreachable file.
	tx, err := m.MetaDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE databases SET name = ?, filename = ?, updated_at = ? WHERE name = ?`,
		newName, filepath.Base(newPath), formatTime(time.Now().UTC()), oldName)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: database %q", ErrAlreadyExists, newName)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: database %q", ErrNotFound, oldName)
	}

	// The file must be closed before it moves, or readers would keep operating
	// on the old inode.
	if err := m.closeHandle(ctx, oldName); err != nil {
		return err
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename database file: %w", err)
	}
	for i, side := range sidecarPaths(oldPath) {
		if _, err := os.Stat(side); err == nil {
			_ = os.Rename(side, sidecarPaths(newPath)[i])
		}
	}
	return tx.Commit()
}

// Delete removes a database file, its sidecars and its metadata.
func (m *Manager) Delete(ctx context.Context, name string) error {
	path, err := m.pathFor(name)
	if err != nil {
		return err
	}
	res, err := m.MetaDB().ExecContext(ctx, `DELETE FROM databases WHERE name = ?`, name)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()

	if err := m.closeHandle(ctx, name); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete database file: %w", err)
	}
	for _, side := range sidecarPaths(path) {
		_ = os.Remove(side)
	}
	if affected == 0 {
		return fmt.Errorf("%w: database %q", ErrNotFound, name)
	}
	return nil
}

// List returns registered databases with their on-disk sizes.
func (m *Manager) List(ctx context.Context) ([]Meta, error) {
	rows, err := m.MetaRead().QueryContext(ctx,
		`SELECT id, name, filename, description, created_at, updated_at
		 FROM databases ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Meta{}
	for rows.Next() {
		var d Meta
		var created, updated string
		if err := rows.Scan(&d.ID, &d.Name, &d.Filename, &d.Description, &created, &updated); err != nil {
			return nil, err
		}
		d.CreatedAt = parseTime(created)
		d.UpdatedAt = parseTime(updated)
		if p, err := m.pathFor(d.Name); err == nil {
			d.SizeBytes = fileSizeWithSidecars(p)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Find returns metadata for one database by name.
func (m *Manager) Find(ctx context.Context, name string) (*Meta, error) {
	var d Meta
	var created, updated string
	err := m.MetaRead().QueryRowContext(ctx,
		`SELECT id, name, filename, description, created_at, updated_at
		 FROM databases WHERE name = ?`, name).
		Scan(&d.ID, &d.Name, &d.Filename, &d.Description, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: database %q", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	d.CreatedAt = parseTime(created)
	d.UpdatedAt = parseTime(updated)
	if p, err := m.pathFor(d.Name); err == nil {
		d.SizeBytes = fileSizeWithSidecars(p)
	}
	return &d, nil
}

// FindByID returns metadata for one database by its identifier.
func (m *Manager) FindByID(ctx context.Context, id string) (*Meta, error) {
	var name string
	err := m.MetaRead().QueryRowContext(ctx, `SELECT name FROM databases WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: database id %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	return m.Find(ctx, name)
}

// SetDescription updates the human-readable description of a database.
func (m *Manager) SetDescription(ctx context.Context, name, description string) error {
	res, err := m.MetaDB().ExecContext(ctx,
		`UPDATE databases SET description = ?, updated_at = ? WHERE name = ?`,
		description, formatTime(time.Now().UTC()), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: database %q", ErrNotFound, name)
	}
	return nil
}

// PathFor exposes the resolved file path for a validated database name. It is
// used by the backup subsystem, which needs the file rather than a connection.
func (m *Manager) PathFor(name string) (string, error) { return m.pathFor(name) }

// ReplaceFile swaps a database's file with the contents of srcPath. The
// database is closed first and the replacement is verified before the old file
// is discarded, so a failed restore leaves the original intact.
func (m *Manager) ReplaceFile(ctx context.Context, name, srcPath string) error {
	dstPath, err := m.pathFor(name)
	if err != nil {
		return err
	}
	if err := VerifySQLiteFile(ctx, srcPath); err != nil {
		return err
	}
	if err := m.closeHandle(ctx, name); err != nil {
		return err
	}

	// Stage the replacement beside the target so the final swap is a rename
	// within one filesystem, which is atomic.
	staged := dstPath + ".restore-tmp"
	if err := copyFile(srcPath, staged); err != nil {
		return fmt.Errorf("stage restore: %w", err)
	}
	defer os.Remove(staged)

	backup := dstPath + ".restore-prev"
	hadOriginal := false
	if _, err := os.Stat(dstPath); err == nil {
		if err := os.Rename(dstPath, backup); err != nil {
			return fmt.Errorf("set aside current database: %w", err)
		}
		hadOriginal = true
	}
	if err := os.Rename(staged, dstPath); err != nil {
		if hadOriginal {
			_ = os.Rename(backup, dstPath)
		}
		return fmt.Errorf("install restored database: %w", err)
	}

	// Stale WAL files describe the previous database and must not survive.
	for _, side := range sidecarPaths(dstPath) {
		_ = os.Remove(side)
	}
	if hadOriginal {
		_ = os.Remove(backup)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	// Force the copy to disk before it is treated as authoritative.
	return out.Sync()
}

func fileSizeWithSidecars(path string) int64 {
	var total int64
	if fi, err := os.Stat(path); err == nil {
		total += fi.Size()
	}
	for _, s := range sidecarPaths(path) {
		if fi, err := os.Stat(s); err == nil {
			total += fi.Size()
		}
	}
	return total
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}
