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

// TestServerRefusesOversizedBody is the same rule through the middleware chain the server actually runs, at
// the message bound, and on the GET that must not be bounded because the WebSocket upgrades are GETs. Both
// bodies are ones the handler would act on, so only the bound can stop it: a garbage body would be refused
// with the cap wired up or not.
func TestServerRefusesOversizedBody(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	// A real mkdir, aimed at a directory the handler would create if it ever saw the document; its absence
	// afterwards is the second half of the assertion. The declared length is what this half of the rule reads,
	// so it is set past the cap over that body.
	made := filepath.Join(t.TempDir(), "made")
	body := `{"path":"` + made + `"}`
	oversized := postRequest("/api/fs/mkdir", strings.NewReader(body), cookie, csrf)
	oversized.ContentLength = httputil.MaxMessageBodyBytes + 1

	rr := serveThroughChain(t, s, oversized)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("declared length over the cap: got %d, want %d: %s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
	if _, err := os.Stat(made); err == nil {
		t.Errorf("the handler acted on an oversized body: %s exists", made)
	}

	// The other sender: no declared length at all (chunked), so nothing to refuse up
	// front and the bound has to come from the reader. The body is still one the
	// handler would otherwise act on.
	chunked := postRequest("/api/fs/mkdir", io.MultiReader(
		strings.NewReader(`{"path":"`+made+`","pad":"`),
		&fillerReader{remaining: httputil.MaxMessageBodyBytes},
		strings.NewReader(`"}`),
	), cookie, csrf)
	w := serveThroughChain(t, s, chunked)
	if w.Code == http.StatusOK {
		t.Error("a chunked body with no bound was accepted")
	}
	if _, err := os.Stat(made); err == nil {
		t.Errorf("the handler acted on an oversized chunked body: %s exists", made)
	}

	// A GET has no body to bound — and the WebSocket endpoints ARE GET upgrades, so bounding them
	// would break the stream. This is the other half of why the middleware switches on the method.
	get := secureRequest(httptest.NewRequest(http.MethodGet, "/api/fs", nil))
	get.Host = "localhost"
	get.Header.Set("Cookie", cookie)
	gr := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(gr, get)
	if gr.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("a GET was bounded: %d %s", gr.Code, gr.Body.String())
	}
}

// postRequest builds the POST every route in front of the mux would receive. Its Content-Length is
// whatever the body's reader reports; a test staging the declared-length case overrides it.
func postRequest(target string, body io.Reader, cookie, csrf string) *http.Request {
	req := secureRequest(httptest.NewRequest(http.MethodPost, target, body))
	// The chain is Host-pinned before anything else, and httptest's default Host is
	// not one this server accepts.
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	return req
}

// serveThroughChain runs a request through the middleware chain Serve installs — not against the bare
// mux, which is a different composition and would let an unwired middleware pass.
func serveThroughChain(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(rr, req)
	return rr
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
