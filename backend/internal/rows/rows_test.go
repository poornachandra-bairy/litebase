package rows

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/tables"
)

func newTestService(t *testing.T) (*Service, *tables.Service) {
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
	if _, err := m.Create(ctx, "app", ""); err != nil {
		t.Fatal(err)
	}
	schema := tables.NewService(m)
	if err := schema.CreateTable(ctx, "app", tables.CreateTableInput{
		Name: "products",
		Columns: []tables.Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT", NotNull: true},
			{Name: "category", Type: "TEXT"},
			{Name: "price", Type: "REAL"},
			{Name: "sku", Type: "TEXT", Unique: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return NewService(m, schema, 1000), schema
}

func seed(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	items := []map[string]any{
		{"name": "Widget", "category": "tools", "price": 9.99, "sku": "W-1"},
		{"name": "Gadget", "category": "tools", "price": 24.50, "sku": "G-1"},
		{"name": "Doohickey", "category": "toys", "price": 5.00, "sku": "D-1"},
		{"name": "50% off sign", "category": "signs", "price": 1.00, "sku": "S-1"},
	}
	for _, it := range items {
		if _, err := s.Insert(ctx, "app", "products", it); err != nil {
			t.Fatalf("seed insert %v: %v", it, err)
		}
	}
}

func TestInsertReadBack(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)

	row, err := s.Insert(ctx, "app", "products", map[string]any{
		"name": "Widget", "category": "tools", "price": 9.99, "sku": "W-1",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// The generated primary key must come back to the client.
	if row["id"] == nil {
		t.Error("insert did not return the generated id")
	}
	if row["name"] != "Widget" {
		t.Errorf("name = %v, want Widget", row["name"])
	}
}

func TestInsertRejectsUnknownColumn(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	_, err := s.Insert(ctx, "app", "products", map[string]any{
		"name": "X", "nonexistent": 1,
	})
	if err == nil {
		t.Error("insert with an unknown column was accepted")
	}
}

func TestInsertConstraintErrorsAreReadable(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	if _, err := s.Insert(ctx, "app", "products", map[string]any{"name": "A", "sku": "X-1"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Insert(ctx, "app", "products", map[string]any{"name": "B", "sku": "X-1"})
	if err == nil {
		t.Fatal("duplicate sku accepted")
	}
	if got := err.Error(); got == "" || !contains(got, "already exists") {
		t.Errorf("error = %q, want a readable uniqueness message", got)
	}

	// A missing NOT NULL value must also be explained.
	_, err = s.Insert(ctx, "app", "products", map[string]any{"category": "x"})
	if err == nil || !contains(err.Error(), "required value is missing") {
		t.Errorf("NOT NULL error = %v, want a readable message", err)
	}
}

func TestListFilterSortPaginate(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	seed(t, s)

	page, err := s.List(ctx, "app", "products", ListOptions{
		Filters: []Filter{{Column: "category", Op: OpEq, Value: "tools"}},
		Sorts:   []Sort{{Column: "price", Desc: true}},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Total != 2 {
		t.Errorf("total = %d, want 2", page.Total)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(page.Rows))
	}
	if page.Rows[0]["name"] != "Gadget" {
		t.Errorf("first row = %v, want Gadget (descending price)", page.Rows[0]["name"])
	}
	if len(page.PrimaryKey) != 1 || page.PrimaryKey[0] != "id" {
		t.Errorf("primary key = %v, want [id]", page.PrimaryKey)
	}

	// Pagination.
	first, err := s.List(ctx, "app", "products", ListOptions{
		Limit: 2, Offset: 0, Sorts: []Sort{{Column: "id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 2 || !first.HasMore {
		t.Errorf("first page: rows=%d has_more=%v, want 2 and true", len(first.Rows), first.HasMore)
	}
	last, err := s.List(ctx, "app", "products", ListOptions{
		Limit: 2, Offset: 2, Sorts: []Sort{{Column: "id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if last.HasMore {
		t.Error("last page reports more rows")
	}
}

func TestListRejectsUnknownFilterAndSortColumns(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)

	// A filter column is never interpolated, but an unknown one must still be
	// refused rather than silently ignored.
	if _, err := s.List(ctx, "app", "products", ListOptions{
		Filters: []Filter{{Column: "price\"; DROP TABLE products;--", Op: OpEq, Value: 1}},
	}); err == nil {
		t.Error("filter on an injected column name accepted")
	}
	if _, err := s.List(ctx, "app", "products", ListOptions{
		Sorts: []Sort{{Column: "id; DROP TABLE products"}},
	}); err == nil {
		t.Error("sort on an injected column name accepted")
	}

	// The table must survive both attempts.
	page, err := s.List(ctx, "app", "products", ListOptions{})
	if err != nil {
		t.Fatalf("table damaged by injection attempt: %v", err)
	}
	_ = page
}

func TestSearchAcrossColumns(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	seed(t, s)

	page, err := s.List(ctx, "app", "products", ListOptions{Search: "toys"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if page.Total != 1 || page.Rows[0]["name"] != "Doohickey" {
		t.Errorf("search for 'toys' returned %d rows: %v", page.Total, page.Rows)
	}
}

func TestSearchTreatsWildcardsLiterally(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	seed(t, s)

	// "%" must match the literal character, not act as a wildcard matching
	// every row.
	page, err := s.List(ctx, "app", "products", ListOptions{Search: "50%"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if page.Total != 1 {
		t.Errorf("search for '50%%' matched %d rows, want 1; wildcards are not escaped", page.Total)
	}
}

func TestContainsFilterEscapesWildcards(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	seed(t, s)

	page, err := s.List(ctx, "app", "products", ListOptions{
		Filters: []Filter{{Column: "name", Op: OpContains, Value: "%"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Errorf("contains '%%' matched %d rows, want 1", page.Total)
	}
}

func TestFilterOperators(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	seed(t, s)

	cases := []struct {
		name   string
		filter Filter
		want   int64
	}{
		{"gt", Filter{Column: "price", Op: OpGt, Value: 9.99}, 1},
		{"gte", Filter{Column: "price", Op: OpGte, Value: 9.99}, 2},
		{"lt", Filter{Column: "price", Op: OpLt, Value: 9.99}, 2},
		{"neq", Filter{Column: "category", Op: OpNeq, Value: "tools"}, 2},
		{"in", Filter{Column: "category", Op: OpIn, Values: []any{"tools", "toys"}}, 3},
		{"not_in", Filter{Column: "category", Op: OpNotIn, Values: []any{"tools"}}, 2},
		{"between", Filter{Column: "price", Op: OpBetween, Values: []any{1.0, 10.0}}, 3},
		{"starts_with", Filter{Column: "name", Op: OpStartsWith, Value: "Wid"}, 1},
		{"ends_with", Filter{Column: "name", Op: OpEndsWith, Value: "get"}, 2},
		{"is_not_null", Filter{Column: "price", Op: OpIsNotNull}, 4},
		{"empty_in", Filter{Column: "category", Op: OpIn, Values: []any{}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := s.List(ctx, "app", "products", ListOptions{Filters: []Filter{tc.filter}})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if page.Total != tc.want {
				t.Errorf("total = %d, want %d", page.Total, tc.want)
			}
		})
	}
}

func TestUpdateAndDelete(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	seed(t, s)

	row, err := s.Update(ctx, "app", "products",
		map[string]any{"id": 1}, map[string]any{"price": 12.5})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if row["price"] != 12.5 {
		t.Errorf("price = %v, want 12.5", row["price"])
	}

	if _, err := s.Update(ctx, "app", "products",
		map[string]any{"id": 9999}, map[string]any{"price": 1}); !errors.Is(err, ErrNoRow) {
		t.Errorf("update of a missing row = %v, want ErrNoRow", err)
	}

	if err := s.Delete(ctx, "app", "products", map[string]any{"id": 1}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(ctx, "app", "products", map[string]any{"id": 1}); !errors.Is(err, ErrNoRow) {
		t.Errorf("second delete = %v, want ErrNoRow", err)
	}

	n, err := s.DeleteMany(ctx, "app", "products",
		[]map[string]any{{"id": 2}, {"id": 3}})
	if err != nil {
		t.Fatalf("DeleteMany: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d rows, want 2", n)
	}
}

func TestNumericStringCoercion(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)

	// A form submits numbers as strings; they must land as numbers so that
	// ordering and comparison behave.
	row, err := s.Insert(ctx, "app", "products", map[string]any{
		"name": "Coerced", "price": "19.99",
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, ok := row["price"].(float64); !ok {
		t.Errorf("price stored as %T, want float64", row["price"])
	}

	if _, err := s.Insert(ctx, "app", "products", map[string]any{
		"name": "Bad", "price": "not-a-number",
	}); err == nil {
		t.Error("non-numeric text accepted for a REAL column")
	}
}

func TestBlobRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, schema := newTestService(t)
	if err := schema.CreateTable(ctx, "app", tables.CreateTableInput{
		Name: "files",
		Columns: []tables.Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
			{Name: "data", Type: "BLOB"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Binary that is not valid UTF-8 must survive as a tagged blob.
	blob := map[string]any{"$type": "blob", "base64": "AAECA/8=", "size": 5}
	row, err := s.Insert(ctx, "app", "files", map[string]any{"data": blob})
	if err != nil {
		t.Fatalf("Insert blob: %v", err)
	}
	got, ok := row["data"].(database.BlobValue)
	if !ok {
		t.Fatalf("data came back as %T, want BlobValue", row["data"])
	}
	if got.Base64 != "AAECA/8=" {
		t.Errorf("blob = %q, want AAECA/8=", got.Base64)
	}
}

func TestTableWithoutPrimaryKeyUsesRowID(t *testing.T) {
	ctx := context.Background()
	s, schema := newTestService(t)
	if err := schema.CreateTable(ctx, "app", tables.CreateTableInput{
		Name:    "logs",
		Columns: []tables.Column{{Name: "message", Type: "TEXT"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert(ctx, "app", "logs", map[string]any{"message": "hello"}); err != nil {
		t.Fatal(err)
	}

	page, err := s.List(ctx, "app", "logs", ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.PrimaryKey) != 1 || page.PrimaryKey[0] != "_rowid" {
		t.Fatalf("primary key = %v, want [_rowid]", page.PrimaryKey)
	}
	rowid := page.Rows[0]["_rowid"]
	if rowid == nil {
		t.Fatal("rowid not returned")
	}
	// The rowid must be usable to address the row for editing.
	if _, err := s.Update(ctx, "app", "logs",
		map[string]any{"_rowid": rowid}, map[string]any{"message": "updated"}); err != nil {
		t.Errorf("update by rowid: %v", err)
	}
}

func TestLimitIsCapped(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	s.maxRows = 2
	seed(t, s)

	page, err := s.List(ctx, "app", "products", ListOptions{Limit: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if page.Limit != 2 || len(page.Rows) != 2 {
		t.Errorf("limit = %d rows = %d, want the configured cap of 2", page.Limit, len(page.Rows))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
