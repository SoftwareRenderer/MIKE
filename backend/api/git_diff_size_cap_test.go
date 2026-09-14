package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"kanban/backend/types"
)

// HandleProjectDiffFile reads a file the request names inside the project. The cap is the one
// GET /api/fs/read applies, and the rule is the same: check the stat, then read.

func TestHandleProjectDiffFileRefusesToReadAnOversizedWorkingTreeFile(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	huge := strings.Repeat("y", 2<<20) // 2 MiB: over the cap by definition
	createFile(t, projPath, "huge.txt", huge)
	proj := &types.Project{ID: "diff-cap-proj", Name: "diff-cap-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)
	rr := doRequest(t, mux, http.MethodGet, "/api/projects/diff-cap-proj/git/diff/file?path=huge.txt")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Lines     []string `json:"lines"`
		LineCount int      `json:"line_count"`
		FileSize  int64    `json:"file_size"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Lines) != 0 {
		t.Errorf("an oversized file was read into the response anyway: %d lines", len(resp.Lines))
	}
	// The answer is the size, which is what the UI renders as a "large file" notice.
	if resp.FileSize != int64(len(huge)) {
		t.Errorf("file_size = %d, want %d", resp.FileSize, len(huge))
	}
}

func TestHandleProjectDiffFileStillReadsAFileWithinTheCap(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	body := strings.Repeat("line\n", 1000)
	createFile(t, projPath, "normal.txt", body)
	proj := &types.Project{ID: "diff-cap-ok-proj", Name: "diff-cap-ok-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)
	rr := doRequest(t, mux, http.MethodGet, "/api/projects/diff-cap-ok-proj/git/diff/file?path=normal.txt")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Lines    []string `json:"lines"`
		FileSize int64    `json:"file_size"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Lines) != 1000 {
		t.Errorf("lines = %d, want 1000 — a cap that also stops ordinary files is a regression", len(resp.Lines))
	}
	if resp.FileSize != 0 {
		t.Errorf("a file inside the cap must not be reported as large: %d", resp.FileSize)
	}
}
