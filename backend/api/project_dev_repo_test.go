package api

// Deleting a development project removes its rows and no file: the directory under the extensions tree is
// the person's git repository, so it outlives the project. A row left behind claims the name and can never
// load.

import (
	"errors"
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

// seedRow writes one extension record: a development row has no manifest, an installed one does.
func seedRow(t *testing.T, es *extensions.ExtensionStore, id, author, name, installSource, projectID string) {
	t.Helper()
	ext := &extensions.Extension{
		ID: id, Name: name, Author: author, Version: "0.1.0",
		Type: "ui", InstallSource: installSource, ProjectID: projectID, Enabled: true,
	}
	ext.Manifest = extensions.ExtensionManifest{DisplayName: name, Version: "0.1.0", Author: author}
	if err := es.Create(ext); err != nil {
		t.Fatalf("create extension %s: %v", id, err)
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

func TestDeletingAProjectTakesItsDevelopmentRowAndNoFile(t *testing.T) {
	h, _, s := testProjectHandlerWithStore(t)
	extRoot := filepath.Join(t.TempDir(), "extensions")
	repo := filepath.Join(extRoot, extensions.DevNamespace, "my-ext")
	installed := filepath.Join(extRoot, "acme", "pack")
	home := filepath.Join(t.TempDir(), "work-in-progress")
	writeFileTree(t, repo)
	writeFileTree(t, installed)
	writeFileTree(t, home)

	extStore := extensions.NewExtensionStore(s, nil)
	if err := extStore.InitSchema(); err != nil {
		t.Fatal(err)
	}
	extH := extensions.NewExtensionHandler(extStore, nil, nil, &extensions.ExtensionConfig{ExtensionsDir: extRoot})
	h.SetDevCleanup(extH.DeleteDevRows)

	seedProjectAt(t, s, "dev-proj", repo)
	seedProjectAt(t, s, "installed-proj", installed)
	seedProjectAt(t, s, "ordinary-proj", home)
	seedRow(t, extStore, "dev-my-ext", "operator", "my-ext", extensions.DevInstallSource, "dev-proj")
	seedRow(t, extStore, "acme-pack", "acme", "pack", "registry", "dev-proj")

	for _, id := range []string{"dev-proj", "installed-proj", "ordinary-proj"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/projects/"+id, nil)
		req.SetPathValue("projectID", id)
		rec := httptest.NewRecorder()
		h.HandleByID(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete %s = %d: %s", id, rec.Code, rec.Body.String())
		}
	}

	if _, err := extStore.GetByID("dev-my-ext"); !errors.Is(err, extensions.ErrExtensionNotFound) {
		t.Errorf("the development row outlived its project (%v), so its name stays claimed", err)
	}
	// A non-development row naming the project is not this project's to take.
	if _, err := extStore.GetByID("acme-pack"); err != nil {
		t.Errorf("an installed row went with a project that only named it: %v", err)
	}
	// Nothing on disk goes with a project.
	for _, dir := range []string{repo, installed, home} {
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
			t.Errorf("deleting the project deleted files that were never the server's: %s (%v)", dir, err)
		}
	}
	if _, err := os.Stat(extRoot); err != nil {
		t.Errorf("the extensions root went with one project: %v", err)
	}
}
