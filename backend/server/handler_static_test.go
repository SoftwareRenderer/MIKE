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

// routerResponse is testResponse over s.router: the mux, then the SPA behind it.
func routerResponse(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.router)))).ServeHTTP(rr, req)
	return rr
}

// The SPA sits behind the mux, so a request the mux refused must stay the mux's answer: a catch-all that
// outranked it would hand the application document to a wrong verb on an API path, and the client would
// read HTML where it asked for JSON.
func TestTheSPANeverSwallowsTheMuxsVerbRefusal(t *testing.T) {
	cases := []struct {
		name  string
		verb  string
		path  string
		auth  bool
		allow string
	}{
		// A path registered for POST only, reached with a GET through the frontend chain.
		{name: "a POST-only route under the frontend chain", verb: http.MethodGet,
			path: "/api/projects/p1/tasks/t1/start"},
		// A path registered for GET and POST, refused by the whole chain the server runs.
		{name: "a route with no DELETE, through the whole chain", verb: http.MethodDelete,
			path: "/api/projects", auth: true, allow: "GET"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			req := testRequest(tc.verb, tc.path, nil)
			var rr *httptest.ResponseRecorder
			if tc.auth {
				cookie, csrf := loginTestCSRF(t, s)
				withAuthCSRF(req, cookie, csrf)
				rr = routerResponse(t, s, req)
			} else {
				rr = staticGet(t, s, tc.path)
			}
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s: got %d, want 405 from the mux (body %q)", tc.verb, tc.path, rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "<body>board</body>") {
				t.Errorf("%s %s: the SPA answered a verb the mux refused: %q", tc.verb, tc.path, rr.Body.String())
			}
			if tc.allow != "" {
				if got := rr.Header().Get("Allow"); !strings.Contains(got, tc.allow) {
					t.Errorf("%s %s: Allow=%q, want the registered methods", tc.verb, tc.path, got)
				}
			}
		})
	}
}

// The SPA is a construction requirement, not a route that may be missing.
func TestServerRefusesNoAssetsAtAll(t *testing.T) {
	if _, err := NewServer(startup.Config{}, nil, nil, nil); err == nil {
		t.Fatal("NewServer with no assets: got a server, want an error")
	}
}
