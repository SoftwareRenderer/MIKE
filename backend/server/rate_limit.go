package server

import (
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
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

// trustProxy reports whether the server is behind a trusted reverse proxy and
// should honor forwarding headers (X-Forwarded-For, X-Forwarded-Proto).
func trustProxy() bool {
	return os.Getenv("MIKE_TRUST_PROXY") == "1"
}

// clientIP extracts the client IP from a request, considering
// X-Forwarded-For headers if present (for reverse proxy setups).
func clientIP(r *http.Request) string {
	// Only trust X-Forwarded-For when explicitly behind a trusted proxy.
	// Otherwise an attacker can spoof the header to bypass IP-based
	// rate limiting (e.g. the login brute-force limit).
	if trustProxy() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// X-Forwarded-For can contain multiple IPs; the first is the original client
			if idx := indexByte(xff, ','); idx >= 0 {
				return xff[:idx]
			}
			return xff
		}
	}
	// Fall back to the direct remote address
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// defaultLoginRateLimit is the login budget per client IP per minute, used when
// MIKE_LOGIN_RATE_LIMIT is unset, unparsable or non-positive.
const defaultLoginRateLimit = 5

// newLoginRateLimiter builds the login rate limiter from MIKE_LOGIN_RATE_LIMIT
// (requests per minute per IP, default 5). It cannot be turned off: 0 and
// unparsable values fall back to the default with a warning, so a typo cannot
// silently drop brute-force protection.
func newLoginRateLimiter() *RateLimiter {
	limit := defaultLoginRateLimit
	if v := os.Getenv("MIKE_LOGIN_RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n <= 0 {
			log.Printf("[server] MIKE_LOGIN_RATE_LIMIT=%q is not a positive integer; using the default of %d requests/minute (rate limiting cannot be disabled)", v, defaultLoginRateLimit)
		} else {
			limit = n
		}
	}
	return NewRateLimiter(limit, time.Minute)
}

// RateLimitMiddleware returns a middleware that applies rate limiting
// to all requests passing through it. Clients exceeding the limit
// receive HTTP 429 Too Many Requests with a Retry-After header.
func RateLimitMiddleware(limiter *RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		allowed, retryAfter := limiter.allow(ip)
		if !allowed {
			w.Header().Set("Retry-After", retryAfter.String())
			JSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}
