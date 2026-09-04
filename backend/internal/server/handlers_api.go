package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/litebase/litebase/internal/api"
	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/logging"
	"github.com/litebase/litebase/internal/middleware"
	"github.com/litebase/litebase/internal/query"
)

func (s *Server) handleListEndpoints(w http.ResponseWriter, r *http.Request) {
	list, err := s.api.List(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"endpoints": list})
}

func (s *Server) handleGetEndpoint(w http.ResponseWriter, r *http.Request) {
	e, err := s.api.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func (s *Server) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	var e api.Endpoint
	if err := httpx.DecodeJSON(w, r, &e); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// The id and timestamps are assigned by the server, never accepted from a
	// client.
	e.ID = ""
	if err := s.api.Create(r.Context(), &e); err != nil {
		fail(w, r, mapEndpointError(err))
		return
	}
	s.log.Info("endpoint created", "name", e.Name, "path", e.Path, "kind", e.Kind)
	httpx.JSON(w, http.StatusCreated, e)
}

func (s *Server) handleUpdateEndpoint(w http.ResponseWriter, r *http.Request) {
	var e api.Endpoint
	if err := httpx.DecodeJSON(w, r, &e); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	e.ID = r.PathValue("id")
	if err := s.api.Update(r.Context(), &e); err != nil {
		fail(w, r, mapEndpointError(err))
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func (s *Server) handleDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	if err := s.api.Delete(r.Context(), r.PathValue("id")); err != nil {
		fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (s *Server) handleToggleEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := s.api.SetEnabled(r.Context(), r.PathValue("id"), req.Enabled); err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"enabled": req.Enabled})
}

