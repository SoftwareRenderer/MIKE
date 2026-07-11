package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

// testPassword is the password used in tests (matches KANBAN_PASSWORD env var).
const testPassword = "test-password-1234"

// testServer creates a Server with a temporary database for testing.
// The temp directory is cleaned up by t.Cleanup.
func testServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	worktreesDir := filepath.Join(dir, "worktrees")
	os.MkdirAll(worktreesDir, 0o755)

	// Set env var for worktrees dir
	oldWT := os.Getenv("WORKTREES_DIR")
	os.Setenv("WORKTREES_DIR", worktreesDir)
	t.Cleanup(func() {
		if oldWT != "" {
			os.Setenv("WORKTREES_DIR", oldWT)
		} else {
			os.Unsetenv("WORKTREES_DIR")
		}
	})

	// Set env var for auth password
	oldPW := os.Getenv("KANBAN_PASSWORD")
	os.Setenv("KANBAN_PASSWORD", testPassword)
	t.Cleanup(func() {
		if oldPW != "" {
			os.Setenv("KANBAN_PASSWORD", oldPW)
		} else {
			os.Unsetenv("KANBAN_PASSWORD")
		}
	})

	// Disable sandbox startup cleanup to prevent removing containers from
	// parallel sandbox package tests (they manage their own cleanup via TestMain).
	os.Setenv("SANDBOX_SKIP_STARTUP_CLEANUP", "1")

	s, err := NewServer(dbPath, worktreesDir, ":0", "", nil)
	if err != nil {
		t.Fatalf("create test server: %v", err)
	}
	t.Cleanup(func() { s.Shutdown(nil) })
	return s
}

// testRequest creates an http.Request for testing.
func testRequest(method, path string, body any) *http.Request {
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

// testResponse runs a request against the server and returns the response.
// Uses the full middleware chain (CORS + Session + Compress).
func testResponse(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler := CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
	handler.ServeHTTP(rr, req)
	return rr
}

// loginTest logs in via the API and returns the session cookie string.
func loginTest(t *testing.T, s *Server) string {
	t.Helper()
	req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	return rr.Header().Get("Set-Cookie")
}

// loginTestCSRF logs in and returns the session cookie and CSRF token.
func loginTestCSRF(t *testing.T, s *Server) (cookie, csrfToken string) {
	t.Helper()
	req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	return rr.Header().Get("Set-Cookie"), rr.Header().Get("X-CSRF-Token")
}

// withAuth adds the session cookie to a request.
func withAuth(req *http.Request, cookie string) {
	parts := strings.Split(cookie, ";")[0]
	req.Header.Set("Cookie", strings.TrimSpace(parts))
}

// withAuthCSRF adds the session cookie and CSRF token to a request.
func withAuthCSRF(req *http.Request, cookie, csrfToken string) {
	withAuth(req, cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
}

// testResponseAuth logs in and runs a request against the server with auth.
// Uses the full middleware chain (CORS + Session + CSRF + Compress).
func testResponseAuth(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	cookie, csrfToken := loginTestCSRF(t, s)
	withAuthCSRF(req, cookie, csrfToken)
	rr := httptest.NewRecorder()
	handler := CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
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

// initGitRepo initializes a directory as a git repository.
func initGitRepo(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustRunGit(t, path, "init")
	mustRunGit(t, path, "config", "user.email", "test@test.com")
	mustRunGit(t, path, "config", "user.name", "Test")
	mustRunGit(t, path, "add", "README.md")
	mustRunGit(t, path, "commit", "-m", "Initial")
}

// mustRunGit runs a git command and fails the test on error.
func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", args, string(out), err)
	}
}

// mustRunGitOutput runs a git command and returns its stdout, failing on error.
func mustRunGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %s: %v", args, string(out), err)
	}
	return strings.TrimSpace(string(out))
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

// ensureGitRepo initializes a directory as a git repo.
func ensureGitRepo(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(path, ".git")); os.IsNotExist(err) {
		// Write a README and init git
		if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# Test\n"), 0o644); err != nil {
			t.Fatalf("write README: %v", err)
		}
		// Use git init
		cmd := exec.Command("git", "init")
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %s: %v", string(out), err)
		}
		cmd = exec.Command("git", "config", "user.email", "test@test.com")
		cmd.Dir = path
		cmd.Run()
		cmd = exec.Command("git", "config", "user.name", "Test")
		cmd.Dir = path
		cmd.Run()
		cmd = exec.Command("git", "add", "README.md")
		cmd.Dir = path
		cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "Initial")
		cmd.Dir = path
		cmd.Run()
	}
}

