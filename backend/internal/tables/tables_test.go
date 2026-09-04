package tables

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/litebase/litebase/internal/database"
)

func newTestService(t *testing.T) (*Service, *database.Manager) {
	t.Helper()
	dir := t.TempDir()
	m, err := database.NewManager(context.Background(), database.ManagerOptions{
		DatabasesDir: filepath.Join(dir, "databases"),
		MetaPath:     filepath.Join(dir, "litebase.db"),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	if _, err := m.Create(context.Background(), "app", ""); err != nil {
		t.Fatalf("Create database: %v", err)
	}
	return NewService(m), m
}

func TestValidateColumnType(t *testing.T) {
	ok := map[string]string{
		"integer": "INTEGER", "TEXT": "TEXT", "varchar(255)": "VARCHAR(255)",
		"decimal(10,2)": "DECIMAL(10,2)", "": "TEXT", " real ": "REAL",
	}
	for in, want := range ok {
		got, err := ValidateColumnType(in)
		if err != nil {
			t.Errorf("ValidateColumnType(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ValidateColumnType(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"TEXT); DROP TABLE users;--",
		"INTEGER DEFAULT (SELECT 1)",
		"NOTATYPE",
		"TEXT'",
		strings.Repeat("A", 100),
	}
	for _, in := range bad {
		if _, err := ValidateColumnType(in); err == nil {
			t.Errorf("ValidateColumnType(%q) = nil error, want rejection", in)
		}
	}
}

func TestValidateDefault(t *testing.T) {
	ok := map[string]string{
		"42": "42", "-1.5": "-1.5", "'pending'": "'pending'",
		"NULL": "NULL", "current_timestamp": "CURRENT_TIMESTAMP",
		"true": "TRUE", "x'00ff'": "x'00ff'", "'it''s'": "'it''s'",
	}
	for in, want := range ok {
		got, err := ValidateDefault(in)
		if err != nil {
			t.Errorf("ValidateDefault(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ValidateDefault(%q) = %q, want %q", in, got, want)
		}
	}

	// Anything that could execute is refused rather than escaped.
	bad := []string{
		"(SELECT password FROM users)",
		"datetime('now')",
		"1); DROP TABLE t;--",
		"'unterminated",
		"randomblob(16)",
		"CURRENT_TIMESTAMP, x INTEGER DEFAULT 1",
	}
	for _, in := range bad {
		if _, err := ValidateDefault(in); err == nil {
			t.Errorf("ValidateDefault(%q) = nil error, want rejection", in)
		}
	}
}

func TestBuildCreateTableRejectsInjection(t *testing.T) {
	_, err := buildCreateTable(CreateTableInput{
		Name:    "users\"); DROP TABLE admins;--",
		Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
	})
	if err == nil {
		t.Error("malicious table name accepted")
	}

	_, err = buildCreateTable(CreateTableInput{
		Name:    "users",
		Columns: []Column{{Name: "id\" INTEGER, evil TEXT DEFAULT \"", Type: "INTEGER"}},
	})
	if err == nil {
		t.Error("malicious column name accepted")
	}
}

func TestBuildCreateTable(t *testing.T) {
	stmt, err := buildCreateTable(CreateTableInput{
		Name: "products",
		Columns: []Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
			{Name: "name", Type: "text", NotNull: true},
			{Name: "sku", Type: "TEXT", Unique: true},
			{Name: "price", Type: "REAL", Default: strptr("0")},
			{Name: "cat_id", Type: "INTEGER", References: &ForeignKeyRef{
				Table: "categories", Column: "id", OnDelete: "cascade"}},
		},
	})
	if err != nil {
		t.Fatalf("buildCreateTable: %v", err)
	}
	for _, want := range []string{
		`"id" INTEGER PRIMARY KEY AUTOINCREMENT`,
		`"name" TEXT NOT NULL`,
		`"sku" TEXT UNIQUE`,
		`"price" REAL DEFAULT 0`,
		`REFERENCES "categories" ("id") ON DELETE CASCADE`,
	} {
		if !strings.Contains(stmt, want) {
			t.Errorf("statement missing %q:\n%s", want, stmt)
		}
	}
}

func TestBuildCreateTableCompositePrimaryKey(t *testing.T) {
	stmt, err := buildCreateTable(CreateTableInput{
		Name: "memberships",
		Columns: []Column{
			{Name: "user_id", Type: "INTEGER", PrimaryKey: true},
			{Name: "group_id", Type: "INTEGER", PrimaryKey: true},
		},
	})
	if err != nil {
		t.Fatalf("buildCreateTable: %v", err)
	}
	if !strings.Contains(stmt, `PRIMARY KEY ("user_id", "group_id")`) {
		t.Errorf("composite key not rendered as a table constraint:\n%s", stmt)
	}
}

func TestAutoIncrementRequiresIntegerPrimaryKey(t *testing.T) {
	_, err := buildCreateTable(CreateTableInput{
		Name:    "t",
		Columns: []Column{{Name: "id", Type: "TEXT", PrimaryKey: true, AutoIncrement: true}},
	})
	if err == nil {
		t.Error("AUTOINCREMENT on a TEXT primary key accepted")
	}
	_, err = buildCreateTable(CreateTableInput{
		Name:    "t",
		Columns: []Column{{Name: "id", Type: "INTEGER", AutoIncrement: true}},
	})
	if err == nil {
		t.Error("AUTOINCREMENT without a primary key accepted")
	}
}

func TestSchemaLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)

	if err := s.CreateTable(ctx, "app", CreateTableInput{
		Name: "categories",
		Columns: []Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT", NotNull: true, Unique: true},
		},
	}); err != nil {
		t.Fatalf("CreateTable categories: %v", err)
	}
	if err := s.CreateTable(ctx, "app", CreateTableInput{
		Name: "products",
		Columns: []Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT", NotNull: true},
			{Name: "price", Type: "REAL", Default: strptr("0")},
			{Name: "cat_id", Type: "INTEGER", References: &ForeignKeyRef{
				Table: "categories", Column: "id", OnDelete: "CASCADE"}},
		},
	}); err != nil {
		t.Fatalf("CreateTable products: %v", err)
	}

	if err := s.CreateIndex(ctx, "app", CreateIndexInput{
		Name: "idx_products_name", Table: "products", Columns: []string{"name"},
	}); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	tbl, err := s.GetTable(ctx, "app", "products")
	if err != nil {
		t.Fatalf("GetTable: %v", err)
	}
	if len(tbl.Columns) != 4 {
		t.Errorf("column count = %d, want 4", len(tbl.Columns))
	}
	if !tbl.Columns[0].AutoIncrement {
		t.Error("AUTOINCREMENT not detected")
	}
	if len(tbl.ForeignKeys) != 1 || tbl.ForeignKeys[0].Table != "categories" {
		t.Errorf("foreign keys = %+v, want one to categories", tbl.ForeignKeys)
	}
	if tbl.Columns[3].References == nil || tbl.Columns[3].References.OnDelete != "CASCADE" {
		t.Errorf("column foreign key not attached: %+v", tbl.Columns[3].References)
	}

	schema, err := s.GetSchema(ctx, "app")
	if err != nil {
		t.Fatalf("GetSchema: %v", err)
	}
	if len(schema.Tables) != 2 {
		t.Errorf("tables = %d, want 2", len(schema.Tables))
	}
	foundIdx := false
	for _, i := range schema.Indexes {
		if i.Name == "idx_products_name" {
			foundIdx = true
			if len(i.Columns) != 1 || i.Columns[0] != "name" {
				t.Errorf("index columns = %v, want [name]", i.Columns)
			}
		}
	}
	if !foundIdx {
		t.Error("created index missing from schema listing")
	}

	// Column operations.
	if err := s.AddColumn(ctx, "app", "products", Column{
		Name: "sku", Type: "TEXT", NotNull: true, Default: strptr("''"),
	}); err != nil {
		t.Fatalf("AddColumn: %v", err)
	}
	if err := s.RenameColumn(ctx, "app", "products", "sku", "code"); err != nil {
		t.Fatalf("RenameColumn: %v", err)
	}
	if err := s.DropColumn(ctx, "app", "products", "code"); err != nil {
		t.Fatalf("DropColumn: %v", err)
	}

	if err := s.DropIndex(ctx, "app", "idx_products_name"); err != nil {
		t.Fatalf("DropIndex: %v", err)
	}
	if err := s.RenameTable(ctx, "app", "products", "items"); err != nil {
		t.Fatalf("RenameTable: %v", err)
	}
	if err := s.DropTable(ctx, "app", "items"); err != nil {
		t.Fatalf("DropTable: %v", err)
	}
}

