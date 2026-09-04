package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Local stores backups on the server's own filesystem.
type Local struct {
	root string
}

// NewLocal creates the backup directory and returns a filesystem provider.
func NewLocal(root string) (*Local, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// Backups contain full database contents, so the directory is owner-only.
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create backup directory: %w", err)
	}
	return &Local{root: abs}, nil
}

func (l *Local) Name() string { return "local" }

// Root exposes the backup directory, for the download handler.
func (l *Local) Root() string { return l.root }

// resolve maps a key onto a path inside the backup root.
//
// Keys come from generated filenames rather than user input, but they are still
// checked: a traversal here would let a backup operation read or overwrite an
// arbitrary file.
func (l *Local) resolve(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("empty storage key")
	}
	if strings.ContainsAny(key, `\`) || strings.Contains(key, "..") || filepath.IsAbs(key) {
		return "", fmt.Errorf("invalid storage key %q", key)
	}

	joined := filepath.Join(l.root, filepath.Clean("/"+key))
	// Clean("/"+key) strips any leading traversal, and this check confirms the
	// result really is under the root.
	rel, err := filepath.Rel(l.root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("storage key %q escapes the backup directory", key)
	}
	return joined, nil
}

func (l *Local) Put(ctx context.Context, key string, r io.Reader, size int64) (*Object, error) {
	path, err := l.resolve(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	// Write to a temporary file and rename, so a crash never leaves a partial
	// backup that looks complete.
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	written, copyErr := io.Copy(f, r)
	syncErr := f.Sync()
	closeErr := f.Close()

	if copyErr != nil || syncErr != nil || closeErr != nil {
		os.Remove(tmp)
		return nil, firstError(copyErr, syncErr, closeErr)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}

	return &Object{Key: key, RemoteID: key, Size: written, UpdatedAt: time.Now().UTC()}, nil
}

func (l *Local) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	path, err := l.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	return f, err
}

func (l *Local) Delete(ctx context.Context, key string) error {
	path, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *Local) Stat(ctx context.Context, key string) (*Object, error) {
	path, err := l.resolve(key)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &Object{Key: key, RemoteID: key, Size: fi.Size(), UpdatedAt: fi.ModTime().UTC()}, nil
}

func (l *Local) List(ctx context.Context, prefix string) ([]Object, error) {
	entries, err := os.ReadDir(l.root)
	if err != nil {
		return nil, err
	}

	out := []Object{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Object{
			Key: e.Name(), RemoteID: e.Name(),
			Size: info.Size(), UpdatedAt: info.ModTime().UTC(),
		})
	}
	return out, nil
}

// LocalPath returns the on-disk path for a key, for streaming downloads
// directly from the filesystem.
func (l *Local) LocalPath(key string) (string, error) { return l.resolve(key) }

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
