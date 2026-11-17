package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// --- token minting -------------------------------------------------------------

// previewTokenPattern — the only shape a preview token may ever take: 16
// random bytes as lowercase hex. A time-derived or short token is guessable,
// which would expose the doc plus every sibling under its asset root.
var previewTokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestPreviewToken_Minting(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		token, err := newPreviewToken()
		if err != nil {
			t.Fatalf("newPreviewToken: %v", err)
		}
		if !previewTokenPattern.MatchString(token) {
			t.Fatalf("token %q must match ^[0-9a-f]{32}$", token)
		}
		if seen[token] {
			t.Fatalf("token %q repeated — entropy is not random", token)
		}
		seen[token] = true
	}

	// End-to-end: the served token carries the same shape.
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{"content": "<html></html>"})
	if !previewTokenPattern.MatchString(token) {
		t.Fatalf("served token %q must match ^[0-9a-f]{32}$", token)
	}
}

// failingReader is the previewRand test seam: an entropy source that only
// ever fails.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy source unavailable") }

// L2: when the entropy source fails, minting fails the request — a weak
// (time-derived) token must never be minted, stored, or served.
func TestPreviewToken_MintFailureIsFatal(t *testing.T) {
	oldRand := previewRand
	previewRand = failingReader{}
	t.Cleanup(func() { previewRand = oldRand })

	// Store level: put errors out and registers nothing.
	ps := newPreviewStore()
	token, err := ps.put("<html></html>", "index.html", "", "", previewNoScript)
	if err == nil {
		t.Fatalf("expected put to fail when the entropy source fails, got token %q", token)
	}
	if token != "" {
		t.Fatalf("no token may be returned on mint failure, got %q", token)
	}
	ps.mu.Lock()
	stored := len(ps.docs)
	ps.mu.Unlock()
	if stored != 0 {
		t.Fatalf("no document may be registered on mint failure, got %d", stored)
	}

	// Handler level: POST /api/preview answers 500 with an empty store.
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("POST", "/api/preview", map[string]any{"content": "<html></html>"})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when the token cannot be minted, got %d %s", rr.Code, rr.Body.String())
	}
	s.previewStore.mu.Lock()
	served := len(s.previewStore.docs)
	s.previewStore.mu.Unlock()
	if served != 0 {
		t.Fatalf("expected an empty preview store after a mint failure, got %d docs", served)
	}
}

// --- helpers -------------------------------------------------------------------

func previewTestProject(t *testing.T, s *Server) (projectID, dir string) {
	t.Helper()
	dir = t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "preview-project", "path": dir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	return mustJSON[map[string]any](t, rr.Body)["id"].(string), dir
}

// servePreview POSTs /api/preview and returns the token.
func servePreview(t *testing.T, s *Server, cookie, csrfToken string, body map[string]any) (token, url string) {
	t.Helper()
	req := testRequest("POST", "/api/preview", body)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/preview: %d %s", rr.Code, rr.Body.String())
	}
	res := mustJSON[map[string]any](t, rr.Body)
	return res["token"].(string), res["url"].(string)
}

// getPreview: unauthenticated GET (preview iframes never send a session).
func getPreview(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	return getPreviewWithHeaders(t, s, target, nil)
}

func getPreviewWithHeaders(t *testing.T, s *Server, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://localhost"+target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	handler := s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
	handler.ServeHTTP(rr, req)
	return rr
}

// navState reads GET /api/preview-nav/{token} with a session.
func navState(t *testing.T, s *Server, cookie, token string) map[string]any {
	t.Helper()
	req := testRequest("GET", "/api/preview-nav/"+token, nil)
	withAuth(req, cookie)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/preview-nav: %d %s", rr.Code, rr.Body.String())
	}
	return mustJSON[map[string]any](t, rr.Body)
}

// getPreviewFollowing follows up to 3 redirects (e.g. no-trailing-slash).
func getPreviewFollowing(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rr := getPreview(t, s, target)
	for i := 0; (rr.Code == http.StatusFound || rr.Code == http.StatusMovedPermanently) && i < 3; i++ {
		rr = getPreview(t, s, rr.Header().Get("Location"))
	}
	return rr
}

// --- auth -------------------------------------------------------------------

