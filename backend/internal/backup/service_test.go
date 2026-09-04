package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/storage"
)

func newTestService(t *testing.T, encKey string) (*Service, *database.Manager) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	m, err := database.NewManager(ctx, database.ManagerOptions{
		DatabasesDir: filepath.Join(dir, "databases"),
		MetaPath:     filepath.Join(dir, "litebase.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })

	local, err := storage.NewLocal(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	reg := storage.NewRegistry()
	reg.Register(local)

	svc, err := NewService(Options{
		Manager: m, Storage: reg,
		TempDir: filepath.Join(dir, "tmp"), EncryptionKey: encKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, m
}

// seedDatabase creates a database with a known row count.
func seedDatabase(t *testing.T, m *database.Manager, name string, rowCount int) {
	t.Helper()
	ctx := context.Background()
	if _, err := m.Create(ctx, name, ""); err != nil {
		t.Fatal(err)
	}
	h, err := m.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()

	if _, err := h.Writer().ExecContext(ctx,
		`CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rowCount; i++ {
		if _, err := h.Writer().ExecContext(ctx,
			`INSERT INTO items(name) VALUES(?)`, "item"); err != nil {
			t.Fatal(err)
		}
	}
}

func rowCount(t *testing.T, m *database.Manager, name string) int {
	t.Helper()
	h, err := m.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	var n int
	if err := h.Reader().QueryRowContext(context.Background(),
		`SELECT count(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBackupCreateAndRestore(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 10)

	rec, err := s.Create(ctx, CreateOptions{Database: "app"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.Status != StatusCompleted {
		t.Fatalf("status = %q (%s), want completed", rec.Status, rec.Error)
	}
	if rec.SizeBytes <= 0 || rec.Checksum == "" {
		t.Errorf("record looks incomplete: %+v", rec)
	}
	if rec.CompletedAt == nil {
		t.Error("completed_at not recorded")
	}

	// Change the database after the backup.
	h, _ := m.Get(ctx, "app")
	if _, err := h.Writer().ExecContext(ctx, `DELETE FROM items`); err != nil {
		t.Fatal(err)
	}
	h.Release()
	if n := rowCount(t, m, "app"); n != 0 {
		t.Fatalf("rows = %d, want 0 before restore", n)
	}

	if err := s.Restore(ctx, rec.ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if n := rowCount(t, m, "app"); n != 10 {
		t.Errorf("rows after restore = %d, want 10", n)
	}
}

func TestRestoreTakesSafetySnapshotFirst(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 5)

	rec, err := s.Create(ctx, CreateOptions{Database: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}

	list, err := s.List(ctx, ListOptions{Database: "app"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range list {
		if r.Trigger == TriggerPreRestore {
			found = true
		}
	}
	if !found {
		t.Error("no pre-restore safety snapshot was taken")
	}
}

func TestBackupIsConsistentUnderConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 50)

	// Keep writing while the snapshot runs. VACUUM INTO must produce a
	// transactionally consistent copy rather than a torn one.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h, err := m.Get(ctx, "app")
		if err != nil {
			return
		}
		defer h.Release()
		for {
			select {
			case <-stop:
				return
			default:
				h.Writer().ExecContext(ctx, `INSERT INTO items(name) VALUES('concurrent')`)
			}
		}
	}()

	rec, err := s.Create(ctx, CreateOptions{Database: "app"})
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatalf("Create during writes: %v", err)
	}
	if rec.Status != StatusCompleted {
		t.Fatalf("status = %q: %s", rec.Status, rec.Error)
	}

	// The snapshot must restore to a valid, readable database.
	if err := s.Restore(ctx, rec.ID); err != nil {
		t.Fatalf("Restore of a concurrent snapshot: %v", err)
	}
	if n := rowCount(t, m, "app"); n < 50 {
		t.Errorf("restored row count = %d, want at least the 50 seeded rows", n)
	}
}

func TestEncryptedBackupRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "a-strong-backup-key")
	seedDatabase(t, m, "app", 8)

	rec, err := s.Create(ctx, CreateOptions{Database: "app", Encrypt: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !rec.Encrypted {
		t.Fatal("record not marked encrypted")
	}

	// The stored artefact must not be a readable SQLite file.
	provider, _ := s.storage.Get("local")
	reader, err := provider.Get(ctx, rec.Filename)
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 16)
	io.ReadFull(reader, head)
	reader.Close()
	if string(head[:15]) == "SQLite format 3" {
		t.Error("encrypted backup was stored as a plain SQLite file")
	}

	h, _ := m.Get(ctx, "app")
	h.Writer().ExecContext(ctx, `DELETE FROM items`)
	h.Release()

	if err := s.Restore(ctx, rec.ID); err != nil {
		t.Fatalf("Restore encrypted: %v", err)
	}
	if n := rowCount(t, m, "app"); n != 8 {
		t.Errorf("rows after encrypted restore = %d, want 8", n)
	}
}

func TestEncryptRequiresConfiguredKey(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 1)

	if _, err := s.Create(ctx, CreateOptions{Database: "app", Encrypt: true}); !errors.Is(err, ErrNoKey) {
		t.Errorf("Create with encryption but no key = %v, want ErrNoKey", err)
	}
}

func TestRestoreRejectsCorruptedBackup(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 5)

	rec, err := s.Create(ctx, CreateOptions{Database: "app"})
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt the stored artefact; the checksum must catch it before the live
	// database is overwritten.
	local, _ := s.storage.Get("local")
	path, err := local.(*storage.Local).LocalPath(rec.Filename)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt([]byte("garbage!"), 200)
	f.Close()

	err = s.Restore(ctx, rec.ID)
	if err == nil {
		t.Fatal("restore of a corrupted backup succeeded")
	}
	// The live database must be untouched.
	if n := rowCount(t, m, "app"); n != 5 {
		t.Errorf("rows = %d, want 5; a failed restore damaged the database", n)
	}
}

func TestOpenDecryptsForDownload(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "download-key")
	seedDatabase(t, m, "app", 3)

	rec, err := s.Create(ctx, CreateOptions{Database: "app", Encrypt: true})
	if err != nil {
		t.Fatal(err)
	}

	reader, _, err := s.Open(ctx, rec.ID, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()

	head := make([]byte, 16)
	if _, err := io.ReadFull(reader, head); err != nil {
		t.Fatal(err)
	}
	// A download should hand the operator a usable database file.
	if string(head[:15]) != "SQLite format 3" {
		t.Errorf("download did not produce a SQLite file: %q", head)
	}
}

func TestDeleteRemovesArtefactAndRecord(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 2)

	rec, err := s.Create(ctx, CreateOptions{Database: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, rec.ID); !errors.Is(err, ErrBackupNotFound) {
		t.Errorf("record still present: %v", err)
	}
	provider, _ := s.storage.Get("local")
	if _, err := provider.Stat(ctx, rec.Filename); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("artefact still present: %v", err)
	}
}

func TestRetentionKeepsMostRecentAndCount(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 1)

	for i := 0; i < 5; i++ {
		if _, err := s.Create(ctx, CreateOptions{Database: "app"}); err != nil {
			t.Fatal(err)
		}
		// Filenames embed a second-resolution timestamp; keep them distinct.
		time.Sleep(1100 * time.Millisecond)
	}

	if err := s.ApplyRetention(ctx, "app", 2, 0); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	list, err := s.List(ctx, ListOptions{Database: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("kept %d backups, want 2", len(list))
	}
}

func TestRetentionNeverRemovesTheLastBackup(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 1)

	rec, err := s.Create(ctx, CreateOptions{Database: "app"})
	if err != nil {
		t.Fatal(err)
	}
	// An aggressive policy must still leave a recovery point behind.
	if err := s.ApplyRetention(ctx, "app", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, rec.ID); err != nil {
		t.Errorf("the only backup was deleted by retention: %v", err)
	}
}

func TestScheduleLifecycle(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 3)
	meta, err := m.Find(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}

	sch := &Schedule{
		DatabaseID: meta.ID, Name: "nightly", IntervalSecs: 3600,
		Provider: "local", Enabled: true, RetentionCount: 3,
	}
	if err := s.CreateSchedule(ctx, sch); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if sch.NextRunAt.IsZero() {
		t.Error("next run not scheduled")
	}

	// Nothing is due yet.
	due, err := s.DueSchedules(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("due = %d, want 0", len(due))
	}
	// It becomes due once its interval has elapsed.
	due, err = s.DueSchedules(ctx, time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("due = %d, want 1", len(due))
	}

	if err := s.RunSchedule(ctx, &due[0]); err != nil {
		t.Fatalf("RunSchedule: %v", err)
	}
	list, err := s.List(ctx, ListOptions{Database: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Trigger != TriggerScheduled {
		t.Errorf("scheduled backup not created: %+v", list)
	}

	// The run must be recorded and the next one pushed forward.
	updated, err := s.GetSchedule(ctx, sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastRunAt == nil || updated.LastStatus != "ok" {
		t.Errorf("run not recorded: %+v", updated)
	}
	if !updated.NextRunAt.After(time.Now()) {
		t.Error("next run was not advanced")
	}

	if err := s.DeleteSchedule(ctx, sch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSchedule(ctx, sch.ID); !errors.Is(err, ErrScheduleNotFound) {
		t.Errorf("schedule still present: %v", err)
	}
}

func TestScheduleValidation(t *testing.T) {
	bad := []Schedule{
		{Name: "", IntervalSecs: 3600},
		{Name: "x", IntervalSecs: 10}, // below the minimum interval
		{Name: "x", IntervalSecs: 3600, RetentionCount: -1},
	}
	for i := range bad {
		if err := bad[i].Validate(); err == nil {
			t.Errorf("case %d accepted: %+v", i, bad[i])
		}
	}
}

func TestScheduleForAllDatabases(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "one", 2)
	seedDatabase(t, m, "two", 2)

	sch := &Schedule{Name: "all", IntervalSecs: 3600, Provider: "local", Enabled: true}
	if err := s.CreateSchedule(ctx, sch); err != nil {
		t.Fatal(err)
	}
	if err := s.RunSchedule(ctx, sch); err != nil {
		t.Fatalf("RunSchedule: %v", err)
	}

	for _, name := range []string{"one", "two"} {
		list, err := s.List(ctx, ListOptions{Database: name})
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 {
			t.Errorf("database %q got %d backups, want 1", name, len(list))
		}
	}
}

func TestBackupFilenamesAreUniqueWithinASecond(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t, "")
	seedDatabase(t, m, "app", 1)

	// Two backups taken in the same second must not share a filename, or the
	// second would overwrite the first's artefact.
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		rec, err := s.Create(ctx, CreateOptions{Database: "app"})
		if err != nil {
			t.Fatal(err)
		}
		if seen[rec.Filename] {
			t.Fatalf("duplicate backup filename %q", rec.Filename)
		}
		seen[rec.Filename] = true
	}

	// All three artefacts must still exist on disk.
	provider, _ := s.storage.Get("local")
	for name := range seen {
		if _, err := provider.Stat(ctx, name); err != nil {
			t.Errorf("artefact %q missing: %v", name, err)
		}
	}
}
