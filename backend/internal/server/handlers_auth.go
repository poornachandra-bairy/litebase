package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/middleware"
)

// Version is stamped at build time.
var Version = "dev"

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Health must stay cheap and must not disclose anything about the
	// deployment beyond the fact that the process is up.
	httpx.JSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": Version,
		"uptime":  time.Since(s.started).Round(time.Second).String(),
	})
}

// handleReady reports whether the server can actually serve requests, which
// means the metadata database is reachable.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 3*time.Second)
	defer cancel()

	if err := s.mgr.MetaRead().PingContext(ctx); err != nil {
		s.log.Error("readiness check failed", "error", err)
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"reason": "the metadata database is not reachable",
		})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type sessionResponse struct {
	User      *auth.User `json:"user"`
	CSRFToken string     `json:"csrf_token"`
	ExpiresAt time.Time  `json:"expires_at"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	user, err := s.auth.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			// The same message for a wrong password and an unknown address, so
			// the response cannot be used to enumerate accounts.
			httpx.Fail(w, r, httpx.Unauthorized("incorrect email or password"))
		case errors.Is(err, auth.ErrUserDisabled):
			httpx.Fail(w, r, httpx.Forbidden("this account has been disabled"))
		default:
			httpx.Fail(w, r, httpx.Internal(err))
		}
		return
	}

	token, sess, err := s.auth.CreateSession(r.Context(), user.ID,
		r.UserAgent(), middleware.ClientIP(r, s.cfg.TrustProxy))
	if err != nil {
		httpx.Fail(w, r, httpx.Internal(err))
		return
	}

	s.setSessionCookies(w, r, token, sess.CSRFToken, s.auth.SessionTTL())
	s.log.Info("administrator signed in", "user_id", user.ID,
		"ip", middleware.ClientIP(r, s.cfg.TrustProxy))

	httpx.JSON(w, http.StatusOK, sessionResponse{
		User: user, CSRFToken: sess.CSRFToken, ExpiresAt: sess.ExpiresAt,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Logout works from the cookie alone: it revokes access, so requiring a
	// valid session first would only make signing out harder.
	if cookie, err := r.Cookie(middleware.SessionCookieName); err == nil && cookie.Value != "" {
		if sess, _, lookupErr := s.auth.LookupSession(r.Context(), cookie.Value); lookupErr == nil {
			if err := s.auth.DeleteSession(r.Context(), sess.ID); err != nil {
				s.log.Warn("delete session", "error", err)
			}
		}
	}
	s.clearSessionCookies(w, r)
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "signed out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFrom(r.Context())
	sess := middleware.SessionFrom(r.Context())
	if user == nil || sess == nil {
		httpx.Fail(w, r, httpx.Unauthorized(""))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"user":        user,
		"csrf_token":  sess.CSRFToken,
		"expires_at":  sess.ExpiresAt,
		"permissions": user.Role.Permissions(),
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFrom(r.Context())
	sess := middleware.SessionFrom(r.Context())
	if user == nil || sess == nil {
		httpx.Fail(w, r, httpx.Unauthorized(""))
		return
	}

	var req changePasswordRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	// The current session is kept so the operator is not signed out of the tab
	// they just changed their password in; every other session is revoked.
	err := s.auth.ChangePassword(r.Context(), user.ID, req.CurrentPassword, req.NewPassword, sess.ID)
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrInvalidCredentials):
		httpx.Fail(w, r, httpx.Unauthorized("your current password is incorrect"))
		return
	case errors.Is(err, auth.ErrWeakPassword):
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	default:
		httpx.Fail(w, r, httpx.Internal(err))
		return
	}

	s.log.Info("password changed", "user_id", user.ID)
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "password changed"})
}

// requestIsHTTPS reports whether the request reached the server over TLS,
// either directly or through a terminating reverse proxy.
//
// This is used only to decide whether to mark cookies Secure, so trusting the
// forwarded header here is safe: the worst a spoofed value can do is make the
// browser refuse to send the cookie back over plain HTTP, which fails closed.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// setSessionCookies writes the session and CSRF cookies.
func (s *Server) setSessionCookies(w http.ResponseWriter, r *http.Request, token, csrf string, ttl time.Duration) {
	// Secure is set when configured, and also whenever the request itself came
	// over HTTPS. That means a deployment behind a TLS-terminating proxy gets
	// Secure cookies automatically, without the operator having to remember a
	// setting, while a plain HTTP install on localhost still works.
	secure := s.cfg.SecureCookies || requestIsHTTPS(r)
	// The session cookie is HttpOnly so page scripts cannot read it, which
	// contains the damage from any XSS. SameSite=Lax stops it riding along with
	// cross-site form posts.
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    token,
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
	// The CSRF cookie is deliberately readable: the frontend echoes it back in
	// a header, and an attacker's origin cannot read it to do the same.
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.CSRFCookieName,
		Value:    csrf,
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

func (s *Server) clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := s.cfg.SecureCookies || requestIsHTTPS(r)
	for _, name := range []string{middleware.SessionCookieName, middleware.CSRFCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			Domain:   s.cfg.CookieDomain,
			HttpOnly: name == middleware.SessionCookieName,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
	}
}