// One preview surface, two guards, and the public half is decided by the verb rather than the prefix:
// an opaque-origin frame cannot send a session cookie, so GET and HEAD under /api/preview/ answer on the
// token alone — and everything else under that prefix, including its DELETE, stays session-authed, as
// does the watchdog record that sits beside the tree.
func TestPreviewRoutesAreGuardedByVerbAndSession(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)

	cases := []struct {
		name    string
		method  string
		path    string
		session bool
		csrf    bool
		want    int
	}{
		{"serve with no session", http.MethodPost, "/api/preview", false, false, http.StatusUnauthorized},
		{"serve with a session and no CSRF token", http.MethodPost, "/api/preview", true, false, http.StatusForbidden},
		{"serve with a verb it never registered", http.MethodPut, "/api/preview", true, true, http.StatusMethodNotAllowed},
		{"release with no session", http.MethodDelete, "/api/preview/some-token", false, false, http.StatusUnauthorized},
		{"the nav record with no session", http.MethodGet, "/api/preview-nav/some-token", false, false, http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest(tc.method, tc.path, map[string]any{"content": "<html></html>"})
			if tc.session {
				withAuth(req, cookie)
			}
			if tc.csrf {
				req.Header.Set("X-CSRF-Token", csrfToken)
			}
			if rr := testResponse(t, s, req); rr.Code != tc.want {
				t.Fatalf("%s %s: got %d, want %d (%s)", tc.method, tc.path, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// --- serve + doc -------------------------------------------------------------

func TestPreviewServesDocWithoutSession(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	html := "<!DOCTYPE html><html><body><h1>hi</h1></body></html>"
	token, url := servePreview(t, s, cookie, csrfToken, map[string]any{"content": html})

	if !strings.HasPrefix(url, "/api/preview/"+token+"/") {
		t.Fatalf("unexpected url %q (want /api/preview/%s/)", url, token)
	}
	if !previewTokenPattern.MatchString(token) {
		t.Fatalf("token %q is not a 128-bit hex bearer", token)
	}

	rr := getPreview(t, s, "/api/preview/"+token+"/")
	if rr.Code != http.StatusOK {
		t.Fatalf("doc GET: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html…", ct)
	}
	// Default (no posture) → the no-script policy.
	csp := rr.Header().Get("Content-Security-Policy")
	if csp != previewDocCSPNoScript {
		t.Errorf("default doc must carry the no-script CSP, got:\n%s", csp)
	}
	if strings.Contains(csp, "frame-ancestors") {
		t.Errorf("preview doc must be frameable (no frame-ancestors), got:\n%s", csp)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if body := rr.Body.String(); body != html {
		t.Errorf("body = %q, want %q", body, html)
	}
}

// Each posture answers with exactly one policy, and a posture the client invents gets the narrowest one:
// an absent field, a stale bundle's `allow_network` and a forged string all answer the no-script policy.
// The policy has to reach the browser as the tokens its own text promises — comparing the header with
// the constant is what proves the `sandbox allow-scripts` survived the trip.
func TestPreviewServe_PostureSelectsPolicy(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	html := "<!DOCTYPE html><html><body><h1>hi</h1></body></html>"
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
		// wantSandbox is the sandbox directive the served header must carry, spelled out rather than
		// read back from the policy: a policy that stopped sandboxing would otherwise look compliant.
		wantSandbox string
	}{
		{"default", map[string]any{"content": html}, previewDocCSPNoScript, ""},
		{"no-script", map[string]any{"content": html, "posture": "no-script"}, previewDocCSPNoScript, ""},
		{"scripts", map[string]any{"content": html, "posture": "scripts"}, previewDocCSPStrict, "allow-scripts"},
		{"network", map[string]any{"content": html, "posture": "network"}, previewDocCSPNetwork, "allow-scripts"},
		{"unknown posture", map[string]any{"content": html, "posture": "everything"}, previewDocCSPNoScript, ""},
		{"legacy allow_network", map[string]any{"content": html, "allow_network": true}, previewDocCSPNoScript, ""},
	} {
		token, _ := servePreview(t, s, cookie, csrfToken, tc.body)
		rr := getPreview(t, s, "/api/preview/"+token+"/")
		if rr.Code != http.StatusOK {
			t.Fatalf("%s doc GET: expected 200, got %d %s", tc.name, rr.Code, rr.Body.String())
		}
		csp := rr.Header().Get("Content-Security-Policy")
		if csp != tc.want {
			t.Errorf("%s: CSP =\n%s\nwant\n%s", tc.name, csp, tc.want)
		}
		if strings.Contains(csp, "frame-ancestors") {
			t.Errorf("%s: preview doc must be frameable (no frame-ancestors), got:\n%s", tc.name, csp)
		}
		if !cspHasDirective(csp, "sandbox") {
			t.Errorf("%s: served document has no sandbox directive, so an unframed preview document would "+
				"keep the app's origin, its cookies and its API", tc.name)
			continue
		}
		if got := cspDirectiveValue(csp, "sandbox"); got != tc.wantSandbox {
			t.Errorf("%s: sandbox = %q, want %q", tc.name, got, tc.wantSandbox)
		}
	}
}

// INVARIANT: no policy that can run a byte leaves that byte a way out. A `*` in a load directive is a way
// out, because the load itself carries the URL to a remote server, so every policy that runs scripts
// confines its load directives and shuts connect-src. previewDocCSPNetwork is the exception, and it sits
// behind the preview.serveNetwork grant.
func TestPreviewDocCSP_NeverPairsScriptsWithEgress(t *testing.T) {
	load := []string{"default-src", "script-src", "style-src", "img-src", "font-src", "media-src", "frame-src", "worker-src", "object-src"}
	for _, tc := range []struct {
		name        string
		csp         string
		runsScripts bool
	}{
		{"no-script", previewDocCSPNoScript, false},
		{"strict", previewDocCSPStrict, true},
	} {
		// runsScripts is written out rather than read from the policy: a policy that stopped running
		// scripts would otherwise skip the checks below and look compliant.
		if runs := cspDirectiveValue(tc.csp, "script-src") != "'none'"; runs != tc.runsScripts {
			t.Errorf("%s policy: script-src = %q against runsScripts = %v", tc.name,
				cspDirectiveValue(tc.csp, "script-src"), tc.runsScripts)
		}
		if !tc.runsScripts {
			continue
		}
		for _, name := range load {
			if got := cspDirectiveValue(tc.csp, name); strings.Contains(got, "*") {
				t.Errorf("%s policy runs scripts and would send data out through %s = %q", tc.name, name, got)
			}
		}
		if got := cspDirectiveValue(tc.csp, "connect-src"); got != "'none'" {
			t.Errorf("%s policy runs scripts: connect-src = %q, want 'none'", tc.name, got)
		}
	}
}

// INVARIANT: the no-script policy runs nothing, and every directive that could reach a URL the document
// invented is 'none'. Only the load directives a scriptless render needs stay open.
func TestPreviewDocCSPNoScript_RendersWithoutRunningAnything(t *testing.T) {
	for _, name := range []string{"default-src", "script-src", "worker-src", "connect-src", "object-src", "base-uri", "form-action"} {
		if got := cspDirectiveValue(previewDocCSPNoScript, name); got != "'none'" {
			t.Errorf("no-script CSP: %s = %q, want 'none'", name, got)
		}
	}
	for _, name := range []string{"style-src", "img-src", "font-src", "media-src", "frame-src"} {
		if got := cspDirectiveValue(previewDocCSPNoScript, name); !strings.Contains(got, "*") {
			t.Errorf("no-script CSP: %s = %q, want a wildcard (a no-JS browser loads these)", name, got)
		}
	}
}

// INVARIANT: the preview document is opaque-origin because the server says so, not because the extension
// remembered to write `sandbox` on its frame. This URL is same-origin with the application and
// GET /api/auth/session answers any same-origin GET with the CSRF token, so a document that kept its
// origin is one fetch away from every mutating endpoint. `allow-same-origin` would hand that back, so it
// never appears; `allow-scripts` appears only in the two postures whose policy already permits scripts.
func TestPreviewDocCSP_SandboxsWithoutSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		name        string
		csp         string
		wantScripts bool
	}{
		{"no-script", previewDocCSPNoScript, false},
		{"strict", previewDocCSPStrict, true},
		{"network", previewDocCSPNetwork, true},
	} {
		if !cspHasDirective(tc.csp, "sandbox") {
			t.Errorf("%s CSP: no sandbox directive — an unframed preview document would keep the app's origin", tc.name)
			continue
		}
		sandbox := cspDirectiveValue(tc.csp, "sandbox")
		for _, token := range strings.Fields(sandbox) {
			if token == "allow-same-origin" {
				t.Errorf("%s CSP: sandbox carries allow-same-origin, which restores the app's origin, its cookies and its API", tc.name)
			}
		}
		if got := strings.Contains(" "+sandbox+" ", " allow-scripts "); got != tc.wantScripts {
			t.Errorf("%s CSP: sandbox = %q, allow-scripts present = %v, want %v", tc.name, sandbox, got, tc.wantScripts)
		}
	}
}

// cspHasDirective reports whether the policy names a directive at all, even with no value after it.
func cspHasDirective(csp, name string) bool {
	for _, part := range strings.Split(csp, ";") {
		fields := strings.Fields(part)
		if len(fields) > 0 && fields[0] == name {
			return true
		}
	}
	return false
}

// cspDirectiveValue extracts one directive's value from a CSP string ("" if absent).
func cspDirectiveValue(csp, name string) string {
	for _, part := range strings.Split(csp, ";") {
		fields := strings.Fields(part)
		if len(fields) > 0 && fields[0] == name {
			return strings.Join(fields[1:], " ")
		}
	}
	return ""
}

// Doc Content-Type follows the previewed file's extension (host-derived).
func TestPreviewDocContentType_FollowsFileExtension(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"index.html", "text/html"},
		{"page.htm", "text/html"},
		{"logo.svg", "image/svg+xml"},
		{"page.xhtml", "application/xhtml+xml"},
		{"noext", "text/html; charset=utf-8"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			s := testServer(t)
			cookie, csrfToken := loginTestCSRF(t, s)
			projectID, _ := previewTestProject(t, s)
			token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
				"content": "x", "project": projectID, "path": tc.path,
			})
			rr := getPreview(t, s, "/api/preview/"+token+"/")
			if rr.Code != http.StatusOK {
				t.Fatalf("doc GET: %d %s", rr.Code, rr.Body.String())
			}
			ct := rr.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, tc.want) {
				t.Errorf("Content-Type = %q, want prefix %q", ct, tc.want)
			}
			if strings.Contains(ct, "; charset=") && strings.Count(ct, "; charset=") > 1 {
				t.Errorf("Content-Type has duplicate charset: %q", ct)
			}
		})
	}

	// No file context at all → plain HTML document.
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{"content": "x"})
	rr := getPreview(t, s, "/api/preview/"+token+"/")
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("no-context Content-Type = %q, want text/html…", ct)
	}
}

