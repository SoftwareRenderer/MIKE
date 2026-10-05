package server

import (
	"bytes"
	stdtls "crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"mike/backend/testutil/testgit"
	"mike/backend/tls"
	"mike/backend/types"

	"mike/backend/startup"
)

// testServer is the one place the suite builds a server, on a temporary database that t.Cleanup
// removes. It receives a resolved configuration, never an environment, so one test cannot change what
// another test's server does. mutate is how a test asks for a different decision (see testServerWith).
func testServer(t *testing.T) *Server {
	t.Helper()
	return testServerWith(t, nil)
}

// testServerWith builds a server with one test's change to the resolved configuration. The mutate hook
// is how a test pins a decision that would otherwise come from the environment: a refusal test asks
// for DangerZone=false, a jail test for AllowRootFS=false, a proxy test for TrustProxy=true.
func testServerWith(t *testing.T, mutate func(*startup.Config)) *Server {
	t.Helper()
	return testServerWithAssets(t, testAssets(), mutate)
}

// testAssets is the frontend a test server serves: a stamped document, one hashed asset, and the worker's
// own companion script, which a test asks for with no session to present.
func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html":             &fstest.MapFile{Data: []byte(`<!doctype html><html><head><script nonce="{{.Nonce}}"></script></head><body>board</body></html>`)},
		"assets/app-12345678.js": &fstest.MapFile{Data: []byte(`console.log("app")`)},
		"sw-notifications.js":    &fstest.MapFile{Data: []byte(`self.addEventListener("notificationclick", () => {})`)},
	}
}

