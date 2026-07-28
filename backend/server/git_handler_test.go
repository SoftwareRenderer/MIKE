package server

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// createTestProjectWithPath creates a project and returns its ID and filesystem path.
func createTestProjectWithPath(t *testing.T, s *Server) (string, string) {
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
	return result["id"].(string), dir
}

func authenticatedRequest(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler := s.CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))
	handler.ServeHTTP(rr, req)
	return rr
}

func TestGitLogBasic(t *testing.T) {
	s := testServer(t)
	projID, dir := createTestProjectWithPath(t, s)
	// Add a second commit
	mustRunGit(t, dir, "commit", "--allow-empty", "-m", "Second commit")

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("GET", "/api/projects/"+projID+"/git/log", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := authenticatedRequest(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	commits := mustJSON[[]map[string]any](t, rr.Body)
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	// First entry should be the most recent commit
	if !strings.Contains(commits[0]["subject"].(string), "Second commit") {
		t.Fatalf("unexpected subject: %s", commits[0]["subject"])
	}
}

func TestGitLogFull(t *testing.T) {
	s := testServer(t)
	projID, dir := createTestProjectWithPath(t, s)
	// Add more commits
	mustRunGit(t, dir, "commit", "--allow-empty", "-m", "Second commit")
	mustRunGit(t, dir, "commit", "--allow-empty", "-m", "Third commit")

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("GET", "/api/projects/"+projID+"/git/log?full=1", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := authenticatedRequest(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	resp := mustJSON[map[string]any](t, rr.Body)
	commitsAny := resp["commits"].([]any)
	commits := make([]map[string]any, len(commitsAny))
	for i, c := range commitsAny {
		commits[i] = c.(map[string]any)
	}

	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}

	// Check wrapper fields
	if resp["current_branch"] == nil || resp["current_branch"].(string) == "" {
		t.Fatal("expected current_branch field in response")
	}

	// Check first commit has full fields
	first := commits[0]
	if first["hash"] == "" || first["hash"] == nil {
		t.Fatal("expected hash field in full log")
	}
	if first["short"] == "" || first["short"] == nil {
		t.Fatal("expected short field in full log")
	}
	if first["subject"] == "" || first["subject"] == nil {
		t.Fatal("expected subject field in full log")
	}
	if first["date"] == "" || first["date"] == nil {
		t.Fatal("expected date field in full log")
	}
	// Parents - the first commit (most recent) has one parent
	parents := first["parents"]
	if parents != nil {
		parentSlice := parents.([]any)
		if len(parentSlice) != 1 {
			t.Fatalf("expected 1 parent, got %d", len(parentSlice))
		}
	}

	// Check last commit (oldest) has no parents
	last := commits[len(commits)-1]
	parents = last["parents"]
	if parents != nil {
		parentSlice := parents.([]any)
		if len(parentSlice) != 0 {
			t.Fatalf("expected 0 parents for root commit, got %d", len(parentSlice))
		}
	}
}

func TestGitLogFullWithBranch(t *testing.T) {
	s := testServer(t)
	projID, dir := createTestProjectWithPath(t, s)
	mustRunGit(t, dir, "commit", "--allow-empty", "-m", "On main")
	// Create a feature branch
	mustRunGit(t, dir, "checkout", "-b", "feature")
	mustRunGit(t, dir, "commit", "--allow-empty", "-m", "On feature")

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("GET", "/api/projects/"+projID+"/git/log?full=1&branch=feature", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := authenticatedRequest(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	resp := mustJSON[map[string]any](t, rr.Body)
	commitsAny := resp["commits"].([]any)
	commits := make([]map[string]any, len(commitsAny))
	for i, c := range commitsAny {
		commits[i] = c.(map[string]any)
	}
	if len(commits) != 3 {
		t.Fatalf("expected 3 commits on feature branch, got %d", len(commits))
	}
	if !strings.Contains(commits[0]["subject"].(string), "On feature") {
		t.Fatalf("unexpected subject: %s", commits[0]["subject"])
	}
	// current_branch should be "feature" (the branch checked out in the test repo)
	if resp["current_branch"].(string) != "feature" {
		t.Fatalf("expected current_branch 'feature', got '%s'", resp["current_branch"])
	}
}

func TestGitLogNotFound(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)

	req := testRequest("GET", "/api/projects/nonexistent/git/log?full=1", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := authenticatedRequest(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGitCheckout(t *testing.T) {
	s := testServer(t)
	projID, dir := createTestProjectWithPath(t, s)
	// Create a develop branch from master
	mustRunGit(t, dir, "checkout", "-b", "develop")

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("POST", "/api/projects/"+projID+"/git/checkout", map[string]any{
		"branch": "master",
	})
	withAuthCSRF(req, cookie, csrfToken)
	rr := authenticatedRequest(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify checkout worked
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse: %s: %v", string(out), err)
	}
	branch := strings.TrimSpace(string(out))
	if branch != "master" {
		t.Fatalf("expected branch 'master', got '%s'", branch)
	}
}

func TestGitLogFullDecodesParents(t *testing.T) {
	s := testServer(t)
	projID, dir := createTestProjectWithPath(t, s)
	mustRunGit(t, dir, "commit", "--allow-empty", "-m", "Commit A")
	mustRunGit(t, dir, "commit", "--allow-empty", "-m", "Commit B")

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("GET", "/api/projects/"+projID+"/git/log?full=1", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := authenticatedRequest(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Decode as wrapper response
	resp := mustJSON[map[string]any](t, rr.Body)
	commitsAny := resp["commits"].([]any)
	commits := make([]map[string]any, len(commitsAny))
	for i, c := range commitsAny {
		commits[i] = c.(map[string]any)
	}

	// Most recent commit should have 1 parent
	if len(commits) < 2 {
		t.Fatalf("expected at least 2 commits, got %d", len(commits))
	}
	parents := commits[0]["parents"]
	if parents == nil {
		t.Fatal("expected parents field on non-root commit")
	}
	parentSlice := parents.([]any)
	if len(parentSlice) != 1 {
		t.Fatalf("expected 1 parent, got %d", len(parentSlice))
	}
	// Parent hash should match the second commit's hash
	if parentSlice[0] != commits[1]["hash"] {
		t.Fatalf("parent hash mismatch: %s != %s", parentSlice[0], commits[1]["hash"])
	}
}

func TestGitLogFullBranchesAndOrigin(t *testing.T) {
	s := testServer(t)
	projID, dir := createTestProjectWithPath(t, s)

	// Add commits on master
	mustRunGit(t, dir, "commit", "--allow-empty", "-m", "Commit A")
	mustRunGit(t, dir, "commit", "--allow-empty", "-m", "Commit B")

	// Create a branch pointing at Commit A
	cmd := exec.Command("git", "rev-parse", "HEAD~1")
	cmd.Dir = dir
	aHashOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse: %s: %v", string(aHashOut), err)
	}
	mustRunGit(t, dir, "branch", "release", strings.TrimSpace(string(aHashOut)))

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("GET", "/api/projects/"+projID+"/git/log?full=1", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := authenticatedRequest(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	resp := mustJSON[map[string]any](t, rr.Body)
	commitsAny := resp["commits"].([]any)
	commits := make([]map[string]any, len(commitsAny))
	for i, c := range commitsAny {
		commits[i] = c.(map[string]any)
	}

	// current_branch should be "master"
	if resp["current_branch"].(string) != "master" {
		t.Fatalf("expected current_branch 'master', got '%s'", resp["current_branch"])
	}

	// origin_tip should be empty (no remote configured)
	if resp["origin_tip"] != nil && resp["origin_tip"].(string) != "" {
		t.Fatalf("expected empty origin_tip, got '%s'", resp["origin_tip"])
	}

	// Find the commit with the "release" branch
	found := false
	for _, c := range commits {
		branches, ok := c["branches"]
		if !ok {
			continue
		}
		branchSlice := branches.([]any)
		for _, b := range branchSlice {
			if b.(string) == "release" {
				found = true
				// This should be Commit A (the older one)
				if !strings.Contains(c["subject"].(string), "Commit A") {
					t.Fatalf("release branch should point to 'Commit A', got subject: %s", c["subject"])
				}
			}
		}
	}
	if !found {
		t.Fatal("expected to find 'release' branch in commit branches")
	}

	// Find the commit with "master" branch (most recent = Commit B)
	masterFound := false
	for _, c := range commits {
		branches, ok := c["branches"]
		if !ok {
			continue
		}
		branchSlice := branches.([]any)
		for _, b := range branchSlice {
			if b.(string) == "master" {
				masterFound = true
				if !strings.Contains(c["subject"].(string), "Commit B") {
					t.Fatalf("master branch should point to 'Commit B', got subject: %s", c["subject"])
				}
			}
		}
	}
	if !masterFound {
		t.Fatal("expected to find 'master' branch in commit branches")
	}
}
