package server

import (
	"net/http"
	"time"

	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/middleware"
)

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.auth.ListAPIKeys(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"keys": keys})
}

type createKeyRequest struct {
	Name         string     `json:"name"`
	Scopes       []string   `json:"scopes"`
	AllEndpoints bool       `json:"all_endpoints"`
	EndpointIDs  []string   `json:"endpoint_ids"`
	RateLimit    int        `json:"rate_limit"`
	ExpiresAt    *time.Time `json:"expires_at"`
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	user := middleware.UserFrom(r.Context())

	key, plaintext, err := s.auth.CreateAPIKey(r.Context(), auth.CreateAPIKeyInput{
		Name:         req.Name,
		Scopes:       req.Scopes,
		AllEndpoints: req.AllEndpoints,
		EndpointIDs:  req.EndpointIDs,
		RateLimit:    req.RateLimit,
		ExpiresAt:    req.ExpiresAt,
		CreatedBy:    userID(user),
	})
	if err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}
	s.log.Info("api key created", "key_id", key.ID, "name", key.Name, "user_id", userID(user))

	// The plaintext is returned exactly once. It is not stored anywhere, so it
	// cannot be shown again and the response says so explicitly.
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"key":     key,
		"secret":  plaintext,
		"warning": "Copy this key now. It is stored only as a hash and cannot be shown again.",
	})
}

func (s *Server) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		createKeyRequest
		Disabled bool `json:"disabled"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	if err := s.auth.UpdateAPIKey(r.Context(), r.PathValue("id"), auth.UpdateAPIKeyInput{
		Name:         req.Name,
		Scopes:       req.Scopes,
		AllEndpoints: req.AllEndpoints,
		EndpointIDs:  req.EndpointIDs,
		RateLimit:    req.RateLimit,
		Disabled:     req.Disabled,
		ExpiresAt:    req.ExpiresAt,
	}); err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "updated"})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.DeleteAPIKey(r.Context(), r.PathValue("id")); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Info("api key revoked", "key_id", r.PathValue("id"))
	httpx.NoContent(w)
}

func userID(u *auth.User) string {
	if u == nil {
		return ""
	}
	return u.ID
}
