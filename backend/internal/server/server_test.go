package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/litebase/litebase/internal/api"
	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/backup"
	"github.com/litebase/litebase/internal/config"
	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/middleware"
	"github.com/litebase/litebase/internal/query"
	"github.com/litebase/litebase/internal/rows"
	"github.com/litebase/litebase/internal/storage"
	"github.com/litebase/litebase/internal/tables"
)

// testServer wires a complete server against temporary directories, so these
// tests exercise the real middleware chain and routing rather than handlers in
// isolation.
type testServer struct {
	t      *testing.T
	srv    *Server
	http   *httptest.Server
	client *http.Client
	csrf   string
	mgr    *database.Manager
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	dir := t.TempDir()

	cfg := &config.Config{
		Addr:             "127.0.0.1:0",
		DataDir:          dir,
		SessionTTL:       time.Hour,
		QueryTimeout:     10 * time.Second,
		MaxQueryRows:     1000,
		MaxOpenDatabases: 16,
		MaxBodyBytes:     1 << 20,
		MaxUploadBytes:   8 << 20,
		AdminRatePerMin:  10000,
		APIRatePerMin:    10000,
		AuthRatePerMin:   10000,
		ReadTimeout:      10 * time.Second,
		WriteTimeout:     30 * time.Second,
		ShutdownTimeout:  time.Second,
		LogLevel:         "error",
		LogFormat:        "text",
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := database.NewManager(t.Context(), database.ManagerOptions{
		DatabasesDir: cfg.DatabasesDir(),
		MetaPath:     cfg.MetaDBPath(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })

	authSvc := auth.NewService(mgr.Meta(), cfg.SessionTTL)
	if _, err := authSvc.CreateUser(t.Context(), "owner@example.com", "Owner", "ownerpassword1", auth.RoleOwner); err != nil {
		t.Fatal(err)
	}

	tablesSvc := tables.NewService(mgr)
	rowsSvc := rows.NewService(mgr, tablesSvc, cfg.MaxQueryRows)
	querySvc := query.NewService(mgr, cfg.MaxQueryRows, cfg.QueryTimeout)
	apiSvc := api.NewService(mgr, rowsSvc, querySvc, tablesSvc)

	registry := storage.NewRegistry()
	local, err := storage.NewLocal(cfg.BackupsDir())
	if err != nil {
		t.Fatal(err)
	}
	registry.Register(local)

	backupSvc, err := backup.NewService(backup.Options{
		Manager: mgr, Storage: registry, TempDir: cfg.TempDir(), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := New(Options{
		Config: cfg, Logger: logger, Manager: mgr, Auth: authSvc,
		Tables: tablesSvc, Rows: rowsSvc, Query: querySvc, API: apiSvc,
		Backups: backupSvc, StorageRegistry: registry,
	})
	t.Cleanup(func() { srv.Shutdown(t.Context()) })

	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// A cookie jar makes the client behave like the browser the server expects.
	client := &http.Client{Timeout: 30 * time.Second, Jar: newJar()}
	return &testServer{t: t, srv: srv, http: hs, client: client, mgr: mgr}
}

// do issues a request, attaching the CSRF token to unsafe methods.
func (ts *testServer) do(method, path string, body any) *http.Response {
	ts.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			ts.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, ts.http.URL+path, reader)
	if err != nil {
		ts.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ts.csrf != "" {
		req.Header.Set(middleware.CSRFHeaderName, ts.csrf)
	}

	res, err := ts.client.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	return res
}

// login authenticates and captures the CSRF token for later writes.
func (ts *testServer) login() {
	ts.t.Helper()
	res := ts.do("POST", "/api/admin/auth/login", map[string]string{
		"email": "owner@example.com", "password": "ownerpassword1",
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		ts.t.Fatalf("login failed: %d", res.StatusCode)
	}

	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	json.NewDecoder(res.Body).Decode(&session)
	ts.csrf = session.CSRFToken
}

func decode[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	defer res.Body.Close()
	var out T
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestHealthAndReadyAreUnauthenticated(t *testing.T) {
	ts := newTestServer(t)

	for _, path := range []string{"/api/health", "/api/ready"} {
		res := ts.do("GET", path, nil)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, res.StatusCode)
		}
	}
}

func TestAdminRoutesRequireAuthentication(t *testing.T) {
	ts := newTestServer(t)

	protected := []struct{ method, path string }{
		{"GET", "/api/admin/databases"},
		{"GET", "/api/admin/endpoints"},
		{"GET", "/api/admin/keys"},
		{"GET", "/api/admin/backups"},
		{"GET", "/api/admin/settings"},
		{"GET", "/api/admin/users"},
	}
	for _, r := range protected {
		res := ts.do(r.method, r.path, nil)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", r.method, r.path, res.StatusCode)
		}
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	ts := newTestServer(t)

	for _, creds := range []map[string]string{
		{"email": "owner@example.com", "password": "wrongpassword"},
		{"email": "nobody@example.com", "password": "ownerpassword1"},
		{"email": "not-an-email", "password": "ownerpassword1"},
	} {
		res := ts.do("POST", "/api/admin/auth/login", creds)
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("login %v = %d, want 401", creds["email"], res.StatusCode)
		}
		// The message must not reveal whether the account exists.
		if strings.Contains(string(body), "not found") || strings.Contains(string(body), "no such user") {
			t.Errorf("response discloses account existence: %s", body)
		}
	}
}

func TestCSRFIsEnforcedOnWrites(t *testing.T) {
	ts := newTestServer(t)
	ts.login()

	// Drop the token to simulate a cross-site request that carries the cookie
	// but cannot read it.
	saved := ts.csrf
	ts.csrf = ""
	res := ts.do("POST", "/api/admin/databases", map[string]string{"name": "sneaky"})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("write without a CSRF token = %d, want 403", res.StatusCode)
	}

	ts.csrf = saved
	res = ts.do("POST", "/api/admin/databases", map[string]string{"name": "legitimate"})
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Errorf("write with a CSRF token = %d, want 201", res.StatusCode)
	}
}

func TestDatabaseAndTableLifecycleOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	ts.login()

	res := ts.do("POST", "/api/admin/databases", map[string]string{"name": "shop", "description": "demo"})
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("create database = %d: %s", res.StatusCode, body)
	}
	res.Body.Close()

	// Creating the same database twice is a conflict, not a 500.
	res = ts.do("POST", "/api/admin/databases", map[string]string{"name": "shop"})
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate database = %d, want 409", res.StatusCode)
	}

	// An invalid name is a client error with an explanation.
	res = ts.do("POST", "/api/admin/databases", map[string]string{"name": "../evil"})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid name = %d, want 400", res.StatusCode)
	}

	res = ts.do("POST", "/api/admin/databases/shop/tables", map[string]any{
		"name": "products",
		"columns": []map[string]any{
			{"name": "id", "type": "INTEGER", "primary_key": true, "auto_increment": true},
			{"name": "name", "type": "TEXT", "not_null": true},
			{"name": "price", "type": "REAL"},
		},
	})
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("create table = %d: %s", res.StatusCode, body)
	}
	res.Body.Close()

	res = ts.do("POST", "/api/admin/databases/shop/tables/products/rows", map[string]any{
		"values": map[string]any{"name": "Widget", "price": 9.99},
	})
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("create row = %d: %s", res.StatusCode, body)
	}
	res.Body.Close()

	res = ts.do("POST", "/api/admin/databases/shop/tables/products/rows/query", map[string]any{})
	page := decode[struct {
		Rows  []map[string]any `json:"rows"`
		Total int64            `json:"total"`
	}](t, res)
	if page.Total != 1 || page.Rows[0]["name"] != "Widget" {
		t.Errorf("row listing = %+v", page)
	}

	// Deleting without the confirmation parameter must be refused.
	res = ts.do("DELETE", "/api/admin/databases/shop", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("delete without confirm = %d, want 400", res.StatusCode)
	}

	res = ts.do("DELETE", "/api/admin/databases/shop?confirm=shop", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("delete with confirm = %d, want 204", res.StatusCode)
	}
}

func TestSQLEditorOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	ts.login()

	res := ts.do("POST", "/api/admin/databases", map[string]string{"name": "app"})
	res.Body.Close()

	res = ts.do("POST", "/api/admin/databases/app/query", map[string]any{
		"sql": "CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t(v) VALUES('a'); SELECT * FROM t;",
	})
	result := decode[struct {
		Results []struct {
			Rows  []map[string]any `json:"rows"`
			Error string           `json:"error"`
		} `json:"results"`
		Failed bool `json:"failed"`
	}](t, res)

	if result.Failed {
		t.Fatalf("script failed: %+v", result.Results)
	}
	if len(result.Results) != 3 || len(result.Results[2].Rows) != 1 {
		t.Errorf("unexpected results: %+v", result.Results)
	}
}

func TestGeneratedAPIRequiresKeyAndHonoursScopes(t *testing.T) {
	ts := newTestServer(t)
	ts.login()

	// Build a database, a table and a CRUD endpoint.
	res := ts.do("POST", "/api/admin/databases", map[string]string{"name": "shop"})
	dbMeta := decode[struct {
		ID string `json:"id"`
	}](t, res)

	res = ts.do("POST", "/api/admin/databases/shop/tables", map[string]any{
		"name": "products",
		"columns": []map[string]any{
			{"name": "id", "type": "INTEGER", "primary_key": true, "auto_increment": true},
			{"name": "name", "type": "TEXT", "not_null": true},
		},
	})
	res.Body.Close()

	res = ts.do("POST", "/api/admin/databases/shop/tables/products/rows", map[string]any{
		"values": map[string]any{"name": "Widget"},
	})
	res.Body.Close()

	res = ts.do("POST", "/api/admin/endpoints", map[string]any{
		"database_id": dbMeta.ID,
		"kind":        "crud",
		"name":        "products",
		"path":        "/products",
		"table":       "products",
		"enabled":     true,
		"config": map[string]any{
			"operations": []string{"list", "create"},
		},
	})
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("create endpoint = %d: %s", res.StatusCode, body)
	}
	res.Body.Close()

	// Without a key the endpoint is closed.
	anon, err := http.Get(ts.http.URL + "/api/products")
	if err != nil {
		t.Fatal(err)
	}
	anon.Body.Close()
	if anon.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous call = %d, want 401", anon.StatusCode)
	}

	// Issue a read-only key.
	res = ts.do("POST", "/api/admin/keys", map[string]any{
		"name": "reader", "scopes": []string{"read"}, "all_endpoints": true,
	})
	created := decode[struct {
		Secret string `json:"secret"`
	}](t, res)
	if created.Secret == "" {
		t.Fatal("no key secret returned")
	}

	call := func(method, path string, body io.Reader) *http.Response {
		req, _ := http.NewRequest(method, ts.http.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+created.Secret)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	listRes := call("GET", "/api/products", nil)
	listBody := decode[struct {
		Data  []map[string]any `json:"data"`
		Total int64            `json:"total"`
	}](t, listRes)
	if listBody.Total != 1 {
		t.Errorf("list total = %d, want 1", listBody.Total)
	}

	// A read-only key must not be able to write.
	writeRes := call("POST", "/api/products", strings.NewReader(`{"name":"Sneaky"}`))
	writeRes.Body.Close()
	if writeRes.StatusCode != http.StatusForbidden {
		t.Errorf("write with a read-only key = %d, want 403", writeRes.StatusCode)
	}

	// An invalid key is rejected.
	req, _ := http.NewRequest("GET", ts.http.URL+"/api/products", nil)
	req.Header.Set("Authorization", "Bearer lbk_totally_invalid_key_value")
	bad, _ := http.DefaultClient.Do(req)
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Errorf("invalid key = %d, want 401", bad.StatusCode)
	}

	// An unknown path under the API mount is a clean 404.
	missing, _ := http.Get(ts.http.URL + "/api/does-not-exist")
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("unknown endpoint = %d, want 404", missing.StatusCode)
	}
}

