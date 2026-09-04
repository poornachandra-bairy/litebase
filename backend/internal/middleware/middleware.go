// Package middleware provides the cross-cutting HTTP layers: logging,
// panic recovery, security headers, body limits, CORS, rate limiting,
// authentication and CSRF protection.
package middleware

import (
	"net"
	"net/http"
	"strings"
)

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so that the first listed runs outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// statusWriter records the status and byte count for request logging, and
// tracks whether the handler wrote anything at all.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.status = code
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

// Flush forwards to the underlying writer when it supports streaming, which
// query result streaming relies on.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the original writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ClientIP determines the caller's address.
//
// Forwarding headers are only consulted when the operator has declared that the
// server sits behind a trusted proxy; otherwise any client could spoof its
// address and defeat per-IP rate limiting.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// The left-most entry is the original client; later entries are
			// proxies that appended themselves.
			if comma := strings.IndexByte(xff, ','); comma > 0 {
				xff = xff[:comma]
			}
			if ip := strings.TrimSpace(xff); ip != "" {
				return ip
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
