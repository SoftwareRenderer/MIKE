package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
)

// TestFSWriteExclusive holds the difference between creating a file and saving one: the listing's New
// file row writes empty content, so a name the listing had not heard about would be emptied by the
// click. A create is refused with "path already exists"; a request that asks for nothing keeps
// overwriting, which is what the editor's save is.
func TestFSWriteExclusive(t *testing.T) {
	s := testServer(t)
	tmpDir := t.TempDir()
	const projID = "exclusive-write-project"
	if err := s.store.CreateProject(&types.Project{ID: projID, Name: "Exclusive", Path: tmpDir}); err != nil {
		t.Fatal(err)
	}

	t.Run("a taken name is refused and its bytes stay", func(t *testing.T) {
		path := filepath.Join(tmpDir, "notes.md")
		if err := os.WriteFile(path, []byte("written by the agent"), 0o644); err != nil {
			t.Fatal(err)
		}

		rr := createFile(t, s, projID, path)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("create over an existing file: expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "path already exists") {
			t.Errorf("refusal named no reason: %s", rr.Body.String())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "written by the agent" {
			t.Errorf("the collided file lost its bytes: %q", data)
		}
	})

	t.Run("a folder at the name is refused too", func(t *testing.T) {
		path := filepath.Join(tmpDir, "docs")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}

		rr := createFile(t, s, projID, path)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("create over a directory: expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("the directory is no longer a directory: %v", err)
		}
	})

	t.Run("a free name is created with its parents", func(t *testing.T) {
		path := filepath.Join(tmpDir, "sub", "deep", "new.txt")

		rr := createFile(t, s, projID, path)
		if rr.Code != http.StatusOK {
			t.Fatalf("create of a free name: expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != 0 {
			t.Errorf("a new file is not empty: %q", data)
		}
	})

	t.Run("a save overwrites what a create refuses", func(t *testing.T) {
		path := filepath.Join(tmpDir, "edited.txt")
		if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
			t.Fatal(err)
		}

		rr := saveFile(t, s, projID, path, "edited")
		if rr.Code != http.StatusOK {
			t.Fatalf("save over an existing file: expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "edited" {
			t.Errorf("content = %q", data)
		}
	})
}

// createFile posts a new file the way the listing's New file row does: empty content, and a name that
// must be free.
func createFile(t *testing.T, s *Server, projectID, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := testRequest(http.MethodPost, "/api/fs/write?project="+projectID, map[string]any{
		"path":      path,
		"content":   "",
		"exclusive": true,
	})
	return testResponseAuth(t, s, req)
}
