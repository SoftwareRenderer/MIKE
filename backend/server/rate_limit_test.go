package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"mike/backend/startup"
)

// TestRateLimitMiddleware_BlocksOverLimit is one window of the budget: every request up to the limit
// reaches the handler, and the one past it is refused with the header that says when to come back. The
// passing requests are the control — a limiter that refused its first request would fail here, not only
// in the 429 rows.
func TestRateLimitMiddleware_BlocksOverLimit(t *testing.T) {
	const limit = 3
	limiter := NewRateLimiter(limit, 10*time.Second)
	defer limiter.Stop()

	handler := RateLimitMiddleware(limiter, trustProxyForTest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/begin", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	for i := range limit {
		if rr := post(); rr.Code != http.StatusOK {
			t.Fatalf("request %d of %d: expected 200, got %d", i+1, limit, rr.Code)
		}
	}

	rr := post()
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d: expected 429, got %d", limit+1, rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on rate limited response")
	}
}

func TestRateLimitMiddleware_IPBased(t *testing.T) {
	// Each IP gets its own bucket
	limiter := NewRateLimiter(2, 50*time.Millisecond)
	defer limiter.Stop()

	handler := RateLimitMiddleware(limiter, trustProxyForTest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// IP 1 makes 2 requests
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/begin", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("IP1 request %d: expected 200, got %d", i, rr.Code)
		}
	}

	// IP 1's 3rd request should be blocked
	req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/begin", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("IP1 request 3: expected 429, got %d", rr.Code)
	}

	// IP 2 should still be allowed (different bucket)
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/begin", nil)
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
	defer limiter.Stop()

	handler := RateLimitMiddleware(limiter, trustProxyForTest, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request should succeed
	req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/begin", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rr.Code)
	}

	// Second request should be blocked
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/begin", nil)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", rr2.Code)
	}

	// Wait for window to expire
	time.Sleep(30 * time.Millisecond)

	// Third request should succeed again
	req3 := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/begin", nil)
	rr3 := httptest.NewRecorder()
	handler.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("third request after window: expected 200, got %d", rr3.Code)
	}
}

// TestTheCeremonyRouteIsRateLimited: the pairing code is the one secret here a person types, so the route
// that consumes it is the one a script brute-forces, and that route is rate limited — whatever budget the
// operator configured, including one that cannot be read as a budget at all.
func TestTheCeremonyRouteIsRateLimited(t *testing.T) {
	for _, budget := range []int{5, 0} {
		name := "the configured budget"
		if budget == 0 {
			name = "a budget that cannot disable the limiter"
		}
		t.Run(name, func(t *testing.T) {
			// A tight budget for this test alone: the suite runs with an effectively unlimited one so
			// that unrelated tests can pair devices repeatedly.
			s := testServerWith(t, func(c *startup.Config) { c.LoginRateLimit = budget })

			rateLimited := 0
			for i := 0; i < 12; i++ {
				req := testRequest(http.MethodPost, "/api/auth/passkey/register/begin", map[string]string{
					"code": "NOSUCHCODE",
				})
				if rr := testResponse(t, s, req); rr.Code == http.StatusTooManyRequests {
					rateLimited++
				}
			}

			if rateLimited == 0 {
				t.Errorf("12 rapid attempts at the ceremony route were never rate limited (budget %d)", budget)
			}
		})
	}
}

// trustProxyForTest is the resolved MIKE_TRUST_PROXY decision the middleware tests run under when the
// case is about the ordinary (unproxied) deployment. A proxy case passes true at the call.
const trustProxyForTest = false

