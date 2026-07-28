package server

import (
	"net/http"
	"net/http/httptest"
	"os"
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
