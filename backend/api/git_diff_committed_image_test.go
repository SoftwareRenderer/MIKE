package api

import (
	"bytes"
	"net/http"
	"path/filepath"
	"testing"

	"mike/backend/types"
)

// The manifest is all the panel is told about a binary file, so what git said about it has to reach this body.
func TestTaskDiffManifestNamesTheCommittedSideOfABinaryFile(t *testing.T) {
	h, s, _, worktreesDir := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	if err := s.CreateProject(&types.Project{ID: "imgmanifest-proj", Name: "imgmanifest-proj", Path: projPath}); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: "imgmanifest-task", ProjectID: "imgmanifest-proj", Title: "add a logo", Status: types.StatusInProgress, Branch: "task/imgmanifest-task"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	// Its bytes differ from the logo's, or git reports a rename instead of an added and a removed file.
	bannerBytes := append(append([]byte{}, pngBytes...), bytes.Repeat([]byte{'B'}, 256)...)
	writeWorktreeBytes(t, projPath, "old-banner.png", bannerBytes)
	mustRun(t, projPath, "git", "add", "--", "old-banner.png")
	mustRun(t, projPath, "git", "commit", "-m", "a banner to remove")
	wtPath := filepath.Join(worktreesDir, "imgmanifest-proj", "imgmanifest-task")
	createWorktreeAt(t, projPath, wtPath, "task/imgmanifest-task")

	writeWorktreeBytes(t, wtPath, "docs/logo.png", pngBytes)
	mustRun(t, wtPath, "git", "add", "--", "docs/logo.png")
	mustRun(t, wtPath, "git", "rm", "-q", "--", "old-banner.png")
	mustRun(t, wtPath, "git", "commit", "-m", "add the logo, drop the banner")

	mux := newTestGitMux(h)
	body := decodeJSONBody(t, doRequest(t, mux, http.MethodGet,
		"/api/projects/imgmanifest-proj/tasks/imgmanifest-task/diff").Body.Bytes())
	files, _ := body["files"].(map[string]any)

	logo, _ := files["docs/logo.png"].(map[string]any)
	if logo == nil {
		t.Fatalf("the committed image is not in the manifest at all: %v", body["files"])
	}
	if logo["is_binary"] != true {
		t.Errorf("is_binary = %v, so the panel has no notice and no picture to offer", logo["is_binary"])
	}
	if size, _ := logo["file_size"].(float64); size == 0 {
		t.Errorf("file_size = %v, so the notice has no size to name", logo["file_size"])
	}
	if logo["isNew"] != true {
		t.Errorf("isNew = %v: without it the viewer asks for an old side this branch created, and gets none", logo["isNew"])
	}
	if logo["hasCommitted"] != true {
		t.Errorf("hasCommitted = %v, so the committed section cannot list the picture", logo["hasCommitted"])
	}
	if _, ok := logo["hasWorking"]; ok {
		t.Errorf("hasWorking = %v on a file the tree does not change; that is what files it under Unstaged", logo["hasWorking"])
	}

	banner, _ := files["old-banner.png"].(map[string]any)
	if banner == nil {
		t.Fatalf("the committed removal is not in the manifest: %v", body["files"])
	}
	if banner["isDeleted"] != true {
		t.Errorf("isDeleted = %v, so a picture would be offered for a file that is gone", banner["isDeleted"])
	}

	// The browser reads /diff/file after the manifest, so the same names have to be there too.
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", h.HandleTaskDiffFile)
	one := decodeJSONBody(t, doRequest(t, mux, http.MethodGet,
		"/api/projects/imgmanifest-proj/tasks/imgmanifest-task/diff/file?path=docs/logo.png").Body.Bytes())
	if one["hasCommitted"] != true {
		t.Errorf("hasCommitted = %v from /diff/file, so a refresh that re-reads one file loses its section", one["hasCommitted"])
	}
	if _, ok := one["hasWorking"]; ok {
		t.Errorf("hasWorking = %v from /diff/file on a file the tree does not change", one["hasWorking"])
	}
}

