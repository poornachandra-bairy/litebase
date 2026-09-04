package api

import (
	"context"
	"fmt"
	"strings"
)

// OpenAPI documentation is generated from the stored endpoint definitions, so
// it cannot drift from what the server actually serves.

// Document is a minimal OpenAPI 3.1 document.
type Document struct {
	OpenAPI    string         `json:"openapi"`
	Info       Info           `json:"info"`
	Servers    []Server       `json:"servers"`
	Paths      map[string]any `json:"paths"`
	Components Components     `json:"components"`
}

type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

type Server struct {
	URL string `json:"url"`
}

type Components struct {
	SecuritySchemes map[string]any `json:"securitySchemes"`
}

// BasePath is where generated endpoints are mounted.
const BasePath = "/api"

// GenerateOpenAPI builds an OpenAPI document describing every enabled endpoint.
func (s *Service) GenerateOpenAPI(ctx context.Context, baseURL, version string) (*Document, error) {
	endpoints, err := s.List(ctx)
	if err != nil {
		return nil, err
	}

	doc := &Document{
		OpenAPI: "3.1.0",
		Info: Info{
			Title:       "Litebase Data API",
			Version:     version,
			Description: "Endpoints defined in the Litebase API Builder. Authenticate with an API key sent as `Authorization: Bearer <key>` or `X-API-Key: <key>`.",
		},
		Servers: []Server{{URL: strings.TrimSuffix(baseURL, "/") + BasePath}},
		Paths:   map[string]any{},
		Components: Components{
			SecuritySchemes: map[string]any{
				"ApiKeyAuth": map[string]any{
					"type": "http", "scheme": "bearer",
					"description": "An API key issued from the API Keys page.",
				},
				"ApiKeyHeader": map[string]any{
					"type": "apiKey", "in": "header", "name": "X-API-Key",
				},
			},
		},
	}

	for _, e := range endpoints {
		if !e.Enabled {
			continue
		}
		for _, route := range e.Routes() {
			pathItem, _ := doc.Paths[route.Path].(map[string]any)
			if pathItem == nil {
				pathItem = map[string]any{}
				doc.Paths[route.Path] = pathItem
			}
			pathItem[strings.ToLower(route.Method)] = s.operationFor(ctx, e, route)
		}
	}
	return doc, nil
}

func (s *Service) operationFor(ctx context.Context, e Endpoint, route Route) map[string]any {
	op := map[string]any{
		"summary":     e.Name,
		"description": e.Description,
		"tags":        []string{e.Database},
		"responses":   defaultResponses(route),
	}
	if !e.Public {
		op["security"] = []map[string][]string{
			{"ApiKeyAuth": {}}, {"ApiKeyHeader": {}},
		}
	}

	params := []map[string]any{}
	if e.Kind == KindQuery {
		for _, p := range e.Params {
			if p.In == InBody {
				continue
			}
			params = append(params, map[string]any{
				"name":        p.Name,
				"in":          string(p.In),
				"required":    p.Required,
				"description": p.Description,
				"schema":      schemaFor(p),
			})
		}
		if body := bodySchemaFor(e.Params); body != nil {
			op["requestBody"] = body
		}
	} else {
		params = append(params, crudParams(e, route)...)
		if route.Operation == OpCreate || route.Operation == OpUpdate {
			op["requestBody"] = s.crudBodySchema(ctx, e)
		}
	}
	if len(params) > 0 {
		op["parameters"] = params
	}
	return op
}

