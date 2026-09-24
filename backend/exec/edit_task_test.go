package exec

import (
	"testing"

	"mike/backend/store"
	"mike/backend/types"
)

// editTaskTarget creates a backlog task in the given project with explicit
// auto-merge fields, returning it for use as the current task if needed.
func editTaskTarget(t *testing.T, s *store.Store, projectID, taskID string, autoCommit, autoMerge, subtasksAutoMerge bool, parentTaskID string) *types.Task {
	t.Helper()
	task := createTaskInProject(t, s, projectID, taskID)
	task.AutoCommit = autoCommit
	task.AutoMerge = autoMerge
	task.SubtasksAutoMerge = subtasksAutoMerge
	task.ParentTaskID = parentTaskID
	if err := s.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}
	return task
}

// TestHandleEditTask_NestingRespectsParentSubtasksAutoMerge_False verifies that
// nesting a task under a parent whose "allow subtask to auto merge" preference
// is off disables the task's auto_merge (it previously was true from config).
func TestHandleEditTask_NestingRespectsParentSubtasksAutoMerge_False(t *testing.T) {
	t.Parallel()
	// Config defaults: auto_merge=true — but the parent explicitly disallows
	// subtask auto-merge.
	te := newTestExecutorWithConfig(t, true, true)
	parent := createParentTask(t, te.store, "parent-nest-1", "proj-nest-1", "backlog")
	parent.SubtasksAutoMerge = false
	_ = te.store.UpdateTask(parent)

	editTaskTarget(t, te.store, "proj-nest-1", "target-nest-1", true, true, true, "")

	_, err := te.tools.handleEditTask(map[string]any{
		"task_id":        "target-nest-1",
		"parent_task_id": "parent-nest-1",
	}, parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := te.store.GetTaskByID("target-nest-1")
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if updated.ParentTaskID != "parent-nest-1" {
		t.Errorf("expected ParentTaskID=parent-nest-1, got %q", updated.ParentTaskID)
	}
	// auto_merge must follow the parent's "allow subtask to auto merge" preference
	if updated.AutoMerge {
		t.Errorf("expected AutoMerge=false (parent SubtasksAutoMerge=false), got true")
	}
	// the task's own subtasks_auto_merge (governing its children) is preserved
	if !updated.SubtasksAutoMerge {
		t.Errorf("expected SubtasksAutoMerge=true (preserved), got false")
	}
}

// TestHandleEditTask_NestingInheritsSubtasksAutoMergeTrue verifies that nesting
// under a parent that allows subtask auto-merge enables the task's auto_merge
// (with the auto_commit prerequisite).
func TestHandleEditTask_NestingInheritsSubtasksAutoMergeTrue(t *testing.T) {
	t.Parallel()
	// Config defaults: auto_merge=false — but the parent allows subtask auto-merge.
	te := newTestExecutorWithConfig(t, false, false)
	parent := createParentTask(t, te.store, "parent-nest-2", "proj-nest-2", "backlog")
	parent.SubtasksAutoMerge = true
	_ = te.store.UpdateTask(parent)

	editTaskTarget(t, te.store, "proj-nest-2", "target-nest-2", false, false, false, "")

	_, err := te.tools.handleEditTask(map[string]any{
		"task_id":        "target-nest-2",
		"parent_task_id": "parent-nest-2",
	}, parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := te.store.GetTaskByID("target-nest-2")
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if !updated.AutoMerge {
		t.Errorf("expected AutoMerge=true (parent SubtasksAutoMerge=true), got false")
	}
	if !updated.AutoCommit {
		t.Errorf("expected AutoCommit=true (prerequisite for AutoMerge), got false")
	}
}

// TestHandleEditTask_UnnestResetsToConfigDefaults verifies that removing the
// parent resets auto_merge/auto_commit to the server config defaults while
// preserving the task's own subtasks_auto_merge.
func TestHandleEditTask_UnnestResetsToConfigDefaults(t *testing.T) {
	t.Parallel()
	// Config defaults: auto_merge=true, subtasks_auto_merge=false.
	te := newTestExecutorWithConfig(t, true, false)
	parent := createParentTask(t, te.store, "parent-unnest-3", "proj-unnest-3", "backlog")
	parent.SubtasksAutoMerge = false
	_ = te.store.UpdateTask(parent)

	// Subtask with auto_merge=false (inherited from parent's disallow) and its
	// own subtasks_auto_merge=true.
	editTaskTarget(t, te.store, "proj-unnest-3", "target-unnest-3", false, false, true, "parent-unnest-3")

	_, err := te.tools.handleEditTask(map[string]any{
		"task_id":        "target-unnest-3",
		"parent_task_id": "",
	}, parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := te.store.GetTaskByID("target-unnest-3")
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if updated.ParentTaskID != "" {
		t.Errorf("expected ParentTaskID empty, got %q", updated.ParentTaskID)
	}
	if !updated.AutoMerge {
		t.Errorf("expected AutoMerge=true (config default after un-nesting), got false")
	}
	if !updated.AutoCommit {
		t.Errorf("expected AutoCommit=true (prerequisite for AutoMerge), got false")
	}
	if !updated.SubtasksAutoMerge {
		t.Errorf("expected SubtasksAutoMerge=true (preserved), got false")
	}
}

// TestHandleEditTask_UnchangedParentDoesNotClobberAutoMerge verifies that
// re-sending the current parent (a no-op reparent) leaves an explicit
// auto_merge override untouched.
func TestHandleEditTask_UnchangedParentDoesNotClobberAutoMerge(t *testing.T) {
	t.Parallel()
	te := newTestExecutorWithConfig(t, true, true)
	parent := createParentTask(t, te.store, "parent-same-4", "proj-same-4", "backlog")
	parent.SubtasksAutoMerge = false
	_ = te.store.UpdateTask(parent)

	// Subtask whose auto_merge was explicitly overridden to true despite the
	// parent disallowing subtask auto-merge.
	editTaskTarget(t, te.store, "proj-same-4", "target-same-4", true, true, true, "parent-same-4")

	_, err := te.tools.handleEditTask(map[string]any{
		"task_id":        "target-same-4",
		"parent_task_id": "parent-same-4", // same parent — no-op reparent
	}, parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := te.store.GetTaskByID("target-same-4")
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if !updated.AutoMerge {
		t.Errorf("expected AutoMerge=true (unchanged parent must not clobber override), got false")
	}
}

// TestHandleEditTask_UnnestWhenAlreadyIndependent verifies that clearing the
// parent on an already-independent task leaves auto_merge untouched (no
// unnecessary reset to config defaults).
func TestHandleEditTask_UnnestWhenAlreadyIndependent(t *testing.T) {
	t.Parallel()
	// Config defaults: auto_merge=false.
	te := newTestExecutorWithConfig(t, false, false)
	createProject(t, te.store, "proj-ind-5", "proj-ind-5")
	editTaskTarget(t, te.store, "proj-ind-5", "target-ind-5", true, true, true, "")

	current, err := te.store.GetTaskByID("target-ind-5")
	if err != nil {
		t.Fatalf("get current task: %v", err)
	}
	_, err = te.tools.handleEditTask(map[string]any{
		"task_id":        "target-ind-5",
		"parent_task_id": "",
	}, current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := te.store.GetTaskByID("target-ind-5")
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if !updated.AutoMerge {
		t.Errorf("expected AutoMerge=true (already independent, no reset), got false")
	}
}

// TestHandleEditTask_TitleOnlyEditKeepsAutoMerge verifies that editing only the
// title leaves auto-merge fields untouched.
func TestHandleEditTask_TitleOnlyEditKeepsAutoMerge(t *testing.T) {
	t.Parallel()
	te := newTestExecutorWithConfig(t, false, false)
	createProject(t, te.store, "proj-title-6", "proj-title-6")
	editTaskTarget(t, te.store, "proj-title-6", "target-title-6", true, true, true, "")

	current, err := te.store.GetTaskByID("target-title-6")
	if err != nil {
		t.Fatalf("get current task: %v", err)
	}
	_, err = te.tools.handleEditTask(map[string]any{
		"task_id": "target-title-6",
		"title":   "Renamed",
	}, current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := te.store.GetTaskByID("target-title-6")
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if updated.Title != "Renamed" {
		t.Errorf("expected title Renamed, got %q", updated.Title)
	}
	if !updated.AutoMerge {
		t.Errorf("expected AutoMerge unchanged (true), got false")
	}
}

// TestHandleEditTask_StartedParentAccepted: nesting a backlog task under a started parent
// is accepted, and the child inherits that parent's subtasks_auto_merge.
func TestHandleEditTask_StartedParentAccepted(t *testing.T) {
	t.Parallel()
	te := newTestExecutorWithConfig(t, true, true)
	parent := createParentTask(t, te.store, "parent-nb-7", "proj-nb-7", "in_progress")
	parent.SubtasksAutoMerge = false
	_ = te.store.UpdateTask(parent)

	editTaskTarget(t, te.store, "proj-nb-7", "target-nb-7", true, true, true, "")

	if _, err := te.tools.handleEditTask(map[string]any{
		"task_id":        "target-nb-7",
		"parent_task_id": "parent-nb-7",
	}, parent); err != nil {
		t.Fatalf("a started parent must accept the nesting: %v", err)
	}

	updated, err := te.store.GetTaskByID("target-nb-7")
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if updated.ParentTaskID != "parent-nb-7" {
		t.Errorf("ParentTaskID = %q, want parent-nb-7", updated.ParentTaskID)
	}
	// The parent owns its children: subtasks_auto_merge=false means no auto-merge.
	if updated.AutoMerge {
		t.Errorf("AutoMerge = true, want false (inherited from the new parent)")
	}
}

// TestHandleEditTask_NestingClearsBaseBranch: nesting moves the work to the parent's branch, so the
// task's own base branch must not stay on the row.
func TestHandleEditTask_NestingClearsBaseBranch(t *testing.T) {
	t.Parallel()
	te := newTestExecutorWithConfig(t, false, false)
	parent := createParentTask(t, te.store, "parent-base-1", "proj-base-edit", "backlog")

	target := editTaskTarget(t, te.store, "proj-base-edit", "target-base-1", false, false, false, "")
	target.BaseRef = "test"
	target.BaseCommit = "0123456789abcdef0123456789abcdef01234567"
	if err := te.store.UpdateTask(target); err != nil {
		t.Fatalf("configure base branch: %v", err)
	}

	if _, err := te.tools.handleEditTask(map[string]any{
		"task_id":        "target-base-1",
		"parent_task_id": "parent-base-1",
	}, parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := te.store.GetTaskByID("target-base-1")
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if updated.BaseRef != "" {
		t.Errorf("base_ref = %q on a nested task, want empty", updated.BaseRef)
	}
	if updated.BaseCommit != "" {
		t.Errorf("base_commit = %q on a nested task, want empty: it pins the base branch", updated.BaseCommit)
	}
}

// TestHandleEditTask_TitleOnlyEditKeepsBaseBranch: only the parent link drops the base branch.
func TestHandleEditTask_TitleOnlyEditKeepsBaseBranch(t *testing.T) {
	t.Parallel()
	te := newTestExecutorWithConfig(t, false, false)
	current := createParentTask(t, te.store, "current-base-2", "proj-base-edit-2", "backlog")

	target := editTaskTarget(t, te.store, "proj-base-edit-2", "target-base-2", false, false, false, "")
	target.BaseRef = "test"
	if err := te.store.UpdateTask(target); err != nil {
		t.Fatalf("configure base branch: %v", err)
	}

	if _, err := te.tools.handleEditTask(map[string]any{
		"task_id": "target-base-2",
		"title":   "Renamed",
	}, current); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := te.store.GetTaskByID("target-base-2")
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if updated.BaseRef != "test" {
		t.Errorf("base_ref = %q after a title-only edit, want %q", updated.BaseRef, "test")
	}
}
