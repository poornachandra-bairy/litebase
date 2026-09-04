package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/litebase/litebase/internal/query"
	"github.com/litebase/litebase/internal/rows"
)

// Request is one call to a generated endpoint, already parsed by the HTTP
// layer.
type Request struct {
	Method string
	Path   string
	Query  map[string][]string
	Body   map[string]any
}

// Response is the result of executing an endpoint.
type Response struct {
	Status int
	Body   any
}

// Match resolves a request to an endpoint definition.
type Match struct {
	Endpoint   Endpoint
	Operation  CRUDOperation
	PathParams map[string]string
}

var (
	// ErrNoRoute means no enabled endpoint serves the path.
	ErrNoRoute = errors.New("no endpoint serves this path")
	// ErrMethodNotAllowed means the path exists but not for this method.
	ErrMethodNotAllowed = errors.New("method not allowed")
)

// Resolve finds the endpoint for a request.
func (s *Service) Resolve(ctx context.Context, method, path string) (*Match, []string, error) {
	r, err := s.getRouter(ctx)
	if err != nil {
		return nil, nil, err
	}

	route, params, ok := r.match(method, path)
	if !ok {
		if allowed := r.methodsFor(path); len(allowed) > 0 {
			return nil, allowed, ErrMethodNotAllowed
		}
		return nil, nil, ErrNoRoute
	}
	return &Match{Endpoint: route.endpoint, Operation: route.operation, PathParams: params}, nil, nil
}

// listEnvelope is the shape of a paginated list response.
type listEnvelope struct {
	Data       []map[string]any `json:"data"`
	Total      int64            `json:"total"`
	Limit      int              `json:"limit"`
	Offset     int              `json:"offset"`
	HasMore    bool             `json:"has_more"`
	Truncated  bool             `json:"truncated,omitempty"`
	DurationMS float64          `json:"duration_ms,omitempty"`
}

// Execute runs a matched endpoint.
func (s *Service) Execute(ctx context.Context, m *Match, req Request) (*Response, error) {
	if !m.Endpoint.Enabled {
		return nil, ErrNoRoute
	}
	if m.Endpoint.Kind == KindQuery {
		return s.executeQuery(ctx, m, req)
	}
	return s.executeCRUD(ctx, m, req)
}

// executeQuery runs a custom SQL endpoint with bound parameters.
func (s *Service) executeQuery(ctx context.Context, m *Match, req Request) (*Response, error) {
	e := m.Endpoint

	args, err := BindParams(e.Params, RawValues{
		Path:  m.PathParams,
		Query: req.Query,
		Body:  req.Body,
	})
	if err != nil {
		return nil, err
	}

	// The statement is the administrator's stored text, unchanged; the caller's
	// values travel only as bound arguments.
	resp, err := s.query.Execute(ctx, e.Database, e.SQL, query.ExecOptions{
		Params:   args,
		MaxRows:  e.MaxRows,
		ReadOnly: query.IsReadOnly(e.SQL),
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return &Response{Status: 200, Body: listEnvelope{Data: []map[string]any{}}}, nil
	}

	result := resp.Results[0]
	if result.Error != "" {
		// The statement is administrator-authored, so its failure is a server
		// fault from the caller's point of view.
		return nil, fmt.Errorf("the endpoint's query failed: %s", result.Error)
	}

	// A statement that returns rows answers with them; one that writes reports
	// what it changed.
	if result.Kind == query.KindSelect || len(result.Columns) > 0 {
		return &Response{Status: 200, Body: listEnvelope{
			Data:       result.Rows,
			Total:      int64(len(result.Rows)),
			Limit:      len(result.Rows),
			Truncated:  result.Truncated,
			DurationMS: result.DurationMS,
		}}, nil
	}

	status := 200
	if e.Method == "POST" {
		status = 201
	}
	return &Response{Status: status, Body: map[string]any{
		"rows_affected":  result.RowsAffected,
		"last_insert_id": result.LastInsertID,
		"duration_ms":    result.DurationMS,
	}}, nil
}

// executeCRUD runs one generated table operation.
func (s *Service) executeCRUD(ctx context.Context, m *Match, req Request) (*Response, error) {
	e := m.Endpoint

	switch m.Operation {
	case OpList:
		return s.crudList(ctx, e, req)
	case OpRead:
		return s.crudRead(ctx, e, m)
	case OpCreate:
		return s.crudCreate(ctx, e, req)
	case OpUpdate:
		return s.crudUpdate(ctx, e, m, req)
	case OpDelete:
		return s.crudDelete(ctx, e, m)
	default:
		return nil, ErrNoRoute
	}
}

func (s *Service) crudList(ctx context.Context, e Endpoint, req Request) (*Response, error) {
	opts := rows.ListOptions{
		Limit:   e.Config.DefaultLimit,
		Columns: e.Config.ReadableColumns,
	}

	if v := firstValue(req.Query, "limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, &ValidationError{Errors: []ParamError{{"limit", "must be a positive whole number"}}}
		}
		opts.Limit = min(n, e.Config.MaxLimit)
	}
	if v := firstValue(req.Query, "offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, &ValidationError{Errors: []ParamError{{"offset", "must be zero or a positive whole number"}}}
		}
		opts.Offset = n
	}
	if e.Config.AllowSearch {
		opts.Search = firstValue(req.Query, "search")
	}

	if v := firstValue(req.Query, "sort"); v != "" {
		sorts, err := parseSort(v)
		if err != nil {
			return nil, &ValidationError{Errors: []ParamError{{"sort", err.Error()}}}
		}
		opts.Sorts = sorts
	}

	if e.Config.AllowFilters {
		filters, err := parseFilters(req.Query)
		if err != nil {
			return nil, err
		}
		opts.Filters = filters
	}

	page, err := s.rows.List(ctx, e.Database, e.Table, opts)
	if err != nil {
		return nil, err
	}
	return &Response{Status: 200, Body: listEnvelope{
		Data:    page.Rows,
		Total:   page.Total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.HasMore,
	}}, nil
}