func crudParams(e Endpoint, route Route) []map[string]any {
	if route.Operation != OpList {
		if strings.Contains(route.Path, "{id}") {
			return []map[string]any{{
				"name": "id", "in": "path", "required": true,
				"description": "The row's primary key.",
				"schema":      map[string]any{"type": "string"},
			}}
		}
		return nil
	}

	out := []map[string]any{
		{
			"name": "limit", "in": "query", "required": false,
			"description": fmt.Sprintf("Rows to return (default %d, maximum %d).",
				e.Config.DefaultLimit, e.Config.MaxLimit),
			"schema": map[string]any{"type": "integer", "minimum": 1, "maximum": e.Config.MaxLimit},
		},
		{
			"name": "offset", "in": "query", "required": false,
			"description": "Rows to skip.",
			"schema":      map[string]any{"type": "integer", "minimum": 0},
		},
		{
			"name": "sort", "in": "query", "required": false,
			"description": "Comma-separated columns; prefix with '-' for descending, e.g. '-created_at,name'.",
			"schema":      map[string]any{"type": "string"},
		},
	}
	if e.Config.AllowSearch {
		out = append(out, map[string]any{
			"name": "search", "in": "query", "required": false,
			"description": "Matches the term against every text column.",
			"schema":      map[string]any{"type": "string"},
		})
	}
	if e.Config.AllowFilters {
		out = append(out, map[string]any{
			"name": "column__op", "in": "query", "required": false,
			"description": "Filter rows, for example `price__lte=100` or `category=tools`. " +
				"Operators: eq, neq, gt, gte, lt, lte, like, contains, starts_with, ends_with, in, not_in, between, is_null, is_not_null.",
			"schema": map[string]any{"type": "string"},
		})
	}
	return out
}

func (s *Service) crudBodySchema(ctx context.Context, e Endpoint) map[string]any {
	properties := map[string]any{}

	t, err := s.schema.GetTable(ctx, e.Database, e.Table)
	if err == nil {
		writable := map[string]bool{}
		for _, c := range e.Config.WritableColumns {
			writable[c] = true
		}
		for _, c := range t.Columns {
			if len(writable) > 0 && !writable[c.Name] {
				continue
			}
			// A generated key is supplied by the database, not the caller.
			if c.PrimaryKey && c.AutoIncrement {
				continue
			}
			properties[c.Name] = map[string]any{
				"type":        jsonTypeForColumn(c.Type),
				"description": strings.TrimSpace(c.Type),
			}
		}
	}

	return map[string]any{
		"required": true,
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{"type": "object", "properties": properties},
			},
		},
	}
}

func jsonTypeForColumn(declared string) string {
	switch strings.ToUpper(strings.Split(declared, "(")[0]) {
	case "INTEGER", "INT", "BIGINT", "SMALLINT", "TINYINT":
		return "integer"
	case "REAL", "DOUBLE", "FLOAT", "NUMERIC", "DECIMAL":
		return "number"
	case "BOOLEAN":
		return "boolean"
	default:
		return "string"
	}
}

func bodySchemaFor(params []Param) map[string]any {
	properties := map[string]any{}
	required := []string{}
	for _, p := range params {
		if p.In != InBody {
			continue
		}
		properties[p.Name] = schemaFor(p)
		if p.Required {
			required = append(required, p.Name)
		}
	}
	if len(properties) == 0 {
		return nil
	}

	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"required": len(required) > 0,
		"content":  map[string]any{"application/json": map[string]any{"schema": schema}},
	}
}

func schemaFor(p Param) map[string]any {
	schema := map[string]any{"type": string(p.Type)}
	if p.Type == "" {
		schema["type"] = "string"
	}
	if p.Default != nil {
		schema["default"] = p.Default
	}
	if p.Min != nil {
		schema["minimum"] = *p.Min
	}
	if p.Max != nil {
		schema["maximum"] = *p.Max
	}
	if p.MinLength != nil {
		schema["minLength"] = *p.MinLength
	}
	if p.MaxLength != nil {
		schema["maxLength"] = *p.MaxLength
	}
	if p.Pattern != "" {
		schema["pattern"] = p.Pattern
	}
	if len(p.Enum) > 0 {
		schema["enum"] = p.Enum
	}
	return schema
}

func defaultResponses(route Route) map[string]any {
	responses := map[string]any{
		"200": map[string]any{"description": "Success"},
		"400": map[string]any{"description": "The request was malformed"},
		"401": map[string]any{"description": "A valid API key is required"},
		"403": map[string]any{"description": "The key may not call this endpoint"},
		"422": map[string]any{"description": "A parameter failed validation"},
		"429": map[string]any{"description": "Rate limit exceeded"},
	}
	switch route.Operation {
	case OpCreate:
		delete(responses, "200")
		responses["201"] = map[string]any{"description": "Created"}
	case OpDelete:
		delete(responses, "200")
		responses["204"] = map[string]any{"description": "Deleted"}
	}
	if route.Operation == OpRead || route.Operation == OpUpdate || route.Operation == OpDelete {
		responses["404"] = map[string]any{"description": "No such row"}
	}
	return responses
}
