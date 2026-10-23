package server

import (
	stdtls "crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/pkg/logging"
)

// refusalLines returns the refusal records this server logged, and ignores everything else the server
// had to say while it was running.
func refusalLines(buf *logging.SafeBuffer) []string {
	var out []string
	for _, line := range buf.Lines() {
		if strings.Contains(line, "component=http") && strings.Contains(line, " status=") {
			out = append(out, line)
		}
	}
	return out
}

// chainDo sends a request through the middleware chain that the running server uses.
func chainDo(s *Server, req *http.Request) *httptest.ResponseRecorder {
	// The chain pins the Host header before anything else, and httptest's default Host is not one it
	// accepts.
	req.Host = "localhost"
	rr := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(rr, req)
	return rr
}

// A stranger guessing at the pairing code the server printed at boot has to show up in the log with the
// address that tried.
func TestStatusLogReportsARefusedPairingCode(t *testing.T) {
	s := testServer(t)
	buf, restore := logging.TestSink()
	defer restore()

	rr := chainDo(s, testRequest(http.MethodPost, "/api/auth/passkey/register/begin",
		map[string]any{"code": "WRONGCODE"}))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong pairing code: got %d, want 401: %s", rr.Code, rr.Body.String())
	}

	lines := refusalLines(buf)
	if len(lines) != 1 {
		t.Fatalf("want one refusal line, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, want := range []string{
		"level=WARN",
		`route="POST /api/auth/passkey/register/begin"`,
		"method=POST",
		"status=401",
		"addr=",
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("refusal line is missing %q:\n%s", want, lines[0])
		}
	}
	// The code is what a guesser is testing, so it is the one thing this line must not repeat.
	if strings.Contains(lines[0], "WRONGCODE") {
		t.Errorf("the refused code was logged: %s", lines[0])
	}
}

func TestStatusLogLevelsFollowTheStatus(t *testing.T) {
	logging.SetLevel("debug")
	defer logging.SetLevel("info")

	s := testServer(t)
	buf, restore := logging.TestSink()
	defer restore()

	tests := []struct {
		name      string
		request   func() *httptest.ResponseRecorder
		wantLevel string
	}{
		{"no session is an attempt", func() *httptest.ResponseRecorder {
			return chainDo(s, testRequest(http.MethodGet, "/api/auth/passkeys", nil))
		}, "level=WARN"},
		{"a login budget spent is an attempt", func() *httptest.ResponseRecorder {
			// The budget under test is one, so this limiter runs on a chain of its own.
			limiter := NewRateLimiter(1, time.Minute)
			defer limiter.Stop()
			h := s.StatusLogMiddleware(RateLimitMiddleware(limiter, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})))
			budget := httptest.NewRecorder()
			h.ServeHTTP(budget, testRequest(http.MethodPost, "/api/auth/passkey/register/begin", map[string]any{}))
			if budget.Code != http.StatusOK {
				t.Fatalf("the first request of a budget of one: got %d", budget.Code)
			}
			refused := httptest.NewRecorder()
			h.ServeHTTP(refused, testRequest(http.MethodPost, "/api/auth/passkey/register/begin", map[string]any{}))
			return refused
		}, "level=WARN"},
		{"a server fault reaches a person", func() *httptest.ResponseRecorder {
			s.mux.HandleFunc("GET /api/status-log-fault", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			})
			cookie, _ := loginTestCSRF(t, s)
			req := testRequest(http.MethodGet, "/api/status-log-fault", nil)
			req.Header.Set("Cookie", cookie)
			return chainDo(s, req)
		}, "level=ERROR"},
		{"a missing row is normal traffic", func() *httptest.ResponseRecorder {
			return chainDo(s, testRequest(http.MethodGet, "/api/preview/deadbeefdeadbeefdeadbeefdeadbeef/asset.css", nil))
		}, "level=DEBUG"},
		{"a bad request is normal traffic", func() *httptest.ResponseRecorder {
			cookie, csrf := loginTestCSRF(t, s)
			req := testRequest(http.MethodPost, "/api/fs/mkdir", map[string]any{})
			req.Header.Set("Cookie", cookie)
			req.Header.Set("X-CSRF-Token", csrf)
			return chainDo(s, req)
		}, "level=DEBUG"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(refusalLines(buf))
			rr := tc.request()
			if rr.Code < http.StatusBadRequest {
				t.Fatalf("the request was meant to be refused: got %d", rr.Code)
			}
			lines := refusalLines(buf)
			if len(lines) != before+1 {
				t.Fatalf("want one new refusal line for %d, got %d:\n%s", rr.Code, len(lines)-before, strings.Join(lines[before:], "\n"))
			}
			if !strings.Contains(lines[before], tc.wantLevel) {
				t.Errorf("status %d logged at the wrong level, want %s:\n%s", rr.Code, tc.wantLevel, lines[before])
			}
			if !strings.Contains(lines[before], fmt.Sprintf("status=%d", rr.Code)) {
				t.Errorf("the line does not name the status it answered with:\n%s", lines[before])
			}
		})
	}
}