// --- sibling assets -----------------------------------------------------------

// A token opens a read of the tree under its document's root, so a sibling is served from the doc's own
// directory and from the worktree root beside it, at whatever depth.
func TestPreviewServesSiblingAsset(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body { color: red; }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>",
		"project": projectID,
		"path":    "page.html",
	})

	for _, sibling := range []struct{ path, body, ext string }{
		{"style.css", "body { color: red; }", ".css"},
		{"assets/app.js", "console.log(1)", ".js"},
	} {
		rr := getPreview(t, s, "/api/preview/"+token+"/"+sibling.path)
		if rr.Code != http.StatusOK {
			t.Fatalf("asset GET %s: expected 200, got %d %s", sibling.path, rr.Code, rr.Body.String())
		}
		wantMime := mime.TypeByExtension(sibling.ext)
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, wantMime) {
			t.Errorf("asset %s Content-Type = %q, want prefix %q", sibling.path, ct, wantMime)
		}
		if body := rr.Body.String(); body != sibling.body {
			t.Errorf("asset %s body = %q, want %q", sibling.path, body, sibling.body)
		}
	}
}

// This list is the whole answer to what a leaked preview token can read. An unmapped name once answered
// application/octet-stream, which made every byte under the root readable — `.env`, keys, git config.
func TestPreviewAsset_TypeAllowList(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)

	want := map[string]int{
		// What a page embeds: styles, scripts, media, and the HTML a link walks to.
		"rel.css":        http.StatusOK,
		"app.js":         http.StatusOK,
		"logo.svg":       http.StatusOK,
		"sub/page2.html": http.StatusOK,
		"logo.png":       http.StatusOK,
		"font.woff2":     http.StatusOK,
		"clip.mp3":       http.StatusOK,
		// What only a token thief wants.
		".env":                http.StatusNotFound,
		".env.production":     http.StatusNotFound,
		"id_rsa":              http.StatusNotFound,
		"server.pem":          http.StatusNotFound,
		"config.json":         http.StatusNotFound,
		"local.settings.json": http.StatusNotFound,
		"notes.txt":           http.StatusNotFound,
		"data.csv":            http.StatusNotFound,
		"app.js.map":          http.StatusNotFound,
		"doc.pdf":             http.StatusNotFound,
		"mod.wasm":            http.StatusNotFound,
		"LICENSE":             http.StatusNotFound,
		// Names the type rule would serve, so only the `.git` rule refuses them. `sub/.git/` keeps the
		// fixture's real checkout intact.
		"sub/.git/config.html": http.StatusNotFound,
		"sub/.git/hooks/x.js":  http.StatusNotFound,
		// Refused by the type rule alone: no extension, so no type.
		".git/config.json": http.StatusNotFound,
		"sub/.git/HEAD":    http.StatusNotFound,
	}

	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name := range want {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("content of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>", "project": projectID, "path": "page.html",
	})

	for name, wantStatus := range want {
		rr := getPreview(t, s, "/api/preview/"+token+"/"+name)
		if rr.Code != wantStatus {
			t.Errorf("anon GET %s: got %d, want %d (%s)", name, rr.Code, wantStatus, rr.Body.String())
		}
	}
}

