package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/litebase/litebase/internal/auth"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}

func TestSecureHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	SecureHeaders(false)(okHandler()).ServeHTTP(rec, req)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP is too permissive: %q", csp)
	}
	// Production CSP must not allow inline or eval'd scripts.
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("production CSP allows unsafe-eval: %q", csp)
	}
	// HSTS must not be asserted over plain HTTP, or it would lock users out.
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS set on a plain HTTP response")
	}
}

func TestSecureHeadersSetsHSTSBehindTLSProxy(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	SecureHeaders(false)(okHandler()).ServeHTTP(rec, req)

	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing on an HTTPS request")
	}
}

func TestBodyLimitRejectsOversizedRequest(t *testing.T) {
	handler := BodyLimit(100)(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 500)))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}

	// A body within the limit passes.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/", strings.NewReader("small"))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestRateLimiterAllowsThenBlocks(t *testing.T) {
	rl := NewRateLimiter(60) // one per second, burst of 60
	defer rl.Close()

	for i := 0; i < 60; i++ {
		if ok, _ := rl.Allow("client"); !ok {
			t.Fatalf("request %d was blocked while within the burst allowance", i+1)
		}
	}
	ok, retry := rl.Allow("client")
	if ok {
		t.Error("request past the burst allowance was permitted")
	}
	if retry <= 0 {
		t.Error("no retry duration reported")
	}

	// A different identity has its own budget.
	if ok, _ := rl.Allow("other"); !ok {
		t.Error("a separate client was blocked by another client's usage")
	}
}

func TestRateLimiterRefills(t *testing.T) {
	rl := NewRateLimiter(6000) // 100 per second
	defer rl.Close()

	// Drain the bucket.
	for i := 0; i < 6000; i++ {
		rl.Allow("c")
	}
	if ok, _ := rl.Allow("c"); ok {
		t.Fatal("bucket was not drained")
	}

	// Tokens accrue continuously, so a short wait restores some capacity.
	time.Sleep(60 * time.Millisecond)
	if ok, _ := rl.Allow("c"); !ok {
		t.Error("bucket did not refill over time")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	rl := NewRateLimiter(1)
	defer rl.Close()

	handler := RateLimit(rl, func(*http.Request) string { return "fixed" })(okHandler())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("second request = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing on a 429")
	}
}

func TestClientIPTrustsPrivatePeersAutomatically(t *testing.T) {
	// Behind Docker, Coolify, Traefik or nginx the peer is always a private
	// address, so the real client must be recovered without configuration.
	privatePeers := []string{
		"172.17.0.1:5000",  // docker bridge
		"10.0.0.5:5000",    // RFC 1918
		"192.168.1.10:443", // RFC 1918
		"127.0.0.1:8080",   // loopback
		"100.64.0.3:80",    // carrier-grade NAT, used by some overlay networks
	}
	for _, peer := range privatePeers {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = peer
		req.Header.Set("X-Forwarded-For", "203.0.113.9")

		if got := ClientIP(req, false); got != "203.0.113.9" {
			t.Errorf("peer %s: ClientIP = %q, want the forwarded client address", peer, got)
		}
	}
}

func TestClientIPIgnoresForwardedHeadersFromPublicPeer(t *testing.T) {
	// A request straight off the internet must never be believed, or anyone
	// could spoof an address and evade per-client rate limiting.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.50:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := ClientIP(req, false); got != "203.0.113.50" {
		t.Errorf("ClientIP = %q, want the real public peer address", got)
	}
	// An explicit trust setting still overrides, for an unusual topology.
	if got := ClientIP(req, true); got != "1.2.3.4" {
		t.Errorf("ClientIP(trusted) = %q, want the forwarded address", got)
	}
}

func TestClientIPIgnoresForwardedHeadersWhenProxyUntrusted(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	// Without a trusted proxy a client could otherwise spoof its address and
	// evade per-IP rate limiting.
	if got := ClientIP(req, false); got != "192.0.2.10" {
		t.Errorf("ClientIP(untrusted) = %q, want the real peer address", got)
	}
	if got := ClientIP(req, true); got != "1.2.3.4" {
		t.Errorf("ClientIP(trusted) = %q, want the forwarded address", got)
	}
}

func TestClientIPTakesLeftmostForwardedEntry(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:80"
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.2, 10.0.0.3")

	if got := ClientIP(req, true); got != "203.0.113.5" {
		t.Errorf("ClientIP = %q, want the original client", got)
	}
}

func TestCSRFBlocksUnsafeMethodWithoutToken(t *testing.T) {
	sess := &auth.Session{CSRFToken: "the-real-token"}
	handler := CSRF()(okHandler())

	// A state-changing request with no token is refused.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", nil)
	req = req.WithContext(WithUser(req.Context(), &auth.User{}, sess))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST without a token = %d, want 403", rec.Code)
	}

	// A wrong token is refused.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/", nil)
	req.Header.Set(CSRFHeaderName, "wrong")
	req = req.WithContext(WithUser(req.Context(), &auth.User{}, sess))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST with a wrong token = %d, want 403", rec.Code)
	}

	// The correct token passes.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/", nil)
	req.Header.Set(CSRFHeaderName, "the-real-token")
	req = req.WithContext(WithUser(req.Context(), &auth.User{}, sess))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("POST with the correct token = %d, want 200", rec.Code)
	}
}

func TestCSRFAllowsSafeMethodsAndAPIKeys(t *testing.T) {
	sess := &auth.Session{CSRFToken: "tok"}
	handler := CSRF()(okHandler())

	// GET carries no state change and needs no token.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(WithUser(req.Context(), &auth.User{}, sess))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET = %d, want 200", rec.Code)
	}

	// An API key is never attached automatically by a browser, so CSRF does
	// not apply to it.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/", nil)
	req = req.WithContext(WithAPIKey(req.Context(), &auth.APIKey{ID: "k"}))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("API key POST = %d, want 200", rec.Code)
	}
}

func TestRequirePermission(t *testing.T) {
	handler := RequirePermission(auth.PermSQLWrite)(okHandler())

	// A viewer lacks sql:write.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(WithUser(req.Context(), &auth.User{Role: auth.RoleViewer}, nil))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(WithUser(req.Context(), &auth.User{Role: auth.RoleOwner}, nil))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("owner = %d, want 200", rec.Code)
	}

	// No user at all is unauthenticated, not merely forbidden.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401", rec.Code)
	}
}

func TestRecoverTurnsPanicIntoInternalError(t *testing.T) {
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	rec := httptest.NewRecorder()
	Recover()(panicking).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	// The panic value must not reach the client.
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("panic detail leaked to the client: %s", rec.Body.String())
	}
}

func TestExtractAPIKey(t *testing.T) {
	cases := map[string]string{
		"Bearer lbk_abc": "lbk_abc",
		"ApiKey lbk_abc": "lbk_abc",
		"Basic dXNlcg==": "",
	}
	for header, want := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", header)
		if got := ExtractAPIKey(req); got != want {
			t.Errorf("ExtractAPIKey(%q) = %q, want %q", header, got, want)
		}
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "lbk_xyz")
	if got := ExtractAPIKey(req); got != "lbk_xyz" {
		t.Errorf("X-API-Key = %q, want lbk_xyz", got)
	}
}

func TestCORSOnlyEchoesAllowedOrigins(t *testing.T) {
	handler := CORS([]string{"https://admin.example.com"})(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("an unlisted origin was allowed")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://admin.example.com")
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://admin.example.com" {
		t.Error("a listed origin was not allowed")
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Error("Vary: Origin missing, so a cache could serve the wrong CORS headers")
	}
}