func TestAddColumnGuards(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	if err := s.CreateTable(ctx, "app", CreateTableInput{
		Name:    "t",
		Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
	}); err != nil {
		t.Fatal(err)
	}

	// A NOT NULL column with no default would fail inside SQLite; it must be
	// rejected with an explanation first.
	if err := s.AddColumn(ctx, "app", "t", Column{Name: "a", Type: "TEXT", NotNull: true}); err == nil {
		t.Error("NOT NULL column without a default accepted")
	}
	if err := s.AddColumn(ctx, "app", "t", Column{Name: "b", Type: "TEXT", PrimaryKey: true}); err == nil {
		t.Error("primary key column added to an existing table")
	}
	if err := s.AddColumn(ctx, "app", "t", Column{Name: "c", Type: "TEXT", Unique: true}); err == nil {
		t.Error("UNIQUE column added directly")
	}
}

func TestModifyColumnPreservesData(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService(t)

	if err := s.CreateTable(ctx, "app", CreateTableInput{
		Name: "people",
		Columns: []Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT"},
			{Name: "age", Type: "TEXT"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateIndex(ctx, "app", CreateIndexInput{
		Name: "idx_people_name", Table: "people", Columns: []string{"name"},
	}); err != nil {
		t.Fatal(err)
	}

	h, err := m.Get(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		name string
		age  string
	}{{"Ada", "36"}, {"Grace", "45"}} {
		if _, err := h.Writer().ExecContext(ctx,
			`INSERT INTO people(name, age) VALUES(?, ?)`, v.name, v.age); err != nil {
			t.Fatal(err)
		}
	}
	h.Release()

	// Change age from TEXT to INTEGER and make it NOT NULL with a default.
	if err := s.ModifyColumn(ctx, "app", "people", "age", Column{
		Name: "age", Type: "INTEGER", NotNull: true, Default: strptr("0"),
	}); err != nil {
		t.Fatalf("ModifyColumn: %v", err)
	}

	tbl, err := s.GetTable(ctx, "app", "people")
	if err != nil {
		t.Fatal(err)
	}
	var age *Column
	for i := range tbl.Columns {
		if tbl.Columns[i].Name == "age" {
			age = &tbl.Columns[i]
		}
	}
	if age == nil {
		t.Fatal("age column missing after rebuild")
	}
	if age.Type != "INTEGER" || !age.NotNull {
		t.Errorf("age column = %+v, want INTEGER NOT NULL", *age)
	}
	if tbl.RowCount != 2 {
		t.Errorf("row count = %d, want 2 (data lost in rebuild)", tbl.RowCount)
	}
	// The index must survive the rebuild.
	found := false
	for _, i := range tbl.Indexes {
		if i.Name == "idx_people_name" {
			found = true
		}
	}
	if !found {
		t.Error("index was not recreated after the rebuild")
	}

	h2, _ := m.Get(ctx, "app")
	defer h2.Release()
	var name string
	if err := h2.Reader().QueryRowContext(ctx,
		`SELECT name FROM people WHERE age = 36`).Scan(&name); err != nil {
		t.Fatalf("query after rebuild: %v", err)
	}
	if name != "Ada" {
		t.Errorf("name = %q, want Ada", name)
	}
}

func TestIntegrityCheckAndStats(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	if err := s.CreateTable(ctx, "app", CreateTableInput{
		Name:    "t",
		Columns: []Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
	}); err != nil {
		t.Fatal(err)
	}

	report, err := s.CheckIntegrity(ctx, "app")
	if err != nil {
		t.Fatalf("CheckIntegrity: %v", err)
	}
	if !report.OK {
		t.Errorf("fresh database reported problems: %v", report.Problems)
	}

	st, err := s.GetStats(ctx, "app")
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if st.TableCount != 1 {
		t.Errorf("table count = %d, want 1", st.TableCount)
	}
	if st.PageSize <= 0 || st.SizeBytes <= 0 {
		t.Errorf("stats look wrong: %+v", st)
	}
	if st.JournalMode != "wal" {
		t.Errorf("journal mode = %q, want wal", st.JournalMode)
	}
}

func TestDropImplicitIndexIsRefused(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	if err := s.CreateTable(ctx, "app", CreateTableInput{
		Name: "t",
		Columns: []Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true},
			{Name: "email", Type: "TEXT", Unique: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	tbl, err := s.GetTable(ctx, "app", "t")
	if err != nil {
		t.Fatal(err)
	}
	for _, idx := range tbl.Indexes {
		if idx.Origin == "u" {
			if err := s.DropIndex(ctx, "app", idx.Name); err == nil {
				t.Error("dropped a constraint-backed index that SQLite owns")
			}
			return
		}
	}
	t.Skip("no implicit unique index reported")
}

func strptr(s string) *string { return &s }