func TestPreviewAsset_TraversalRejected(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	secret := filepath.Join(dir, "..", "preview-secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(secret)

	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>",
		"project": projectID,
		"path":    "page.html",
	})

	// Encoded traversal — the mux may redirect or reject; the final answer
	// must be 404, never the file content.
	rr := getPreviewFollowing(t, s, "/api/preview/"+token+"/%2e%2e/preview-secret.txt")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("traversal: expected 404, got %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "top secret") {
		t.Fatal("traversal leaked file content")
	}
}

func TestPreviewAsset_SymlinkEscapeRejected(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.css")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(dir, "link.css")); err != nil {
		t.Fatalf("plant the link this refusal is about: %v", err)
	}

	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>",
		"project": projectID,
		"path":    "page.html",
	})

	rr := getPreview(t, s, "/api/preview/"+token+"/link.css")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("symlink escape: expected 404, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestPreviewAsset_DirectoryRejected(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>",
		"project": projectID,
		"path":    "page.html",
	})

	for _, target := range []string{"/api/preview/" + token + "/subdir", "/api/preview/" + token + "/subdir/"} {
		rr := getPreviewFollowing(t, s, target)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("directory %q: expected 404, got %d", target, rr.Code)
		}
	}
}

func TestPreviewAsset_SelfContainedDocHasNoAssets(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	// No project/path — a generically served doc has no base directory.
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{"content": "<html></html>"})
	rr := getPreview(t, s, "/api/preview/"+token+"/style.css")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 asset for self-contained doc, got %d", rr.Code)
	}
}

func TestPreviewServe_PathOutsideProjectRejected(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, _ := previewTestProject(t, s)
	req := testRequest("POST", "/api/preview", map[string]any{
		"content": "<html></html>",
		"project": projectID,
		"path":    "/etc/passwd",
	})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for path outside project, got %d %s", rr.Code, rr.Body.String())
	}
}

// --- lifecycle ----------------------------------------------------------------

// Any read refreshes the idle TTL, and a read costs only the URL, so a stolen token lives as long as
// whoever holds it keeps asking. The absolute cap is what ends that.
func TestPreviewMaxAge_ReadsCannotRenewADoc(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	serve := func() string {
		tok, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
			"content": "<html></html>", "project": projectID, "path": "page.html",
		})
		// Both docs are already overdue on the idle rule, well inside the absolute cap.
		s.previewStore.mu.Lock()
		s.previewStore.docs[tok].lastAccess = time.Now().Add(-previewTTL - time.Second)
		s.previewStore.mu.Unlock()
		return tok
	}
	serves := func(tok string) bool {
		s.previewStore.sweep(time.Now())
		s.previewStore.mu.Lock()
		defer s.previewStore.mu.Unlock()
		d, ok := s.previewStore.docs[tok]
		return ok && !d.expired
	}

	read, idle := serve(), serve()
	getPreview(t, s, "/api/preview/"+read+"/style.css") // the holder renews it
	if !serves(read) {
		t.Fatal("a doc the holder read was swept — the read no longer renews the idle TTL, so this test's premise is gone")
	}
	if serves(idle) {
		t.Fatal("an idle doc survived the idle rule")
	}

	// Both now past the absolute cap. The read must not save it this time.
	s.previewStore.mu.Lock()
	if d, ok := s.previewStore.docs[read]; ok {
		d.createdAt = time.Now().Add(-previewMaxAge - time.Second)
	}
	s.previewStore.mu.Unlock()
	getPreview(t, s, "/api/preview/"+read+"/style.css")
	if serves(read) {
		t.Error("a read renewed a doc past previewMaxAge — a leaked token stays readable for as long as it is used")
	}
}

// INVARIANT: no response under /api/preview/ grants a cross-origin read. A preview document has an opaque
// origin, and `Access-Control-Allow-Origin: null` is the only value that would let it read the bytes it is
// handed — and that value admits every sandboxed iframe on the internet. Nothing sets it today; this
// pins the decision rather than the current handlers.
func TestPreviewResponsesNeverGrantACrossOriginRead(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>", "project": projectID, "path": "page.html",
	})

	for _, target := range []string{
		"/api/preview/" + token + "/",                   // the document the frame shows
		"/api/preview/" + token + "/style.css",          // a sibling it loads
		"/api/preview/" + token + "/page.html",          // the self-reference
		"/api/preview/" + token + "/.env",               // refused by type
		"/api/preview/" + token + "/gone.css",           // refused because absent
		"/api/preview/" + strings.Repeat("f", 32) + "/", // refused because unknown
		"/api/preview-nav/" + token,                     // the session-authed record beside the tree
	} {
		req := secureRequest(httptest.NewRequest(http.MethodGet, "http://localhost"+target, nil))
		req.Host = "localhost"
		rr := httptest.NewRecorder()
		// Served through the full middleware chain, so a header CORSMiddleware would add shows up here.
		s.middlewareChain(s.mux).ServeHTTP(rr, req)
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q — the preview document is now allowed to read this response", target, got)
		}
	}
}

