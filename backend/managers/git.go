package managers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"
)

// GitHandler handles git operations.
type GitHandler struct {
	store       *store.Store
	worktreeMgr *git.WorktreeManager
}

// NewGitHandler creates a new GitHandler.
func NewGitHandler(store *store.Store, worktreeMgr *git.WorktreeManager) *GitHandler {
	return &GitHandler{store: store, worktreeMgr: worktreeMgr}
}

// HandleLog returns commit log for a project.
func (h *GitHandler) HandleLog(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "HEAD"
	}
	c := exec.Command("git", "-C", project.Path, "log", "--oneline", "--decorate", branch)
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []types.GitCommit
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		commit := types.GitCommit{Short: parts[0], Subject: parts[1]}
		commits = append(commits, commit)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(commits)
}

// HandleBranches lists branches for a project.
func (h *GitHandler) HandleBranches(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "branch", "-a", "--format=%(refname:short)")
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git branches failed: %v", err))
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var branches []string
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			branches = append(branches, line)
		}
	}
	httputil.JSONResponse(w, http.StatusOK, branches)
}

// HandleCurrentBranch returns the current branch name for a project.
func (h *GitHandler) HandleCurrentBranch(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := c.CombinedOutput()
	branch := strings.TrimSpace(string(out))
	if err != nil {
		// Not a git repo or other error
		httputil.JSONResponse(w, http.StatusOK, map[string]string{"branch": ""})
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"branch": branch})
}

// HandleDiff returns diff between refs.
func (h *GitHandler) HandleDiff(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "HEAD"
	}
	c := exec.Command("git", "-C", project.Path, "diff", ref)
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(out)
}

// HandleStatus returns working tree status.
func (h *GitHandler) HandleStatus(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "status", "--porcelain")
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}
	clean := len(strings.TrimSpace(string(out))) == 0
	httputil.JSONResponse(w, http.StatusOK, map[string]interface{}{"clean": clean, "status": string(out)})
}

// HandleCheckout checks out a branch.
func (h *GitHandler) HandleCheckout(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	var req types.GitCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	c := exec.Command("git", "-C", project.Path, "checkout", req.Branch)
	if out, err := c.CombinedOutput(); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git checkout failed: %v: %s", err, out))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"branch": req.Branch})
}

// HandlePull pulls from remote.
func (h *GitHandler) HandlePull(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "pull")
	if out, err := c.CombinedOutput(); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git pull failed: %v: %s", err, out))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "pulled"})
}

// HandlePush pushes to remote.
func (h *GitHandler) HandlePush(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "push")
	if out, err := c.CombinedOutput(); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git push failed: %v: %s", err, out))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "pushed"})
}

// HandleTaskDiff returns diff and untracked files for a task's worktree.
func (h *GitHandler) HandleTaskDiff(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if h.worktreeMgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return
	}
	thread, _ := h.store.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	baseRef, _ := h.worktreeMgr.ResolveDiffBaseline(taskID, task.BaseRef, task.Branch, forkBranch)
	taskDiff, err := h.worktreeMgr.GetTaskDiff(taskID, baseRef)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
		return
	}
	if taskDiff.Untracked == nil {
		taskDiff.Untracked = []git.UntrackedFile{}
	}
	httputil.JSONResponse(w, http.StatusOK, taskDiff)
}
