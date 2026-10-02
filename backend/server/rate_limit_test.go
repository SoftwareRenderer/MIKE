package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"mike/backend/startup"
)

func TestRateLimitMiddleware_AllowsUnderLimit(t *testing.T) {
	// Create a rate limiter that allows 3 requests per 100ms
	limiter := NewRateLimiter(3, 100*time.Millisecond)

	count := 0
	handler := RateLimitMiddleware(limiter, trustProxyForTest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rr.Code)
		}
	}
}

func TestRateLimitMiddleware_BlocksOverLimit(t *testing.T) {
	// Create a rate limiter that allows only 1 request per 50ms
	limiter := NewRateLimiter(1, 50*time.Millisecond)

	handler := RateLimitMiddleware(limiter, trustProxyForTest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request should succeed
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rr.Code)
	}

	// Second request should be rate limited
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", rr2.Code)
	}

	// Verify Retry-After header is set
	if rr2.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on rate limited response")
	}
}

func TestRateLimitMiddleware_IPBased(t *testing.T) {
	// Each IP gets its own bucket
	limiter := NewRateLimiter(2, 50*time.Millisecond)

	handler := RateLimitMiddleware(limiter, trustProxyForTest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// IP 1 makes 2 requests
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("IP1 request %d: expected 200, got %d", i, rr.Code)
		}
	}

	// IP 1's 3rd request should be blocked
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("IP1 request 3: expected 429, got %d", rr.Code)
	}

	// IP 2 should still be allowed (different bucket)
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req2.RemoteAddr = "192.168.1.2:12345"
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("IP2 request: expected 200, got %d", rr2.Code)
	}
}

func TestRateLimitMiddleware_WaitsAfterWindow(t *testing.T) {
	// Create a rate limiter that allows 1 request per 20ms
	limiter := NewRateLimiter(1, 20*time.Millisecond)

	handler := RateLimitMiddleware(limiter, trustProxyForTest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request should succeed
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rr.Code)
	}

	// Second request should be blocked
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", rr.Code)
	}

	// Wait for window to expire
	time.Sleep(30 * time.Millisecond)

	// Third request should succeed again
	req3 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("third request after window: expected 200, got %d", rr3.Code)
	}
}

func TestRateLimitMiddleware_LoginEndpoint(t *testing.T) {
	// A tight budget for this test alone: the suite runs with an effectively unlimited one so that
	// unrelated tests can log in repeatedly.
	s := testServerWith(t, func(c *startup.Config) { c.LoginRateLimit = 5 })

	// The login endpoint should be rate limited.
	// Make several rapid requests and verify at least one gets 429.
	successCount := 0
	rateLimitedCount := 0

	for i := 0; i < 10; i++ {
		req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
			"password": "wrong-password",
		})
		rr := testResponse(t, s, req)
		switch rr.Code {
		case http.StatusOK, http.StatusUnauthorized:
			successCount++
		case http.StatusTooManyRequests:
			rateLimitedCount++
		}
	}

	// At least one request should be rate limited after 5 attempts
	if rateLimitedCount == 0 {
		t.Errorf("expected at least 1 rate limited request, got 0 (success=%d, limited=%d)", successCount, rateLimitedCount)
	}
}

// trustProxyForTest is the resolved MIKE_TRUST_PROXY decision the middleware tests run under when the
// case is about the ordinary (unproxied) deployment. A proxy case passes true at the call.
const trustProxyForTest = false

func TestClientIP_IgnoresXFFByDefault(t *testing.T) {
	// By default the client-supplied X-Forwarded-For header must NOT be trusted,
	// otherwise an attacker can spoof it to bypass IP-based rate limiting.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "203.0.113.9:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(req, false); got != "203.0.113.9" {
		t.Fatalf("clientIP with spoofed XFF (proxy disabled): got %q, want %q", got, "203.0.113.9")
	}
}

func TestClientIP_UsesTheAddressTheTrustedProxyAppended(t *testing.T) {
	// A proxy appends, so everything left of the last entry is what the client wrote. Keying the
	// limit on the first entry let a caller buy a fresh login budget per attempt.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.7")
	if got := clientIP(req, true); got != "198.51.100.7" {
		t.Fatalf("clientIP with XFF (proxy enabled): got %q, want %q", got, "198.51.100.7")
	}
}

