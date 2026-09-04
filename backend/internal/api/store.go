package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/query"
	"github.com/litebase/litebase/internal/rows"
	"github.com/litebase/litebase/internal/tables"
)

var (
	ErrEndpointNotFound = errors.New("endpoint not found")
	ErrRouteConflict    = errors.New("another endpoint already serves that route")
)

// Service stores endpoint definitions and executes requests against them.
type Service struct {
	mgr    *database.Manager
	rows   *rows.Service
	query  *query.Service
	schema *tables.Service

	// router caches the compiled route table. Endpoints change rarely and are
	// read on every data-API request, so the compiled form is kept in memory
	// and rebuilt whenever a definition changes.
	mu     sync.RWMutex
	router *router
}

// NewService builds an API builder service.
func NewService(mgr *database.Manager, rowsSvc *rows.Service, querySvc *query.Service, schema *tables.Service) *Service {
	return &Service{mgr: mgr, rows: rowsSvc, query: querySvc, schema: schema}
}

const endpointColumns = `e.id, e.database_id, COALESCE(d.name, ''), e.name, e.description, e.kind,
	e.method, e.path, e.table_name, e.sql_text, e.params, e.config,
	e.enabled, e.public, e.rate_limit, e.max_rows, e.created_at, e.updated_at`

func scanEndpoint(sc interface{ Scan(...any) error }) (*Endpoint, error) {
	var (
		e                      Endpoint
		paramsJSON, configJSON string
		enabled, public        int
		created, updated       string
		kind, method           string
	)
	if err := sc.Scan(&e.ID, &e.DatabaseID, &e.Database, &e.Name, &e.Description, &kind,
		&method, &e.Path, &e.Table, &e.SQL, &paramsJSON, &configJSON,
		&enabled, &public, &e.RateLimit, &e.MaxRows, &created, &updated); err != nil {
		return nil, err
	}
	e.Kind = Kind(kind)
	e.Method = method
	e.Enabled = enabled != 0
	e.Public = public != 0
	e.CreatedAt = database.ParseTime(created)
	e.UpdatedAt = database.ParseTime(updated)

	if paramsJSON != "" {
		if err := json.Unmarshal([]byte(paramsJSON), &e.Params); err != nil {
			return nil, fmt.Errorf("endpoint %s has unreadable parameters: %w", e.ID, err)
		}
	}
	if configJSON != "" {
		if err := json.Unmarshal([]byte(configJSON), &e.Config); err != nil {
			return nil, fmt.Errorf("endpoint %s has unreadable configuration: %w", e.ID, err)
		}
	}
	if e.Params == nil {
		e.Params = []Param{}
	}
	return &e, nil
}

