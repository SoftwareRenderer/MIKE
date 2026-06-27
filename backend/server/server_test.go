package server

import (
	"bytes"
	"encoding/json"
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

	s, err := NewServer(dbPath, worktreesDir)
	if err != nil {
		t.Fatalf("create test server: %v", err)
	}
	t.Cleanup(func() { s.Shutdown(nil) })
	return s
}

// testRequest creates an http.Request for testing.
func testRequest(method, path string, body interface{}) *http.Request {
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

// createTestProject creates a project via the API and returns its ID.
// The project path is initialized as a git repo.
func createTestProject(t *testing.T, s *Server) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]interface{}{
		"name": "test-project",
		"path": dir,
	}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]interface{}](t, rr.Body)
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

// createTestTask creates a task via the API and returns its ID.
func createTestTask(t *testing.T, s *Server, projectID string, prompt string) string {
	t.Helper()
	cookie, csrfToken := loginTestCSRF(t, s)
	task := map[string]interface{}{
		"prompt": prompt,
	}
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks", task)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]interface{}](t, rr.Body)
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

	t.Run("writes file content", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "test.txt")
		content := "hello world\nline two\n"

		req := testRequest(http.MethodPost, "/api/fs/write", map[string]string{
			"path":    filePath,
			"content": content,
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		result := mustJSON[map[string]interface{}](t, rr.Body)
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
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "a", "b", "c", "test.txt")
		content := "nested content"

		req := testRequest(http.MethodPost, "/api/fs/write", map[string]string{
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
		req := testRequest(http.MethodPost, "/api/fs/write", map[string]string{
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

		req := testRequest(http.MethodGet, "/api/fs/write?path="+filePath, nil)
		cookie := loginTest(t, s)
		withAuth(req, cookie)
		rr := testResponse(t, s, req)

		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rr.Code)
		}
	})

	t.Run("rejects files larger than 1MB", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "large.txt")
		largeContent := strings.Repeat("x", 1_000_001)

		req := testRequest(http.MethodPost, "/api/fs/write", map[string]string{
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

	t.Run("allows access without project param", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/fs/read?path="+filepath.Join(tmpDir, "allowed.txt"), nil)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}
