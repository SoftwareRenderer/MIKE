package api

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/git"
	"mike/backend/types"
)

// GET /projects/{id}/tasks/{id}/diff/file?full=1 re-reads the file from the task's worktree. The
// path came from git status and the size is whatever the task wrote, so the read needs the same
// jail and cap as the listing.

// taskDiffFileFixture builds a project, a task, a real worktree for it, and an entry in the task diff cache
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
	h.diffCache.put(taskID, &git.TaskDiff{
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
// lines with truncated=true, and the cap that refuses them must not also stop an ordinary file.
func TestHandleTaskDiffFileFullAppliesTheCapToTheReRead(t *testing.T) {
	huge := strings.Repeat("y", 2<<20) // 2 MiB: over maxDiffFileBytes by definition
	body := strings.Repeat("line\n", 50)

	for _, tt := range []struct {
		name  string
		task  string
		write string
		// wantLines is the answer's line count; wantTruncated is what `truncated` must say.
		wantLines     int
		wantTruncated bool
		// leak is the content that must not reach the response when the cap refuses the read.
		leak string
	}{
		{
			name:          "an oversized file is answered by the cached lines, and says so",
			task:          "difffull-cap-task",
			write:         huge,
			wantLines:     1,
			wantTruncated: true,
			leak:          huge,
		},
		{
			name:      "a file within the cap comes back whole",
			task:      "difffull-ok-task",
			write:     body,
			wantLines: 50,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			projID := "difffull-" + strings.TrimPrefix(tt.task, "difffull-")
			h, mux, wtPath := taskDiffFileFixture(t, projID, tt.task)
			if err := os.WriteFile(filepath.Join(wtPath, "asked.txt"), []byte(tt.write), 0o600); err != nil {
				t.Fatal(err)
			}
			cacheOneUntracked(h, tt.task, "asked.txt")

			rr := doRequest(t, mux, http.MethodGet, "/api/projects/"+projID+"/tasks/"+tt.task+"/diff/file?path=asked.txt&full=1")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
			}
			if tt.leak != "" && strings.Contains(rr.Body.String(), tt.leak) {
				t.Fatal("an oversized file was read into the response: the cap is not applied to this read")
			}
			var resp struct {
				Lines     []string `json:"lines"`
				Truncated bool     `json:"truncated"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Lines) != tt.wantLines {
				t.Errorf("lines = %d, want %d", len(resp.Lines), tt.wantLines)
			}
			if resp.Truncated != tt.wantTruncated {
				t.Errorf("truncated = %v, want %v", resp.Truncated, tt.wantTruncated)
			}
		})
	}
}
