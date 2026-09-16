package server

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"mike/backend/startup"
)

// staticGet runs a request through the nonce middleware and the router, the pair that serves the SPA.
func staticGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	NonceMiddleware(s.router).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

var noncePattern = regexp.MustCompile(`nonce="([^"]+)"`)

// A constant nonce is a published CSP bypass, so "/" carries a fresh one per request.
func TestRootServesTheInjectedDocumentWithItsOwnNonce(t *testing.T) {
	s := testServer(t)

	first := staticGet(t, s, "/")
	if first.Code != http.StatusOK {
		t.Fatalf("GET /: got %d, want 200", first.Code)
	}
	body := first.Body.String()
	if !strings.Contains(body, "<body>board</body>") {
		t.Errorf("GET /: served %q, want the injected document", body)
	}
	if strings.Contains(body, "{{.Nonce}}") {
		t.Errorf("GET /: the placeholder survived into the response: %q", body)
	}
	nonceOne := noncePattern.FindStringSubmatch(body)
	if nonceOne == nil || nonceOne[1] == "" {
		t.Fatalf("GET /: no nonce in %q", body)
	}
	if ct := first.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type: got %q, want text/html", ct)
	}

	nonceTwo := noncePattern.FindStringSubmatch(staticGet(t, s, "/").Body.String())
	if nonceTwo == nil || nonceTwo[1] == nonceOne[1] {
		t.Errorf("second GET /: nonce %v, want one that differs from %q", nonceTwo, nonceOne[1])
	}
}

func TestAssetServedFromTheInjectedTree(t *testing.T) {
	s := testServer(t)

	rr := staticGet(t, s, "/assets/app-12345678.js")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /assets/app-12345678.js: got %d, want 200", rr.Code)
	}
	if got := rr.Body.String(); got != `console.log("app")` {
		t.Errorf("asset body: got %q", got)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type: got %q, want a JavaScript type", ct)
	}
}

// An index of the asset directory hands an unauthenticated caller every chunk the build made.
func TestAssetDirectoryIsNotListed(t *testing.T) {
	s := testServer(t)

	rr := staticGet(t, s, "/assets/")
	if rr.Code == http.StatusOK {
		t.Fatalf("GET /assets/: got 200 %q, want a refusal", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "app-12345678.js") {
		t.Errorf("GET /assets/: body lists the asset tree: %q", rr.Body.String())
	}
}

// An empty 200 would boot a blank page the client then caches.
func TestRootRefusesWhenTheDocumentIsAbsent(t *testing.T) {
	s := testServerWithAssets(t, fstest.MapFS{}, nil)

	rr := staticGet(t, s, "/")
	if rr.Code == http.StatusOK {
		t.Fatalf("GET / on an empty asset tree: got 200 %q, want a refusal", rr.Body.String())
	}
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("GET / on an empty asset tree: got %d, want 500", rr.Code)
	}
}

// A wrong verb on an API path is the mux's refusal, not the application document.
func TestSPAServesNothingToAWrongVerbOnAnAPIPath(t *testing.T) {
	s := testServer(t)

	rr := staticGet(t, s, "/api/projects/p1/tasks/t1/start")
	if rr.Code == http.StatusOK {
		t.Fatalf("GET on a POST-only API route: got 200 %q, want the mux's refusal", rr.Body.String())
	}
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on a POST-only API route: got %d, want 405", rr.Code)
	}
}

// routerResponse is testResponse over s.router: the mux, then the SPA behind it.
func routerResponse(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.router)))).ServeHTTP(rr, req)
	return rr
}

// The 405 and its Allow header stay the mux's answer with the frontend behind it.
func TestMuxRefusesAnUnregisteredVerbWhileTheSPAServes(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	req := testRequest(http.MethodDelete, "/api/projects", nil)
	withAuthCSRF(req, cookie, csrf)
	rr := routerResponse(t, s, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /api/projects: got %d, want 405 from the mux (body %q)", rr.Code, rr.Body.String())
	}
	if allow := rr.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("DELETE /api/projects: Allow=%q, want the registered methods", allow)
	}
}

// The SPA is a construction requirement, not a route that may be missing.
func TestServerRefusesNoAssetsAtAll(t *testing.T) {
	if _, err := NewServer(startup.Config{}, nil, nil); err == nil {
		t.Fatal("NewServer with no assets: got a server, want an error")
	}
}
