package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/logging"
)

type (
	userKey    struct{}
	sessionKey struct{}
	apiKeyKey  struct{}
)

// SessionCookieName is the cookie holding the admin session token.
const SessionCookieName = "litebase_session"

// CSRFCookieName is the readable cookie holding the CSRF token for the
// double-submit check.
const CSRFCookieName = "litebase_csrf"

// CSRFHeaderName is where the frontend echoes the CSRF token back.
const CSRFHeaderName = "X-CSRF-Token"

// UserFrom returns the authenticated administrator, if any.
func UserFrom(ctx context.Context) *auth.User {
	u, _ := ctx.Value(userKey{}).(*auth.User)
	return u
}

// SessionFrom returns the active session, if any.
func SessionFrom(ctx context.Context) *auth.Session {
	s, _ := ctx.Value(sessionKey{}).(*auth.Session)
	return s
}

// APIKeyFrom returns the API key that authenticated the request, if any.
func APIKeyFrom(ctx context.Context) *auth.APIKey {
	k, _ := ctx.Value(apiKeyKey{}).(*auth.APIKey)
	return k
}

// WithUser attaches a user and session to a context. It exists for tests and
// for handlers that re-authenticate mid-request.
func WithUser(ctx context.Context, u *auth.User, s *auth.Session) context.Context {
	ctx = context.WithValue(ctx, userKey{}, u)
	return context.WithValue(ctx, sessionKey{}, s)
}

// WithAPIKey attaches an API key to a context.
func WithAPIKey(ctx context.Context, k *auth.APIKey) context.Context {
	return context.WithValue(ctx, apiKeyKey{}, k)
}

// RequireSession authenticates admin dashboard requests from the session
// cookie and rejects anything unauthenticated.
func RequireSession(svc *auth.Service) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil || cookie.Value == "" {
				httpx.Fail(w, r, httpx.Unauthorized("sign in to continue"))
				return
			}

			sess, user, err := svc.LookupSession(r.Context(), cookie.Value)
			if err != nil {
				switch {
				case errors.Is(err, auth.ErrSessionNotFound):
					httpx.Fail(w, r, httpx.Unauthorized("your session has expired; sign in again"))
				case errors.Is(err, auth.ErrUserDisabled):
					httpx.Fail(w, r, httpx.Forbidden("this account has been disabled"))
				default:
					httpx.Fail(w, r, httpx.Internal(err))
				}
				return
			}

			// Sliding expiry keeps an active operator signed in; the write is
			// rate-limited inside TouchSession.
			if err := svc.TouchSession(r.Context(), sess); err != nil {
				logging.FromContext(r.Context()).Warn("touch session", "error", err)
			}

			ctx := WithUser(r.Context(), user, sess)
			ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("user_id", user.ID))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequirePermission rejects an authenticated user who lacks a permission.
func RequirePermission(p auth.Permission) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := UserFrom(r.Context())
			if u == nil {
				httpx.Fail(w, r, httpx.Unauthorized(""))
				return
			}
			if !u.Can(p) {
				httpx.Fail(w, r, httpx.Forbidden(
					"your role does not allow this action"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRF protects cookie-authenticated state-changing requests.
//
// The session cookie is SameSite=Lax, which already blocks cross-site form
// posts, and this double-submit check is the second layer: an attacker's page
// cannot read the CSRF cookie from another origin, so it cannot reproduce the
// header. Requests that authenticate with an API key are exempt, because a
// bearer credential is never attached automatically by a browser.
func CSRF() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			// An API-key request carries no ambient authority to abuse.
			if APIKeyFrom(r.Context()) != nil {
				next.ServeHTTP(w, r)
				return
			}
			sess := SessionFrom(r.Context())
			if sess == nil {
				next.ServeHTTP(w, r)
				return
			}

			token := r.Header.Get(CSRFHeaderName)
			if token == "" {
				token = r.FormValue("csrf_token")
			}
			if !sess.VerifyCSRF(token) {
				httpx.Fail(w, r, httpx.Forbidden(
					"missing or invalid CSRF token; reload the page and try again"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// ExtractAPIKey pulls a key from the Authorization or X-API-Key header.
func ExtractAPIKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
		if after, ok := strings.CutPrefix(h, "ApiKey "); ok {
			return strings.TrimSpace(after)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}
