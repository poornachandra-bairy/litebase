package database

import (
	"context"
	"database/sql"
	"fmt"
)

// migration is one forward-only change to the internal metadata schema.
//
// Migrations are append-only: once a version has shipped its statements must
// never be edited, because existing installations have already applied them.
type migration struct {
	Version int
	Name    string
	Stmts   []string
}

// metaMigrations defines the schema of the application's own database. User
// databases are never touched by these.
var metaMigrations = []migration{
	{
		Version: 1,
		Name:    "initial_schema",
		Stmts: []string{
			`CREATE TABLE users (
				id            TEXT PRIMARY KEY,
				email         TEXT NOT NULL,
				email_lower   TEXT NOT NULL UNIQUE,
				name          TEXT NOT NULL DEFAULT '',
				password_hash TEXT NOT NULL,
				role          TEXT NOT NULL DEFAULT 'admin',
				disabled      INTEGER NOT NULL DEFAULT 0,
				created_at    TEXT NOT NULL,
				updated_at    TEXT NOT NULL
			)`,

			// Sessions store only a hash of the token, so a leaked database
			// snapshot cannot be replayed as a live login.
			`CREATE TABLE sessions (
				id            TEXT PRIMARY KEY,
				token_hash    TEXT NOT NULL UNIQUE,
				user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				csrf_token    TEXT NOT NULL,
				user_agent    TEXT NOT NULL DEFAULT '',
				ip            TEXT NOT NULL DEFAULT '',
				created_at    TEXT NOT NULL,
				last_seen_at  TEXT NOT NULL,
				expires_at    TEXT NOT NULL
			)`,
			`CREATE INDEX idx_sessions_user ON sessions(user_id)`,
			`CREATE INDEX idx_sessions_expires ON sessions(expires_at)`,

			`CREATE TABLE databases (
				id          TEXT PRIMARY KEY,
				name        TEXT NOT NULL UNIQUE,
				filename    TEXT NOT NULL UNIQUE,
				description TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL,
				updated_at  TEXT NOT NULL
			)`,

			`CREATE TABLE api_keys (
				id            TEXT PRIMARY KEY,
				name          TEXT NOT NULL,
				key_hash      TEXT NOT NULL UNIQUE,
				key_prefix    TEXT NOT NULL,
				scopes        TEXT NOT NULL DEFAULT '[]',
				all_endpoints INTEGER NOT NULL DEFAULT 0,
				rate_limit    INTEGER NOT NULL DEFAULT 0,
				disabled      INTEGER NOT NULL DEFAULT 0,
				expires_at    TEXT,
				last_used_at  TEXT,
				created_by    TEXT REFERENCES users(id) ON DELETE SET NULL,
				created_at    TEXT NOT NULL,
				updated_at    TEXT NOT NULL
			)`,
			`CREATE INDEX idx_api_keys_prefix ON api_keys(key_prefix)`,

			`CREATE TABLE api_endpoints (
				id            TEXT PRIMARY KEY,
				database_id   TEXT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
				name          TEXT NOT NULL,
				description   TEXT NOT NULL DEFAULT '',
				kind          TEXT NOT NULL,
				method        TEXT NOT NULL,
				path          TEXT NOT NULL,
				table_name    TEXT NOT NULL DEFAULT '',
				sql_text      TEXT NOT NULL DEFAULT '',
				params        TEXT NOT NULL DEFAULT '[]',
				config        TEXT NOT NULL DEFAULT '{}',
				enabled       INTEGER NOT NULL DEFAULT 1,
				public        INTEGER NOT NULL DEFAULT 0,
				rate_limit    INTEGER NOT NULL DEFAULT 0,
				max_rows      INTEGER NOT NULL DEFAULT 0,
				created_at    TEXT NOT NULL,
				updated_at    TEXT NOT NULL,
				UNIQUE(method, path)
			)`,
			`CREATE INDEX idx_api_endpoints_database ON api_endpoints(database_id)`,

			`CREATE TABLE api_key_endpoints (
				key_id      TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
				endpoint_id TEXT NOT NULL REFERENCES api_endpoints(id) ON DELETE CASCADE,
				PRIMARY KEY (key_id, endpoint_id)
			)`,

			`CREATE TABLE backup_schedules (
				id              TEXT PRIMARY KEY,
				database_id     TEXT REFERENCES databases(id) ON DELETE CASCADE,
				name            TEXT NOT NULL,
				interval_secs   INTEGER NOT NULL,
				provider        TEXT NOT NULL DEFAULT 'local',
				encrypt         INTEGER NOT NULL DEFAULT 0,
				retention_count INTEGER NOT NULL DEFAULT 0,
				retention_days  INTEGER NOT NULL DEFAULT 0,
				enabled         INTEGER NOT NULL DEFAULT 1,
				last_run_at     TEXT,
				last_status     TEXT NOT NULL DEFAULT '',
				last_error      TEXT NOT NULL DEFAULT '',
				next_run_at     TEXT NOT NULL,
				created_at      TEXT NOT NULL,
				updated_at      TEXT NOT NULL
			)`,
			`CREATE INDEX idx_backup_schedules_next ON backup_schedules(enabled, next_run_at)`,

			`CREATE TABLE backups (
				id           TEXT PRIMARY KEY,
				database_id  TEXT REFERENCES databases(id) ON DELETE SET NULL,
				database_name TEXT NOT NULL DEFAULT '',
				schedule_id  TEXT REFERENCES backup_schedules(id) ON DELETE SET NULL,
				filename     TEXT NOT NULL,
				provider     TEXT NOT NULL DEFAULT 'local',
				remote_id    TEXT NOT NULL DEFAULT '',
				size_bytes   INTEGER NOT NULL DEFAULT 0,
				checksum     TEXT NOT NULL DEFAULT '',
				encrypted    INTEGER NOT NULL DEFAULT 0,
				trigger      TEXT NOT NULL DEFAULT 'manual',
				status       TEXT NOT NULL DEFAULT 'pending',
				error        TEXT NOT NULL DEFAULT '',
				started_at   TEXT NOT NULL,
				completed_at TEXT
			)`,
			`CREATE INDEX idx_backups_database ON backups(database_id, started_at DESC)`,

			// Settings hold operator-managed values such as remote storage
			// credentials. Secret values are encrypted before they are stored.
			`CREATE TABLE settings (
				key        TEXT PRIMARY KEY,
				value      TEXT NOT NULL,
				secret     INTEGER NOT NULL DEFAULT 0,
				updated_at TEXT NOT NULL
			)`,
		},
	},
}

// migrate applies every migration newer than the recorded version.
//
// Each migration runs inside its own transaction together with the bookkeeping
// row, so an interrupted upgrade can never record a version it did not fully
// apply.
func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var current int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for _, m := range metaMigrations {
		if m.Version <= current {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, stmt := range m.Stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d: %w", i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, datetime('now'))`,
		m.Version, m.Name); err != nil {
		return err
	}
	return tx.Commit()
}

// SchemaVersion reports the highest applied metadata migration.
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}