// TestClientIP is the address the login budget is keyed under, for every way an address can be written to
// this server. One client must not buy two budgets, and neither may a caller that writes its own header.
func TestClientIP(t *testing.T) {
	const peer = "10.0.0.1:54321"

	cases := []struct {
		name       string
		remoteAddr string
		forwarded  string
		trustProxy bool
		want       string
	}{
		{
			// By default the client-supplied X-Forwarded-For header must NOT be trusted, otherwise an
			// attacker can spoof it to bypass IP-based rate limiting.
			name:       "a spoofed X-Forwarded-For is not evidence",
			remoteAddr: "203.0.113.9:54321",
			forwarded:  "1.2.3.4",
			trustProxy: trustProxyForTest,
			want:       "203.0.113.9",
		},
		{
			name:       "no header at all falls back to the peer",
			remoteAddr: "192.0.2.55:4444",
			trustProxy: trustProxyForTest,
			want:       "192.0.2.55",
		},
		{
			// A proxy appends, so everything left of the last entry is what the client wrote. Keying the
			// limit on the first entry let a caller buy a fresh login budget per attempt.
			name:       "the address the trusted proxy appended",
			remoteAddr: peer,
			forwarded:  "1.2.3.4, 198.51.100.7",
			trustProxy: true,
			want:       "198.51.100.7",
		},
		{
			// Scanning left for a parseable entry would be the bypass again.
			name:       "a chain that does not end in an address uses the peer",
			remoteAddr: peer,
			forwarded:  "198.51.100.7, evil",
			trustProxy: true,
			want:       "10.0.0.1",
		},
		{name: "an IPv6 chain is canonicalised", remoteAddr: peer, forwarded: "1.2.3.4, FE80::1", trustProxy: true, want: "fe80::1"},
		{name: "leading zeroes are the same client", remoteAddr: peer, forwarded: "1.2.3.4, fe80:0:0:0:0:0:0:1", trustProxy: true, want: "fe80::1"},
		{name: "padded whitespace is the same client", remoteAddr: peer, forwarded: "1.2.3.4,  fe80::1 ", trustProxy: true, want: "fe80::1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/begin", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := clientIP(req, tc.trustProxy); got != tc.want {
				t.Errorf("clientIP: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRateLimitMiddleware_XFFCannotBypassTheLoginBudget is the brute-force shape on both sides of the
// proxy decision: the part of X-Forwarded-For the caller writes rotates per attempt, and every attempt
// still shares one budget — whether the header is believed at all (proxy trust off) or only for the entry
// the proxy appended (trust on).
func TestRateLimitMiddleware_XFFCannotBypassTheLoginBudget(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		forwarded  func(attempt int) string
		trustProxy bool
	}{
		{
			name:       "the header is not believed",
			remoteAddr: "203.0.113.9:54321",
			forwarded:  func(i int) string { return "1.2.3." + strconv.Itoa(i) },
			trustProxy: false,
		},
		{
			name:       "only the proxy's own entry is",
			remoteAddr: "10.0.0.1:54321",
			forwarded:  func(i int) string { return "1.2.3." + strconv.Itoa(i) + ", 203.0.113.9" },
			trustProxy: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limiter := NewRateLimiter(2, time.Minute)
			defer limiter.Stop()
			handler := RateLimitMiddleware(limiter, tc.trustProxy, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			limited := false
			for i := 0; i < 5; i++ {
				req := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/begin", nil)
				req.RemoteAddr = tc.remoteAddr
				req.Header.Set("X-Forwarded-For", tc.forwarded(i))
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				if rr.Code == http.StatusTooManyRequests {
					limited = true
					break
				}
			}
			if !limited {
				t.Error("rotating the client-written part of X-Forwarded-For bought a login past its limit")
			}
		})
	}
}

// newLoginRateLimiter cannot be turned off by a caller that hands it a non-positive budget. startup
// refuses such a value at resolution (startup.TestLoginRateLimitCannotBeDisabled); the constructor
// falling back too is what keeps a hand-built limiter from being an unlimited one — and a real budget is
// honoured rather than rounded to the default.
func TestNewLoginRateLimiterBudget(t *testing.T) {
	cases := []struct {
		budget int
		want   int
	}{
		{budget: 0, want: defaultLoginRateLimit},
		{budget: -1, want: defaultLoginRateLimit},
		{budget: -100, want: defaultLoginRateLimit},
		{budget: 17, want: 17},
	}
	for _, tc := range cases {
		rl := newLoginRateLimiter(tc.budget)
		if rl == nil {
			t.Fatalf("newLoginRateLimiter(%d) returned nil; login rate limiting was disabled", tc.budget)
		}
		rl.Stop()
		if rl.limit != tc.want {
			t.Errorf("newLoginRateLimiter(%d).limit = %d; want %d", tc.budget, rl.limit, tc.want)
		}
		if rl.window != time.Minute {
			t.Errorf("newLoginRateLimiter(%d).window = %v; want 1m", tc.budget, rl.window)
		}
	}
}