// An expired token is answered apart from one never minted, because a caller can act on the difference:
// re-serve, or say the preview stopped.
func TestPreviewExpiredToken_AnswersExpiredNotNotFound(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>", "project": projectID, "path": "page.html",
	})
	s.previewStore.mu.Lock()
	s.previewStore.docs[token].createdAt = time.Now().Add(-previewMaxAge - time.Second)
	s.previewStore.mu.Unlock()
	s.previewStore.sweep(time.Now())

	for _, target := range []string{"/api/preview/" + token + "/", "/api/preview/" + token + "/style.css"} {
		rr := getPreview(t, s, target)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", target, rr.Code)
		}
		if body := rr.Body.String(); !strings.Contains(body, "preview expired") {
			t.Errorf("%s: %s, want the answer to say the token expired", target, body)
		}
	}

	// A token that was never minted answers with the other message.
	rr := getPreview(t, s, "/api/preview/"+strings.Repeat("e", 32)+"/")
	if body := rr.Body.String(); rr.Code != http.StatusNotFound || !strings.Contains(body, "preview not found") {
		t.Errorf("unknown token: %d %s, want 404 preview not found", rr.Code, body)
	}

	// A 404 on the stub must not renew it; the idle rule still retires it.
	s.previewStore.mu.Lock()
	s.previewStore.docs[token].lastAccess = time.Now().Add(-previewTTL - time.Second)
	s.previewStore.mu.Unlock()
	getPreview(t, s, "/api/preview/"+token+"/")
	s.previewStore.sweep(time.Now())
	s.previewStore.mu.Lock()
	_, stillThere := s.previewStore.docs[token]
	s.previewStore.mu.Unlock()
	if stillThere {
		t.Error("an expired stub survived a read plus the idle rule — a dead token would sit in the table for as long as anything asked for it")
	}
}

func TestPreviewCap_EvictsOldest(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	first := ""
	for i := 0; i < previewMaxLive+1; i++ {
		tok, _ := servePreview(t, s, cookie, csrfToken, map[string]any{"content": "<p>x</p>"})
		if i == 0 {
			first = tok
		}
	}
	rr := getPreview(t, s, "/api/preview/"+first+"/")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected oldest doc evicted (404), got %d", rr.Code)
	}
}

func TestPreviewDelete(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{"content": "<html></html>"})

	req := testRequest("DELETE", "/api/preview/"+token, nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE: expected 2xx, got %d %s", rr.Code, rr.Body.String())
	}
	rr = getPreview(t, s, "/api/preview/"+token+"/")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rr.Code)
	}
}

// --- size limits ---------------------------------------------------------------

func TestPreviewServe_ContentTooLarge(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	big := strings.Repeat("a", previewMaxContent+1)
	req := testRequest("POST", "/api/preview", map[string]any{"content": big})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized content, got %d", rr.Code)
	}
}

// --- path resolution -------------------------------------------------------------

// The asset root a preview token exposes is decided here: the project's checkout plus the worktree of the
// task the request named, never the workspace dir. A relative path resolves against the worktree root,
// and anything that lands outside those roots — by name or by an absolute path elsewhere on the host —
// is refused rather than made readable.
func TestResolveFSFilePathJailsToTheProjectAndItsWorktree(t *testing.T) {
	cases := []struct {
		name     string
		project  string
		absolute bool // name the file by its host path rather than relative to the worktree
		path     string
		wantErr  bool
		wantDir  string // relative to the project dir; only read when the call succeeds
		wantHost string // relative to the project dir
	}{
		{name: "a nested page, named relatively", path: "nested/page.html", wantDir: "nested", wantHost: "nested/page.html"},
		{name: "the same page, named absolutely", absolute: true, path: "page.html", wantDir: ".", wantHost: "page.html"},
		{name: "a sibling's checkout", path: "../elsewhere/page.html", wantErr: true},
		{name: "a host file no project holds", path: "/etc/passwd", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			dir := t.TempDir()
			initGitRepo(t, dir)
			projectID := createTestProjectInDir(t, s, dir)
			if tc.project != "" {
				projectID = tc.project
			}
			path := tc.path
			if tc.absolute {
				path = filepath.Join(dir, tc.path)
			}

			root, got, host, err := s.resolveFSFilePath(projectID, "", path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveFSFilePath(%q) succeeded: root=%q dir=%q host=%q", path, root, got, host)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveFSFilePath(%q): %v", path, err)
			}
			if root != filepath.Clean(dir) {
				t.Errorf("worktree root = %q, want %q", root, dir)
			}
			if want := filepath.Join(filepath.Clean(dir), tc.wantDir); filepath.Clean(got) != want {
				t.Errorf("dir = %q, want %q", got, want)
			}
			if want := filepath.Join(filepath.Clean(dir), tc.wantHost); filepath.Clean(host) != want {
				t.Errorf("hostPath = %q, want %q", host, want)
			}
		})
	}

	// A call that names neither half has no root to derive, so it cannot be answered at all.
	t.Run("missing inputs", func(t *testing.T) {
		s := testServer(t)
		if _, _, _, err := s.resolveFSFilePath("", "", "page.html"); err == nil {
			t.Error("an empty project was accepted")
		}
		if _, _, _, err := s.resolveFSFilePath("some-id", "", ""); err == nil {
			t.Error("an empty path was accepted")
		}
	})
}

