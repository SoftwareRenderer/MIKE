package server

// Deleting a task releases the snapshots its rows pinned, or every task ever run keeps a full copy of its
// files alive in the user's repository.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/git"
)

func pinFixtureRepo(t *testing.T, s *Server, name string) (projID, repoPath, head string) {
	t.Helper()
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	initGitRepo(t, tmpDir)
	projID = mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST", "/api/projects",
		map[string]any{"name": name, "path": tmpDir})).Body)["id"].(string)
	return projID, tmpDir, strings.TrimSpace(mustRunGitOutput(t, tmpDir, "rev-parse", "HEAD"))
}

// refExists reports any ref, including ones MIKE does not own — a user branch has to survive cleanup, and
// a snapshot-only helper could not say so.
func refExists(t *testing.T, repoPath, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	cmd.Dir = repoPath
	return cmd.Run() == nil
}

func TestDeleteTaskUnpinsOnlyItsSnapshots(t *testing.T) {
	s := testServer(t)
	projID, repoPath, head := pinFixtureRepo(t, s, "snapshot-lifecycle")
	wm := s.executor.WorktreeMgr()

	taskA := createTestTask(t, s, projID, "task a")
	taskB := createTestTask(t, s, projID, "task b")

	// Two kinds of pins on task A (a checkpoint and a thread handoff), one on task B, plus a ref of the
	// user's own that must survive.
	pinned := []string{
		git.SnapshotRef(taskA, "asst-1"),
		git.SnapshotRef(taskA, git.ThreadSnapshotOwner("thread-a1")),
		git.SnapshotRef(taskB, "asst-9"),
	}
	for _, ref := range pinned {
		if err := wm.PinSnapshot(repoPath, ref, head); err != nil {
			t.Fatalf("pin %s: %v", ref, err)
		}
	}

	del := testResponseAuth(t, s, testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskA, nil))
	if del.Code != 204 {
		t.Fatalf("delete task: expected 204, got %d: %s", del.Code, del.Body.String())
	}

	for _, ref := range pinned[:2] {
		if refExists(t, repoPath, ref) {
			t.Errorf("%s is still pinned after its task was deleted — its objects can never be reclaimed", ref)
		}
	}
	if !refExists(t, repoPath, pinned[2]) {
		t.Error("deleting one task must not unpin another task's snapshots")
	}
	if !refExists(t, repoPath, "refs/heads/master") {
		t.Error("the user's own branch ref must survive snapshot cleanup")
	}
}
