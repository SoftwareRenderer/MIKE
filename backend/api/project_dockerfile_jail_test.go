package api

// The build reads the stored Dockerfile path itself, so the jail applies where the value is
// accepted — the rule types.ValidateCommitHash follows for the same reason.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/fsutil"
	"mike/backend/store"
	"mike/backend/types"
)

func jailedDockerfileProject(t *testing.T) (h *ProjectHandler, s *store.Store, id, inside, outside string) {
	t.Helper()
	var home string
	h, s, home, outside = jailedProjectHandler(t)
	inside = filepath.Join(home, "checkout")
	if err := s.CreateProject(&types.Project{ID: "df-proj", Name: "df-proj", Path: inside}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return h, s, "df-proj", inside, outside
}

func putDockerfile(t *testing.T, h *ProjectHandler, id, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}", h.HandleByID)
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+id,
		strings.NewReader(`{"sandbox_dockerfile_path":"`+path+`"}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestUpdateProjectJailsTheSandboxDockerfile(t *testing.T) {
	h, s, id, inside, outside := jailedDockerfileProject(t)
	const previous = "/home/operator/kept/Dockerfile"
	if err := s.UpdateProjectSandboxDockerfilePath(id, previous); err != nil {
		t.Fatal(err)
	}

	// Outside every root: refused, and the project keeps what it had.
	rr := putDockerfile(t, h, id, filepath.Join(outside, "Dockerfile"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("PUT a dockerfile outside the roots: status = %d, want 403 (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), fsutil.AllowRootFSEnv) {
		t.Errorf("refusal must name %s, got %s", fsutil.AllowRootFSEnv, rr.Body.String())
	}
	if p, err := s.GetProject(id); err != nil || p.SandboxDockerfilePath != previous {
		t.Errorf("a refused PUT wrote the path: stored=%q err=%v", p.SandboxDockerfilePath, err)
	}

	// Inside the roots, where the picker starts: stored.
	if rr = putDockerfile(t, h, id, filepath.Join(inside, "Dockerfile")); rr.Code != http.StatusOK {
		t.Fatalf("PUT a dockerfile inside the roots: status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	p, err := s.GetProject(id)
	if err != nil || p.SandboxDockerfilePath != filepath.Join(inside, "Dockerfile") {
		t.Errorf("the dockerfile path was not stored: %q err=%v", p.SandboxDockerfilePath, err)
	}

	// Clearing the setting stays possible.
	if rr = putDockerfile(t, h, id, ""); rr.Code != http.StatusOK {
		t.Fatalf("PUT an empty dockerfile path: status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if p, err := s.GetProject(id); err != nil || p.SandboxDockerfilePath != "" {
		t.Errorf("clearing the setting left %q (err=%v)", p.SandboxDockerfilePath, err)
	}

	// Widened: the same request goes through.
	widenHostRoot(h)
	if rr = putDockerfile(t, h, id, filepath.Join(outside, "Dockerfile")); rr.Code != http.StatusOK {
		t.Errorf("widened PUT outside the roots: status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
}

func TestCreateProjectJailsTheSandboxDockerfile(t *testing.T) {
	h, s, home, outside := jailedProjectHandler(t)
	repo := filepath.Join(home, "repo")
	createBareRepo(t, repo)

	body := func(dockerfile string) string {
		return `{"path":"` + repo + `","sandbox_enabled":true,"sandbox_network_isolation":false,` +
			`"sandbox_dockerfile_path":"` + dockerfile + `"}`
	}
	create := func(dockerfile string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.HandleCreate(rr, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body(dockerfile))))
		return rr
	}

	if rr := create(filepath.Join(outside, "Dockerfile")); rr.Code != http.StatusForbidden {
		t.Fatalf("create with a dockerfile outside the roots: status = %d, want 403 (%s)", rr.Code, rr.Body.String())
	}
	if projects, err := s.ListProjects(); err != nil || len(projects) != 0 {
		t.Errorf("a refused create stored %d project rows (err=%v)", len(projects), err)
	}

	if rr := create(filepath.Join(repo, "Dockerfile")); rr.Code != http.StatusCreated {
		t.Fatalf("create with a dockerfile inside the roots: status = %d, want 201 (%s)", rr.Code, rr.Body.String())
	}
	projects, err := s.ListProjects()
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects = %d (err=%v), want the one created inside the roots", len(projects), err)
	}
	if want := filepath.Join(repo, "Dockerfile"); projects[0].SandboxDockerfilePath != want {
		t.Errorf("stored dockerfile = %q, want %q", projects[0].SandboxDockerfilePath, want)
	}
}
