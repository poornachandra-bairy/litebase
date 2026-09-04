package tables

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/litebase/litebase/internal/database"
)

// Every statement in this file is assembled from validated, quoted identifiers
// and whitelisted keywords. No caller-supplied string is ever concatenated into
// SQL without first passing through database.QuoteIdent or one of the
// validators in validate.go.

// CreateTableInput describes a table to create.
type CreateTableInput struct {
	Name         string   `json:"name"`
	Columns      []Column `json:"columns"`
	WithoutRowID bool     `json:"without_rowid"`
	Strict       bool     `json:"strict"`
	IfNotExists  bool     `json:"if_not_exists"`
}

// CreateTable creates a table from a validated definition.
func (s *Service) CreateTable(ctx context.Context, dbName string, in CreateTableInput) error {
	stmt, err := buildCreateTable(in)
	if err != nil {
		return err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	if _, err := h.Writer().ExecContext(ctx, stmt); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// buildCreateTable renders a CREATE TABLE statement. It is separated from
// execution so the generated SQL can be unit tested directly.
func buildCreateTable(in CreateTableInput) (string, error) {
	quotedTable, err := database.QuoteIdent(in.Name)
	if err != nil {
		return "", fmt.Errorf("table name: %w", err)
	}
	if len(in.Columns) == 0 {
		return "", fmt.Errorf("a table needs at least one column")
	}
	if len(in.Columns) > 999 {
		return "", fmt.Errorf("a table may have at most 999 columns")
	}

	seen := map[string]bool{}
	pkCount := 0
	var defs []string

	for i := range in.Columns {
		c := &in.Columns[i]
		if err := validateColumn(c); err != nil {
			return "", err
		}
		lower := strings.ToLower(c.Name)
		if seen[lower] {
			return "", fmt.Errorf("duplicate column name %q", c.Name)
		}
		seen[lower] = true
		if c.PrimaryKey {
			pkCount++
		}
		def, err := columnDefinition(*c, pkCount == 1 && countPrimaryKeys(in.Columns) == 1)
		if err != nil {
			return "", err
		}
		defs = append(defs, def)
	}

	// A composite primary key must be declared as a table constraint rather
	// than inline on each column.
	if n := countPrimaryKeys(in.Columns); n > 1 {
		var pkCols []string
		for _, c := range in.Columns {
			if c.PrimaryKey {
				q, err := database.QuoteIdent(c.Name)
				if err != nil {
					return "", err
				}
				pkCols = append(pkCols, q)
			}
		}
		defs = append(defs, "PRIMARY KEY ("+strings.Join(pkCols, ", ")+")")
	}
	if in.WithoutRowID && countPrimaryKeys(in.Columns) == 0 {
		return "", fmt.Errorf("a WITHOUT ROWID table requires a primary key")
	}

	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	if in.IfNotExists {
		b.WriteString("IF NOT EXISTS ")
	}
	b.WriteString(quotedTable)
	b.WriteString(" (\n  ")
	b.WriteString(strings.Join(defs, ",\n  "))
	b.WriteString("\n)")

	var opts []string
	if in.WithoutRowID {
		opts = append(opts, "WITHOUT ROWID")
	}
	if in.Strict {
		opts = append(opts, "STRICT")
	}
	if len(opts) > 0 {
		b.WriteString(" " + strings.Join(opts, ", "))
	}
	return b.String(), nil
}

func countPrimaryKeys(cols []Column) int {
	n := 0
	for _, c := range cols {
		if c.PrimaryKey {
			n++
		}
	}
	return n
}

// columnDefinition renders one column clause. inlinePK controls whether the
// PRIMARY KEY keyword is emitted here or deferred to a table constraint.
func columnDefinition(c Column, inlinePK bool) (string, error) {
	quoted, err := database.QuoteIdent(c.Name)
	if err != nil {
		return "", err
	}
	// c.Type came from ValidateColumnType, so it is a whitelisted keyword with
	// an optional numeric precision.
	parts := []string{quoted, c.Type}

	if c.PrimaryKey && inlinePK {
		parts = append(parts, "PRIMARY KEY")
		if c.AutoIncrement {
			parts = append(parts, "AUTOINCREMENT")
		}
	}
	if c.NotNull && !(c.PrimaryKey && inlinePK) {
		parts = append(parts, "NOT NULL")
	}
	if c.Unique && !c.PrimaryKey {
		parts = append(parts, "UNIQUE")
	}
	if c.Default != nil && *c.Default != "" {
		// Already checked by ValidateDefault.
		parts = append(parts, "DEFAULT "+*c.Default)
	}
	if c.References != nil && c.References.Table != "" {
		ref, err := database.QuoteIdent(c.References.Table)
		if err != nil {
			return "", err
		}
		clause := "REFERENCES " + ref
		if c.References.Column != "" {
			refCol, err := database.QuoteIdent(c.References.Column)
			if err != nil {
				return "", err
			}
			clause += " (" + refCol + ")"
		}
		if c.References.OnDelete != "" {
			clause += " ON DELETE " + c.References.OnDelete
		}
		if c.References.OnUpdate != "" {
			clause += " ON UPDATE " + c.References.OnUpdate
		}
		parts = append(parts, clause)
	}
	return strings.Join(parts, " "), nil
}

// DropTable removes a table or view.
func (s *Service) DropTable(ctx context.Context, dbName, tableName string) error {
	quoted, err := database.QuoteIdent(tableName)
	if err != nil {
		return err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	var kind string
	err = h.Reader().QueryRowContext(ctx,
		`SELECT type FROM sqlite_schema WHERE name = ? AND type IN ('table','view')`,
		tableName).Scan(&kind)
	if err == sql.ErrNoRows {
		return fmt.Errorf("%w: %q", ErrObjectNotFound, tableName)
	}
	if err != nil {
		return err
	}

	stmt := "DROP TABLE " + quoted
	if ObjectKind(kind) == KindView {
		stmt = "DROP VIEW " + quoted
	}
	if _, err := h.Writer().ExecContext(ctx, stmt); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// RenameTable renames a table.
func (s *Service) RenameTable(ctx context.Context, dbName, oldName, newName string) error {
	from, err := database.QuoteIdent(oldName)
	if err != nil {
		return err
	}
	to, err := database.QuoteIdent(newName)
	if err != nil {
		return err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	if _, err := h.Writer().ExecContext(ctx,
		"ALTER TABLE "+from+" RENAME TO "+to); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// AddColumn appends a column to an existing table.
func (s *Service) AddColumn(ctx context.Context, dbName, tableName string, col Column) error {
	quotedTable, err := database.QuoteIdent(tableName)
	if err != nil {
		return err
	}
	if err := validateColumn(&col); err != nil {
		return err
	}
	// SQLite refuses these on ALTER TABLE ADD COLUMN; catching them here gives
	// a clearer message than the driver's.
	if col.PrimaryKey {
		return fmt.Errorf("a primary key column cannot be added to an existing table; recreate the table instead")
	}
	if col.Unique {
		return fmt.Errorf("a UNIQUE column cannot be added directly; add the column, then create a unique index")
	}
	if col.NotNull && (col.Default == nil || *col.Default == "" || strings.EqualFold(*col.Default, "NULL")) {
		return fmt.Errorf("a NOT NULL column added to an existing table needs a default value")
	}

	def, err := columnDefinition(col, false)
	if err != nil {
		return err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	if _, err := h.Writer().ExecContext(ctx,
		"ALTER TABLE "+quotedTable+" ADD COLUMN "+def); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// RenameColumn renames a column in place.
func (s *Service) RenameColumn(ctx context.Context, dbName, tableName, oldName, newName string) error {
	quotedTable, err := database.QuoteIdent(tableName)
	if err != nil {
		return err
	}
	from, err := database.QuoteIdent(oldName)
	if err != nil {
		return err
	}
	to, err := database.QuoteIdent(newName)
	if err != nil {
		return err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	if _, err := h.Writer().ExecContext(ctx,
		"ALTER TABLE "+quotedTable+" RENAME COLUMN "+from+" TO "+to); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// DropColumn removes a column.
func (s *Service) DropColumn(ctx context.Context, dbName, tableName, colName string) error {
	quotedTable, err := database.QuoteIdent(tableName)
	if err != nil {
		return err
	}
	quotedCol, err := database.QuoteIdent(colName)
	if err != nil {
		return err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	if _, err := h.Writer().ExecContext(ctx,
		"ALTER TABLE "+quotedTable+" DROP COLUMN "+quotedCol); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// CreateIndexInput describes an index to create.
type CreateIndexInput struct {
	Name    string   `json:"name"`
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

// CreateIndex creates an index over one or more columns.
func (s *Service) CreateIndex(ctx context.Context, dbName string, in CreateIndexInput) error {
	quotedIndex, err := database.QuoteIdent(in.Name)
	if err != nil {
		return fmt.Errorf("index name: %w", err)
	}
	quotedTable, err := database.QuoteIdent(in.Table)
	if err != nil {
		return fmt.Errorf("table name: %w", err)
	}
	if len(in.Columns) == 0 {
		return fmt.Errorf("an index needs at least one column")
	}

	cols := make([]string, 0, len(in.Columns))
	for _, c := range in.Columns {
		q, err := database.QuoteIdent(c)
		if err != nil {
			return fmt.Errorf("index column: %w", err)
		}
		cols = append(cols, q)
	}

	stmt := "CREATE "
	if in.Unique {
		stmt += "UNIQUE "
	}
	stmt += "INDEX " + quotedIndex + " ON " + quotedTable + " (" + strings.Join(cols, ", ") + ")"

	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	if _, err := h.Writer().ExecContext(ctx, stmt); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// DropIndex removes an index.
func (s *Service) DropIndex(ctx context.Context, dbName, indexName string) error {
	quoted, err := database.QuoteIdent(indexName)
	if err != nil {
		return err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	// Indexes SQLite created for a UNIQUE or PRIMARY KEY constraint have no
	// CREATE statement and cannot be dropped on their own.
	var origin sql.NullString
	if err := h.Reader().QueryRowContext(ctx,
		`SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = ?`, indexName).
		Scan(&origin); err == sql.ErrNoRows {
		return fmt.Errorf("%w: index %q", ErrObjectNotFound, indexName)
	} else if err != nil {
		return err
	}
	if !origin.Valid {
		return fmt.Errorf("index %q was created by a UNIQUE or PRIMARY KEY constraint and cannot be dropped on its own", indexName)
	}

	if _, err := h.Writer().ExecContext(ctx, "DROP INDEX "+quoted); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// DropTrigger removes a trigger.
func (s *Service) DropTrigger(ctx context.Context, dbName, name string) error {
	quoted, err := database.QuoteIdent(name)
	if err != nil {
		return err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	if _, err := h.Writer().ExecContext(ctx, "DROP TRIGGER "+quoted); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// DropView removes a view.
func (s *Service) DropView(ctx context.Context, dbName, name string) error {
	quoted, err := database.QuoteIdent(name)
	if err != nil {
		return err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	if _, err := h.Writer().ExecContext(ctx, "DROP VIEW "+quoted); err != nil {
		return wrapSQLiteError(err)
	}
	return nil
}

// wrapSQLiteError turns a driver error into a message an operator can act on,
// without exposing internals.
func wrapSQLiteError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "already exists"):
		return fmt.Errorf("%w: %s", database.ErrAlreadyExists, msg)
	case strings.Contains(lower, "no such table"), strings.Contains(lower, "no such index"),
		strings.Contains(lower, "no such column"), strings.Contains(lower, "no such trigger"):
		return fmt.Errorf("%w: %s", ErrObjectNotFound, msg)
	}
	return err
}
