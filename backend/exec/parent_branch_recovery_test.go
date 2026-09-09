package exec

import (
	"testing"
	"time"

	"kanban/backend/types"
)

// TestBranchChildFrom_RecreatesMissingParentBranch: a merged and cleaned-up parent still
// names a branch git no longer has, which would fail the child's start with "invalid
// reference", so branchChildFrom recreates it from HEAD.
func TestBranchChildFrom_RecreatesMissingParentBranch(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	now := time.Now().UTC()

	parentID := "parent-branch-gone"
	parentBranch := "task/" + parentID
	if err := te.store.CreateTask(&types.Task{
		ID:        parentID,
		ProjectID: projectID,
		Title:     "Merged and cleaned up",
		Prompt:    "Parent prompt",
		Status:    types.StatusDone,
		Branch:    parentBranch, // recorded on the row, but gone from the repo
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if te.branchExists(projectID, parentBranch) {
		t.Fatalf("fixture expects %s to be missing from git", parentBranch)
	}

	childID := "child-of-cleaned-up-parent"
	child := &types.Task{
		ID:           childID,
		ProjectID:    projectID,
		Title:        "Follow-up",
		Prompt:       "Child prompt",
		Status:       types.StatusBacklog,
		ParentTaskID: parentID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := te.store.CreateTask(child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	source, err := te.branchChildFrom(projectID, child)
	if err != nil {
		t.Fatalf("branchChildFrom: %v", err)
	}
	if source != parentBranch {
		t.Errorf("child branches from %q, want %q", source, parentBranch)
	}
	if !te.branchExists(projectID, parentBranch) {
		t.Fatalf("parent branch %s was not recreated - the child would fail to start", parentBranch)
	}

	// The ref comes back under the same name.
	reloaded, err := te.store.GetTaskByID(parentID)
	if err != nil || reloaded.Branch != parentBranch {
		t.Errorf("parent branch = %q (err %v), want %s", reloaded.Branch, err, parentBranch)
	}

	// And the child's worktree builds from it.
	wtPath, baseRef, err := te.worktreeMgr.CreateWorktree(projectID, childID, "task/"+childID, source)
	if err != nil {
		t.Fatalf("create child worktree from %s: %v", source, err)
	}
	if wtPath == "" {
		t.Fatal("empty child worktree path")
	}
	if baseRef != parentBranch {
		t.Errorf("child baseRef = %q, want %q (its merge target is the parent's branch)", baseRef, parentBranch)
	}
}
