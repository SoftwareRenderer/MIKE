package api

// A development project has no home elsewhere: the directory the server created for it under the
// extensions tree *is* its repository, so the project's deletion is what removes it. No other
// project's directory is ever removed this way — for those, the directory is the person's own work.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mike/backend/extensions"
	"mike/backend/store"
	"mike/backend/types"
)

func seedProjectAt(t *testing.T, s *store.Store, id, path string) {
	t.Helper()
	if err := s.CreateProject(&types.Project{ID: id, Name: id, Path: path, SandboxEnabled: true}); err != nil {
		t.Fatalf("create project %s: %v", id, err)
	}
}

func writeFileTree(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"display_name":"x","version":"0.1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeletingAProjectRemovesOnlyADevelopmentRepository(t *testing.T) {
	h, _, s := testProjectHandlerWithStore(t)
	extRoot := filepath.Join(t.TempDir(), "extensions")
	repo := filepath.Join(extRoot, extensions.DevNamespace, "my-ext")
	installed := filepath.Join(extRoot, "acme", "pack")
	home := filepath.Join(t.TempDir(), "work-in-progress")
	writeFileTree(t, repo)
	writeFileTree(t, installed)
	writeFileTree(t, home)

	extH := extensions.NewExtensionHandler(nil, nil, nil, &extensions.ExtensionConfig{ExtensionsDir: extRoot})
	h.SetDevRepoCleanup(extH.RemoveDevRepo)

	seedProjectAt(t, s, "dev-proj", repo)
	seedProjectAt(t, s, "installed-proj", installed)
	seedProjectAt(t, s, "ordinary-proj", home)

	for _, id := range []string{"dev-proj", "installed-proj", "ordinary-proj"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/projects/"+id, nil)
		req.SetPathValue("projectID", id)
		rr := httptest.NewRecorder()
		h.HandleByID(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("delete %s = %d: %s", id, rr.Code, rr.Body.String())
		}
	}

	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Error("the development repository outlived its project, so its name stays claimed")
	}
	for _, dir := range []string{installed, home} {
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
			t.Errorf("deleting the project deleted files that were never the server's: %s", dir)
		}
	}
	// The namespace root and the extensions root survive their last repository.
	if _, err := os.Stat(extRoot); err != nil {
		t.Errorf("the extensions root went with one project: %v", err)
	}
}
