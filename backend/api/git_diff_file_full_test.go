package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/git"
	"kanban/backend/types"
)

// GET /projects/{id}/tasks/{id}/diff/file?full=1 re-reads the file from the task's worktree. The
// path came from git status and the size is whatever the task wrote, so the read needs the same
// jail and cap as the listing.

// taskDiffFileFixture builds a project, a task, a real worktree for it, and a diffCache entry
// whose one untracked entry is truncated — the state the Changes panel is in when it asks for
// the full file.
func taskDiffFileFixture(t *testing.T, projID, taskID string) (*GitHandler, *http.ServeMux, string) {
	t.Helper()
	h, s, _, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: projID, Name: projID, Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: taskID, ProjectID: projID, Title: "full re-read", Status: types.StatusInProgress, Branch: "main"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(worktreesDir, projID, taskID)
	createWorktreeAt(t, projPath, wtPath, "main")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", h.HandleTaskDiffFile)
	return h, mux, wtPath
}

func cacheOneUntracked(h *GitHandler, taskID, path string) {
	h.diffCache.Store(taskID, &git.TaskDiff{
		Tracked:   map[string]*git.TrackedDiffFile{},
		Untracked: []git.UntrackedFile{{Path: path, Lines: []string{"cached line"}, Truncated: true}},
	})
}

// The link is what the listing already refuses; the re-read is a second read of the same
// path and must not be a second, wider door.
func TestHandleTaskDiffFileFullRefusesASymlinkOutOfTheWorktree(t *testing.T) {
	const secret = "PRIVATE-KEY-BYTES-MUST-NOT-APPEAR"
	secretFile := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(secretFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	h, mux, wtPath := taskDiffFileFixture(t, "difffull-proj", "difffull-link-task")
	if err := os.Symlink(secretFile, filepath.Join(wtPath, "key")); err != nil {
		t.Fatal(err)
	}
	cacheOneUntracked(h, "difffull-link-task", "key")

	rr := doRequest(t, mux, http.MethodGet, "/api/projects/difffull-proj/tasks/difffull-link-task/diff/file?path=key&full=1")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Errorf("the link target's bytes reached the response: %s", rr.Body.String())
	}
}

// A file inside the worktree is still refused by size: past the cap the answer is the cached
// lines with truncated=true.
func TestHandleTaskDiffFileFullDoesNotReadAnOversizedFile(t *testing.T) {
	h, mux, wtPath := taskDiffFileFixture(t, "difffull-cap-proj", "difffull-cap-task")
	huge := strings.Repeat("y", 2<<20) // 2 MiB: over maxDiffFileBytes by definition
	if err := os.WriteFile(filepath.Join(wtPath, "huge.txt"), []byte(huge), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheOneUntracked(h, "difffull-cap-task", "huge.txt")

	rr := doRequest(t, mux, http.MethodGet, "/api/projects/difffull-cap-proj/tasks/difffull-cap-task/diff/file?path=huge.txt&full=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), huge) {
		t.Fatal("an oversized file was read into the response: the cap is not applied to this read")
	}
	var resp struct {
		Lines     []string `json:"lines"`
		Truncated bool     `json:"truncated"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Truncated {
		t.Error("truncated = false for a file that was NOT handed over in full")
	}
	if len(resp.Lines) != 1 {
		t.Errorf("lines = %d, want the 1 cached line kept as the answer", len(resp.Lines))
	}
}

// The control: an ordinary truncated file still comes back in full, and says so.
func TestHandleTaskDiffFileFullStillReadsAFileWithinTheCap(t *testing.T) {
	h, mux, wtPath := taskDiffFileFixture(t, "difffull-ok-proj", "difffull-ok-task")
	body := strings.Repeat("line\n", 50)
	if err := os.WriteFile(filepath.Join(wtPath, "small.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheOneUntracked(h, "difffull-ok-task", "small.txt")

	rr := doRequest(t, mux, http.MethodGet, "/api/projects/difffull-ok-proj/tasks/difffull-ok-task/diff/file?path=small.txt&full=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Lines     []string `json:"lines"`
		Truncated bool     `json:"truncated"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Lines) != 50 {
		t.Errorf("lines = %d, want all 50 — a cap that also stops ordinary files is a regression", len(resp.Lines))
	}
	if resp.Truncated {
		t.Error("truncated = true for a file that was handed over in full")
	}
}