// testServerWithAssets is testServerWith with the frontend under the test's control too.
func testServerWithAssets(t *testing.T, assets fs.FS, mutate func(*startup.Config)) *Server {
	t.Helper()
	dir := t.TempDir()
	worktreesDir := filepath.Join(dir, "worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatalf("create worktrees dir: %v", err)
	}

	// Absolute path, because the test CWD is backend/server/, not the project root: the bridge binary
	// sits beside the source tree, not beside the temporary test executable.
	agentPath, _ := filepath.Abs("../../sandbox/sandbox-bridge")

	cfg := startup.Config{
		Port:    0,
		Listen:  "127.0.0.1",
		Addr:    ":0",
		DataDir: dir,
		DBPath:  filepath.Join(dir, "test.db"),
		// Every fixture here is a t.TempDir() directory under /tmp, outside the jail, so the suite
		// runs widened; a test that pins the jailed behaviour asks for AllowRootFS=false.
		AllowRootFS:       true,
		DangerZone:        true,
		WorktreesOverride: worktreesDir,
		EncryptionKey:     "test-encryption-key",
		AgentPath:         agentPath,
		// Left false on purpose, and named rather than omitted: the boot sweep deletes every stale
		// kanban-* container family, which is a host-wide delete and not a test server's to run.
		SweepStaleContainersAtBoot: false,
		// Login rate limiting stays ON but effectively unlimited: 0 (and any unparsable value) is
		// no longer a way to disable it, and tests make many rapid login attempts that would hit the
		// 5/minute default. A rate-limit test lowers it deliberately.
		LoginRateLimit: 100000,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	s, err := NewServer(cfg, testTLSConfig(t, dir), assets)
	if err != nil {
		t.Fatalf("create test server: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return s
}

// testTLSConfig is the certificate pair main.go builds, generated into the test's own data directory.
// A server is never wired without one, so no test can read a nil config as "serve plainly".
func testTLSConfig(t *testing.T, dataDir string) *tls.Config {
	t.Helper()
	cfg, err := tls.LoadWithCert(dataDir, "", "")
	if err != nil {
		t.Fatalf("test TLS material: %v", err)
	}
	return cfg
}

// secureRequest marks a request as arrived over TLS, which is how a browser reaches this server. The
// chain redirects a request that did not, so a test that wants the redirect says so with plainRequest.
func secureRequest(req *http.Request) *http.Request {
	req.TLS = &stdtls.ConnectionState{}
	return req
}

// plainRequest is the one request the chain is asked to answer without TLS: the redirect is what it gets.
func plainRequest(method, target string, body any) *http.Request {
	return rawRequest(method, target, body)
}

// testRequest creates an http.Request for testing, on the connection a browser uses — the suite's
// default, because this server has no plain-HTTP answer to give.
func testRequest(method, path string, body any) *http.Request {
	return secureRequest(rawRequest(method, path, body))
}

func rawRequest(method, path string, body any) *http.Request {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// testResponse runs a request through the whole middleware chain in front of the API mux
// (CORS + Session + CSRF + Compress) — the answer a browser gets. routerResponse is the same chain
// in front of the router, for the cases where the SPA fallback is what is under test.
func testResponse(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler := s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
	handler.ServeHTTP(rr, req)
	return rr
}

// loginTest opens a session and returns the cookie string. It goes through the session manager rather
// than the HTTP ceremony: a passkey is a browser's credential, and a test about some other route has no
// business driving one. The ceremony itself is what handler_passkey_test.go exercises.
func loginTest(t *testing.T, s *Server) string {
	t.Helper()
	cookie, _ := loginTestCSRF(t, s)
	return cookie
}

// loginTestCSRF opens a session and returns the session cookie and the CSRF token that was stored
// beside it — the two things a browser is handed when a ceremony finishes.
func loginTestCSRF(t *testing.T, s *Server) (cookie, csrfToken string) {
	t.Helper()
	_, token, csrfToken, err := s.sessionMgr.StartSession(t.Context(), "10.0.0.1", "test harness")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	return sessionCookieName + "=" + token, csrfToken
}

// withAuth adds the session cookie to a request and hands it back, so a request can be built inside the
// expression that runs it.
func withAuth(req *http.Request, cookie string) *http.Request {
	parts := strings.Split(cookie, ";")[0]
	req.Header.Set("Cookie", strings.TrimSpace(parts))
	return req
}

// withAuthCSRF adds the session cookie and CSRF token to a request and hands it back.
func withAuthCSRF(req *http.Request, cookie, csrfToken string) *http.Request {
	withAuth(req, cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	return req
}

// testResponseAuth logs in and runs a request against the server with auth.
// Uses the full middleware chain (CORS + Session + CSRF + Compress).
func testResponseAuth(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	cookie, csrfToken := loginTestCSRF(t, s)
	withAuthCSRF(req, cookie, csrfToken)
	rr := httptest.NewRecorder()
	handler := s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
	handler.ServeHTTP(rr, req)
	return rr
}

// mustJSON decodes a response body into a destination struct.
func mustJSON[T any](t *testing.T, body io.Reader) T {
	t.Helper()
	var dest T
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&dest); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return dest
}

// toAnySlice converts a nil or []any interface to []any safely.
func toAnySlice(v any) []any {
	if v == nil {
		return []any{}
	}
	return v.([]any)
}

// createTestProject creates a project via the API and returns its ID.
// The project path is initialized as a git repo.
func createTestProject(t *testing.T, s *Server) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{
		"name": "test-project",
		"path": dir,
	}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	return result["id"].(string)
}

// initGitRepo initializes a directory as a git repository with one committed file.
// The subject is "Initial": divergence_baseline_test.go identifies the root commit by it.
func initGitRepo(t *testing.T, path string) {
	t.Helper()
	testgit.RepoAtMessage(t, path, "Initial")
}

// createTestTask creates a task via the API and returns its ID.
func createTestTask(t *testing.T, s *Server, projectID string, prompt string) string {
	t.Helper()
	cookie, csrfToken := loginTestCSRF(t, s)
	task := map[string]any{
		"prompt": prompt,
	}
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks", task)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	return result["id"].(string)
}

func TestHandleFSWrite(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	project := &types.Project{
		ID:   "test-project-write",
		Name: "FS Write Test",
		Path: tmpDir,
	}
	if err := s.store.CreateProject(project); err != nil {
		t.Fatal(err)
	}

	t.Run("writes file content", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "test.txt")
		content := "hello world\nline two\n"

		req := testRequest(http.MethodPost, "/api/fs/write?project=test-project-write", map[string]string{
			"path":    filePath,
			"content": content,
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		if result["status"] != "written" {
			t.Fatalf("expected status=written, got %v", result["status"])
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if string(data) != content {
			t.Fatalf("expected %q, got %q", content, string(data))
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "a", "b", "c", "test.txt")
		content := "nested content"

		req := testRequest(http.MethodPost, "/api/fs/write?project=test-project-write", map[string]string{
			"path":    filePath,
			"content": content,
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if string(data) != content {
			t.Fatalf("expected %q, got %q", content, string(data))
		}
	})

	t.Run("rejects missing path", func(t *testing.T) {
		req := testRequest(http.MethodPost, "/api/fs/write?project=test-project-write", map[string]string{
			"path":    "",
			"content": "test",
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("rejects method other than POST", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "test.txt")

		req := testRequest(http.MethodGet, "/api/fs/write?path="+filePath+"&project=test-project-write", nil)
		cookie := loginTest(t, s)
		withAuth(req, cookie)
		rr := testResponse(t, s, req)

		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rr.Code)
		}
	})

	t.Run("rejects files larger than 1MB", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "large.txt")
		largeContent := strings.Repeat("x", 1_000_001)

		req := testRequest(http.MethodPost, "/api/fs/write?project=test-project-write", map[string]string{
			"path":    filePath,
			"content": largeContent,
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)

		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestHandleFSPathValidation(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	project := &types.Project{
		ID:   "test-project-fs",
		Name: "FS Test Project",
		Path: tmpDir,
	}
	if err := s.store.CreateProject(project); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(tmpDir, "allowed.txt"), []byte("ok"), 0644)

	cookie, csrfToken := loginTestCSRF(t, s)

	t.Run("allows file within project root", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/read?path="+filepath.Join(tmpDir, "allowed.txt")+"&project=test-project-fs", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("rejects file outside project root", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/read?path=/etc/passwd&project=test-project-fs", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("rejects path traversal via ..", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/read?path="+tmpDir+"/../../../etc/passwd&project=test-project-fs", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("allows write within project root", func(t *testing.T) {
		req := testRequest(http.MethodPost, "/api/fs/write?project=test-project-fs", map[string]string{
			"path":    filepath.Join(tmpDir, "new-file.txt"),
			"content": "hello",
		})
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("rejects write outside project root", func(t *testing.T) {
		req := testRequest(http.MethodPost, "/api/fs/write?project=test-project-fs", map[string]string{
			"path":    "/tmp/escape.txt",
			"content": "escaped",
		})
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("allows fs listing within project root", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs?path="+tmpDir+"&files=1&project=test-project-fs", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("rejects fs listing outside project root", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs?path=/tmp&files=1&project=test-project-fs", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("requires project param", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/read?path="+filepath.Join(tmpDir, "allowed.txt"), nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestHandleFSReadGutterMarkers(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	project := &types.Project{
		ID:   "test-project-gutter",
		Name: "Gutter Test Project",
		Path: tmpDir,
	}
	if err := s.store.CreateProject(project); err != nil {
		t.Fatal(err)
	}

	testgit.Git(t, tmpDir, "init")
	testgit.Git(t, tmpDir, "config", "user.email", "test@test.com")
	testgit.Git(t, tmpDir, "config", "user.name", "Test")

	originalContent := "line1\nline2\nline3\nline4\nline5\n"
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte(originalContent), 0644)
	testgit.Git(t, tmpDir, "add", "test.txt")
	testgit.Git(t, tmpDir, "commit", "-m", "initial")

	cookie, csrfToken := loginTestCSRF(t, s)

	t.Run("modified file returns gutter markers", func(t *testing.T) {
		// Change line 2, delete line 4, append a line.
		modifiedContent := "line1\nline2-changed\nline3\nline5\nline6-new\n"
		os.WriteFile(testFile, []byte(modifiedContent), 0644)

		req := testRequest(http.MethodGet, "/api/fs/read?path="+testFile+"&project=test-project-gutter", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp fsReadResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp.GutterMarkers == nil {
			t.Fatal("expected gutter markers, got nil")
		}

		if markers, ok := resp.GutterMarkers[2]; !ok || len(markers) == 0 {
			t.Errorf("expected marker on line 2, got %v", resp.GutterMarkers)
		} else if markers[0] != "modified" {
			t.Errorf("expected 'modified' on line 2, got %v", markers)
		}

		// Line 4 was deleted, so its "removed" marker sits on line 3, before the gap.
		if markers, ok := resp.GutterMarkers[5]; !ok || len(markers) == 0 {
			t.Errorf("expected marker on line 5 (added), got %v", resp.GutterMarkers)
		} else if markers[0] != "added" {
			t.Errorf("expected 'added' on line 5, got %v", markers)
		}
	})

	t.Run("unmodified file returns no markers", func(t *testing.T) {
		os.WriteFile(testFile, []byte(originalContent), 0644)

		req := testRequest(http.MethodGet, "/api/fs/read?path="+testFile+"&project=test-project-gutter", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp fsReadResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp.GutterMarkers != nil {
			t.Errorf("expected no markers for unmodified file, got %v", resp.GutterMarkers)
		}
	})

	t.Run("untracked file returns no markers", func(t *testing.T) {
		untrackedFile := filepath.Join(tmpDir, "untracked.txt")
		os.WriteFile(untrackedFile, []byte("new content\n"), 0644)

		req := testRequest(http.MethodGet, "/api/fs/read?path="+untrackedFile+"&project=test-project-gutter", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp fsReadResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp.GutterMarkers != nil {
			t.Errorf("expected no markers for untracked file, got %v", resp.GutterMarkers)
		}
	})

	t.Run("ref parameter returns no gutter markers", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/read?path="+testFile+"&project=test-project-gutter&ref=HEAD", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp fsReadResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp.Content != originalContent {
			t.Errorf("expected original content, got %q", resp.Content)
		}
		if resp.GutterMarkers != nil {
			t.Errorf("expected no markers with ref param, got %v", resp.GutterMarkers)
		}
	})
}

func TestHandleFSSearch(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)

	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "src", "components"), 0o755)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("readme"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.ts"), []byte("app"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "components", "Button.svelte"), []byte("button"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "utils.ts"), []byte("utils"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "node_modules", "pkg", "index.js"), []byte("pkg"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, ".hidden"), []byte("hidden"), 0o644)

	projectID := "test-search-project"
	if err := s.store.CreateProject(&types.Project{ID: projectID, Name: "Test Search", Path: tmpDir}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	t.Run("searches files by name", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=app&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := result["results"].([]any)
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		first := results[0].(map[string]any)
		if first["path"] != "src/app.ts" {
			t.Fatalf("expected src/app.ts, got %v", first["path"])
		}
	})

	t.Run("searches files by path segment", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=src&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := result["results"].([]any)
		if len(results) < 2 {
			t.Fatalf("expected at least 2 results, got %d", len(results))
		}
	})

	t.Run("returns all files with empty query", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := result["results"].([]any)
		// Every file under the root except those under node_modules and every dot-prefixed name
		if len(results) != 4 {
			paths := make([]string, len(results))
			for i, r := range results {
				paths[i] = r.(map[string]any)["path"].(string)
			}
			t.Fatalf("expected 4 results, got %d: %v", len(results), paths)
		}
	})

	t.Run("excludes ignored directories", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=pkg&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := toAnySlice(result["results"])
		if len(results) != 0 {
			t.Fatalf("expected 0 results (node_modules excluded), got %d", len(results))
		}
	})

	t.Run("excludes dot-prefixed files", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=hidden&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := toAnySlice(result["results"])
		if len(results) != 0 {
			t.Fatalf("expected 0 results (.hidden is never searched), got %d", len(results))
		}
	})

	t.Run("rejects missing project parameter", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=test", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("rejects path traversal via project param", func(t *testing.T) {
		// Try to search a nonexistent project that would resolve outside allowed path
		req := testRequest(http.MethodGet, "/api/fs/search?q=test&project=../../etc", nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("filename search lists more than ten files", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			filename := filepath.Join(tmpDir, fmt.Sprintf("file%02d.txt", i))
			os.WriteFile(filename, []byte(fmt.Sprintf("content %d", i)), 0o644)
		}
		req := testRequest(http.MethodGet, "/api/fs/search?q=file&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		if results := result["results"].([]any); len(results) != 20 {
			t.Fatalf("filename search is capped below searchLimit: got %d of 20 files", len(results))
		}
	})

	t.Run("content search stops at the ceiling inside one file", func(t *testing.T) {
		// The ceiling has to stop the scan of one file, not just the walk, or a single file answers twice
		// what the request asked for.
		var many strings.Builder
		for i := 0; i < 2*searchLimit; i++ {
			many.WriteString("needle line\n")
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "many.txt"), []byte(many.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		req := testRequest(http.MethodGet, "/api/fs/search?q=needle&content=1&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		if results := result["results"].([]any); len(results) != searchLimit {
			t.Fatalf("expected exactly %d results, got %d", searchLimit, len(results))
		}
		// The panel cannot tell 1000 matches from 1000+ by row count, so a stopped walk has to say so.
		if result["truncated"] != true {
			t.Errorf("expected truncated=true when the ceiling stopped the walk, got %v", result["truncated"])
		}
	})

	t.Run("says the answer is complete when the walk ran to the end", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=README&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		if len(result["results"].([]any)) == 0 {
			t.Fatal("the fixture's README.md was not found")
		}
		if result["truncated"] != false {
			t.Errorf("expected truncated=false for an answer the ceiling did not reach, got %v", result["truncated"])
		}
	})

	// Content search.
	os.WriteFile(filepath.Join(tmpDir, "greeting.txt"), []byte("Hello world\nGoodbye world\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.ts"), []byte("const x = 42;\nfunction hello() { return 'hi'; }\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "utils.ts"), []byte("// utility functions\nexport function format(s: string) { return s.trim(); }\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "binary.dat"), []byte("hello\x00world\x00data"), 0o644)

	t.Run("content search finds matching lines", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=hello&content=1&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := result["results"].([]any)
		// Should find "Hello world" in greeting.txt and "function hello()" in app.ts
		found := false
		for _, r := range results {
			m := r.(map[string]any)
			if m["path"] == "src/app.ts" {
				if m["line"].(float64) != 2 {
					t.Fatalf("expected line 2, got %v", m["line"])
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("expected to find match in src/app.ts, got: %v", results)
		}
	})

	t.Run("content search with case sensitivity", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=Hello&case=1&content=1&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := result["results"].([]any)
		// Only "Hello world" in greeting.txt line 1 should match (not "hello()" in app.ts)
		var foundGreeting, foundApp bool
		for _, r := range results {
			m := r.(map[string]any)
			if m["path"] == "greeting.txt" {
				foundGreeting = true
			}
			if m["path"] == "src/app.ts" {
				foundApp = true
			}
		}
		if !foundGreeting {
			t.Fatal("expected match in greeting.txt")
		}
		if foundApp {
			t.Fatal("expected NO match in src/app.ts (case sensitive)")
		}
	})

	t.Run("content search with regex", func(t *testing.T) {
		q := url.QueryEscape(`export\s+function`)
		req := testRequest(http.MethodGet, "/api/fs/search?q="+q+"&regex=1&content=1&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := result["results"].([]any)
		found := false
		for _, r := range results {
			m := r.(map[string]any)
			if m["path"] == "src/utils.ts" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected regex match in src/utils.ts, got: %v", results)
		}
	})

	t.Run("content search with whole word", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=hi&word=1&content=1&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := result["results"].([]any)
		// "hi" should match in app.ts (as 'hi') but not in "utility" in utils.ts
		found := false
		for _, r := range results {
			m := r.(map[string]any)
			if m["path"] == "src/app.ts" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected whole-word match for 'hi' in src/app.ts, got: %v", results)
		}
	})

	t.Run("binary file skipped for content search but matched by filename", func(t *testing.T) {
		// Content search for "hello" should NOT match inside binary.dat
		req := testRequest(http.MethodGet, "/api/fs/search?q=hello&content=1&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := result["results"].([]any)
		for _, r := range results {
			m := r.(map[string]any)
			if m["path"] == "binary.dat" {
				t.Fatalf("expected binary.dat to be skipped in content search, but found: %v", m)
			}
		}
		// But filename search should still find it
		req2 := testRequest(http.MethodGet, "/api/fs/search?q=binary&project="+projectID, nil)
		withAuthCSRF(req2, cookie, csrfToken)
		rr2 := testResponse(t, s, req2)
		if rr2.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr2.Code, rr2.Body.String())
		}
		result2 := mustJSON[map[string]any](t, rr2.Body)
		results2 := result2["results"].([]any)
		found := false
		for _, r := range results2 {
			m := r.(map[string]any)
			if m["path"] == "binary.dat" {
				found = true
			}
		}
		if !found {
			t.Fatal("expected binary.dat in filename search")
		}
	})

	t.Run("invalid regex returns error", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=[invalid&regex=1&content=1&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestGitIdentityAvailable(t *testing.T) {
	saveEnv := func(keys ...string) map[string]string {
		orig := make(map[string]string)
		for _, k := range keys {
			orig[k] = os.Getenv(k)
		}
		return orig
	}
	restoreEnv := func(saved map[string]string) {
		for k, v := range saved {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}

	gitEnvKeys := []string{
		"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME",
		"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL",
	}

	t.Run("standard git env vars satisfy check", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}
		os.Setenv("GIT_AUTHOR_NAME", "Git User")
		os.Setenv("GIT_AUTHOR_EMAIL", "git@example.com")

		name, email := resolveGitIdentity()
		if name != "Git User" {
			t.Errorf("expected name=\"Git User\", got %q", name)
		}
		if email != "git@example.com" {
			t.Errorf("expected email=\"git@example.com\", got %q", email)
		}
	})

	t.Run("GIT_COMMITTER takes precedence over GIT_AUTHOR", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}
		os.Setenv("GIT_AUTHOR_NAME", "Author")
		os.Setenv("GIT_COMMITTER_NAME", "Committer")
		os.Setenv("GIT_AUTHOR_EMAIL", "author@example.com")
		os.Setenv("GIT_COMMITTER_EMAIL", "committer@example.com")

		name, email := resolveGitIdentity()
		if name != "Committer" {
			t.Errorf("expected name=\"Committer\", got %q", name)
		}
		if email != "committer@example.com" {
			t.Errorf("expected email=\"committer@example.com\", got %q", email)
		}
	})

	t.Run("partial identity — only email from env, name from git config", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}
		os.Setenv("GIT_AUTHOR_EMAIL", "only-email@example.com")

		name, email := resolveGitIdentity()
		// Email comes from env var, name falls back to git config (or empty)
		if email != "only-email@example.com" {
			t.Errorf("expected email=\"only-email@example.com\", got %q", email)
		}
		_ = name
	})

	t.Run("partial identity — only name from env, email from git config", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}
		os.Setenv("GIT_AUTHOR_NAME", "Only Name")

		name, email := resolveGitIdentity()
		if name != "Only Name" {
			t.Errorf("expected name=\"Only Name\", got %q", name)
		}
		_ = email
	})

	t.Run("no env vars falls back to git config", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}

		name, email := resolveGitIdentity()
		// Asserts only that the git-config fallback does not panic: the answer depends
		// on the machine's global config.
		_ = name
		_ = email
	})
}

func TestHandleFSInitGit_EnvVars(t *testing.T) {
	s := testServer(t)

	gitEnvKeys := []string{
		"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME",
		"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL",
	}
	saveEnv := func(keys ...string) map[string]string {
		orig := make(map[string]string)
		for _, k := range keys {
			orig[k] = os.Getenv(k)
		}
		return orig
	}
	restoreEnv := func(saved map[string]string) {
		for k, v := range saved {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}

	t.Run("succeeds with git env vars even without git config", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}
		// resolveGitIdentity reads GIT_* at call time, so env vars stand in for a
		// machine-level git config.
		os.Setenv("GIT_AUTHOR_NAME", "Test Author")
		os.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")

		tmpDir := t.TempDir()
		req := testRequest(http.MethodPost, "/api/fs/init-git", map[string]string{
			"path": tmpDir,
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		if _, err := os.Stat(filepath.Join(tmpDir, ".git")); os.IsNotExist(err) {
			t.Fatal("expected .git directory to exist")
		}
	})

	t.Run("succeeds with fallback identity", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}
		os.Setenv("GIT_COMMITTER_NAME", "Kanban Bot")
		os.Setenv("GIT_COMMITTER_EMAIL", "bot@kanban.local")

		tmpDir := t.TempDir()
		req := testRequest(http.MethodPost, "/api/fs/init-git", map[string]string{
			"path": tmpDir,
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("rejects when no identity available", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}

		tmpDir := t.TempDir()
		req := testRequest(http.MethodPost, "/api/fs/init-git", map[string]string{
			"path": tmpDir,
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)

		if rr.Code == http.StatusOK {
			// A machine-level git config makes the negative case impossible here.
			t.Log("git config was available, skipping negative test")
			return
		}
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "user.name") && !strings.Contains(rr.Body.String(), "user.email") {
			t.Fatalf("expected identity error, got: %s", rr.Body.String())
		}
	})
}

func TestHandleStatus_IncludesAuthorInfo(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	project := &types.Project{
		ID:   "test-status-author",
		Name: "Status Author Test",
		Path: tmpDir,
	}
	s.store.CreateProject(project)

	// Initialize a git repo with specific author config
	initGitRepo(t, tmpDir)

	req := testRequest("GET", "/api/projects/test-status-author/git/status", nil)
	cookie, csrfToken := loginTestCSRF(t, s)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("git status: %d %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)

	authorName, okName := result["authorName"].(string)
	authorEmail, okEmail := result["authorEmail"].(string)

	if !okName || authorName == "" {
		t.Fatalf("expected author_name in response, got: %v", result["author_name"])
	}
	if !okEmail || authorEmail == "" {
		t.Fatalf("expected author_email in response, got: %v", result["author_email"])
	}

	if authorName != "Test" {
		t.Fatalf("expected author_name 'Test', got '%s'", authorName)
	}
	if authorEmail != "test@test.com" {
		t.Fatalf("expected author_email 'test@test.com', got '%s'", authorEmail)
	}
}

// TestDoneTaskDiffReturnsCommittedDiff verifies that the diff API for a done task
// returns the committed diff (BaseCommit..BranchTip) instead of a working-tree diff.
func TestDoneTaskDiffReturnsCommittedDiff(t *testing.T) {
	s := testServer(t)

	dir := t.TempDir()
	exec.Command("git", "-C", dir, "init", "-b", "main").Run()
	exec.Command("git", "-C", dir, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()

	// The initial commit is the task's base.
	os.WriteFile(join(dir, "initial.txt"), []byte("initial\n"), 0644)
	exec.Command("git", "-C", dir, "add", "initial.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "Initial commit").Run()
	baseCommit := strings.TrimSpace(execOutput(t, "git", "-C", dir, "rev-parse", "HEAD"))

	proj := &types.Project{
		ID:   "diff-test-proj",
		Name: "diff-test",
		Path: dir,
	}
	s.store.CreateProject(proj)

	// Create a task branch with a commit
	exec.Command("git", "-C", dir, "checkout", "-b", "task/test-done-diff").Run()
	os.WriteFile(join(dir, "new-file.txt"), []byte("committed content\n"), 0644)
	exec.Command("git", "-C", dir, "add", "new-file.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "Add new file").Run()
	branchTip := strings.TrimSpace(execOutput(t, "git", "-C", dir, "rev-parse", "HEAD"))

	// Create a done task with base_commit and branch_tip
	task := &types.Task{
		ID:         "done-diff-task",
		ProjectID:  proj.ID,
		Title:      "Done diff test",
		Prompt:     "Test done task diff",
		Status:     types.StatusDone,
		Branch:     "task/test-done-diff",
		BaseRef:    "main",
		BaseCommit: baseCommit,
		BranchTip:  branchTip,
	}
	s.store.CreateTask(task)

	// For done tasks, the backend should automatically use BranchTip
	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest(http.MethodGet, "/api/projects/diff-test-proj/tasks/done-diff-task/diff", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("diff API returned %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)

	// A done task answers with a raw diff string, not a manifest.
	diff, ok := resp["diff"].(string)
	if !ok {
		t.Fatalf("expected 'diff' field (string) in response, got: %v", resp)
	}

	if !strings.Contains(diff, "new-file.txt") {
		t.Fatalf("diff does not contain committed file 'new-file.txt':\n%s", diff)
	}
	if !strings.Contains(diff, "committed content") {
		t.Fatalf("diff does not contain committed content:\n%s", diff)
	}
}

// TestDoneTaskDiffServedFromSnapshot verifies that the canonical done-task
// diff is persisted as a snapshot on first fetch and replayed from the
// snapshot on subsequent fetches — even when git is no longer usable
// (e.g. the repo or worktree is gone).
func TestDoneTaskDiffServedFromSnapshot(t *testing.T) {
	s := testServer(t)

	dir := t.TempDir()
	exec.Command("git", "-C", dir, "init", "-b", "main").Run()
	exec.Command("git", "-C", dir, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()

	os.WriteFile(join(dir, "initial.txt"), []byte("initial\n"), 0644)
	exec.Command("git", "-C", dir, "add", "initial.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "Initial commit").Run()
	baseCommit := strings.TrimSpace(execOutput(t, "git", "-C", dir, "rev-parse", "HEAD"))

	proj := &types.Project{
		ID:   "diff-snap-proj",
		Name: "diff-snap",
		Path: dir,
	}
	s.store.CreateProject(proj)

	exec.Command("git", "-C", dir, "checkout", "-b", "task/test-done-snap").Run()
	os.WriteFile(join(dir, "new-file.txt"), []byte("committed content\n"), 0644)
	exec.Command("git", "-C", dir, "add", "new-file.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "Add new file").Run()
	branchTip := strings.TrimSpace(execOutput(t, "git", "-C", dir, "rev-parse", "HEAD"))

	task := &types.Task{
		ID:         "done-snap-task",
		ProjectID:  proj.ID,
		Title:      "Done snapshot test",
		Prompt:     "Test done task diff snapshot",
		Status:     types.StatusDone,
		Branch:     "task/test-done-snap",
		BaseRef:    "main",
		BaseCommit: baseCommit,
		BranchTip:  branchTip,
	}
	s.store.CreateTask(task)

	cookie, csrfToken := loginTestCSRF(t, s)
	diffRequest := func() *httptest.ResponseRecorder {
		req := testRequest(http.MethodGet, "/api/projects/diff-snap-proj/tasks/done-snap-task/diff", nil)
		withAuthCSRF(req, cookie, csrfToken)
		return testResponse(t, s, req)
	}

	// First fetch materializes the snapshot.
	first := diffRequest()
	if first.Code != http.StatusOK {
		t.Fatalf("first diff fetch returned %d: %s", first.Code, first.Body.String())
	}
	snap, err := s.store.GetTaskDiffSnapshot("done-snap-task")
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if snap == nil {
		t.Fatal("expected a persisted snapshot after the first fetch")
	}
	if snap.BaseCommit != baseCommit || snap.BranchTip != branchTip {
		t.Fatalf("unexpected snapshot refs: %+v", snap)
	}

	// Break git — the second fetch must be served from the snapshot.
	if err := os.Rename(join(dir, ".git"), join(dir, ".git.bak")); err != nil {
		t.Fatalf("rename .git: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(join(dir, ".git.bak"), join(dir, ".git")) })

	second := diffRequest()
	if second.Code != http.StatusOK {
		t.Fatalf("second diff fetch returned %d: %s", second.Code, second.Body.String())
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("snapshot replay differs from the live response:\nfirst: %s\nsecond: %s", first.Body.String(), second.Body.String())
	}
}

// TestDoneTaskDiffSnapshotBypassedForFullAndCustomRefs verifies that full
// (untruncated) requests and custom ref comparisons still use the live git
// path and never serve the truncated snapshot.
func TestDoneTaskDiffSnapshotBypassedForFullAndCustomRefs(t *testing.T) {
	s := testServer(t)

	dir := t.TempDir()
	exec.Command("git", "-C", dir, "init", "-b", "main").Run()
	exec.Command("git", "-C", dir, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()

	os.WriteFile(join(dir, "initial.txt"), []byte("initial\n"), 0644)
	exec.Command("git", "-C", dir, "add", "initial.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "Initial commit").Run()
	baseCommit := strings.TrimSpace(execOutput(t, "git", "-C", dir, "rev-parse", "HEAD"))

	proj := &types.Project{
		ID:   "diff-bypass-proj",
		Name: "diff-bypass",
		Path: dir,
	}
	s.store.CreateProject(proj)

	exec.Command("git", "-C", dir, "checkout", "-b", "task/test-done-bypass").Run()
	os.WriteFile(join(dir, "new-file.txt"), []byte("line1\nline2\n"), 0644)
	exec.Command("git", "-C", dir, "add", "new-file.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "Add new file").Run()
	branchTip := strings.TrimSpace(execOutput(t, "git", "-C", dir, "rev-parse", "HEAD"))

	task := &types.Task{
		ID:         "done-bypass-task",
		ProjectID:  proj.ID,
		Title:      "Done bypass test",
		Prompt:     "Test snapshot bypass",
		Status:     types.StatusDone,
		Branch:     "task/test-done-bypass",
		BaseRef:    "main",
		BaseCommit: baseCommit,
		BranchTip:  branchTip,
	}
	s.store.CreateTask(task)

	cookie, csrfToken := loginTestCSRF(t, s)

	// full=1 must go through the live path even for a done task.
	req := testRequest(http.MethodGet, "/api/projects/diff-bypass-proj/tasks/done-bypass-task/diff?full=1&head="+branchTip, nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("full diff fetch returned %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if d, _ := resp["diff"].(string); !strings.Contains(d, "line2") {
		t.Fatalf("full=1 response missing diff content: %v", resp)
	}

	// Materialize the snapshot (canonical fetch), then break git: the
	// canonical request must still succeed from the snapshot while a custom
	// ref comparison (live path) must fail — proving the bypass guards.
	req = testRequest(http.MethodGet, "/api/projects/diff-bypass-proj/tasks/done-bypass-task/diff?head="+branchTip, nil)
	withAuthCSRF(req, cookie, csrfToken)
	if rr = testResponse(t, s, req); rr.Code != http.StatusOK {
		t.Fatalf("canonical diff fetch returned %d: %s", rr.Code, rr.Body.String())
	}
	if snap, err := s.store.GetTaskDiffSnapshot("done-bypass-task"); err != nil || snap == nil {
		t.Fatalf("expected a persisted snapshot (err=%v snap=%+v)", err, snap)
	}

	if err := os.Rename(join(dir, ".git"), join(dir, ".git.bak")); err != nil {
		t.Fatalf("rename .git: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(join(dir, ".git.bak"), join(dir, ".git")) })

	req = testRequest(http.MethodGet, "/api/projects/diff-bypass-proj/tasks/done-bypass-task/diff?head="+branchTip, nil)
	withAuthCSRF(req, cookie, csrfToken)
	if rr = testResponse(t, s, req); rr.Code != http.StatusOK {
		t.Fatalf("canonical diff from snapshot returned %d: %s", rr.Code, rr.Body.String())
	}

	req = testRequest(http.MethodGet, "/api/projects/diff-bypass-proj/tasks/done-bypass-task/diff?base=main&head="+branchTip, nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr = testResponse(t, s, req)
	if rr.Code == http.StatusOK {
		t.Fatalf("expected custom-base diff to fail without git (snapshot bypass), got 200: %s", rr.Body.String())
	}
}

// TestSandboxDisableBlockedWithoutDangerZone verifies that disabling sandbox
// returns 403 Forbidden when DANGER_ZONE is not set to "1".
func TestSandboxDisableBlockedWithoutDangerZone(t *testing.T) {
	// Without the grant, switching a project to unsandboxed is refused. The grant is the resolved
	// DANGER_ZONE decision the server was built with, so this is the process being pinned.
	s := testServerWith(t, func(c *startup.Config) { c.DangerZone = false })

	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	netIso := true
	proj := map[string]any{
		"name":                      "sandbox-test-proj",
		"path":                      dir,
		"sandbox_enabled":           true,
		"sandbox_network_isolation": netIso,
	}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	projectID := result["id"].(string)

	update := map[string]any{
		"sandbox_enabled": false,
	}
	req = testRequest("PUT", "/api/projects/"+projectID, update)
	withAuthCSRF(req, cookie, csrfToken)
	rr = testResponse(t, s, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d: %s", rr.Code, rr.Body.String())
	}
	var errResp map[string]any
	json.NewDecoder(rr.Body).Decode(&errResp)
	errMsg, _ := errResp["error"].(string)
	if !strings.Contains(errMsg, "DANGER_ZONE") {
		t.Fatalf("expected error mentioning DANGER_ZONE, got: %s", errMsg)
	}
}

// TestSandboxDisableAllowedWithDangerZone verifies that disabling sandbox
// succeeds when DANGER_ZONE is set to "1".
func TestSandboxDisableAllowedWithDangerZone(t *testing.T) {
	// The grant, stated as the decision the server was built with rather than as a variable this
	// process happens to carry.
	s := testServerWith(t, func(c *startup.Config) { c.DangerZone = true })

	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	netIso := true
	proj := map[string]any{
		"name":                      "sandbox-test-proj-2",
		"path":                      dir,
		"sandbox_enabled":           true,
		"sandbox_network_isolation": netIso,
	}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	projectID := result["id"].(string)

	update := map[string]any{
		"sandbox_enabled": false,
	}
	req = testRequest("PUT", "/api/projects/"+projectID, update)
	withAuthCSRF(req, cookie, csrfToken)
	rr = testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]any](t, rr.Body)
	sandboxEnabled, _ := result["sandbox_enabled"].(bool)
	if sandboxEnabled {
		t.Fatal("expected sandbox_enabled to be false")
	}
}

// TestSandboxCreateDisabledBlockedWithoutDangerZone verifies that creating a
// project with sandbox disabled returns 403 when DANGER_ZONE is not set.
func TestSandboxCreateDisabledBlockedWithoutDangerZone(t *testing.T) {
	// Creating a project with sandboxing disabled is refused without the grant.
	s := testServerWith(t, func(c *startup.Config) { c.DangerZone = false })

	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{
		"name":            "sandbox-test-proj-3",
		"path":            dir,
		"sandbox_enabled": false,
	}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d: %s", rr.Code, rr.Body.String())
	}
	var errResp map[string]any
	json.NewDecoder(rr.Body).Decode(&errResp)
	errMsg, _ := errResp["error"].(string)
	if !strings.Contains(errMsg, "DANGER_ZONE") {
		t.Fatalf("expected error mentioning DANGER_ZONE, got: %s", errMsg)
	}

	// Creating with sandbox enabled should always work
	netIso := true
	proj = map[string]any{
		"name":                      "sandbox-test-proj-4",
		"path":                      dir,
		"sandbox_enabled":           true,
		"sandbox_network_isolation": netIso,
	}
	req = testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr = testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project with sandbox enabled: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

// execOutput runs a command and returns its stdout, failing the test on error.
func execOutput(t *testing.T, cmd string, args ...string) string {
	t.Helper()
	c := exec.Command(cmd, args...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", cmd, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func join(parts ...string) string {
	return filepath.Join(parts...)
}

func TestRedirectHTTPSMiddleware_IgnoresXForwardedProtoByDefault(t *testing.T) {
	// No proxy trust: a client-written X-Forwarded-Proto is not evidence.
	s := &Server{tlsConfig: &tls.Config{CertFile: "cert", KeyFile: "key"}}
	var passed bool
	h := s.RedirectHTTPSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = true
		w.WriteHeader(http.StatusOK)
	}))

	// Attacker spoofs X-Forwarded-Proto to claim HTTPS; with proxy trust off it
	// must still be redirected to HTTPS, not passed through.
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if passed {
		t.Fatal("request with spoofed X-Forwarded-Proto was passed through; expected HTTPS redirect")
	}
	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("expected 301 redirect, got %d", rr.Code)
	}
}

func TestRedirectHTTPSMiddleware_TrustsXForwardedProtoWhenProxyEnabled(t *testing.T) {
	// The resolved MIKE_TRUST_PROXY decision is a field, not an environment lookup.
	s := &Server{tlsConfig: &tls.Config{CertFile: "cert", KeyFile: "key"}, settings: startup.Config{TrustProxy: true}}
	var passed bool
	h := s.RedirectHTTPSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !passed {
		t.Fatal("request with X-Forwarded-Proto=https (proxy enabled) should pass through")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}