func (s *Service) crudRead(ctx context.Context, e Endpoint, m *Match) (*Response, error) {
	key, err := s.primaryKeyFor(ctx, e, m.PathParams["id"])
	if err != nil {
		return nil, err
	}
	row, err := s.rows.Get(ctx, e.Database, e.Table, key)
	if err != nil {
		return nil, err
	}
	return &Response{Status: 200, Body: map[string]any{"data": filterColumns(row, e.Config.ReadableColumns)}}, nil
}

func (s *Service) crudCreate(ctx context.Context, e Endpoint, req Request) (*Response, error) {
	if req.Body == nil {
		return nil, &ValidationError{Errors: []ParamError{{"body", "a JSON object is required"}}}
	}
	values, err := allowedWrites(req.Body, e.Config.WritableColumns)
	if err != nil {
		return nil, err
	}
	row, err := s.rows.Insert(ctx, e.Database, e.Table, values)
	if err != nil {
		return nil, err
	}
	return &Response{Status: 201, Body: map[string]any{"data": filterColumns(row, e.Config.ReadableColumns)}}, nil
}

func (s *Service) crudUpdate(ctx context.Context, e Endpoint, m *Match, req Request) (*Response, error) {
	if req.Body == nil {
		return nil, &ValidationError{Errors: []ParamError{{"body", "a JSON object is required"}}}
	}
	key, err := s.primaryKeyFor(ctx, e, m.PathParams["id"])
	if err != nil {
		return nil, err
	}
	values, err := allowedWrites(req.Body, e.Config.WritableColumns)
	if err != nil {
		return nil, err
	}
	row, err := s.rows.Update(ctx, e.Database, e.Table, key, values)
	if err != nil {
		return nil, err
	}
	return &Response{Status: 200, Body: map[string]any{"data": filterColumns(row, e.Config.ReadableColumns)}}, nil
}

func (s *Service) crudDelete(ctx context.Context, e Endpoint, m *Match) (*Response, error) {
	key, err := s.primaryKeyFor(ctx, e, m.PathParams["id"])
	if err != nil {
		return nil, err
	}
	if err := s.rows.Delete(ctx, e.Database, e.Table, key); err != nil {
		return nil, err
	}
	return &Response{Status: 204}, nil
}