func TestProbeStagedNewImage(t *testing.T) {
	h, s, _, worktreesDir := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	if err := s.CreateProject(&types.Project{ID: "p", Name: "p", Path: projPath}); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: "t", ProjectID: "p", Title: "x", Status: types.StatusInProgress, Branch: "task/t"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(worktreesDir, "p", "t")
	createWorktreeAt(t, projPath, wtPath, "task/t")
	writeWorktreeBytes(t, wtPath, "docs/logo.png", pngBytes)
	mustRun(t, wtPath, "git", "add", "--", "docs/logo.png")

	mux := newTestGitMux(h)
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", h.HandleTaskDiffFile)
	body := decodeJSONBody(t, doRequest(t, mux, http.MethodGet, "/api/projects/p/tasks/t/diff").Body.Bytes())
	t.Logf("MANIFEST = %s", body["files"])
	one := decodeJSONBody(t, doRequest(t, mux, http.MethodGet, "/api/projects/p/tasks/t/diff/file?path=docs/logo.png").Body.Bytes())
	t.Logf("FILE = %s", one)
	t.Logf("RAW = %s", doRequest(t, mux, http.MethodGet, "/api/projects/p/tasks/t/diff/file?path=docs/logo.png").Body.String())
	if cached, ok := h.diffCache.get("t"); ok {
		cf := cached.Tracked["docs/logo.png"]
		t.Logf("CACHED Rows nil? %v len=%d IsBinary=%v IsNew=%v", cf.Rows == nil, len(cf.Rows), cf.IsBinary, cf.IsNew)
	}
}

// A picture the person staged and never committed has no rows either, and the panel renders its size and
// its "View image" button from these names alone. The per-file answer carries an empty rows array —
// encoding/json/v2 gives a nil slice as [] — so the names are the only thing distinguishing the picture
// from a mode-only change, which the panel is right to call an empty file.
func TestTaskDiffNamesAStagedImageThatHasNoRows(t *testing.T) {
	h, s, _, worktreesDir := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	if err := s.CreateProject(&types.Project{ID: "stagedimg-proj", Name: "stagedimg-proj", Path: projPath}); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: "stagedimg-task", ProjectID: "stagedimg-proj", Title: "add a logo", Status: types.StatusInProgress, Branch: "task/stagedimg-task"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(worktreesDir, "stagedimg-proj", "stagedimg-task")
	createWorktreeAt(t, projPath, wtPath, "task/stagedimg-task")
	writeWorktreeBytes(t, wtPath, "docs/logo.png", pngBytes)
	mustRun(t, wtPath, "git", "add", "--", "docs/logo.png")

	mux := newTestGitMux(h)
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", h.HandleTaskDiffFile)
	body := decodeJSONBody(t, doRequest(t, mux, http.MethodGet,
		"/api/projects/stagedimg-proj/tasks/stagedimg-task/diff").Body.Bytes())
	logo, _ := (body["files"].(map[string]any))["docs/logo.png"].(map[string]any)
	if logo == nil {
		t.Fatalf("the staged picture is not in the manifest: %v", body["files"])
	}
	for _, name := range []string{"is_binary", "file_size", "isNew"} {
		if logo[name] == nil || logo[name] == false {
			t.Errorf("%s = %v on a staged picture, so the panel has nothing to render it from", name, logo[name])
		}
	}
	if logo["staged"] != true {
		t.Errorf("staged = %v, so the picture is filed under Unstaged", logo["staged"])
	}
	if logo["hasWorking"] != true {
		t.Errorf("hasWorking = %v, so no section lists the picture at all", logo["hasWorking"])
	}

	one := decodeJSONBody(t, doRequest(t, mux, http.MethodGet,
		"/api/projects/stagedimg-proj/tasks/stagedimg-task/diff/file?path=docs/logo.png").Body.Bytes())
	if one["isBinary"] != true {
		t.Errorf("isBinary = %v from /diff/file, so the row falls through to \"Empty file\"", one["isBinary"])
	}
	if size, _ := one["fileSize"].(float64); size == 0 {
		t.Errorf("fileSize = %v from /diff/file, so the notice has no size to name", one["fileSize"])
	}
}
