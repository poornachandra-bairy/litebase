package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	m, err := NewManager(context.Background(), ManagerOptions{
		DatabasesDir: filepath.Join(dir, "databases"),
		MetaPath:     filepath.Join(dir, "litebase.db"),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestManagerMigratesMetaSchema(t *testing.T) {
	m := newTestManager(t)
	v, err := SchemaVersion(context.Background(), m.MetaRead())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != len(metaMigrations) {
		t.Errorf("schema version = %d, want %d", v, len(metaMigrations))
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	m := newTestManager(t)
	// Re-running migrations against an up-to-date database must be a no-op
	// rather than an error, since it happens on every restart.
	if err := migrate(context.Background(), m.MetaDB()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestManagerDatabaseLifecycle(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)

	meta, err := m.Create(ctx, "shop", "store data")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if meta.Name != "shop" {
		t.Errorf("name = %q", meta.Name)
	}

	if _, err := m.Create(ctx, "shop", ""); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate Create error = %v, want ErrAlreadyExists", err)
	}

	h, err := m.Get(ctx, "shop")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := h.Writer().ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := h.Writer().ExecContext(ctx, `INSERT INTO t(v) VALUES('hello')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	h.Release()

	if err := m.Rename(ctx, "shop", "store"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := m.Find(ctx, "shop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find(old name) = %v, want ErrNotFound", err)
	}

	// Data must survive the rename.
	h2, err := m.Get(ctx, "store")
	if err != nil {
		t.Fatalf("Get after rename: %v", err)
	}
	var v string
	if err := h2.Reader().QueryRowContext(ctx, `SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("select after rename: %v", err)
	}
	if v != "hello" {
		t.Errorf("value = %q, want hello", v)
	}
	h2.Release()

	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "store" {
		t.Fatalf("List = %+v, want one entry named store", list)
	}
	if list[0].SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want > 0", list[0].SizeBytes)
	}

	if err := m.Delete(ctx, "store"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := m.Get(ctx, "store"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := m.Delete(ctx, "store"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestManagerRejectsUnsafeNames(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)
	for _, name := range []string{"../evil", "a/b", "", "sqlite_x", "has space"} {
		if _, err := m.Create(ctx, name, ""); err == nil {
			t.Errorf("Create(%q) = nil error, want rejection", name)
		}
	}
}

func TestManagerImportRejectsNonSQLite(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)

	junk := filepath.Join(t.TempDir(), "notadb.db")
	if err := os.WriteFile(junk, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ImportFile(ctx, "imported", "", junk); !errors.Is(err, ErrNotSQLite) {
		t.Errorf("ImportFile(junk) = %v, want ErrNotSQLite", err)
	}
	// A rejected import must not leave a registration behind.
	if _, err := m.Find(ctx, "imported"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find after failed import = %v, want ErrNotFound", err)
	}
}

func TestManagerImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)

	// Build a source database, then import a copy of its file.
	if _, err := m.Create(ctx, "source", ""); err != nil {
		t.Fatal(err)
	}
	h, err := m.Get(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Writer().ExecContext(ctx, `CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Writer().ExecContext(ctx, `INSERT INTO items(name) VALUES('widget')`); err != nil {
		t.Fatal(err)
	}
	// Fold the WAL in so the file on disk is complete.
	if err := h.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	srcPath := h.Path()
	h.Release()

	copyPath := filepath.Join(t.TempDir(), "upload.db")
	if err := copyFile(srcPath, copyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ImportFile(ctx, "restored", "imported copy", copyPath); err != nil {
		t.Fatalf("ImportFile: %v", err)
	}

	h2, err := m.Get(ctx, "restored")
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Release()
	var name string
	if err := h2.Reader().QueryRowContext(ctx, `SELECT name FROM items`).Scan(&name); err != nil {
		t.Fatalf("query imported: %v", err)
	}
	if name != "widget" {
		t.Errorf("imported value = %q, want widget", name)
	}
}

func TestHandleReferenceCounting(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)
	if _, err := m.Create(ctx, "refs", ""); err != nil {
		t.Fatal(err)
	}

	a, err := m.Get(ctx, "refs")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Get(ctx, "refs")
	if err != nil {
		t.Fatal(err)
	}
	// Both Gets must share one handle so a database is never backed by two pools.
	if a != b {
		t.Error("Get returned distinct handles for the same database")
	}
	if !a.inUse() {
		t.Error("handle not marked in use")
	}
	a.Release()
	if !b.inUse() {
		t.Error("handle released while a reference remains")
	}
	b.Release()
	if b.inUse() {
		t.Error("handle still in use after all references released")
	}
}
