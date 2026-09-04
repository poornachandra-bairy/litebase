package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
)

// OpenReadOnly returns a read-only connection to a user database.
//
// The connection is opened with SQLite's mode=ro, so the engine itself refuses
// any statement that would write. That is a stronger guarantee than inspecting
// the SQL text: it holds for statements our parser might misclassify, and for
// side effects reached through triggers or nested statements.
//
// The caller must close the returned database.
func (m *Manager) OpenReadOnly(ctx context.Context, name string) (*sql.DB, error) {
	path, err := m.pathFor(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: database %q", ErrNotFound, name)
		}
		return nil, err
	}
	// A read-only connection cannot create the WAL index, so the database must
	// already be open elsewhere or have been checkpointed. Taking a normal
	// handle first guarantees the WAL is initialised.
	h, err := m.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	h.Release()

	return open(ctx, path, OpenOptions{
		ReadOnly:     true,
		MaxOpenConns: 2,
		BusyTimeout:  m.busyTimeout,
	})
}
