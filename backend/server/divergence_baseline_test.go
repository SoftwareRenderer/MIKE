package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"mike/backend/testutil/testgit"
)

// TestTaskDiffFallbackDivergenceBaseline verifies that a task whose branch exists but has
// no recorded base_ref/base_commit (e.g., a parent branch auto-created for subtask
// merging) gets its diff and commits resolved against the commit where the branch
// diverged from the main branch — so changes merged into the branch are visible.
func TestTaskDiffFallbackDivergenceBaseline(t *testing.T) {
	s := testServer(t)
	projID, dir := createTestProjectWithPath(t, s)

	mainBranch := testgit.GitOutput(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if mainBranch == "" || mainBranch == "HEAD" {
		t.Skipf("cannot determine main branch: %q", mainBranch)
	}

	taskID := createTestTask(t, s, projID, "parent task")

	// Simulate EnsureParentBranch: the branch + worktree are created from HEAD, but
	// no base_ref/base_commit is recorded on the task.
	wm := s.executor.WorktreeMgr()
	branchName := "task/" + taskID
	if _, _, err := wm.CreateWorktree(projID, taskID, branchName, ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = branchName
	if task.BaseRef != "" || task.BaseCommit != "" {
		t.Fatalf("test precondition failed: task should have no recorded baseline")
	}
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	// A subtask merged its changes into the parent branch (commit on the branch,
	// clean working tree).
	wtPath, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "subtask-feature.txt"), []byte("from subtask\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	testgit.Git(t, wtPath, "add", "subtask-feature.txt")
	testgit.Git(t, wtPath, "commit", "-m", "Merge subtask changes")

	// Move to review (HandleTaskCommits returns empty for backlog tasks).
	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]string{
		"status": "review",
	})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("move to review: %d %s", rr.Code, rr.Body.String())
	}

	// /diff must list the merged file (committed rows, clean working tree).
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/diff", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr = testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("diff: %d %s", rr.Code, rr.Body.String())
	}
	manifest := mustJSON[map[string]any](t, rr.Body)
	files, _ := manifest["files"].(map[string]any)
	entryRaw, ok := files["subtask-feature.txt"]
	if !ok {
		t.Fatalf("diff manifest should contain subtask-feature.txt, got keys: %v", keysOf(files))
	}
	entry, _ := entryRaw.(map[string]any)
	if added, _ := entry["added"].(float64); added != 1 {
		t.Errorf("expected 1 added line for subtask-feature.txt, got %v", entry["added"])
	}

	// /commits must list only the branch's commits (not the full history) and
	// report the divergence commit as base_commit.
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/commits", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr = testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("commits: %d %s", rr.Code, rr.Body.String())
	}
	commitsResp := mustJSON[map[string]any](t, rr.Body)
	commitsAny, _ := commitsResp["commits"].([]any)
	if len(commitsAny) != 1 {
		t.Fatalf("expected exactly 1 branch commit, got %d: %v", len(commitsAny), commitsResp)
	}
	if subject, _ := commitsAny[0].(map[string]any)["subject"].(string); subject != "Merge subtask changes" {
		t.Errorf("unexpected commit subject: %q", subject)
	}
	if base, _ := commitsResp["base_commit"].(map[string]any); base == nil {
		t.Error("expected base_commit in commits response")
	} else if subject, _ := base["subject"].(string); subject != "Initial" {
		t.Errorf("expected base_commit to be the divergence commit (Initial), got %q", subject)
	}
}

// TestTaskDiffUnusableBaselineFallsBack verifies that a recorded base_ref that
// can't be used as a diff baseline — the task's own branch (structurally empty
// diff) or a custom name that doesn't exist in the repo yet (e.g. typed into
// Edit Task for a separate exploration branch) — falls back to the divergence
// point, so merged changes stay visible.
func TestTaskDiffUnusableBaselineFallsBack(t *testing.T) {
	tests := []struct {
		name    string
		baseRef string
	}{
		{"own-branch", "task/own-branch-base"}, // set equal to the task's own branch below
		{"missing-ref", "explore/idea"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID, dir := createTestProjectWithPath(t, s)

			mainBranch := testgit.GitOutput(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
			if mainBranch == "" || mainBranch == "HEAD" {
				t.Skipf("cannot determine main branch: %q", mainBranch)
			}

			taskID := createTestTask(t, s, projID, "unusable base task")
			wm := s.executor.WorktreeMgr()
			branchName := "task/" + taskID
			if tc.name == "own-branch" {
				branchName = "task/own-branch-base"
			}
			if _, _, err := wm.CreateWorktree(projID, taskID, branchName, ""); err != nil {
				t.Fatalf("create worktree: %v", err)
			}

			// A subtask merged its changes into the branch (clean working tree).
			wtPath, err := wm.WorktreePath(taskID)
			if err != nil {
				t.Fatalf("worktree path: %v", err)
			}
			if err := os.WriteFile(filepath.Join(wtPath, "merged-file.txt"), []byte("merged\n"), 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}
			testgit.Git(t, wtPath, "add", "merged-file.txt")
			testgit.Git(t, wtPath, "commit", "-m", "Merged subtask work")

			task, err := s.store.GetTask(projID, taskID)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			task.Branch = branchName
			task.BaseRef = tc.baseRef // own branch, or a name with no ref in the repo
			if err := s.store.UpdateTask(task); err != nil {
				t.Fatalf("update task: %v", err)
			}

			cookie, csrfToken := loginTestCSRF(t, s)
			req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/diff", nil)
			withAuthCSRF(req, cookie, csrfToken)
			rr := testResponse(t, s, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("diff: %d %s", rr.Code, rr.Body.String())
			}
			manifest := mustJSON[map[string]any](t, rr.Body)
			files, _ := manifest["files"].(map[string]any)
			if _, ok := files["merged-file.txt"]; !ok {
				t.Fatalf("diff manifest should contain merged-file.txt despite unusable base %q, got keys: %v", tc.baseRef, keysOf(files))
			}
		})
	}
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
