package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/query"
	"github.com/litebase/litebase/internal/rows"
	"github.com/litebase/litebase/internal/tables"
)

func newTestService(t *testing.T) (*Service, *database.Meta) {
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

	meta, err := m.Create(ctx, "shop", "")
	if err != nil {
		t.Fatal(err)
	}
	schema := tables.NewService(m)
	if err := schema.CreateTable(ctx, "shop", tables.CreateTableInput{
		Name: "products",
		Columns: []tables.Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT", NotNull: true},
			{Name: "category", Type: "TEXT"},
			{Name: "price", Type: "REAL"},
			{Name: "secret", Type: "TEXT"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	rowsSvc := rows.NewService(m, schema, 1000)
	for _, p := range []map[string]any{
		{"name": "Widget", "category": "tools", "price": 9.99, "secret": "s1"},
		{"name": "Gadget", "category": "tools", "price": 49.99, "secret": "s2"},
		{"name": "Doll", "category": "toys", "price": 19.99, "secret": "s3"},
	} {
		if _, err := rowsSvc.Insert(ctx, "shop", "products", p); err != nil {
			t.Fatal(err)
		}
	}

	querySvc := query.NewService(m, 1000, 10*time.Second)
	return NewService(m, rowsSvc, querySvc, schema), meta
}

func TestValidatePath(t *testing.T) {
	ok := map[string]string{
		"/products":           "/products",
		"products":            "/products",
		"/products/":          "/products",
		"/products/{id}":      "/products/{id}",
		"/v1/products/search": "/v1/products/search",
	}
	for in, want := range ok {
		got, err := ValidatePath(in)
		if err != nil {
			t.Errorf("ValidatePath(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ValidatePath(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"", "/", "//products", "/products//x",
		"/../etc/passwd", "/products/..", "/a/./b",
		"/products?x=1", "/products#frag", "/pro ducts",
		"/admin", "/admin/users", "/ADMIN/x",
		"/products/{id}/{id}",
		`/products\x`,
	}
	for _, in := range bad {
		if _, err := ValidatePath(in); err == nil {
			t.Errorf("ValidatePath(%q) = nil error, want rejection", in)
		}
	}
}

func TestCountPlaceholders(t *testing.T) {
	cases := map[string]int{
		"SELECT * FROM t":                          0,
		"SELECT * FROM t WHERE a = ?":              1,
		"SELECT * FROM t WHERE a = ? AND b = ?":    2,
		"SELECT '?' FROM t":                        0,
		"SELECT * FROM t WHERE a = ? -- ? comment": 1,
		"SELECT /* ? */ * FROM t WHERE a = ?":      1,
		`SELECT "col?" FROM t WHERE a = ?`:         1,
	}
	for stmt, want := range cases {
		if got := CountPlaceholders(stmt); got != want {
			t.Errorf("CountPlaceholders(%q) = %d, want %d", stmt, got, want)
		}
	}
}

func TestValidateSQLRequiresMatchingPlaceholders(t *testing.T) {
	params := []Param{{Name: "a", Type: TypeString}}

	if err := ValidateSQL("SELECT * FROM t WHERE a = ?", params); err != nil {
		t.Errorf("matching placeholders rejected: %v", err)
	}
	// A mismatch would bind values to the wrong positions at runtime.
	if err := ValidateSQL("SELECT * FROM t WHERE a = ? AND b = ?", params); err == nil {
		t.Error("statement with too many placeholders accepted")
	}
	if err := ValidateSQL("SELECT * FROM t", params); err == nil {
		t.Error("statement with too few placeholders accepted")
	}
	// Multiple statements would let one endpoint do unrelated work.
	if err := ValidateSQL("SELECT * FROM t WHERE a = ?; DROP TABLE t;", params); err == nil {
		t.Error("multi-statement SQL accepted")
	}
	if err := ValidateSQL("SELECT * FROM t WHERE a = :name", params); err == nil {
		t.Error("named parameters accepted")
	}
	if err := ValidateSQL("", nil); err == nil {
		t.Error("empty SQL accepted")
	}
}

func TestEndpointValidation(t *testing.T) {
	e := &Endpoint{
		Kind: KindQuery, Name: "search", Method: "get", Path: "products/search",
		SQL:    "SELECT * FROM products WHERE category = ? AND price <= ? LIMIT ?",
		Params: []Param{{Name: "category"}, {Name: "max_price", Type: TypeNumber}, {Name: "limit", Type: TypeInteger}},
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if e.Method != "GET" || e.Path != "/products/search" {
		t.Errorf("normalisation failed: method=%q path=%q", e.Method, e.Path)
	}
	if e.Params[0].Type != TypeString || e.Params[0].In != InQuery {
		t.Errorf("parameter defaults not applied: %+v", e.Params[0])
	}

	// A path parameter with no declaration must be caught.
	bad := &Endpoint{
		Kind: KindQuery, Name: "x", Method: "GET", Path: "/products/{id}",
		SQL: "SELECT * FROM products WHERE id = ?", Params: []Param{{Name: "other"}},
	}
	if err := bad.Validate(); err == nil {
		t.Error("undeclared path parameter accepted")
	}
}

func TestBindParamsValidation(t *testing.T) {
	minLen := 2
	params := []Param{
		{Name: "category", In: InQuery, Type: TypeString, Required: true, MinLength: &minLen},
		{Name: "max_price", In: InQuery, Type: TypeNumber, Default: 100.0},
		{Name: "active", In: InQuery, Type: TypeBoolean},
	}

	args, err := BindParams(params, RawValues{Query: map[string][]string{
		"category": {"tools"}, "active": {"true"},
	}})
	if err != nil {
		t.Fatalf("BindParams: %v", err)
	}
	if len(args) != 3 {
		t.Fatalf("args = %v, want 3", args)
	}
	if args[0] != "tools" {
		t.Errorf("arg0 = %v, want tools", args[0])
	}
	// The default fills in for the omitted parameter.
	if args[1] != 100.0 {
		t.Errorf("arg1 = %v, want the default 100", args[1])
	}
	if args[2] != true {
		t.Errorf("arg2 = %v, want true", args[2])
	}

	// A missing required parameter is reported, not defaulted.
	if _, err := BindParams(params, RawValues{Query: map[string][]string{}}); err == nil {
		t.Error("missing required parameter accepted")
	}
	// Type errors are reported per field.
	_, err = BindParams(params, RawValues{Query: map[string][]string{
		"category": {"tools"}, "max_price": {"not-a-number"},
	}})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if verr.Fields()["max_price"] == "" {
		t.Errorf("fields = %v, want an entry for max_price", verr.Fields())
	}
	// Constraint violations are enforced.
	if _, err := BindParams(params, RawValues{Query: map[string][]string{"category": {"x"}}}); err == nil {
		t.Error("value below the minimum length accepted")
	}
}

func TestBindParamsEnumAndRange(t *testing.T) {
	maxV := 10.0
	params := []Param{
		{Name: "status", Type: TypeString, Enum: []string{"open", "closed"}},
		{Name: "n", Type: TypeInteger, Max: &maxV},
	}
	if _, err := BindParams(params, RawValues{Query: map[string][]string{
		"status": {"open"}, "n": {"5"},
	}}); err != nil {
		t.Errorf("valid values rejected: %v", err)
	}
	if _, err := BindParams(params, RawValues{Query: map[string][]string{"status": {"other"}}}); err == nil {
		t.Error("value outside the enum accepted")
	}
	if _, err := BindParams(params, RawValues{Query: map[string][]string{"n": {"99"}}}); err == nil {
		t.Error("value above the maximum accepted")
	}
}

func TestQueryEndpointBindsParametersSafely(t *testing.T) {
	ctx := context.Background()
	s, meta := newTestService(t)

	e := &Endpoint{
		DatabaseID: meta.ID, Kind: KindQuery, Name: "search", Method: "GET",
		Path:    "/products/search",
		SQL:     "SELECT name, price FROM products WHERE category = ? AND price <= ? ORDER BY price DESC LIMIT ?",
		Params:  []Param{{Name: "category", Required: true}, {Name: "max_price", Type: TypeNumber}, {Name: "limit", Type: TypeInteger, Default: 10}},
		Enabled: true,
	}
	if err := s.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m, _, err := s.Resolve(ctx, "GET", "/products/search")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	resp, err := s.Execute(ctx, m, Request{
		Method: "GET",
		Query:  map[string][]string{"category": {"tools"}, "max_price": {"50"}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := resp.Body.(listEnvelope)
	if len(env.Data) != 2 {
		t.Fatalf("returned %d rows, want 2", len(env.Data))
	}
	if env.Data[0]["name"] != "Gadget" {
		t.Errorf("first row = %v, want Gadget", env.Data[0]["name"])
	}

	// A parameter carrying SQL must be treated purely as a value.
	resp, err = s.Execute(ctx, m, Request{
		Method: "GET",
		Query:  map[string][]string{"category": {"tools' OR '1'='1"}, "max_price": {"1000"}},
	})
	if err != nil {
		t.Fatalf("Execute with injected value: %v", err)
	}
	env = resp.Body.(listEnvelope)
	if len(env.Data) != 0 {
		t.Errorf("injected parameter returned %d rows, want 0", len(env.Data))
	}
}

func TestCRUDEndpointLifecycle(t *testing.T) {
	ctx := context.Background()
	s, meta := newTestService(t)

	e := &Endpoint{
		DatabaseID: meta.ID, Kind: KindCRUD, Name: "products", Path: "/products",
		Table:   "products",
		Enabled: true,
		Config: CRUDConfig{
			Operations:      AllCRUDOperations(),
			ReadableColumns: []string{"id", "name", "category", "price"},
			WritableColumns: []string{"name", "category", "price"},
			AllowFilters:    true,
			AllowSearch:     true,
		},
	}
	if err := s.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// List
	m, _, err := s.Resolve(ctx, "GET", "/products")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Execute(ctx, m, Request{Method: "GET", Query: map[string][]string{}})
	if err != nil {
		t.Fatal(err)
	}
	env := resp.Body.(listEnvelope)
	if env.Total != 3 {
		t.Errorf("total = %d, want 3", env.Total)
	}
	// A column left out of ReadableColumns must not appear.
	if _, leaked := env.Data[0]["secret"]; leaked {
		t.Error("a column outside ReadableColumns was returned")
	}

	// Filtering
	resp, err = s.Execute(ctx, m, Request{Method: "GET",
		Query: map[string][]string{"category": {"tools"}, "price__lte": {"10"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Body.(listEnvelope).Total; got != 1 {
		t.Errorf("filtered total = %d, want 1", got)
	}

	// Create
	mCreate, _, err := s.Resolve(ctx, "POST", "/products")
	if err != nil {
		t.Fatal(err)
	}
	resp, err = s.Execute(ctx, mCreate, Request{Method: "POST",
		Body: map[string]any{"name": "New", "category": "toys", "price": 5.0}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.Status != 201 {
		t.Errorf("status = %d, want 201", resp.Status)
	}
	created := resp.Body.(map[string]any)["data"].(map[string]any)
	newID := created["id"]

	// Writing a column outside WritableColumns must be refused, not ignored.
	if _, err := s.Execute(ctx, mCreate, Request{Method: "POST",
		Body: map[string]any{"name": "X", "secret": "leak"}}); err == nil {
		t.Error("write to a non-writable column accepted")
	}

	// Read
	mRead, _, err := s.Resolve(ctx, "GET", "/products/1")
	if err != nil {
		t.Fatal(err)
	}
	if mRead.Operation != OpRead {
		t.Fatalf("operation = %q, want read", mRead.Operation)
	}
	if _, err := s.Execute(ctx, mRead, Request{Method: "GET"}); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Update
	mUpdate, _, err := s.Resolve(ctx, "PATCH", "/products/1")
	if err != nil {
		t.Fatal(err)
	}
	resp, err = s.Execute(ctx, mUpdate, Request{Method: "PATCH", Body: map[string]any{"price": 11.0}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if resp.Body.(map[string]any)["data"].(map[string]any)["price"] != 11.0 {
		t.Error("update did not apply")
	}

	// Delete
	mDelete, _, err := s.Resolve(ctx, "DELETE", "/products/"+toString(newID))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = s.Execute(ctx, mDelete, Request{Method: "DELETE"})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if resp.Status != 204 {
		t.Errorf("status = %d, want 204", resp.Status)
	}
}

func TestLiteralRouteBeatsParameterRoute(t *testing.T) {
	ctx := context.Background()
	s, meta := newTestService(t)

	if err := s.Create(ctx, &Endpoint{
		DatabaseID: meta.ID, Kind: KindCRUD, Name: "products", Path: "/products",
		Table: "products", Enabled: true,
		Config: CRUDConfig{Operations: []CRUDOperation{OpList, OpRead}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, &Endpoint{
		DatabaseID: meta.ID, Kind: KindQuery, Name: "search", Method: "GET",
		Path: "/products/search", SQL: "SELECT * FROM products", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	// "/products/search" must reach the query endpoint, not "/products/{id}".
	m, _, err := s.Resolve(ctx, "GET", "/products/search")
	if err != nil {
		t.Fatal(err)
	}
	if m.Endpoint.Kind != KindQuery {
		t.Errorf("matched %q endpoint, want the literal query route", m.Endpoint.Kind)
	}

	m, _, err = s.Resolve(ctx, "GET", "/products/42")
	if err != nil {
		t.Fatal(err)
	}
	if m.Endpoint.Kind != KindCRUD || m.PathParams["id"] != "42" {
		t.Errorf("id route: kind=%q params=%v", m.Endpoint.Kind, m.PathParams)
	}
}

func TestRouteConflictsRejected(t *testing.T) {
	ctx := context.Background()
	s, meta := newTestService(t)

	first := &Endpoint{
		DatabaseID: meta.ID, Kind: KindQuery, Name: "a", Method: "GET",
		Path: "/things", SQL: "SELECT 1", Enabled: true,
	}
	if err := s.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &Endpoint{
		DatabaseID: meta.ID, Kind: KindQuery, Name: "b", Method: "GET",
		Path: "/things", SQL: "SELECT 2", Enabled: true,
	}
	if err := s.Create(ctx, second); !errors.Is(err, ErrRouteConflict) {
		t.Errorf("duplicate route error = %v, want ErrRouteConflict", err)
	}

	// A CRUD endpoint whose generated list route collides must also be caught.
	crud := &Endpoint{
		DatabaseID: meta.ID, Kind: KindCRUD, Name: "c", Path: "/things",
		Table: "products", Enabled: true,
		Config: CRUDConfig{Operations: []CRUDOperation{OpList}},
	}
	if err := s.Create(ctx, crud); !errors.Is(err, ErrRouteConflict) {
		t.Errorf("colliding CRUD route error = %v, want ErrRouteConflict", err)
	}
}

func TestDisabledEndpointDoesNotResolve(t *testing.T) {
	ctx := context.Background()
	s, meta := newTestService(t)

	e := &Endpoint{
		DatabaseID: meta.ID, Kind: KindQuery, Name: "a", Method: "GET",
		Path: "/things", SQL: "SELECT 1", Enabled: true,
	}
	if err := s.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(ctx, "GET", "/things"); err != nil {
		t.Fatal(err)
	}

	if err := s.SetEnabled(ctx, e.ID, false); err != nil {
		t.Fatal(err)
	}
	// Disabling must take effect immediately, not after a cache expiry.
	if _, _, err := s.Resolve(ctx, "GET", "/things"); !errors.Is(err, ErrNoRoute) {
		t.Errorf("disabled endpoint resolved: %v", err)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	ctx := context.Background()
	s, meta := newTestService(t)
	if err := s.Create(ctx, &Endpoint{
		DatabaseID: meta.ID, Kind: KindQuery, Name: "a", Method: "GET",
		Path: "/things", SQL: "SELECT 1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	_, allowed, err := s.Resolve(ctx, "POST", "/things")
	if !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("error = %v, want ErrMethodNotAllowed", err)
	}
	if len(allowed) != 1 || allowed[0] != "GET" {
		t.Errorf("allowed = %v, want [GET]", allowed)
	}
}

func TestEndpointRejectsMissingTable(t *testing.T) {
	ctx := context.Background()
	s, meta := newTestService(t)
	err := s.Create(ctx, &Endpoint{
		DatabaseID: meta.ID, Kind: KindCRUD, Name: "x", Path: "/missing",
		Table: "nosuchtable", Enabled: true,
	})
	if err == nil {
		t.Error("endpoint backed by a missing table accepted")
	}
}

func toString(v any) string {
	switch t := v.(type) {
	case int64:
		return string(rune('0' + t))
	default:
		return "4"
	}
}
