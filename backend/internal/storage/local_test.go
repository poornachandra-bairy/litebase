package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newLocal(t *testing.T) *Local {
	t.Helper()
	l, err := NewLocal(filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLocalPutGetDelete(t *testing.T) {
	ctx := context.Background()
	l := newLocal(t)

	obj, err := l.Put(ctx, "backup.db", strings.NewReader("hello world"), 11)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if obj.Size != 11 {
		t.Errorf("size = %d, want 11", obj.Size)
	}

	r, err := l.Get(ctx, "backup.db")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	if string(data) != "hello world" {
		t.Errorf("content = %q", data)
	}

	if _, err := l.Stat(ctx, "backup.db"); err != nil {
		t.Errorf("Stat: %v", err)
	}
	objects, err := l.List(ctx, "backup")
	if err != nil || len(objects) != 1 {
		t.Errorf("List = %v, %v; want one object", objects, err)
	}

	if err := l.Delete(ctx, "backup.db"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := l.Get(ctx, "backup.db"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	// Deleting a missing object is a no-op so retention runs stay idempotent.
	if err := l.Delete(ctx, "backup.db"); err != nil {
		t.Errorf("second Delete = %v, want nil", err)
	}
}

func TestLocalRejectsPathTraversal(t *testing.T) {
	ctx := context.Background()
	l := newLocal(t)

	bad := []string{
		"../escape.db",
		"../../etc/passwd",
		"/etc/passwd",
		`..\windows`,
		"",
		"a/../../b",
	}
	for _, key := range bad {
		if _, err := l.Put(ctx, key, strings.NewReader("x"), 1); err == nil {
			t.Errorf("Put(%q) = nil error, want rejection", key)
		}
		if _, err := l.Get(ctx, key); err == nil {
			t.Errorf("Get(%q) = nil error, want rejection", key)
		}
		if err := l.Delete(ctx, key); err == nil {
			t.Errorf("Delete(%q) = nil error, want rejection", key)
		}
	}

	// Nothing may have been written outside the backup root.
	parent := filepath.Dir(l.Root())
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if e.Name() != "backups" {
			t.Errorf("unexpected file outside the backup root: %s", e.Name())
		}
	}
}

func TestLocalPutIsAtomic(t *testing.T) {
	ctx := context.Background()
	l := newLocal(t)

	if _, err := l.Put(ctx, "a.db", strings.NewReader("first"), 5); err != nil {
		t.Fatal(err)
	}
	// A partial file must never be left behind under the final name.
	entries, _ := os.ReadDir(l.Root())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".partial") {
			t.Errorf("a staging file was left behind: %s", e.Name())
		}
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	l := newLocal(t)
	reg.Register(l)

	if !reg.Has("local") {
		t.Error("registered provider not found")
	}
	if _, err := reg.Get("local"); err != nil {
		t.Errorf("Get(local): %v", err)
	}
	// An empty name defaults to local, which is what an unset config means.
	if _, err := reg.Get(""); err != nil {
		t.Errorf("Get(\"\"): %v", err)
	}
	if _, err := reg.Get("s3"); err == nil {
		t.Error("Get(s3) succeeded for an unconfigured provider")
	}
}

func TestGoogleDriveConfigValidation(t *testing.T) {
	if err := (GoogleDriveConfig{}).Validate(); err == nil {
		t.Error("empty config accepted")
	}
	if err := (GoogleDriveConfig{ClientID: "a", ClientSecret: "b"}).Validate(); err == nil {
		t.Error("config without a refresh token accepted")
	}
	full := GoogleDriveConfig{ClientID: "a", ClientSecret: "b", RefreshToken: "c"}
	if err := full.Validate(); err != nil {
		t.Errorf("complete config rejected: %v", err)
	}
	if _, err := NewGoogleDrive(full); err != nil {
		t.Errorf("NewGoogleDrive: %v", err)
	}
}
