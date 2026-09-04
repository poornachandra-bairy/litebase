package tables

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/litebase/litebase/internal/database"
)

// Service reads and modifies the schema of user databases.
type Service struct {
	mgr *database.Manager
}

// NewService builds a schema service.
func NewService(mgr *database.Manager) *Service { return &Service{mgr: mgr} }

// ErrObjectNotFound is returned when a named schema object does not exist.
var ErrObjectNotFound = errors.New("schema object not found")

// GetSchema lists every user-visible object in a database.
func (s *Service) GetSchema(ctx context.Context, dbName string) (*Schema, error) {
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return nil, err
	}
	defer h.Release()

	schema := &Schema{Tables: []TableSummary{}, Views: []View{}, Indexes: []Index{}, Triggers: []Trigger{}}

	// sqlite_% is SQLite's own bookkeeping (sqlite_sequence, sqlite_stat1) and
	// is deliberately hidden from the dashboard.
	rows, err := h.Reader().QueryContext(ctx,
		`SELECT type, name, COALESCE(tbl_name, ''), COALESCE(sql, '')
		 FROM sqlite_schema
		 WHERE name NOT LIKE 'sqlite_%'
		 ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	defer rows.Close()

	type tableRef struct{ name, sql string }
	var tableRefs []tableRef

	for rows.Next() {
		var kind, name, tblName, sqlText string
		if err := rows.Scan(&kind, &name, &tblName, &sqlText); err != nil {
			return nil, err
		}
		switch ObjectKind(kind) {
		case KindTable:
			tableRefs = append(tableRefs, tableRef{name, sqlText})
		case KindView:
			schema.Views = append(schema.Views, View{Name: name, SQL: sqlText})
		case KindTrigger:
			schema.Triggers = append(schema.Triggers, Trigger{Name: name, Table: tblName, SQL: sqlText})
		case KindIndex:
			// Indexes created implicitly by UNIQUE or PRIMARY KEY have no SQL
			// text; they are reported per table rather than as standalone
			// objects the operator can drop.
			if sqlText != "" {
				schema.Indexes = append(schema.Indexes, Index{Name: name, Table: tblName, SQL: sqlText, Origin: "c"})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, t := range tableRefs {
		summary := TableSummary{
			Name:         t.name,
			Kind:         string(KindTable),
			WithoutRowID: hasTableOption(t.sql, "WITHOUT ROWID"),
			Strict:       hasTableOption(t.sql, "STRICT"),
		}
		cols, err := s.columnsOf(ctx, h.Reader(), t.name)
		if err != nil {
			return nil, err
		}
		summary.ColumnCount = len(cols)
		if summary.RowCount, err = countRows(ctx, h.Reader(), t.name); err != nil {
			return nil, err
		}
		schema.Tables = append(schema.Tables, summary)
	}

	// Fill in the column list for each standalone index.
	for i := range schema.Indexes {
		cols, unique, partial, origin, err := indexDetail(ctx, h.Reader(), schema.Indexes[i].Table, schema.Indexes[i].Name)
		if err != nil {
			return nil, err
		}
		schema.Indexes[i].Columns = cols
		schema.Indexes[i].Unique = unique
		schema.Indexes[i].Partial = partial
		if origin != "" {
			schema.Indexes[i].Origin = origin
		}
	}
	return schema, nil
}

// GetTable returns the full description of one table or view.
func (s *Service) GetTable(ctx context.Context, dbName, tableName string) (*Table, error) {
	if err := database.ValidateIdentifier(tableName); err != nil {
		return nil, err
	}
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return nil, err
	}
	defer h.Release()

	var kind, sqlText string
	err = h.Reader().QueryRowContext(ctx,
		`SELECT type, COALESCE(sql, '') FROM sqlite_schema
		 WHERE name = ? AND type IN ('table', 'view')`, tableName).Scan(&kind, &sqlText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", ErrObjectNotFound, tableName)
	}
	if err != nil {
		return nil, err
	}

	t := &Table{
		Name:         tableName,
		Kind:         kind,
		SQL:          sqlText,
		WithoutRowID: hasTableOption(sqlText, "WITHOUT ROWID"),
		Strict:       hasTableOption(sqlText, "STRICT"),
		ForeignKeys:  []ForeignKey{},
		Indexes:      []Index{},
		Triggers:     []Trigger{},
	}
	if t.Columns, err = s.columnsOf(ctx, h.Reader(), tableName); err != nil {
		return nil, err
	}
	if t.RowCount, err = countRows(ctx, h.Reader(), tableName); err != nil {
		return nil, err
	}
	// Views have no constraints, indexes or triggers of their own.
	if ObjectKind(kind) != KindTable {
		return t, nil
	}

	if t.ForeignKeys, err = foreignKeysOf(ctx, h.Reader(), tableName); err != nil {
		return nil, err
	}
	if t.Indexes, err = indexesOf(ctx, h.Reader(), tableName); err != nil {
		return nil, err
	}
	if t.Triggers, err = triggersOf(ctx, h.Reader(), tableName); err != nil {
		return nil, err
	}

	// Mark columns that a single-column unique index covers, so the dashboard
	// can show the constraint against the column rather than only as an index.
	for _, idx := range t.Indexes {
		if !idx.Unique || len(idx.Columns) != 1 {
			continue
		}
		for i := range t.Columns {
			if t.Columns[i].Name == idx.Columns[0] {
				t.Columns[i].Unique = true
			}
		}
	}
	// Attach single-column foreign keys to their column.
	for _, fk := range t.ForeignKeys {
		if len(fk.From) != 1 || len(fk.To) != 1 {
			continue
		}
		for i := range t.Columns {
			if t.Columns[i].Name == fk.From[0] {
				t.Columns[i].References = &ForeignKeyRef{
					Table: fk.Table, Column: fk.To[0],
					OnDelete: fk.OnDelete, OnUpdate: fk.OnUpdate,
				}
			}
		}
	}
	return t, nil
}

// columnsOf reads a table's columns.
//
// Every pragma below is called as a table-valued function so the object name
// travels as a bound parameter rather than as interpolated SQL text.
func (s *Service) columnsOf(ctx context.Context, db *sql.DB, table string) ([]Column, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT cid, name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("read columns: %w", err)
	}
	defer rows.Close()

	cols := []Column{}
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		c := Column{
			Name:       name,
			Type:       strings.ToUpper(typ),
			NotNull:    notNull != 0,
			PrimaryKey: pk > 0,
			Position:   cid,
		}
		if dflt.Valid {
			v := dflt.String
			c.Default = &v
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// AUTOINCREMENT leaves no trace in table_info; it is only visible in the
	// original CREATE statement.
	var createSQL string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(sql, '') FROM sqlite_schema WHERE name = ? AND type = 'table'`,
		table).Scan(&createSQL); err == nil && strings.Contains(strings.ToUpper(createSQL), "AUTOINCREMENT") {
		for i := range cols {
			if cols[i].PrimaryKey && strings.EqualFold(cols[i].Type, "INTEGER") {
				cols[i].AutoIncrement = true
				break
			}
		}
	}
	return cols, nil
}

