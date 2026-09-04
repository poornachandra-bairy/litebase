package server

import (
	"net/http"

	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/middleware"
)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"users": users,
		"roles": auth.Roles(),
	})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	user, err := s.auth.CreateUser(r.Context(), req.Email, req.Name, req.Password, auth.Role(req.Role))
	if err != nil {
		fail(w, r, err)
		return
	}
	s.log.Info("user created", "user_id", user.ID, "role", user.Role)
	httpx.JSON(w, http.StatusCreated, user)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		Disabled bool   `json:"disabled"`
		Password string `json:"password,omitempty"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	id := r.PathValue("id")
	actor := middleware.UserFrom(r.Context())
	// Disabling your own account would lock you out of the instance mid-request.
	if actor != nil && actor.ID == id && req.Disabled {
		httpx.Fail(w, r, httpx.BadRequest("you cannot disable your own account"))
		return
	}

	user, err := s.auth.UpdateUser(r.Context(), id, req.Name, auth.Role(req.Role), req.Disabled)
	if err != nil {
		fail(w, r, err)
		return
	}
	if req.Password != "" {
		// An owner-initiated reset does not require the old password, but it
		// does revoke that user's sessions.
		if err := s.auth.SetPassword(r.Context(), id, req.Password); err != nil {
			fail(w, r, err)
			return
		}
		s.log.Warn("password reset by administrator", "target_user", id, "by", userID(actor))
	}
	httpx.JSON(w, http.StatusOK, user)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	actor := middleware.UserFrom(r.Context())
	if actor != nil && actor.ID == id {
		httpx.Fail(w, r, httpx.BadRequest("you cannot delete your own account"))
		return
	}
	if err := s.auth.DeleteUser(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Warn("user deleted", "user_id", id, "by", userID(actor))
	httpx.NoContent(w)
}