func TestCustomQueryEndpointBindsParameters(t *testing.T) {
	ts := newTestServer(t)
	ts.login()

	res := ts.do("POST", "/api/admin/databases", map[string]string{"name": "shop"})
	dbMeta := decode[struct {
		ID string `json:"id"`
	}](t, res)

	res = ts.do("POST", "/api/admin/databases/shop/query", map[string]any{
		"sql": "CREATE TABLE products(id INTEGER PRIMARY KEY, category TEXT, name TEXT); " +
			"INSERT INTO products(category, name) VALUES('tools','Widget'),('toys','Doll');",
	})
	res.Body.Close()

	res = ts.do("POST", "/api/admin/endpoints", map[string]any{
		"database_id": dbMeta.ID,
		"kind":        "query",
		"name":        "search",
		"method":      "GET",
		"path":        "/search",
		"enabled":     true,
		"sql":         "SELECT name FROM products WHERE category = ?",
		"params": []map[string]any{
			{"name": "category", "in": "query", "type": "string", "required": true},
		},
	})
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("create endpoint = %d: %s", res.StatusCode, body)
	}
	res.Body.Close()

	res = ts.do("POST", "/api/admin/keys", map[string]any{
		"name": "k", "scopes": []string{"read"}, "all_endpoints": true,
	})
	key := decode[struct {
		Secret string `json:"secret"`
	}](t, res)

	get := func(url string) *http.Response {
		req, _ := http.NewRequest("GET", ts.http.URL+url, nil)
		req.Header.Set("Authorization", "Bearer "+key.Secret)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	ok := get("/api/search?category=tools")
	body := decode[struct {
		Data []map[string]any `json:"data"`
	}](t, ok)
	if len(body.Data) != 1 || body.Data[0]["name"] != "Widget" {
		t.Errorf("query result = %+v", body.Data)
	}

	// A missing required parameter is a validation error naming the field.
	missing := get("/api/search")
	missingBody, _ := io.ReadAll(missing.Body)
	missing.Body.Close()
	if missing.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("missing parameter = %d, want 422", missing.StatusCode)
	}
	if !strings.Contains(string(missingBody), "category") {
		t.Errorf("error does not name the field: %s", missingBody)
	}

	// A parameter carrying SQL is treated purely as a value.
	inj := get("/api/search?category=tools%27%20OR%20%271%27%3D%271")
	injBody := decode[struct {
		Data []map[string]any `json:"data"`
	}](t, inj)
	if len(injBody.Data) != 0 {
		t.Errorf("injected parameter returned %d rows, want 0", len(injBody.Data))
	}

	// The table must still be intact.
	verify := get("/api/search?category=toys")
	verifyBody := decode[struct {
		Data []map[string]any `json:"data"`
	}](t, verify)
	if len(verifyBody.Data) != 1 {
		t.Errorf("table damaged by the injection attempt: %+v", verifyBody.Data)
	}
}