func TestClientIP_CanonicalisesTheForwardedAddress(t *testing.T) {
	// One client must not buy two budgets by spelling its address differently.
	for _, written := range []string{"FE80::1", "fe80:0:0:0:0:0:0:1", " fe80::1 "} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		req.Header.Set("X-Forwarded-For", "1.2.3.4, "+written)
		if got := clientIP(req, true); got != "fe80::1" {
			t.Errorf("clientIP with %q: got %q, want the canonical %q", written, got, "fe80::1")
		}
	}
}

func TestClientIP_ChainThatDoesNotEndInAnAddressUsesThePeer(t *testing.T) {
	// Scanning left for a parseable entry would be the bypass again.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "198.51.100.7, evil")
	if got := clientIP(req, true); got != "10.0.0.1" {
		t.Fatalf("clientIP with a non-address last entry: got %q, want the peer %q", got, "10.0.0.1")
	}
}

func TestRateLimitMiddleware_XFFCannotBypassATrustedProxyLimit(t *testing.T) {
	// The brute-force shape behind a proxy: rotate the entry the client writes, keep the one the
	// proxy appends. All attempts share one budget.
	limiter := NewRateLimiter(2, time.Minute)
	defer limiter.Stop()
	handler := RateLimitMiddleware(limiter, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	limited := false
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		req.Header.Set("X-Forwarded-For", "1.2.3."+strconv.Itoa(i)+", 203.0.113.9")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("rotating the client-written part of X-Forwarded-For kept a proxy-fronted login past its limit")
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "192.0.2.55:4444"
	if got := clientIP(req, trustProxyForTest); got != "192.0.2.55" {
		t.Fatalf("clientIP no XFF: got %q, want %q", got, "192.0.2.55")
	}
}

func TestRateLimitMiddleware_XFFCannotBypassByDefault(t *testing.T) {
	// A client rotating X-Forwarded-For must not be able to bypass the limit
	// when proxy trust is disabled.
	limiter := NewRateLimiter(2, time.Minute)
	handler := RateLimitMiddleware(limiter, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	limited := false
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = "203.0.113.9:54321"
		// Rotate the spoofed header each attempt.
		req.Header.Set("X-Forwarded-For", "1.2.3."+strconv.Itoa(i))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("expected XFF spoofing to be blocked by default (should hit rate limit)")
	}
}

// newLoginRateLimiter cannot be turned off by a caller that hands it a non-positive budget. startup
// refuses such a value at resolution (startup.TestLoginRateLimitCannotBeDisabled); the constructor
// falling back too is what keeps a hand-built limiter from being an unlimited one.
func TestNewLoginRateLimiterCannotBeDisabled(t *testing.T) {
	for _, budget := range []int{0, -1, -100} {
		rl := newLoginRateLimiter(budget)
		if rl == nil {
			t.Fatalf("newLoginRateLimiter(%d) returned nil; login rate limiting was disabled", budget)
		}
		rl.Stop()
		if rl.limit != defaultLoginRateLimit {
			t.Errorf("limit = %d; want the default %d", rl.limit, defaultLoginRateLimit)
		}
		if rl.window != time.Minute {
			t.Errorf("window = %v; want 1m", rl.window)
		}
	}
}

func TestNewLoginRateLimiterHonoursValidValue(t *testing.T) {
	rl := newLoginRateLimiter(17)
	defer rl.Stop()
	if rl.limit != 17 {
		t.Errorf("limit = %d; want 17", rl.limit)
	}
}

// TestLoginRouteRateLimitedWithZeroBudget: a zero budget does not disable the limiter — the login
// route must still answer 429.
func TestLoginRouteRateLimitedWithZeroBudget(t *testing.T) {
	s := testServerWith(t, func(c *startup.Config) { c.LoginRateLimit = 0 })

	limited := false
	for i := 0; i < 12; i++ {
		req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
			"password": "wrong-password",
		})
		rr := testResponse(t, s, req)
		if rr.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("12 rapid login attempts were never rate limited with MIKE_LOGIN_RATE_LIMIT=0")
	}
}
