package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestRateLimitMiddleware_AllowsUnderLimit(t *testing.T) {
	// Create a rate limiter that allows 3 requests per 100ms
	limiter := NewRateLimiter(3, 100*time.Millisecond)

	count := 0
	handler := RateLimitMiddleware(limiter, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	handler := RateLimitMiddleware(limiter, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	handler := RateLimitMiddleware(limiter, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	handler := RateLimitMiddleware(limiter, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	// Enable rate limiting for this specific test (tests normally disable it)
	oldVal := os.Getenv("MIKE_LOGIN_RATE_LIMIT")
	os.Setenv("MIKE_LOGIN_RATE_LIMIT", "5")
	defer func() {
		if oldVal != "" {
			os.Setenv("MIKE_LOGIN_RATE_LIMIT", oldVal)
		} else {
			os.Unsetenv("MIKE_LOGIN_RATE_LIMIT")
		}
	}()

	s := testServer(t)

	// The login endpoint should be rate limited.
	// Make several rapid requests and verify at least one gets 429.
	successCount := 0
	rateLimitedCount := 0

	for i := 0; i < 10; i++ {
		req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
			"password": "wrong-password",
		})
		rr := testResponseRateLimited(t, s, req)
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

func testResponseRateLimited(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler := s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
	handler.ServeHTTP(rr, req)
	return rr
}

func setTrustProxy(t *testing.T, enabled bool) {
	t.Helper()
	old := os.Getenv("MIKE_TRUST_PROXY")
	if enabled {
		os.Setenv("MIKE_TRUST_PROXY", "1")
	} else {
		os.Unsetenv("MIKE_TRUST_PROXY")
	}
	t.Cleanup(func() {
		if old != "" {
			os.Setenv("MIKE_TRUST_PROXY", old)
		} else {
			os.Unsetenv("MIKE_TRUST_PROXY")
		}
	})
}

func TestClientIP_IgnoresXFFByDefault(t *testing.T) {
	// By default the client-supplied X-Forwarded-For header must NOT be trusted,
	// otherwise an attacker can spoof it to bypass IP-based rate limiting.
	setTrustProxy(t, false)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "203.0.113.9:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(req); got != "203.0.113.9" {
		t.Fatalf("clientIP with spoofed XFF (proxy disabled): got %q, want %q", got, "203.0.113.9")
	}
}

func TestClientIP_UsesTheAddressTheTrustedProxyAppended(t *testing.T) {
	// A proxy appends, so everything left of the last entry is what the client wrote. Keying the
	// limit on the first entry let a caller buy a fresh login budget per attempt.
	setTrustProxy(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.7")
	if got := clientIP(req); got != "198.51.100.7" {
		t.Fatalf("clientIP with XFF (proxy enabled): got %q, want %q", got, "198.51.100.7")
	}
}

func TestClientIP_CanonicalisesTheForwardedAddress(t *testing.T) {
	// One client must not buy two budgets by spelling its address differently.
	setTrustProxy(t, true)
	for _, written := range []string{"FE80::1", "fe80:0:0:0:0:0:0:1", " fe80::1 "} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		req.Header.Set("X-Forwarded-For", "1.2.3.4, "+written)
		if got := clientIP(req); got != "fe80::1" {
			t.Errorf("clientIP with %q: got %q, want the canonical %q", written, got, "fe80::1")
		}
	}
}

func TestClientIP_ChainThatDoesNotEndInAnAddressUsesThePeer(t *testing.T) {
	// Scanning left for a parseable entry would be the bypass again.
	setTrustProxy(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "198.51.100.7, evil")
	if got := clientIP(req); got != "10.0.0.1" {
		t.Fatalf("clientIP with a non-address last entry: got %q, want the peer %q", got, "10.0.0.1")
	}
}

func TestRateLimitMiddleware_XFFCannotBypassATrustedProxyLimit(t *testing.T) {
	// The brute-force shape behind a proxy: rotate the entry the client writes, keep the one the
	// proxy appends. All attempts share one budget.
	setTrustProxy(t, true)
	limiter := NewRateLimiter(2, time.Minute)
	defer limiter.Stop()
	handler := RateLimitMiddleware(limiter, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if got := clientIP(req); got != "192.0.2.55" {
		t.Fatalf("clientIP no XFF: got %q, want %q", got, "192.0.2.55")
	}
}

func TestRateLimitMiddleware_XFFCannotBypassByDefault(t *testing.T) {
	// A client rotating X-Forwarded-For must not be able to bypass the limit
	// when proxy trust is disabled.
	setTrustProxy(t, false)
	limiter := NewRateLimiter(2, time.Minute)
	handler := RateLimitMiddleware(limiter, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// setLoginRateLimit pins MIKE_LOGIN_RATE_LIMIT for one test ("" = unset).
func setLoginRateLimit(t *testing.T, value string) {
	t.Helper()
	old, had := os.LookupEnv("MIKE_LOGIN_RATE_LIMIT")
	if value == "" {
		os.Unsetenv("MIKE_LOGIN_RATE_LIMIT")
	} else {
		os.Setenv("MIKE_LOGIN_RATE_LIMIT", value)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("MIKE_LOGIN_RATE_LIMIT", old)
		} else {
			os.Unsetenv("MIKE_LOGIN_RATE_LIMIT")
		}
	})
}

// TestNewLoginRateLimiterCannotBeDisabled: 0 and unparsable values used to
// return a nil limiter, silently switching brute-force protection off. They now
// fall back to the documented default.
func TestNewLoginRateLimiterCannotBeDisabled(t *testing.T) {
	for _, v := range []string{"", "0", "-1", "abc", "1.5", "  "} {
		v := v
		t.Run("MIKE_LOGIN_RATE_LIMIT="+v, func(t *testing.T) {
			setLoginRateLimit(t, v)
			rl := newLoginRateLimiter()
			if rl == nil {
				t.Fatal("newLoginRateLimiter returned nil; login rate limiting was disabled")
			}
			defer rl.Stop()
			if rl.limit != defaultLoginRateLimit {
				t.Errorf("limit = %d; want the default %d", rl.limit, defaultLoginRateLimit)
			}
			if rl.window != time.Minute {
				t.Errorf("window = %v; want 1m", rl.window)
			}
		})
	}
}

func TestNewLoginRateLimiterHonoursValidValue(t *testing.T) {
	setLoginRateLimit(t, "17")
	rl := newLoginRateLimiter()
	defer rl.Stop()
	if rl.limit != 17 {
		t.Errorf("limit = %d; want 17", rl.limit)
	}
}

// TestLoginRouteRateLimitedWithZeroEnv is the end-to-end regression for the
// "0 disables it" behaviour: the login route must still answer 429.
func TestLoginRouteRateLimitedWithZeroEnv(t *testing.T) {
	setLoginRateLimit(t, "0")
	s := testServer(t)

	limited := false
	for i := 0; i < 12; i++ {
		req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
			"password": "wrong-password",
		})
		rr := testResponseRateLimited(t, s, req)
		if rr.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("12 rapid login attempts were never rate limited with MIKE_LOGIN_RATE_LIMIT=0")
	}
}
