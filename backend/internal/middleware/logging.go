package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/logging"
)

// RequestID assigns each request an identifier, exposes it on the response and
// binds a logger carrying it to the request context.
func RequestID(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := database.NewID()
			ctx := httpx.WithRequestID(r.Context(), id)
			ctx = logging.WithLogger(ctx, logger.With("request_id", id))
			w.Header().Set("X-Request-ID", id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestLogger emits one structured line per completed request.
func RequestLogger() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(sw, r)

			log := logging.FromContext(r.Context())
			// Only the path is logged, never the query string, which can carry
			// filter values drawn from user data.
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
				"bytes", sw.written,
			}
			switch {
			case sw.status >= 500:
				log.Error("request", attrs...)
			case sw.status >= 400:
				log.Warn("request", attrs...)
			default:
				log.Info("request", attrs...)
			}
		})
	}
}

// Recover turns a panic into a 500 so one bad request cannot take the server
// down. The stack is logged; the client is told nothing beyond the generic
// internal-error envelope.
func Recover() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// A dropped connection surfaces as a panic with this sentinel
				// and is not an application fault.
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logging.FromContext(r.Context()).Error("panic recovered",
					"panic", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"stack", string(debug.Stack()))

				defer func() { _ = recover() }() // the response may already be partly written
				httpx.Fail(w, r, httpx.Internal(nil))
			}()
			next.ServeHTTP(w, r)
		})
	}
}