func TestHandleFSWrite(t *testing.T) {
	s := testServer(t)

	// Create a test project for FS write tests
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

		// Verify file was written
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

	// Create a test project
	tmpDir := t.TempDir()
	project := &types.Project{
		ID:   "test-project-fs",
		Name: "FS Test Project",
		Path: tmpDir,
	}
	if err := s.store.CreateProject(project); err != nil {
		t.Fatal(err)
	}
	// Create a file inside the project
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

func TestHandleFSSearch(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)

	// Create a test project with files
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "src", "components"), 0o755)
	os.MkdirAll(filepath.Join(tmpDir, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("readme"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.ts"), []byte("app"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "components", "Button.svelte"), []byte("button"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "utils.ts"), []byte("utils"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "node_modules", "pkg", "index.js"), []byte("pkg"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, ".hidden"), []byte("hidden"), 0o644)

	// Register project
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
		// Should have README.md, src/app.ts, src/utils.ts, src/components/Button.svelte (4 files)
		// Excludes: node_modules/*, .hidden
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

	t.Run("excludes hidden files", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/search?q=hidden&project="+projectID, nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]any](t, rr.Body)
		results := toAnySlice(result["results"])
		if len(results) != 0 {
			t.Fatalf("expected 0 results (.hidden excluded), got %d", len(results))
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

	t.Run("limits results to 10", func(t *testing.T) {
		// Create many files
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
		results := result["results"].([]any)
		if len(results) > 10 {
			t.Fatalf("expected at most 10 results, got %d", len(results))
		}
	})
}

func TestGitIdentityAvailable(t *testing.T) {
	// Save and restore env vars for standard git identity
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
		"KANBAN_GIT_AUTHOR_NAME", "KANBAN_GIT_AUTHOR_EMAIL",
	}

	t.Run("KANBAN env vars satisfy check", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		// Clear all git env vars
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}
		// Set KANBAN vars
		os.Setenv("KANBAN_GIT_AUTHOR_NAME", "Kanban User")
		os.Setenv("KANBAN_GIT_AUTHOR_EMAIL", "kanban@example.com")

		// gitIdentityAvailable uses git.GitAuthorName/Email (package-level, set at init)
		// and os.Getenv for standard git env vars. For this test we verify the
		// standard git env var path.
		name, email := resolveGitIdentity()
		if name == "" || email == "" {
			t.Errorf("expected non-empty identity from KANBAN env vars, got name=%q email=%q", name, email)
		}
	})

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
		// Name may come from git config if set on this system
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
		// Email may come from git config if set on this system
		_ = email
	})

	t.Run("no env vars falls back to git config", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}

		name, email := resolveGitIdentity()
		// Falls back to git config --global. If configured, should return values.
		// If not configured, returns empty strings.
		// This test just verifies the function doesn't panic.
		_ = name
		_ = email
	})
}

func TestHandleFSInitGit_EnvVars(t *testing.T) {
	s := testServer(t)

	// Save and restore env vars
	gitEnvKeys := []string{
		"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME",
		"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL",
		"KANBAN_GIT_AUTHOR_NAME", "KANBAN_GIT_AUTHOR_EMAIL",
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
		// Set standard git env vars (KANBAN vars are package-level, set at init)
		// The handler checks resolveGitIdentity() which reads GIT_* env vars at call time
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

		// Verify git repo was created
		if _, err := os.Stat(filepath.Join(tmpDir, ".git")); os.IsNotExist(err) {
			t.Fatal("expected .git directory to exist")
		}
	})

	t.Run("succeeds with KANBAN env vars", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}
		// KANBAN vars are read at package init time, so this tests the code path
		// where git.GitAuthorName/Email are already set. If they're empty, falls back
		// to GIT_* env vars or git config.
		// For this test, set standard git env vars as fallback
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

		// Should fail if git config is also not set
		if rr.Code == http.StatusOK {
			// This is acceptable if the test environment has git config set
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

	// Fetch git status
	req := testRequest("GET", "/api/projects/test-status-author/git/status", nil)
	cookie, csrfToken := loginTestCSRF(t, s)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("git status: %d %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)

	// Verify authorName and authorEmail are present
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