// The serve url is the doc's directory under the token root, and the routes answer around it: a root-level
// file has the token root as its directory, a file deeper down gets that subdirectory, and the token root
// itself is then the worktree root — the whole tree, so never a listing. Relative links must resolve
// against the doc's own siblings and `../` must walk to the worktree root.
func TestPreviewDocDirectoryURLAndRouting(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, _ := previewTestProject(t, s)
	content := "<html><body>SERVED</body></html>"

	cases := []struct {
		name string
		path string // the previewed file, as the serve request names it
		url  string // the url the serve answers, under the token
		// rootStatus is what GET /api/preview/<token>/ answers: the doc itself for a root-level file,
		// a 404 for a deeper one, whose directory is not the token root.
		rootStatus int
	}{
		{"a file at the worktree root", "index.html", "/", http.StatusOK},
		{"a file one directory down", "docs/page.html", "/docs/", http.StatusNotFound},
		{"a file three directories down", "a/b/c.html", "/a/b/", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, url := servePreview(t, s, cookie, csrfToken, map[string]any{
				"content": content, "project": projectID, "path": tc.path,
			})
			if want := "/api/preview/" + token + tc.url; url != want {
				t.Fatalf("serve url = %q, want %q", url, want)
			}

			// The doc at its directory URL.
			rr := getPreview(t, s, url)
			if rr.Code != http.StatusOK || rr.Body.String() != content {
				t.Fatalf("doc GET %s: %d %q, want 200 %q", url, rr.Code, rr.Body.String(), content)
			}
			if csp := rr.Header().Get("Content-Security-Policy"); csp != previewDocCSPNoScript {
				t.Errorf("doc CSP = %q, want the doc's posture policy", csp)
			}

			// The directory URL without its slash, and the bare token, both redirect to the doc's dir.
			for _, unslashed := range []string{strings.TrimSuffix(url, "/"), "/api/preview/" + token} {
				rr := getPreview(t, s, unslashed)
				if rr.Code != http.StatusMovedPermanently && rr.Code != http.StatusFound {
					t.Fatalf("GET %s: expected a redirect, got %d", unslashed, rr.Code)
				}
				if loc := rr.Header().Get("Location"); loc != url {
					t.Errorf("GET %s: Location = %q, want %q", unslashed, loc, url)
				}
			}
			if final := getPreviewFollowing(t, s, "/api/preview/"+token); final.Code != http.StatusOK {
				t.Errorf("after the bare-token redirect: %d, want 200", final.Code)
			}

			// The token root: the worktree root for a deeper doc, so 404 rather than a listing.
			if rr := getPreview(t, s, "/api/preview/"+token+"/"); rr.Code != tc.rootStatus {
				t.Errorf("token root GET: %d, want %d", rr.Code, tc.rootStatus)
			}
		})
	}
}

// TestPreviewServe_WorkspaceOnlyPathIsRefused: a path inside the workspace dir but inside neither the
// project nor the named task's worktree is refused.
//
// The workspace dir holds every project's task worktrees, so the asset root is the whole reach of a
// preview token — the document itself is unauthenticated by design (an iframe cannot carry a session).
// The project checkout plus the request's own worktree is the jail.
func TestPreviewServe_WorkspaceOnlyPathIsRefused(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, _ := previewTestProject(t, s)

	cfg, err := s.configStore.GetConfig()
	if err != nil || cfg.WorkspaceDir == "" {
		t.Fatalf("need a configured workspace dir, got %+v / %v", cfg.WorkspaceDir, err)
	}
	strayDir := filepath.Join(cfg.WorkspaceDir, "stray-"+randHex(4))
	if err := os.MkdirAll(strayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(strayDir, "page.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := testRequest("POST", "/api/preview", map[string]any{
		"content": "<html></html>", "project": projectID, "path": filepath.Join(strayDir, "page.html"),
	})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code == http.StatusOK {
		t.Fatalf("a path outside the project and its worktree was served: %s", rr.Body.String())
	}

	// The project's own checkout still serves, so this is the jail and not a broken
	// resolver.
	if err := os.WriteFile(filepath.Join(strayDir, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	token, url := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>", "project": projectID, "path": "index.html",
	})
	if url != "/api/preview/"+token+"/" {
		t.Fatalf("project-root preview url = %q, want the token root", url)
	}
}

// Self-reference by file name → served content (possibly unsaved), not disk — for a doc at the worktree
// root and for one deeper down, whose self name carries its directory as well.
func TestPreviewSelfReference_ServesServedContent(t *testing.T) {
	s := testServer(t)
	projectID, dir := previewTestProject(t, s)
	cookie, csrfToken := loginTestCSRF(t, s)

	cases := []struct {
		name        string
		onDisk      string
		path        string
		requestPath string
	}{
		{"a doc at the worktree root", "index.html", "index.html", "index.html"},
		{"a doc in a subdirectory", "docs/page.html", "docs/page.html", "docs/page.html"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, tc.onDisk)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.onDisk), []byte("ON-DISK"), 0o644); err != nil {
				t.Fatal(err)
			}
			content := "<html><body>EDITED</body></html>"
			token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
				"content": content, "project": projectID, "path": tc.path,
			})
			rr := getPreview(t, s, "/api/preview/"+token+"/"+tc.requestPath)
			if rr.Code != http.StatusOK {
				t.Fatalf("self-ref GET: %d %s", rr.Code, rr.Body.String())
			}
			if body := rr.Body.String(); body != content {
				t.Errorf("body = %q, want the served content %q, not the file on disk", body, content)
			}
			if csp := rr.Header().Get("Content-Security-Policy"); csp != previewDocCSPNoScript {
				t.Errorf("self-ref doc must carry the preview CSP, got %q", csp)
			}
		})
	}
}

// createTestProjectInDir is like createTestProject but in a caller-chosen dir.
func createTestProjectInDir(t *testing.T, s *Server, dir string) string {
	t.Helper()
	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "preview-project-" + randHex(4), "path": dir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	return mustJSON[map[string]any](t, rr.Body)["id"].(string)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Widened root: a subdir doc reaches BOTH its own dir's files and files at
// the worktree root.
func TestPreviewSubdirDoc_SiblingsResolveFromWorktreeRoot(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.css"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "local.css"), []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>", "project": projectID, "path": "docs/page.html",
	})

	rr := getPreview(t, s, "/api/preview/"+token+"/root.css")
	if rr.Code != http.StatusOK || rr.Body.String() != "root" {
		t.Fatalf("worktree-root asset: %d %q", rr.Code, rr.Body.String())
	}
	rr = getPreview(t, s, "/api/preview/"+token+"/docs/local.css")
	if rr.Code != http.StatusOK || rr.Body.String() != "local" {
		t.Fatalf("doc-dir asset: %d %q", rr.Code, rr.Body.String())
	}
}

