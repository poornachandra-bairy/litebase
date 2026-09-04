package query

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/litebase/litebase/internal/database"
)

func newTestService(t *testing.T) (*Service, *database.Manager) {
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
	return NewService(m, 100, 10*time.Second), m
}

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   int
	}{
		{"single", "SELECT 1", 1},
		{"two", "SELECT 1; SELECT 2", 2},
		{"trailing semicolon", "SELECT 1;", 1},
		{"empty parts", "SELECT 1;;; SELECT 2;", 2},
		{"semicolon in string", "SELECT 'a;b'", 1},
		{"escaped quote", "SELECT 'it''s; here'", 1},
		{"line comment", "SELECT 1 -- comment; not a split\n; SELECT 2", 2},
		{"block comment", "SELECT /* ; */ 1; SELECT 2", 2},
		{"quoted identifier", `SELECT "a;b" FROM t`, 1},
		{"bracket identifier", "SELECT [a;b] FROM t", 1},
		{
			"trigger body",
			`CREATE TRIGGER t AFTER INSERT ON x BEGIN UPDATE x SET a=1; UPDATE x SET b=2; END; SELECT 1`,
			2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitStatements(tc.script)
			if len(got) != tc.want {
				t.Errorf("SplitStatements(%q) = %d statements %q, want %d",
					tc.script, len(got), got, tc.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]Kind{
		"SELECT * FROM t":                      KindSelect,
		"  select 1":                           KindSelect,
		"WITH x AS (SELECT 1) SELECT * FROM x": KindSelect,
		"INSERT INTO t VALUES(1)":              KindWrite,
		"update t set a=1":                     KindWrite,
		"DELETE FROM t":                        KindWrite,
		"CREATE TABLE t(a)":                    KindDDL,
		"DROP TABLE t":                         KindDDL,
		"PRAGMA journal_mode":                  KindPragma,
		"-- comment\nSELECT 1":                 KindSelect,
		"/* c */ INSERT INTO t VALUES(1)":      KindWrite,
	}
	for stmt, want := range cases {
		if got := Classify(stmt); got != want {
			t.Errorf("Classify(%q) = %q, want %q", stmt, got, want)
		}
	}
}

func TestIsReadOnly(t *testing.T) {
	readOnly := []string{"SELECT 1", "select * from t", "EXPLAIN SELECT 1", "PRAGMA table_info(t)"}
	for _, s := range readOnly {
		if !IsReadOnly(s) {
			t.Errorf("IsReadOnly(%q) = false, want true", s)
		}
	}
	// Anything not provably read-only must be treated as a write.
	writes := []string{
		"INSERT INTO t VALUES(1)",
		"DROP TABLE t",
		"PRAGMA journal_mode = DELETE",
		"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
		"VACUUM",
		"gibberish",
	}
	for _, s := range writes {
		if IsReadOnly(s) {
			t.Errorf("IsReadOnly(%q) = true, want false", s)
		}
	}
}

func TestExecuteSelect(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)

	resp, err := s.Execute(ctx, "app", `
		CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO t(name) VALUES('a'), ('b');
		SELECT * FROM t ORDER BY id;
	`, ExecOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Failed {
		t.Fatalf("script failed: %+v", resp.Results)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %d, want 3", len(resp.Results))
	}

	insert := resp.Results[1]
	if insert.RowsAffected != 2 {
		t.Errorf("rows affected = %d, want 2", insert.RowsAffected)
	}
	sel := resp.Results[2]
	if len(sel.Rows) != 2 {
		t.Fatalf("selected %d rows, want 2", len(sel.Rows))
	}
	if sel.Rows[0]["name"] != "a" {
		t.Errorf("first row = %v, want a", sel.Rows[0]["name"])
	}
	if len(sel.Columns) != 2 {
		t.Errorf("columns = %v, want 2", sel.Columns)
	}
	if resp.DurationMS < 0 {
		t.Error("negative duration reported")
	}
}

func TestExecuteReportsErrorsWithoutFailingTheRequest(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)

	resp, err := s.Execute(ctx, "app", "SELECT * FROM does_not_exist", ExecOptions{})
	if err != nil {
		t.Fatalf("Execute returned a transport error: %v", err)
	}
	if !resp.Failed {
		t.Error("response not marked failed")
	}
	if resp.Results[0].Error == "" {
		t.Error("no error message reported to the editor")
	}
	if !strings.Contains(resp.Results[0].Error, "does_not_exist") {
		t.Errorf("error %q does not name the missing table", resp.Results[0].Error)
	}
}

func TestExecuteStopsAtFirstError(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)

	resp, err := s.Execute(ctx, "app",
		"CREATE TABLE ok(id INTEGER); SELECT * FROM missing; CREATE TABLE never(id INTEGER);",
		ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Errorf("results = %d, want 2 (execution should stop at the failure)", len(resp.Results))
	}

	// The third statement must not have run.
	check, err := s.Execute(ctx, "app",
		"SELECT count(*) AS n FROM sqlite_schema WHERE name = 'never'", ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if check.Results[0].Rows[0]["n"].(int64) != 0 {
		t.Error("a statement after the failure was executed")
	}
}

func TestReadOnlyModeIsEnforcedBySQLite(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)

	if _, err := s.Execute(ctx, "app", "CREATE TABLE t(id INTEGER)", ExecOptions{}); err != nil {
		t.Fatal(err)
	}

	// A read-only run must be refused by the engine, not merely by keyword
	// inspection, so the write genuinely cannot happen.
	resp, err := s.Execute(ctx, "app", "INSERT INTO t VALUES(1)", ExecOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !resp.Failed {
		t.Fatal("write succeeded on a read-only connection")
	}

	verify, err := s.Execute(ctx, "app", "SELECT count(*) AS n FROM t", ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if n := verify.Results[0].Rows[0]["n"].(int64); n != 0 {
		t.Errorf("row count = %d, want 0; the read-only write was not prevented", n)
	}

	// Reads must still work in read-only mode.
	ok, err := s.Execute(ctx, "app", "SELECT 1 AS v", ExecOptions{ReadOnly: true})
	if err != nil || ok.Failed {
		t.Errorf("read-only SELECT failed: %v %+v", err, ok.Results)
	}
}

func TestRowLimitTruncates(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	s.maxRows = 5

	seed, err := s.Execute(ctx, "app", `
		CREATE TABLE nums(n INTEGER);
		INSERT INTO nums VALUES(1),(2),(3),(4),(5),(6),(7),(8),(9),(10);
	`, ExecOptions{})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if seed.Failed {
		t.Fatalf("seed failed: %+v", seed.Results)
	}

	resp, err := s.Execute(ctx, "app", "SELECT * FROM nums", ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("select failed: %+v", resp.Results)
	}
	r := resp.Results[0]
	if len(r.Rows) > 5 {
		t.Errorf("returned %d rows, want at most 5", len(r.Rows))
	}
	if !r.Truncated {
		t.Error("truncation not reported to the client")
	}
}

func TestExecuteRejectsEmptyScript(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	for _, script := range []string{"", "   ", "-- just a comment\n"} {
		if _, err := s.Execute(ctx, "app", script, ExecOptions{}); err == nil {
			t.Errorf("Execute(%q) = nil error, want ErrScriptEmpty", script)
		}
	}
}

func TestParametersAreBound(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	if _, err := s.Execute(ctx, "app", `
		CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO t(name) VALUES('safe');
	`, ExecOptions{}); err != nil {
		t.Fatal(err)
	}

	// A parameter containing SQL must be treated as data.
	resp, err := s.Execute(ctx, "app", "SELECT * FROM t WHERE name = ?",
		ExecOptions{Params: []any{"safe'; DROP TABLE t;--"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("query failed: %+v", resp.Results)
	}
	if len(resp.Results[0].Rows) != 0 {
		t.Error("injected parameter matched a row")
	}

	check, err := s.Execute(ctx, "app", "SELECT count(*) AS n FROM t", ExecOptions{})
	if err != nil || check.Failed {
		t.Fatalf("table was dropped by a bound parameter: %v", err)
	}
}

func TestUserFacingErrorHidesPaths(t *testing.T) {
	err := userFacingSQLError(errStr("unable to open database file /var/lib/litebase/data/app.db"))
	if strings.Contains(err, "/var/lib") {
		t.Errorf("error leaked a filesystem path: %q", err)
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