// handleTestEndpoint runs a candidate query without saving it, backing the
// API explorer's "try it" panel.
func (s *Server) handleTestEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Database string         `json:"database"`
		SQL      string         `json:"sql"`
		Params   []api.Param    `json:"params"`
		Values   map[string]any `json:"values"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	// The same validation the saved endpoint would get, so the test reflects
	// what will actually run.
	if err := api.ValidateParams(req.Params, "/"); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}
	if err := api.ValidateSQL(req.SQL, req.Params); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}

	args, err := api.BindParams(req.Params, api.RawValues{Body: req.Values})
	if err != nil {
		fail(w, r, err)
		return
	}

	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	resp, err := s.query.Execute(ctx, req.Database, req.SQL, query.ExecOptions{
		Params:   args,
		ReadOnly: query.IsReadOnly(req.SQL),
		MaxRows:  100,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	baseURL := s.cfg.BaseURL
	if baseURL == "" {
		baseURL = inferBaseURL(r)
	}
	doc, err := s.api.GenerateOpenAPI(r.Context(), baseURL, Version)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, doc)
}

// inferBaseURL reconstructs the public URL when none is configured.
func inferBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func mapEndpointError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, api.ErrRouteConflict):
		return httpx.Conflict("%s", err.Error())
	case errors.Is(err, api.ErrEndpointNotFound):
		return httpx.NotFound("%s", err.Error())
	}
	// A rejected definition is the administrator's input problem, and the
	// message explains exactly what to fix.
	if isDefinitionError(err) {
		return httpx.BadRequest("%s", err.Error())
	}
	return err
}

// isDefinitionError reports whether the error came from validating a submitted
// definition rather than from the database.
func isDefinitionError(err error) bool {
	msg := err.Error()
	for _, marker := range []string{
		"path", "parameter", "placeholder", "statement", "method", "name must",
		"unknown", "column", "table", "operation", "interval", "limit",
		"does not exist", "reserved", "SQL",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// handleGeneratedAPI dispatches a request to an administrator-defined endpoint.
func (s *Server) handleGeneratedAPI(w http.ResponseWriter, r *http.Request) {
	// Strip the mount prefix so stored paths are matched as written.
	path := strings.TrimPrefix(r.URL.Path, api.BasePath)
	if path == "" {
		path = "/"
	}

	match, allowed, err := s.api.Resolve(r.Context(), r.Method, path)
	switch {
	case errors.Is(err, api.ErrMethodNotAllowed):
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		httpx.Fail(w, r, httpx.UserError(http.StatusMethodNotAllowed, "method_not_allowed",
			"this endpoint does not accept "+r.Method, nil))
		return
	case errors.Is(err, api.ErrNoRoute):
		httpx.Fail(w, r, httpx.NotFound("no endpoint is defined for this path"))
		return
	case err != nil:
		fail(w, r, err)
		return
	}

	key, err := s.authenticateAPIRequest(r, match)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if key != nil {
		r = r.WithContext(middleware.WithAPIKey(r.Context(), key))
	}

	if err := s.checkAPIRateLimit(r, match, key); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	body, err := decodeAPIBody(w, r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	ctx, cancel := contextWithTimeout(r, s.cfg.QueryTimeout+5*time.Second)
	defer cancel()

	resp, err := s.api.Execute(ctx, match, api.Request{
		Method: r.Method,
		Path:   path,
		Query:  r.URL.Query(),
		Body:   body,
	})
	if err != nil {
		fail(w, r, err)
		return
	}

	if key != nil {
		// Usage tracking must never fail a request that already succeeded.
		if err := s.auth.TouchAPIKey(context.WithoutCancel(ctx), key.ID); err != nil {
			logging.FromContext(r.Context()).Debug("touch api key", "error", err)
		}
	}
	if resp.Status == http.StatusNoContent {
		httpx.NoContent(w)
		return
	}
	httpx.JSON(w, resp.Status, resp.Body)
}

// authenticateAPIRequest checks the API key for a generated endpoint.
func (s *Server) authenticateAPIRequest(r *http.Request, match *api.Match) (*auth.APIKey, error) {
	presented := middleware.ExtractAPIKey(r)

	if match.Endpoint.Public {
		// A public endpoint still honours a key when one is supplied, so its
		// own rate limit applies rather than the shared anonymous one.
		if presented == "" {
			return nil, nil
		}
	}
	if presented == "" {
		return nil, httpx.Unauthorized(
			"an API key is required; send it as 'Authorization: Bearer <key>' or 'X-API-Key: <key>'")
	}

	key, err := s.auth.VerifyAPIKey(r.Context(), presented)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrKeyExpired):
			return nil, httpx.Unauthorized("this API key has expired")
		case errors.Is(err, auth.ErrKeyDisabled):
			return nil, httpx.Unauthorized("this API key has been disabled")
		case errors.Is(err, auth.ErrKeyInvalid):
			return nil, httpx.Unauthorized("the API key is not valid")
		default:
			return nil, httpx.Internal(err)
		}
	}

	// The key must carry the scope the method needs and be granted this
	// specific endpoint.
	if !key.CanScope(auth.ScopeForMethod(r.Method)) {
		return nil, httpx.Forbidden("this API key does not have permission for " + r.Method + " requests")
	}
	if !key.CanEndpoint(match.Endpoint.ID) {
		return nil, httpx.Forbidden("this API key has not been granted access to this endpoint")
	}
	return key, nil
}

// checkAPIRateLimit applies the endpoint's or key's quota.
func (s *Server) checkAPIRateLimit(r *http.Request, match *api.Match, key *auth.APIKey) error {
	// Identify by key where one is present, so a single client cannot spend
	// another's quota, and by IP otherwise.
	identity := "ip:" + middleware.ClientIP(r, s.cfg.TrustProxy)
	capacity := float64(s.cfg.APIRatePerMin)

	if key != nil {
		identity = "key:" + key.ID
		if key.RateLimit > 0 {
			capacity = float64(key.RateLimit)
		}
	}
	// An endpoint-specific limit is the tighter constraint when set.
	if match.Endpoint.RateLimit > 0 {
		endpointIdentity := identity + "|endpoint:" + match.Endpoint.ID
		if ok, retry := s.apiLimiter.AllowN(endpointIdentity, float64(match.Endpoint.RateLimit), 1); !ok {
			return rateLimitError(retry)
		}
	}
	if ok, retry := s.apiLimiter.AllowN(identity, capacity, 1); !ok {
		return rateLimitError(retry)
	}
	return nil
}

func rateLimitError(retry time.Duration) error {
	seconds := int(retry.Seconds() + 0.999)
	if seconds < 1 {
		seconds = 1
	}
	return httpx.RateLimited(
		"rate limit exceeded; retry in about " + itoa(seconds) + " seconds")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// decodeAPIBody reads a JSON body for methods that carry one.
func decodeAPIBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodDelete:
		return nil, nil
	}
	if r.ContentLength == 0 {
		return map[string]any{}, nil
	}

	var body map[string]any
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, nil
}