// List returns every defined endpoint.
func (s *Service) List(ctx context.Context) ([]Endpoint, error) {
	rows, err := s.mgr.MetaRead().QueryContext(ctx,
		`SELECT `+endpointColumns+` FROM api_endpoints e
		 LEFT JOIN databases d ON d.id = e.database_id
		 ORDER BY e.path, e.method`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Endpoint{}
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// Get returns one endpoint by id.
func (s *Service) Get(ctx context.Context, id string) (*Endpoint, error) {
	row := s.mgr.MetaRead().QueryRowContext(ctx,
		`SELECT `+endpointColumns+` FROM api_endpoints e
		 LEFT JOIN databases d ON d.id = e.database_id
		 WHERE e.id = ?`, id)
	e, err := scanEndpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEndpointNotFound
	}
	return e, err
}

// Create stores a new endpoint after validating it.
func (s *Service) Create(ctx context.Context, e *Endpoint) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if err := s.verifyBacking(ctx, e); err != nil {
		return err
	}
	if err := s.checkRouteConflicts(ctx, e); err != nil {
		return err
	}

	paramsJSON, err := json.Marshal(e.Params)
	if err != nil {
		return err
	}
	configJSON, err := json.Marshal(e.Config)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	e.ID = database.NewID()
	e.CreatedAt, e.UpdatedAt = now, now

	if _, err := s.mgr.MetaDB().ExecContext(ctx,
		`INSERT INTO api_endpoints (id, database_id, name, description, kind, method, path,
			table_name, sql_text, params, config, enabled, public, rate_limit, max_rows,
			created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.DatabaseID, e.Name, e.Description, string(e.Kind), e.Method, e.Path,
		e.Table, e.SQL, string(paramsJSON), string(configJSON),
		boolToInt(e.Enabled), boolToInt(e.Public), e.RateLimit, e.MaxRows,
		database.FormatTime(now), database.FormatTime(now)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return ErrRouteConflict
		}
		if strings.Contains(strings.ToLower(err.Error()), "foreign key") {
			return fmt.Errorf("the selected database does not exist")
		}
		return fmt.Errorf("create endpoint: %w", err)
	}
	s.invalidate()
	return nil
}

// Update replaces an endpoint definition.
func (s *Service) Update(ctx context.Context, e *Endpoint) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if err := s.verifyBacking(ctx, e); err != nil {
		return err
	}
	if err := s.checkRouteConflicts(ctx, e); err != nil {
		return err
	}

	paramsJSON, err := json.Marshal(e.Params)
	if err != nil {
		return err
	}
	configJSON, err := json.Marshal(e.Config)
	if err != nil {
		return err
	}

	res, err := s.mgr.MetaDB().ExecContext(ctx,
		`UPDATE api_endpoints SET database_id = ?, name = ?, description = ?, kind = ?,
			method = ?, path = ?, table_name = ?, sql_text = ?, params = ?, config = ?,
			enabled = ?, public = ?, rate_limit = ?, max_rows = ?, updated_at = ?
		 WHERE id = ?`,
		e.DatabaseID, e.Name, e.Description, string(e.Kind), e.Method, e.Path,
		e.Table, e.SQL, string(paramsJSON), string(configJSON),
		boolToInt(e.Enabled), boolToInt(e.Public), e.RateLimit, e.MaxRows,
		database.FormatTime(time.Now().UTC()), e.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return ErrRouteConflict
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrEndpointNotFound
	}
	s.invalidate()
	return nil
}

// SetEnabled toggles an endpoint without rewriting its definition.
func (s *Service) SetEnabled(ctx context.Context, id string, enabled bool) error {
	res, err := s.mgr.MetaDB().ExecContext(ctx,
		`UPDATE api_endpoints SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(enabled), database.FormatTime(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrEndpointNotFound
	}
	s.invalidate()
	return nil
}

// Delete removes an endpoint.
func (s *Service) Delete(ctx context.Context, id string) error {
	res, err := s.mgr.MetaDB().ExecContext(ctx, `DELETE FROM api_endpoints WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrEndpointNotFound
	}
	s.invalidate()
	return nil
}

// verifyBacking confirms the endpoint's database, and any backing table,
// actually exist, so a broken definition is caught at save time rather than on
// the first request.
func (s *Service) verifyBacking(ctx context.Context, e *Endpoint) error {
	meta, err := s.mgr.FindByID(ctx, e.DatabaseID)
	if err != nil {
		return fmt.Errorf("the selected database does not exist")
	}
	e.Database = meta.Name

	if e.Kind != KindCRUD {
		return nil
	}
	t, err := s.schema.GetTable(ctx, meta.Name, e.Table)
	if err != nil {
		return fmt.Errorf("table %q was not found in database %q", e.Table, meta.Name)
	}

	// Restricting columns only makes sense if those columns exist.
	valid := map[string]bool{}
	for _, c := range t.Columns {
		valid[c.Name] = true
	}
	for _, c := range append(append([]string{}, e.Config.ReadableColumns...), e.Config.WritableColumns...) {
		if !valid[c] {
			return fmt.Errorf("column %q does not exist in table %q", c, e.Table)
		}
	}
	return nil
}

// checkRouteConflicts rejects a definition whose routes collide with another
// endpoint's. The database's UNIQUE(method, path) index cannot catch this on
// its own, because a CRUD endpoint expands into several routes.
func (s *Service) checkRouteConflicts(ctx context.Context, e *Endpoint) error {
	existing, err := s.List(ctx)
	if err != nil {
		return err
	}

	mine := map[string]bool{}
	for _, r := range e.Routes() {
		mine[r.Method+" "+r.Path] = true
	}
	for _, other := range existing {
		if other.ID == e.ID {
			continue
		}
		for _, r := range other.Routes() {
			if mine[r.Method+" "+r.Path] {
				return fmt.Errorf("%w: %s %s is already served by %q",
					ErrRouteConflict, r.Method, r.Path, other.Name)
			}
		}
	}
	return nil
}

// invalidate drops the compiled route table so the next request rebuilds it.
func (s *Service) invalidate() {
	s.mu.Lock()
	s.router = nil
	s.mu.Unlock()
}

// getRouter returns the compiled route table, rebuilding it when stale.
func (s *Service) getRouter(ctx context.Context) (*router, error) {
	s.mu.RLock()
	r := s.router
	s.mu.RUnlock()
	if r != nil {
		return r, nil
	}

	endpoints, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	built := newRouter(endpoints)

	s.mu.Lock()
	// Another goroutine may have built one already; either is equally valid, so
	// keep whichever landed first to avoid discarding a warm table.
	if s.router == nil {
		s.router = built
	} else {
		built = s.router
	}
	s.mu.Unlock()
	return built, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
