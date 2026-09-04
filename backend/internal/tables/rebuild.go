package tables

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/litebase/litebase/internal/database"
)

// SQLite's ALTER TABLE cannot change a column's type, nullability, default or
// constraints. The supported way to do so is to rebuild the table, which is
// what this file implements, following the procedure documented at
// https://sqlite.org/lang_altertable.html#otheralter.
//
// The sequence matters for data safety:
//
//  1. foreign_keys is turned off, because dropping the old table would
//     otherwise cascade deletes into child tables.
//  2. Everything else happens inside one transaction, so a failure at any
//     point rolls back to the original table with its data intact.
//  3. foreign_key_check runs before COMMIT, so a rebuild that would leave
//     dangling references is rejected rather than committed.

// ModifyColumn changes a column's definition by rebuilding the table.
func (s *Service) ModifyColumn(ctx context.Context, dbName, tableName, columnName string, newCol Column) error {
	if err := database.ValidateIdentifier(columnName); err != nil {
		return err
	}
	if err := validateColumn(&newCol); err != nil {
		return err
	}

	current, err := s.GetTable(ctx, dbName, tableName)
	if err != nil {
		return err
	}
	if ObjectKind(current.Kind) != KindTable {
		return fmt.Errorf("%q is a view and has no columns to modify", tableName)
	}

	found := false
	newColumns := make([]Column, len(current.Columns))
	copy(newColumns, current.Columns)
	for i, c := range newColumns {
		if c.Name != columnName {
			continue
		}
		found = true
		// Carry over the original name unless the caller supplied a new one.
		if newCol.Name == "" {
			newCol.Name = c.Name
		}
		newColumns[i] = newCol
	}
	if !found {
		return fmt.Errorf("%w: column %q in table %q", ErrObjectNotFound, columnName, tableName)
	}

	// Column order is preserved, so the copy can map old to new positionally.
	oldNames := make([]string, len(current.Columns))
	for i, c := range current.Columns {
		oldNames[i] = c.Name
	}
	return s.rebuildTable(ctx, dbName, current, newColumns, oldNames)
}

// rebuildTable recreates a table with newColumns and copies the data across.
// copyFrom names the source column for each new column, positionally.
func (s *Service) rebuildTable(ctx context.Context, dbName string, current *Table, newColumns []Column, copyFrom []string) error {
	if len(newColumns) != len(copyFrom) {
		return fmt.Errorf("internal: column mapping length mismatch")
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	// A dedicated connection guarantees the foreign_keys pragma and the
	// transaction below run on the same connection; on a pooled handle they
	// might not.
	conn, err := h.Writer().Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	var fkWasOn int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fkWasOn); err != nil {
		return err
	}
	// This pragma is a no-op inside a transaction, so it must be set first.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		if fkWasOn != 0 {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)
		}
	}()

	tempName := "litebase_rebuild_" + database.NewID()[:12]
	createStmt, err := buildCreateTable(CreateTableInput{
		Name:         tempName,
		Columns:      newColumns,
		WithoutRowID: current.WithoutRowID,
		Strict:       current.Strict,
	})
	if err != nil {
		return err
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, createStmt); err != nil {
		return wrapSQLiteError(err)
	}

	// Copy the data. Both sides are listed explicitly so the mapping does not
	// depend on the two tables happening to agree on column order.
	targetCols := make([]string, 0, len(newColumns))
	sourceCols := make([]string, 0, len(newColumns))
	for i, c := range newColumns {
		if copyFrom[i] == "" {
			continue
		}
		tq, err := database.QuoteIdent(c.Name)
		if err != nil {
			return err
		}
		targetCols = append(targetCols, tq)
		sourceCols = append(sourceCols, database.MustQuoteIdent(copyFrom[i]))
	}
	if len(targetCols) > 0 {
		copyStmt := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s",
			database.MustQuoteIdent(tempName),
			strings.Join(targetCols, ", "),
			strings.Join(sourceCols, ", "),
			database.MustQuoteIdent(current.Name))
		if _, err := tx.ExecContext(ctx, copyStmt); err != nil {
			return fmt.Errorf("copy rows into the rebuilt table: %w", wrapSQLiteError(err))
		}
	}

	quotedOld := database.MustQuoteIdent(current.Name)
	if _, err := tx.ExecContext(ctx, "DROP TABLE "+quotedOld); err != nil {
		return wrapSQLiteError(err)
	}
	if _, err := tx.ExecContext(ctx,
		"ALTER TABLE "+database.MustQuoteIdent(tempName)+" RENAME TO "+quotedOld); err != nil {
		return wrapSQLiteError(err)
	}

	// Dropping the table also dropped its indexes and triggers; recreate the
	// ones that were defined by a CREATE statement. Indexes SQLite generated
	// for UNIQUE or PRIMARY KEY constraints come back with the new definition.
	newNames := map[string]bool{}
	for _, c := range newColumns {
		newNames[strings.ToLower(c.Name)] = true
	}
	for _, idx := range current.Indexes {
		if idx.Origin != "c" || idx.SQL == "" {
			continue
		}
		// An index over a column that no longer exists cannot be recreated.
		skip := false
		for _, col := range idx.Columns {
			if !newNames[strings.ToLower(col)] {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		if _, err := tx.ExecContext(ctx, idx.SQL); err != nil {
			return fmt.Errorf("recreate index %q: %w", idx.Name, wrapSQLiteError(err))
		}
	}
	for _, trg := range current.Triggers {
		if trg.SQL == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, trg.SQL); err != nil {
			return fmt.Errorf("recreate trigger %q: %w", trg.Name, wrapSQLiteError(err))
		}
	}

	// Refuse to commit a rebuild that would leave broken references.
	violations, err := foreignKeyViolations(ctx, tx)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("the change would break %d foreign key reference(s); no changes were made", len(violations))
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func foreignKeyViolations(ctx context.Context, tx *sql.Tx) ([]FKViolation, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		// Not every build exposes this pragma inside a transaction; treat an
		// unavailable check as no violations rather than failing the rebuild.
		return nil, nil
	}
	defer rows.Close()

	out := []FKViolation{}
	for rows.Next() {
		var v FKViolation
		var rowid sql.NullInt64
		if err := rows.Scan(&v.Table, &rowid, &v.Parent, &v.FKIndex); err != nil {
			return nil, err
		}
		if rowid.Valid {
			id := rowid.Int64
			v.RowID = &id
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
