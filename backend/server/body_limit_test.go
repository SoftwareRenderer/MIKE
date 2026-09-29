package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/httputil"
)

// TestMaxBodyMiddlewareBoundsEveryReader covers the handlers that decode straight from
// r.Body instead of through httputil.DecodeJSON — git checkout, fs write/mkdir/
// init-git, worktree reset — which would otherwise read a body of any size into
// memory for anyone holding a session.
func TestMaxBodyMiddlewareBoundsEveryReader(t *testing.T) {
	t.Parallel()
	const limit = 1024
	readAll := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			httputil.JSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	h := MaxBodyMiddleware(limit)(readAll)

	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/anything", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	if rr := post(`{"small":true}`); rr.Code != http.StatusOK {
		t.Errorf("a small body was refused: %d %s", rr.Code, rr.Body.String())
	}
	// Reading past the cap must fail the read, not hand over a truncated body: a
	// handler decoding a cut-off document would act on half a request.
	if rr := post(strings.Repeat("x", limit*8)); rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: got %d, want %d (%s)", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
	// A GET has no body to bound — and the WebSocket endpoints ARE GET upgrades, so
	// bounding them would break the stream.
	getReq := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
	getRR := httptest.NewRecorder()
	h.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Errorf("a GET was refused: %d %s", getRR.Code, getRR.Body.String())
	}
}

// TestServerRefusesOversizedBody is the same rule through the middleware chain the
// server actually runs, on a route that decodes its body itself, at the real limit.
//
// The body is one the handler would happily act on — a valid request with an extra
// field padded past the cap, and an unknown JSON field is ignored — so the only thing
// that can stop it is the bound on the reader. A test whose body is garbage proves
// nothing: it would be refused with the cap wired up or not.
func TestServerRefusesOversizedBody(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	// A real mkdir, aimed at a directory the handler would create if it ever saw the
	// document. Its absence afterwards is the second half of the assertion.
	made := filepath.Join(t.TempDir(), "made")
	body := `{"path":"` + made + `","pad":"` + strings.Repeat("y", int(httputil.MaxRequestBodyBytes)) + `"}`

	rr := postThroughChain(t, s, "/api/fs/mkdir", strings.NewReader(body), cookie, csrf)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("declared length over the cap: got %d, want %d: %s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
	if _, err := os.Stat(made); err == nil {
		t.Errorf("the handler acted on an oversized body: %s exists", made)
	}

	// The other sender: no declared length at all (chunked), so nothing to refuse up
	// front and the bound has to come from the reader. The body is still one the
	// handler would otherwise act on.
	chunked := httptest.NewRequest(http.MethodPost, "/api/fs/mkdir", io.MultiReader(
		strings.NewReader(`{"path":"`+made+`","pad":"`),
		&fillerReader{remaining: httputil.MaxRequestBodyBytes},
		strings.NewReader(`"}`),
	))
	chunked.Host = "localhost"
	chunked.Header.Set("Content-Type", "application/json")
	chunked.Header.Set("Cookie", cookie)
	chunked.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(w, chunked)
	if w.Code == http.StatusOK {
		t.Error("a chunked body with no bound was accepted")
	}
	if _, err := os.Stat(made); err == nil {
		t.Errorf("the handler acted on an oversized chunked body: %s exists", made)
	}
}

// postThroughChain posts to the middleware chain Serve installs — not to the bare mux,
// which is a different composition and would let an unwired middleware pass.
func postThroughChain(t *testing.T, s *Server, target string, body io.Reader, cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, body)
	// The chain is Host-pinned before anything else, and httptest's default Host is
	// not one this server accepts.
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rr := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(rr, req)
	return rr
}

// TestGETBodyIsNotBounded is the other side of the same middleware, and the reason it
// switches on method: the WebSocket endpoints are GET upgrades, and a bounded reader
// on the upgrade path is a broken stream.
func TestMiddlewareChainKeepsGetUpgrades(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)

	req := httptest.NewRequest(http.MethodGet, "/api/fs", nil)
	req.Host = "localhost"
	req.Header.Set("Cookie", cookie)
	rr := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(rr, req)

	if rr.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("a GET was bounded: %d %s", rr.Code, rr.Body.String())
	}
}

// fillerReader yields exactly remaining bytes and then EOF, so a test can ask for a
// body of an exact size without materialising it.
type fillerReader struct{ remaining int64 }

func (f *fillerReader) Read(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > f.remaining {
		n = int(f.remaining)
	}
	for i := range p[:n] {
		p[i] = 'y'
	}
	f.remaining -= int64(n)
	return n, nil
}