// Sibling DOCUMENTS carry the doc's CSP, whichever posture served it — a navigated page must keep the
// doc's egress posture, or the posture could be skipped by linking to a sibling .html. A sibling that is
// not a document carries none.
func TestPreviewSiblingDoc_CarriesDocCSP(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	if err := os.WriteFile(filepath.Join(dir, "page2.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		posture string
		want    string
	}{
		{"the default posture", "", previewDocCSPNoScript},
		{"the network posture", "network", previewDocCSPNetwork},
	} {
		token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
			"content": "<html></html>", "project": projectID, "path": "page.html",
			"posture": tc.posture,
		})
		rr := getPreview(t, s, "/api/preview/"+token+"/page2.html")
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: sibling doc GET: %d", tc.name, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: sibling Content-Type = %q", tc.name, ct)
		}
		if csp := rr.Header().Get("Content-Security-Policy"); csp != tc.want {
			t.Errorf("%s: sibling doc CSP = %q, want the doc's posture policy", tc.name, csp)
		}

		// A sibling that is not a document carries no policy of its own.
		if rr := getPreview(t, s, "/api/preview/"+token+"/style.css"); rr.Code != http.StatusOK {
			t.Fatalf("%s: css GET: %d", tc.name, rr.Code)
		} else if csp := rr.Header().Get("Content-Security-Policy"); csp != "" {
			t.Errorf("%s: non-doc sibling must carry no CSP, got %q", tc.name, csp)
		}
	}
}

// --- root-relative rewrite ---------------------------------------------------

var rewriteTestRoot = "/api/preview/" + strings.Repeat("a", 32) + "/"

