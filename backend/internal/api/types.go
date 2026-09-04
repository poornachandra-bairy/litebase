// Package api implements the API Builder: administrator-defined REST endpoints
// backed either by a table's generated CRUD operations or by a custom SQL
// query with bound parameters.
package api

import "time"

// Kind distinguishes the two ways an endpoint is defined.
type Kind string

const (
	// KindCRUD exposes a table's rows through generated REST operations.
	KindCRUD Kind = "crud"
	// KindQuery runs an administrator-supplied SQL statement with bound
	// parameters.
	KindQuery Kind = "query"
)

// ParamIn is where a parameter's value is read from.
type ParamIn string

const (
	InQuery ParamIn = "query"
	InPath  ParamIn = "path"
	InBody  ParamIn = "body"
)

// ParamType is the type a parameter is validated and converted to.
type ParamType string

const (
	TypeString  ParamType = "string"
	TypeInteger ParamType = "integer"
	TypeNumber  ParamType = "number"
	TypeBoolean ParamType = "boolean"
)

// Param declares one parameter of a custom query endpoint.
//
// Parameters are bound to the statement's placeholders in declaration order.
// A value never becomes SQL text; it is always passed to SQLite as a bound
// argument.
type Param struct {
	Name        string    `json:"name"`
	In          ParamIn   `json:"in"`
	Type        ParamType `json:"type"`
	Required    bool      `json:"required"`
	Description string    `json:"description,omitempty"`

	// Default supplies a value when the caller omits an optional parameter.
	Default any `json:"default,omitempty"`

	// Validation constraints. Zero values mean "unconstrained".
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	MinLength *int     `json:"min_length,omitempty"`
	MaxLength *int     `json:"max_length,omitempty"`
	Enum      []string `json:"enum,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
}

// CRUDOperation names one generated REST operation.
type CRUDOperation string

const (
	OpList   CRUDOperation = "list"
	OpRead   CRUDOperation = "read"
	OpCreate CRUDOperation = "create"
	OpUpdate CRUDOperation = "update"
	OpDelete CRUDOperation = "delete"
)

// AllCRUDOperations lists every generated operation.
func AllCRUDOperations() []CRUDOperation {
	return []CRUDOperation{OpList, OpRead, OpCreate, OpUpdate, OpDelete}
}

// CRUDConfig configures a generated CRUD resource.
type CRUDConfig struct {
	// Operations are the enabled REST operations. An empty list means none,
	// which effectively disables the resource.
	Operations []CRUDOperation `json:"operations"`
	// ReadableColumns restricts which columns are returned. Empty means all,
	// which lets an operator hide a password or token column from the API.
	ReadableColumns []string `json:"readable_columns,omitempty"`
	// WritableColumns restricts which columns a caller may set. Empty means
	// all columns except generated primary keys.
	WritableColumns []string `json:"writable_columns,omitempty"`
	// DefaultLimit and MaxLimit bound list responses.
	DefaultLimit int `json:"default_limit"`
	MaxLimit     int `json:"max_limit"`
	// AllowFilters lets callers pass filter query parameters.
	AllowFilters bool `json:"allow_filters"`
	// AllowSearch lets callers pass a full-text-ish search term.
	AllowSearch bool `json:"allow_search"`
}

// Endpoint is an administrator-defined API endpoint.
type Endpoint struct {
	ID          string `json:"id"`
	DatabaseID  string `json:"database_id"`
	Database    string `json:"database"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Kind        Kind   `json:"kind"`

	// Method and Path define the route. For a CRUD endpoint, Path is the
	// resource base and Method is unused.
	Method string `json:"method"`
	Path   string `json:"path"`

	// Table is the backing table for a CRUD endpoint.
	Table string `json:"table"`
	// SQL is the statement for a query endpoint.
	SQL string `json:"sql"`

	Params []Param    `json:"params"`
	Config CRUDConfig `json:"config"`

	Enabled bool `json:"enabled"`
	// Public exposes the endpoint without an API key. It defaults to false so
	// that a newly created endpoint is never unintentionally open.
	Public bool `json:"public"`
	// RateLimit overrides the global API rate limit for this endpoint.
	RateLimit int `json:"rate_limit"`
	// MaxRows caps rows returned by this endpoint.
	MaxRows int `json:"max_rows"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Routes returns the concrete method/path pairs an endpoint serves.
func (e *Endpoint) Routes() []Route {
	if e.Kind == KindQuery {
		return []Route{{Method: e.Method, Path: e.Path, Operation: ""}}
	}

	base := e.Path
	itemPath := base + "/{id}"
	routes := []Route{}
	for _, op := range e.Config.Operations {
		switch op {
		case OpList:
			routes = append(routes, Route{Method: "GET", Path: base, Operation: OpList})
		case OpRead:
			routes = append(routes, Route{Method: "GET", Path: itemPath, Operation: OpRead})
		case OpCreate:
			routes = append(routes, Route{Method: "POST", Path: base, Operation: OpCreate})
		case OpUpdate:
			routes = append(routes, Route{Method: "PATCH", Path: itemPath, Operation: OpUpdate})
		case OpDelete:
			routes = append(routes, Route{Method: "DELETE", Path: itemPath, Operation: OpDelete})
		}
	}
	return routes
}

// Route is one concrete method/path an endpoint answers.
type Route struct {
	Method    string
	Path      string
	Operation CRUDOperation
}
