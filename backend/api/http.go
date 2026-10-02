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

// resolveProject returns the project the {projectID} path segment names.
func resolveProject(w http.ResponseWriter, r *http.Request, store httputil.ProjectGetter) (*types.Project, bool) {
	return httputil.ResolveProjectID(w, store, r.PathValue("projectID"))
}

// resolveTask returns the task the path names, or answers 404 (500, logged, for any other
// store failure) and returns false.
func resolveTask(w http.ResponseWriter, r *http.Request, store taskGetter) (*types.Task, bool) {
	task, err := store.GetTask(r.PathValue("projectID"), r.PathValue("taskID"))
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return nil, false
	}
	return task, true
}

// requireWorktreeMgr answers 500 when this server has no worktree manager at all — a wiring
// fault, not a user error. Handlers that fetch the worktree path right away use
// resolveWorktree, which checks it for them.
func requireWorktreeMgr(w http.ResponseWriter, mgr *git.WorktreeManager) bool {
	if mgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return false
	}
	return true
}

// resolveWorktree returns the worktree directory of the task the path names. It answers the
// two ways a task can have no directory to work in: no worktree manager at all, or no
// worktree for this task.
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

// resolveTaskWithWorktree resolves the task the path names and checks a worktree manager exists before
// any worktree question is asked of it. A handler that only needs the directory uses resolveWorktree.
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
