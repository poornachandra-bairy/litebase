package rows

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/tables"
)

var (
	ErrNoRow        = errors.New("row not found")
	ErrNoPrimaryKey = errors.New("table has no primary key, so individual rows cannot be addressed")
)

// Service reads and writes the contents of user tables.
type Service struct {
	mgr     *database.Manager
	schema  *tables.Service
	maxRows int
}

// NewService builds a row service. maxRows caps how many rows one request may
// return, protecting memory regardless of what a client asks for.
func NewService(mgr *database.Manager, schema *tables.Service, maxRows int) *Service {
	if maxRows <= 0 {
		maxRows = 1000
	}
	return &Service{mgr: mgr, schema: schema, maxRows: maxRows}
}

// ListOptions describes a page of rows to fetch.
type ListOptions struct {
	Filters []Filter `json:"filters"`
	Sorts   []Sort   `json:"sorts"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
	// Search matches the term against every text-like column.
	Search string `json:"search"`
	// Columns restricts the projection; empty means all columns.
	Columns []string `json:"columns"`
}

// Page is a page of rows plus the metadata a browser needs to paginate.
type Page struct {
	Rows       []map[string]any `json:"rows"`
	Columns    []string         `json:"columns"`
	Total      int64            `json:"total"`
	Limit      int              `json:"limit"`
	Offset     int              `json:"offset"`
	HasMore    bool             `json:"has_more"`
	PrimaryKey []string         `json:"primary_key"`
}

// tableMeta caches what the row operations need to know about a table.
type tableMeta struct {
	columns     []tables.Column
	columnTypes map[string]string
	primaryKey  []string
	isView      bool
	// rowIDUsable reports whether the implicit rowid can address a row, which
	// is the fallback when a table declares no primary key.
	rowIDUsable bool
}

func (s *Service) describe(ctx context.Context, dbName, tableName string) (*tableMeta, error) {
	t, err := s.schema.GetTable(ctx, dbName, tableName)
	if err != nil {
		return nil, err
	}

	meta := &tableMeta{
		columns:     t.Columns,
		columnTypes: make(map[string]string, len(t.Columns)),
		isView:      tables.ObjectKind(t.Kind) == tables.KindView,
	}
	for _, c := range t.Columns {
		meta.columnTypes[c.Name] = c.Type
		if c.PrimaryKey {
			meta.primaryKey = append(meta.primaryKey, c.Name)
		}
	}
	// A WITHOUT ROWID table has no implicit rowid to fall back on, and neither
	// does a view.
	meta.rowIDUsable = !meta.isView && !t.WithoutRowID
	return meta, nil
}

// List returns a page of rows.
func (s *Service) List(ctx context.Context, dbName, tableName string, opts ListOptions) (*Page, error) {
	if err := database.ValidateIdentifier(tableName); err != nil {
		return nil, err
	}
	meta, err := s.describe(ctx, dbName, tableName)
	if err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > s.maxRows {
		limit = s.maxRows
	}
	offset := max(opts.Offset, 0)

	filters := opts.Filters
	if term := strings.TrimSpace(opts.Search); term != "" {
		searchFilter, err := s.buildSearch(term, meta)
		if err != nil {
			return nil, err
		}
		if searchFilter != "" {
			// The search clause is appended as pre-built SQL with its own
			// arguments, so it is handled separately from the filter list.
			return s.listWithSearch(ctx, dbName, tableName, meta, filters, opts, searchFilter, limit, offset)
		}
	}

	where, args, err := buildWhere(filters, meta.columnTypes)
	if err != nil {
		return nil, err
	}
	orderBy, err := buildOrderBy(opts.Sorts, meta.columnTypes)
	if err != nil {
		return nil, err
	}
	projection, selected, err := buildProjection(opts.Columns, meta)
	if err != nil {
		return nil, err
	}
	return s.runList(ctx, dbName, tableName, meta, projection, selected, where, orderBy, args, limit, offset)
}

// buildSearch renders a clause matching term against every text-like column.
func (s *Service) buildSearch(term string, meta *tableMeta) (string, error) {
	var parts []string
	for _, c := range meta.columns {
		// Blob columns hold binary data that a text search cannot meaningfully
		// match, and coercing them would be wasteful on large values.
		if database.NormalizeType(c.Type) == "BLOB" {
			continue
		}
		quoted, err := database.QuoteIdent(c.Name)
		if err != nil {
			// A column whose name our validator rejects still exists in the
			// database; skip it rather than failing the search.
			continue
		}
		// CAST lets a numeric column match a typed digit string too.
		parts = append(parts, "CAST("+quoted+" AS TEXT) LIKE ? ESCAPE '\\'")
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", nil
}

func (s *Service) listWithSearch(ctx context.Context, dbName, tableName string, meta *tableMeta,
	filters []Filter, opts ListOptions, searchClause string, limit, offset int) (*Page, error) {

	where, args, err := buildWhere(filters, meta.columnTypes)
	if err != nil {
		return nil, err
	}
	pattern := "%" + escapeLikePattern(strings.TrimSpace(opts.Search)) + "%"
	// One bound argument per column referenced in the search clause.
	searchArgs := make([]any, strings.Count(searchClause, "?"))
	for i := range searchArgs {
		searchArgs[i] = pattern
	}

	if where == "" {
		where = " WHERE " + searchClause
	} else {
		where += " AND " + searchClause
	}
	args = append(args, searchArgs...)

	orderBy, err := buildOrderBy(opts.Sorts, meta.columnTypes)
	if err != nil {
		return nil, err
	}
	projection, selected, err := buildProjection(opts.Columns, meta)
	if err != nil {
		return nil, err
	}
	return s.runList(ctx, dbName, tableName, meta, projection, selected, where, orderBy, args, limit, offset)
}

func buildProjection(requested []string, meta *tableMeta) (string, []string, error) {
	if len(requested) == 0 {
		names := make([]string, 0, len(meta.columns))
		quoted := make([]string, 0, len(meta.columns))
		for _, c := range meta.columns {
			names = append(names, c.Name)
			quoted = append(quoted, database.MustQuoteIdent(c.Name))
		}
		return strings.Join(quoted, ", "), names, nil
	}

	names := make([]string, 0, len(requested))
	quoted := make([]string, 0, len(requested))
	for _, name := range requested {
		if _, ok := meta.columnTypes[name]; !ok {
			return "", nil, fmt.Errorf("unknown column %q", name)
		}
		q, err := database.QuoteIdent(name)
		if err != nil {
			return "", nil, err
		}
		names = append(names, name)
		quoted = append(quoted, q)
	}
	return strings.Join(quoted, ", "), names, nil
}

func (s *Service) runList(ctx context.Context, dbName, tableName string, meta *tableMeta,
	projection string, selected []string, where, orderBy string, args []any, limit, offset int) (*Page, error) {

	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return nil, err
	}
	defer h.Release()

	quotedTable := database.MustQuoteIdent(tableName)

	var total int64
	countSQL := "SELECT count(*) FROM " + quotedTable + where
	if err := h.Reader().QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count rows: %w", translate(err))
	}

	// The rowid is selected alongside the data so the browser can address rows
	// in a table that declares no primary key.
	selectList := projection
	includeRowID := meta.rowIDUsable && len(meta.primaryKey) == 0
	if includeRowID {
		selectList = "rowid AS " + database.MustQuoteIdent("_rowid") + ", " + projection
	}

	// LIMIT and OFFSET are bound rather than formatted into the statement.
	querySQL := "SELECT " + selectList + " FROM " + quotedTable + where + orderBy + " LIMIT ? OFFSET ?"
	queryArgs := append(append([]any{}, args...), limit, offset)

	rows, err := h.Reader().QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("read rows: %w", translate(err))
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0, limit)
	for rows.Next() {
		values, targets := database.ScanTargets(len(cols))
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		record := make(map[string]any, len(cols))
		for i, name := range cols {
			record[name] = database.NormalizeValue(values[i])
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pk := meta.primaryKey
	if includeRowID {
		pk = []string{"_rowid"}
	}
	return &Page{
		Rows:       out,
		Columns:    selected,
		Total:      total,
		Limit:      limit,
		Offset:     offset,
		HasMore:    int64(offset+len(out)) < total,
		PrimaryKey: pk,
	}, nil
}

// keyCondition builds the WHERE clause addressing a single row by its key.
func keyCondition(meta *tableMeta, key map[string]any) (string, []any, error) {
	if meta.isView {
		return "", nil, fmt.Errorf("rows in a view cannot be edited directly")
	}

	keyCols := meta.primaryKey
	if len(keyCols) == 0 {
		if !meta.rowIDUsable {
			return "", nil, ErrNoPrimaryKey
		}
		keyCols = []string{"_rowid"}
	}

	conditions := make([]string, 0, len(keyCols))
	args := make([]any, 0, len(keyCols))
	for _, col := range keyCols {
		raw, ok := key[col]
		if !ok {
			return "", nil, fmt.Errorf("missing key column %q", col)
		}
		bound, err := database.BindValue(raw)
		if err != nil {
			return "", nil, fmt.Errorf("key column %q: %w", col, err)
		}

		if col == "_rowid" {
			conditions = append(conditions, "rowid = ?")
			args = append(args, bound)
			continue
		}
		quoted, err := database.QuoteIdent(col)
		if err != nil {
			return "", nil, err
		}
		coerced, err := database.CoerceToColumnType(bound, meta.columnTypes[col])
		if err != nil {
			return "", nil, fmt.Errorf("key column %q: %w", col, err)
		}
		conditions = append(conditions, quoted+" = ?")
		args = append(args, coerced)
	}
	return " WHERE " + strings.Join(conditions, " AND "), args, nil
}

// Get returns one row addressed by its primary key.
func (s *Service) Get(ctx context.Context, dbName, tableName string, key map[string]any) (map[string]any, error) {
	if err := database.ValidateIdentifier(tableName); err != nil {
		return nil, err
	}
	meta, err := s.describe(ctx, dbName, tableName)
	if err != nil {
		return nil, err
	}
	where, args, err := keyCondition(meta, key)
	if err != nil {
		return nil, err
	}

	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return nil, err
	}
	defer h.Release()

	projection, _, err := buildProjection(nil, meta)
	if err != nil {
		return nil, err
	}
	selectList := projection
	if meta.rowIDUsable && len(meta.primaryKey) == 0 {
		selectList = "rowid AS " + database.MustQuoteIdent("_rowid") + ", " + projection
	}

	row := h.Reader().QueryRowContext(ctx,
		"SELECT "+selectList+" FROM "+database.MustQuoteIdent(tableName)+where+" LIMIT 1", args...)

	cols := make([]string, 0, len(meta.columns)+1)
	if selectList != projection {
		cols = append(cols, "_rowid")
	}
	for _, c := range meta.columns {
		cols = append(cols, c.Name)
	}

	values, targets := database.ScanTargets(len(cols))
	if err := row.Scan(targets...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoRow
		}
		return nil, translate(err)
	}
	record := make(map[string]any, len(cols))
	for i, name := range cols {
		record[name] = database.NormalizeValue(values[i])
	}
	return record, nil
}

// Insert adds a row and returns it as stored.
func (s *Service) Insert(ctx context.Context, dbName, tableName string, values map[string]any) (map[string]any, error) {
	if err := database.ValidateIdentifier(tableName); err != nil {
		return nil, err
	}
	meta, err := s.describe(ctx, dbName, tableName)
	if err != nil {
		return nil, err
	}
	if meta.isView {
		return nil, fmt.Errorf("rows cannot be inserted into a view")
	}

	cols, placeholders, args, err := prepareWriteColumns(values, meta)
	if err != nil {
		return nil, err
	}

	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return nil, err
	}
	defer h.Release()

	quotedTable := database.MustQuoteIdent(tableName)
	var stmt string
	if len(cols) == 0 {
		// Every column is defaulted; SQLite spells this case out explicitly.
		stmt = "INSERT INTO " + quotedTable + " DEFAULT VALUES"
	} else {
		stmt = "INSERT INTO " + quotedTable + " (" + strings.Join(cols, ", ") + ") VALUES (" + placeholders + ")"
	}

	res, err := h.Writer().ExecContext(ctx, stmt, args...)
	if err != nil {
		return nil, translate(err)
	}

	// Read the row back so the client sees defaults and generated keys.
	if len(meta.primaryKey) == 1 {
		pkCol := meta.primaryKey[0]
		if _, supplied := values[pkCol]; !supplied {
			if id, err := res.LastInsertId(); err == nil && id > 0 {
				return s.Get(ctx, dbName, tableName, map[string]any{pkCol: id})
			}
		}
		if v, ok := values[pkCol]; ok {
			return s.Get(ctx, dbName, tableName, map[string]any{pkCol: v})
		}
	}
	if len(meta.primaryKey) == 0 && meta.rowIDUsable {
		if id, err := res.LastInsertId(); err == nil {
			return s.Get(ctx, dbName, tableName, map[string]any{"_rowid": id})
		}
	}
	if len(meta.primaryKey) > 1 {
		key := make(map[string]any, len(meta.primaryKey))
		complete := true
		for _, c := range meta.primaryKey {
			v, ok := values[c]
			if !ok {
				complete = false
				break
			}
			key[c] = v
		}
		if complete {
			return s.Get(ctx, dbName, tableName, key)
		}
	}
	// The row was written but cannot be addressed for read-back.
	return values, nil
}

// prepareWriteColumns validates the supplied values against the table's real
// columns and returns quoted names, placeholders and bound arguments.
func prepareWriteColumns(values map[string]any, meta *tableMeta) (cols []string, placeholders string, args []any, err error) {
	// Iterating the table's columns rather than the input map gives a stable
	// order and silently drops nothing: an unknown key is an error below.
	known := make(map[string]bool, len(values))
	for _, c := range meta.columns {
		raw, ok := values[c.Name]
		if !ok {
			continue
		}
		known[c.Name] = true

		quoted, qErr := database.QuoteIdent(c.Name)
		if qErr != nil {
			return nil, "", nil, qErr
		}
		bound, bErr := database.BindValue(raw)
		if bErr != nil {
			return nil, "", nil, fmt.Errorf("column %q: %w", c.Name, bErr)
		}
		coerced, cErr := database.CoerceToColumnType(bound, c.Type)
		if cErr != nil {
			return nil, "", nil, fmt.Errorf("column %q: %w", c.Name, cErr)
		}
		cols = append(cols, quoted)
		args = append(args, coerced)
	}

	for name := range values {
		if !known[name] && name != "_rowid" {
			return nil, "", nil, fmt.Errorf("unknown column %q", name)
		}
	}
	placeholders = strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	return cols, placeholders, args, nil
}

// Update modifies one row addressed by its key.
func (s *Service) Update(ctx context.Context, dbName, tableName string, key, values map[string]any) (map[string]any, error) {
	if err := database.ValidateIdentifier(tableName); err != nil {
		return nil, err
	}
	meta, err := s.describe(ctx, dbName, tableName)
	if err != nil {
		return nil, err
	}
	if meta.isView {
		return nil, fmt.Errorf("rows in a view cannot be updated")
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("no columns to update")
	}

	cols, _, args, err := prepareWriteColumns(values, meta)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("no known columns to update")
	}

	where, whereArgs, err := keyCondition(meta, key)
	if err != nil {
		return nil, err
	}

	assignments := make([]string, len(cols))
	for i, c := range cols {
		assignments[i] = c + " = ?"
	}
	stmt := "UPDATE " + database.MustQuoteIdent(tableName) +
		" SET " + strings.Join(assignments, ", ") + where

	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return nil, err
	}
	defer h.Release()

	res, err := h.Writer().ExecContext(ctx, stmt, append(args, whereArgs...)...)
	if err != nil {
		return nil, translate(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNoRow
	}

	// Re-read using the updated key values where the key itself changed.
	newKey := make(map[string]any, len(key))
	for k, v := range key {
		if updated, ok := values[k]; ok {
			newKey[k] = updated
		} else {
			newKey[k] = v
		}
	}
	return s.Get(ctx, dbName, tableName, newKey)
}

// Delete removes one row addressed by its key.
func (s *Service) Delete(ctx context.Context, dbName, tableName string, key map[string]any) error {
	if err := database.ValidateIdentifier(tableName); err != nil {
		return err
	}
	meta, err := s.describe(ctx, dbName, tableName)
	if err != nil {
		return err
	}
	if meta.isView {
		return fmt.Errorf("rows cannot be deleted from a view")
	}
	where, args, err := keyCondition(meta, key)
	if err != nil {
		return err
	}

	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return err
	}
	defer h.Release()

	res, err := h.Writer().ExecContext(ctx,
		"DELETE FROM "+database.MustQuoteIdent(tableName)+where, args...)
	if err != nil {
		return translate(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoRow
	}
	return nil
}

// DeleteMany removes several rows in one transaction, so a failure part-way
// through leaves the table untouched.
func (s *Service) DeleteMany(ctx context.Context, dbName, tableName string, keys []map[string]any) (int, error) {
	if err := database.ValidateIdentifier(tableName); err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	if len(keys) > 1000 {
		return 0, fmt.Errorf("cannot delete more than 1000 rows at once")
	}
	meta, err := s.describe(ctx, dbName, tableName)
	if err != nil {
		return 0, err
	}
	if meta.isView {
		return 0, fmt.Errorf("rows cannot be deleted from a view")
	}

	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return 0, err
	}
	defer h.Release()

	deleted := 0
	err = h.Tx(ctx, func(tx *sql.Tx) error {
		for _, key := range keys {
			where, args, err := keyCondition(meta, key)
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx,
				"DELETE FROM "+database.MustQuoteIdent(tableName)+where, args...)
			if err != nil {
				return translate(err)
			}
			n, _ := res.RowsAffected()
			deleted += int(n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// translate turns SQLite constraint errors into messages an operator can act
// on, without leaking internals.
func translate(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "unique constraint failed"):
		return fmt.Errorf("a row with these values already exists (%s)", trimConstraint(msg))
	case strings.Contains(lower, "foreign key constraint failed"):
		return fmt.Errorf("this change would break a foreign key reference")
	case strings.Contains(lower, "not null constraint failed"):
		return fmt.Errorf("a required value is missing (%s)", trimConstraint(msg))
	case strings.Contains(lower, "check constraint failed"):
		return fmt.Errorf("a value failed a CHECK constraint (%s)", trimConstraint(msg))
	case strings.Contains(lower, "no such table"):
		return fmt.Errorf("%w: %s", tables.ErrObjectNotFound, msg)
	case strings.Contains(lower, "datatype mismatch"):
		return fmt.Errorf("a value does not match its column type")
	}
	return err
}

// trimConstraint extracts the "table.column" that SQLite names in a constraint
// error, which is safe to show and is the part an operator needs.
func trimConstraint(msg string) string {
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		return msg[i+2:]
	}
	return msg
}
