package api

// Import is where a repository's submodules are fetched, and it must never be the thing that stops a project
// existing. These fixtures drive a real git with a real local remote.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// recordSubmodule makes a checkout that names the repository at upstream as a submodule at subPath and never
// populates it — the state a clone arrives in. The gitlink is recorded by hand rather than with `git submodule
// add`, which would populate it: `git add -A` prunes the gitlink again, the directory not existing on disk.
func recordSubmodule(t *testing.T, dir, upstream, subPath string) {
	t.Helper()
	testgit.RepoAt(t, dir)
	testgit.Git(t, dir, "config", "-f", ".gitmodules", "submodule.mods.path", subPath)
	testgit.Git(t, dir, "config", "-f", ".gitmodules", "submodule.mods.url", upstream)
	testgit.Git(t, dir, "add", ".gitmodules")
	testgit.Git(t, dir, "commit", "-m", "gitmodules")
	testgit.Git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+testgit.GitOutput(t, upstream, "rev-parse", "HEAD")+","+subPath)
	testgit.Git(t, dir, "commit", "-m", "record submodule")
}

// TestHandleCreateImportsDespiteAnUnfetchableSubmodule: a submodule whose upstream has gone is reported and
// skipped, and the project is still created — an unrelated repository does not get to decide a person cannot
// use MIKE with this one. The task that needs that submodule is what stops, and it names it.
func TestHandleCreateImportsDespiteAnUnfetchableSubmodule(t *testing.T) {
	h, _ := testProjectHandlerGranted(t)
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "up")
	makeCommittedRepo(t, upstream, 1)
	project := filepath.Join(tmp, "super")
	recordSubmodule(t, project, upstream, "vendor/lib")
	if err := os.RemoveAll(upstream); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(types.CreateProjectRequest{Path: project})
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.HandleCreate(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("an unreachable submodule must not fail the import: %d %s", rr.Code, rr.Body.String())
	}
	var proj types.Project
	if err := json.Unmarshal(rr.Body.Bytes(), &proj); err != nil {
		t.Fatal(err)
	}
	if proj.Path != project {
		t.Errorf("project path = %q, want %q", proj.Path, project)
	}
	if _, err := os.Stat(filepath.Join(project, "vendor", "lib", "data.txt")); err == nil {
		t.Error("the unreachable submodule's files were fetched after all")
	}
}