// The rewrite is the whole difference between a previewed site and a link into the application: a
// root-relative ref must land inside the token's tree, and anything the document's author addressed
// somewhere else — absolute, protocol-relative, merely relative — must be left exactly as written.
func TestRewriteRootRelative(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"double-quoted", `<a href="/x.html">`, `<a href="` + rewriteTestRoot + `x.html">`},
		{"single-quoted", `<a href='/x.html'>`, `<a href='` + rewriteTestRoot + `x.html'>`},
		{"spaced-equals", `<a href = "/x.html">`, `<a href = "` + rewriteTestRoot + `x.html">`},
		{"src", `<img src="/img/p.png">`, `<img src="` + rewriteTestRoot + `img/p.png">`},
		{"action", `<form action="/submit">`, `<form action="` + rewriteTestRoot + `submit">`},
		{"poster", `<video poster="/p.jpg">`, `<video poster="` + rewriteTestRoot + `p.jpg">`},
		{"content-attr", `<meta content="/x">`, `<meta content="` + rewriteTestRoot + `x">`},
		{"bare-root", `<a href="/">`, `<a href="` + rewriteTestRoot + `">`},
		{"protocol-relative", `<a href="//cdn.example/x">`, `<a href="//cdn.example/x">`},
		{"absolute", `<a href="https://example.com/x">`, `<a href="https://example.com/x">`},
		{"relative", `<a href="sub/x.html">`, `<a href="sub/x.html">`},
		{"dot-relative", `<a href="./x.html">`, `<a href="./x.html">`},
		{"meta-refresh-delayed", `<meta http-equiv="refresh" content="2;url=/x">`, `<meta http-equiv="refresh" content="2;url=/x">`},
		{"two-refs", `<a href="/a"><b src="/b.png">`, `<a href="` + rewriteTestRoot + `a"><b src="` + rewriteTestRoot + `b.png">`},
		{"css-url-unquoted", `background: url(/img/bg.png)`, `background: url(` + rewriteTestRoot + `img/bg.png)`},
		{"css-url-double-quoted", `background: url("/img/bg.png")`, `background: url("` + rewriteTestRoot + `img/bg.png")`},
		{"css-url-single-quoted", `background: url('/img/bg.png')`, `background: url('` + rewriteTestRoot + `img/bg.png')`},
		{"css-url-spaced", `background: url( /img/bg.png )`, `background: url( ` + rewriteTestRoot + `img/bg.png )`},
		{"css-url-protocol-relative-quoted", `url("//cdn.example/b.png")`, `url("//cdn.example/b.png")`},
		{"css-url-protocol-relative-unquoted", `url(//cdn.example/b.png)`, `url(//cdn.example/b.png)`},
		{"css-url-absolute", `url(https://x/b.png)`, `url(https://x/b.png)`},
		{"css-url-relative", `url(relative/b.png)`, `url(relative/b.png)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rewriteRootRelative(tc.in, rewriteTestRoot); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Serve applies the rewrite to HTML-family files only (the content type is
// host-derived from the file name).
func TestPreviewServe_RewriteAppliesToHTMLFamilyOnly(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, _ := previewTestProject(t, s)

	// index.html → root-rewritten.
	tokenHTML, urlHTML := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": `<a href="/x.html"></a>`, "project": projectID, "path": "index.html",
	})
	root := urlHTML // the doc url ends in / and is the token root (root-level file)
	rr := getPreview(t, s, urlHTML)
	if rr.Code != http.StatusOK {
		t.Fatalf("doc GET: %d", rr.Code)
	}
	want := `<a href="` + root + `x.html"></a>`
	if body := rr.Body.String(); body != want {
		t.Fatalf("html body = %q, want %q", body, want)
	}
	_ = tokenHTML

	// README.md → untouched.
	_, urlMD := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": `<a href="/x.md"></a>`, "project": projectID, "path": "README.md",
	})
	rr = getPreview(t, s, urlMD)
	if rr.Code != http.StatusOK {
		t.Fatalf("md doc GET: %d", rr.Code)
	}
	if body := rr.Body.String(); body != `<a href="/x.md"></a>` {
		t.Fatalf("md body = %q (want untouched)", body)
	}
}

// The size cap is enforced on the STORED (post-rewrite) content: dense
// root-refs can push content over the cap after substitution.
func TestPreviewStorePut_RewriteGrowthExceedsCap(t *testing.T) {
	ps := newPreviewStore()
	unit := `a href="/x">` // 10 chars → ~55 after rewrite
	n := previewMaxContent / len(unit)
	content := strings.Repeat(unit, n) // ≤ cap pre-rewrite
	if _, err := ps.put(content, "index.html", "", "", previewNoScript); err == nil {
		t.Fatal("expected errPreviewTooLarge for content that exceeds the cap post-rewrite")
	} else if !errors.Is(err, errPreviewTooLarge) {
		t.Fatalf("unexpected error: %v", err)
	}
	// Control: same size, no refs → stored fine.
	if _, err := ps.put(strings.Repeat("x", n*len(unit)), "index.html", "", "", previewNoScript); err != nil {
		t.Fatalf("no-ref content at the cap must store: %v", err)
	}
}

// --- nav record + the nav route -----------------------------------------------

func TestPreviewNavRecord_CommittedDocumentsBumpSequence(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	for name, content := range map[string]string{
		"page2.html": "<html></html>",
		"style.css":  "body{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>", "project": projectID, "path": "page.html",
	})
	docNav := map[string]string{"Sec-Fetch-Dest": "document"}
	frameNav := map[string]string{"Sec-Fetch-Dest": "iframe"}
	imgSub := map[string]string{"Sec-Fetch-Dest": "image"}

	st := navState(t, s, cookie, token)
	expectNav := func(label string, wantSeq int, wantPath string, wantOK, wantIsDoc bool) {
		t.Helper()
		st = navState(t, s, cookie, token)
		if st["seq"].(float64) != float64(wantSeq) || st["path"].(string) != wantPath ||
			st["ok"].(bool) != wantOK || st["isDoc"].(bool) != wantIsDoc {
			t.Fatalf("%s: %v (want seq %d path %q ok %v isDoc %v)", label, st, wantSeq, wantPath, wantOK, wantIsDoc)
		}
	}

	// No document request yet → seq 0, no record.
	expectNav("initial", 0, "", false, false)

	// Doc GET as a top-frame load (dest=document) → seq 1, the doc itself.
	rr := getPreviewWithHeaders(t, s, "/api/preview/"+token+"/", docNav)
	if rr.Code != http.StatusOK {
		t.Fatalf("doc GET: %d", rr.Code)
	}
	expectNav("after doc GET (document)", 1, "", true, true)

	// The preview frame is a SUBFRAME — real frame loads carry dest=iframe
	// (empirically verified in Chromium); those must record too.
	rr = getPreviewWithHeaders(t, s, "/api/preview/"+token+"/", frameNav)
	if rr.Code != http.StatusOK {
		t.Fatalf("doc GET (iframe): %d", rr.Code)
	}
	expectNav("after doc GET (iframe)", 2, "", true, true)

	// Subresource GETs (no dest / dest=image) never bump.
	rr = getPreview(t, s, "/api/preview/"+token+"/page2.html")
	if rr.Code != http.StatusOK {
		t.Fatalf("subresource GET: %d", rr.Code)
	}
	expectNav("after subresource (no dest)", 2, "", true, true)
	rr = getPreviewWithHeaders(t, s, "/api/preview/"+token+"/style.css", imgSub)
	if rr.Code != http.StatusOK {
		t.Fatalf("img GET: %d", rr.Code)
	}
	expectNav("after subresource (image)", 2, "", true, true)

	// Sibling page frame navigation (dest=iframe) → in-tree, not the doc.
	rr = getPreviewWithHeaders(t, s, "/api/preview/"+token+"/page2.html", frameNav)
	if rr.Code != http.StatusOK {
		t.Fatalf("sibling doc GET: %d", rr.Code)
	}
	expectNav("after sibling nav", 3, "page2.html", true, false)

	// A non-HTML sibling as a document-dest request: it commits a document
	// frame-wise, but the record tracks HTML-family documents only (a CSS
	// "navigation" is a degenerate case the watchdog may contain).
	rr = getPreviewWithHeaders(t, s, "/api/preview/"+token+"/style.css", frameNav)
	if rr.Code != http.StatusOK {
		t.Fatalf("css GET: %d", rr.Code)
	}
	expectNav("after css nav (non-HTML)", 3, "page2.html", true, false)

	// Missing in-tree file: 404 commits no document → the record stays put
	// (the watchdog sees a stale record and contains the navigation).
	rr = getPreviewWithHeaders(t, s, "/api/preview/"+token+"/nope.html", frameNav)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing GET: %d", rr.Code)
	}
	expectNav("after 404 nav", 3, "page2.html", true, false)

	// Self-reference → isDoc=true (serves the stored content).
	rr = getPreviewWithHeaders(t, s, "/api/preview/"+token+"/page.html", frameNav)
	if rr.Code != http.StatusOK {
		t.Fatalf("self-ref GET: %d", rr.Code)
	}
	expectNav("after self-ref nav", 4, "page.html", true, true)
}

func TestPreviewNav_UnknownToken(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	req := testRequest("GET", "/api/preview-nav/nonexistent-token", nil)
	withAuth(req, cookie)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown token, got %d", rr.Code)
	}
}

// The watchdog route sits beside /api/preview/{token}/, so no name inside a previewed tree is reserved by
// it: a file called nav there is answered by the asset rules, not by the record.
func TestPreviewNavName_IsAnAssetNotTheRecord(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	if err := os.WriteFile(filepath.Join(dir, "nav"), []byte("NAV-ON-DISK"), 0o644); err != nil {
		t.Fatal(err)
	}
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>", "project": projectID, "path": "page.html",
	})

	rr := getPreview(t, s, "/api/preview/"+token+"/nav")
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("the asset route answers without a session; got the watchdog's 401")
	}
	if body := rr.Body.String(); strings.Contains(body, `"seq"`) {
		t.Fatalf("an asset request must not answer with the nav record: %s", body)
	}

	// And the record is readable, for the token, at its own path.
	rec := navState(t, s, cookie, token)
	if rec["seq"] == nil {
		t.Fatalf("nav record must carry a sequence: %v", rec)
	}
}
