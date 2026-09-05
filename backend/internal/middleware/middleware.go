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
// Forwarding headers are honoured when the operator has declared a trusted
// proxy, and also when the immediate peer is on a private or loopback address.
// That second case is what makes a containerised deployment correct out of the
// box: behind Docker, Coolify, Traefik or nginx the peer is always a private
// address, so the real client address is recovered without configuration.
//
// A request arriving directly from a public address is never trusted, because
// there anyone could set X-Forwarded-For and evade per-client rate limiting.
func ClientIP(r *http.Request, trustProxy bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	if trustProxy || isPrivatePeer(host) {
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
	return host
}

// isPrivatePeer reports whether an address belongs to a network that cannot be
// reached directly from the internet, which means anything connecting from it
// is a proxy on the same host or private network rather than an arbitrary
// client.
func isPrivatePeer(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		// A unix socket or an unparseable address is local by definition.
		return host == "" || strings.HasPrefix(host, "/") || host == "@"
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() || // RFC 1918 and IPv6 unique-local
		ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() ||
		isCarrierGradeNAT(ip)
}

// carrierGradeNAT is 100.64.0.0/10, used by some container and overlay networks
// and never routable from the public internet.
var carrierGradeNAT = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

func isCarrierGradeNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && carrierGradeNAT.Contains(v4)
}