func foreignKeysOf(ctx context.Context, db *sql.DB, table string) ([]ForeignKey, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, seq, "table", "from", COALESCE("to", ''), on_update, on_delete
		 FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		return nil, fmt.Errorf("read foreign keys: %w", err)
	}
	defer rows.Close()

	// A composite foreign key appears as several rows sharing an id.
	byID := map[int]*ForeignKey{}
	order := []int{}
	for rows.Next() {
		var id, seq int
		var target, from, to, onUpdate, onDelete string
		if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete); err != nil {
			return nil, err
		}
		fk, ok := byID[id]
		if !ok {
			fk = &ForeignKey{ID: id, Table: target, OnUpdate: onUpdate, OnDelete: onDelete}
			byID[id] = fk
			order = append(order, id)
		}
		fk.From = append(fk.From, from)
		if to != "" {
			fk.To = append(fk.To, to)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]ForeignKey, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

func indexesOf(ctx context.Context, db *sql.DB, table string) ([]Index, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, "unique", origin, partial FROM pragma_index_list(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("read indexes: %w", err)
	}
	defer rows.Close()

	out := []Index{}
	for rows.Next() {
		var name, origin string
		var unique, partial int
		if err := rows.Scan(&name, &unique, &origin, &partial); err != nil {
			return nil, err
		}
		out = append(out, Index{
			Name: name, Table: table, Unique: unique != 0,
			Partial: partial != 0, Origin: origin,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		cols, err := indexColumns(ctx, db, out[i].Name)
		if err != nil {
			return nil, err
		}
		out[i].Columns = cols
		var sqlText sql.NullString
		if err := db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = ?`,
			out[i].Name).Scan(&sqlText); err == nil && sqlText.Valid {
			out[i].SQL = sqlText.String
		}
	}
	return out, nil
}

func indexColumns(ctx context.Context, db *sql.DB, indexName string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT COALESCE(name, '') FROM pragma_index_info(?) ORDER BY seqno`, indexName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		// An expression index reports a NULL column name.
		if name != "" {
			cols = append(cols, name)
		}
	}
	return cols, rows.Err()
}

func indexDetail(ctx context.Context, db *sql.DB, table, indexName string) (cols []string, unique, partial bool, origin string, err error) {
	if table != "" {
		rows, qErr := db.QueryContext(ctx,
			`SELECT "unique", origin, partial FROM pragma_index_list(?) WHERE name = ?`, table, indexName)
		if qErr != nil {
			return nil, false, false, "", qErr
		}
		defer rows.Close()
		if rows.Next() {
			var u, p int
			if err := rows.Scan(&u, &origin, &p); err != nil {
				return nil, false, false, "", err
			}
			unique, partial = u != 0, p != 0
		}
	}
	cols, err = indexColumns(ctx, db, indexName)
	return cols, unique, partial, origin, err
}

func triggersOf(ctx context.Context, db *sql.DB, table string) ([]Trigger, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, COALESCE(sql, '') FROM sqlite_schema
		 WHERE type = 'trigger' AND tbl_name = ? ORDER BY name`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Trigger{}
	for rows.Next() {
		var t Trigger
		if err := rows.Scan(&t.Name, &t.SQL); err != nil {
			return nil, err
		}
		t.Table = table
		out = append(out, t)
	}
	return out, rows.Err()
}

// countRows returns a table's row count.
//
// The table name is validated and quoted rather than bound, because SQLite has
// no way to parameterise the table in a FROM clause.
func countRows(ctx context.Context, db *sql.DB, table string) (int64, error) {
	quoted := database.MustQuoteIdent(table)
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+quoted).Scan(&n); err != nil {
		// A view over a missing table, or one with an error, should not fail
		// the whole schema listing.
		return 0, nil
	}
	return n, nil
}

// hasTableOption reports whether a CREATE TABLE statement carries a trailing
// option such as STRICT or WITHOUT ROWID.
func hasTableOption(createSQL, option string) bool {
	upper := strings.ToUpper(createSQL)
	idx := strings.LastIndex(upper, ")")
	if idx < 0 {
		return false
	}
	return strings.Contains(upper[idx:], option)
}

// GetStats collects size and object counts for a database.
func (s *Service) GetStats(ctx context.Context, dbName string) (*Stats, error) {
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return nil, err
	}
	defer h.Release()

	st := &Stats{Name: dbName, CollectedAt: time.Now().UTC()}
	db := h.Reader()

	scalar := func(query string, dst any) {
		_ = db.QueryRowContext(ctx, query).Scan(dst)
	}
	scalar(`PRAGMA page_size`, &st.PageSize)
	scalar(`PRAGMA page_count`, &st.PageCount)
	scalar(`PRAGMA freelist_count`, &st.FreelistCount)
	scalar(`PRAGMA encoding`, &st.Encoding)
	scalar(`PRAGMA journal_mode`, &st.JournalMode)
	scalar(`PRAGMA user_version`, &st.UserVersion)
	scalar(`PRAGMA schema_version`, &st.SchemaVersion)

	var fk int
	scalar(`PRAGMA foreign_keys`, &fk)
	st.ForeignKeys = fk != 0
	st.SizeBytes = int64(st.PageSize) * st.PageCount

	if err := db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(type = 'table'), 0),
			COALESCE(SUM(type = 'view'), 0),
			COALESCE(SUM(type = 'index'), 0),
			COALESCE(SUM(type = 'trigger'), 0)
		FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).
		Scan(&st.TableCount, &st.ViewCount, &st.IndexCount, &st.TriggerCount); err != nil {
		return nil, err
	}

	names, err := tableNames(ctx, db)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		c, _ := countRows(ctx, db, n)
		st.TotalRows += c
	}
	return st, nil
}

func tableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CheckIntegrity runs SQLite's consistency checks.
func (s *Service) CheckIntegrity(ctx context.Context, dbName string) (*IntegrityReport, error) {
	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return nil, err
	}
	defer h.Release()

	start := time.Now()
	report := &IntegrityReport{OK: true, Problems: []string{}, ForeignKeyErrors: []FKViolation{}}

	rows, err := h.Reader().QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return nil, fmt.Errorf("integrity check: %w", err)
	}
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			rows.Close()
			return nil, err
		}
		// A healthy database reports the single row "ok".
		if !strings.EqualFold(msg, "ok") {
			report.OK = false
			report.Problems = append(report.Problems, msg)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	fkRows, err := h.Reader().QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		// foreign_key_check is unavailable on some builds; the integrity
		// result still stands on its own.
		report.Duration = time.Since(start).String()
		return report, nil
	}
	defer fkRows.Close()

	for fkRows.Next() {
		var v FKViolation
		var rowid sql.NullInt64
		if err := fkRows.Scan(&v.Table, &rowid, &v.Parent, &v.FKIndex); err != nil {
			return nil, err
		}
		if rowid.Valid {
			id := rowid.Int64
			v.RowID = &id
		}
		report.OK = false
		report.ForeignKeyErrors = append(report.ForeignKeyErrors, v)
	}
	report.Duration = time.Since(start).String()
	return report, nil
}
