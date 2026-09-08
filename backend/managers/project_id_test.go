package managers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

// "<repo>/sub/.." names the repository, and its base name is "..".
func TestCreateProjectFromAnEscapedPathKeepsWorktreesInsideTheWorktreesDir(t *testing.T) {
	h, _, home, _ := jailedProjectHandler(t)
	repo := filepath.Join(home, "repo")
	makeCommittedRepo(t, repo, 1)
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A raw string: filepath.Join would clean the traversal away.
	escaped := repo + "/sub/.."
	rr := httptest.NewRecorder()
	h.HandleCreate(rr, httptest.NewRequest(http.MethodPost, "/api/projects",
		strings.NewReader(string(repoProjectRequest(t, escaped)))))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create from %q = %d, want 201: %s", escaped, rr.Code, rr.Body.String())
	}
	var p types.Project
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}

	worktrees := filepath.Join(home, "worktrees")
	wt := filepath.Join(worktrees, p.ID, "frost-amber-quartz")
	if !strings.HasPrefix(wt, worktrees+string(filepath.Separator)) {
		t.Errorf("a task's worktree would be %s, outside the worktrees dir %s (project id %q)", wt, worktrees, p.ID)
	}
	if p.Path != repo {
		t.Errorf("stored path = %q, want the cleaned checkout %q", p.Path, repo)
	}
	// The name defaults to the base name of the same path, so it escapes with it.
	if p.Name != "repo" {
		t.Errorf("default name = %q, want %q", p.Name, "repo")
	}
}

// The rename endpoint reaches the same name rule as creation.
func TestProjectRenameToADirectoryThatIsNotOneIsRefused(t *testing.T) {
	h, s, home, _ := jailedProjectHandler(t)
	repo := filepath.Join(home, "repo")
	makeCommittedRepo(t, repo, 1)
	rr := httptest.NewRecorder()
	h.HandleCreate(rr, httptest.NewRequest(http.MethodPost, "/api/projects",
		strings.NewReader(string(repoProjectRequest(t, repo)))))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	var p types.Project
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{".", "/", "./"} {
		body, err := json.Marshal(types.UpdateProjectRequest{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(string(body)))
		req.SetPathValue("projectID", p.ID)
		rr := httptest.NewRecorder()
		h.HandleByID(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("rename to %q = %d, want 400: %s", name, rr.Code, rr.Body.String())
		}
	}
	stored, err := s.GetProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != p.Name {
		t.Errorf("a refused rename still renamed the project: %q -> %q", p.Name, stored.Name)
	}
}

func TestGenerateProjectIDNeverReturnsANameThatIsNotOne(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		existing []string
		want     string
	}{
		{"base name", "/home/u/projects/app", nil, "app"},
		{"collision slugs the whole path", "/home/u/projects/app", []string{"app"}, "home-u-projects-app"},
		{"escaped path is cleaned, not slugified", "/home/u/repo/sub/..", nil, "repo"},
		{"base name starting with a dash is slugged", "/home/u/repo/-dash", nil, "home-u-repo-dash"},
		{"a path with no letters or digits has no slug", "/..", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := generateProjectID(tc.path, tc.existing); got != tc.want {
				t.Errorf("generateProjectID(%q, %v) = %q, want %q", tc.path, tc.existing, got, tc.want)
			}
		})
	}
}
