package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- helpers ---------------------------------------------------------------

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
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://localhost"+target, nil)
	rr := httptest.NewRecorder()
	handler := s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
	handler.ServeHTTP(rr, req)
	return rr
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

func TestPreviewServe_RequiresSession(t *testing.T) {
	s := testServer(t)
	req := testRequest("POST", "/api/preview", map[string]any{"content": "<html></html>"})
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", rr.Code)
	}
}

func TestPreviewServe_RequiresCSRF(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginTestCSRF(t, s)
	req := testRequest("POST", "/api/preview", map[string]any{"content": "<html></html>"})
	withAuth(req, cookie) // session cookie but no CSRF header
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF, got %d", rr.Code)
	}
}

func TestPreviewServe_RejectsNonPOST(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("PUT", "/api/preview", map[string]any{"content": "<html></html>"})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for PUT, got %d", rr.Code)
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
	if len(token) < 32 {
		t.Fatalf("token too short to be a 128-bit bearer: %q", token)
	}

	rr := getPreview(t, s, "/api/preview/"+token+"/")
	if rr.Code != http.StatusOK {
		t.Fatalf("doc GET: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html…", ct)
	}
	csp := rr.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src * 'unsafe-inline'", "style-src * 'unsafe-inline'", "object-src 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q:\n%s", want, csp)
		}
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

func TestPreviewDoc_NoTrailingSlashRedirects(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{"content": "<html></html>"})

	rr := getPreview(t, s, "/api/preview/"+token)
	if rr.Code != http.StatusMovedPermanently && rr.Code != http.StatusFound {
		t.Fatalf("expected redirect for no-trailing-slash doc URL, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/api/preview/"+token+"/" {
		t.Errorf("Location = %q, want /api/preview/%s/", loc, token)
	}
	final := getPreviewFollowing(t, s, "/api/preview/"+token)
	if final.Code != http.StatusOK {
		t.Fatalf("after redirect: expected 200, got %d", final.Code)
	}
}

func TestPreviewDoc_UnknownToken(t *testing.T) {
	s := testServer(t)
	rr := getPreview(t, s, "/api/preview/nonexistent-token/")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown token, got %d", rr.Code)
	}
}

// --- sibling assets -----------------------------------------------------------

func TestPreviewServesSiblingAsset(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body { color: red; }"), 0o644); err != nil {
		t.Fatal(err)
	}

	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>",
		"project": projectID,
		"path":    "page.html",
	})

	rr := getPreview(t, s, "/api/preview/"+token+"/style.css")
	if rr.Code != http.StatusOK {
		t.Fatalf("asset GET: expected 200, got %d %s", rr.Code, rr.Body.String())
	}
	wantMime := mime.TypeByExtension(".css")
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, wantMime) {
		t.Errorf("asset Content-Type = %q, want prefix %q", ct, wantMime)
	}
	if body := rr.Body.String(); body != "body { color: red; }" {
		t.Errorf("asset body = %q", body)
	}
}

func TestPreviewAsset_Subdirectory(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	projectID, dir := previewTestProject(t, s)
	sub := filepath.Join(dir, "assets")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": "<html></html>",
		"project": projectID,
		"path":    "page.html",
	})

	rr := getPreview(t, s, "/api/preview/"+token+"/assets/app.js")
	if rr.Code != http.StatusOK {
		t.Fatalf("subdir asset GET: expected 200, got %d %s", rr.Code, rr.Body.String())
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
		t.Skipf("symlinks unsupported: %v", err)
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

func TestPreviewTTL_SweepExpiresIdleDocs(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{"content": "<html></html>"})

	// Age the doc beyond the TTL and sweep.
	s.previewStore.mu.Lock()
	doc := s.previewStore.docs[token]
	if doc == nil {
		t.Fatal("doc not in store")
	}
	doc.lastAccess = time.Now().Add(-previewTTL - time.Minute)
	s.previewStore.mu.Unlock()
	s.previewStore.sweep(time.Now())

	rr := getPreview(t, s, "/api/preview/"+token+"/")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after TTL sweep, got %d", rr.Code)
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

func TestPreviewDelete_RequiresSession(t *testing.T) {
	s := testServer(t)
	req := testRequest("DELETE", "/api/preview/some-token", nil)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated DELETE, got %d", rr.Code)
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

func TestResolveFSFilePath_ProjectRoot(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	projectID := createTestProjectInDir(t, s, dir)

	got, _, err := s.resolveFSFilePath(projectID, "", "nested/page.html")
	if err != nil {
		t.Fatalf("resolveFSFilePath: %v", err)
	}
	want := filepath.Join(filepath.Clean(dir), "nested")
	if filepath.Clean(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveFSFilePath_AbsolutePath(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	projectID := createTestProjectInDir(t, s, dir)

	got, _, err := s.resolveFSFilePath(projectID, "", filepath.Join(dir, "page.html"))
	if err != nil {
		t.Fatalf("resolveFSFilePath: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(dir) {
		t.Fatalf("got %q, want %q", got, dir)
	}
}

func TestResolveFSFilePath_OutsideProject(t *testing.T) {
	s := testServer(t)
	projectID, _ := previewTestProject(t, s)
	if _, _, err := s.resolveFSFilePath(projectID, "", "../elsewhere/page.html"); err == nil {
		t.Fatal("expected error for path outside project")
	}
	if _, _, err := s.resolveFSFilePath(projectID, "", "/etc/passwd"); err == nil {
		t.Fatal("expected error for absolute path outside project")
	}
}

func TestResolveFSFilePath_MissingInputs(t *testing.T) {
	s := testServer(t)
	if _, _, err := s.resolveFSFilePath("", "", "page.html"); err == nil {
		t.Fatal("expected error for empty project")
	}
	if _, _, err := s.resolveFSFilePath("some-id", "", ""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

// Self-reference by file name → served content (possibly unsaved), not disk.
func TestPreviewSelfReference_ServesServedContent(t *testing.T) {
	s := testServer(t)
	projectID, dir := previewTestProject(t, s)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("ON-DISK"), 0o644)
	cookie, csrfToken := loginTestCSRF(t, s)
	content := "<html><body>EDITED</body></html>"
	token, _ := servePreview(t, s, cookie, csrfToken, map[string]any{
		"content": content, "project": projectID, "path": "index.html",
	})
	rr := getPreview(t, s, "/api/preview/"+token+"/index.html")
	if rr.Code != http.StatusOK {
		t.Fatalf("self-ref GET: %d %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); body != content {
		t.Errorf("body = %q, want served content %q", body, content)
	}
	if csp := rr.Header().Get("Content-Security-Policy"); csp != previewDocCSP {
		t.Errorf("self-ref doc must carry the preview CSP, got %q", csp)
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

// sanity: the JSON body decodes into the expected request shape
func TestPreviewServeRequestShape(t *testing.T) {
	var req previewServeRequest
	body := []byte(`{"content":"<i>hi</i>","project":"p1","path":"a/b.html","task":"t1"}`)
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&req); err != nil {
		t.Fatal(err)
	}
	if req.Content != "<i>hi</i>" || req.Project != "p1" || req.Path != "a/b.html" || req.Task != "t1" {
		t.Fatalf("unexpected decode: %+v", req)
	}
	if _, err := io.Copy(io.Discard, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
}
