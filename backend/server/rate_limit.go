package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// clientBucket tracks the request count for a single client IP within
// a sliding window.
type clientBucket struct {
	count     int       // number of requests in the current window
	expiresAt time.Time // when the current window expires
}

// RateLimiter implements a sliding-window rate limiter keyed by client IP.
// It uses a token-bucket approach: each client gets `limit` requests per
// `window` duration. Requests beyond the limit receive HTTP 429.
type RateLimiter struct {
	limit  int           // max requests per window
	window time.Duration // window duration

	mu      sync.Mutex
	buckets map[string]*clientBucket
	stopCh  chan struct{}
}

// NewRateLimiter creates a new IP-based rate limiter.
// limit is the maximum number of requests allowed per window per IP.
// window is the duration of the sliding window.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]*clientBucket),
		stopCh:  make(chan struct{}),
	}
	// Clean up stale buckets periodically to prevent unbounded memory growth.
	go rl.cleanupLoop()
	return rl
}

// allow checks if a request from the given IP is allowed.
// Returns (allowed, retryAfter) where retryAfter is the duration until
// the next request will be allowed.
func (rl *RateLimiter) allow(ip string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	bucket, exists := rl.buckets[ip]

	if !exists || now.After(bucket.expiresAt) {
		// Start a new window for this client
		rl.buckets[ip] = &clientBucket{
			count:     1,
			expiresAt: now.Add(rl.window),
		}
		return true, 0
	}

	if bucket.count >= rl.limit {
		// Rate limited — calculate retry-after time
		retryAfter := time.Until(bucket.expiresAt)
		return false, retryAfter
	}

	bucket.count++
	return true, 0
}

// cleanupLoop periodically removes expired buckets to prevent memory leaks.
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.window * 2)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for ip, bucket := range rl.buckets {
				if now.After(bucket.expiresAt) {
					delete(rl.buckets, ip)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopCh:
			return
		}
	}
}

// Stop shuts down the cleanup goroutine. Should be called during server shutdown.
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

// trustProxyIsSet is the name of the resolved decision, kept as a parameter rather than a lookup:
// the answer decides whether a client-supplied header is believed, so it must be the same for the
// rate limiter's key and for the cookie's Secure flag, and it must not change between two requests.

// clientIP is the address the login rate limiter budgets a request under. Behind a trusted proxy
// (trustProxy, the resolved MIKE_TRUST_PROXY) that is the rightmost X-Forwarded-For entry — the only one the proxy wrote. Everything to its
// left is client-chosen, so a key taken from there can be changed per attempt; a chain not ending
// in an address falls back to the peer for the same reason.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if last := lastForwardedIP(r.Header.Get("X-Forwarded-For")); last != "" {
			return last
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// lastForwardedIP is the rightmost X-Forwarded-For entry, or "" when the header does not end in an
// IP address. Canonical form: "FE80::1" and "fe80::1" are one client.
func lastForwardedIP(xff string) string {
	entries := strings.Split(xff, ",")
	ip := net.ParseIP(strings.TrimSpace(entries[len(entries)-1]))
	if ip == nil {
		return ""
	}
	return ip.String()
}

// defaultLoginRateLimit is the login budget per client IP per minute, used when
// MIKE_LOGIN_RATE_LIMIT is unset, unparsable or non-positive.
const defaultLoginRateLimit = 5

// newLoginRateLimiter builds the login rate limiter from the resolved budget (requests per minute
// per IP). The budget is always positive — startup refuses 0, negative and unparsable values by
// falling back to the default with a warning — because the protection must survive a typo.
func newLoginRateLimiter(limit int) *RateLimiter {
	if limit <= 0 {
		limit = defaultLoginRateLimit
	}
	return NewRateLimiter(limit, time.Minute)
}

// RateLimitMiddleware returns a middleware that applies rate limiting
// to all requests passing through it. Clients exceeding the limit
// receive HTTP 429 Too Many Requests with a Retry-After header.
func RateLimitMiddleware(limiter *RateLimiter, trustProxy bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, trustProxy)
		allowed, retryAfter := limiter.allow(ip)
		if !allowed {
			w.Header().Set("Retry-After", retryAfter.String())
			JSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}
