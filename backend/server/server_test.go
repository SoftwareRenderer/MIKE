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
	handler := CORSMiddleware(s.SessionMiddleware(CompressMiddleware(s.mux)))
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

// withAuth adds the session cookie to a request.
func withAuth(req *http.Request, cookie string) {
	parts := strings.Split(cookie, ";")[0]
	req.Header.Set("Cookie", strings.TrimSpace(parts))
}

// testResponseAuth logs in and runs a request against the server with auth.
// Uses the full middleware chain (CORS + Session + Compress).
func testResponseAuth(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	cookie := loginTest(t, s)
	withAuth(req, cookie)
	rr := httptest.NewRecorder()
	handler := CORSMiddleware(s.SessionMiddleware(CompressMiddleware(s.mux)))
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
	cookie := loginTest(t, s)
	proj := map[string]interface{}{
		"name": "test-project",
		"path": dir,
	}
	req := testRequest("POST", "/api/projects", proj)
	withAuth(req, cookie)
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
	cookie := loginTest(t, s)
	task := map[string]interface{}{
		"prompt": prompt,
	}
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks", task)
	withAuth(req, cookie)
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