func TestStatusLogStaysQuietAboutAnswers(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	buf, restore := logging.TestSink()
	defer restore()

	if rr := chainDo(s, testRequest(http.MethodGet, "/api/health", nil)); rr.Code != http.StatusOK {
		t.Fatalf("health: got %d", rr.Code)
	}

	passkeys := testRequest(http.MethodGet, "/api/auth/passkeys", nil)
	passkeys.Header.Set("Cookie", cookie)
	if rr := chainDo(s, passkeys); rr.Code != http.StatusOK {
		t.Fatalf("signed-in request: got %d", rr.Code)
	}

	revalidated := testRequest(http.MethodGet, "/api/auth/passkeys", nil)
	revalidated.Header.Set("Cookie", cookie)
	revalidated.Header.Set("If-None-Match", "*")
	if rr := chainDo(s, revalidated); rr.Code < http.StatusBadRequest {
		t.Logf("the revalidation answered %d; either answer under 400 is quiet", rr.Code)
	}

	if lines := refusalLines(buf); len(lines) != 0 {
		t.Errorf("accepted requests logged refusals:\n%s", strings.Join(lines, "\n"))
	}
}

// A preview path segment is a live bearer token and a pairing query carries a code, so a refusal names
// the route and never the request.
func TestStatusLogNamesTheRouteAndNotTheRequest(t *testing.T) {
	logging.SetLevel("debug")
	defer logging.SetLevel("info")

	s := testServer(t)
	buf, restore := logging.TestSink()
	defer restore()

	const token = "0123456789abcdef0123456789abcdef"
	chainDo(s, testRequest(http.MethodGet,
		"/api/preview/"+token+"/secrets/.env?code=ABCDEFGH&path=/home/operator/.ssh/id_ed25519", nil))

	lines := refusalLines(buf)
	if len(lines) != 1 {
		t.Fatalf("want one refusal line, got %d:\n%s", len(lines), buf.String())
	}
	got := lines[0]
	for _, absent := range []string{token, "ABCDEFGH", "id_ed25519", ".env", "/api/preview/0"} {
		if strings.Contains(got, absent) {
			t.Errorf("the line names part of the request (%q):\n%s", absent, got)
		}
	}
	if !strings.Contains(got, `route="GET /api/preview/{token}/{path...}"`) {
		t.Errorf("the route pattern is missing:\n%s", got)
	}
}

// A request that matched no route is named by its first path segment, which is all of a path that is
// safe to repeat.
func TestStatusLogNamesAnUnroutedRequestByItsSegment(t *testing.T) {
	s := testServer(t)
	buf, restore := logging.TestSink()
	defer restore()

	chainDo(s, testRequest(http.MethodGet, "/nothing-here/and-a-secret-part", nil))

	lines := refusalLines(buf)
	if len(lines) != 1 {
		t.Fatalf("want one refusal line, got %d:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], `route="unrouted /nothing-here"`) {
		t.Errorf("an unrouted request should name one segment:\n%s", lines[0])
	}
	if strings.Contains(lines[0], "and-a-secret-part") {
		t.Errorf("the rest of the path was logged: %s", lines[0])
	}
}

// A handler that takes the socket must still be able to, or every websocket and terminal upgrade fails
// with nothing but a client-side error to show for it.
func TestStatusLogKeepsTheSocketReachable(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	s.mux.HandleFunc("GET /api/status-log-hijack", func(w http.ResponseWriter, r *http.Request) {
		// compress.go and the websocket library both ask the writer they were handed for these interfaces
		// before they look anywhere else.
		if _, ok := w.(http.Hijacker); !ok {
			t.Error("a handler is handed a writer that cannot take the socket")
		}
		if _, ok := w.(http.Flusher); !ok {
			t.Error("a handler is handed a writer that cannot flush")
		}
		conn, bw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack through the chain: %v", err)
			return
		}
		_, _ = bw.WriteString("HTTP/1.1 101 Switching Protocols\r\nX-Took-The-Socket: yes\r\n\r\nhijacked\r\n")
		_ = bw.Flush()
		_ = conn.Close()
	})

	// The chain redirects a plain request before it reaches this route, so this connection is TLS.
	upstream := httptest.NewTLSServer(s.middlewareChain(s.mux))
	defer upstream.Close()

	conn, err := stdtls.Dial("tcp", strings.TrimPrefix(upstream.URL, "https://"), &stdtls.Config{
		InsecureSkipVerify: true, // This certificate is the test server's own throwaway one.
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	// CompressMiddleware leaves an upgrade request unbuffered because the handler is about to take the
	// socket.
	request := "GET /api/status-log-hijack HTTP/1.1\r\n" +
		"Host: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Cookie: " + cookie + "\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply := make([]byte, 256)
	n, err := conn.Read(reply)
	if err != nil && n == 0 {
		t.Fatalf("read: %v", err)
	}
	if got := string(reply[:n]); !strings.Contains(got, "X-Took-The-Socket: yes") || !strings.Contains(got, "hijacked") {
		t.Errorf("the handler never got the socket:\n%s", got)
	}
}
