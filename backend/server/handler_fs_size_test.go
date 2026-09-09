package server

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"kanban/backend/types"
)

// The file these tests ask for, and what a refused read may allocate. Both the fixed
// and the unfixed handler answer 413, so the allocation is what tells them apart: a
// refusal costs kilobytes, a read costs the file.
const (
	oversizedMiB = 32 << 20
	allocBudget  = 4 << 20
)

// sparseFile writes a file of size bytes without writing size bytes, so the cost being
// measured is the server's, not the disk's.
func sparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// fsSizeProject registers a git project whose checkout holds `name` at `size` bytes,
// committed, and returns the project id and the file path.
func fsSizeProject(t *testing.T, s *Server, projectID, name string, size int64) (string, string) {
	t.Helper()
	repo := t.TempDir()
	if err := s.store.CreateProject(&types.Project{
		ID: projectID, Name: projectID, Path: repo,
	}); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, repo, "init")
	mustRunGit(t, repo, "config", "user.email", "test@test.com")
	mustRunGit(t, repo, "config", "user.name", "Test")
	file := filepath.Join(repo, name)
	sparseFile(t, file, size)
	mustRunGit(t, repo, "add", name)
	mustRunGit(t, repo, "commit", "-m", "big")
	return projectID, file
}

// fsReadStatus issues GET /api/fs/read and reports the status and what the request
// allocated.
func fsReadStatus(t *testing.T, s *Server, cookie, csrf, projectID, file, ref string) int {
	t.Helper()
	q := url.Values{"path": {file}, "project": {projectID}}
	if ref != "" {
		q.Set("ref", ref)
	}
	req := testRequest(http.MethodGet, "/api/fs/read?"+q.Encode(), nil)
	withAuthCSRF(req, cookie, csrf)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rr := testResponse(t, s, req)
	runtime.ReadMemStats(&after)

	if grew := after.TotalAlloc - before.TotalAlloc; grew > allocBudget {
		t.Errorf("the request allocated %d bytes (budget %d): the size cap ran AFTER reading %s, not before",
			grew, allocBudget, ref)
	}
	return rr.Code
}

// TestFSReadRefusesOversizedWorkingTreeFile: the cap used to be applied to the string
// that had already been read, so one request allocated the whole file before refusing
// it.
func TestFSReadRefusesOversizedWorkingTreeFile(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	projectID, file := fsSizeProject(t, s, "fs-size-worktree", "big.bin", oversizedMiB)

	if code := fsReadStatus(t, s, cookie, csrf, projectID, file, ""); code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", code, http.StatusRequestEntityTooLarge)
	}

	// A normal file must keep reading, or this is a broken endpoint rather than a
	// capped one.
	small := filepath.Join(filepath.Dir(file), "small.txt")
	if err := os.WriteFile(small, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"path": {small}, "project": {projectID}}
	req := testRequest(http.MethodGet, "/api/fs/read?"+q.Encode(), nil)
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code != http.StatusOK {
		t.Errorf("a normal file was refused: %d %s", rr.Code, rr.Body.String())
	}
}

// TestFSReadRefusesOversizedBlobAtRef: for `?ref=` the bytes come out of `git show`, so
// capping the read first means asking git how big the object is before asking for it.
func TestFSReadRefusesOversizedBlobAtRef(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	projectID, file := fsSizeProject(t, s, "fs-size-ref", "big.bin", oversizedMiB)

	// Gone from the working tree, still in history — this read can only be served
	// from the object database.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}

	if code := fsReadStatus(t, s, cookie, csrf, projectID, file, "HEAD"); code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", code, http.StatusRequestEntityTooLarge)
	}
}

// TestGitBlobSize pins the probe: the size git reports for a blob, and that a spec git
// cannot answer reports "unknown" rather than a size — a wrong "0, ok" would leave the
// read uncapped.
func TestGitBlobSize(t *testing.T) {
	s := testServer(t)
	projectID, _ := fsSizeProject(t, s, "fs-size-probe", "sized.txt", 1234)
	wm := s.executor.WorktreeMgr()
	runGit := func(args ...string) (string, error) {
		return wm.GitRunInDirOutput(projectDirOf(t, s, projectID), args...)
	}

	if size, ok := gitBlobSize(runGit, "HEAD:sized.txt"); !ok || size != 1234 {
		t.Errorf("gitBlobSize(HEAD:sized.txt) = %d, %v; want 1234, true", size, ok)
	}
	if _, ok := gitBlobSize(runGit, "HEAD:not-there.txt"); ok {
		t.Error("an unknown path reported a size")
	}
	// Unresolvable is "unknown", not 0: the caller falls through to the read.
	if _, ok := gitBlobSize(runGit, "no-such-revision:sized.txt"); ok {
		t.Error("an unresolvable revision reported a size")
	}
}

// projectDirOf reads back the checkout a project row was created with.
func projectDirOf(t *testing.T, s *Server, projectID string) string {
	t.Helper()
	p, err := s.store.GetProject(projectID)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(p.Path)
}

// TestTooLargeMessagesNameTheirCaps is the drift gate: every cap that answers 413 has
// a sentence saying how big it is, and a raised cap left behind its sentence would
// refuse a file for a size the user was told they could send. The rule is the one the
// strings were written with — the printed figure is the cap's decimal megabytes.
func TestTooLargeMessagesNameTheirCaps(t *testing.T) {
	cases := []struct {
		name    string
		message string
		bytes   int64
	}{
		{"fs read", fsTooLargeMessage, maxFSReadBytes},
		{"fs write", fsTooLargeMessage, maxFSWriteBytes},
		{"upload", uploadTooLargeMessage, MaxUploadSize},
	}
	for _, tc := range cases {
		var stated int
		if _, err := fmt.Sscanf(tc.message, "file too large (max %dMB)", &stated); err != nil {
			t.Errorf("%s: %q does not read as \"file too large (max NMB)\": %v", tc.name, tc.message, err)
			continue
		}
		if want := int(tc.bytes / 1_000_000); stated != want {
			t.Errorf("%s cap is %d bytes (%dMB) but the message says %dMB", tc.name, tc.bytes, want, stated)
		}
	}
}
