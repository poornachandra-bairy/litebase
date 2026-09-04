package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/litebase/litebase/internal/httpx"
)

// SecureHeaders sets conservative response headers on every request.
func SecureHeaders(devMode bool) Middleware {
	// The dashboard is a self-contained bundle, so everything can be locked to
	// the same origin. In development the Vite client needs websocket and
	// inline-script access for hot reload, which is why the policy is relaxed
	// only when dev mode is explicitly enabled.
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
	if devMode {
		csp = strings.Join([]string{
			"default-src 'self'",
			"script-src 'self' 'unsafe-inline' 'unsafe-eval'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data: blob:",
			"font-src 'self' data:",
			"connect-src 'self' ws: wss:",
			"object-src 'none'",
			"base-uri 'none'",
			"frame-ancestors 'none'",
		}, "; ")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), payment=(), usb=()")
			h.Set("Content-Security-Policy", csp)

			// HSTS is only meaningful over TLS, and asserting it on a plain
			// HTTP deployment would lock users out of their own dashboard.
			if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit caps how much a request may send, protecting memory and disk.
func BodyLimit(limit int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// ContentLength is a fast pre-check; MaxBytesReader is the actual
			// enforcement, since the header may be absent or dishonest.
			if r.ContentLength > limit {
				httpx.Fail(w, r, httpx.TooLarge(
					fmt.Sprintf("request body exceeds the %d byte limit", limit)))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// CORS answers cross-origin requests for the configured origins.
//
// Credentials are only allowed for explicitly listed origins; a wildcard is
// never combined with cookie access, which would let any site drive the
// dashboard API as the logged-in administrator.
func CORS(allowed []string) Middleware {
	allowSet := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		allowSet[strings.TrimSuffix(strings.TrimSpace(o), "/")] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSuffix(r.Header.Get("Origin"), "/")
			if origin != "" && allowSet[origin] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Headers",
					"Content-Type, Authorization, X-CSRF-Token, X-API-Key")
				h.Set("Access-Control-Allow-Methods",
					"GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Max-Age", "600")
				// The response varies per origin, so caches must key on it.
				h.Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
