package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

// testPassword is the password used in tests (matches LOGIN_PASSWORD env var).
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
	oldWT := os.Getenv("MIKE_WORKTREES_DIR")
	os.Setenv("MIKE_WORKTREES_DIR", worktreesDir)
	t.Cleanup(func() {
		if oldWT != "" {
			os.Setenv("MIKE_WORKTREES_DIR", oldWT)
		} else {
			os.Unsetenv("MIKE_WORKTREES_DIR")
		}
	})

	// Disable login rate limiting during tests — tests make many rapid login
	// attempts that would hit the default limit of 5/minute.
	// Skip if the caller already set MIKE_LOGIN_RATE_LIMIT (e.g., rate limit tests).
	if os.Getenv("MIKE_LOGIN_RATE_LIMIT") == "" {
		os.Setenv("MIKE_LOGIN_RATE_LIMIT", "0")
		t.Cleanup(func() { os.Unsetenv("MIKE_LOGIN_RATE_LIMIT") })
	}

	// Set env var for auth password
	oldPW := os.Getenv("MIKE_LOGIN_PASSWORD")
	os.Setenv("MIKE_LOGIN_PASSWORD", testPassword)
	t.Cleanup(func() {
		if oldPW != "" {
			os.Setenv("MIKE_LOGIN_PASSWORD", oldPW)
		} else {
			os.Unsetenv("MIKE_LOGIN_PASSWORD")
		}
	})

	// Disable sandbox startup cleanup to prevent removing containers from
	// parallel sandbox package tests (they manage their own cleanup via TestMain).
	os.Setenv("MIKE_SANDBOX_SKIP_STARTUP_CLEANUP", "1")

	// Allow sandbox to be disabled in tests (individual tests can override).
	os.Setenv("DANGER_ZONE", "1")

	// Set path to sandbox-bridge binary so tests can create sandbox containers.
	// Without this, GetAgentBinaryPath() searches relative to the test executable
	// (in /tmp/go-build/...) and never finds it.
	oldAgentPath := os.Getenv("MIKE_AGENT_PATH")
	// Use absolute path since test CWD is backend/server/, not project root.
	agentPath, _ := filepath.Abs("../../sandbox/sandbox-bridge")
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	t.Cleanup(func() {
		if oldAgentPath != "" {
			os.Setenv("MIKE_AGENT_PATH", oldAgentPath)
		} else {
			os.Unsetenv("MIKE_AGENT_PATH")
		}
	})

	s, err := NewServer(dbPath, worktreesDir, ":0", "", nil)
	if err != nil {
		t.Fatalf("create test server: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.TODO()) })
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
	handler := s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
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

func TestHandleFSReadGutterMarkers(t *testing.T) {
	s := testServer(t)

	// Create a git repo with a committed file
	tmpDir := t.TempDir()
	project := &types.Project{
		ID:   "test-project-gutter",
		Name: "Gutter Test Project",
		Path: tmpDir,
	}
	if err := s.store.CreateProject(project); err != nil {
		t.Fatal(err)
	}

	// Initialize git repo and commit a file
	mustRunGit(t, tmpDir, "init")
	mustRunGit(t, tmpDir, "config", "user.email", "test@test.com")
	mustRunGit(t, tmpDir, "config", "user.name", "Test")

	// Create and commit a file with known content
	originalContent := "line1\nline2\nline3\nline4\nline5\n"
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte(originalContent), 0644)
	mustRunGit(t, tmpDir, "add", "test.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "initial")

	cookie, csrfToken := loginTestCSRF(t, s)

	t.Run("modified file returns gutter markers", func(t *testing.T) {
		// Modify the file: change line 2, delete line 4, add a line at end
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

		// Line 2 was modified (line2 → line2-changed)
		if markers, ok := resp.GutterMarkers[2]; !ok || len(markers) == 0 {
			t.Errorf("expected marker on line 2, got %v", resp.GutterMarkers)
		} else if markers[0] != "modified" {
			t.Errorf("expected 'modified' on line 2, got %v", markers)
		}

		// Line 4 (was line5, now at position 4) — line 4 was removed,
		// so a "removed" marker should appear on line 3 (before the gap)
		// Line 5 (line6-new) is added
		if markers, ok := resp.GutterMarkers[5]; !ok || len(markers) == 0 {
			t.Errorf("expected marker on line 5 (added), got %v", resp.GutterMarkers)
		} else if markers[0] != "added" {
			t.Errorf("expected 'added' on line 5, got %v", markers)
		}
	})

	t.Run("unmodified file returns no markers", func(t *testing.T) {
		// Reset file to match HEAD
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
		// Create a file that is not tracked by git
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

	// --- Content search tests ---
	// Create files with known content for content search
	os.WriteFile(filepath.Join(tmpDir, "greeting.txt"), []byte("Hello world\nGoodbye world\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "app.ts"), []byte("const x = 42;\nfunction hello() { return 'hi'; }\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "src", "utils.ts"), []byte("// utility functions\nexport function format(s: string) { return s.trim(); }\n"), 0o644)
	// Binary file
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
		// Use url.QueryEscape to properly encode the regex pattern
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

	t.Run("succeeds with fallback identity", func(t *testing.T) {
		saved := saveEnv(gitEnvKeys...)
		defer restoreEnv(saved)
		for _, k := range gitEnvKeys {
			os.Unsetenv(k)
		}
		// Set standard git env vars as fallback
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

// TestDoneTaskDiffReturnsCommittedDiff verifies that the diff API for a done task
// returns the committed diff (BaseCommit..BranchTip) instead of a working-tree diff.
func TestDoneTaskDiffReturnsCommittedDiff(t *testing.T) {
	s := testServer(t)

	// Create a git repo in the temp dir
	dir := t.TempDir()
	exec.Command("git", "-C", dir, "init", "-b", "main").Run()
	exec.Command("git", "-C", dir, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run()

	// Create initial commit (this will be the base)
	os.WriteFile(join(dir, "initial.txt"), []byte("initial\n"), 0644)
	exec.Command("git", "-C", dir, "add", "initial.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "Initial commit").Run()
	baseCommit := strings.TrimSpace(execOutput(t, "git", "-C", dir, "rev-parse", "HEAD"))

	// Create a project pointing to this repo
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
		Status:     "done",
		Branch:     "task/test-done-diff",
		BaseRef:    "main",
		BaseCommit: baseCommit,
		BranchTip:  branchTip,
	}
	s.store.CreateTask(task)

	// Call the diff API without explicit head param
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

	// For done tasks, the response should contain a raw diff string (not a manifest)
	diff, ok := resp["diff"].(string)
	if !ok {
		t.Fatalf("expected 'diff' field (string) in response, got: %v", resp)
	}

	// The diff should contain the committed file
	if !strings.Contains(diff, "new-file.txt") {
		t.Fatalf("diff does not contain committed file 'new-file.txt':\n%s", diff)
	}
	if !strings.Contains(diff, "committed content") {
		t.Fatalf("diff does not contain committed content:\n%s", diff)
	}
}

// TestSandboxDisableBlockedWithoutDangerZone verifies that disabling sandbox
// returns 403 Forbidden when DANGER_ZONE is not set to "1".
func TestSandboxDisableBlockedWithoutDangerZone(t *testing.T) {
	s := testServer(t)

	// Ensure DANGER_ZONE is not set
	oldVal := os.Getenv("DANGER_ZONE")
	os.Unsetenv("DANGER_ZONE")
	t.Cleanup(func() {
		if oldVal != "" {
			os.Setenv("DANGER_ZONE", oldVal)
		} else {
			os.Unsetenv("DANGER_ZONE")
		}
	})

	// Create a project with sandbox enabled
	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	netIso := true
	proj := map[string]any{
		"name":                       "sandbox-test-proj",
		"path":                       dir,
		"sandbox_enabled":            true,
		"sandbox_network_isolation":  netIso,
	}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	projectID := result["id"].(string)

	// Try to disable sandbox — should be blocked
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
	s := testServer(t)

	// Set DANGER_ZONE=1
	oldVal := os.Getenv("DANGER_ZONE")
	os.Setenv("DANGER_ZONE", "1")
	t.Cleanup(func() {
		if oldVal != "" {
			os.Setenv("DANGER_ZONE", oldVal)
		} else {
			os.Unsetenv("DANGER_ZONE")
		}
	})

	// Create a project with sandbox enabled
	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	netIso := true
	proj := map[string]any{
		"name":                       "sandbox-test-proj-2",
		"path":                       dir,
		"sandbox_enabled":            true,
		"sandbox_network_isolation":  netIso,
	}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	projectID := result["id"].(string)

	// Disable sandbox — should succeed with DANGER_ZONE=1
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
	s := testServer(t)

	// Ensure DANGER_ZONE is not set
	oldVal := os.Getenv("DANGER_ZONE")
	os.Unsetenv("DANGER_ZONE")
	t.Cleanup(func() {
		if oldVal != "" {
			os.Setenv("DANGER_ZONE", oldVal)
		} else {
			os.Unsetenv("DANGER_ZONE")
		}
	})

	// Try to create a project with sandbox disabled — should be blocked
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
		"name":                       "sandbox-test-proj-4",
		"path":                       dir,
		"sandbox_enabled":            true,
		"sandbox_network_isolation":  netIso,
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