// primaryKeyFor maps the {id} path segment onto the table's key column.
func (s *Service) primaryKeyFor(ctx context.Context, e Endpoint, id string) (map[string]any, error) {
	t, err := s.schema.GetTable(ctx, e.Database, e.Table)
	if err != nil {
		return nil, err
	}

	var pk []string
	for _, c := range t.Columns {
		if c.PrimaryKey {
			pk = append(pk, c.Name)
		}
	}
	switch len(pk) {
	case 0:
		// Without a declared key the implicit rowid addresses the row.
		return map[string]any{"_rowid": id}, nil
	case 1:
		return map[string]any{pk[0]: id}, nil
	default:
		// A composite key cannot be expressed in one path segment.
		return nil, fmt.Errorf("table %q has a composite primary key and cannot be addressed by a single id", e.Table)
	}
}

// filterColumns drops columns the endpoint does not expose, so a hidden column
// cannot leak through a read-back after a write.
func filterColumns(row map[string]any, readable []string) map[string]any {
	if len(readable) == 0 || row == nil {
		return row
	}
	allowed := make(map[string]bool, len(readable))
	for _, c := range readable {
		allowed[c] = true
	}
	out := make(map[string]any, len(readable))
	for k, v := range row {
		if allowed[k] {
			out[k] = v
		}
	}
	return out
}

// allowedWrites rejects a body that sets a column the endpoint does not expose
// for writing, rather than silently ignoring it.
func allowedWrites(body map[string]any, writable []string) (map[string]any, error) {
	if len(writable) == 0 {
		return body, nil
	}
	allowed := make(map[string]bool, len(writable))
	for _, c := range writable {
		allowed[c] = true
	}

	verr := &ValidationError{}
	out := make(map[string]any, len(body))
	for k, v := range body {
		if !allowed[k] {
			verr.Errors = append(verr.Errors, ParamError{k, "this field cannot be set through this endpoint"})
			continue
		}
		out[k] = v
	}
	if len(verr.Errors) > 0 {
		return nil, verr
	}
	return out, nil
}

// parseSort reads a "sort" query value such as "-created_at,name".
func parseSort(v string) ([]rows.Sort, error) {
	parts := strings.Split(v, ",")
	if len(parts) > 5 {
		return nil, fmt.Errorf("at most 5 sort columns are allowed")
	}
	out := make([]rows.Sort, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		desc := false
		if strings.HasPrefix(p, "-") {
			desc, p = true, p[1:]
		}
		// The column is checked against the table's real columns downstream,
		// where an unknown name is rejected.
		out = append(out, rows.Sort{Column: p, Desc: desc})
	}
	return out, nil
}

// filterParamRe matches "filter[column][op]=value" style query parameters.
var reservedListParams = map[string]bool{
	"limit": true, "offset": true, "sort": true, "search": true,
}

// parseFilters reads filter parameters of the form "column=value" or
// "column__op=value", for example "price__lte=100".
func parseFilters(q map[string][]string) ([]rows.Filter, error) {
	verr := &ValidationError{}
	out := []rows.Filter{}

	for key, values := range q {
		if reservedListParams[key] || len(values) == 0 {
			continue
		}
		column, op := key, rows.OpEq
		if i := strings.LastIndex(key, "__"); i > 0 {
			column = key[:i]
			op = rows.Operator(key[i+2:])
		}
		if !rows.ValidOperator(op) {
			verr.Errors = append(verr.Errors, ParamError{key,
				fmt.Sprintf("unknown filter operator %q", op)})
			continue
		}

		f := rows.Filter{Column: column, Op: op}
		switch op {
		case rows.OpIn, rows.OpNotIn:
			parts := strings.Split(values[0], ",")
			f.Values = make([]any, 0, len(parts))
			for _, p := range parts {
				f.Values = append(f.Values, p)
			}
		case rows.OpBetween:
			parts := strings.SplitN(values[0], ",", 2)
			if len(parts) != 2 {
				verr.Errors = append(verr.Errors, ParamError{key, "between needs two comma-separated values"})
				continue
			}
			f.Values = []any{parts[0], parts[1]}
		case rows.OpIsNull, rows.OpIsNotNull:
			// No operand.
		default:
			f.Value = values[0]
		}
		out = append(out, f)
	}

	if len(verr.Errors) > 0 {
		return nil, verr
	}
	return out, nil
}

func firstValue(q map[string][]string, key string) string {
	if vs, ok := q[key]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}
