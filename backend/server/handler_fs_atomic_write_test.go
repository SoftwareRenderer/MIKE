package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mike/backend/fsutil"
	"mike/backend/types"
)

// TestFSWriteAtomic: a save renames a temporary file into place, so a caller never
// sees a half-written file — and never sees the temporary either.
func TestFSWriteAtomic(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	tmpDir := t.TempDir()
	const projID = "atomic-write-project"
	if err := s.store.CreateProject(&types.Project{ID: projID, Name: "Atomic", Path: tmpDir}); err != nil {
		t.Fatal(err)
	}

	// A leftover temporary would also be a watcher bug: its name is what the
	// watchers filter on.
	assertNoTempFiles := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if fsutil.IsAtomicTemp(e.Name()) {
				t.Errorf("temporary file left in %s: %s", dir, e.Name())
			}
		}
	}

	t.Run("new file is 0644 and leaves no temporary behind", func(t *testing.T) {
		path := filepath.Join(tmpDir, "fresh.txt")
		rr := saveFile(t, s, projID, path, "first")
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "first" {
			t.Errorf("content = %q", data)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0644 {
			t.Errorf("new file mode = %v, want -rw-r--r--", fi.Mode().Perm())
		}
		assertNoTempFiles(tmpDir)
	})

	t.Run("an executable keeps its +x across a save", func(t *testing.T) {
		path := filepath.Join(tmpDir, "deploy.sh")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
		rr := saveFile(t, s, projID, path, "#!/bin/sh\necho new\n")
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0755 {
			t.Errorf("mode after save = %v, want -rwxr-xr-x", fi.Mode().Perm())
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "#!/bin/sh\necho new\n" {
			t.Errorf("content = %q, err = %v", data, err)
		}
		assertNoTempFiles(tmpDir)
	})

	t.Run("parent directories are still created", func(t *testing.T) {
		path := filepath.Join(tmpDir, "deep", "nested", "file.txt")
		rr := saveFile(t, s, projID, path, "nested")
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a failed write answers 500 and leaves the target untouched", func(t *testing.T) {
		// A directory at the target fails the rename; what was there must survive.
		path := filepath.Join(tmpDir, "occupied")
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		inside := filepath.Join(path, "keep.txt")
		if err := os.WriteFile(inside, []byte("untouched"), 0644); err != nil {
			t.Fatal(err)
		}

		rr := saveFile(t, s, projID, path, "must not land")
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
		}
		data, err := os.ReadFile(inside)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "untouched" {
			t.Errorf("contents behind the target changed: %q", data)
		}
		assertNoTempFiles(tmpDir)
	})

	t.Run("a path outside the project is still refused", func(t *testing.T) {
		rr := saveFile(t, s, projID, filepath.Join(t.TempDir(), "elsewhere.txt"), "nope")
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

// saveFile posts a file save the way the editor does.
func saveFile(t *testing.T, s *Server, projectID, path, content string) *httptest.ResponseRecorder {
	t.Helper()
	req := testRequest(http.MethodPost, "/api/fs/write?project="+projectID, map[string]string{
		"path":    path,
		"content": content,
	})
	return testResponseAuth(t, s, req)
}
