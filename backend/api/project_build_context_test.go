package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mike/backend/types"
)

// The build context is a path inside a commit, so the field is checked as a repository path on both
// writes. A value naming a directory on this machine has to be refused where it is typed, not discovered
// at build time when the person is looking at a build stream.
func TestProjectWritesValidateTheBuildContextAsARepositoryPath(t *testing.T) {
	h, _ := testProjectHandler(t)
	repo := t.TempDir()
	createBareRepo(t, repo)
	isolation := true

	for _, bad := range []string{"/etc", "../secrets", ".git", "~", "$HOME", "C:\\build", "/", "a/../b"} {
		_, code, msg := h.createProjectFromRequest(types.CreateProjectRequest{
			Path: repo, Name: "ctx-reject-proj", SandboxEnabled: true,
			SandboxNetworkIsolation: &isolation, SandboxBuildContext: bad,
		})
		if code != http.StatusBadRequest {
			t.Errorf("create with context %q: status %d (%s), want %d", bad, code, msg, http.StatusBadRequest)
		}
		if !strings.Contains(msg, "sandbox_build_context") {
			t.Errorf("create with context %q: message %q does not name the field", bad, msg)
		}
	}

	created, code, msg := h.createProjectFromRequest(types.CreateProjectRequest{
		Path: repo, Name: "ctx-ok-proj", SandboxEnabled: true,
		SandboxNetworkIsolation: &isolation, SandboxBuildContext: "services/api",
	})
	if code != http.StatusCreated {
		t.Fatalf("status = %d (%s), want %d", code, msg, http.StatusCreated)
	}
	if created.SandboxBuildContext != "services/api" {
		t.Fatalf("stored context = %q, want the subtree the request named", created.SandboxBuildContext)
	}

	// A spelling of the same subtree is stored as one path, because the build's cache key is derived from
	// it and `services/api/` building separately from `services/api` would be a surprise.
	spelling, code, msg := h.createProjectFromRequest(types.CreateProjectRequest{
		Path: secondRepo(t), Name: "ctx-spelling-proj", SandboxEnabled: true,
		SandboxNetworkIsolation: &isolation, SandboxBuildContext: "./services/api/",
	})
	if code != http.StatusCreated {
		t.Fatalf("status = %d (%s), want %d", code, msg, http.StatusCreated)
	}
	if spelling.SandboxBuildContext != "services/api" {
		t.Fatalf("stored context = %q, want the cleaned path", spelling.SandboxBuildContext)
	}
}

// secondRepo is a checkout distinct from the first: the project table keys on the path.
func secondRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	createBareRepo(t, repo)
	return repo
}

// A rejected context leaves the row exactly as it was, including the context it already held.
func TestUpdateProjectRejectsABuildContextOutsideTheRepository(t *testing.T) {
	h, projID := testProjectHandler(t)
	if err := h.store.UpdateProjectSandboxBuildContext(projID, "services/api"); err != nil {
		t.Fatalf("seed context: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}", h.HandleByID)

	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+projID, strings.NewReader(`{"sandbox_build_context":"/home/operator"}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "sandbox_build_context") {
		t.Fatalf("body = %q, want it to name the field", rr.Body.String())
	}

	got, err := h.store.GetProject(projID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxBuildContext != "services/api" {
		t.Fatalf("context = %q after a refused write, want the stored subtree", got.SandboxBuildContext)
	}

	// Clearing it is a legal write: empty means the directory holding the Dockerfile.
	req = httptest.NewRequest(http.MethodPut, "/api/projects/"+projID, strings.NewReader(`{"sandbox_build_context":""}`))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("clearing the context: status = %d body = %q", rr.Code, rr.Body.String())
	}
	got, _ = h.store.GetProject(projID)
	if got.SandboxBuildContext != "" {
		t.Fatalf("context = %q after clearing it, want empty", got.SandboxBuildContext)
	}
}
