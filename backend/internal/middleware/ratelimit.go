package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/litebase/litebase/internal/httpx"
)

// bucket is a token bucket refilled continuously rather than in fixed windows,
// so a caller cannot burst twice at a window boundary.
type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// RateLimiter enforces per-identity request quotas in memory.
//
// State is per process, which suits a single-binary deployment. Behind several
// replicas each would enforce the limit independently; that is a deliberate
// trade for not requiring an external store.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	rate     float64 // tokens per second
	capacity float64

	stop chan struct{}
	once sync.Once
}

// NewRateLimiter builds a limiter allowing perMinute requests per identity,
// with a burst allowance of one minute's worth.
func NewRateLimiter(perMinute int) *RateLimiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	rl := &RateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     float64(perMinute) / 60.0,
		capacity: float64(perMinute),
		stop:     make(chan struct{}),
	}
	go rl.gc()
	return rl
}

// Allow reports whether the identity may make a request now, and how long to
// wait if not.
func (rl *RateLimiter) Allow(identity string) (bool, time.Duration) {
	return rl.AllowN(identity, rl.capacity, 1)
}

// AllowN applies an identity-specific capacity, letting an individual API key
// override the global default.
func (rl *RateLimiter) AllowN(identity string, capacity, cost float64) (bool, time.Duration) {
	if capacity <= 0 {
		capacity = rl.capacity
	}
	rate := capacity / 60.0

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[identity]
	if !ok {
		b = &bucket{tokens: capacity, lastSeen: now}
		rl.buckets[identity] = b
	} else {
		elapsed := now.Sub(b.lastSeen).Seconds()
		b.tokens = min(capacity, b.tokens+elapsed*rate)
		b.lastSeen = now
	}

	if b.tokens >= cost {
		b.tokens -= cost
		return true, 0
	}
	// Report when the bucket will hold enough tokens again, so the caller can
	// send a meaningful Retry-After.
	deficit := cost - b.tokens
	return false, time.Duration(deficit/rate*float64(time.Second)) + time.Second
}

// gc discards buckets that have been idle long enough to have fully refilled,
// which bounds memory under a churn of distinct client addresses.
func (rl *RateLimiter) gc() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case now := <-ticker.C:
			rl.mu.Lock()
			for k, b := range rl.buckets {
				if now.Sub(b.lastSeen) > 10*time.Minute {
					delete(rl.buckets, k)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// Close stops the background collector.
func (rl *RateLimiter) Close() { rl.once.Do(func() { close(rl.stop) }) }

// IdentityFunc derives the rate-limit key for a request.
type IdentityFunc func(*http.Request) string

// RateLimit rejects requests from an identity that has exhausted its quota.
func RateLimit(rl *RateLimiter, identity IdentityFunc) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retryAfter := rl.Allow(identity(r))
			if !ok {
				w.Header().Set("Retry-After", formatSeconds(retryAfter))
				httpx.Fail(w, r, httpx.RateLimited(
					"too many requests; please slow down"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IPIdentity keys the limiter on the client address.
func IPIdentity(trustProxy bool) IdentityFunc {
	return func(r *http.Request) string { return ClientIP(r, trustProxy) }
}

func formatSeconds(d time.Duration) string {
	secs := int(d.Seconds() + 0.999)
	if secs < 1 {
		secs = 1
	}
	return itoa(secs)
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
