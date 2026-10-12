package api

// Every task-scoped handler opens the same way: the {projectID}/{taskID} pair in the
// path becomes a task, then the worktree directory that task owns. Each step answers its
// own failure so the handler reads as a linear happy path.

import (
	"net/http"

	"mike/backend/httputil"
	"mike/backend/types"

	"mike/backend/git"
)

// taskGetter is the store call the shared preambles need.
type taskGetter interface {
	GetTask(projectID, taskID string) (*types.Task, error)
}

func resolveProject(w http.ResponseWriter, r *http.Request, store httputil.ProjectGetter) (*types.Project, bool) {
	return httputil.ResolveProjectID(w, store, r.PathValue("projectID"))
}

// resolveTask returns the task for the path, or answers 404/500.
func resolveTask(w http.ResponseWriter, r *http.Request, store taskGetter) (*types.Task, bool) {
	task, err := store.GetTask(r.PathValue("projectID"), r.PathValue("taskID"))
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return nil, false
	}
	return task, true
}

// requireWorktreeMgr returns false and answers 500 if worktree manager is missing.
func requireWorktreeMgr(w http.ResponseWriter, mgr *git.WorktreeManager) bool {
	if mgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return false
	}
	return true
}

func resolveWorktree(w http.ResponseWriter, r *http.Request, mgr *git.WorktreeManager) (string, bool) {
	if !requireWorktreeMgr(w, mgr) {
		return "", false
	}
	path, err := mgr.WorktreePath(r.PathValue("taskID"))
	if err != nil {
		httputil.Fail(w, err, "resolve worktree", "task worktree not found")
		return "", false
	}
	return path, true
}

// resolveTaskWithWorktree resolves the task and ensures worktree manager exists.
func resolveTaskWithWorktree(w http.ResponseWriter, r *http.Request, store taskGetter, mgr *git.WorktreeManager) (*types.Task, bool) {
	task, ok := resolveTask(w, r, store)
	if !ok {
		return nil, false
	}
	if !requireWorktreeMgr(w, mgr) {
		return nil, false
	}
	return task, true
}
