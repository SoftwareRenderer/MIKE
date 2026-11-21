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