func TestBodyLimitAndMalformedJSON(t *testing.T) {
	ts := newTestServer(t)
	ts.login()

	req, _ := http.NewRequest("POST", ts.http.URL+"/api/admin/databases",
		strings.NewReader(`{"name": "x"`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(middleware.CSRFHeaderName, ts.csrf)
	res, err := ts.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed JSON = %d, want 400", res.StatusCode)
	}

	// An unknown field is rejected rather than silently ignored.
	res = ts.do("POST", "/api/admin/databases", map[string]any{"name": "y", "bogus": 1})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", res.StatusCode)
	}
}

func TestErrorsDoNotLeakInternals(t *testing.T) {
	ts := newTestServer(t)
	ts.login()

	// A missing database produces a 404 whose body must not contain a
	// filesystem path.
	res := ts.do("GET", "/api/admin/databases/nosuchdb/schema", nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	text := string(body)
	if strings.Contains(text, os.TempDir()) || strings.Contains(text, "/databases/") {
		t.Errorf("error response leaked a filesystem path: %s", text)
	}
	// Every error carries a request id so a report can be traced in the log.
	if !strings.Contains(text, "request_id") {
		t.Errorf("error response has no request id: %s", text)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	ts := newTestServer(t)
	ts.login()

	res := ts.do("GET", "/api/admin/auth/me", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me = %d, want 200", res.StatusCode)
	}

	res = ts.do("POST", "/api/admin/auth/logout", nil)
	res.Body.Close()

	res = ts.do("GET", "/api/admin/auth/me", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("me after logout = %d, want 401", res.StatusCode)
	}
}

func TestBackupAndRestoreOverHTTP(t *testing.T) {
	ts := newTestServer(t)
	ts.login()

	res := ts.do("POST", "/api/admin/databases", map[string]string{"name": "app"})
	res.Body.Close()
	res = ts.do("POST", "/api/admin/databases/app/query", map[string]any{
		"sql": "CREATE TABLE t(v TEXT); INSERT INTO t VALUES('keep');",
	})
	res.Body.Close()

	res = ts.do("POST", "/api/admin/backups", map[string]any{"database": "app"})
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("create backup = %d: %s", res.StatusCode, body)
	}
	rec := decode[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, res)
	if rec.Status != "completed" {
		t.Fatalf("backup status = %q", rec.Status)
	}

	// Destroy the data, then restore.
	res = ts.do("POST", "/api/admin/databases/app/query", map[string]any{"sql": "DELETE FROM t"})
	res.Body.Close()

	// Restoring requires naming the target database.
	res = ts.do("POST", "/api/admin/backups/"+rec.ID+"/restore", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("restore without confirm = %d, want 400", res.StatusCode)
	}

	res = ts.do("POST", "/api/admin/backups/"+rec.ID+"/restore?confirm=app", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("restore = %d, want 200", res.StatusCode)
	}

	res = ts.do("POST", "/api/admin/databases/app/query", map[string]any{
		"sql": "SELECT count(*) AS n FROM t",
	})
	out := decode[struct {
		Results []struct {
			Rows []map[string]any `json:"rows"`
		} `json:"results"`
	}](t, res)
	if n := out.Results[0].Rows[0]["n"]; n != float64(1) {
		t.Errorf("row count after restore = %v, want 1", n)
	}
}

func TestNoFrontendFallbackDoesNotServeAPIPaths(t *testing.T) {
	ts := newTestServer(t)

	// With no bundle embedded, an unknown non-API path returns the placeholder
	// page, but an unknown API path must still be a JSON 404.
	res := ts.do("GET", "/some/client/route", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("client route = %d, want 200 placeholder", res.StatusCode)
	}
}

func TestCookiesAreSecureBehindHTTPSProxy(t *testing.T) {
	ts := newTestServer(t)

	// A plain HTTP request on localhost must not get Secure cookies, or the
	// browser would refuse to send them back and login would be impossible.
	res := ts.do("POST", "/api/admin/auth/login", map[string]string{
		"email": "owner@example.com", "password": "ownerpassword1",
	})
	res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Secure {
			t.Errorf("cookie %q is Secure over plain HTTP, which would break local use", c.Name)
		}
	}

	// The same request forwarded by a TLS-terminating proxy must get Secure
	// cookies automatically, with no configuration.
	req, _ := http.NewRequest("POST", ts.http.URL+"/api/admin/auth/login",
		strings.NewReader(`{"email":"owner@example.com","password":"ownerpassword1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")

	proxied, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	proxied.Body.Close()

	found := map[string]bool{}
	for _, c := range proxied.Cookies() {
		found[c.Name] = true
		if !c.Secure {
			t.Errorf("cookie %q is not Secure behind an HTTPS proxy", c.Name)
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie %q has SameSite %v, want Lax", c.Name, c.SameSite)
		}
	}
	if !found[middleware.SessionCookieName] {
		t.Error("session cookie was not set")
	}
	// The session cookie must stay unreadable to page scripts; the CSRF one
	// must stay readable so the frontend can echo it back.
	for _, c := range proxied.Cookies() {
		if c.Name == middleware.SessionCookieName && !c.HttpOnly {
			t.Error("session cookie is not HttpOnly")
		}
		if c.Name == middleware.CSRFCookieName && c.HttpOnly {
			t.Error("CSRF cookie is HttpOnly, so the frontend cannot read it")
		}
	}
}
